package toolgrant

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/service/plugin"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
	"flame/nexus/shared/repository/customtools"
	"flame/nexus/shared/repository/mcpconnections"
	pluginsrepo "flame/nexus/shared/repository/plugins"
	"flame/nexus/shared/repository/rpcconnections"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/repository/toolgrants"
	"flame/tools/xlog"
	"flame/tools/xsql"
)

// listLimit is the cap used when listing connections / endpoints (far above the real count, so the default paging value never
// truncates it).
const listLimit = 1000

// Unavailable attribution (the values of ToolInfo.UnavailableKind / GroupInfo.UnavailableKind).
//
// Why not collapse them into one "unavailable": these classes point at **different fix entry points** -
// capability and plugin suspension belong to the platform administrator, while plugin enablement / connections / endpoints
// belong to the tenant.
// Collapsing them makes the user fix the wrong layer (enabling on the tenant side over and over while the platform never
// granted it at all).
const (
	UnavailableCapability     = "capability"      // the platform has not granted this kind of capability (exec / browser ...)
	UnavailablePluginPolicy   = "plugin_policy"   // the platform has disabled this plugin for this tenant
	UnavailablePluginInactive = "plugin_inactive" // the platform did not disable it; the tenant has not enabled it
	UnavailableConnection     = "connection"      // the MCP / RPC connection is not enabled
	UnavailableEndpoint       = "endpoint"        // the local rpc endpoint is not exposed
)

// The **codes** for unavailable reasons (ToolInfo.UnavailableCode / GroupInfo.UnavailableCode).
//
// Finer than UnavailableKind: kind says "which layer it belongs to" (capability / plugin / connection), code says "which exact
// sentence", and the frontend looks it up in the locale bundle. Both are needed, because kind also carries the panel badge
// semantics.
//
// The values are an API contract (the frontend resolves copy via toolUnavailable.<code>); changing one blank the UI - like kind,
// this is the "change it and the UI silently breaks" category, so search the frontend first.
const (
	UnavailableCodeCapability     = "capability"           // param: key
	UnavailableCodePluginBlocked  = "plugin_blocked"       // param: plugin (disabled globally by the platform)
	UnavailableCodePluginDisabled = "plugin_disabled"      // param: plugin (disabled for this tenant by the platform)
	UnavailableCodePluginInactive = "plugin_inactive"      // param: plugin (not enabled by the tenant)
	UnavailableCodePolicyReadFail = "policy_read_failed"   // no params
	UnavailableCodeMcpConnection  = "mcp_conn_disabled"    // param: connection
	UnavailableCodeRpcConnection  = "rpc_conn_disabled"    // param: connection
	UnavailableCodeEndpoint       = "endpoint_not_exposed" // param: endpoint
)

// unavail is one "unavailable reason": a code plus params for frontend localisation, and a short reason for diagnostics.
//
// Both are kept: the backend does not know the UI language (see nexus/docs/i18n.md: return codes and params only), but logs,
// older clients and codes the frontend has not translated yet still need one readable sentence.
// The frontend localises from code + params first and falls back to reason.
type unavail struct {
	code   string
	params map[string]string
	reason string
}

// unavailFail is the "read failed" reason (no params): all platform-side failures with no clickable fix entry point.
func unavailFail(reason string) unavail {
	return unavail{code: UnavailableCodePolicyReadFail, reason: reason}
}

// empty means no reason at all (the tool is available). A struct with map fields cannot be compared with ==, hence this check.
func (u unavail) empty() bool { return u.code == "" && u.reason == "" }

// ToolInfo is a single tool (a leaf node after expansion).
type ToolInfo struct {
	Ref            string `json:"ref"`  // builtin:read_file / mcp:fs:read_file / plugin:web_search
	Name           string `json:"name"` // LLM tool name: read_file / mcp__fs__read_file / web_search
	Label          string `json:"label"`
	Description    string `json:"description"`
	Available      bool   `json:"available"`       // whether the owning connection / endpoint is usable (connection off / endpoint retracted / plugin inactive / capability not granted -> false)
	Source         string `json:"source"`          // source group: builtin:file / mcp:fs / local:rpc / plugin:web_search / group:xxx
	SupervisorOnly bool   `json:"supervisor_only"` // bindable / runnable by the supervisor only
	// UnavailableReason is why it is unavailable (human readable, shown directly).
	// The three reasons must stay separable: capability not granted / connection or endpoint not enabled / the tool itself is
	// unusable - collapsing them into one "unavailable" makes the user fix the wrong layer.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// UnavailableKind is the attribution (values in the Unavailable* constants): the frontend renders a different badge from it
	// without parsing the reason text. Empty = available.
	UnavailableKind string `json:"unavailable_kind,omitempty"`
	// UnavailableCode is the exact reason code (values in the UnavailableCode* constants): the frontend looks it up in the
	// locale bundle, so fine-grained cases such as "plugin disabled globally / disabled for this tenant" are translatable too,
	// not just the kind.
	// Empty = available, or an older backend - the frontend then falls back to UnavailableReason.
	UnavailableCode string `json:"unavailable_code,omitempty"`
	// UnavailableParams are the interpolation params of UnavailableCode (e.g. {"plugin": "web_search"}).
	UnavailableParams map[string]string `json:"unavailable_params,omitempty"`
	// Parameters is the input schema (JSON Schema object).
	//
	// Built-in tools may omit it (the agent side takes Meta().Parameters from tools.List() on the fly),
	// but external tools (mcp / plugin) have no such fallback - a plugin tool must carry its schema,
	// otherwise the model is declared an empty input.
	Parameters map[string]any `json:"parameters,omitempty"`
}

