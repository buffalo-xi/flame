package executors

import (
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
)

// A while condition has to see the values as they are now: the body writes its result back and the next round's
// condition reads it. Reading the startup snapshot would pin the condition to the first value forever, so the loop would
// either stop before the first round or run to max_times.
func TestLoopConditionRereadsInputsEachRound(t *testing.T) {
	node := newExecNode("2", "loop", map[string]any{
		"inputs": map[string]any{
			"acc": map[string]any{"type": "number", "source_type": "flow", "source_key": "limit"},
		},
		"loop": map[string]any{"type": "while", "max_times": 5},
	})
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]interface{}{"limit": 10}
	nr := newExecResult(node, ctx)
	inputsCfg, _ := node.Data.Config["inputs"].(map[string]interface{})

	// What Execute does once, before the loop starts
	snapshot, err := ctx.GetVariables(inputsCfg, "1")
	if err != nil {
		t.Fatalf("GetVariables = %v", err)
	}
	nr.SetInputVars(snapshot)

	got, err := conditionInputs(ctx, nr, inputsCfg, "1")
	if err != nil {
		t.Fatalf("conditionInputs = %v", err)
	}
	if fmt.Sprint(got["acc"]) != "10" {
		t.Errorf("acc = %v, want 10", got["acc"])
	}

	// The body moves the source value; the stored snapshot still says 10
	ctx.RunFlowVars["limit"] = 1
	got, err = conditionInputs(ctx, nr, inputsCfg, "1")
	if err != nil {
		t.Fatalf("conditionInputs = %v", err)
	}
	if fmt.Sprint(got["acc"]) != "1" {
		t.Errorf("acc = %v, want the re-read value 1", got["acc"])
	}
	if fmt.Sprint(nr.GetInputVars()["acc"]) != "10" {
		t.Error("the startup snapshot was modified; the run record must keep what was handed in")
	}
}

// A body writes the loop's own outputs through targets, and the condition reads them without the author declaring them in
// config.inputs: that declaration cannot be satisfied before the node runs.
func TestLoopConditionSeesOwnOutputs(t *testing.T) {
	node := newExecNode("2", "loop", map[string]any{
		"outputs": map[string]any{"total": map[string]any{"type": "number"}},
		"loop":    map[string]any{"type": "while", "max_times": 3},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := nr.SetOutputVar("total", 5); err != nil {
		t.Fatalf("SetOutputVar = %v", err)
	}

	got, err := conditionInputs(ctx, nr, nil, "1")
	if err != nil {
		t.Fatalf("conditionInputs = %v", err)
	}
	if fmt.Sprint(got["total"]) != "5" {
		t.Errorf("total = %v, want the loop's own output 5", got["total"])
	}
}

// Loop variables win over an input declared with the same name: index / item / total are what the author means by those
// names inside a loop.
func TestLoopConditionLoopVarsWinOverInputs(t *testing.T) {
	node := newExecNode("2", "loop", map[string]any{
		"inputs": map[string]any{
			"index": map[string]any{"type": "number", "source_type": "flow", "source_key": "idx"},
		},
		"loop": map[string]any{"type": "while", "max_times": 3},
	})
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]interface{}{"idx": 10}
	nr := newExecResult(node, ctx)
	nr.LoopResult = &flow.LoopResult{LoopVars: map[string]interface{}{"index": 0}}

	inputsCfg, _ := node.Data.Config["inputs"].(map[string]interface{})
	got, err := conditionInputs(ctx, nr, inputsCfg, "1")
	if err != nil {
		t.Fatalf("conditionInputs = %v", err)
	}
	if fmt.Sprint(got["index"]) != "0" {
		t.Errorf("index = %v, want the loop variable 0 rather than the declared input 10", got["index"])
	}
}
