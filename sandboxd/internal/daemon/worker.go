package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// tmpGuestDir is the in-sandbox mount point of a worker's writable directory, which doubles as the script cwd.
//
// Why the generic /tmp: a bind mount target must already exist, and /tmp exists in every rootfs / image;
// picking a made-up path would mean editing every rootfs.
const tmpGuestDir = "/tmp"

const (
	// reapTimeout is the longest wait for reclaiming a discarded worker: once it elapses we stop waiting and
	// never hold up the return path.
	reapTimeout = 5 * time.Second
	// Deletion retries of removeTmpDir (see the comment there).
	tmpRemoveAttempts   = 3
	tmpRemoveRetryDelay = 50 * time.Millisecond
)

// prepareTmpRoot prepares the host root of the worker writable directories.
//
// The whole tree is emptied at startup, so at run time no per-worker directory ever has to be deleted:
//   - when a process is killed its directory may still be mounted into the jail / container, where RemoveAll
//     returns EBUSY;
//   - leftovers from a previous abnormal exit are cleared along the way.
//
// The cost is that directories only accumulate during a run, but the pool capacity is bounded, so the volume is
// controlled.
func prepareTmpRoot(root string) error {
	if root == "" {
		return fmt.Errorf("worker: tmp_dir is not configured")
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("worker: clean tmp root %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return fmt.Errorf("worker: create tmp root %s: %w", root, err)
	}
	return nil
}

// Worker wraps a single worker process and its stdin/stdout pipes.
type Worker struct {
	ID     string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	proc   sandbox.Process
	// alive is maintained by watch (set to false when the process exits) and set by Kill; readers only call
	// Alive(). An atomic is used because "the pool reads on borrow/return" and "the watch goroutine writes" are
	// concurrent.
	alive atomic.Bool
	// exited is closed when the process really exits (Wait returns) and is the only signal that "the child has
	// been reaped".
	exited chan struct{}
	// tmpDir is this worker's exclusive writable directory (mounted into the sandbox as /tmp and serving as the
	// script cwd). It is deleted on destroy; a failed deletion is left to the next startup's prepareTmpRoot.
	tmpDir string
}

// spawnCodehost starts a resident codehost process inside the isolation environment, talking over
// stdin/stdout. It is the script executor shared by all three modes (none / nsjail / docker): sandboxd keeps it
// resident and every execute forks a brand-new interpreter process from it. Under docker codehost comes from
// /usr/local/bin/codehost inside the image (see sandboxd/Dockerfile), so a code change requires rebuilding the
// image.
//
// Differences from the old spawnWorker (L3 hot interpreter):
//   - it starts the codehost binary (a single argv) instead of "<interpreter> <worker script>";
//   - it mounts no workersDir — codehost embeds the wrapper templates, so worker.lua/py/js are gone;
//   - the interpreter entry (command) is sent with each request and forked by codehost at that point.
//
// Path convention is the same as the old worker: a fresh exclusive subdirectory under tmpRoot is mounted at
// tmpGuestDir (writable + cwd). codehost's cwd is /tmp, the forked interpreter inherits it, and a script writing
// a relative path lands in this session's exclusive tmpDir, which is removed together with the Worker.
func spawnCodehost(sb sandbox.Sandbox, binary, tmpRoot string, limits sandbox.Limits) (*Worker, error) {
	if sb == nil {
		return nil, fmt.Errorf("worker: no isolate implementation")
	}

	binary, err := resolveCodehostBinary(binary)
	if err != nil {
		return nil, fmt.Errorf("worker: abs codehost binary: %w", err)
	}

	// ⚠️ tmpRoot must be made absolute: os.MkdirTemp inherits a relative path as given, while docker's -v only
	// accepts an absolute host path (a relative one becomes a named volume). nsjail's chroot also only accepts
	// absolute host paths, so both are converted here.
	absTmpRoot, err := filepath.Abs(tmpRoot)
	if err != nil {
		return nil, fmt.Errorf("worker: abs tmp dir: %w", err)
	}

	// One exclusive writable directory per codehost, so they cannot see each other's temporary files.
	//
	// ⚠️ chmod 0777 cannot be skipped: MkdirTemp creates it as 0700 owned by sandboxd, while the process inside
	// nsjail runs as another uid (--user 99999) — without relaxing it the forked interpreter cannot write a
	// single temporary file (EACCES).
	tmpDir, err := os.MkdirTemp(absTmpRoot, "codehost-")
	if err != nil {
		return nil, fmt.Errorf("worker: create tmp dir under %s: %w", tmpRoot, err)
	}
	if err := os.Chmod(tmpDir, 0o777); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: chmod tmp dir %s: %w", tmpDir, err)
	}

	spec := &sandbox.Spec{
		Argv: []string{binary},
		Dir:  tmpGuestDir, // the cwd must be a writable directory
		// The interpreter's stderr (print / traceback) is not echoed to the terminal: it is kept at the tail by
		// stderrCollector and returned in the logs field of the done/error frame (handle_code additionally logs
		// errors), so discarding the forwarded copy is enough to keep sandboxd's terminal free of script output.
		Stderr: io.Discard,
		Mounts: []sandbox.Mount{
			{Host: tmpDir, Guest: tmpGuestDir},
		},
		Limits: limits,
	}

	// Defensive assertion: Dir must sit under a mount point.
	if err := guardDir(spec.Dir, spec.Mounts); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: internal path error: %w", err)
	}

	proc, err := sb.Start(context.Background(), spec)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("worker: start codehost: %w", err)
	}

	w := &Worker{
		ID:     uuid.NewString(),
		stdin:  proc.Stdin(),
		stdout: proc.Stdout(),
		proc:   proc,
		exited: make(chan struct{}),
		tmpDir: tmpDir,
	}
	w.alive.Store(true)

	// Exit watch: it alone reaps the child (see watch), and the pool's hot path uses it to decide "is this still
	// usable" without sending a ping on every borrow / return.
	go w.watch()

	// Health check: send ping, wait for pong, confirming codehost really started.
	// It only happens once while establishing the connection; the per-execute hot path never probes again.
	if err := w.ping(); err != nil {
		w.Destroy()
		return nil, fmt.Errorf("worker: codehost health check: %w", err)
	}
	return w, nil
}

