package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// Control signals travel through the error interface but are normal control flow: each executor succeeds its
// own node and hands the signal to the enclosing loop / function / flow.
func TestControlSignalExecutors(t *testing.T) {
	cases := []struct {
		name     string
		exec     Executor
		signal   error
		nodeType string
	}{
		{"break", &BreakExecutor{}, SignalBreak, "break"},
		{"continue", &ContinueExecutor{}, SignalContinue, "continue"},
		{"return", &ReturnExecutor{}, SignalReturn, "return"},
		{"exit", &ExitExecutor{}, SignalExit, "exit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", tc.nodeType, map[string]any{})
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			err := tc.exec.Execute(node, ctx, "1", nr)
			if !errors.Is(err, tc.signal) {
				t.Fatalf("Execute = %v, want the control signal", err)
			}
			if !isControlSignal(err) {
				t.Error("isControlSignal does not recognise the signal: the runner would treat it as a failure")
			}
			if nr.Status != flow.StatusSuccess {
				t.Errorf("status = %v, want success", nr.Status)
			}
			if nr.ErrCode != "" {
				t.Errorf("ErrCode = %q, want empty", nr.ErrCode)
			}
		})
	}
}

func TestIsControlSignal(t *testing.T) {
	if isControlSignal(nil) {
		t.Error("nil is not a control signal")
	}
	if isControlSignal(errors.New("boom")) {
		t.Error("a plain error is not a control signal")
	}
	// Wrapped signals still count: containers add context layer by layer.
	if !isControlSignal(fmt.Errorf("loop body: %w", SignalBreak)) {
		t.Error("a wrapped signal was not recognised")
	}
}