// GroupInfo is a referenceable group.
type GroupInfo struct {
	Ref         string     `json:"ref"`  // builtin:file / mcp:fs / rpc:erp / local:rpc / group:xxx / preset:xxx
	Name        string     `json:"name"` // display name
	Kind        string     `json:"kind"` // builtin | mcp | rpc | local | custom | preset
	Description string     `json:"description"`
	Available   bool       `json:"available"`
	Items       []ToolInfo `json:"items"`
	SourceID    string     `json:"source_id"`    // connection id (mcp / rpc groups): needed by frontend actions such as "refresh the tool list"
	RefreshedAt string     `json:"refreshed_at"` // when the tool list was last refreshed / updated (RFC3339; connection and endpoint groups only)
	// UnavailableReason is why the group is unavailable (same as ToolInfo.UnavailableReason).
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// UnavailableKind is the attribution for an unavailable group (same as ToolInfo.UnavailableKind).
	UnavailableKind string `json:"unavailable_kind,omitempty"`
	// UnavailableCode is the reason code for an unavailable group (same as ToolInfo.UnavailableCode).
	UnavailableCode string `json:"unavailable_code,omitempty"`
	// UnavailableParams are the reason params for an unavailable group (same as ToolInfo.UnavailableParams).
	UnavailableParams map[string]string `json:"unavailable_params,omitempty"`
	// Synthetic true = a category container, **not a grantable object** (the plugin and custom-tool categories).
	//
	// Their members are tenant-side entities whose count varies per tenant: one card per tool would degrade the group name into a
	// tool name (the "compound interest" group), which is hard to recognise and also misleads item_type into group. Here one card
	// holds every member, and the grant still targets each member's own tool ref (a container ref looks like "custom:*" and
	// ValidateRef rejects it).
	Synthetic bool `json:"synthetic,omitempty"`
}

// markUnavailable writes a complete reason onto a leaf (Available is set false as well, so the frontend never sees the
// contradictory state "reason present, still available").
func (t *ToolInfo) markUnavailable(kind string, u unavail) {
	t.Available = false
	t.UnavailableKind = kind
	t.UnavailableCode = u.code
	t.UnavailableParams = u.params
	t.UnavailableReason = u.reason
}

// markUnavailable is the ToolInfo counterpart for groups: the group-level reason is copied up from the leaf, and the fields must
// stay in sync, otherwise the card is greyed out with no reason.
func (g *GroupInfo) markUnavailable(kind string, u unavail) {
	g.Available = false
	g.UnavailableKind = kind
	g.UnavailableCode = u.code
	g.UnavailableParams = u.params
	g.UnavailableReason = u.reason
}

// PresetGroup is a preset group definition (built into the code, not editable).
type PresetGroup struct {
	Name string `json:"name"`
	// Label / Description are language-neutral on purpose: they are the preset **identifier** and a short English summary.
	// The UI resolves the user-visible copy from its locale bundle by Name (see the agent tool-grant panel), so the backend
	// never ships display text.
	Label       string                 `json:"label"`
	Description string                 `json:"description"`
	Items       []models.ToolGroupItem `json:"items"`
}

// PresetGroups are the built-in preset groups: cross-category common combinations for one-click grants.
//
// Only preset:default is kept (the default set of a new agent, see models.DefaultAgentTools) -
// the other per-category presets (the former readonly / file-write / exec / collaborate) were removed:
// the natural groups (builtin:file / builtin:exec / builtin:agent / builtin:skill) plus multi-selecting
// individual tools already cover them, and keeping them duplicated the default set.
//
// Management tools (categories agent-admin / skill-admin / tool-grant / custom-tool) are **not** listed as a preset here - they
// are carried by their natural groups and are visible to the super agent only (see Catalog.builtinTools).
//
// Note: removing a preset makes existing grant rows that reference it resolve to empty (ToolsOfRef returns an empty list plus a
// warning - no error, but the tools disappear). Run sql/agent_preset_cleanup.sql to expand old references when upgrading.
var PresetGroups = []PresetGroup{
	{
		Name: "default", Label: "default",
		Description: "Tools a new agent gets by default: read and write its own workspace, look up agents and skills, delegate (no command execution)",
		Items:       models.DefaultAgentToolGrants(),
	},
}

// Catalog is the referenceable catalog: built-in tools, natural groups, preset groups, custom groups, connections and local
// endpoints, disk plugins.
type Catalog struct {
	schema       string
	isSupervisor bool // viewing identity: in a non-supervisor view the catalog hides "supervisor only" management tools (categories in supervisorOnlyCategories)
	grants       toolgrants.Repository
	mcp          mcpconnections.Repository
	rpc          rpcconnections.Repository
	eps          svcendpoints.SvcEndpointRepository
	plugins      pluginsrepo.Repository
	scripts      customtools.Repository
	policies     policyMapFunc // platform policy source (straight to the database by default; tests inject a fake)
	// caps are the capabilities the platform has granted this tenant (see shared/capabilitypolicy.ResolveAll).
	// nil = unrestricted (callers without tenant semantics only); when non-nil a missing key counts as not granted (deny by
	// default).
	caps map[string]bool
	// mode is the definition source for custom tools: a production run uses the current version (same source as the script
	// directory), draft mode uses the draft.
	// The default (zero value) is draft - the admin grant panel must see "what is being edited".
	mode tools.DefinitionMode
}

// policyMapFunc fetches every effective policy of one tenant at once (see shared/pluginpolicy.List).
type policyMapFunc func(schema string) (map[string]pluginpolicy.Policy, error)

// NewCatalog creates a catalog (schema is the tenant schema; isSupervisor says whether the viewing agent holds the elevated
// attribute).
//
// In a non-supervisor view, management tools (categories in tools.SupervisorOnlyCategories) appear in no group at all - natural
// groups do not emit them, and custom / preset group expansion filters them out as well (an empty group disappears entirely).
func NewCatalog(schema string, grants toolgrants.Repository, isSupervisor bool) *Catalog {
	return &Catalog{
		schema:       schema,
		isSupervisor: isSupervisor,
		grants:       grants,
		mcp:          mcpconnections.NewRepository(),
		rpc:          rpcconnections.NewRepository(),
		eps:          svcendpoints.NewRepository(),
		plugins:      pluginsrepo.NewRepository(),
		// Which custom tools exist comes from the tools/ directory (customtoolstore merges the contract and the policy),
		// the same source as the execution layer: a tool present there is visible and grantable in the grant catalog.
		scripts:  customtoolstore.New(config.AgentRootDir(), schema),
		policies: pluginpolicy.List,
	}
}

// WithDefinitionMode makes the catalog read custom tool definitions per run mode.
//
// A production run (MCP / RPC / endpoint / flow) must see "what this version released": only tools in the current version exist,
// and the parameter schema comes from that version's TOOL.json - otherwise editing a draft's parameters or creating a tool would
// change production immediately.
// The admin grant catalog must not call it (it needs to see the draft).
func (c *Catalog) WithDefinitionMode(mode tools.DefinitionMode) *Catalog {
	c.mode = mode
	return c
}

