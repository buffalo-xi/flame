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

// setProcessGroup puts the interpreter process into a new process group.
//
// Windows has no "process group" object that can be killed; the flag here only keeps the child from receiving events
// such as Ctrl+C sent to codehost's console. The real whole-tree cleanup relies on killTree's taskkill /T.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree kills the interpreter and all its descendants with taskkill /T.
//
// Returning os.ErrProcessDone means "the process is already gone" (the semantics exec.Cmd.Cancel expects).
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
