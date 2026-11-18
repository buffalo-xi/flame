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

// taskkillTimeout taskkill 自身的执行上限：杀进程的路径不能变成新的阻塞点。
const taskkillTimeout = 5 * time.Second

// taskkillNotFound taskkill 找不到进程时的退出码。
//
// 用退出码而不是输出文案：文案是本地化的（"ERROR: The process ... not found."
// 在中文 Windows 上完全是另一句话），而这个码不是。
//
// 这条路径是**常态**而不是异常：docker 模式下先删容器，CLI 会因此自己退出，
// 紧接着的 killTree 就会撞上「进程已经不在了」。
const taskkillNotFound = 128

// setProcessGroup 把子进程放进一个新的进程组。
//
// Windows 没有可杀的「进程组」对象（只有控制台进程组，且不可用于杀），
// 这里的旗标作用是让子进程不接收发给 sandboxd 控制台的 Ctrl+C 之类事件，
// 真正的整树清理靠 killTree 的 taskkill /T。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree 用 taskkill /T 杀掉子进程及其全部后代。
//
// 为什么用 taskkill 而不是 Job Object：Job 更严谨（分配即归属，进程无法脱离），
// 但要引入 golang.org/x/sys 依赖并自己管句柄生命周期 —— 当前收益不抵成本。
// /T 是按父子关系遍历，对「父进程已退出、后代被重新挂靠」的极端情况覆盖不到，
// 这是已知的、可接受的缺口（Windows 只是 none 模式的开发环境）。
//
// 返回 os.ErrProcessDone 表示这次执行已经结束（exec.Cmd.Cancel 约定的语义）。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid

	err := taskkillTree(pid)
	if err == nil {
		return nil
	}

	// 根进程已经不存在：这次执行结束了 —— 归一成 ErrProcessDone
	// （exec.Cmd.Cancel 约定的语义），不要报给调用方当失败。
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == taskkillNotFound {
		return os.ErrProcessDone
	}

	// 其它失败：再用 os 自己的判定确认一次「是不是本来就没了」，
	// 仍无法确认就如实上报 —— 证明不了杀干净时不能假装成功。
	if killErr := cmd.Process.Kill(); errors.Is(killErr, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}

// taskkillTree 执行 taskkill /T /F。
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
