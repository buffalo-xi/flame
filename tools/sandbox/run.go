package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

// Run is the convenience wrapper for a one-shot execution:
//
//	Start → write Spec.Stdin (if any) → close stdin → collect stdout/stderr → Wait
//
// It does not mutate the spec it is given (a copy is made internally) and folds
// stderr into Result. Output is bounded by Limits.MaxOutputBytes (excess is cut
// off and flagged as Truncated) so a huge output cannot exhaust memory. For a
// long-lived interactive session do not use this — call Sandbox.Start directly.
func Run(ctx context.Context, sb Sandbox, spec *Spec) (*Result, error) {
	if sb == nil {
		return nil, errors.New("sandbox: nil sandbox")
	}

	stderrOut := newLimitedWriter(spec.Limits.MaxOutputBytes)
	local := *spec // copy, so the caller's spec stays untouched
	local.Stderr = stderrOut
	local.Stdin = nil // Stdin is consumed by Run only; never passed to the implementation

	proc, err := sb.Start(ctx, &local)
	if err != nil {
		return nil, err
	}

	// Write the input and close stdin (signals end of input).
	if len(spec.Stdin) > 0 {
		if _, werr := proc.Stdin().Write(spec.Stdin); werr != nil {
			_ = proc.Kill()
			return nil, werr
		}
	}
	_ = proc.Stdin().Close()

	// Read stdout concurrently (a full pipe would block the child process).
	stdoutOut := newLimitedWriter(spec.Limits.MaxOutputBytes)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdoutOut, proc.Stdout())
	}()

	waitErr := proc.Wait()
	wg.Wait()

	res := &Result{
		Stdout:    stdoutOut.Bytes(),
		Stderr:    stderrOut.Bytes(),
		Truncated: stdoutOut.Truncated() || stderrOut.Truncated(),
	}
	if waitErr != nil {
		var ee *ExitError
		if errors.As(waitErr, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil // a non-zero exit code is not an error; the caller decides
		}
		return res, waitErr
	}
	return res, nil
}

// limitedWriter buffers up to a cap: past the cap it keeps accepting but
// discards the content and sets Truncated. It always returns len(p) so the
// writer (the child process) is never blocked.
type limitedWriter struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func newLimitedWriter(max int64) *limitedWriter {
	return &limitedWriter{max: max}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.max <= 0 {
		return w.buf.Write(p)
	}
	remain := w.max - int64(w.buf.Len())
	if remain <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remain {
		_, _ = w.buf.Write(p[:remain])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) Bytes() []byte   { return w.buf.Bytes() }
func (w *limitedWriter) Truncated() bool { return w.truncated }
