package executors

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The chain walker follows one outgoing edge per node, and structural nodes (layout) are transparent: they have no
// executor and are not recorded, but the walk continues through them.
func TestExecuteFollowsEdgesThroughStructuralNodes(t *testing.T) {
	layout := &flow.Node{ID: "l1", Type: "layout", Data: &flow.NodeData{Label: "spacer"}}
	assign := newExecNode("a1", "assign", map[string]any{})
	layout.OutEdges = []*flow.Edge{{ID: "e1", Source: "l1", Target: "a1"}}

	ctx := newContainerContext(t, []*flow.Node{layout, assign})
	if err := execute(layout, ctx, "1", 0); err != nil {
		t.Fatalf("execute = %v", err)
	}
	if _, ok := ctx.ExecutedNodeMap.Get("1_a1"); !ok {
		t.Error("the node after the layout was not executed")
	}
	if _, ok := ctx.ExecutedNodeMap.Get("1_l1"); ok {
		t.Error("a layout node must not be recorded: it has no execution")
	}
}

// Several outgoing edges look valid on the canvas but only the first would ever run, which is the hardest fault to
// trace — so the walker refuses instead.
func TestExecuteRejectsMultipleOutgoingEdges(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{})
	node.OutEdges = []*flow.Edge{
		{ID: "e1", Source: "2", Target: "n1"},
		{ID: "e2", Source: "2", Target: "n2"},
	}
	ctx := newContainerContext(t, []*flow.Node{node})

	err := execute(node, ctx, "1", 0)
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Fatalf("execute = %v, want a config error", err)
	}
	if !strings.Contains(ee.Msg, "allows only one") {
		t.Errorf("msg = %q, want it to explain the single-edge rule", ee.Msg)
	}
}

// A branch node owns its outgoing edges: they are branch handles its executor already walked in a child process, so
// the main walk must stop instead of following one of them again.
func TestExecuteStopsAfterBranchNode(t *testing.T) {
	branch := newExecNode("2", "branch", map[string]any{
		"branch": map[string]any{"lang": "template"},
		"children": []any{
			map[string]any{"id": "b1", "condition": "false"},
		},
	})
	next := newExecNode("n1", "assign", map[string]any{})
	branch.OutEdges = []*flow.Edge{{ID: "e1", Source: "2", Target: "n1", SourceHandle: "sourceb1right"}}

	ctx := newContainerContext(t, []*flow.Node{branch, next})
	if err := execute(branch, ctx, "1", 0); err != nil {
		t.Fatalf("execute = %v", err)
	}
	if _, ok := ctx.ExecutedNodeMap.Get("1_n1"); ok {
		t.Error("the node behind a branch handle was executed by the main walk")
	}
}

// A cancelled context stops the walk before the next node, and the walker reports the cancellation itself rather than
// a node failure.
func TestExecuteStopsOnCancellation(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{node})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.StdCtx = cancelled

	err := execute(node, ctx, "1", 0)
	if err == nil {
		t.Fatal("execute must stop on a cancelled context")
	}
	if _, ok := ctx.ExecutedNodeMap.Get("1_2"); ok {
		t.Error("the node was executed although the run was cancelled")
	}
}
