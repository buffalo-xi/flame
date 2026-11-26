package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// fork only builds the group; the members join it later through their concurrency config. A limit that cannot be
// read must fail rather than silently fall back to unlimited: the UI would show a limit that is not in effect.
func TestForkExecutorMaxConcurrency(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		want    int
		wantErr bool
	}{
		{"not configured", map[string]any{}, -1, false},
		{"explicit -1", map[string]any{"max_concurrency": -1}, -1, false},
		{"null", map[string]any{"max_concurrency": nil}, -1, false},
		{"int", map[string]any{"max_concurrency": 4}, 4, false},
		{"float", map[string]any{"max_concurrency": float64(2)}, 2, false},
		{"fractional float is truncated", map[string]any{"max_concurrency": float64(2.9)}, 2, false},
		{"string is rejected", map[string]any{"max_concurrency": "4"}, 0, true},
		{"bool is rejected", map[string]any{"max_concurrency": true}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "fork", tc.config)
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			err := (&ForkExecutor{}).Execute(node, ctx, "1", nr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Execute = nil, want a config error for %#v", tc.config["max_concurrency"])
				}
				if nr.ErrCode != flow.ErrCodeConfig {
					t.Errorf("ErrCode = %q, want config", nr.ErrCode)
				}
				if !strings.Contains(nr.ErrMsg, "must be a number") {
					t.Errorf("ErrMsg = %q, want it to name the type problem", nr.ErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
			}
			if nr.ForkResult == nil {
				t.Fatal("ForkResult is nil: the members would have nowhere to join")
			}
			if nr.ForkResult.MaxConcurrency != tc.want {
				t.Errorf("MaxConcurrency = %d, want %d", nr.ForkResult.MaxConcurrency, tc.want)
			}
			if nr.Status != flow.StatusSuccess {
				t.Errorf("status = %v, want success", nr.Status)
			}
		})
	}
}

// A fork node without data is legal (nothing configured means unlimited).
func TestForkExecutorWithoutData(t *testing.T) {
	node := newExecNode("2", "fork", nil)
	node.Data = nil
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&ForkExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.ForkResult == nil || nr.ForkResult.MaxConcurrency != -1 {
		t.Errorf("ForkResult = %#v, want unlimited", nr.ForkResult)
	}
}

// A container without an entry node means its body never runs: failing is the only honest answer, because a
// silent success would look like a completed flow with a missing section.
func TestContainerExecutorsMissingEntry(t *testing.T) {
	cases := []struct {
		name string
		exec Executor
		typ  string
	}{
		{"function", &FunctionExecutor{}, "function"},
		{"process", &ProcessExecutor{}, "process"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", tc.typ, map[string]any{})
			ctx := newContainerContext(t, nil)
			nr := newExecResult(node, ctx)
			if err := tc.exec.Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail without an entry node")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, "body entry node not found") {
				t.Errorf("ErrMsg = %q, want it to name the missing entry", nr.ErrMsg)
			}
		})
	}
}

func TestContainerExecutorsMissingConfig(t *testing.T) {
	cases := []struct {
		name string
		exec Executor
		typ  string
	}{
		{"function", &FunctionExecutor{}, "function"},
		{"process", &ProcessExecutor{}, "process"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", tc.typ, nil)
			node.Data = nil
			ctx := newContainerContext(t, nil)
			nr := newExecResult(node, ctx)
			if err := tc.exec.Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail without node data")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
		})
	}
}

