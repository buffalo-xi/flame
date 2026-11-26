package executors

import (
	"context"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/flowconfig"
)

// newExecNode is the node a canvas hands to an executor: an id, a type and one config block.
func newExecNode(id, typ string, config map[string]any) *flow.Node {
	return &flow.Node{ID: id, Type: typ, Data: &flow.NodeData{Label: typ, Config: config}}
}

// newExecContext is the smallest context the executors actually read: StdCtx for cancellation and timeouts, plus
// GetVariables / ResolveScriptTimeout, which both tolerate a nil Flow (the runner fills it in).
//
// Limits must be injected: a nil snapshot makes limits() resolve the tenant configuration once, which reaches Redis and
// the database. The built-in fallback is what an unconfigured tenant runs with, so it is the right snapshot here.
func newExecContext() *flow.ExecContext {
	limits := flowconfig.Fallback()
	return &flow.ExecContext{StdCtx: context.Background(), RunID: "test-run", Limits: &limits}
}

// newExecResult mirrors what the runner does before calling an executor: declare the outputs, then run. An executor is
// allowed to write only declared variables, so a test that skips this would fail on a rule, not on the behaviour it wants
// to check.
func newExecResult(node *flow.Node, ctx *flow.ExecContext) *flow.NodeResult {
	nr := flow.NewNodeResult(node, "1", ctx)
	// A node without data is a case the executors themselves reject; the helper must survive it instead of
	// panicking before the executor gets to say so.
	if node.Data != nil {
		if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
			nr.InitOutputVars(outputs)
		}
	}
	return nr
}
