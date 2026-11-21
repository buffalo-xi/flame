//go:build !windows

package browserpool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into its own process group, so that killTree can signal the whole group.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree kills the child and every process in its group.
//
// A negative pid addresses the group; the browser's renderer / gpu / crashpad children inherit it, so one signal is
// enough. Falling back to killing the root alone keeps "the process is already gone" from being reported as an error.
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	} else if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
