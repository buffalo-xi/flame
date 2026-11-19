// Package toolgrant implements the parsing and expansion side of the tool grant system:
//   - ref syntax parsing and validation (ValidateRef / ParseRef)
//   - the referenceable catalog (Catalog: natural groups, preset groups, tool groups, single tools)
//   - final tool-set resolution (Resolver.Resolve: required ∪ on-demand candidates − deny)
//
// ref syntax (uniform type:subject[:member], parsed with SplitN(ref, ":", 3), and the third segment kept
// verbatim so tool names containing ':' survive):
//
//	builtin:file | builtin:exec | builtin:shell | builtin:browser | builtin:agent | builtin:skill | builtin:super   (built-in category groups)
//	builtin:<toolName>                                                            (a single built-in tool)
//	mcp:<connName>[:<toolName>]                                                   (external MCP connection)
//	rpc:<connName>[:<toolName>]                                                   (external RPC connection)
//	local:rpc[:<endpointName>]                                                    (local endpoint, rpc protocol only, workflow endpoints only)
//	plugin:<toolName>                                                             (on-disk Python plugin, one ref per tool)
//	custom:<toolName>                                                             (tenant custom tool, one ref per tool)
//	group:<groupName>                                                             (tool group, tenant defined)
//	preset:<presetName>                                                           (preset group, built into the code)
//
// Legacy alias: custom:<groupName> is the old spelling of group:<groupName>, recognised in ParseRef only
// (read compatibility); no write path produces it any more. See the notes on LegacyScopeCustom.
package toolgrant

import (
	"fmt"
	"regexp"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// ref prefixes (scopes).
const (
	ScopeBuiltin = "builtin" // built-in tools
	ScopeMCP     = "mcp"     // external MCP connection
	ScopeRPC     = "rpc"     // external RPC connection
	ScopeLocal   = "local"   // local endpoint (rpc protocol only)
	ScopePlugin  = "plugin"  // on-disk Python plugin (one ref per tool, no third segment accepted)
	ScopeCustom  = "custom"  // tenant custom tool (one ref per tool, no third segment accepted)
	ScopeGroup   = "group"   // tool group (tenant defined; the group name is the ref subject)
	ScopePreset  = "preset"  // preset group (built into the code)
)

// LocalProtocolRPC is the only protocol local endpoints currently open (the others exist for external exposure and stay out of the
// grant system).
const LocalProtocolRPC = "rpc"

// LocalTargetFlow is the only target type in the grant system for local endpoints (workflows).
//
// Agent endpoints stay out of the tool catalog, for two reasons:
//  1. calls between agents already have a controlled channel, call_agent - agent_relations (no row means refusal) +
//     IsRunnable + "main cannot be called by other agents"; whereas a direct call from inside an endpoint (a flow's rpc node with
//     target_type=endpoint) goes through none of those gates, so exposing it as a tool would be a back door;
//  2. an endpoint name always equals the entity name (svcendpoints.Create enforces it), so an agent and a flow with the same name
//     collide both in the local:rpc:<name> ref and in the local__<name> tool name (a silent map overwrite).
//
// Aligned with svcendpoints' literal "flow" (that repository validates target_type ∈ {agent, flow}); a test guards it.
const LocalTargetFlow = "flow"

// MaxRefLength is the maximum ref length (aligned with the DB column varchar(160)).
const MaxRefLength = 160

// namePattern is the allowed character set for connection / endpoint / group / tool names (loose: letters, digits and _ - .).
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ParsedRef is a parsed reference.
type ParsedRef struct {
	Scope   string // builtin | mcp | rpc | local | plugin | script | group | preset
	Subject string // builtin: category or tool name; mcp/rpc: connection name; local: protocol name; group/preset: group name
	Member  string // mcp/rpc: tool name; local: endpoint name; empty otherwise
}

// ParseRef parses a ref; an illegal syntax returns an error.
// Note that this is format parsing only: it does not check "does that connection / tool exist" (that is Catalog's job).
func ParseRef(ref string) (*ParsedRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("ref is empty")
	}
	if len(ref) > MaxRefLength {
		return nil, fmt.Errorf("ref too long (max %d)", MaxRefLength)
	}
	parts := strings.SplitN(ref, ":", 3)
	p := &ParsedRef{Scope: parts[0]}
	if len(parts) > 1 {
		p.Subject = parts[1]
	}
	if len(parts) > 2 {
		p.Member = parts[2]
	}

	switch p.Scope {
	case ScopeBuiltin, ScopeMCP, ScopeRPC, ScopeLocal, ScopePlugin, ScopeCustom, ScopeGroup, ScopePreset:
	default:
		return nil, fmt.Errorf("unsupported ref scope %q", p.Scope)
	}
	if p.Subject == "" {
		return nil, fmt.Errorf("ref %q missing subject", ref)
	}
	if !namePattern.MatchString(p.Subject) {
		return nil, fmt.Errorf("invalid subject in ref %q", ref)
	}
	if p.Member != "" && !namePattern.MatchString(p.Member) {
		return nil, fmt.Errorf("invalid member in ref %q", ref)
	}
	// Local endpoints open the rpc protocol only
	if p.Scope == ScopeLocal && p.Subject != LocalProtocolRPC {
		return nil, fmt.Errorf("local endpoint protocol %q is not allowed (only %q)", p.Subject, LocalProtocolRPC)
	}
	// These scopes do not accept a third segment. plugin is one of them: it is one ref per tool, the implementation is decided by
	// the tenant configuration, and the grant surface stays at "may use web_search" rather than "may use tavily".
	if (p.Scope == ScopeBuiltin || p.Scope == ScopePlugin || p.Scope == ScopeCustom ||
		p.Scope == ScopeGroup || p.Scope == ScopePreset) && p.Member != "" {
		return nil, fmt.Errorf("ref %q does not accept a third segment", ref)
	}
	return p, nil
}

