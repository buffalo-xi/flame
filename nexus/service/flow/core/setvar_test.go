package flow

import (
	"strings"
	"testing"
)

// Run-context variables (builtin / env / argument) are read-only inputs of this run; a node must not
// use them as an assignment target. That has to be rejected explicitly instead of falling through to
// "unsupported target type".
func TestSetVariableRejectsReadOnlySources(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}}

	for _, src := range []string{"builtin", "env", "argument"} {
		err := ctx.SetVariable(map[string]interface{}{
			"source_type": src,
			"source_key":  "session_id",
		}, "rewritten", "")
		if err == nil {
			t.Fatalf("writing through the %s source must be rejected", src)
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Errorf("the %s error should say read-only, got: %v", src, err)
		}
	}
}

// Without a selected target, say so explicitly instead of an obscure error about an empty source_type.
func TestSetVariableRejectsEmptyTargetType(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}}
	err := ctx.SetVariable(map[string]interface{}{"source_key": "somewhere"}, "v", "")
	if err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("err = %v, want one containing \"not selected\"", err)
	}
}

// Flow variables are writable at run time and must not be caught by the two rules above.
func TestSetVariableWritesFlowVar(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{"k": "old"}}
	if err := ctx.SetVariable(map[string]interface{}{
		"source_type": "flow",
		"source_key":  "k",
	}, "new", ""); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	if v, _ := GetVarByPath(ctx.RunFlowVars, "k"); v != "new" {
		t.Errorf("k = %v, want new", v)
	}
}
