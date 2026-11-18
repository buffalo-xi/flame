package executors

import (
	"context"
	"testing"
	"time"

	flow "flame/nexus/service/flow/core"
)

// StopRun 能取消本进程上的一条 flow 运行；注销之后就不再命中。
func TestStopRunCancelsExecContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ec := &flow.ExecContext{RunID: "run-flow-stop", StdCtx: ctx}
	ec.SetCancel(cancel)

	unreg := registerLocalRun(ec)
	defer unreg()

	if !StopRun("run-flow-stop") {
		t.Fatal("StopRun returned false, want true")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ExecContext 没有被取消")
	}

	// 不在这台的 run：返回 false，不能误伤别人
	if StopRun("run-elsewhere") {
		t.Fatal("StopRun on an unknown run returned true")
	}

	unreg()
	if StopRun("run-flow-stop") {
		t.Fatal("注销后仍能命中")
	}
}
