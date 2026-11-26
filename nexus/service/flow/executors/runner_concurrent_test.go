package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// A concurrent node is dispatched, not executed inline: the main walk returns immediately, the node runs in a child
// process, and its outgoing edge is not followed (the fork / mono group owns the fan-out).
func TestDispatchConcurrentFollowedByForkGroup(t *testing.T) {
	fr := flow.NewForkResult(0)
	node := newExecNode("2", "assign", map[string]any{})
	next := newExecNode("3", "assign", map[string]any{})
	node.OutEdges = []*flow.Edge{{ID: "e1", Source: "2", Target: "3"}}

	ctx := newContainerContext(t, []*flow.Node{node, next})
	forkNR := flow.NewNodeResult(&flow.Node{ID: "f1", Type: "fork"}, "1", ctx)
	forkNR.ForkResult = fr
	ctx.ExecutedNodeMap.Set("1_f1", forkNR)

	if err := dispatchConcurrent(node, ctx, "1", &concurrencyConfig{Mode: "fork", ForkNodeID: "f1"}); err != nil {
		t.Fatalf("dispatchConcurrent = %v", err)
	}
	if completed, err := fr.WaitTimeout(5); !completed || err != nil {
		t.Fatalf("the group did not finish: completed=%v err=%v", completed, err)
	}

	members := fr.Members()
	if len(members) != 1 || members[0].NodeID != "2" {
		t.Fatalf("members = %#v, want the dispatched node", members)
	}
	if _, ok := ctx.ExecutedNodeMap.Get(members[0].ProcessID + "_2"); !ok {
		t.Error("the node was not executed under its child process")
	}
	if _, ok := ctx.ExecutedNodeMap.Get("1_3"); ok {
		t.Error("the node behind the concurrent one was executed by the main walk")
	}
}

// mono is the standalone variant: no fork group, tracked by the run's wait group instead.
func TestDispatchConcurrentMonoTrackedByWaitGroup(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{node})

	if err := dispatchConcurrent(node, ctx, "1", &concurrencyConfig{Mode: "mono"}); err != nil {
		t.Fatalf("dispatchConcurrent = %v", err)
	}
	ctx.MonoWGWait()

	children := ctx.ProcessManager.GetChildren("1")
	if len(children) != 1 {
		t.Fatalf("children = %v, want one child process", children)
	}
	if _, ok := ctx.ExecutedNodeMap.Get(children[0] + "_2"); !ok {
		t.Error("the mono node was not executed under its child process")
	}
}

// fork without a fork id cannot be joined anywhere: the dispatcher refuses before creating anything.
func TestDispatchConcurrentForkWithoutID(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{node})

	err := dispatchConcurrent(node, ctx, "1", &concurrencyConfig{Mode: "fork"})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Fatalf("dispatchConcurrent = %v, want a config error", err)
	}
	if !strings.Contains(ee.Msg, "fork_node_id is required") {
		t.Errorf("msg = %q, want it to name the missing id", ee.Msg)
	}
	if children := ctx.ProcessManager.GetChildren("1"); len(children) != 0 {
		t.Errorf("children = %v, want none: nothing should have been created", children)
	}
}

// A fork id that is not on this process chain (a wrong graph) is the same class of error.
func TestDispatchConcurrentUnknownFork(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{node})

	err := dispatchConcurrent(node, ctx, "1", &concurrencyConfig{Mode: "fork", ForkNodeID: "ghost"})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Fatalf("dispatchConcurrent = %v, want a config error", err)
	}
	if !strings.Contains(ee.Msg, "fork node result not found") {
		t.Errorf("msg = %q, want it to name the missing fork", ee.Msg)
	}
}

// A group that is already waiting cannot accept members; the child process created for the attempt is reclaimed so no
// dangling record is left behind.
func TestDispatchConcurrentReclaimsProcessWhenJoinFails(t *testing.T) {
	fr := flow.NewForkResult(0)
	node := newExecNode("2", "assign", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{node})
	forkNR := flow.NewNodeResult(&flow.Node{ID: "f1", Type: "fork"}, "1", ctx)
	forkNR.ForkResult = fr
	ctx.ExecutedNodeMap.Set("1_f1", forkNR)

	// An empty group finishes at once, which already puts it past the "accepting members" state.
	if completed, err := fr.Wait(); !completed || err != nil {
		t.Fatalf("the empty group did not finish: completed=%v err=%v", completed, err)
	}

	before := len(ctx.ProcessManager.GetChildren("1"))
	err := dispatchConcurrent(node, ctx, "1", &concurrencyConfig{Mode: "fork", ForkNodeID: "f1"})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeInternal {
		t.Fatalf("dispatchConcurrent = %v, want an internal error", err)
	}
	if !strings.Contains(ee.Msg, "fork add failed") {
		t.Errorf("msg = %q, want it to name the join failure", ee.Msg)
	}
	if after := len(ctx.ProcessManager.GetChildren("1")); after != before {
		t.Errorf("children before/after = %d/%d, want the child process reclaimed", before, after)
	}
}
