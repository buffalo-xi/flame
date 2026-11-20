//go:build !windows

package isolate

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child process into its own process group (pgid = pid).
//
// Why it is necessary: kill can only target a single pid, while one execution is usually a process tree
// (shell → command-line tool → its own children). Once the child is in a separate process group, one signal to that
// group covers the whole tree.
//
// A side benefit: the child no longer receives signals sent to sandboxd (Ctrl-C does not reach into the sandbox).
//
// ⚠️ Limit: a process group is joined voluntarily and can be left with setsid. Once it has left it can no longer be
// killed, and the none mode has no cgroup / namespace to fall back on — so its guarantee is only "clean inside the
// group", not "every descendant killed". Only the docker mode's fallback is inescapable (see docker.go).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree kills every member of the child's process group.
//
// SIGKILL rather than TERM first: the caller (client disconnect / timeout / cancellation) wants "cleaned up right now",
// with no window left for a graceful exit.
//
// Returning os.ErrProcessDone means "this execution has already ended" — that is the semantics exec.Cmd.Cancel expects
// (see watchCtx in os/exec): it should not report an error when the process was already gone.
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	// Hit the whole group first (the normal way to cover descendants), then the pid once more — the child may not have
	// run setpgid yet (the group does not exist), in which case the group kill returns ESRCH.
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	selfErr := syscall.Kill(pid, syscall.SIGKILL)

	switch {
	case groupErr == nil || selfErr == nil:
		return nil
	case errors.Is(groupErr, syscall.ESRCH) && errors.Is(selfErr, syscall.ESRCH):
		return os.ErrProcessDone
	case errors.Is(selfErr, syscall.ESRCH):
		// The process itself is gone but the group kill failed: report the group kill's error honestly instead of
		// pretending success.
		return groupErr
	default:
		return selfErr
	}
}