// resolveCodehostBinary normalizes the codehost binary path.
//
// binary may be a bare command name (resolved through PATH), a container path (/usr/local/bin/codehost) or a host
// path (relative / Windows absolute). Only "host-side" paths are made absolute:
//   - a Unix absolute path (leading /) is a container path whose meaning belongs to the container and must be
//     passed through unchanged — filepath.Abs is platform dependent: on Windows it would turn
//     /usr/local/bin/codehost into d:\usr\..., and docker/nsjail could not exec it (the root cause of
//     "read header: EOF");
//   - any other path containing a separator is made absolute relative to sandboxd's cwd: under none the exec'd
//     cmd.Dir is set to this session's exclusive tmpDir, so a relative path would resolve against tmpDir and
//     miss (no such file);
//   - a bare command name is left alone and resolved through PATH.
func resolveCodehostBinary(binary string) (string, error) {
	switch {
	case strings.HasPrefix(binary, "/"):
		return binary, nil
	case strings.ContainsAny(binary, `/\`):
		abs, err := filepath.Abs(binary)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return binary, nil
}

// watch reaps the child process and maintains the liveness flag.
//
// Why it alone calls Wait: exec.Cmd.Wait must not be called concurrently or twice, and after Kill someone must
// reap the child (otherwise zombies pile up in the process table). So "who Waits" has exactly one answer — here;
// Destroy only waits on exited and never calls Wait a second time.
//
// This also removes liveness probing from the hot path: alive goes false as soon as the process exits, so borrow
// / return only reads the flag instead of a synchronous "ping → wait for pong" round trip (lua is a µs-level
// call, and the fixed cost of those two round trips would exceed the script itself). The janitor still pings
// every 30s as a backstop.
func (w *Worker) watch() {
	_ = w.proc.Wait() // the process exited / was killed, so an error is normal
	w.alive.Store(false)
	close(w.exited)
}

// ping sends a ping and waits for the pong, verifying the worker process is alive.
//
// It is only used when establishing the connection (health check) and by the janitor sweep, **never on the
// borrow / return hot path**.
func (w *Worker) ping() error {
	if err := sendMsg(w.stdin, map[string]any{"action": "ping"}); err != nil {
		return fmt.Errorf("send ping: %w", err)
	}

	type result struct {
		msg map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := recvMsg(w.stdout)
		ch <- result{msg, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("recv pong: %w", r.err)
		}
		action, _ := r.msg["action"].(string)
		if action != "pong" {
			return fmt.Errorf("unexpected response: %s", action)
		}
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for pong")
	}
}

// Send sends a message to the worker (writing to stdin).
func (w *Worker) Send(msg map[string]any) error { return sendMsg(w.stdin, msg) }

// Recv receives a message from the worker (reading stdout).
func (w *Worker) Recv() (map[string]any, error) { return recvMsg(w.stdout) }

// Kill force-kills the worker process and marks it unusable.
func (w *Worker) Kill() {
	if !w.alive.Load() {
		return
	}
	w.alive.Store(false)
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
	if w.stdout != nil {
		_ = w.stdout.Close()
	}
	if w.proc != nil {
		_ = w.proc.Kill()
	}
}

// Destroy destroys the worker for good: force kill → reap the child → delete its exclusive tmp directory.
//
// The difference from Kill (unskippable now that restart turns over a worker at the end of every session):
//   - Kill only sends a signal and **does not Wait** — the isolation implementation's Process.Kill does not reap,
//     so a discarded worker stays in the process table as a zombie. Under L3 every request discards a worker, so
//     not reaping means a per-request leak.
//   - The tmp directory used to be cleared wholesale only at the next startup; with restart it must be deleted
//     together with the worker, otherwise the number of directories grows monotonically with the request volume.
func (w *Worker) Destroy() {
	if w == nil {
		return
	}
	w.Kill()
	w.reap()
	removeTmpDir(w.tmpDir)
	w.tmpDir = ""
}

// reap waits for the child to be reaped by watch (usually immediately after Kill).
// The timeout only guards against extremes such as "the signal never arrived / the process is in D state", so the
// return path cannot hang.
// It deliberately does **not** call Wait itself: exec.Cmd.Wait must not be called concurrently, and reaping is
// owned by watch.
func (w *Worker) reap() {
	if w.proc == nil {
		return
	}
	select {
	case <-w.exited:
	case <-time.After(reapTimeout):
		// Being unable to reap must not block the caller: the process table is the backstop, at least the service
		// does not fall over
	}
}

// removeTmpDir deletes a worker's exclusive directory.
//
// The retry exists because right after a process is killed the isolation layer (nsjail's mount namespace /
// docker's mounts) may not have released it yet, and RemoveAll returns EBUSY at that moment; after a retry it
// usually works. A total failure is not fatal — the next startup's prepareTmpRoot empties the whole tmp_root.
func removeTmpDir(dir string) {
	if dir == "" {
		return
	}
	for attempt := 0; attempt < tmpRemoveAttempts; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(tmpRemoveRetryDelay)
	}
	xlog.Error("sandboxd: remove worker tmp dir %s failed, left for next startup cleanup", dir)
}

// Alive reports whether the worker is still considered alive (watch sets it false when the process exits).
func (w *Worker) Alive() bool { return w.alive.Load() }
