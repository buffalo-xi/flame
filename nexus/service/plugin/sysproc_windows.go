//go:build windows

package plugin

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNewProcessGroup puts the host process in a process group of its own (only then can
// taskkill /T kill the whole tree).
const createNewProcessGroup = 0x00000200

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killGroup kills the process tree with taskkill /T; when the main process has already exited it
// returns non-zero, which can simply be ignored. The main process is killed once more afterwards, so
// this also works in a bare environment where taskkill is not on PATH.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	if err := cmd.Process.Kill(); err != nil {
		return nil // already exited: from the caller's point of view this is a success
	}
	return nil
}
