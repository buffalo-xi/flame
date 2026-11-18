//go:build windows

package plugin

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNewProcessGroup 让宿主进程独占一个进程组（taskkill /T 才能整棵树杀掉）。
const createNewProcessGroup = 0x00000200

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killGroup 用 taskkill /T 杀进程树；主进程已退出时它返回非零，忽略即可。
// 最后无论如何再 Kill 一次主进程：taskkill 不在 PATH 的极简环境里也能收口。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	if err := cmd.Process.Kill(); err != nil {
		return nil // 已经退出：对调用方而言这就是成功
	}
	return nil
}
