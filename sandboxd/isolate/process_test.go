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

// Role protocol: the same test binary recognises which layer it is playing from an environment variable.
// Re-executing itself builds the process tree without relying on platform shell features such as sh / cmd.
const (
	helperRoleEnv = "ISOLATE_TEST_HELPER_ROLE"
	roleChild     = "child"
	roleGrand     = "grandchild"
	heartbeatEnv  = "ISOLATE_TEST_HEARTBEAT"
)

// TestKillTreeKillsGrandchildren verifies the core promise of "kill everything": when an execution is cleaned up, the
// **grandchildren** it spawned must disappear too.
//
// Why the assertion must be on the grandchild: an implementation that kills only the direct child also makes this
// test's parent exit, so the parent's termination cannot be the criterion — only whether the grandchild still writes
// its heartbeat.
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

	// It must be built with CommandContext: os/exec refuses Cancel on a Cmd created by exec.Command
	// ("command with a non-nil Cancel was not created with CommandContext").
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestKillTreeKillsGrandchildren")
	cmd.Env = append(os.Environ(),
		helperRoleEnv+"="+roleChild,
		heartbeatEnv+"="+beat,
	)
	// Only Stderr is set (a nil Spec.Stderr is wired to the null device); Stdout must not be set — startWithPipes
	// creates its own pipes.
	cmd.Stderr = io.Discard

	proc, err := startWithPipes(cmd, &sandbox.Spec{}, nil, nil)
	if err != nil {
		t.Fatalf("startWithPipes: %v", err)
	}

	// Wait until the grandchild is really running, otherwise the later assertion may just be testing "it is not up
	// yet".
	waitHeartbeat(t, beat)

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill: %v", err)
	}

	// The parent must be collected as well: after Kill, Wait has to return quickly.
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the parent did not end after Kill (Wait hung)")
	}

	// The key assertion: the grandchild no longer writes a heartbeat = it really no longer exists.
	// The heartbeat is an incrementing counter, so it necessarily changes as long as it is alive.
	before := readHeartbeat(beat)
	time.Sleep(500 * time.Millisecond)
	after := readHeartbeat(beat)
	if before != after {
		t.Errorf("the grandchild is still running: heartbeat %q → %q — this Kill only covered the direct child", before, after)
	}
}

// childHelper is the middle layer: it spawns the grandchild and then stays alive itself.
//
// Not exiting voluntarily is the point: the parent exiting by itself does not count as "killed", so this layer must stay
// alive until it is taken out by a signal for the test to be testing the same thing.
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

	select {} // block until killed
}

// grandchildHelper is the heartbeat layer: it keeps refreshing the heartbeat file until the process disappears.
func grandchildHelper() {
	beat := os.Getenv(heartbeatEnv)
	for tick := 0; ; tick++ {
		_ = os.WriteFile(beat, []byte(strconv.Itoa(tick)), 0o644)
		time.Sleep(50 * time.Millisecond)
	}
}

// waitHeartbeat waits for the heartbeat file to appear and have content.
func waitHeartbeat(t *testing.T, beat string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if readHeartbeat(beat) != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the grandchild never wrote a heartbeat: the helper process tree did not start, so the test premise does not hold")
}

func readHeartbeat(beat string) string {
	b, err := os.ReadFile(beat)
	if err != nil {
		return ""
	}
	return string(b)
}
