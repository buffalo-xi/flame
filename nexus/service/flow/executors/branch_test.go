package executors

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// A branch condition is an ordinary script, so `lang: template` keeps the whole walk in pure Go: no sandbox,
// no child run. These cases stop before a matched branch actually executes (that needs the runner).
func TestBranchExecutorNoMatchSucceeds(t *testing.T) {
	node := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1", "label": "never", "condition": "false"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.BranchResult == nil || len(nr.BranchResult.ExecutedBranches) != 0 {
		t.Errorf("executed branches = %v, want none", nr.BranchResult)
	}
}

// A branch with no condition script is a container that represents "the rest of the canvas": it is skipped
// rather than failing.
func TestBranchExecutorSkipsBranchWithoutCondition(t *testing.T) {
	node := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if !strings.Contains(strings.Join(nr.RunLogs, "\n"), "has no condition script") {
		t.Errorf("logs = %v, want the skip to be logged", nr.RunLogs)
	}
}

func TestBranchExecutorMissingConfigFails(t *testing.T) {
	node := newExecNode("2", "branch", nil)
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without a config")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
}

func TestBranchExecutorWithoutChildrenFails(t *testing.T) {
	for _, cfg := range []map[string]any{
		{},
		{"children": []any{}},
	} {
		node := newExecNode("2", "branch", cfg)
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err == nil {
			t.Fatalf("Execute(%v) must fail without branches", cfg)
		}
		if nr.ErrCode != flow.ErrCodeConfig {
			t.Errorf("Execute(%v) ErrCode = %q, want config", cfg, nr.ErrCode)
		}
	}
}

// children entries without an id cannot be addressed by an edge's source_handle, so they are dropped; losing
// all of them is a config error rather than a silent "no branch matched".
func TestBranchExecutorChildrenWithoutIDFail(t *testing.T) {
	node := newExecNode("2", "branch", map[string]any{
		"children": []any{map[string]any{"label": "no id"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when no child has an id")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "failed to parse the branch config") {
		t.Errorf("ErrMsg = %q, want it to name the parse failure", nr.ErrMsg)
	}
}

// A condition that does not render means the operator cannot be trusted to route on it: the node fails as a
// config problem instead of quietly evaluating as false.
func TestBranchExecutorBrokenConditionFails(t *testing.T) {
	node := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1", "condition": "{{"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for a condition that does not render")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "condition failed") {
		t.Errorf("ErrMsg = %q, want it to name the condition", nr.ErrMsg)
	}
}

// A matched branch whose edge points at a node the flow does not contain is a config error on the container
// node itself (it never got as far as running the child chain).
func TestBranchExecutorMissingTargetNodeFails(t *testing.T) {
	node := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1", "condition": "true"},
		},
	})
	node.OutEdges = []*flow.Edge{{ID: "e1", Source: "2", Target: "ghost", SourceHandle: "sourceb1right"}}
	ctx := newExecContext()
	ctx.Flow = &flow.Flow{NodeMap: map[string]*flow.Node{"2": node}}
	nr := newExecResult(node, ctx)
	if err := (&BranchExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the branch edge points at a missing node")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "missing target node ghost") {
		t.Errorf("ErrMsg = %q, want it to name the missing node", nr.ErrMsg)
	}
}

// A run that was already cancelled stops before evaluating anything, and the code distinguishes "the user
// stopped it" from "the deadline passed".
func TestBranchExecutorCancelledBeforeWalk(t *testing.T) {
	c, cancel := context.WithCancel(context.Background())
	cancel()
	node := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1", "condition": "true"},
		},
	})
	ctx := newExecContext()
	ctx.StdCtx = c
	nr := newExecResult(node, ctx)
	err := (&BranchExecutor{}).Execute(node, ctx, "1", nr)
	if err == nil {
		t.Fatal("Execute must return the cancellation")
	}
	if nr.Status != flow.StatusCancelled {
		t.Errorf("status = %v, want cancelled", nr.Status)
	}
	if nr.ErrCode != flow.ErrCodeCancel {
		t.Errorf("ErrCode = %q, want cancel", nr.ErrCode)
	}
}

// exclusive defaults to true: the first match ends the node. Only an explicit false / "false" / "0" / "no"
// switches to "keep checking the next branch"; an invalid value counts as true.
func TestParseBranchChildren(t *testing.T) {
	branches, err := parseBranchChildren([]any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b", "exclusive": false},
		map[string]any{"id": "c", "exclusive": "no"},
		map[string]any{"id": "d", "exclusive": "garbage"},
	})
	if err != nil {
		t.Fatalf("parseBranchChildren = %v", err)
	}
	want := []branchConfig{
		{id: "a", label: "a", exclusive: true},
		{id: "b", label: "b", exclusive: false},
		{id: "c", label: "c", exclusive: false},
		{id: "d", label: "d", exclusive: true},
	}
	if len(branches) != len(want) {
		t.Fatalf("parsed %d branches, want %d", len(branches), len(want))
	}
	for i := range want {
		if branches[i] != want[i] {
			t.Errorf("branch[%d] = %#v, want %#v", i, branches[i], want[i])
		}
	}

	if _, err := parseBranchChildren(nil); err == nil {
		t.Error("parseBranchChildren(nil) must fail: a branch node needs at least one branch")
	}
}

func TestIsConditionTrue(t *testing.T) {
	cases := []struct {
		result any
		want   bool
	}{
		{nil, false},
		{true, true},
		{false, false},
		{"true", true},
		{"TRUE", true},
		{"false", false},
		{"1", false}, // a string is only true when it spells true
		{1, true},
		{0, false},
		{int64(2), true},
		{1.5, true},
		{float32(0), false},
		{map[string]any{}, false}, // a JSON object is not a verdict
	}
	for _, tc := range cases {
		if got := isConditionTrue(tc.result); got != tc.want {
			t.Errorf("isConditionTrue(%#v) = %v, want %v", tc.result, got, tc.want)
		}
	}
}

// Edges carry a direction suffix ("sourceb1right"), so the lookup prefix-matches on "source"+branchID.
func TestFindBranchEdge(t *testing.T) {
	node := newExecNode("2", "branch", nil)
	node.OutEdges = []*flow.Edge{
		{ID: "e1", Target: "n1", SourceHandle: "sourceb10right"},
		{ID: "e2", Target: "n2", SourceHandle: "source2bottom"},
	}
	if got := findBranchEdge(node, "b1"); got == nil || got.Target != "n1" {
		t.Errorf("findBranchEdge(b1) = %v, want the edge to n1", got)
	}
	if got := findBranchEdge(node, "2"); got == nil || got.Target != "n2" {
		t.Errorf("findBranchEdge(default) = %v, want the edge to n2", got)
	}
	if got := findBranchEdge(node, "nope"); got != nil {
		t.Errorf("findBranchEdge(nope) = %v, want nil", got)
	}
}
