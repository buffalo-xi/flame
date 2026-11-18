//go:build !windows

package isolate

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup 让子进程自成一个进程组（pgid = pid）。
//
// 为什么必须：kill 只能杀一个 pid，而一次执行往往是一棵进程树
// （shell → 命令行工具 → 它自己的子进程）。把子进程放进独立进程组之后，
// 对该组发一次信号就能覆盖整棵树。
//
// 顺带的收益：子进程不再收到发给 sandboxd 的信号（Ctrl-C 不会穿透进沙箱）。
//
// ⚠️ 上限：进程组是自愿加入、且可以主动 setsid 脱离的。脱离之后就杀不到了，
// 而 none 模式没有 cgroup / namespace 兜底 —— 所以它的保证只是「组内干净」，
// 不是「杀掉全部后代」。docker 模式的兜底才是不可逃逸的（见 docker.go）。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree 杀掉子进程所在进程组的全部成员。
//
// 用 SIGKILL 而不是先 TERM：调用方（客户端断开 / 超时 / 取消）要的是
// 「立刻收干净」，没有留给优雅退出的窗口。
//
// 返回 os.ErrProcessDone 表示「这次执行已经结束」—— 这是 exec.Cmd.Cancel
// 约定的语义（见 os/exec 的 watchCtx）：进程本来就没了时不该报错。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	// 先对整组下手（覆盖后代的常规手段），再对 pid 补一次 ——
	// 子进程可能还没来得及执行 setpgid（组还不存在），此时组杀会 ESRCH。
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	selfErr := syscall.Kill(pid, syscall.SIGKILL)

	switch {
	case groupErr == nil || selfErr == nil:
		return nil
	case errors.Is(groupErr, syscall.ESRCH) && errors.Is(selfErr, syscall.ESRCH):
		return os.ErrProcessDone
	case errors.Is(selfErr, syscall.ESRCH):
		// 自己没了但组杀失败：如实上报组杀的错误，不假装成功。
		return groupErr
	default:
		return selfErr
	}
}
