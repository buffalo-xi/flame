package executors

import (
	"errors"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/flowconfig"
)

// The call node is a function invocation with a contract check first: target present, target really a function,
// arguments matching the declaration. All of those are configuration errors the author can fix.
func TestCallExecutorConfigErrors(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{
		"params": map[string]any{
			"required_one": map[string]any{"required": true},
			"optional_one": map[string]any{"required": false},
		},
	})
	body := newExecNode("fc1", "assign", map[string]any{})
	body.ParentNode = "f1"

	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"missing target", map[string]any{}, "call node missing config.target"},
		{"target not in the flow", map[string]any{"target": "ghost"}, "target function node ghost not found"},
		{"target is not a function", map[string]any{"target": "fc1"}, "is not a function node"},
		{"missing required arg", map[string]any{"target": "f1"}, `missing required arg "required_one"`},
		{
			name: "unknown arg",
			config: map[string]any{"target": "f1", "args": map[string]any{
				// The required one has to be present, otherwise it fails first and the extra key is never reported.
				"required_one": map[string]any{"source_type": "flow", "source_key": "v"},
				"nope":         map[string]any{"source_type": "flow", "source_key": "v"},
			}},
			wantMsg: `unknown arg "nope"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newCallScaffold(t, funcNode, body)
			node := newExecNode("2", "call", tc.config)
			nr := newExecResult(node, ctx)
			if err := (&CallExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed call")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}
}

func TestCallExecutorMissingData(t *testing.T) {
	node := newExecNode("2", "call", nil)
	node.Data = nil
	ctx := newCallScaffold(t, newFunctionNode("f1", map[string]any{}))
	nr := newExecResult(node, ctx)
	if err := (&CallExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without node data")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
}

// The happy path: arguments resolve in the caller's context, the target function's body runs in a child process,
// and its outputs are mirrored onto the call node.
func TestCallExecutorRunsTargetFunction(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{
		"outputs": map[string]any{"result": map[string]any{"type": "string"}},
		"params": map[string]any{
			"x": map[string]any{"required": true},
			"y": map[string]any{"required": false},
		},
	})
	body := newExecNode("fc1", "assign", map[string]any{})
	body.ParentNode = "f1"

	ctx := newCallScaffold(t, funcNode, body)
	ctx.RunFlowVars = map[string]any{"v": "from the caller"}

	node := newExecNode("2", "call", map[string]any{
		"target":  "f1",
		"args":    map[string]any{"x": map[string]any{"source_type": "flow", "source_key": "v"}},
		"outputs": map[string]any{"result": map[string]any{"type": "string"}},
	})
	nr := newExecResult(node, ctx)
	if err := (&CallExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if _, ok := nr.OutputVars["result"]; !ok {
		t.Errorf("OutputVars = %v, want the target function's outputs mirrored", nr.OutputVars)
	}
}

// A failing function body travels up with its own code: the call node is the same layer as any other container and
// must not flatten it, or on_error one level above never matches.
func TestCallExecutorPropagatesChildFailure(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{})
	// A code node with no config.code fails as a config error without touching the sandbox.
	body := newExecNode("fc1", "code", map[string]any{})
	body.ParentNode = "f1"

	ctx := newCallScaffold(t, funcNode, body)
	node := newExecNode("2", "call", map[string]any{"target": "f1"})
	nr := newExecResult(node, ctx)
	if err := (&CallExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the function body fails")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want the body's code", nr.ErrCode)
	}
	if nr.Status != flow.StatusFailed {
		t.Errorf("status = %v, want failed", nr.Status)
	}
}

// `exit` in the body ends the whole flow: the function reports success and the call node lets the signal through.
func TestCallExecutorPropagatesExitSignal(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{})
	body := &flow.Node{ID: "fx1", Type: "exit", ParentNode: "f1", Data: &flow.NodeData{}}

	ctx := newCallScaffold(t, funcNode, body)
	node := newExecNode("2", "call", map[string]any{"target": "f1"})
	nr := newExecResult(node, ctx)
	err := (&CallExecutor{}).Execute(node, ctx, "1", nr)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal", err)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success: exit is control flow", nr.Status)
	}
}

// The recursion gate runs before the child process is created: a call chain that reached the tenant's depth limit
// stops here instead of growing the stack.
func TestCallExecutorDepthLimit(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{})
	body := newExecNode("fc1", "assign", map[string]any{})
	body.ParentNode = "f1"

	ctx := newCallScaffold(t, funcNode, body)
	limits := flowconfig.Fallback()
	limits.MaxRecursionDepth = 1
	ctx.Limits = &limits

	// A process created by a call does carry call_func, and the depth counts it.
	childPID, err := ctx.ProcessManager.NewProcess("1", map[string]any{"call_func": true})
	if err != nil {
		t.Fatalf("NewProcess = %v", err)
	}

	node := newExecNode("2", "call", map[string]any{"target": "f1"})
	nr := newExecResult(node, ctx)
	if err := (&CallExecutor{}).Execute(node, ctx, childPID, nr); err == nil {
		t.Fatal("Execute must fail when the depth limit is reached")
	}
	if nr.ErrCode != flow.ErrCodeInternal {
		t.Errorf("ErrCode = %q, want internal", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "exceeded max recursion depth") {
		t.Errorf("ErrMsg = %q, want it to name the limit", nr.ErrMsg)
	}
}

// An optional parameter may be omitted: only required ones are enforced.
func TestCallExecutorOptionalParamMayBeOmitted(t *testing.T) {
	funcNode := newFunctionNode("f1", map[string]any{
		"params": map[string]any{"opt": map[string]any{"required": false}},
	})
	body := newExecNode("fc1", "assign", map[string]any{})
	body.ParentNode = "f1"

	ctx := newCallScaffold(t, funcNode, body)
	node := newExecNode("2", "call", map[string]any{"target": "f1"})
	nr := newExecResult(node, ctx)
	if err := (&CallExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
}

// newFunctionNode builds the target of a call node: a function node whose body entry is created by the caller's
// scaffold.
func newFunctionNode(id string, config map[string]any) *flow.Node {
	return &flow.Node{ID: id, Type: "function", Data: &flow.NodeData{Label: id, Config: config}}
}

// newCallScaffold wires a function node plus its body into a flow, with the process bookkeeping the runner would
// normally provide.
func newCallScaffold(t *testing.T, funcNode *flow.Node, body ...*flow.Node) *flow.ExecContext {
	t.Helper()
	children := make([]*flow.Node, 0, len(body)+2)
	children = append(children, funcNode)
	children = append(children, body...)
	if len(body) > 0 {
		children = append(children, entryTo(funcNode.ID, body[0]))
	}
	return newContainerContext(t, children)
}
