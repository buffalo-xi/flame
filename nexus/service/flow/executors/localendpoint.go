package executors

import (
	"context"

	"flame/nexus/service/agent"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
)

// init hands "run one endpoint in this process" to the agent package, for the local__<endpoint> tool calls.
//
// Dependency direction: executors -> service/agent (the rpc node already calls agent endpoints directly),
// and service/agent does not import this package — this registration point breaks the cycle. Any process
// that loads flow (cmd/main, websocket, rpc and mcp all import this package) gains the capability
// automatically.
func init() { agent.SetLocalEndpointRunner(runEndpointForTool) }

// runEndpointForTool serves agent's local__<endpoint> tool calls.
//
// wantProgress is always false: a tool call only returns the final result and has no progress channel;
// passing true would make the endpoint collect the agent's thinking and text fragments for nothing.
// Passing nil for onProgress is consistent with that.
//
// callDepth is the calling agent's current depth (tools.Context.CallDepth), and endpoint execution passes
// it on as +1 — that is what makes the agent -> flow -> agent cycle converge.
//
// The call chain arrives through ctx (callchain.With in the agent-side Invoke) rather than as another
// parameter: the signature already has eight positional arguments, and the chain is pure control
// information that carries no business meaning.
func runEndpointForTool(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	sessionID string,
	callDepth int,
	endpointName string, // endpoint name (= flow name / agent name), no longer an id
	args map[string]any,
	wantProgress bool,
	followDrafts bool,
) (string, bool, error) {
	// followDrafts comes from the calling agent's definition-reading mode: an agent under debug calling a
	// local endpoint makes the flow it runs follow drafts too (otherwise a debug chain ends up half draft,
	// half production). A real run passes false = nil scope = read the released version.
	var scope *flow.DebugScope
	if followDrafts {
		scope = flow.NewDebugScope(true)
	}
	return runEndpoint(ctx, authInfo, sessionID, callDepth, callchain.From(ctx), endpointName, args, wantProgress, scope, nil)
}