// WithCapabilities injects the platform capability view (see shared/capabilitypolicy.ResolveAll).
//
// nil = unrestricted; once passed, any capability absent from the map counts as not granted (deny by default).
// It only affects the catalog's availability marks (greyed out plus a reason); the real interception happens in the execution
// layer: a capability can be revoked between two calls while an agent that already bound the tools still holds its old tool list.
func (c *Catalog) WithCapabilities(allowed map[string]bool) *Catalog {
	c.caps = allowed
	return c
}

// capabilityBlocked reports whether the platform blocked a capability (a nil view = nothing is blocked).
func (c *Catalog) capabilityBlocked(key string) (bool, unavail) {
	if c.caps == nil || key == "" || c.caps[key] {
		return false, unavail{}
	}
	// Only the key goes in: it is the API contract the frontend localises from (capability.Label is UI copy and must not be mixed
	// into a diagnostic string).
	return true, unavail{
		code:   UnavailableCodeCapability,
		params: map[string]string{"key": key},
		reason: "the platform has not granted capability " + key + " to this tenant; a platform administrator must enable it",
	}
}

// policyMap fetches this tenant's effective policy table; the second return value means "read failed".
//
// A read failure must not be treated as the default: the default is "open", while "cannot read" does not mean "nobody disabled it".
// When the caller gets failed=true it should show plugins as **unavailable** (the conservative direction);
// the real interception still happens in Runner.Invoke (a read failure there is a hard error).
func (c *Catalog) policyMap() (map[string]pluginpolicy.Policy, bool) {
	if c.policies == nil {
		return map[string]pluginpolicy.Policy{}, false
	}
	all, err := c.policies(c.schema)
	if err != nil {
		xlog.Error("failed to read plugin policy schema=%s (shown as disabled): %v", c.schema, err)
		return map[string]pluginpolicy.Policy{}, true
	}
	return all, false
}

func (c *Catalog) auth() *auth.AuthInfo { return &auth.AuthInfo{Schema: c.schema} }

// BuiltinCategories is the set of built-in categories (the builtin:<category> groups).
//
// tools.CategorySuper is listed but **owns no tools**: it is the aggregated category from before the split, kept only as a
// resolution entry for existing grant rows (builtin:super), expanding into the union of the five management groups (see
// builtinByCategory).
// It is not part of builtinGroupCategories, so the panel gains no extra card duplicating the four groups.
func BuiltinCategories() map[string]bool {
	return map[string]bool{
		tools.CategoryFile:       true,
		tools.CategoryExec:       true,
		tools.CategoryShell:      true,
		tools.CategoryScript:     true,
		tools.CategoryBrowser:    true,
		tools.CategoryAgent:      true,
		tools.CategorySkill:      true,
		tools.CategorySkillOwn:   true,
		tools.CategoryAgentAdmin: true,
		tools.CategorySkillAdmin: true,
		tools.CategoryToolGrant:  true,
		tools.CategoryCustomTool: true,
		tools.CategoryFlowAdmin:  true,
		tools.CategorySuper:      true,
	}
}

