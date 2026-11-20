//go:build windows

package isolate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// taskkillTimeout caps the taskkill invocation itself: the kill path must not become a new blocking point.
const taskkillTimeout = 5 * time.Second

// taskkillNotFound is the exit code of taskkill when it cannot find the process.
//
// An exit code rather than output text: the text is localised ("ERROR: The process ... not found." is a completely
// different sentence on Chinese Windows), while the code is not.
//
// This path is the **norm**, not an anomaly: under docker the container is deleted first, the CLI exits because of it,
// and the killTree that follows immediately runs into "the process is already gone".
const taskkillNotFound = 128

// setProcessGroup puts the child process into a new process group.
//
// Windows has no "process group" object that can be killed (only a console process group, which cannot be used for
// killing); the flag here makes the child stop receiving events such as Ctrl+C sent to sandboxd's console, and the real
// whole-tree cleanup relies on killTree's taskkill /T.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree kills the child process and all its descendants with taskkill /T.
//
// Why taskkill rather than a Job Object: a Job is stricter (assignment is ownership, and a process cannot escape), but
// it would pull in the golang.org/x/sys dependency and require managing handle lifetimes by hand — the benefit does not
// justify the cost today. /T walks the parent-child relation, so it cannot cover the extreme case of "the parent
// already exited and the descendants were re-parented"; that gap is known and acceptable (Windows is only a development
// environment for the none mode).
//
// Returning os.ErrProcessDone means this execution has already ended (the semantics exec.Cmd.Cancel expects).
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	err := taskkillTree(pid)
	if err == nil {
		return nil
	}

	// The root process no longer exists: this execution is over — normalise to ErrProcessDone (the semantics
	// exec.Cmd.Cancel expects) and do not report it to the caller as a failure.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == taskkillNotFound {
		return os.ErrProcessDone
	}

	// Any other failure: use os's own judgement once more to confirm "was it already gone anyway", and if that still
	// cannot be confirmed, report it honestly — do not pretend success when "killed everything" cannot be proven.
	if killErr := cmd.Process.Kill(); errors.Is(killErr, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}

// taskkillTree runs taskkill /T /F.
func taskkillTree(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), taskkillTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return fmt.Errorf("isolate: taskkill pid %d: %w", pid, err)
	}
	return fmt.Errorf("isolate: taskkill pid %d: %w (%s)", pid, err, msg)
}
