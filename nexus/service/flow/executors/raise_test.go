package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

func raiseNode(raiseCfg map[string]any, inputs map[string]any) *flow.Node {
	cfg := map[string]any{"raise": raiseCfg}
	if inputs != nil {
		cfg["inputs"] = inputs
	}
	return newExecNode("2", "raise", cfg)
}

// The message is a template over config.inputs: the point of the node is a reason an operator can act on, so a literal
// string would force the author to build the same text in an assign first.
func TestRaiseExecutorRendersMessage(t *testing.T) {
	node := raiseNode(
		map[string]any{"code": "not_found", "message": "order {{ .input.order_id }} not found"},
		map[string]any{"order_id": map[string]any{"type": "string", "source_type": "flow", "source_key": "order_id"}},
	)
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]interface{}{"order_id": "A-1"}
	nr := newExecResult(node, ctx)

	err := (&RaiseExecutor{}).Execute(node, ctx, "1", nr)
	if err == nil {
		t.Fatal("Execute must fail")
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "not_found" {
		t.Errorf("error = %v, want the raised code not_found", err)
	}
	if nr.Status != flow.StatusFailed {
		t.Errorf("status = %q, want failed", nr.Status)
	}
	if nr.ErrMsg != "order A-1 not found" {
		t.Errorf("ErrMsg = %q, want the rendered message", nr.ErrMsg)
	}
}

// config / internal are bypassed by shouldBypassOnError, so raising one would look catchable and silently not be; cancel
// would record a failure whose code claims the user stopped the run. All three are refused rather than downgraded.
func TestRaiseExecutorRefusesReservedAndUnknownCodes(t *testing.T) {
	for _, code := range []string{"config", "internal", "cancel", "bogus", ""} {
		t.Run(code, func(t *testing.T) {
			node := raiseNode(map[string]any{"code": code, "message": "boom"}, nil)
			ctx := newExecContext()
			nr := newExecResult(node, ctx)

			err := (&RaiseExecutor{}).Execute(node, ctx, "1", nr)
			if err == nil {
				t.Fatalf("Execute must refuse code %q", code)
			}
			var ee *ExecError
			if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
				t.Errorf("error = %v, want a config error", err)
			}
			if !strings.Contains(nr.ErrMsg, "not raisable") {
				t.Errorf("ErrMsg = %q, want it to explain why the code is refused", nr.ErrMsg)
			}
		})
	}
}

func TestRaiseExecutorRequiresConfigBlockAndMessage(t *testing.T) {
	cases := []struct {
		name string
		node *flow.Node
		want string
	}{
		{"no raise block", newExecNode("2", "raise", map[string]any{}), "missing config.raise"},
		{"no message", raiseNode(map[string]any{"code": "other", "message": "  "}, nil), "missing config.raise.message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newExecContext()
			nr := newExecResult(tc.node, ctx)
			err := (&RaiseExecutor{}).Execute(tc.node, ctx, "1", nr)
			if err == nil {
				t.Fatal("Execute must fail")
			}
			if !strings.Contains(nr.ErrMsg, tc.want) {
				t.Errorf("ErrMsg = %q, want it to mention %q", nr.ErrMsg, tc.want)
			}
		})
	}
}

// GetVariables is all-or-nothing. Rendering with the surviving half would print a sentence with a hole in it into the
// error an operator reads, which is exactly the kind of silent wrong value the node exists to prevent.
func TestRaiseExecutorFailsOnUnresolvableInputs(t *testing.T) {
	node := raiseNode(
		map[string]any{"code": "other", "message": "boom {{ .input.x }}"},
		map[string]any{"x": map[string]any{"type": "string", "source_type": "", "source_key": ""}},
	)
	ctx := newExecContext()
	nr := newExecResult(node, ctx)

	err := (&RaiseExecutor{}).Execute(node, ctx, "1", nr)
	if err == nil {
		t.Fatal("Execute must fail when an input cannot be resolved")
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Errorf("error = %v, want a config error", err)
	}
	if !strings.Contains(nr.ErrMsg, "failed to read the input variables") {
		t.Errorf("ErrMsg = %q, want the input failure, not a rendered hole", nr.ErrMsg)
	}
}

