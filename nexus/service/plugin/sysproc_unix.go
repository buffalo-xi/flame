//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// setProcAttr 让宿主进程独占一个进程组 —— 超时时才能整组杀干净。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup 杀掉整个进程组（负 pid 即进程组）。杀不到时退回杀主进程。
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
