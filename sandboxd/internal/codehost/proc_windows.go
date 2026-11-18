//go:build windows

package codehost

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

const (
	taskkillTimeout  = 5 * time.Second
	taskkillNotFound = 128
)

// setProcessGroup 把解释器进程放进新的进程组。
//
// Windows 没有可杀的「进程组」对象，这里的旗标只让子进程不接收发给 codehost 控制台
// 的 Ctrl+C 之类事件；真正的整树清理靠 killTree 的 taskkill /T。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree 用 taskkill /T 杀掉解释器及其全部后代。
//
// 返回 os.ErrProcessDone 表示「进程已经结束」（exec.Cmd.Cancel 约定的语义）。
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

func taskkillTree(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), taskkillTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return fmt.Errorf("codehost: taskkill pid %d: %w", pid, err)
	}
	return fmt.Errorf("codehost: taskkill pid %d: %w (%s)", pid, err, msg)
}
