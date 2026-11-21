package browserpool

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// procHandle is the minimal process surface the pool needs.
//
// It exists so the interesting logic (leasing, queueing, reclamation, stdout dispatch) can be tested without a real
// Chromium: a machine without Chrome must still be able to run the pool's tests, and a test that spawns a browser is a
// test that fails for the wrong reason.
type procHandle interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Kill() error
	Wait() error
}

// execProc is a real child process.
type execProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// startProc launches the driver.
//
// The process is put into its own group and killed through killTree, never through Process.Kill alone: Chromium is a
// process tree (browser + gpu + renderer + crashpad), and killing only the root leaves the children holding hundreds of
// megabytes until the machine is rebooted.
func startProc(ctx context.Context, binary string, args []string, dir string) (procHandle, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	// The cwd is the session's workspace: browserhost writes a relative screenshot path and a download straight into
	// it, which is what makes those files readable by the caller afterwards.
	cmd.Dir = dir
	setProcessGroup(cmd)
	// The context path (shutdown / cancel) must take the whole tree too, exactly like an explicit Kill.
	cmd.Cancel = func() error { return killTree(cmd) }
	// Do not let a stuck pipe keep Wait blocked forever after the kill.
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("browserd: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("browserd: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("browserd: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browserd: start %s: %w", binary, err)
	}
	return &execProc{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *execProc) Stdin() io.WriteCloser { return p.stdin }
func (p *execProc) Stdout() io.ReadCloser { return p.stdout }
func (p *execProc) Stderr() io.ReadCloser { return p.stderr }

// Kill takes down the process tree.
func (p *execProc) Kill() error {
	return killTree(p.cmd)
}

// Wait reaps the process; it must only be called after the stdout stream ended (the driver writes its last line, then
// exits), otherwise it competes with the pump for the same pipes.
func (p *execProc) Wait() error {
	return p.cmd.Wait()
}

// sink is a writer whose target can be swapped: the driver's stdout is persistent, while the connection it must be
// forwarded to changes with every call.
//
// Write always reports success: a failed write means the client went away, and that must not tear down the forwarding
// path — the next call would then get no output at all (the same reasoning as writerSwitch in sandboxd).
type sink struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *sink) set(w io.Writer) {
	s.mu.Lock()
	s.w = w
	s.mu.Unlock()
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.w
	s.mu.Unlock()
	if w == nil {
		return len(p), nil // between calls: drop (the driver only writes in response to a request)
	}
	_, _ = w.Write(p)
	return len(p), nil
}

// lineRing keeps the last N stderr lines of a worker. Chromium's failure reasons end up on stderr, and the useful line is
// usually the last one — keeping everything would be a memory leak, keeping nothing makes "the browser died" undebuggable.
type lineRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newLineRing(max int) *lineRing {
	return &lineRing{max: max}
}

func (r *lineRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

// tail returns the buffered lines joined by newlines.
func (r *lineRing) tail() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := ""
	for i, l := range r.lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
