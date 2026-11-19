package executors

import (
	"context"
	"testing"
	"time"

	flow "flame/nexus/service/flow/core"
)

// StopRun cancels a flow run on this process; after unregistering it no longer matches.
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
		t.Fatal("the ExecContext was not cancelled")
	}

	// A run living elsewhere: returns false and must not hit someone else
	if StopRun("run-elsewhere") {
		t.Fatal("StopRun on an unknown run returned true")
	}

	unreg()
	if StopRun("run-flow-stop") {
		t.Fatal("it still matched after unregistering")
	}
}
