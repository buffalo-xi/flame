package executors

import (
	"context"
	"strings"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
	"flame/nexus/shared/runledger"
)

// The memory node's early exits are the ones that must not touch the database: without a session id, and with
// session memory switched off for the flow, it emits an empty message array and succeeds.
func TestMemoryExecutorWithoutSessionEmitsEmptyArray(t *testing.T) {
	node := newExecNode("2", "memory", map[string]any{})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&MemoryExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	// The write goes through SetVarByPath, which normalises the value to []any; the assertion only cares about
	// "an array with no message".
	if _, exists := nr.OutputVars["messages"]; !exists {
		t.Fatalf("OutputVars = %v, want the messages variable to be written", nr.OutputVars)
	}
	if msgs := toSlice(nr.OutputVars["messages"]); len(msgs) != 0 {
		t.Errorf("messages = %v, want no message", msgs)
	}
}

// A flow with session memory turned off never reads records, even when the run does carry a session id.
func TestMemoryExecutorWithoutSessionMemoryEmitsEmptyArray(t *testing.T) {
	node := newExecNode("2", "memory", map[string]any{})
	ctx := newExecContext()
	ctx.SessionID = "s1"
	ctx.Flow = &flow.Flow{Instance: &models.Flow{SessionMemory: false}}
	nr := newExecResult(node, ctx)
	if err := (&MemoryExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if msgs := toSlice(nr.OutputVars["messages"]); len(msgs) != 0 {
		t.Errorf("messages = %v, want no message", msgs)
	}
}

// The fixed `messages` variable is merged in, and a declaration the author made is kept next to it.
func TestMemoryExecutorDeclaresFixedMessages(t *testing.T) {
	node := newExecNode("2", "memory", map[string]any{
		"outputs": map[string]any{"extra": map[string]any{"type": "string"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&MemoryExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if _, ok := nr.OutputVars["messages"]; !ok {
		t.Errorf("OutputVars = %v, want the fixed messages variable", nr.OutputVars)
	}
	if _, ok := nr.OutputVars["extra"]; !ok {
		t.Errorf("OutputVars = %v, want the declared variable kept", nr.OutputVars)
	}
}

func TestMemoryExecutorMissingConfig(t *testing.T) {
	node := newExecNode("2", "memory", nil)
	node.Data = nil
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&MemoryExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without node data")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "node data or config is nil") {
		t.Errorf("ErrMsg = %q, want it to name the missing config", nr.ErrMsg)
	}
}

// The window only narrows the query when it is enabled with a positive value and a known unit; everything else
// means "no lower bound".
func TestTimeWindowDuration(t *testing.T) {
	cases := []struct {
		unit  string
		value int
		want  time.Duration
	}{
		{"second", 5, 5 * time.Second},
		{"minute", 2, 2 * time.Minute},
		{"hour", 1, time.Hour},
		{"day", 1, 24 * time.Hour},
		{"minute", 0, 0},
		{"minute", -1, 0},
		{"week", 1, 0},
		{"", 1, 0},
	}
	for _, tc := range cases {
		if got := timeWindowDuration(tc.unit, tc.value); got != tc.want {
			t.Errorf("timeWindowDuration(%q, %d) = %v, want %v", tc.unit, tc.value, got, tc.want)
		}
	}
}

// The local stop table is what a cross-instance stop signal lands on: an unknown run is ignored, a wrong kind is
// ignored, and an empty run id is never matched.
func TestStopRunEdgeCases(t *testing.T) {
	if StopRun("") {
		t.Error("StopRun(\"\") = true, want false")
	}
	if StopRun("never-registered") {
		t.Error("StopRun on an unknown run = true, want false")
	}

	// A context without a run id is not registered: there would be nothing to look it up by.
	unregNil := registerLocalRun(nil)
	unregEmpty := registerLocalRun(&flow.ExecContext{})
	unregNil()
	unregEmpty()
	if StopRun("") {
		t.Error("an unregistered empty run matched")
	}

	// Unregistering twice is idempotent (several exits call it).
	c, cancel := context.WithCancel(context.Background())
	ec := &flow.ExecContext{RunID: "run-idempotent", StdCtx: c}
	ec.SetCancel(cancel)
	unreg := registerLocalRun(ec)
	unreg()
	unreg()
	if StopRun("run-idempotent") {
		t.Error("the run was still registered after being unregistered twice")
	}
}

// Every instance receives every stop signal: the flow handler has to ignore the agent kind and match its own.
func TestHandleRunStopSignal(t *testing.T) {
	t.Run("flow kind cancels", func(t *testing.T) {
		c, cancel := context.WithCancel(context.Background())
		ec := &flow.ExecContext{RunID: "run-signal-flow", StdCtx: c}
		ec.SetCancel(cancel)
		defer registerLocalRun(ec)()

		handleRunStopSignal(runledger.StopSignal{Kind: runledger.KindFlow, RunID: "run-signal-flow"})
		select {
		case <-c.Done():
		case <-time.After(time.Second):
			t.Fatal("the run was not cancelled by the stop signal")
		}
	})

	t.Run("empty kind is treated as flow", func(t *testing.T) {
		c, cancel := context.WithCancel(context.Background())
		ec := &flow.ExecContext{RunID: "run-signal-nokind", StdCtx: c}
		ec.SetCancel(cancel)
		defer registerLocalRun(ec)()

		handleRunStopSignal(runledger.StopSignal{RunID: "run-signal-nokind"})
		select {
		case <-c.Done():
		case <-time.After(time.Second):
			t.Fatal("a signal without a kind must be handled as a flow stop")
		}
	})

	t.Run("another kind is ignored", func(t *testing.T) {
		c, cancel := context.WithCancel(context.Background())
		ec := &flow.ExecContext{RunID: "run-signal-agent", StdCtx: c}
		ec.SetCancel(cancel)
		defer registerLocalRun(ec)()

		handleRunStopSignal(runledger.StopSignal{Kind: runledger.KindAgent, RunID: "run-signal-agent"})
		select {
		case <-c.Done():
			t.Fatal("an agent stop signal must not cancel a flow run")
		case <-time.After(50 * time.Millisecond):
		}
	})
}