// ValidateRef validates whether a ref may be used as a grant item / group member.
// With allowGroup=false, group: is refused (group members may not reference tool groups, to avoid nesting).
func ValidateRef(ref string, allowGroup bool) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if !allowGroup && p.Scope == ScopeGroup {
		return fmt.Errorf("tool group reference is not allowed here")
	}
	return nil
}

// IsGroup reports whether the ref has "group" shape:
// builtin categories, mcp/rpc connections (no member), local:rpc (no member), group:, preset:.
// builtin additionally needs the set of built-in categories to tell a "category" from a "tool".
func (p *ParsedRef) IsGroup(builtinCategories map[string]bool) bool {
	switch p.Scope {
	case ScopeGroup, ScopePreset:
		return true
	case ScopeMCP, ScopeRPC:
		return p.Member == ""
	case ScopeLocal:
		return p.Member == ""
	case ScopePlugin:
		// A plugin tool only has a single-tool ref: group and tool coincide, so grants always write it as a tool
		return false
	case ScopeBuiltin:
		return p.Member == "" && builtinCategories[p.Subject]
	}
	return false
}

// ItemTypeOfRef derives item_type from the ref shape (group -> models.GrantItemGroup, single tool -> models.GrantItemTool).
//
// Every write entry point (meta tools, admin-side agent grants / skill dependencies / group member maintenance) must derive it this
// way and **must not accept it from the caller**. The reason: when a caller supplies both ref and item_type the two easily
// contradict (item_type=tool + ref=group:x, or item_type=group + ref=builtin:read_file), and a contradictory row raises no error,
// it only silently corrupts the frontend's checkbox state - a reader then believes the data is broken.
//
// The model-side meta tools do not expose item_type in the first place; deriving it here closes the loop so both entry points share
// one source.
func ItemTypeOfRef(ref string) string {
	p, err := ParseRef(ref)
	if err != nil {
		return models.GrantItemTool
	}
	if p.IsGroup(BuiltinCategories()) {
		return models.GrantItemGroup
	}
	return models.GrantItemTool
}

// ToolNameOfRef returns the ref's name in the LLM tool list (used for runtime binding):
//
//	builtin:read_file        -> read_file
//	mcp:fs:read_file         -> mcp__fs__read_file
//	rpc:erp:create_order     -> rpc__erp__create_order
//	local:rpc:weather        -> local__weather
//	plugin:web_search        -> web_search
//
// A group-shaped ref has no single tool name, so an empty string is returned.
func ToolNameOfRef(ref string) string {
	p, err := ParseRef(ref)
	if err != nil {
		return ""
	}
	switch p.Scope {
	case ScopeBuiltin:
		return p.Member
	case ScopeMCP, ScopeRPC:
		if p.Member == "" {
			return ""
		}
		return fmt.Sprintf("%s__%s__%s", p.Scope, p.Subject, p.Member)
	case ScopeLocal:
		if p.Member == "" {
			return ""
		}
		return "local__" + p.Member
	case ScopePlugin:
		// A plugin tool's name is its own (no prefix): the model must see a clean web_search
		return p.Subject
	case ScopeCustom:
		// Custom tools are prefixed: their name comes from the tenant, and it has to stay separate from built-in / plugin names
		return tools.CustomToolPrefix + p.Subject
	}
	return ""
}
