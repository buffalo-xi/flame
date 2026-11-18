//go:build !windows

package codehost

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup 让解释器进程自成一个进程组（pgid = pid）。
//
// 与 sandboxd/isolate 的 sysproc_unix.go 同款收口：kill 只能杀单 pid，而脚本可能
// fork 后代，放进独立进程组后对整组发一次信号即可覆盖整棵树。代价是子进程不再收到
// 发给 codehost 的信号（本就不该穿透）。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree 杀掉解释器所在进程组的全部成员（SIGKILL，不留优雅退出窗口）。
//
// 返回 os.ErrProcessDone 表示「进程已经结束」—— 这是 exec.Cmd.Cancel 约定的语义。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	// 先对整组下手，再对 pid 补一次 —— 子进程可能还没来得及 setpgid（组还不存在），
	// 此时组杀会 ESRCH。
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