// No declared codes (the author picks, so a list would rewrite their choice to "other"), no actions and no post: the
// failure is authored, so the catch point is the enclosing host or the call node.
func TestRaiseExecutorDeclaresNothingButPassThrough(t *testing.T) {
	ex, ok := GetExecutor("raise")
	if !ok {
		t.Fatal("raise is not registered")
	}
	caps := ex.Capabilities()
	if len(caps.ErrorCodes) != 0 {
		t.Errorf("ErrorCodes = %v, want none so every code passes through", caps.ErrorCodes)
	}
	if len(caps.Actions) != 0 {
		t.Errorf("Actions = %v, want none (a catch belongs to the caller)", caps.Actions)
	}
	if caps.HasPost {
		t.Error("HasPost = true, want false: nothing downstream reads the outputs of a node that always fails")
	}
	if len(caps.FixedOutputs) != 0 {
		t.Errorf("FixedOutputs = %v, want none", caps.FixedOutputs)
	}
}

// A raise at the end of a concurrent body's chain is the case where the aggregate join could have flattened the code: the
// members succeed, the body's own chain fails after them, and the host has to carry the raised code rather than "other".
// The join only overrides the body's error when the body itself succeeded (process.go), so this pins that down.
func TestProcessBodyTailRaiseKeepsItsCode(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 10})
	tail := newExecNode("r1", "raise", map[string]any{
		"raise": map[string]any{"code": "not_found", "message": "order gone"},
	})
	host, ctx := buildHost(t, "p1", map[string]any{"max_concurrency": 1}, []*flow.Node{m1, tail})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want the body tail's raise to fail the host")
	}
	if nr.Status != flow.StatusFailed || nr.ErrCode != "not_found" {
		t.Errorf("status/code = %q/%q, want failed/not_found", nr.Status, nr.ErrCode)
	}
	// The host's ErrMsg is what an on_error handler reads as error_message and what the run record shows for the
	// container: it has to name the child instead of only repeating its sentence.
	if !strings.Contains(nr.ErrMsg, "node r1:") {
		t.Errorf("ErrMsg = %q, want it to name the failing child (node r1)", nr.ErrMsg)
	}
}

// A loop rewrites the per-round failure as "iteration N failed: ...", and it used to stop there: the message named the
// round but not the node, which is the part an author needs.
func TestFailIterationNamesTheFailingNode(t *testing.T) {
	node := newExecNode("l1", "loop", nil)
	ctx := newExecContext()
	nr := newExecResult(node, ctx)

	if err := failIteration(nr, 2, &ExecError{Code: "not_found", Msg: "order gone", NodeID: "r1"}); err == nil {
		t.Fatal("failIteration = nil, want an error")
	}
	if nr.ErrCode != "not_found" {
		t.Errorf("ErrCode = %q, want the child's code", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "iteration 2 failed: node r1: order gone") {
		t.Errorf("ErrMsg = %q, want the round and the failing node", nr.ErrMsg)
	}
}

// The same contract as TestExecuteNodeIgnoresUndeclaredRetry for result: a skip block on a node that does not declare it
// is dropped, so the raise still fails. Without this, allowing skip would turn the node into an assign that writes
// nothing - the one thing it must not be.
func TestExecuteNodeDropsUndeclaredSkipOnRaise(t *testing.T) {
	node := raiseNode(map[string]any{"code": "not_found", "message": "gone"}, nil)
	node.Data.Config["on_error"] = map[string]any{"skip": map[string]any{"codes": []any{"not_found"}}}
	ctx := newContainerContext(t, nil)

	if err := executeNode(node, ctx, "1"); err == nil {
		t.Fatal("executeNode must fail: the skip block is not declared by raise")
	}
	got := attempts(ctx)
	if len(got) != 1 {
		t.Fatalf("attempts = %d, want 1", len(got))
	}
	if got[0].Status != flow.StatusFailed || got[0].ErrorAction == flow.ErrorActionSkip {
		t.Errorf("status/action = %q/%q, want failed with no skip", got[0].Status, got[0].ErrorAction)
	}
}
