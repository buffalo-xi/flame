package isolate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// 角色协议：同一个测试二进制靠环境变量认出自己扮演哪一层。
// 用「重新执行自己」来造进程树，不依赖 sh / cmd 这类平台的 shell 特性。
const (
	helperRoleEnv = "ISOLATE_TEST_HELPER_ROLE"
	roleChild     = "child"
	roleGrand     = "grandchild"
	heartbeatEnv  = "ISOLATE_TEST_HEARTBEAT"
)

// TestKillTreeKillsGrandchildren 验证「杀干净」的核心承诺：
// 收掉一次执行时，它派生出的**孙进程**也必须一起消失。
//
// 为什么断言必须落在孙进程上：只杀直接子进程的实现同样能让本测试的父进程退出，
// 所以父进程的结束不能作为判据 —— 只能看孙进程还写不写心跳。
func TestKillTreeKillsGrandchildren(t *testing.T) {
	switch os.Getenv(helperRoleEnv) {
	case roleChild:
		childHelper(t)
		return
	case roleGrand:
		grandchildHelper()
		return
	}

	beat := filepath.Join(t.TempDir(), "beat")

	// 必须用 CommandContext 构造：os/exec 拒绝为 exec.Command 出来的 Cmd 设置 Cancel
	// （"command with a non-nil Cancel was not created with CommandContext"）。
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestKillTreeKillsGrandchildren")
	cmd.Env = append(os.Environ(),
		helperRoleEnv+"="+roleChild,
		heartbeatEnv+"="+beat,
	)
	// 只设 Stderr（Spec.Stderr 为 nil 时会接到空设备）；
	// Stdout 不能设 —— startWithPipes 要自己建管道。
	cmd.Stderr = io.Discard

	proc, err := startWithPipes(cmd, &sandbox.Spec{}, nil, nil)
	if err != nil {
		t.Fatalf("startWithPipes: %v", err)
	}

	// 等孙进程真正跑起来，否则后面的断言可能只是在测「它还没起来」。
	waitHeartbeat(t, beat)

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill: %v", err)
	}

	// 父进程也要收掉：Kill 之后 Wait 必须很快返回。
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Kill 之后父进程没有结束（Wait 卡住了）")
	}

	// 关键断言：孙进程不再写心跳 = 它真的不存在了。
	// 心跳值是自增序号，只要它活着就一定会变。
	before := readHeartbeat(beat)
	time.Sleep(500 * time.Millisecond)
	after := readHeartbeat(beat)
	if before != after {
		t.Errorf("孙进程仍在运行：心跳 %q → %q —— 这次 Kill 只覆盖了直接子进程", before, after)
	}
}

// childHelper 中间层：派生孙进程后自己一直活着。
//
// 不主动退出是关键：父进程自己退出不算「杀干净」，这一层必须活到被信号收走，
// 测试才在测同一件事。
func childHelper(t *testing.T) {
	beat := os.Getenv(heartbeatEnv)

	grand := exec.Command(os.Args[0], "-test.run=TestKillTreeKillsGrandchildren")
	grand.Env = append(os.Environ(),
		helperRoleEnv+"="+roleGrand,
		heartbeatEnv+"="+beat,
	)
	grand.Stdout, grand.Stderr = io.Discard, io.Discard
	if err := grand.Start(); err != nil {
		t.Fatalf("child: start grandchild: %v", err)
	}

	select {} // 挂住，等被杀
}

// grandchildHelper 心跳层：持续刷新心跳文件，直到进程消失。
func grandchildHelper() {
	beat := os.Getenv(heartbeatEnv)
	for tick := 0; ; tick++ {
		_ = os.WriteFile(beat, []byte(strconv.Itoa(tick)), 0o644)
		time.Sleep(50 * time.Millisecond)
	}
}

// waitHeartbeat 等心跳文件出现且有内容。
func waitHeartbeat(t *testing.T, beat string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if readHeartbeat(beat) != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("孙进程没有写出心跳：helper 进程树没起来，测试前提不成立")
}

func readHeartbeat(beat string) string {
	b, err := os.ReadFile(beat)
	if err != nil {
		return ""
	}
	return string(b)
}
