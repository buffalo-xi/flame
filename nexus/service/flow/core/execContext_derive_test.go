package flow

import (
	"context"
	"sync"
	"testing"
)

// A derived context belongs to the same run: a variable written by the member is visible to the walk and the other way
// round. That is the whole reason the store and its lock are shared by pointer.
func TestWithContextSharesTheVariableStore(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}, VarsMu: &sync.RWMutex{}}
	member := ctx.WithContext(context.Background())

	source := map[string]interface{}{"source_type": "flow", "source_key": "answer"}
	if err := member.SetVariable(source, "42", "1"); err != nil {
		t.Fatalf("member SetVariable = %v", err)
	}
	if got, err := ctx.GetVariable(source, "1"); err != nil || got != "42" {
		t.Fatalf("walk GetVariable = %v, %v; want 42: the member and the walk share one store", got, err)
	}

	chainSource := map[string]interface{}{"source_type": "flow", "source_key": "chain"}
	if err := ctx.SetVariable(chainSource, "x", "1"); err != nil {
		t.Fatalf("walk SetVariable = %v", err)
	}
	if got, err := member.GetVariable(chainSource, "1"); err != nil || got != "x" {
		t.Fatalf("member GetVariable = %v, %v; want x", got, err)
	}
}

// The run output is per-context (a slice header cannot be shared), so a member accumulates into its own record and the
// caller folds it back once the member is done. Without that fold the member's progress and result text would be lost.
func TestWithContextFoldsRunOutputBack(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}, VarsMu: &sync.RWMutex{}}
	member := ctx.WithContext(context.Background())

	member.SendProgress("member progress")
	member.EmitResult("member result", false)
	member.SetError("member error")
	if len(ctx.ProgressMsgs) != 0 || ctx.GetResult() != "" || ctx.GetError() != "" {
		t.Fatal("a derived context must accumulate into its own record, not the parent's")
	}

	ctx.MergeFrom(member)
	if len(ctx.ProgressMsgs) != 1 || ctx.ProgressMsgs[0] != "member progress" {
		t.Errorf("ProgressMsgs = %v, want the member's progress folded back", ctx.ProgressMsgs)
	}
	if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "member result" {
		t.Errorf("ResultMsgs = %v, want the member's result folded back", ctx.ResultMsgs)
	}
	if got := ctx.GetResult(); got != "member result" {
		t.Errorf("Result = %q, want the member's result folded back", got)
	}
	if got := ctx.GetError(); got != "member error" {
		t.Errorf("Error = %q, want the member's error folded back", got)
	}
}

// Only the scope's own cancel func stops a member: ExecContext.Cancel (the debug stop / end-of-run hook) belongs to the
// root, and a derived context carrying it would let one member cancel the whole flow.
func TestWithContextCannotCancelTheRun(t *testing.T) {
	ctx := &ExecContext{StdCtx: context.Background(), RunFlowVars: map[string]interface{}{}}
	derived, cancelMember := context.WithCancel(context.Background())
	member := ctx.WithContext(derived)

	member.Cancel()
	if err := member.StdCtx.Err(); err != nil {
		t.Errorf("derived StdCtx = %v, want it untouched by ExecContext.Cancel", err)
	}
	if err := ctx.StdCtx.Err(); err != nil {
		t.Errorf("root StdCtx = %v, want the run unaffected", err)
	}

	// The scope holds the cancel func of the derived context; that is the one that stops the member.
	cancelMember()
	if err := member.StdCtx.Err(); err == nil {
		t.Error("the derived StdCtx ignored the scope's cancel func")
	}
	if err := ctx.StdCtx.Err(); err != nil {
		t.Errorf("root StdCtx = %v, want the run unaffected by a member cancellation", err)
	}
}
