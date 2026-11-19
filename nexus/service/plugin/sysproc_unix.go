//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the host process in a process group of its own — only then can a timeout kill the
// whole group.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the whole process group (a negative pid means the group). When that fails it falls
// back to killing the main process.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}
