package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ===== Service endpoints (svc_endpoints) — supervisor only =====
//
// An endpoint is the entity's entrance from outside the tenant (mcp / rpc). The verbs follow the endpoint domain:
// **create / delete** the slot, **expose / retract** the switch (never "publish" or "hide").
//
// Two rules the tool layer must not paper over:
//   - the endpoint name always equals the entity name (the repository forces it), so the tools take a name and never a
//     custom endpoint name;
//   - a flow's RPC endpoint is **managed**: it is created with the flow and only has an exposure switch, so it can be
//     exposed / retracted but never created or deleted on its own (see ErrEndpointManaged).

// EndpointsAdmin is endpoint administration for the tools. The implementation pins the tenant schema and the current
// user, so the tool layer cannot pass them in — the same convention as Agents / Skills / Flows.
type EndpointsAdmin interface {
	// List returns every endpoint of the named target.
	List(ctx context.Context, targetType, target string) ([]EndpointState, error)
	// Create adds one endpoint slot to the named target and returns its state.
	Create(ctx context.Context, targetType, target, protocol string, exposed bool) (*EndpointState, error)
	// SetExposed flips the exposure switch on those protocols (empty = every endpoint of the target).
	SetExposed(ctx context.Context, targetType, target string, protocols []string, exposed bool) ([]EndpointState, error)
	// Delete removes one endpoint slot from the named target.
	Delete(ctx context.Context, targetType, target, protocol string) error
}

// EndpointState is one endpoint as the tools report it back.
type EndpointState struct {
	TargetType string `json:"target_type"` // agent / flow
	Target     string `json:"target"`
	Protocol   string `json:"protocol"` // mcp / rpc
	Exposed    bool   `json:"exposed"`
}

// Sentinel errors shared by the manager and the tools: the tool layer turns them into "what to do next" instead of
// leaking a storage error to the model.
var (
	ErrTargetNotFound      = errors.New("the target does not exist")
	ErrEndpointMissing     = errors.New("the target has no such endpoint")
	ErrEndpointManaged     = errors.New("the RPC endpoint is managed with its flow and cannot be created or deleted on its own")
	ErrEndpointExposed     = errors.New("the endpoint is still exposed")
	ErrEndpointNotReleased = errors.New("the target has no released version yet")
)

// ===== shared helpers =====

