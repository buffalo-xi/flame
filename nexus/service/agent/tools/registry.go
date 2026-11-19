package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"flame/nexus/shared/callchain"
	"flame/nexus/shared/capability"
)

var (
	toolMap = make(map[string]Tool)
	mu      sync.RWMutex
)

// ErrToolNotFound means the tool is not registered.
var ErrToolNotFound = errors.New("tool not found")

// Register registers a built-in tool (called from each tool file's init()).
func Register(t Tool) {
	mu.Lock()
	defer mu.Unlock()
	toolMap[t.Meta().Name] = t
}

// Get fetches a tool by name.
func Get(name string) (Tool, bool) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := toolMap[name]
	return t, ok
}

// List returns every registered tool.
func List() []Tool {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Tool, 0, len(toolMap))
	for _, t := range toolMap {
		out = append(out, t)
	}
	return out
}

// Names returns every registered tool name (ascending, for stable output and comparison).
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(toolMap))
	for name := range toolMap {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ExecuteTool is the single dispatcher: grant hard gate -> built-in tool -> plugin tool -> external connection tool ->
// custom tool -> not found.
//
// The order is deliberate: **pass the grant gate by name first**, then look up the tool. Otherwise an ungranted plugin tool
// name would first be handed to the plugin runner (starting a python process, spending the tenant's retrieval quota), and the
// price is already paid before the refusal.
func ExecuteTool(ctx context.Context, c *Context, name string, args map[string]any) (any, error) {
	if c == nil {
		return nil, errors.New("tool context is nil")
	}
	if args == nil {
		args = map[string]any{}
	}

	// Grant hard gate: when AllowedTools is non-nil, only tools granted to this agent pass.
	// The same source as "the tools declared to the model" - otherwise a text-form tool call (the model writes the call into
	// its reply, e.g. DeepSeek's |DSML| marker) would bypass the grant system: even a tool that was never declared could be
	// executed by writing the marker.
	if c.AllowedTools != nil && !c.AllowedTools[name] {
		return nil, fmt.Errorf("tool %s is not granted to agent %s", name, c.AgentName)
	}

	if tool, ok := Get(name); ok {
		return executeBuiltin(ctx, c, tool, name, args)
	}

	// Plugin tools: their names never enter this package's tool table (they are discovered from disk at run time), so they go
	// to the injected runner.
	// Argument validation happens on the plugin side against the manifest's schema (the schema is not reachable here).
	if runner := c.pluginRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		return runner.Invoke(ctx, c.Schema, name, args)
	}

	// External connection tools (mcp__<conn>__<tool> / rpc__<conn>__<tool>): resolving connection name -> connection record ->
	// call protocol all lives in the implementation (repositories + SDK); here the call is only forwarded by name.
	// It sits after the grant gate: an ungranted name does not even reach the database.
	if runner := c.externalRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		// The ancestor chain is handed to the runner through ctx: rpc__ writes it into _meta.call_chain when going out, and
		// mcp__ ignores it (the MCP side has no such convention). The business signatures therefore need no extra parameter.
		ctx = callchain.With(ctx, c.CallChain)
		return runner.Invoke(ctx, c.Schema, name, args, c.MaxOutputLimit())
	}

	// Local endpoints (local__<endpoint name>): they run a flow in-process, without any network connection.
	// They are injected separately from mcp__/rpc__: those two resolve connection records, this one reuses the caller's
	// session identity.
	// It sits after the grant gate: an ungranted name does not even reach the endpoint table.
	if runner := c.endpointRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		return runner.Invoke(ctx, LocalCall{
			Schema:    c.Schema,
			AgentName: c.AgentName,
			AgentID:   c.AgentID,
			UserID:    c.UserID,
			SessionID: c.SessionID,
			RunID:     c.RunID,
			CallDepth: c.CallDepth,
			CallChain: c.CallChain,
			ToolName:  name,
			Args:      args,
			MaxOutput: c.MaxOutputLimit(),
			// Child call mode: computed centrally by ChildMode (own draft && the follow-drafts switch),
			// not by passing c.DefinitionMode directly - that way a local flow would still read the draft with the switch off.
			DefinitionMode: c.ChildMode(),
		})
	}

	// Custom tools: the namespace is fixed at custom__<name>, which does not overlap with built-in / plugin names.
	// Arguments are handled inside executeCustomTool against output / required (again, no Meta is reachable here).
	if IsCustomToolName(name) {
		return executeCustomTool(ctx, c, name, args)
	}

	return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
}

