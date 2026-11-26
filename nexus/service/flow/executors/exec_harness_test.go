package executors

import (
	"context"

	"flame/nexus/service/flow/core"
)

// newExecNode is the node a canvas hands to an executor: an id, a type and one config block.
func newExecNode(id, typ string, config map[string]any) *flow.Node {
	return &flow.Node{ID: id, Type: typ, Data: &flow.NodeData{Label: typ, Config: config}}
}

// newExecContext is the smallest context the executors actually read: StdCtx for cancellation and timeouts, plus
// GetVariables / ResolveScriptTimeout, which both tolerate a nil Flow (the runner fills it in).
func newExecContext() *flow.ExecContext {
	return &flow.ExecContext{StdCtx: context.Background(), RunID: "test-run"}
}

// newExecResult mirrors what the runner does before calling an executor: declare the outputs, then run. An executor is
// allowed to write only declared variables, so a test that skips this would fail on a rule, not on the behaviour it wants
// to check.
func newExecResult(node *flow.Node, ctx *flow.ExecContext) *flow.NodeResult {
	nr := flow.NewNodeResult(node, "1", ctx)
	if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
		nr.InitOutputVars(outputs)
	}
	return nr
}