// BuiltinTools is every built-in tool (with its category).
//
// This function does **no identity filtering** - it is the baseline for "open by default" (the super agent) and for execution-layer
// decisions; for a view-filtered list use (*Catalog).VisibleBuiltinTools.
func BuiltinTools() []ToolInfo {
	list := tools.List()
	out := make([]ToolInfo, 0, len(list))
	for _, t := range list {
		m := t.Meta()
		category := m.Category
		// No category: it joins no category group (Groups does not emit it and builtinByCategory never matches it), and Source is
		// left empty so it is not mistaken for a group name.
		source := ""
		if category != "" {
			source = ScopeBuiltin + ":" + category
		}
		out = append(out, ToolInfo{
			Ref:            ScopeBuiltin + ":" + m.Name,
			Name:           m.Name,
			Label:          m.Name,
			Description:    m.Description,
			Available:      true,
			Source:         source,
			SupervisorOnly: tools.IsSupervisorOnlyCategory(category),
			Parameters:     m.Parameters,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// VisibleBuiltinTools is the view-filtered list of built-in tools (used by the admin catalog endpoint).
func (c *Catalog) VisibleBuiltinTools() []ToolInfo { return c.builtinTools() }

// builtinTools is the view-filtered built-in tool list: a non-super agent has the supervisor-only tools removed, and availability
// is marked from platform capabilities.
//
// The capability gate must sit on the leaf, not just grey out groups in Groups:
// AllTools / ToolsOfRef / VisibleBuiltinTools all take their tools from here, and missing one produces the contradiction "the group
// says not granted, the tool says available"; Resolve's availability filtering also depends on the leaf's Available.
func (c *Catalog) builtinTools() []ToolInfo {
	list := BuiltinTools()
	out := make([]ToolInfo, 0, len(list))
	for _, t := range list {
		if !c.isSupervisor && t.SupervisorOnly {
			continue
		}
		if blocked, why := c.capabilityBlocked(tools.CapabilityOf(builtinCategory(t))); blocked {
			t.markUnavailable(UnavailableCapability, why)
		}
		out = append(out, t)
	}
	return out
}

// builtinCategory is a built-in tool's category (from Source = "builtin:<category>"; empty when uncategorised).
func builtinCategory(t ToolInfo) string {
	return strings.TrimPrefix(t.Source, ScopeBuiltin+":")
}

// builtinByCategory is the tools of one built-in category (view-filtered).
//
// builtin:super is special-cased as "the union of the five management groups": it is the aggregated category from before the split
// and no tool belongs to it now, but existing grant rows may still say builtin:super - without the expansion those agents would
// silently lose every management capability.
// This is transitional code; remove it together with tools.CategorySuper once existing data is clean.
func (c *Catalog) builtinByCategory(category string) []ToolInfo {
	if category == tools.CategorySuper {
		out := make([]ToolInfo, 0)
		for _, sub := range tools.SupervisorOnlyCategories() {
			out = append(out, c.builtinByCategory(sub)...)
		}
		return out
	}
	out := make([]ToolInfo, 0)
	for _, t := range c.builtinTools() {
		if t.Source == ScopeBuiltin+":"+category {
			out = append(out, t)
		}
	}
	return out
}

// ===== Connections and endpoints =====

func (c *Catalog) mcpConns() ([]models.McpConnection, error) {
	list, _, err := c.mcp.List(c.auth(), &mcpconnections.ListMcpConnectionsRequest{Page: 1, Limit: listLimit})
	return list, err
}

func (c *Catalog) rpcConns() ([]models.RpcConnection, error) {
	list, _, err := c.rpc.List(c.auth(), &rpcconnections.ListRpcConnectionsRequest{Page: 1, Limit: listLimit})
	return list, err
}

// localRPCConns are this tenant's endpoints with protocol=rpc that target a flow.
//
// Only target_type=flow is taken (see the two reasons on LocalTargetFlow: an agent endpoint would bypass the call_agent gate, and a
// same-named flow would collide on both the ref and the tool name).
// Narrowing it in one place takes effect on three paths at once: ToolsOfRef(local:rpc[:name]), AllTools and Groups.
//
// It uses ListAll rather than List: the whole endpoint catalog is loaded into memory here, and List's paging cap would make the
// 1001st endpoint and beyond vanish (see the comment on svcendpoints.ListAll).
func (c *Catalog) localRPCConns() ([]models.SvcEndpoint, error) {
	return c.eps.ListAll(c.auth(), &svcendpoints.ListSvcEndpointsRequest{
		Protocol:   LocalProtocolRPC,
		TargetType: LocalTargetFlow,
	})
}

// ===== Disk plugins =====

// pluginTool turns one discovered disk plugin into a tool leaf.
//
// Available means: the manifest exists, the tenant has enabled it ({schema}.plugins.is_active), and the platform has not disabled it
// (platform.tenant_plugins counts as disabled only with an explicit is_allowed=false).
// A plugin that was never configured **still** appears in the catalog (available=false): an administrator has to see it before
// knowing there is a tool to enable.
//
// ok=false means the tool is not in the registry (not discovered / dropped by the duplicate-name rule).
func (c *Catalog) pluginTool(name string) (ToolInfo, bool, error) {
	return c.pluginToolWith(name, nil)
}

// pluginToolWith is pluginTool with a pre-fetched policy table (for batch paths, avoiding N queries).
// pol == nil means it resolves policy per plugin itself.
func (c *Catalog) pluginToolWith(name string, pol map[string]pluginpolicy.Policy) (ToolInfo, bool, error) {
	reg := plugin.DefaultSource()
	if reg == nil {
		return ToolInfo{}, false, nil
	}
	m, ok := reg.Get(name)
	if !ok {
		return ToolInfo{}, false, nil
	}

	active := false
	p, err := c.plugins.Get(c.auth(), m.Kind, m.Name)
	switch {
	case err == nil:
		active = p.IsActive
	case errors.Is(err, sql.ErrNoRows):
		active = false
	default:
		return ToolInfo{}, false, err
	}

	failed := false
	if pol == nil {
		pol, failed = c.policyMap()
	}
	if failed {
		// The policy could not be read, so show it as disabled. The default is open, but "cannot read" does not mean "nobody disabled
		// it", and showing it as available would leave the user looking at a lit-up tool that cannot be called.
		ti := ToolInfo{
			Ref:         ScopePlugin + ":" + m.Name,
			Name:        m.Name,
			Label:       m.DisplayName,
			Description: m.Description,
			Source:      ScopePlugin + ":" + m.Name,
			Parameters:  m.Parameters,
		}
		ti.markUnavailable(UnavailablePluginPolicy,
			unavailFail("failed to read the platform plugin policy; shown as disabled for now, retry later"))
		return ti, true, nil
	}
	avail, kind, why := pluginUnavailable(m.Name, policyOf(pol, m.Name), active)

	ti := ToolInfo{
		Ref:         ScopePlugin + ":" + m.Name,
		Name:        m.Name,
		Label:       m.DisplayName,
		Description: m.Description,
		Available:   avail,
		Source:      ScopePlugin + ":" + m.Name,
		Parameters:  m.Parameters,
	}
	if !avail {
		ti.markUnavailable(kind, why)
	}
	return ti, true, nil
}

// pluginUnavailable is a plugin tool's availability and attribution (pure logic, easy to unit test).
//
// The two gates are reported separately, with the platform first: when the platform disabled it, a tenant enabling it has no effect -
// reporting "not enabled" first would send the tenant to click enable and still find it unusable, a wasted round trip.
// "Disabled for this tenant" and "disabled globally" must also stay apart: the first affects this tenant only, the second waits for
// the platform to lift the gate.
func pluginUnavailable(name string, pol pluginpolicy.Policy, active bool) (avail bool, kind string, u unavail) {
	plugin := map[string]string{"plugin": name}
	switch {
	case pol.Blocked:
		return false, UnavailablePluginPolicy, unavail{
			code:   UnavailableCodePluginBlocked,
			params: plugin,
			reason: "plugin " + name + " was disabled globally by the platform; a platform administrator must lift it under Plugins & Capabilities",
		}
	case !pol.Allowed:
		return false, UnavailablePluginPolicy, unavail{
			code:   UnavailableCodePluginDisabled,
			params: plugin,
			reason: "plugin " + name + " was disabled for this tenant by the platform; a platform administrator must restore it under Plugins & Capabilities",
		}
	case !active:
		return false, UnavailablePluginInactive, unavail{
			code:   UnavailableCodePluginInactive,
			params: plugin,
			reason: "the tenant has not enabled plugin " + name + "; enable it and pick an implementation under Tools & Endpoints -> Plugins",
		}
	}
	return true, "", unavail{}
}

// policyOf picks a policy out of the "fetched in one go" result; missing from the map = open (consistent with pluginpolicy.Default).
func policyOf(all map[string]pluginpolicy.Policy, name string) pluginpolicy.Policy {
	if p, ok := all[name]; ok {
		return p
	}
	return pluginpolicy.Default()
}

// pluginTools is every discovered plugin tool (sorted by name; the registry is already sorted).
func (c *Catalog) pluginTools() ([]ToolInfo, error) {
	reg := plugin.DefaultSource()
	if reg == nil {
		return nil, nil
	}
	snap := reg.Snapshot()
	pols, failed := c.policyMap()
	if failed {
		// When the batch fetch fails, mark every plugin as disabled - an empty table plus the open default would show everything as
		// available
		pols = make(map[string]pluginpolicy.Policy, len(snap.Tools))
		for _, m := range snap.Tools {
			pols[m.Name] = pluginpolicy.Policy{Allowed: false}
		}
	}
	out := make([]ToolInfo, 0, len(snap.Tools))
	for _, m := range snap.Tools {
		info, ok, err := c.pluginToolWith(m.Name, pols)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// mcpRefreshedAt / rpcRefreshedAt is when that connection's tool list was last refreshed (empty when there are no tools).
func (c *Catalog) mcpRefreshedAt(connID string) string {
	list, err := c.mcp.ListTools(c.auth(), connID)
	if err != nil {
		return ""
	}
	times := make([]xsql.Time, 0, len(list))
	for _, t := range list {
		times = append(times, t.UpdatedAt)
	}
	return maxTime(times)
}

func (c *Catalog) rpcRefreshedAt(connID string) string {
	list, err := c.rpc.ListTools(c.auth(), connID)
	if err != nil {
		return ""
	}
	times := make([]xsql.Time, 0, len(list))
	for _, t := range list {
		times = append(times, t.UpdatedAt)
	}
	return maxTime(times)
}

// maxTime returns the newest time (RFC3339); an empty list / zero value returns an empty string.
func maxTime(times []xsql.Time) string {
	var max time.Time
	for _, t := range times {
		if tt := time.Time(t); tt.After(max) {
			max = tt
		}
	}
	if max.IsZero() {
		return ""
	}
	return max.Format(time.RFC3339)
}

// toolSchema turns a connection tool's input_schema into the parameter object handed to the model.
//
// It must be passed through here: not giving built-in tools their parameters is fine (the agent side takes Meta().Parameters on the
// fly), but mcp / rpc tools have no such fallback - without it the model sees a tool with "no parameters" and either dares not call
// it or invents arguments that trip the required check.
//
// Both carriers must be recognised: the admin side writes map[string]any through gin binding, while the rpc/client discovery logic
// writes json.RawMessage (see the tools/list parsing in conn.go). xsql.JSONB.Unmarshal treats both as JSON.
//
// A schema whose top level is not an object falls back to nil (upstream would reject it, or the model would think the tool takes no
// parameters): the same direction as the isObjectSchema check in rpc/server listTools.
func toolSchema(j xsql.JSONB) map[string]any {
	if j.IsNil() {
		return nil
	}
	var m map[string]any
	if err := j.Unmarshal(&m); err != nil || m == nil {
		return nil
	}
	if t, _ := m["type"].(string); t != "" && t != "object" {
		return nil
	}
	// Fill in a missing type: the model side requires an object shape, and some upstreams reject the whole request without it.
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	return m
}

// customTools are this tenant's usable custom tools (status=1).
//
// A read failure **degrades to empty** with a log line: the catalog is the display / binding surface, and a read failure should not
// make the whole tool tree disappear; the real interception is in the execution layer (ExecuteTool -> executeCustomTool), where a
// read failure is a hard error.
func (c *Catalog) customTools() []ToolInfo {
	if c.scripts == nil || c.schema == "" {
		return nil
	}
	// A production run uses the "version view": a tool absent from the current version does not exist, and the parameter schema comes
	// from the contract inside the version.
	// The same source as the execution layer (ToolDir / CustomTools.Get); otherwise the model would call with the draft's parameters
	// while the script is the released one.
	var (
		list     []models.CustomTool
		err      error
		released bool
	)
	if store, ok := c.scripts.(*customtoolstore.Store); ok && c.mode.IsCurrent() {
		list, err = store.ListReleased()
		released = true
	}
	if !released {
		list, err = c.scripts.ListEnabled(c.schema)
	}
	if err != nil {
		xlog.Error("failed to read custom tools schema=%s (treated as none): %v", c.schema, err)
		return nil
	}
	// The capability gate moves onto the leaf: a custom tool runs through /v1/exec (a one-shot process) and belongs to the
	// sandbox.exec tier, so it is greyed out when the platform has not granted it - the check must agree with the execution layer
	// (tools/executeCustomTool), otherwise the catalog says available while the call is rejected.
	// The group-level reason is copied from the leaf by Groups, so the check lives in this one place.
	blocked, why := c.capabilityBlocked(capability.Exec)

	out := make([]ToolInfo, 0, len(list))
	for i := range list {
		t := &list[i]
		ti := ToolInfo{
			Ref:         ScopeCustom + ":" + t.Name,
			Name:        tools.CustomToolPrefix + t.Name,
			Label:       t.Label(),
			Description: t.Description,
			Available:   true,
			Source:      ScopeCustom,
			Parameters:  t.ParamSchema(),
		}
		if blocked {
			ti.markUnavailable(UnavailableCapability, why)
		}
		out = append(out, ti)
	}
	return out
}

// ===== ref expansion =====

// ToolsOfRef expands a ref into a tool list (a group -> its members; a single tool -> itself).
// A missing reference (connection / endpoint / group / tool deleted) returns an empty list, not an error.
// In a non-super-agent view, built-in tools in the super category do not appear in the result (neither a single ref nor a group
// expansion).
func (c *Catalog) ToolsOfRef(ref string) ([]ToolInfo, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	switch p.Scope {
	case ScopeBuiltin:
		if BuiltinCategories()[p.Subject] {
			return c.builtinByCategory(p.Subject), nil
		}
		for _, t := range c.builtinTools() {
			if t.Ref == ref {
				return []ToolInfo{t}, nil
			}
		}
		return nil, nil

	case ScopeMCP, ScopeRPC:
		return c.toolsOfConnection(p), nil

	case ScopePlugin:
		info, ok, err := c.pluginTool(p.Subject)
		if err != nil || !ok {
			return nil, err
		}
		return []ToolInfo{info}, nil

	case ScopeCustom:
		for _, t := range c.customTools() {
			if t.Ref == ref {
				return []ToolInfo{t}, nil
			}
		}
		return nil, nil

	case ScopeLocal:
		eps, err := c.localRPCConns()
		if err != nil {
			return nil, err
		}
		out := make([]ToolInfo, 0, len(eps))
		for _, ep := range eps {
			if p.Member != "" && !strings.EqualFold(ep.Name, p.Member) {
				continue
			}
			ti := ToolInfo{
				Ref:         ScopeLocal + ":" + LocalProtocolRPC + ":" + ep.Name,
				Name:        "local__" + ep.Name,
				Label:       ep.Name,
				Description: ep.Description,
				Available:   bool(ep.Exposed),
				Source:      ScopeLocal + ":" + LocalProtocolRPC,
				// A flow's input parameters are the tool's parameters: without them the model sees a "parameterless tool" and either
				// dares not call it or invents arguments (same convention as the mcp / rpc branches).
				Parameters: toolSchema(ep.InputSchema),
			}
			if !ti.Available {
				ti.markUnavailable(endpointIssue(ep.Name))
			}
			out = append(out, ti)
		}
		return out, nil

	case ScopeGroup:
		g, err := c.grants.GetGroup(c.schema, p.Subject)
		if err != nil || g == nil {
			return nil, err
		}
		return c.expandItems(g.Items, ref)

	case ScopePreset:
		for _, pg := range PresetGroups {
			if pg.Name == p.Subject {
				return c.expandItems(pg.Items, ref)
			}
		}
		return nil, nil
	}
	return nil, nil
}

// expandItems expands group / preset members (a member can only be a natural group or a single tool; custom groups never nest).
func (c *Catalog) expandItems(items []models.ToolGroupItem, source string) ([]ToolInfo, error) {
	out := make([]ToolInfo, 0)
	seen := map[string]bool{}
	for _, it := range items {
		sub, err := c.ToolsOfRef(it.Ref)
		if err != nil {
			return nil, err
		}
		for _, t := range sub {
			if seen[t.Ref] {
				continue
			}
			seen[t.Ref] = true
			t.Source = source // record the source group so the UI can show which group the tool came from
			out = append(out, t)
		}
	}
	return out, nil
}

// toolsOfConnection expands mcp:<conn>[:tool] / rpc:<conn>[:tool].
func (c *Catalog) toolsOfConnection(p *ParsedRef) []ToolInfo {
	out := make([]ToolInfo, 0)
	if p.Scope == ScopeMCP {
		conns, err := c.mcpConns()
		if err != nil {
			return out
		}
		for _, conn := range conns {
			if !strings.EqualFold(conn.Name, p.Subject) {
				continue
			}
			list, err := c.mcp.ListTools(c.auth(), conn.ID)
			if err != nil {
				return out
			}
			for _, t := range list {
				if p.Member != "" && !strings.EqualFold(t.Name, p.Member) {
					continue
				}
				ti := ToolInfo{
					Ref:         ScopeMCP + ":" + conn.Name + ":" + t.Name,
					Name:        ScopeMCP + "__" + conn.Name + "__" + t.Name,
					Label:       t.Name,
					Description: t.Description,
					Available:   conn.IsActive,
					Source:      ScopeMCP + ":" + conn.Name,
					Parameters:  toolSchema(t.InputSchema),
				}
				if !ti.Available {
					ti.markUnavailable(mcpConnIssue(conn.Name))
				}
				out = append(out, ti)
			}
			return out
		}
		return out
	}

	conns, err := c.rpcConns()
	if err != nil {
		return out
	}
	for _, conn := range conns {
		if !strings.EqualFold(conn.Name, p.Subject) {
			continue
		}
		list, err := c.rpc.ListTools(c.auth(), conn.ID)
		if err != nil {
			return out
		}
		for _, t := range list {
			if p.Member != "" && !strings.EqualFold(t.Name, p.Member) {
				continue
			}
			ti := ToolInfo{
				Ref:         ScopeRPC + ":" + conn.Name + ":" + t.Name,
				Name:        ScopeRPC + "__" + conn.Name + "__" + t.Name,
				Label:       t.Name,
				Description: t.Description,
				Available:   conn.IsActive,
				Source:      ScopeRPC + ":" + conn.Name,
				Parameters:  toolSchema(t.InputSchema),
			}
			if !ti.Available {
				ti.markUnavailable(rpcConnIssue(conn.Name))
			}
			out = append(out, ti)
		}
		return out
	}
	return out
}

// AllTools is every available tool (built-in + plugin + custom + external connection + local rpc endpoint), indexed by ref.
//
// This is the only source the Resolver uses to build the final tool set: **if one class of tool is missing here, the grant still
// resolves but is dropped in the last step and reported as "the referenced tool no longer exists"** (custom tools hit exactly this:
// ToolsOfRef could expand them, but AllTools lacked the class, so the grant looked saved while the agent never saw the tool).
func (c *Catalog) AllTools() (map[string]ToolInfo, error) {
	out := map[string]ToolInfo{}
	for _, t := range c.builtinTools() {
		out[t.Ref] = t
	}

	// Built-in category groups are not indexed here (a group is not a tool); only leaf tools go in

	plugins, err := c.pluginTools()
	if err != nil {
		return nil, err
	}
	for _, t := range plugins {
		out[t.Ref] = t
	}

	// Custom tools: the attribution rule agrees with the execution layer (executeCustomTool) - always indexed as leaves, and when
	// the platform has not granted exec, customTools() marks them unavailable (the Resolver warns "not granted by the platform" and
	// drops them).
	for _, t := range c.customTools() {
		out[t.Ref] = t
	}

	mcpConns, err := c.mcpConns()
	if err != nil {
		return nil, err
	}
	for _, conn := range mcpConns {
		list, err := c.mcp.ListTools(c.auth(), conn.ID)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			ref := ScopeMCP + ":" + conn.Name + ":" + t.Name
			ti := ToolInfo{
				Ref: ref, Name: ScopeMCP + "__" + conn.Name + "__" + t.Name, Label: t.Name,
				Description: t.Description, Available: conn.IsActive, Source: ScopeMCP + ":" + conn.Name,
				Parameters: toolSchema(t.InputSchema),
			}
			if !ti.Available {
				ti.markUnavailable(mcpConnIssue(conn.Name))
			}
			out[ref] = ti
		}
	}

	rpcConns, err := c.rpcConns()
	if err != nil {
		return nil, err
	}
	for _, conn := range rpcConns {
		list, err := c.rpc.ListTools(c.auth(), conn.ID)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			ref := ScopeRPC + ":" + conn.Name + ":" + t.Name
			ti := ToolInfo{
				Ref: ref, Name: ScopeRPC + "__" + conn.Name + "__" + t.Name, Label: t.Name,
				Description: t.Description, Available: conn.IsActive, Source: ScopeRPC + ":" + conn.Name,
				Parameters: toolSchema(t.InputSchema),
			}
			if !ti.Available {
				ti.markUnavailable(rpcConnIssue(conn.Name))
			}
			out[ref] = ti
		}
	}

	eps, err := c.localRPCConns()
	if err != nil {
		return nil, err
	}
	for _, ep := range eps {
		ref := ScopeLocal + ":" + LocalProtocolRPC + ":" + ep.Name
		ti := ToolInfo{
			Ref: ref, Name: "local__" + ep.Name, Label: ep.Name,
			Description: ep.Description, Available: bool(ep.Exposed), Source: ScopeLocal + ":" + LocalProtocolRPC,
			Parameters: toolSchema(ep.InputSchema),
		}
		if !ti.Available {
			ti.markUnavailable(endpointIssue(ep.Name))
		}
		out[ref] = ti
	}
	return out, nil
}

// builtinGroupCategories are the built-in categories Groups() emits (the order is the display order).
//
// It must be an **explicit list**: one missing category makes that group vanish entirely from the grant panel (the frontend renders
// groups only, and tools appear as their members) with no error at all.
// builtinByCategory compares on Source rather than iterating BuiltinCategories' keys, so this list is the source of truth;
// TestBuiltinGroupCategoriesCoverTools guards "a category that owns tools must not be missing".
//
// CategoryScript is not listed: no built-in tool belongs to it (run_skill_script now belongs to exec), and listing it would be
// skipped as empty anyway. When that category gains tools, the test above fails first and reminds you to add it.
//
// CategorySuper is not listed either: it owns no tools and is only the compatibility expansion entry for builtin:super (see
// builtinByCategory). Listing it would add a card duplicating the five management groups.
var builtinGroupCategories = []string{
	tools.CategoryFile, tools.CategoryExec, tools.CategoryShell, tools.CategoryBrowser,
	tools.CategoryAgent, tools.CategorySkill, tools.CategorySkillOwn,
	tools.CategoryAgentAdmin, tools.CategorySkillAdmin, tools.CategoryToolGrant, tools.CategoryCustomTool,
	tools.CategoryFlowAdmin,
}

// Groups is the referenceable group catalog (built-in categories / connections / local endpoints / presets / custom groups).
//
// It is view-dependent: in a non-super-agent view the built-in builtin:super group (and its members) does not appear at all, and a
// preset / custom group whose members were all filtered out disappears with it.
func (c *Catalog) Groups() ([]GroupInfo, error) {
	out := make([]GroupInfo, 0)

	// Built-in categories
	for _, category := range builtinGroupCategories {
		items := c.builtinByCategory(category)
		if len(items) == 0 {
			continue
		}
		// Platform capability gate: exec / shell / browser availability follows the tenant's capability switches.
		// The leaves were already marked by builtinTools (the check lives in one place so groups and tools cannot drift); here the
		// conclusion is only lifted onto the group for the frontend card to read directly.
		g := GroupInfo{
			Ref: ScopeBuiltin + ":" + category, Name: "Built-in · " + category, Kind: ScopeBuiltin,
			Description: builtinCategoryLabel(category), Available: true, Items: items,
		}
		if blocked, why := c.capabilityBlocked(tools.CapabilityOf(category)); blocked {
			g.markUnavailable(UnavailableCapability, why)
		}
		out = append(out, g)
	}

	// Preset groups
	for _, pg := range PresetGroups {
		items, err := c.expandItems(pg.Items, ScopePreset+":"+pg.Name)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			continue
		}
		out = append(out, GroupInfo{
			Ref: ScopePreset + ":" + pg.Name, Name: pg.Label, Kind: ScopePreset,
			Description: pg.Description, Available: true, Items: items,
		})
	}

	// Disk plugins: same-kind tools merge into one "category card", and the container itself is not grantable (Synthetic).
	// One card per tool would degrade the group name into a tool name (the "search" group), which is hard to recognise and also gets
	// item_type recorded as group.
	pluginItems, err := c.pluginTools()
	if err != nil {
		return nil, err
	}
	if g, ok := categoryGroup(ScopePlugin, "Plugins",
		"Python tool plugins on disk (one tool, several implementations; enabled and configured per tenant)", pluginItems); ok {
		out = append(out, g)
	}

	// Custom tools: same as above (the tenant's own code, count varying per tenant, and one card per tool is particularly unsuited).
	if g, ok := categoryGroup(ScopeCustom, "Custom tools",
		"Code tools built by this tenant (grant ref = custom:<name>, model-side tool name custom__<name>)", c.customTools()); ok {
		out = append(out, g)
	}

	// External MCP connections
	mcpConns, err := c.mcpConns()
	if err != nil {
		return nil, err
	}
	for _, conn := range mcpConns {
		items, err := c.ToolsOfRef(ScopeMCP + ":" + conn.Name)
		if err != nil {
			return nil, err
		}
		g := GroupInfo{
			Ref: ScopeMCP + ":" + conn.Name, Name: displayName(conn.DisplayName, conn.Name), Kind: ScopeMCP,
			Description: conn.Description, Available: conn.IsActive, Items: items,
			SourceID: conn.ID, RefreshedAt: c.mcpRefreshedAt(conn.ID),
		}
		if !g.Available {
			g.markUnavailable(mcpConnIssue(conn.Name))
		}
		out = append(out, g)
	}

	// External RPC connections
	rpcConns, err := c.rpcConns()
	if err != nil {
		return nil, err
	}
	for _, conn := range rpcConns {
		items, err := c.ToolsOfRef(ScopeRPC + ":" + conn.Name)
		if err != nil {
			return nil, err
		}
		g := GroupInfo{
			Ref: ScopeRPC + ":" + conn.Name, Name: displayName(conn.DisplayName, conn.Name), Kind: ScopeRPC,
			Description: conn.Description, Available: conn.IsActive, Items: items,
			SourceID: conn.ID, RefreshedAt: c.rpcRefreshedAt(conn.ID),
		}
		if !g.Available {
			g.markUnavailable(rpcConnIssue(conn.Name))
		}
		out = append(out, g)
	}

	// Local rpc endpoints
	eps, err := c.localRPCConns()
	if err != nil {
		return nil, err
	}
	if len(eps) > 0 {
		items, err := c.ToolsOfRef(ScopeLocal + ":" + LocalProtocolRPC)
		if err != nil {
			return nil, err
		}
		times := make([]xsql.Time, 0, len(eps))
		for _, ep := range eps {
			times = append(times, ep.UpdatedAt)
		}
		out = append(out, GroupInfo{
			Ref: ScopeLocal + ":" + LocalProtocolRPC, Name: "Local RPC endpoints", Kind: ScopeLocal,
			Description: "rpc-protocol endpoints of this tenant's flows (use call_agent for agent-to-agent calls)",
			Available:   true, Items: items, RefreshedAt: maxTime(times),
		})
	}

	// Tool groups (tenant-defined)
	groups, err := c.grants.ListGroups(c.schema)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		items, err := c.expandItems(g.Items, ScopeGroup+":"+g.Name)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			continue
		}
		out = append(out, GroupInfo{
			Ref: ScopeGroup + ":" + g.Name, Name: g.Name, Kind: ScopeGroup,
			Description: g.Description, Available: true, Items: items,
		})
	}
	return out, nil
}

// categoryGroup merges several tools of the same scope into one category card.
//
// Why it exists: the grant panel renders group cards only, and tools must appear as "group members"; the members of plugins and custom
// tools are **tenant-side entities** (count varying per tenant), so one card per tool would degrade the group name into a tool name
// (the "compound interest" group), which is hard to recognise and also misleads item_type into group - it looks like a group was
// granted when only that one tool was.
//
// This card is a **container**: Synthetic=true, the frontend offers no select-all for the group, and selection targets each member's
// own tool ref.
// The ref "<scope>:*" is only a frontend key / display aid - ParseRef rejects "*", so it can never be a valid grant ref.
func categoryGroup(scope, name, desc string, items []ToolInfo) (GroupInfo, bool) {
	if len(items) == 0 {
		return GroupInfo{}, false
	}
	g := GroupInfo{
		Ref: scope + ":*", Name: name, Kind: scope, Description: desc,
		Available: true, Items: items, Synthetic: true,
	}
	// Group-level availability is copied from the leaves (the check stays in one place): any unavailable member marks the card
	// unavailable too, with the first member's reason, matching how built-in category groups behave.
	for _, t := range items {
		if !t.Available {
			// Copy the code and params together: copying only kind/reason would leave the frontend holding a reason it cannot
			// translate, so the card would show untranslated text while everything else is localised.
			g.markUnavailable(t.UnavailableKind, unavail{
				code:   t.UnavailableCode,
				params: t.UnavailableParams,
				reason: t.UnavailableReason,
			})
			break
		}
	}
	return g, true
}

// builtinCategoryLabel is the fallback summary of a built-in category, used when the frontend has no translation for the group ref
// (the panel localises from <ref>.desc first and falls back to this string).
func builtinCategoryLabel(category string) string {
	switch category {
	case tools.CategoryFile:
		return "Files: read, write and browse workspace files"
	case tools.CategoryExec:
		return "One-shot execution: every command / script starts a new process and exits (stateless; includes the skill script run_skill_script)"
	case tools.CategoryShell:
		return "Resident session: reuses a long-lived process, keeping cwd / environment variables / background jobs across calls"
	case tools.CategoryBrowser:
		return "Browser: operate web pages inside the sandbox (network access, can read page content, high risk)"
	case tools.CategoryScript:
		return "Skill scripts: only runs scripts inside the skill directories injected into your workspace (/skill is mounted read-only)"
	case tools.CategoryAgent:
		return "Agents: look up and call other agents"
	case tools.CategorySkill:
		return "Skills: list and inspect the skills injected into your workspace (read the body on demand with read_skill or use_skill; use_skill activates its tool dependencies)"
	case tools.CategorySkillOwn:
		return "Private skills: create / modify / delete **your own** skills (this writes your own capability surface and is not granted by default)"
	case tools.CategoryAgentAdmin:
		return "Agent administration (super agent only): create / modify / delete agents and bind skills to them"
	case tools.CategorySkillAdmin:
		return "Skill administration (super agent only): create / modify / delete **shared (tenant-level)** skills, and create / modify / delete **private** skills for other agents"
	case tools.CategoryToolGrant:
		return "Grant administration (super agent only): grant / revoke an agent's tools and the call relationships between agents"
	case tools.CategoryCustomTool:
		return "Custom tool administration (super agent only): create / modify / delete / list this tenant's custom tools"
	case tools.CategoryFlowAdmin:
		return "Flow administration (super agent only): create / list / read / delete / test-run flows, inspect run records, and expose a flow as an mcp / rpc endpoint"
	case tools.CategorySuper:
		return "Super agent only (transitional aggregate group, equivalent to the union of the five management groups)"
	default:
		return "Uncategorised built-in tools"
	}
}

func displayName(display, fallback string) string {
	if strings.TrimSpace(display) != "" {
		return display
	}
	return fallback
}

// Unavailable attribution for external tools: all three are fixed by the tenant itself (each connection / endpoint has its own
// management page), so the reason says where to enable it. The most common confusion with these tools is not realising the
// connection is disabled.
func mcpConnIssue(name string) (kind string, u unavail) {
	return UnavailableConnection, unavail{
		code:   UnavailableCodeMcpConnection,
		params: map[string]string{"connection": name},
		reason: "MCP connection " + name + " is not enabled; enable it under Settings -> MCP connections",
	}
}

func rpcConnIssue(name string) (kind string, u unavail) {
	return UnavailableConnection, unavail{
		code:   UnavailableCodeRpcConnection,
		params: map[string]string{"connection": name},
		reason: "RPC connection " + name + " is not enabled; enable it under Settings -> RPC connections",
	}
}

func endpointIssue(name string) (kind string, u unavail) {
	return UnavailableEndpoint, unavail{
		code:   UnavailableCodeEndpoint,
		params: map[string]string{"endpoint": name},
		reason: "endpoint " + name + " is not exposed; turn on its \"exposed\" switch on the Endpoints page",
	}
}