// normalizeProtocols lowercases, de-duplicates and validates a protocol list, keeping the given order.
func normalizeProtocols(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "mcp", "rpc":
		case "":
			continue
		default:
			return nil, fmt.Errorf("protocol supports mcp / rpc only, got %q", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// endpointError adds the next step to a sentinel; unknown errors pass through untouched.
func endpointError(domain string, err error) error {
	switch {
	case errors.Is(err, ErrTargetNotFound):
		return fmt.Errorf("%w — check the name with %s first", err, targetListTool(domain))
	case errors.Is(err, ErrEndpointExposed):
		return fmt.Errorf("%w — call retract_%s_endpoint first, then delete", err, domain)
	case errors.Is(err, ErrEndpointManaged):
		return fmt.Errorf("%w — use expose_%s_endpoint / retract_%s_endpoint, or deprecate the flow to take it offline", err, domain, domain)
	case errors.Is(err, ErrEndpointNotReleased):
		return fmt.Errorf("%w — release a version in the console's version panel, then expose it again (the supervisor's own endpoints are exempt)", err)
	}
	return err
}

func targetListTool(domain string) string {
	if domain == "agent" {
		return "list_agents"
	}
	return "list_flows"
}

// releaseRequired is the structured "one step left" answer for an action blocked by a missing released version.
//
// It is a result field, not an error: nothing failed - the slot is in place and only the exposure switch is waiting on a
// human. A bare error used to be the entire answer here ("no released version yet"), which left the round trip with
// nothing to show for it and no way to name what happens next.
func releaseRequired(domain, target string, protocols []string) map[string]any {
	return map[string]any{
		"target_type": domain,
		"target":      target,
		"protocols":   protocols,
		"reason":      "production entry points only serve a released version, and this " + domain + " has none yet",
		"next":        "have the user release a version in the console's version panel, then call expose_" + domain + "_endpoint",
	}
}

// endpointTarget normalizes the shared name argument.
func endpointTarget(domain string, args map[string]any) (string, error) {
	name := strings.TrimSpace(argString(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required: the %s name (an endpoint name always equals it)", domain)
	}
	return name, nil
}

// endpointProtocol parses the single-protocol argument (empty = mcp).
func endpointProtocol(args map[string]any) (string, error) {
	p := strings.ToLower(strings.TrimSpace(argString(args, "protocol")))
	if p == "" {
		p = "mcp"
	}
	if p != "mcp" && p != "rpc" {
		return "", fmt.Errorf("protocol supports mcp / rpc only, got %q", p)
	}
	return p, nil
}

// createEndpoint creates one slot, and turns "it is already there" into a report instead of an error — the model then
// sees the current state and can go straight to expose / retract.
func createEndpoint(ctx context.Context, c *Context, domain string, args map[string]any) (any, error) {
	name, err := endpointTarget(domain, args)
	if err != nil {
		return nil, err
	}
	protocol, err := endpointProtocol(args)
	if err != nil {
		return nil, err
	}
	if c.EndpointAdmin == nil {
		return nil, errors.New("endpoint administration is unavailable in this context")
	}

	// A flow's RPC endpoint is a managed slot: created with the flow, exposure switch only. Reported as "nothing to
	// create" rather than an error, so the model does not retry it with another shape.
	if domain == "flow" && protocol == "rpc" {
		return map[string]any{
			"created":     false,
			"target_type": domain,
			"target":      name,
			"protocol":    protocol,
			"note":        "a flow's RPC endpoint is created together with the flow and cannot be created on its own; use expose_flow_endpoint / retract_flow_endpoint to switch it",
		}, nil
	}

	current, err := c.EndpointAdmin.List(ctx, domain, name)
	if err != nil {
		return nil, endpointError(domain, err)
	}
	for _, ep := range current {
		if ep.Protocol == protocol {
			return map[string]any{
				"created":  false,
				"endpoint": ep,
				"note": fmt.Sprintf("the endpoint already exists; use expose_%s_endpoint / retract_%s_endpoint to change whether it answers outside callers", domain, domain),
			}, nil
		}
	}

	wantExposed := argBool(args, "exposed")
	st, err := c.EndpointAdmin.Create(ctx, domain, name, protocol, wantExposed)
	deferred := false
	if err != nil && wantExposed && errors.Is(err, ErrEndpointNotReleased) {
		// Only the switch needs a released version; the slot itself does not. Creating it unexposed keeps the caller one
		// step from done - failing here would leave nothing behind and force a second create once the user has released.
		if st, err = c.EndpointAdmin.Create(ctx, domain, name, protocol, false); err == nil {
			deferred = true
		}
	}
	if err != nil {
		return nil, endpointError(domain, err)
	}
	out := map[string]any{"created": true, "endpoint": st}
	if deferred {
		out["release_required"] = releaseRequired(domain, name, []string{protocol})
	}
	return out, nil
}

// deleteEndpoint removes one slot. The flow RPC slot is refused up front (managed), everything else goes through the
// manager, which refuses an exposed endpoint and asks for a retract first.
func deleteEndpoint(ctx context.Context, c *Context, domain string, args map[string]any) (any, error) {
	name, err := endpointTarget(domain, args)
	if err != nil {
		return nil, err
	}
	protocol, err := endpointProtocol(args)
	if err != nil {
		return nil, err
	}
	if c.EndpointAdmin == nil {
		return nil, errors.New("endpoint administration is unavailable in this context")
	}
	if domain == "flow" && protocol == "rpc" {
		return nil, endpointError(domain, fmt.Errorf("%w (protocol rpc)", ErrEndpointManaged))
	}
	if err := c.EndpointAdmin.Delete(ctx, domain, name, protocol); err != nil {
		return nil, endpointError(domain, err)
	}
	return map[string]any{"deleted": true, "target_type": domain, "target": name, "protocol": protocol}, nil
}

// setEndpointExposed flips the exposure switch on the named protocols (empty = every endpoint of the target).
func setEndpointExposed(ctx context.Context, c *Context, domain string, args map[string]any, exposed bool) (any, error) {
	name, err := endpointTarget(domain, args)
	if err != nil {
		return nil, err
	}
	if c.EndpointAdmin == nil {
		return nil, errors.New("endpoint administration is unavailable in this context")
	}
	raw, _ := argStringSlice(args, "protocols")
	protocols, err := normalizeProtocols(raw)
	if err != nil {
		return nil, err
	}
	// Retracting with no protocols means "all of them"; exposing with no protocols would be a no-op the caller did not
	// mean, so it is refused instead.
	if exposed && len(protocols) == 0 {
		return nil, errors.New("protocols is required: name which endpoints to expose (mcp / rpc)")
	}
	list, err := c.EndpointAdmin.SetExposed(ctx, domain, name, protocols, exposed)
	if err != nil {
		return nil, endpointError(domain, err)
	}
	result := map[string]any{"target_type": domain, "target": name, "endpoints": list}
	if exposed {
		result["exposed"] = true
	} else {
		result["retracted"] = true
	}
	return result, nil
}

// ===== create_agent_endpoint =====

type createAgentEndpointTool struct{}

func (t *createAgentEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_agent_endpoint",
		Category: CategoryAgentAdmin,
		Description: "Add an endpoint slot (mcp / rpc) to an **agent** (supervisor only). The endpoint name always equals the agent name; " +
			"a newly created endpoint is not exposed unless exposed=true, so it only serves calls from inside the tenant. " +
			"Exposing needs the agent to have a released version (the supervisor itself is exempt: its entry points read the draft); " +
			"when exposed=true and no version is released yet, the slot is still created unexposed and the answer carries release_required " +
			"instead of failing. Calling it when the slot already exists changes nothing and returns created=false with the current state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Agent name (the endpoint name always equals it)"},
				"protocol": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}, "description": "Endpoint protocol: mcp or rpc"},
				"exposed":  map[string]any{"type": "boolean", "description": "Expose it to callers outside the tenant right away (optional, default false)"},
			},
			"required": []any{"name", "protocol"},
		},
	}
}

