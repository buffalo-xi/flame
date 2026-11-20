//go:build !windows

package codehost

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the interpreter process into its own process group (pgid = pid).
//
// The same approach as sysproc_unix.go in sandboxd/isolate: kill can only target a single pid, while a script may fork
// descendants, so putting it in a separate process group lets one signal to the whole group cover the entire tree. The
// cost is that the child no longer receives signals sent to codehost (which should not reach it anyway).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree kills every member of the interpreter's process group (SIGKILL, leaving no window for a graceful exit).
//
// Returning os.ErrProcessDone means "the process is already gone" — that is the semantics exec.Cmd.Cancel expects.
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	// Hit the whole group first, then the pid once more — the child may not have had time to setpgid yet (the group
	// does not exist then), in which case the group kill returns ESRCH.
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	selfErr := syscall.Kill(pid, syscall.SIGKILL)

	switch {
	case groupErr == nil || selfErr == nil:
		return nil
	case errors.Is(groupErr, syscall.ESRCH) && errors.Is(selfErr, syscall.ESRCH):
		return os.ErrProcessDone
	case errors.Is(selfErr, syscall.ESRCH):
		return groupErr
	default:
		return selfErr
	}
}
