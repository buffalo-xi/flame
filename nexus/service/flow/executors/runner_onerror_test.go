package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// A code node whose template does not parse fails with `script` - a code on_error can act on, with no sandbox and no
// network involved. The declaration also carries skip / retry / handler and HasPost, so every branch is reachable.
func brokenCodeNode(onError map[string]any) *flow.Node {
	return newExecNode("2", "code", map[string]any{
		"code":     map[string]any{"lang": "template", "script": "{{"},
		"outputs":  map[string]any{"fixed": map[string]any{"type": "string"}},
		"on_error": onError,
	})
}

func attempts(ctx *flow.ExecContext) []*flow.NodeResult {
	return ctx.ExecutedProcessMap.Get("1")
}

// Retry runs the node again up to max times; each attempt is recorded (the last one wins in the node map), and the
// exhausted chain finally fails with the original code.
func TestExecuteNodeRetryExhausted(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		// A delay is what makes the retry visible in the log: the line is written on the attempt that is about to be
		// retried, so it stays on the record of that attempt.
		"retry": map[string]any{"codes": []any{"script"}, "max": float64(2), "delay_ms": float64(1)},
	})
	ctx := newContainerContext(t, nil)

	err := executeNode(node, ctx, "1")
	if err == nil {
		t.Fatal("executeNode must return the failure after the retries are exhausted")
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeScript {
		t.Errorf("error = %v, want the script code passed through", err)
	}

	got := attempts(ctx)
	if len(got) != 3 {
		t.Fatalf("attempts = %d, want 1 + 2 retries", len(got))
	}
	if got[2].RetryCount != 2 {
		t.Errorf("last attempt RetryCount = %d, want 2", got[2].RetryCount)
	}
	for i, want := range []string{"retry #1", "retry #2"} {
		if !strings.Contains(strings.Join(got[i].RunLogs, "\n"), want) {
			t.Errorf("attempt %d logs = %v, want %q logged on the attempt being retried", i, got[i].RunLogs, want)
		}
	}
}

// A retry config on a node that does not declare the action is ignored (with a warning): the declaration is what the
// editor's panel renders, so accepting more at run time would make the panel a lie.
func TestExecuteNodeIgnoresUndeclaredRetry(t *testing.T) {
	node := newExecNode("2", "result", map[string]any{
		"code":     map[string]any{"lang": "template", "script": "{{"},
		"on_error": map[string]any{"retry": map[string]any{"codes": []any{"script"}, "max": float64(2)}},
	})
	ctx := newContainerContext(t, nil)

	if err := executeNode(node, ctx, "1"); err == nil {
		t.Fatal("executeNode must return the failure")
	}
	got := attempts(ctx)
	if len(got) != 1 {
		t.Fatalf("attempts = %d, want 1: the result node declares no retry", len(got))
	}
	if !strings.Contains(strings.Join(got[0].RunLogs, "\n"), "does not declare a retry action") {
		t.Errorf("logs = %v, want the ignored config logged", got[0].RunLogs)
	}
}

// Skip swallows the failure: the node is marked skipped and the flow continues.
func TestExecuteNodeSkip(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		"skip": map[string]any{"codes": []any{"script"}},
	})
	ctx := newContainerContext(t, nil)

	if err := executeNode(node, ctx, "1"); err != nil {
		t.Fatalf("executeNode = %v, want the failure swallowed by skip", err)
	}
	rec := recordedNode(t, ctx, "2")
	if rec.Status != flow.StatusSkipped {
		t.Errorf("status = %v, want skipped", rec.Status)
	}
	if rec.ErrorAction != flow.ErrorActionSkip {
		t.Errorf("ErrorAction = %q, want skip", rec.ErrorAction)
	}
}

// A handler may decide at run time: returning action=skip has the same effect as the static skip rule.
func TestExecuteNodeHandlerSkips(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		"handler": map[string]any{
			"codes":  []any{"script"},
			"lang":   "template",
			"script": `{"action": "skip"}`,
		},
	})
	ctx := newContainerContext(t, nil)

	if err := executeNode(node, ctx, "1"); err != nil {
		t.Fatalf("executeNode = %v, want the handler's skip to end the node", err)
	}
	rec := recordedNode(t, ctx, "2")
	if rec.Status != flow.StatusSkipped || rec.ErrorAction != flow.ErrorActionSkip {
		t.Errorf("record = %v/%q, want skipped/skip", rec.Status, rec.ErrorAction)
	}
}

