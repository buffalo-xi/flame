package executors

import (
	"testing"

	"flame/nexus/service/flow/core"
)

// A fixed output variable must not depend on whether the frontend configured it: when one mode forgets
// reasoning_content, the executor's write fails with "output var reasoning_content not defined" and fails the
// node.
func TestEnsureFixedOutputsFillsMissing(t *testing.T) {
	outputs := map[string]interface{}{
		"content": map[string]interface{}{"type": "string"},
		"answer":  map[string]interface{}{"type": "string"},
	}

	merged := ensureFixedOutputs(outputs, llmFixedOutputVars)

	for _, name := range []string{"content", "reasoning_content", "tool_calls"} {
		if _, ok := merged[name]; !ok {
			t.Errorf("merged is missing the fixed variable %s", name)
		}
	}
	if _, ok := merged["answer"]; !ok {
		t.Error("merged lost the custom variable answer")
	}
	if _, ok := outputs["reasoning_content"]; ok {
		t.Error("ensureFixedOutputs must not modify the map the caller passed in")
	}
}

// A script returning a fixed variable must be ignored: content means append, and stitching once more after the
// executor wrote it would produce two copies, leaving "the final result" without a single answer.
func TestApplyHandlerAppendDeniesLLMFixedOutputs(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "llm"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	merged := ensureFixedOutputs(nil, llmFixedOutputVars)
	merged["answer"] = map[string]interface{}{"type": "string"}
	nr.InitOutputVars(merged)

	if err := applyHandlerAppend(nr, map[string]any{
		"content":           "text the script wanted to append",
		"reasoning_content": "reasoning the script wanted to append",
		"answer":            "script result",
	}, llmReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend failed: %v", err)
	}

	if v, _ := nr.GetVariable("content"); v != "" {
		t.Errorf("content = %v, should stay empty (a fixed variable is off-limits to scripts)", v)
	}
	if v, _ := nr.GetVariable("reasoning_content"); v != "" {
		t.Errorf("reasoning_content = %v, should stay empty (a fixed variable is off-limits to scripts)", v)
	}
	if v, _ := nr.GetVariable("answer"); v != "script result" {
		t.Errorf("answer = %v, want script result (a custom variable should be writable)", v)
	}
}
