package executors

import (
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
)

// An old node may not declare every HTTP fixed variable (stream_finished, say), so the executor has to top them up
// before writing — otherwise the write fails outright and the node is failed with it.
func TestEnsureFixedOutputsFillsHTTPVars(t *testing.T) {
	merged := ensureFixedOutputs(map[string]interface{}{
		"body": map[string]interface{}{"type": "string"},
	}, httpFixedOutputVars)

	for _, name := range []string{"mode", "status", "headers", "body", "sse_events", "stream_finished"} {
		if _, ok := merged[name]; !ok {
			t.Errorf("merged is missing the fixed variable %s", name)
		}
	}
}

// A script returning a fixed variable must be ignored: body means append, and stitching once more after the
// executor wrote it would produce two copies.
func TestApplyHandlerAppendDeniesHTTPFixedOutputs(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "http"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	merged := ensureFixedOutputs(nil, httpFixedOutputVars)
	merged["text"] = map[string]interface{}{"type": "string"}
	nr.InitOutputVars(merged)

	if err := applyHandlerAppend(nr, map[string]any{
		"body":   "response body the script wanted to append",
		"status": float64(500),
		"text":   "script result",
	}, httpReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend failed: %v", err)
	}

	if v, _ := nr.GetVariable("body"); v != "" {
		t.Errorf("body = %v, should stay empty (a fixed variable is off-limits to scripts)", v)
	}
	if v, _ := nr.GetVariable("status"); fmt.Sprint(v) != "0" {
		t.Errorf("status = %v, should keep its initial value 0 (a fixed variable is off-limits to scripts)", v)
	}
	if v, _ := nr.GetVariable("text"); v != "script result" {
		t.Errorf("text = %v, want script result (a custom variable should be writable)", v)
	}
}
