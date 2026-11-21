//go:build windows

package browserpool

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
// An exit code rather than output text: the text is localised (Chinese Windows prints a different sentence), while the
// code is not.
const taskkillNotFound = 128

// setProcessGroup puts the child into a new process group.
//
// Windows has no killable process-group object (only a console group, which cannot be used for killing); this only keeps
// the child from receiving events such as Ctrl+C sent to this service's console. The real tree cleanup is killTree.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree kills the child and all its descendants with taskkill /T /F.
//
// Why taskkill rather than a Job Object: a Job is stricter (assignment is ownership, a process cannot escape), but it
// would require managing handle lifetimes by hand. /T walks the parent-child relation, so it cannot cover "the parent
// already exited and the descendants were re-parented" — a known gap, acceptable for a development host.
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	err := taskkillTree(pid)
	if err == nil {
		return nil
	}

	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == taskkillNotFound {
		return os.ErrProcessDone
	}
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
		return fmt.Errorf("browserd: taskkill pid %d: %w", pid, err)
	}
	return fmt.Errorf("browserd: taskkill pid %d: %w (%s)", pid, err, msg)
}
