package tools

import "context"

// EndpointRunner runs this tenant's local endpoint tools (local__<endpoint name>); injected by package agent.
//
// Why it is not merged with ExternalRunner: mcp__ / rpc__ "connect out" and have to resolve connection records and
// protocols, while a local endpoint is an exposed endpoint that runs a flow in-process and must carry the caller's
// user / session so that the flow lands on the same ground. The two have different identity pass-through needs, and
// merging them would give the interface the shape "half the parameters are meaningless to half the implementations".
//
// Like ExternalRunner: Has only checks the prefix and does not query the database (the grant gate has already run), and a
// missing / deleted endpoint yields an explicit error from Invoke.
type EndpointRunner interface {
	Has(name string) bool
	Invoke(ctx context.Context, in LocalCall) (any, error)
}

// LocalCall is the input of one local endpoint call.
//
// The identity fields all come from tools.Context (Schema / UserID / SessionID / RunID), so that the flow reuses the
// caller's tenant and session - the same rule as call_agent passing session_id through.
type LocalCall struct {
	Schema    string
	AgentName string
	AgentID   string
	UserID    string
	SessionID string
	RunID     string
	// CallDepth is the caller agent's current stack depth (0 at the top). A local endpoint run passes it down as +1,
	// which blocks agent -> flow -> agent mutual calls (peer tool calls do not accumulate).
	CallDepth int
	// CallChain is the caller's current ancestor chain (this target excluded): a local endpoint run passes it down
	// unchanged, and the endpoint side appends "the entity about to execute" before handing it to agent / flow.
	CallChain []string
	ToolName  string // local__<endpoint name>
	Args      map[string]any
	MaxOutput int
	// DefinitionMode is the caller agent's definition-reading mode: when a debugging agent calls a local endpoint, the flow
	// it runs must follow the draft too (otherwise the debug chain ends up half draft, half live).
	DefinitionMode DefinitionMode
}

// endpointRunner picks the local endpoint runner this context should use (an explicit Context injection wins, handy for tests).
func (c *Context) endpointRunner() EndpointRunner {
	if c == nil {
		return nil
	}
	return c.Endpoints
}
