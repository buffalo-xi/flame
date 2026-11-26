package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
)

// The template language renders in pure Go and then decodes the text as JSON, so a code node's whole
// "return value -> declared variable" dispatch is testable without a sandbox. `lang: template` is the one
// path where a code node never reaches globalCodeClient.
func TestCodeExecutorTemplateDispatchesDeclaredVars(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code": map[string]any{
			"lang":   "template",
			"script": `{"total": 42, "note": "hi"}`,
		},
		"outputs": map[string]any{"total": map[string]any{"type": "number"}},
		"locals":  map[string]any{"note": map[string]any{"type": "string"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Fatalf("status = %v, want success", nr.Status)
	}
	if got := fmt.Sprint(nr.OutputVars["total"]); got != "42" {
		t.Errorf("output total = %v, want 42", nr.OutputVars["total"])
	}
	if got, _ := nr.LocalVars["note"].(string); got != "hi" {
		t.Errorf("local note = %v, want hi", nr.LocalVars["note"])
	}
}

// An undeclared key is a fail-fast error rather than a silent drop: a mistyped variable name has to surface
// at the node that returned it.
func TestCodeExecutorUndeclaredVariableFails(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code": map[string]any{"lang": "template", "script": `{"nope": 1}`},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for an undeclared return key")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "undeclared variable: nope") {
		t.Errorf("ErrMsg = %q, want it to name the key", nr.ErrMsg)
	}
}

// The three declaration sets are pairwise disjoint; a name in two of them is a configuration error, reported
// before the script's keys are dispatched.
func TestCodeExecutorConflictingDeclarationsFail(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code":    map[string]any{"lang": "template", "script": `{}`},
		"outputs": map[string]any{"x": map[string]any{"type": "string"}},
		"locals":  map[string]any{"x": map[string]any{"type": "string"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when a name is declared in both outputs and locals")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "both outputs and locals") {
		t.Errorf("ErrMsg = %q, want it to name the conflict", nr.ErrMsg)
	}
}

// The return value has to be a JSON object: anything else cannot be dispatched onto declared variables, and
// guessing (wrapping it, say) would hide a wrong script.
func TestCodeExecutorNonMapReturnFails(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code": map[string]any{"lang": "template", "script": `"just text"`},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for a non-object return value")
	}
	if nr.ErrCode != flow.ErrCodeScript {
		t.Errorf("ErrCode = %q, want script", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "unexpected return type") {
		t.Errorf("ErrMsg = %q, want it to report the type mismatch", nr.ErrMsg)
	}
}

// A null return is the "script has no output" case, not an error: it succeeds without writing anything.
func TestCodeExecutorNullReturnSucceeds(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code":    map[string]any{"lang": "template", "script": `null`},
		"outputs": map[string]any{"total": map[string]any{"type": "number"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if got := fmt.Sprint(nr.OutputVars["total"]); got != "0" {
		t.Errorf("output total = %v, want the declared default 0", nr.OutputVars["total"])
	}
}

// A target writes outside the node, into a flow variable, so it needs the flow's node map to read the target
// definition - the part of the scaffolding a bare newExecResult does not provide.
func TestCodeExecutorTargetsWritesFlowVar(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code": map[string]any{"lang": "template", "script": `{"total": 7}`},
		"targets": map[string]any{
			"total": map[string]any{"source_type": "flow", "source_key": "total"},
		},
	})
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]any{}
	ctx.Flow = &flow.Flow{NodeMap: map[string]*flow.Node{"2": node}}
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if got := fmt.Sprint(ctx.RunFlowVars["total"]); got != "7" {
		t.Errorf("flow var total = %v, want 7", ctx.RunFlowVars["total"])
	}
}

func TestCodeExecutorInvalidTargetDefFails(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"code":    map[string]any{"lang": "template", "script": `{"total": 7}`},
		"targets": map[string]any{"total": "not-a-map"},
	})
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]any{}
	ctx.Flow = &flow.Flow{NodeMap: map[string]*flow.Node{"2": node}}
	nr := newExecResult(node, ctx)
	if err := (&CodeExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for a target definition that is not an object")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
}

// The platform capability gate sits in ExecuteScript because every script entry funnels into it: template
// returns before the gate (pure Go rendering, no sandbox), every other language has to pass.
func TestExecuteScriptCapabilityGate(t *testing.T) {
	restore := capabilitySnapshot
	t.Cleanup(func() { capabilitySnapshot = restore })

	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "code", map[string]any{}), ctx)
	ctx.Schema = "tenant-denied"
	capabilitySnapshot = func(string) map[string]bool { return map[string]bool{} }

	_, err := ExecuteScript(context.Background(), "lua", "return 1", nil, nr, 30)
	var capErr *capabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("ExecuteScript = %v, want a capability error", err)
	}

	// template never reaches the gate, even for a tenant that has no code capability
	got, err := ExecuteScript(context.Background(), "template", `"rendered"`, nil, nr, 30)
	if err != nil {
		t.Fatalf("template ExecuteScript = %v", err)
	}
	if s, _ := got.(string); s != "rendered" {
		t.Errorf("template result = %v, want rendered", got)
	}

	// granted: the call goes through and stops at the (unset) client instead
	capabilitySnapshot = func(string) map[string]bool { return map[string]bool{capability.Code: true} }
	_, err = ExecuteScript(context.Background(), "lua", "return 1", nil, nr, 30)
	if err == nil || !strings.Contains(err.Error(), "code client not initialized") {
		t.Errorf("ExecuteScript with the capability granted = %v, want the missing-client error", err)
	}

	// an empty schema means "no tenant policy": the gate passes, same as the nil-caps rule in toolgrant
	ctx.Schema = ""
	_, err = ExecuteScript(context.Background(), "lua", "return 1", nil, nr, 30)
	if err == nil || !strings.Contains(err.Error(), "code client not initialized") {
		t.Errorf("ExecuteScript without a tenant = %v, want the missing-client error", err)
	}
}