func (t *createAgentEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createEndpoint(ctx, c, "agent", args)
}

// ===== delete_agent_endpoint =====

type deleteAgentEndpointTool struct{}

func (t *deleteAgentEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_agent_endpoint",
		Category: CategoryAgentAdmin,
		Description: "Delete one endpoint slot (mcp / rpc) of an **agent** (supervisor only). An endpoint that is still exposed is refused — " +
			"retract it first (retract_agent_endpoint), because outside callers may be using it right now. " +
			"Use create_agent_endpoint to add the slot back.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Agent name"},
				"protocol": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}, "description": "Which endpoint to delete: mcp or rpc"},
			},
			"required": []any{"name", "protocol"},
		},
	}
}

func (t *deleteAgentEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteEndpoint(ctx, c, "agent", args)
}

// ===== expose_agent_endpoint / retract_agent_endpoint =====

type exposeAgentEndpointTool struct{}

func (t *exposeAgentEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "expose_agent_endpoint",
		Category: CategoryAgentAdmin,
		Description: "Expose an **agent's** endpoints to callers outside the tenant (supervisor only). The endpoint slots must already exist " +
			"(create_agent_endpoint); this only flips the switch. Refused while the agent is deprecated, or when it has never released a version " +
			"and is not the supervisor (its entry points read the draft, so it is exempt).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Agent name"},
				"protocols": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}}, "description": "Which endpoints to expose (mcp / rpc)"},
			},
			"required": []any{"name", "protocols"},
		},
	}
}

func (t *exposeAgentEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return setEndpointExposed(ctx, c, "agent", args, true)
}

type retractAgentEndpointTool struct{}

