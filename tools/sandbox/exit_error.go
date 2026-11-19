package sandbox

import "fmt"

// ExitError reports a non-zero exit code from a process inside the sandbox.
//
// Why it exists: the ProcessState inside *exec.ExitError is unexported, so the
// remote implementation cannot construct one. This type is the single way to
// express "non-zero exit code"; the local implementation (execProcess.Wait in
// process.go) normalises to it as well, leaving callers with one error type.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ExitCode returns the exit code (same shape as *exec.ExitError, so callers can
// handle either uniformly).
func (e *ExitError) ExitCode() int { return e.Code }
