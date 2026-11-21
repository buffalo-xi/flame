package browserpool

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// stderrTailLines is how many driver stderr lines are kept for diagnostics.
const stderrTailLines = 20

// stdoutLineBytes is the maximum length of one protocol line. The driver caps its own responses too; this is the "the
// protocol is broken" backstop, not a tuning knob.
const stdoutLineBytes = 4 << 20

// workerSpec is everything a worker is created from.
type workerSpec struct {
	ID         int
	Session    string
	Tenant     string
	ProfileDir string
	// WorkspaceDir is the session's directory inside the shared workspace root; it becomes the driver's cwd, so a
	// relative screenshot path or a download lands where the caller can read it.
	WorkspaceDir string
	// DownloadsDir is WorkspaceDir/downloads: downloads are files the model must be able to open afterwards.
	DownloadsDir string
}

// worker is one Chromium owned by exactly one session.
//
// One worker serves one session for its whole life. Reusing a worker across sessions would be cheaper, but Chromium
// keeps cookies, local storage and login state in its user-data-dir — reuse would hand one tenant's session to the
// next. The pool therefore offers capacity, queueing, isolation and crash containment, not process reuse.
type worker struct {
	id      int
	session string
	tenant  string
	proc    procHandle
	sink    *sink
	stderr  *lineRing
	started time.Time

	// workspace / downloads are kept for the debug endpoint ("where did this session's files go").
	workspace string
	downloads string

	// onExit is called exactly once when the driver ends on its own. Set before start().
	onExit func(reason string)

	writeMu sync.Mutex

	mu     sync.Mutex
	dead   bool
	reason string
}

func newWorker(spec workerSpec, p procHandle) *worker {
	return &worker{
		id:        spec.ID,
		session:   spec.Session,
		tenant:    spec.Tenant,
		proc:      p,
		sink:      &sink{},
		stderr:    newLineRing(stderrTailLines),
		started:   time.Now(),
		workspace: spec.WorkspaceDir,
		downloads: spec.DownloadsDir,
	}
}

// start launches the stdout pump and the stderr drain. It must be called after onExit is set: a driver that dies
// immediately would otherwise call a nil callback.
func (w *worker) start() {
	go w.pumpStdout()
	go w.drainStderr()
}

// Write forwards one chunk of the protocol to the driver.
func (w *worker) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	if !w.alive() {
		return 0, fmt.Errorf("browser process exited: %s", w.exitReason())
	}
	n, err := w.proc.Stdin().Write(p)
	if err != nil {
		return n, fmt.Errorf("browser process is gone: %w", err)
	}
	return n, nil
}

// attach points the driver's stdout at the current connection; detach restores "drop everything".
func (w *worker) attach(out io.Writer) { w.sink.set(out) }
func (w *worker) detach()              { w.sink.set(nil) }

func (w *worker) alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.dead
}

func (w *worker) exitReason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason
}

// kill takes the whole tree down and waits (bounded) for the pump to notice.
func (w *worker) kill() {
	_ = w.proc.Kill()
}

// pumpStdout is the single reader of the driver's stdout: one JSON line per response, forwarded to whatever connection
// is attached at that moment.
//
// It must stay the only reader — the same reason as sandboxd's stdout broadcast goroutine: two readers on one pipe tear
// the output apart, and a reader bound to a connection would block forever after a disconnect.
func (w *worker) pumpStdout() {
	sc := bufio.NewScanner(w.proc.Stdout())
	sc.Buffer(make([]byte, 0, 64*1024), stdoutLineBytes)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		buf := make([]byte, 0, len(line)+1)
		buf = append(buf, line...)
		buf = append(buf, '\n')
		_, _ = w.sink.Write(buf)
	}

	// stdout ended: the driver is going away (exit, crash, or a kill that already happened).
	reason := "browser process ended"
	if err := sc.Err(); err != nil {
		reason = "browser process ended: " + err.Error()
	}
	if tail := w.stderr.tail(); tail != "" {
		reason += "; last stderr: " + tail
	}

	w.mu.Lock()
	w.dead = true
	if w.reason == "" {
		w.reason = reason
	}
	w.mu.Unlock()

	_ = w.proc.Wait()
	if w.onExit != nil {
		w.onExit(w.exitReason())
	}
}

// drainStderr keeps the tail of the driver's stderr. Chromium's real failure reasons appear there (missing libraries,
// profile locked, sandbox errors) and nowhere else.
func (w *worker) drainStderr() {
	sc := bufio.NewScanner(w.proc.Stderr())
	sc.Buffer(make([]byte, 0, 16*1024), 256*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			w.stderr.add(line)
		}
	}
}

// factory creates workers; tests substitute it to run the pool without a browser.
type factory interface {
	create(ctx context.Context, spec workerSpec) (*worker, error)
}

// execFactory starts a real browserhost per session.
type execFactory struct {
	cfg *Config
}

func (f *execFactory) create(ctx context.Context, spec workerSpec) (*worker, error) {
	if err := os.MkdirAll(spec.ProfileDir, 0o755); err != nil {
		return nil, fmt.Errorf("browserd: create profile dir %s: %w", spec.ProfileDir, err)
	}
	if err := os.MkdirAll(spec.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("browserd: create workspace dir %s: %w", spec.WorkspaceDir, err)
	}
	if err := os.MkdirAll(spec.DownloadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("browserd: create downloads dir %s: %w", spec.DownloadsDir, err)
	}

	args := make([]string, 0, 8)
	if f.cfg.Browser.ExecPath != "" {
		args = append(args, "--exec-path", f.cfg.Browser.ExecPath)
	}
	args = append(args, "--profile", spec.ProfileDir, "--downloads", spec.DownloadsDir)
	if !f.cfg.HeadlessDefault() {
		// Headed by default only makes sense where headed is allowed at all (a developer's desktop).
		if !f.cfg.HeadedAllowed() {
			return nil, fmt.Errorf("browser.headless is false but browser.allow_headed is false — a headed browser needs a display")
		}
		args = append(args, "--allow-headed", "--default-headed")
	} else if f.cfg.HeadedAllowed() {
		args = append(args, "--allow-headed")
	}

	p, err := startProc(ctx, f.cfg.Browser.Binary, args, spec.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	return newWorker(spec, p), nil
}

// sessionProfileDir is the per-session user-data-dir.
func sessionProfileDir(cfg *Config, id int) string {
	return filepath.Join(cfg.Browser.ProfileRoot, fmt.Sprintf("w%03d", id))
}

// workspaceDir resolves the driver's working directory for one session.
//
// workDir is relative to WorkspaceRoot and comes from the caller, so it is treated as untrusted: an absolute path or a
// ".." escape would point the browser at any directory this service can write to. An empty value gets a scratch
// directory under the profile root — never this process's own cwd (which the tenant cannot read) and never inside the
// workspace root (which belongs to nexus: an unattributed _scratch/ directory there would look like tenant data).
func workspaceDir(cfg *Config, id int, workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return filepath.Join(cfg.Browser.ProfileRoot, "_scratch", fmt.Sprintf("w%03d", id)), nil
	}
	raw := strings.TrimSpace(workDir)
	// A leading separator is rejected on every platform: on Windows "/etc/passwd" is not "absolute" by Go's definition
	// (no volume), but it still reads as "somewhere else" to a caller and is never a legitimate storage key.
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", fmt.Errorf("workspace path %q must be relative and inside the workspace root", workDir)
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path %q must be relative and inside the workspace root", workDir)
	}
	return filepath.Join(cfg.WorkspaceRoot, clean), nil
}
