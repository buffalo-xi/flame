package executors

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The fixed output variables are written by the executor, so a script returning the same key must be ignored:
// string means append, and writing on both sides would mix "the final result" with "a fragment the script
// stitched on", leaving no answer to what the variable actually is.
func TestApplyHandlerAppendDeniesFixedOutputVars(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "rpc"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	nr.InitOutputVars(map[string]interface{}{
		"result": map[string]interface{}{"type": "string"},
		"note":   map[string]interface{}{"type": "string"},
	})

	if err := applyHandlerAppend(nr, map[string]any{
		"result": "what the script wanted to write",
		"note":   "fragment",
	}, rpcReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend failed: %v", err)
	}

	if v, _ := nr.GetVariable("result"); v != "" {
		t.Errorf("result = %v, should stay empty (a fixed variable is off-limits to scripts)", v)
	}
	if v, _ := nr.GetVariable("note"); v != "fragment" {
		t.Errorf("note = %v, want fragment (a custom variable should be writable)", v)
	}
}

// A per-frame call hits the same conflict repeatedly, so the log has to be deduplicated: 500 frames must not
// flood 500 lines.
func TestApplyHandlerAppendWarnsOncePerVar(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "rpc"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	nr.InitOutputVars(map[string]interface{}{
		"result": map[string]interface{}{"type": "string"},
	})

	for i := 0; i < 5; i++ {
		if err := applyHandlerAppend(nr, map[string]any{"result": "x"}, rpcReservedOutputVars); err != nil {
			t.Fatalf("applyHandlerAppend failed: %v", err)
		}
	}

	warned := 0
	for _, line := range nr.GetLogs() {
		if strings.Contains(line, "ignored") {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("%d warnings were logged for the fixed variable, want 1 (one hint per variable per run)", warned)
	}
}

// The fixed variables no longer depend on "is there a script": they are written with or without one.
// When the script fails the already written result should still be visible (the write happens before the
// script).
func TestFinishRpcResultWritesFixedVarsRegardlessOfHandler(t *testing.T) {
	newNR := func() *flow.NodeResult {
		node := &flow.Node{ID: "n1", Type: "rpc"}
		nr := flow.NewNodeResult(node, "1", &flow.ExecContext{StdCtx: context.Background()})
		nr.InitOutputVars(map[string]interface{}{
			"result":   map[string]interface{}{"type": "string"},
			"progress": map[string]interface{}{"type": "array[string]"},
			"error":    map[string]interface{}{"type": "string"},
		})
		return nr
	}

	t.Run("result is written without a script", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		if err := e.finishRpcResult(nr.Ctx, nr, nil, false, "", "", 0, "hello", false, nil, nil); err != nil {
			t.Fatalf("finishRpcResult: %v", err)
		}
		if v, _ := nr.GetVariable("result"); v != "hello" {
			t.Errorf("result = %v, want hello", v)
		}
		if v, _ := nr.GetVariable("error"); v != "" {
			t.Errorf("error = %v, should be empty", v)
		}
	})

	t.Run("a remote business error writes error rather than result", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		if err := e.finishRpcResult(nr.Ctx, nr, nil, false, "", "", 0, "boom", true, nil, nil); err != nil {
			t.Fatalf("finishRpcResult: %v", err)
		}
		if v, _ := nr.GetVariable("error"); v != "boom" {
			t.Errorf("error = %v, want boom", v)
		}
		if v, _ := nr.GetVariable("result"); v != "" {
			t.Errorf("result = %v, should be empty", v)
		}
	})

	t.Run("result is still written when there is a script", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		// The test environment has no script client, so the handler necessarily fails; what is asserted here is
		// the order: the fixed variables are written before the script, and a failing script does not undo the
		// result that already landed.
		_ = e.finishRpcResult(nr.Ctx, nr, nil, true, "lua", "return {}", 0, "hello", false, nil, []string{})
		if v, _ := nr.GetVariable("result"); v != "hello" {
			t.Errorf("result = %v, want hello (the fixed variables are written before the script)", v)
		}
	})
}