// A child chain ending in `return` is how a function body hands its result back: the function node itself
// succeeds and swallows the signal.
func TestFunctionExecutorReturnEndsNormally(t *testing.T) {
	node := newExecNode("2", "function", map[string]any{})
	ret := &flow.Node{ID: "r1", Type: "return", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newContainerContext(t, []*flow.Node{ret})
	ctx.Flow.Nodes = append(ctx.Flow.Nodes, entryTo("2", ret))

	nr := newExecResult(node, ctx)
	if err := (&FunctionExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.FunctionResult == nil {
		t.Error("FunctionResult is nil")
	}
}

// break / continue belong to a loop; reached through a function body they are a diagram error, not a silent
// no-op.
func TestFunctionExecutorBreakOutsideLoopFails(t *testing.T) {
	node := newExecNode("2", "function", map[string]any{})
	brk := &flow.Node{ID: "b1", Type: "break", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newContainerContext(t, []*flow.Node{brk})
	ctx.Flow.Nodes = append(ctx.Flow.Nodes, entryTo("2", brk))

	nr := newExecResult(node, ctx)
	if err := (&FunctionExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the body holds a break outside a loop")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "break/continue cannot appear outside a loop") {
		t.Errorf("ErrMsg = %q, want it to explain the signal", nr.ErrMsg)
	}
}

// exit stops the whole flow: the process node succeeds and passes the signal upwards so the runner can end the
// run.
func TestProcessExecutorExitPropagates(t *testing.T) {
	node := newExecNode("2", "process", map[string]any{})
	exit := &flow.Node{ID: "x1", Type: "exit", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newContainerContext(t, []*flow.Node{exit})
	ctx.Flow.Nodes = append(ctx.Flow.Nodes, entryTo("2", exit))

	nr := newExecResult(node, ctx)
	err := (&ProcessExecutor{}).Execute(node, ctx, "1", nr)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal to travel up", err)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success: exit is control flow, not a failure", nr.Status)
	}
}

// A normal child chain with no signal simply finishes, and the process node records success.
func TestProcessExecutorRunsToCompletion(t *testing.T) {
	node := newExecNode("2", "process", map[string]any{})
	child := newExecNode("c1", "assign", map[string]any{})
	child.ParentNode = "2"
	ctx := newContainerContext(t, []*flow.Node{child})
	ctx.Flow.Nodes = append(ctx.Flow.Nodes, entryTo("2", child))

	nr := newExecResult(node, ctx)
	if err := (&ProcessExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.ProcessResult == nil {
		t.Error("ProcessResult is nil")
	}
}

// An error inside the body travels up with its own code: the container must not flatten it to internal, or the
// on_error keyed on the real code stops matching one layer above.
func TestProcessExecutorPropagatesChildErrorCode(t *testing.T) {
	node := newExecNode("2", "process", map[string]any{})
	// A code node with no config.code fails with a config error before any sandbox call.
	child := newExecNode("c1", "code", map[string]any{})
	child.ParentNode = "2"
	ctx := newContainerContext(t, []*flow.Node{child})
	ctx.Flow.Nodes = append(ctx.Flow.Nodes, entryTo("2", child))

	nr := newExecResult(node, ctx)
	if err := (&ProcessExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the body fails")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want the child's config code to be passed through", nr.ErrCode)
	}
}

// findEntryNode looks up the entry child of a container, identified by parent_node plus the entry type.
func TestFindEntryNode(t *testing.T) {
	entry := &flow.Node{ID: "e1", Type: "entry", ParentNode: "2"}
	f := &flow.Flow{Nodes: []*flow.Node{
		{ID: "e0", Type: "entry", ParentNode: "3"},
		entry,
		{ID: "c1", Type: "code", ParentNode: "2"},
	}}
	if got := findEntryNode(f, "2"); got != entry {
		t.Errorf("findEntryNode(2) = %v, want e1", got)
	}
	if got := findEntryNode(f, "nope"); got != nil {
		t.Errorf("findEntryNode(nope) = %v, want nil", got)
	}
	if got := findEntryNode(&flow.Flow{}, "2"); got != nil {
		t.Errorf("an empty flow returned %v, want nil", got)
	}
}

// newContainerContext is the scaffolding a function / process node needs: a flow holding the child chain, node
// lookup, and the process bookkeeping the runner itself would have set up.
func newContainerContext(t *testing.T, children []*flow.Node) *flow.ExecContext {
	t.Helper()
	ctx := newExecContext()
	ctx.ExecutedProcessMap = flow.NewProcessExecMap()
	ctx.ExecutedNodeMap = flow.NewNodeExecMap()
	pm := flow.NewProcessManager()
	if _, err := pm.NewProcess("", nil); err != nil {
		t.Fatalf("NewProcess(root) = %v", err)
	}
	ctx.ProcessManager = pm

	f := &flow.Flow{Nodes: children, NodeMap: map[string]*flow.Node{}}
	for _, n := range children {
		f.NodeMap[n.ID] = n
	}
	ctx.Flow = f
	return ctx
}

// entryTo builds the entry node of a container and wires it to the first node of the body, which is how the
// runner finds the body start.
func entryTo(parentID string, first *flow.Node) *flow.Node {
	entry := &flow.Node{ID: "entry-" + parentID, Type: "entry", ParentNode: parentID}
	entry.OutEdges = []*flow.Edge{{ID: "e-" + parentID, Source: entry.ID, Target: first.ID}}
	return entry
}