func (t *retractAgentEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "retract_agent_endpoint",
		Category: CategoryAgentAdmin,
		Description: "Retract an **agent's** endpoints: they stop answering outside callers, while the endpoint records and their " +
			"configuration stay (supervisor only). Omit protocols to retract every endpoint of the agent. Never refused — any state can be " +
			"taken offline, and this is the prerequisite for deleting an exposed endpoint.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Agent name"},
				"protocols": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}}, "description": "Which endpoints to retract (optional; omit for all)"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *retractAgentEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return setEndpointExposed(ctx, c, "agent", args, false)
}

// ===== create_flow_endpoint =====

type createFlowEndpointTool struct{}

func (t *createFlowEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_flow_endpoint",
		Category: CategoryFlowAdmin,
		Description: "Add an endpoint slot to a **workflow** (supervisor only). The endpoint name always equals the workflow name. " +
			"protocol=mcp creates the MCP slot (unexposed unless exposed=true). protocol=rpc is kept for the contract but changes nothing today: " +
			"a flow's RPC endpoint is created together with the flow and only has an exposure switch, so the answer says created=false and points " +
			"at expose_flow_endpoint / retract_flow_endpoint. Exposing needs a released version — a fresh flow has none, so exposed=true creates the " +
			"slot unexposed and returns release_required: have the user release in the console, then call expose_flow_endpoint.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Workflow name (the endpoint name always equals it)"},
				"protocol": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}, "description": "Endpoint protocol (default mcp). rpc is managed by the flow and cannot be created here"},
				"exposed":  map[string]any{"type": "boolean", "description": "Expose it right away (optional, default false; requires a released version)"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *createFlowEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createEndpoint(ctx, c, "flow", args)
}

// ===== delete_flow_endpoint =====

type deleteFlowEndpointTool struct{}

func (t *deleteFlowEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_flow_endpoint",
		Category: CategoryFlowAdmin,
		Description: "Delete a **workflow's** MCP endpoint slot (supervisor only). protocol=mcp deletes it, and it must be retracted first. " +
			"protocol=rpc is refused: the RPC endpoint is created with the flow and references such as local:rpc:<name> point at it, so it is " +
			"retracted (or the flow deprecated) instead of deleted.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Workflow name"},
				"protocol": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}, "description": "Which endpoint to delete (default mcp); rpc is managed by the flow"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *deleteFlowEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteEndpoint(ctx, c, "flow", args)
}

// ===== expose_flow_endpoint / retract_flow_endpoint =====

type exposeFlowEndpointTool struct{}

func (t *exposeFlowEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "expose_flow_endpoint",
		Category: CategoryFlowAdmin,
		Description: "Expose a **workflow's** endpoints (mcp / rpc) to callers outside the tenant (supervisor only). Both slots must exist: " +
			"MCP comes from create_flow_endpoint, RPC is created with the flow. Refused while the flow is deprecated or has no released version — " +
			"release it in the console's version panel first, then expose.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Workflow name"},
				"protocols": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}}, "description": "Which endpoints to expose (mcp / rpc)"},
			},
			"required": []any{"name", "protocols"},
		},
	}
}

func (t *exposeFlowEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return setEndpointExposed(ctx, c, "flow", args, true)
}

type retractFlowEndpointTool struct{}

func (t *retractFlowEndpointTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "retract_flow_endpoint",
		Category: CategoryFlowAdmin,
		Description: "Retract a **workflow's** endpoints (mcp / rpc): they stop answering outside callers, the endpoint records stay " +
			"(supervisor only). Omit protocols to retract all of them. Never refused, and it is the prerequisite for deleting an exposed " +
			"MCP endpoint (the managed RPC endpoint is never deleted).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Workflow name"},
				"protocols": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}}, "description": "Which endpoints to retract (optional; omit for all)"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *retractFlowEndpointTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return setEndpointExposed(ctx, c, "flow", args, false)
}

func init() {
	Register(&createAgentEndpointTool{})
	Register(&deleteAgentEndpointTool{})
	Register(&exposeAgentEndpointTool{})
	Register(&retractAgentEndpointTool{})
	Register(&createFlowEndpointTool{})
	Register(&deleteFlowEndpointTool{})
	Register(&exposeFlowEndpointTool{})
	Register(&retractFlowEndpointTool{})
}