// A handler that returns no skip rewrites the error the caller sees (the returned values on the record are the
// authority, not the locals from before the handler ran).
func TestExecuteNodeHandlerRewritesTheError(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		"handler": map[string]any{
			"codes":  []any{"script"},
			"lang":   "template",
			"script": `{"error_message": "rewritten by the handler", "error_code": "other"}`,
		},
	})
	ctx := newContainerContext(t, nil)

	err := executeNode(node, ctx, "1")
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("executeNode = %v, want an ExecError", err)
	}
	if ee.Code != flow.ErrCodeOther || ee.Msg != "rewritten by the handler" {
		t.Errorf("error = %q/%q, want the handler's rewrite", ee.Code, ee.Msg)
	}
	if rec := recordedNode(t, ctx, "2"); rec.ErrMsg != "rewritten by the handler" {
		t.Errorf("record ErrMsg = %q, want the rewrite to land on the record", rec.ErrMsg)
	}
}

// A handler that cannot run leaves the original failure in place and says so in the log.
func TestExecuteNodeHandlerFailure(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		"handler": map[string]any{"codes": []any{"script"}, "lang": "template", "script": "{{"},
	})
	ctx := newContainerContext(t, nil)

	err := executeNode(node, ctx, "1")
	if err == nil {
		t.Fatal("executeNode must return the failure")
	}
	if !strings.Contains(err.Error(), "template") {
		t.Errorf("error = %v, want the original script failure", err)
	}
	if rec := recordedNode(t, ctx, "2"); !strings.Contains(strings.Join(rec.RunLogs, "\n"), "failed to run the handler script") {
		t.Errorf("logs = %v, want the handler failure logged", rec.RunLogs)
	}
}

// The post block is cleanup, not recovery: it runs on every failure path and writes the configured outputs even
// though the node stays failed.
func TestExecuteNodePostWritesOutputsOnFailure(t *testing.T) {
	node := brokenCodeNode(map[string]any{
		"post": map[string]any{"set_outputs": map[string]any{"fixed": "patched"}},
	})
	ctx := newContainerContext(t, nil)

	if err := executeNode(node, ctx, "1"); err == nil {
		t.Fatal("executeNode must return the failure")
	}
	rec := recordedNode(t, ctx, "2")
	if got, _ := rec.OutputVars["fixed"].(string); got != "patched" {
		t.Errorf("fixed = %v, want the post value written on failure", rec.OutputVars["fixed"])
	}
	if rec.Status != flow.StatusFailed {
		t.Errorf("status = %v, want failed: post does not make the node succeed", rec.Status)
	}
}

// An unknown node type is a diagram problem (a retired type still drawn on the canvas), reported as a config error
// that names the type.
func TestExecuteNodeUnknownType(t *testing.T) {
	node := &flow.Node{ID: "2", Type: "knowledge", Data: &flow.NodeData{Label: "knowledge"}}
	ctx := newContainerContext(t, nil)

	err := executeNode(node, ctx, "1")
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Fatalf("executeNode = %v, want a config error", err)
	}
	if !strings.Contains(ee.Msg, "has no executor") {
		t.Errorf("msg = %q, want it to name the missing executor", ee.Msg)
	}
}

// A run that is already cancelled records the node as cancelled (or timed out) and never calls the executor.
func TestExecuteNodePreCancelled(t *testing.T) {
	node := brokenCodeNode(nil)
	ctx := newContainerContext(t, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.StdCtx = cancelled

	err := executeNode(node, ctx, "1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeNode = %v, want the cancellation", err)
	}
	rec := recordedNode(t, ctx, "2")
	if rec.Status != flow.StatusCancelled || rec.ErrCode != flow.ErrCodeCancel {
		t.Errorf("record = %v/%q, want cancelled/cancel", rec.Status, rec.ErrCode)
	}
}

// recordedNode looks the node up the way the runner and the UI do: by processID_nodeID.
func recordedNode(t *testing.T, ctx *flow.ExecContext, nodeID string) *flow.NodeResult {
	t.Helper()
	nr, ok := ctx.ExecutedNodeMap.Get(fmt.Sprintf("1_%s", nodeID))
	if !ok || nr == nil {
		t.Fatalf("node %s was not recorded", nodeID)
	}
	return nr
}
