package isolate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"flame/tools/sandbox"
)

// waitDelayAfterKill caps how long cmd.Wait waits for the I/O pipes to settle after the process exits.
//
// Why it must be set: whenever Stderr is not an *os.File (handle_exec passes a websocket frame writer), os/exec starts
// an internal copying goroutine for it, and Wait waits for that goroutine to finish. The only exit from that wait is
// the pipe's EOF — and stderr is a fd inherited by the whole process tree, so as long as one grandchild still holds
// it, EOF never arrives and Wait hangs forever. With this set, Wait waits at most that long and then closes the pipe
// and returns os/exec.ErrWaitDelay (see awaitGoroutines in os/exec).
const waitDelayAfterKill = 5 * time.Second

// killFunc cleans up "all the consequences of this execution".
//
// Why a function instead of a plain cmd.Process.Kill(): one execution is usually a process tree, and under docker the
// thing being executed is not even in the host process tree (the container is owned by dockerd) — in both cases
// "kill one pid" does not hold. The default implementation lives in sysproc_*.go (process group / taskkill /T), and
// docker substitutes "delete the container first, then collect the CLI".
//
// The return value follows exec.Cmd.Cancel's convention: os.ErrProcessDone when it has already ended.
type killFunc func() error

// execProcess is the Process implementation shared by all local isolation implementations: it wraps the pipes of an
// *exec.Cmd into a uniform handle and releases cancel after Wait/Kill.
//
// Exit codes are uniformly normalised into *sandbox.ExitError (the remote implementation has no such limitation, but
// both ends keep the same error type so callers only handle one).
type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cancel context.CancelFunc
	kill   killFunc

	mu       sync.Mutex
	finished bool // true after Wait: this execution is over
}

func (p *execProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execProcess) Stdout() io.ReadCloser { return p.stdout }

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	p.markFinished()

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &sandbox.ExitError{Code: ee.ExitCode()}
	}
	return err
}

// Kill ends this execution.
//
// It does not mark finished: a repeated Kill is legal and useful (the client disconnecting and the forwarding wrap-up
// each kill once, the second being a retry) — only Wait means this execution has really ended.
func (p *execProcess) Kill() error {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return os.ErrProcessDone
	}
	kill := p.kill
	p.mu.Unlock()

	err := kill()
	p.releaseCancel()
	return err
}

// markFinished marks the execution as finished and releases cancel.
func (p *execProcess) markFinished() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	p.finished = true
	p.releaseCancelLocked()
}

// releaseCancel releases cancel (the Kill path: the process is already killed, so the ctx timer need not stay armed).
func (p *execProcess) releaseCancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseCancelLocked()
}

func (p *execProcess) releaseCancelLocked() {
	releaseCancel(p.cancel)
	p.cancel = nil
}

// startWithPipes sets up the stdin/stdout pipes, wires stderr, and then starts the process.
// cancel is released once the process ends (Wait/Kill), so a freshly started process is not cancelled early.
//
// kill is the "how to kill this execution" hook; nil means the default (process group / taskkill, see sysproc_*.go).
// It is also bound to cmd.Cancel — that step cannot be skipped: the default cancellation of exec.CommandContext is
// Process.Kill(), which kills a single pid and bypasses the process group / container approach entirely (timeouts and
// ctx cancellation both go down that path).
//
// ⚠️ Callers must therefore build cmd with exec.CommandContext: os/exec refuses Cancel on a Cmd created by
// exec.Command and fails at Start with "command with a non-nil Cancel was not created with CommandContext".
func startWithPipes(cmd *exec.Cmd, spec *sandbox.Spec, cancel context.CancelFunc, kill killFunc) (sandbox.Process, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	// Keep stderr nil when it is nil: os/exec then wires it straight to /dev/null (or NUL on Windows) and creates no
	// copying goroutine. Writing io.Discard there would instead add one more pipe Wait has to wait for — and a
	// grandchild can hold it indefinitely (see waitDelayAfterKill).
	cmd.Stderr = spec.Stderr

	if kill == nil {
		kill = func() error { return killTree(cmd) }
	}
	setProcessGroup(cmd)
	cmd.Cancel = kill
	cmd.WaitDelay = waitDelayAfterKill

	if err := cmd.Start(); err != nil {
		releaseCancel(cancel)
		return nil, err
	}
	return &execProcess{cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel, kill: kill}, nil
}

// releaseCancel safely releases cancel.
func releaseCancel(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
}