// executeBuiltin runs one built-in tool: identity hard gate -> argument normalisation and required check -> timeout wrapper ->
// execution.
func executeBuiltin(ctx context.Context, c *Context, tool Tool, name string, args map[string]any) (any, error) {
	// Meta tool hard gate: management tools (the categories in supervisorOnlyCategories) are usable by a supervisor only.
	// The grant catalog is already filtered by identity; this is defence in depth at the execution layer - even with a
	// misconfigured grant or a historical leftover, a non-supervisor cannot run them.
	if IsSupervisorOnlyTool(name) && !c.IsSupervisor {
		return nil, fmt.Errorf("tool %s is restricted to supervisor agents", name)
	}

	meta := tool.Meta()

	// Capability hard gate: a capability the platform has not enabled for this tenant is refused right here at the execution
	// layer.
	// Greying it out in the catalog is only a hint - an agent that already bound the tool still holds the old tool list, so
	// it has to be caught here.
	//
	// The name uses ShortLabel: this is an execution-layer error and must match what the caller just tried to do; Label's
	// scenario enumeration is for the admin capability cards, and putting it here would mislead whenever the tier and the
	// scenario disagree (see capability.go).
	if key := CapabilityOf(meta.Category); key != "" && c.Caps != nil && !c.Caps[key] {
		return nil, fmt.Errorf("tool %s requires capability %s (%s), which the platform has not granted this tenant", name, key, capability.ShortLabel(key))
	}

	// Argument alias normalisation: map a parameter name the model invented (e.g. skill_name) back to the schema's canonical
	// name (name)
	normalizeArgs(meta, args)
	if err := validateRequired(meta, args); err != nil {
		return nil, err
	}

	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	return tool.Execute(ctx, c, args)
}

// argAliases is the compatibility mapping from common "parameter names the model invents" to the tool schema's canonical
// names.
// Rule: the source name is renamed only when the tool **really declares** the canonical name (it is in properties) and the
// arguments lack it. So a tool's own legitimate parameters are never touched - call_agent's agent_name and bind_skill's
// agent/skill are canonical names and do not take part in the mapping (their properties have no name / path).
var argAliases = map[string][]string{
	"name":     {"skill_name", "agent_name", "tool_name", "skill"},
	"path":     {"file_path", "filepath", "filename", "file"},
	"dir":      {"dir_path", "directory", "folder"},
	"agent_md": {"agent_markdown"},
	"skill_md": {"skill_markdown"},
}

// normalizeArgs moves alias parameters to the canonical name declared in the schema's properties; when no source is found the
// arguments are returned as is.
func normalizeArgs(meta ToolMeta, args map[string]any) {
	if len(args) == 0 {
		return
	}
	props, _ := meta.Parameters["properties"].(map[string]any)
	for canonical, aliases := range argAliases {
		if props != nil {
			if _, declared := props[canonical]; !declared {
				continue // the tool has no such canonical parameter, do not map
			}
		}
		if _, ok := args[canonical]; ok {
			continue // the canonical name is already given, do not overwrite
		}
		for _, alias := range aliases {
			if v, ok := args[alias]; ok {
				args[canonical] = v
				delete(args, alias)
				break
			}
		}
	}
}

// validateRequired checks whether args contains the required fields declared by the schema.
// The error carries "the arguments actually received" and "the parameters this tool expects", so the model can correct itself.
func validateRequired(meta ToolMeta, args map[string]any) error {
	required, _ := meta.Parameters["required"].([]any)
	var missing []string
	for _, r := range required {
		key, _ := r.(string)
		if key == "" {
			continue
		}
		if _, ok := args[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("tool %s: missing required argument(s) %s; received %s; expected params: %s",
		meta.Name, quoteList(missing), mapKeys(args), propKeys(meta))
}

// quoteList renders ["a", "b"].
func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// mapKeys returns the keys of a map (ascending, for stable output).
func mapKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return quoteList(keys)
}

// propKeys returns the property names declared by the schema (ascending).
func propKeys(meta ToolMeta) string {
	props, _ := meta.Parameters["properties"].(map[string]any)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return quoteList(keys)
}