// parseTimeoutConfig is shared by branch / loop / code / result / progress / wait / http, so the "off" cases are
// the contract: an absent or disabled block must mean "no node-level timeout", never 0 seconds.
func TestParseTimeoutConfig(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want int
	}{
		{"absent", nil, -1},
		{"not an object", "5", -1},
		{"disabled", map[string]any{"enabled": false, "seconds": float64(5)}, -1},
		{"enabled without seconds", map[string]any{"enabled": true}, -1},
		{"zero seconds", map[string]any{"enabled": true, "seconds": float64(0)}, -1},
		{"negative seconds", map[string]any{"enabled": true, "seconds": float64(-3)}, -1},
		{"string seconds", map[string]any{"enabled": true, "seconds": "5"}, -1},
		{"float seconds", map[string]any{"enabled": true, "seconds": float64(5)}, 5},
		{"int seconds", map[string]any{"enabled": true, "seconds": 7}, 7},
		{"int64 seconds", map[string]any{"enabled": true, "seconds": int64(9)}, 9},
		// The result is whole seconds: a fractional value is truncated, not rounded up.
		{"fractional seconds", map[string]any{"enabled": true, "seconds": float64(1.9)}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTimeoutConfig(tc.raw); got != tc.want {
				t.Errorf("parseTimeoutConfig(%#v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// A wait node without a fork id (or pointing at a fork that is not on this chain) is a graph configuration
// error, not a platform fault: reporting internal would send people to investigate the wrong layer.
func TestWaitExecutorConfigErrors(t *testing.T) {
	t.Run("missing fork id", func(t *testing.T) {
		node := newExecNode("2", "wait", map[string]any{})
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		if err := (&WaitExecutor{}).Execute(node, ctx, "1", nr); err == nil {
			t.Fatal("Execute must fail without fork_node_id")
		}
		if nr.ErrCode != flow.ErrCodeConfig {
			t.Errorf("ErrCode = %q, want config", nr.ErrCode)
		}
		if nr.WaitResult == nil || nr.WaitResult.ForkNodeID != "" {
			t.Errorf("WaitResult = %#v, want it initialised and empty", nr.WaitResult)
		}
	})

	t.Run("fork not on this chain", func(t *testing.T) {
		node := newExecNode("2", "wait", map[string]any{"fork_node_id": "f1"})
		ctx := newExecContext()
		ctx.ExecutedNodeMap = flow.NewNodeExecMap()
		nr := newExecResult(node, ctx)
		if err := (&WaitExecutor{}).Execute(node, ctx, "1", nr); err == nil {
			t.Fatal("Execute must fail when the fork result is missing")
		}
		if nr.ErrCode != flow.ErrCodeConfig {
			t.Errorf("ErrCode = %q, want config", nr.ErrCode)
		}
		if !strings.Contains(nr.ErrMsg, "fork node result not found: f1") {
			t.Errorf("ErrMsg = %q, want it to name the fork", nr.ErrMsg)
		}
	})
}

// The happy path: every concurrent member finished, none of them failed, so the wait node succeeds.
func TestWaitExecutorSucceedsWhenAllMembersFinish(t *testing.T) {
	ctx, _, fr := newForkScaffold(t, "")
	node := newExecNode("2", "wait", map[string]any{"fork_node_id": "f1"})
	nr := newExecResult(node, ctx)
	fr.Done()

	if err := (&WaitExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.WaitResult == nil || nr.WaitResult.ForkNodeID != "f1" {
		t.Errorf("WaitResult = %#v, want the fork id recorded", nr.WaitResult)
	}
}

// A failed member fails the wait (fail-fast): the wait node does not hide a broken concurrent branch.
func TestWaitExecutorFailsOnFailedMember(t *testing.T) {
	ctx, _, fr := newForkScaffold(t, flow.StatusFailed)
	node := newExecNode("2", "wait", map[string]any{"fork_node_id": "f1"})
	nr := newExecResult(node, ctx)
	fr.Done()

	if err := (&WaitExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when a member failed")
	}
	if nr.ErrCode != flow.ErrCodeOther {
		t.Errorf("ErrCode = %q, want other", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "concurrent branch failed") {
		t.Errorf("ErrMsg = %q, want it to report the failed branch", nr.ErrMsg)
	}
}

// The same fork can be waited on once: a second wait is a graph structure error (a duplicate edge), reported as
// a configuration problem rather than a platform failure.
func TestWaitExecutorRejectsDuplicateWait(t *testing.T) {
	ctx, _, fr := newForkScaffold(t, "")
	node := newExecNode("2", "wait", map[string]any{"fork_node_id": "f1"})
	fr.Done()

	first := newExecResult(node, ctx)
	if err := (&WaitExecutor{}).Execute(node, ctx, "1", first); err != nil {
		t.Fatalf("first wait = %v", err)
	}
	second := newExecResult(node, ctx)
	if err := (&WaitExecutor{}).Execute(node, ctx, "1", second); err == nil {
		t.Fatal("a second wait on the same fork must fail")
	}
	if second.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", second.ErrCode)
	}
}

// A cancelled run stops the wait and reports it as a cancellation, so the caller's on_error does not treat it as
// a wait timeout.
func TestWaitExecutorCancelled(t *testing.T) {
	ctx, _, fr := newForkScaffold(t, "")
	node := newExecNode("2", "wait", map[string]any{"fork_node_id": "f1"})
	nr := newExecResult(node, ctx)
	// The member never finishes, so only the cancellation can end the wait.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.StdCtx = cancelled

	err := (&WaitExecutor{}).Execute(node, ctx, "1", nr)
	if err == nil {
		t.Fatal("Execute must return the cancellation")
	}
	if nr.Status != flow.StatusCancelled {
		t.Errorf("status = %v, want cancelled", nr.Status)
	}
	if nr.ErrCode != flow.ErrCodeCancel {
		t.Errorf("ErrCode = %q, want cancel", nr.ErrCode)
	}
	fr.Done()
}

// newForkScaffold builds a wait node's prerequisites: an executed fork node result carrying a group with one
// member, and an optional status for that member's node result.
func newForkScaffold(t *testing.T, memberStatus flow.NodeStatus) (*flow.ExecContext, *flow.NodeResult, *flow.ForkResult) {
	t.Helper()
	ctx := newExecContext()
	ctx.ExecutedNodeMap = flow.NewNodeExecMap()

	fr := flow.NewForkResult(0)
	if err := fr.Add(1, context.Background(), flow.ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("ForkResult.Add = %v", err)
	}
	forkNR := flow.NewNodeResult(&flow.Node{ID: "f1", Type: "fork"}, "1", ctx)
	forkNR.ForkResult = fr
	ctx.ExecutedNodeMap.Set("1_f1", forkNR)

	if memberStatus != "" {
		memberNR := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ctx)
		memberNR.Status = memberStatus
		memberNR.ErrMsg = "member failed"
		ctx.ExecutedNodeMap.Set("p1_n1", memberNR)
	}
	return ctx, forkNR, fr
}
