package models

import (
	"encoding/json"
	"time"

	"flame/tools/xsql"
)

// LLMProvider is an LLM provider.
type LLMProvider struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	URL         xsql.String `json:"url"`
	Protocol    xsql.String `json:"protocol"`
	APIKey      xsql.String `json:"api_key"`
	Icon        xsql.String `json:"icon"`
	Enabled     xsql.Bool   `json:"enabled"`
	ExtraConfig xsql.JSONB  `json:"extra_config"`
}

// LLMProviderPreset is an LLM provider preset: system-level, globally shared, stored in the platform schema.
type LLMProviderPreset struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Icon        xsql.String `json:"icon"`
	Description xsql.String `json:"description"`
	Protocol    string      `json:"protocol"`
	URL         string      `json:"url"`
	IsPopular   bool        `json:"is_popular"`
	SortOrder   int         `json:"sort_order"`
}

// LLMModel is an LLM model.
type LLMModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow xsql.Int `json:"context_window"`
	// Base generation parameters (jsonb). A key being present means that parameter is used, a missing key means it is
	// not; there is no extra wrapper structure.
	// Only these keys are allowed: max_tokens(int), temperature(float64), top_p(float64)
	BaseParams   xsql.JSONB  `json:"base_params"`
	ProviderID   string      `json:"provider_id"`
	GatewayID    xsql.String `json:"gateway_id"`
	GatewayModel xsql.String `json:"gateway_model"`
	IsDefault    xsql.Bool   `json:"is_default"`
	ExtraParams  xsql.JSONB  `json:"extra_params"`
	Capabilities xsql.JSONB  `json:"capabilities"`
	CreatedAt    xsql.Time   `json:"created_at"`
	UpdatedAt    xsql.Time   `json:"updated_at"`
}

// bpFloat reads a float64 parameter value from the map decoded out of jsonb.
// A missing key or a type mismatch returns (0, false).
func bpFloat(bp map[string]any, key string) (float64, bool) {
	v, ok := bp[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// GetTemperature returns temperature from the base parameters; when it is not configured it returns the default 0.7
// with ok=false.
func (m *LLMModel) GetTemperature() (float64, bool) {
	if m.BaseParams.IsNil() {
		return 0.7, false
	}
	var bp map[string]any
	if err := m.BaseParams.Unmarshal(&bp); err != nil || bp == nil {
		return 0.7, false
	}
	if v, ok := bpFloat(bp, "temperature"); ok {
		return v, true
	}
	return 0.7, false
}

// GetMaxTokens returns max_tokens from the base parameters; when it is not configured it returns the default 40960 with
// ok=false.
func (m *LLMModel) GetMaxTokens() (int, bool) {
	if m.BaseParams.IsNil() {
		return 40960, false
	}
	var bp map[string]any
	if err := m.BaseParams.Unmarshal(&bp); err != nil || bp == nil {
		return 40960, false
	}
	if v, ok := bpFloat(bp, "max_tokens"); ok {
		return int(v), true
	}
	return 40960, false
}

// GetTopP returns top_p from the base parameters; when it is not configured it returns the default 0.6 with ok=false.
func (m *LLMModel) GetTopP() (float64, bool) {
	if m.BaseParams.IsNil() {
		return 0.6, false
	}
	var bp map[string]any
	if err := m.BaseParams.Unmarshal(&bp); err != nil || bp == nil {
		return 0.6, false
	}
	if v, ok := bpFloat(bp, "top_p"); ok {
		return v, true
	}
	return 0.6, false
}

// Agent is an agent.
type Agent struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description xsql.String `json:"description"`
	Config      xsql.JSONB  `json:"config"`
	Status      xsql.Int    `json:"status"` // 1: normal, 3: deprecated (see StatusNormal/StatusDeprecated; agents have no "locked" state)
	Endpoints   xsql.JSONB  `json:"endpoints"`
	// Two agent-level switches (both columns are NOT NULL DEFAULT false):
	//
	//	ToolsUnrestricted: do not restrict tools — this agent accepts the tools declared by the skills it can see
	//	                  (the grant check is skipped; an explicit deny still applies)
	//	SkillDiscovery  : allow discovering unbound skills — the visible scope additionally includes tenant-level skills
	//	                  visible to everyone (progressive disclosure)
	//
	// Note: the system prompt's injected catalogue **always** lists only "private + bound", regardless of these switches.
	ToolsUnrestricted bool `json:"tools_unrestricted"`
	SkillDiscovery    bool `json:"skill_discovery"`
	// IsSupervisor is the super-privilege attribute (agents.is_supervisor).
	//
	// It replaces the old "name equals main" criterion: main is merely the supervisor preinstalled when a tenant is
	// provisioned, and a tenant may configure another agent with the same privileges (several are allowed). The
	// criterion is models.IsSupervisor.
	//
	// It decides: the default core tool set, the availability of management tools, cross-domain file writes, "cannot be
	// called by another agent", and the grants catalogue's full view. It does **not** decide the delete protection — that
	// is IsProtectedAgent (a name list), and the two semantics are orthogonal.
	IsSupervisor bool `json:"is_supervisor"`
	// DefaultLanguage is the agent's default reply language (agents.default_language, a BCP-47 tag; empty = not
	// configured).
	//
	// It is a **definition** column, not free-form config: it is frozen into agent_versions on release, travels with an
	// asset pack, and is what the runtime reads when the caller pins no language. It is the machine-readable half of
	// "what language this agent speaks" — the wording and the exceptions stay in AGENT.md, which still wins.
	DefaultLanguage string `json:"default_language"`
	CreatedBy     xsql.UUID   `json:"created_by"`
	UpdatedBy     xsql.UUID   `json:"updated_by"`
	UpdatedByName xsql.String `json:"updated_by_name"` // the editor's name (joined from users at query time, not a DB column)
	CreatedAt     xsql.Time   `json:"created_at"`
	UpdatedAt     xsql.Time   `json:"updated_at"`
}

// Lifecycle state values (shared by the status column of agents / flows / skills; the column type is int2).
//
//	normal(1): modifiable and runnable — the default for new rows
//	deprecated(3): **not modifiable, not runnable** — only ever set by hand
//
// agents / flows / skills all have two states (CHECK (1,3)). The historical "locked(2): not modifiable, runnable" has
// been removed everywhere: a workflow expresses the production freeze through the version model, while for agents / skills
// "unlock before you can ship a fix" handed the very risk it was meant to prevent right back (the definition became
// editable the moment it was unlocked).
//
// The only place that keeps status=2 is **custom tools**: they keep the old "enabled(1) / disabled(2)" rule, where the
// meaning is "disabled", not "locked". The StatusLocked constant is kept for that; agents / skills must not use it again.
//
// The only automatic coupling (implemented in shared/repository/svcendpoints and each Set*Status handler):
//
//	switch the state to "deprecated" -> automatically retract all of its endpoints (exposed=false, **the endpoint records
//	are kept**).
//
// The coupling that is deliberately **absent**: exposing an endpoint does not change the state. "Offering an entrance to
// the outside" and "may the definition change" are two different things — binding them together early on (exposed means
// locked) cost a prompt edit the price of unlocking first, and unlocking retracted the endpoint and cut the traffic. Now an
// endpoint may stay editable after it is exposed, and the editor warns at save time (a second confirmation while an
// endpoint is exposed).
const (
	// StatusNormal is normal: modifiable and runnable.
	StatusNormal = 1
	// StatusLocked is not modifiable but runnable. agents / flows / skills have all dropped this state; only **custom
	// tools** still use it for "disabled" (the old rule, not the definition-freezing "locked").
	StatusLocked = 2
	// StatusDeprecated is deprecated: not modifiable, not runnable.
	StatusDeprecated = 3
)

// IsModifiable allows changing the definition only in the "normal" state: a deprecated entity may not (a custom tool's
// "disabled" may not either). Guards must all use it — do not write literals such as `status == 1`.
func IsModifiable(status int) bool { return status == StatusNormal }

// IsRunnable allows being run / called: only "deprecated" is blocked.
func IsRunnable(status int) bool { return status != StatusDeprecated }

// Status checks for the three entities (a thin wrapper so callers do not have to write int(x.Status)).
// IsModifiable: changing the definition is allowed (only "normal" allows it).

// IsModifiable: whether this agent's definition may change (including the private skills it owns: a non-normal state
// freezes all of them).
func (a *Agent) IsModifiable() bool { return IsModifiable(int(a.Status)) }

// IsModifiable: whether this flow's definition may change.
func (f *Flow) IsModifiable() bool { return IsModifiable(int(f.Status)) }

// IsModifiable: whether this skill's definition may change.
func (s *Skill) IsModifiable() bool { return IsModifiable(int(s.Status)) }

// Note: the former HasEnabledEndpoint ("has an exposed endpoint") is gone — the definition guard looks only at status
// (see IsModifiable) and is **independent** of endpoint exposure: exposing an endpoint does not change the state, and an
// entity may have an exposed endpoint and still be editable. Where "it has an exposed endpoint" is really needed (a
// warning before saving in the editor, the local endpoint catalogue), read it from entities.endpoints / svc_endpoints;
// never infer it back from status.

// Skill is a skill definition (agent_id NULL means tenant-level, non-NULL means private to that agent).
// The metadata and ownership live in this table, the content lives in files (the path is derived from the skill name and
// its owner).
type Skill struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Description     xsql.String `json:"description"`
	CreatedBy       xsql.UUID   `json:"created_by"`
	UpdatedBy       xsql.UUID   `json:"updated_by"`
	UpdatedByName   xsql.String `json:"updated_by_name"`  // the editor's name (joined from users at query time, not a DB column)
	AgentName       xsql.String `json:"agent_name"`       // the owning agent's name (joined from agents at query time, not a DB column; empty when agent_id is empty)
	Metadata        xsql.JSONB  `json:"metadata"`         // extended metadata (DB column metadata)
	UserInvocable   xsql.Bool   `json:"user_invocable"`   // OpenClaw user-invocable: whether it is exposed as a command the user can call directly
	CommandDispatch xsql.String `json:"command_dispatch"` // OpenClaw command-dispatch: 'tool' = dispatch straight to the tool, bypassing the model
	CommandTool     xsql.String `json:"command_tool"`     // OpenClaw command-tool: the target tool name the command dispatches to
	Status          xsql.Int    `json:"status"`
	Restricted      xsql.Bool   `json:"restricted"` // narrowed visibility: false = visible to everyone (default); true = visible only to granted agents (tenant-level skills only)
	CreatedAt       xsql.Time   `json:"created_at"`
	UpdatedAt       xsql.Time   `json:"updated_at"`
	AgentID         xsql.UUID   `json:"agent_id"`
}

// ===== tool grant system (tool_groups / agent_tool_grants / skill_tool_grants) =====

// Reference kinds (tool_group_items.item_type / *_tool_grants.item_type).
const (
	GrantItemGroup = "group" // group: a native group (builtin:* / mcp:* / rpc:* / local:rpc) or a tool group (group:*)
	GrantItemTool  = "tool"  // a single tool: builtin:<tool> / mcp:<conn>:<tool> / rpc:<conn>:<tool> / local:rpc:<name>
	// Note: local:rpc only takes local endpoints with target_type='flow' (agent endpoints go through call_agent and do
	// not enter the tool catalogue).
)

// Grant kinds (agent_tool_grants.grant_kind).
const (
	GrantKindRequired = "required" // must import: always injected, unaffected by deny
	GrantKindAllow    = "allow"    // may use: overridden by deny
	GrantKindDeny     = "deny"     // explicitly refused: applies to allow only
)

// GrantRefBuiltinPrefix is the ref prefix of a single built-in tool (builtin:<tool>).
// The same value as service/agent/toolgrant.ScopeBuiltin (a test guards it) — the shared layer never depends on the
// service layer.
const GrantRefBuiltinPrefix = "builtin:"

// DefaultAgentTools are the tool names a new agent gets by default (single source of truth).
//
// Rule: read-only metadata + file read/write, by default limited to the agent's own session workspace; in addition the
// "/" prefix may address the root to **read its own definition directory** (agents/{self}/**, including AGENT.md) and
// **write its own private skill** (agents/{self}/skills/{skill}/**, see checkReadOwn / checkWriteOwn in tools/gate.go);
// every other cross-domain path is unreachable.
// It does not include exec (a black-box shell plus the os.Environ leak surface); call_agent is included because
// "a tool is not a permission" — its real gate is agent_relations (no row = refused).
//
// It does **not** include create/update/delete_own_skill: letting an agent build its own private skill turns its prompt
// into a writable surface (the name / description written there sits in the system prompt on the next call), which under
// input-induced pressure becomes a persistent injection. So it is not granted by default and an admin grants it per agent
// explicitly; when granting, materialise it as `required` (always injected, avoiding the trap of allow being narrowed by
// skill dependencies), at the cost that `deny` cannot take it back — revoking needs a wholesale replace (delete these
// three lines).
//
// Two consumers: 1) agents.CreateAgent materialises it into required grants; 2) the preset:default group in toolgrant.
// Changing it changes both, which keeps them from drifting (a test guards it); sql/agent_default_tool_grants.sql must be
// synced as well.
var DefaultAgentTools = []string{
	// read-only files
	"read_file", "list_files", "file_exists",
	// read/write inside its own session workspace
	"write_file", "edit_file", "delete_file",
	// read-only metadata + use skills
	// use_skill must be here: a binding only puts the skill into the prompt catalogue, and the tools appear only after
	// use_skill activates it; without it an ordinary agent has no way at all to light up its allow grants (read_skill is
	// read-only and brings no tools).
	"list_agents", "read_agent", "list_skills", "read_skill", "use_skill",
	// collaboration (the actual permission is decided by agent_relations)
	"call_agent",
}

// DefaultAgentToolGrants returns the default grants of a new agent (item_type=tool / ref=builtin:<name>).
// CreateAgent materialises the grant rows with it, and toolgrant's preset:default builds its members from it.
func DefaultAgentToolGrants() []ToolGroupItem {
	return toolGrantsOf(DefaultAgentTools)
}

// SupervisorCoreGrantRefs are the **groups** the super agent always has (the core set, single source of truth).
//
// Six groups, 20 tools once expanded:
//
//	builtin:file         file read/write (6)
//	builtin:exec         one-shot processes (2)
//	builtin:shell        resident session (1)
//	builtin:agent        look at / call agents (3)
//	builtin:skill        look at / use skills (3: list_skills / read_skill / use_skill)
//	builtin:agent-admin  create / change / delete agents + bind skills (5)
//
// Background: the super agent used to be a privilege "all on, in code" (toolgrant.BuiltinTools in full), so "what the
// super agent holds" was known only to the code, invisible in the database and impossible to adjust per tenant.
// That baseline now lives in the required rows of agent_tool_grants, and the super agent speaks the same language as any
// other agent: **always on = there is a required row**, not "because you are the super agent".
//
// Why groups rather than individual tools: a group is atomic — deleting one by mistake loses a whole class, which is
// obvious on the panel; among 33 single-tool rows, deleting `read_file` raises no error, it just silently loses one tool.
// The cost is that a tool added to a group automatically enters core, which is exactly the meaning "core = the system
// capability baseline" should have.
//
// Not in core (on demand):
//   - builtin:browser — the guidance is "if exec can fetch the content, do not enable it", its schema is the largest
//     (14 actions / 18 parameters), and it is the only combination of "goes out to the network + reads arbitrary pages +
//     carries a login state", so it is a task capability.
//     It **follows no skill**, so allow cannot be used to make it on-demand (allow only filters candidates; with no
//     candidate it does nothing); an admin who wants it adds a required row directly and deletes it afterwards.
//   - skill-own / skill-admin / tool-grant / custom-tool / flow-admin — they follow skills: activating skill-authoring
//     through use_skill brings the first two groups, agent-authoring brings tool-grant (+ skill-admin), custom-tool-authoring
//     brings custom-tool, flow-authoring brings flow-admin.
//     A binding only puts the skill into the prompt catalogue; it does not hand over the tools directly.
//     The super agent does **not** keep these skills activated, it reads the body with use_skill when needed and the
//     activation is reclaimed when the run ends — so the default context saves a whole set of management tool schemas (at
//     the price of one tool call).
//
// Three consumers: 1) agents.CreateAgent (the supervisor branch) materialises them as required;
// 2) sql/main_core_tool_grants.sql backfills existing tenants; 3) admin previews and documentation.
// Changing it requires syncing the hard-coded array in 2 (a test guards it).
var SupervisorCoreGrantRefs = []string{
	GrantRefBuiltinPrefix + "file",
	GrantRefBuiltinPrefix + "exec",
	GrantRefBuiltinPrefix + "shell",
	GrantRefBuiltinPrefix + "agent",
	GrantRefBuiltinPrefix + "skill",
	GrantRefBuiltinPrefix + "agent-admin",
}

// SupervisorCoreGrants returns the super agent's core grants (item_type=group / ref=builtin:<group>).
func SupervisorCoreGrants() []ToolGroupItem {
	items := make([]ToolGroupItem, 0, len(SupervisorCoreGrantRefs))
	for _, ref := range SupervisorCoreGrantRefs {
		items = append(items, ToolGroupItem{ItemType: GrantItemGroup, Ref: ref})
	}
	return items
}

// SupervisorSeedSkills are the skills bound automatically when the super privilege is granted (part of the supervisor's
// starter kit).
//
// Orthogonal to protectedSkills in meaning: that one answers "which ones may not be deleted / renamed", this one answers
// "which manuals should a new supervisor get". They currently hold the same values, but **do not merge them** — when a
// foundational skill appears that must not be deleted yet need not be bound by default, a merged form could not express
// it.
//
// Why these four: the five classes of management tools (agent-admin / skill-admin / tool-grant / custom-tool / flow-admin)
// are all injected on demand with their skill (see the "Not in core (on demand)" paragraph of the SupervisorCoreGrantRefs
// comment), and a binding only puts the skill into the prompt catalogue; without the binding a new supervisor holds only
// the six resident groups, and "create a skill" / "change a grant" / "write a custom tool" / "build a workflow" can never
// light up — an identity without hands.
var SupervisorSeedSkills = []string{
	"agent-authoring",
	"skill-authoring",
	"custom-tool-authoring",
	"flow-authoring",
}

// toolGrantsOf turns a tool name list into a grant item list (used by DefaultAgentToolGrants).
func toolGrantsOf(names []string) []ToolGroupItem {
	items := make([]ToolGroupItem, 0, len(names))
	for _, name := range names {
		items = append(items, ToolGroupItem{ItemType: GrantItemTool, Ref: GrantRefBuiltinPrefix + name})
	}
	return items
}

// Note: there used to be a criterion for "whether the core backfill has run" here (MainCoreMigrationMarker = agent-admin +
// MainCoreMigrationPending). It was removed entirely once the existing tenants had migrated:
//
//   - its only purpose was to let an **unmigrated** supervisor fall back to "everything on by default", and that hidden
//     path was itself a long-term debugging burden (the table said there was nothing, the reality gave everything);
//   - after the removal the supervisor's resident set has exactly one source of truth: the required rows in the table;
//   - the "zero grant rows" fuse was **not** removed along with it — that is not a migration mechanism but bootstrap
//     safety (see the zero-row fallback in agent_impl and the same-named branch in the admin/toolgrants preview).
//
// Before rebuilding this logic, confirm: are there still tenants whose grant table holds only scattered rows and no core
// group? The SQL for that check is at the top of sql/main_core_tool_grants.sql.

// ToolGroup is a tool group (members may only be native groups or single tools; nested tool groups are not allowed).
type ToolGroup struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Items       []ToolGroupItem `json:"items"`
	CreatedBy   xsql.UUID       `json:"created_by"`
	UpdatedBy   xsql.UUID       `json:"updated_by"`
	CreatedAt   xsql.Time       `json:"created_at"`
	UpdatedAt   xsql.Time       `json:"updated_at"`
}

// ToolGroupItem is a tool group member.
type ToolGroupItem struct {
	ItemType string `json:"item_type"` // group | tool
	Ref      string `json:"ref"`
}

// AgentToolGrant is an agent's tool grant (required / allow / deny).
type AgentToolGrant struct {
	AgentID   string    `json:"agent_id"`
	GrantKind string    `json:"grant_kind"` // required | allow | deny
	ItemType  string    `json:"item_type"`  // group | tool
	Ref       string    `json:"ref"`
	UpdatedBy xsql.UUID `json:"updated_by"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// SkillToolGrant is a skill's tool dependency (it expresses a "need" only; it grants nothing).
type SkillToolGrant struct {
	SkillID  string `json:"skill_id"`
	ItemType string `json:"item_type"` // group | tool
	Ref      string `json:"ref"`
}

// A skill's status shares the two-state values with agents (see StatusNormal/StatusDeprecated above): only "normal" may
// be changed, "deprecated" may not be changed or run. A skill has no endpoint, so "deprecating automatically retracts the
// endpoints" does not apply to it.

// AgentSkillBinding is the grant relation between an agent and a skill (the agent_skill_bindings table, a pure
// allowlist).
// It only takes effect when skills.restricted = true: that skill is then visible only to the agents listed in this table.
type AgentSkillBinding struct {
	AgentID   string      `json:"agent_id"`
	SkillID   string      `json:"skill_id"`
	CreatedBy xsql.UUID   `json:"created_by"`
	CreatedAt xsql.Time   `json:"created_at"`
	AgentName xsql.String `json:"agent_name"` // filled by a joined query (not a DB column)
}

// AgentRelation is a call grant between agents (the agent_relations table).
// Semantics: no row = the caller may not call the callee (deny by default); a row = allowed.
type AgentRelation struct {
	CallerID   string      `json:"caller_id"`
	CalleeID   string      `json:"callee_id"`
	CreatedBy  xsql.UUID   `json:"created_by"`
	CreatedAt  xsql.Time   `json:"created_at"`
	CallerName xsql.String `json:"caller_name"` // filled by a joined query (not a DB column)
	CalleeName xsql.String `json:"callee_name"` // filled by a joined query (not a DB column)
}

// CallableAgent is a summary of an agent that may be called (used by the system prompt's "callable agents" list).
// It comes back from one JOIN (agent_relations ⋈ agents), avoiding a second query per target.
type CallableAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      int    `json:"status"`
	// IsSupervisor is whether the target carries the super-privilege attribute. A supervisor may be called (it still
	// reads its draft when invoked), and whether the call is let through is decided by the agent_relations grant row;
	// this is carried out for display and auditing only, never for a business decision.
	IsSupervisor bool `json:"is_supervisor"`
}

// SvcEndpoint is a service endpoint.
type SvcEndpoint struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
	TargetID    string     `json:"target_id"`
	TargetType  string     `json:"target_type"`  // "agent" or "flow"
	Protocol    string     `json:"protocol"`     // "mcp" | "rpc" (llm passes the schema check only; there is no runtime)
	InputSchema xsql.JSONB `json:"input_schema"` // the tool argument JSON Schema: a flow endpoint always equals flows.arguments
	Exposed     xsql.Bool  `json:"exposed"`      // exposed switch: true = usable outside the tenant (external credentials / MCP registration), false = inside the tenant only (the default for new rows)

	// TargetName is the target entity's name (not a DB column): filled by joining agents / flows in list queries, so the
	// frontend does not map id → name again.
	TargetName xsql.String `json:"target_name"`

	// TargetSupervisor is whether the target is the super agent (not a DB column; carried out by the query).
	// A credential's "everyone except the super agent" scope is decided by it — the decision point only has target_type +
	// target_id, and querying agents again would turn the hot path (every tool call) into two queries.
	TargetSupervisor bool `json:"target_supervisor"`
}

// EndpointProtocol is endpoint protocol information (an element of the endpoints jsonb array on agents / flows).
type EndpointProtocol struct {
	Protocol string `json:"protocol"`
	Exposed  bool   `json:"exposed"`
}

// Flow is a workflow definition (stored in the flows table).
type Flow struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Diagram           xsql.JSONB         `json:"diagram"`
	Description       xsql.String        `json:"description"`
	CreatedAt         xsql.Time          `json:"created_at"`
	UpdatedAt         xsql.Time          `json:"updated_at"`
	CreatedBy         xsql.UUID          `json:"created_by"`      // the creator's user id (uuid)
	UpdatedBy         xsql.UUID          `json:"updated_by"`      // the editor's user id (uuid)
	CreatedByName     xsql.String        `json:"created_by_name"` // the creator's name (joined from users at query time, not a DB column)
	UpdatedByName     xsql.String        `json:"updated_by_name"` // the editor's name (joined from users at query time, not a DB column)
	Arguments         xsql.JSONB         `json:"arguments"`
	Outputs           xsql.JSONB         `json:"outputs"`
	EnvVars           xsql.JSONB         `json:"env_vars"`
	FlowVars          xsql.JSONB         `json:"flow_vars"`
	Timeout           xsql.Int           `json:"timeout"`
	MaxExecCount      xsql.Int           `json:"max_exec_count"`
	StoreRuns         bool               `json:"store_runs"`     // false: keep no run records at all
	SessionMemory     bool               `json:"session_memory"` // false: a stateless workflow (no session memory)
	Status            xsql.Int           `json:"status"`      // 1: normal, 3: deprecated (workflows have no locked state, see StatusNormal/StatusDeprecated)
	Endpoints         []EndpointProtocol `json:"endpoints"`   // endpoint protocols + exposed state (a jsonb column)
	Breakpoints       []string           `json:"breakpoints"` // breakpoint node_id list (not a DB column; a save replaces flow_breakpoints wholesale)

	// Version state (not a DB column; computed at query time):
	//   CurrentVersion = 0 -> never released (production calls are unavailable; only the debugger works)
	//   Dirty = the working copy differs from the current version's definition (a rename does not count: the name is not
	//   part of the version content)
	CurrentVersion int  `json:"current_version"`
	Dirty          bool `json:"dirty"`
}

// FlowRunRecord is a workflow run record (the flow_run_records table).
type FlowRunRecord struct {
	ID     string `json:"id"`
	FlowID string `json:"flow_id"`
	// SessionID / UserID answer "in which conversation" and "who triggered this run", the same two dimensions the agent
	// run log has always carried. They are nullable: a scheduled or directly-triggered run has no session, and an API-key
	// caller resolved without a login user has no identity.
	SessionID  string     `json:"session_id"`
	UserID     string     `json:"user_id"`
	Status     string     `json:"status"`   // success/failed/cancelled
	RunType    string     `json:"run_type"` // debug (the debugger) / preview (a trial run) / task / call
	Result     string     `json:"result"`
	Progress   []byte     `json:"progress"` // jsonb, {"progress":[...],"result":[...]}
	ErrMsg     string     `json:"err_msg"`
	NodeCount  int        `json:"node_count"`
	DurationMs int64      `json:"duration_ms"`
	StartedAt  xsql.Time  `json:"started_at"`
	FinishedAt *xsql.Time `json:"finished_at"`
	// Snapshot of the run configuration
	RunArgs              []byte `json:"run_args"`
	RunLimits            []byte `json:"run_limits"`
	RunFlowVars          []byte `json:"run_flow_vars"`
	RunFlowVarsTruncated bool   `json:"run_flow_vars_truncated"`
	RunFlowVarsSize      int    `json:"run_flow_vars_size"`
	ProgressTruncated    bool   `json:"progress_truncated"`
	ProgressSize         int    `json:"progress_size"`
	// Drafts are the drafts this run followed (a jsonb array; an empty array = a pure production run).
	// The debugger runs the working copy, whose content changes, so this list is the only evidence of "which ones it
	// actually ran".
	Drafts []byte `json:"drafts"`
}

// AgentRunRecord is one agent run (the agent_run_records table).
//
// Division of labour with SessionRecord: that table is the **memory** the model reads back as context
// (full text, grouped per session, trimmed by session), while this one is the **audit log** (one row per
// run, trimmed per agent, independent of any session). The split exists because session_records.session_id
// is NOT NULL: MCP / RPC calls that do not pass _meta.session_id write no memory at all, yet they are
// exactly the calls an operator needs to trace. Deleting a row here never touches a conversation.
type AgentRunRecord struct {
	// ID is the run identity, not just a row key: one row is one run, and session_records.run_id carries the same
	// value. There is deliberately no second run_id column - the two copies could drift, and did.
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	SessionID string     `json:"session_id"` // empty = a stateless run (no session)
	UserID    string     `json:"user_id"`    // who called it; empty when the caller has no identity
	RunType    string     `json:"run_type"` // call / rpc / mcp / agent / flow
	Status     string     `json:"status"`   // ok / stopped / failed (the SessionStatus* values)
	ErrMsg     string     `json:"err_msg"`
	DurationMs int64      `json:"duration_ms"`
	StartedAt  xsql.Time  `json:"started_at"`
	FinishedAt *xsql.Time `json:"finished_at"`
	// Question is what the model was asked; Answer is only a preview (answer_truncated says so).
	// The full answer lives in session_records and is read there by the chat page.
	Question        string    `json:"question"`
	QuestionSize    int       `json:"question_size"`
	Answer          string    `json:"answer"`
	AnswerTruncated bool      `json:"answer_truncated"`
	AnswerSize      int       `json:"answer_size"`
	CreatedAt       xsql.Time `json:"created_at"`
}

// SessionOwnerType is the owner type of session memory.
const (
	SessionOwnerTypeFlow  = "flow"  // workflow
	SessionOwnerTypeAgent = "agent" // agent
)

// Session is a session (the sessions table, generalised over agent + flow).
type Session struct {
	ID        string    `json:"id"`
	OwnerType string    `json:"owner_type"` // flow / agent
	OwnerID   string    `json:"owner_id"`
	UserID    string    `json:"user_id"` // the user the session belongs to (nullable; NULL = tenant / system level)
	Title     string    `json:"title"`
	CreatedAt xsql.Time `json:"created_at"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// VisibleToUser reports whether the session is visible to that user — the single criterion for the external entries
// (HTTP chat / MCP / RPC).
//
// A session belongs to a **user**; owner_type / owner_id only record the creator (who opened the session, whose name the
// record is filed under) and no longer take part in access control: when a child agent shares a session with its parent,
// the owner can never match, and closing access on owner would reject every legitimate cascading call.
//
// An empty userID (missing identity) is always refused; an empty session user_id means a tenant-level session (it never
// appears in a list), where there is no "someone else", so it is allowed.
func (s *Session) VisibleToUser(userID string) bool {
	if s == nil || userID == "" {
		return false
	}
	if s.UserID == "" {
		return true
	}
	return s.UserID == userID
}

// OwnedBy reports whether the session was created by the entity (ownerType, ownerID).
//
// It is only for "record ownership / display / auditing": session_records are grouped by owner, so when a parent and a
// child agent share one session their rows are invisible to each other. Do **not** use it for access control — that is
// VisibleToUser's job.
func (s *Session) OwnedBy(ownerType, ownerID string) bool {
	if s == nil || ownerID == "" {
		return false
	}
	return s.OwnerType == ownerType && s.OwnerID == ownerID
}

// SessionRecord is a session record (the session_records table).
// Grouped by (owner_type, owner_id, session_id); each session keeps the latest max_session_records rows.
type SessionRecord struct {
	ID         string    `json:"id"`
	OwnerType  string    `json:"owner_type"` // flow / agent
	OwnerID    string    `json:"owner_id"`
	SessionID  string    `json:"session_id"`
	RunID      string    `json:"run_id"`
	Input      []byte    `json:"input"`       // jsonb, this round's arguments
	Output     string    `json:"output"`      // text, this round's final result
	RunType    string    `json:"run_type"`    // call / debug
	DurationMs int64     `json:"duration_ms"` // this round's duration (ms); 0 = not recorded (legacy rows / memory writes from the flow side)
	Status     string    `json:"status"`      // ok / stopped / failed; legacy rows are filled in as ok by the column default
	Error      string    `json:"error"`       // the plain-language reason for a failure or a stop; empty when status=ok
	CreatedAt  xsql.Time `json:"created_at"`
}

// Session record status values (the session_records.status column; varchar(16) in the database).
//
// Why a separate column rather than mixing errors into output: with a blank answer in the history a user cannot tell "I
// pressed stop" from "the model died" or "a tool errored", and can only come and ask.
const (
	SessionStatusOK      = "ok"      // completed normally
	SessionStatusStopped = "stopped" // actively terminated (the user stopped it / the session was closed / the run timed out)
	SessionStatusFailed  = "failed"  // an error occurred (the reason is in error)
)

// SvcAuthBinding is a service-side authentication identity binding (the svc_auth_bindings table, shared by MCP / RPC).
// It maps an external identity onto a system user: apikey uses the plaintext secret, oauth uses iss + sub.
type SvcAuthBinding struct {
	ID              string `json:"id"`
	AuthType        string `json:"auth_type"`        // apikey | oauth
	ExternalIssuer  string `json:"external_issuer"`  // oauth: token.iss; apikey: ''
	ExternalSubject string `json:"external_subject"` // oauth: token.sub; apikey: the plaintext secret
	UserID          string `json:"user_id"`          // users.id
	IsActive        bool   `json:"is_active"`
	// With RestrictTargets=true this credential may only call the endpoint targets in the allowlist (see
	// svc_auth_binding_targets); an empty allowlist means none at all. A new binding defaults to true (a safe default:
	// forgetting to configure means nothing is callable), while false means unrestricted and has to be turned off
	// explicitly; existing bindings stay false after the migration.
	RestrictTargets bool `json:"restrict_targets"`
	// ExcludeSupervisor only means something when RestrictTargets=false: do not restrict all endpoints, but **exclude the
	// super agent** (a supervisor's endpoint equals opening the tenant's management capability to the outside). It is the
	// middle setting of "unrestricted" that needs no allowlist. It is ignored when RestrictTargets=true (the allowlist
	// already states it precisely).
	ExcludeSupervisor bool `json:"exclude_supervisor"`
	// TargetCount is the allowlist size (for display in a list; it takes no part in authorization).
	TargetCount int       `json:"target_count"`
	CreatedAt   xsql.Time `json:"created_at"`
	UpdatedAt   xsql.Time `json:"updated_at"`
}

// FlowNodeRecord is a workflow node run record (the flow_node_records table).
type FlowNodeRecord struct {
	ID                  string     `json:"id"`
	RunID               string     `json:"run_id"`
	NodeID              string     `json:"node_id"`
	ProcessID           string     `json:"process_id"`
	NodeType            string     `json:"node_type"`
	Status              string     `json:"status"`
	InputVars           []byte     `json:"input_vars"` // jsonb
	InputVarsTruncated  bool       `json:"input_vars_truncated"`
	InputVarsSize       int        `json:"input_vars_size"`
	OutputVars          []byte     `json:"output_vars"` // jsonb
	OutputVarsTruncated bool       `json:"output_vars_truncated"`
	OutputVarsSize      int        `json:"output_vars_size"`
	LocalVars           []byte     `json:"local_vars"` // jsonb
	LocalVarsTruncated  bool       `json:"local_vars_truncated"`
	LocalVarsSize       int        `json:"local_vars_size"`
	RunLogs             []byte     `json:"run_logs"` // jsonb
	RunLogsTruncated    bool       `json:"run_logs_truncated"`
	RunLogsLines        int        `json:"run_logs_lines"`
	ErrCode             string     `json:"err_code"`
	ErrMsg              string     `json:"err_msg"`
	ErrStack            string     `json:"err_stack"`
	RetryCount          int        `json:"retry_count"`
	ErrorAction         string     `json:"error_action"`
	StartTime           *xsql.Time `json:"start_time"`
	EndTime             *xsql.Time `json:"end_time"`
	DurationMs          int64      `json:"duration_ms"`
	Extra               []byte     `json:"extra"` // jsonb, run results specific to the node type
}

// McpConnection is an MCP client connection configuration.
// It maps to the database table t0.mcp_connections.
type McpConnection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`         // machine identifier, used as the tool name prefix, ASCII letters and digits only, globally unique
	DisplayName string     `json:"display_name"` // display name, may contain Chinese / spaces; falls back to Name when empty
	Description string     `json:"description"`
	URL         string     `json:"url"`       // for streamable_http; empty for stdio
	Command     string     `json:"command"`   // for stdio; the local executable command
	Args        xsql.JSONB `json:"args"`      // for stdio; the command argument array
	WorkDir     string     `json:"work_dir"`  // for stdio; the child process working directory, mapped to cmd.Dir
	Env         xsql.JSONB `json:"env"`       // for stdio; environment array ["K=V", ...], appended to the inherited os.Environ()
	Transport   string     `json:"transport"` // "streamable_http" | "stdio"
	Auth        xsql.JSONB `json:"auth"`      // auth configuration (none/bearer/oauth); ignored for stdio, where credentials go through env
	IsActive    bool       `json:"is_active"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
}

// Plugin is one tenant's enablement and implementation configuration for an on-disk plugin.
// It maps to the database table t0.plugins.
//
// The plugin itself lives on disk ({root_dir}/plugins); this only records "which implementation this tenant uses and what
// the keys are":
//   - Kind      the plugin type (currently always tools; together with the unique key (kind, name) it identifies the row)
//   - Name      the plugin name (= the plugin directory name = the grant ref's subject), such as web_search
//   - Impl      the primary implementation name (tavily / searxng / bing_rss ...), which must exist in the manifest
//   - Fallbacks the fallback chain (an ordered array): when the primary implementation fails, try these in order
//   - Configs   configuration stored per implementation: {"tavily":{"api_key":"..."},"searxng":{"endpoint":"..."}}
//     per-implementation keys are deliberate — switching the primary implementation must not wipe the other vendor's key
//
// There is no such thing as "installed or not": every plugin on disk appears in the admin side, and one row here means
// "enabled".
type Plugin struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // plugin type; it must be given explicitly when writing (the repository rejects an empty value)
	Name      string     `json:"name"`
	Impl      string     `json:"impl"`
	Fallbacks xsql.JSONB `json:"fallbacks"`
	Configs   xsql.JSONB `json:"configs"`
	IsActive  bool       `json:"is_active"`
	CreatedAt xsql.Time  `json:"created_at"`
	UpdatedAt xsql.Time  `json:"updated_at"`

	// The next three columns are observability, unrelated to "configuration": a call never touches updated_at
	// (that column is the configuration version, and polluting it with call frequency would make "when was the
	// configuration changed" impossible to answer).
	LastUsedAt  *time.Time `json:"last_used_at"`  // the last successful call
	LastError   string     `json:"last_error"`    // a one-line reason for the last failure (already truncated)
	LastErrorAt *time.Time `json:"last_error_at"` // when that error happened
}

// FallbackImpls is the fallback implementation chain; a JSON decode failure is treated as an empty chain (bad data must
// not break a call).
func (p *Plugin) FallbackImpls() []string {
	raw := jsonbRaw(p.Fallbacks)
	if raw == nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ImplConfig returns one implementation's configuration; a missing one returns an empty map (not nil, so the caller can
// read keys directly).
func (p *Plugin) ImplConfig(impl string) map[string]any {
	out := map[string]any{}
	raw := jsonbRaw(p.Configs)
	if raw == nil {
		return out
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return out
	}
	if c, ok := all[impl]; ok && c != nil {
		return c
	}
	return out
}

// AllConfigs returns every implementation's configuration (keyed per implementation). The admin side uses it to keep the
// other vendor's key when switching implementations.
func (p *Plugin) AllConfigs() map[string]map[string]any {
	out := map[string]map[string]any{}
	raw := jsonbRaw(p.Configs)
	if raw == nil {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]map[string]any{}
	}
	for k, v := range out {
		if v == nil {
			out[k] = map[string]any{}
		}
	}
	return out
}

// jsonbRaw restores the internal value of an xsql.JSONB back to JSON bytes; an empty value returns nil.
func jsonbRaw(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return []byte(v)
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return b
	}
}

// McpConnectionTool is a tool under an MCP connection.
// It maps to the database table t0.mcp_connection_tools. The tool list is discovered from the remote side and persisted
// when the connection is enabled, for local node configuration such as the workflow canvas.
type McpConnectionTool struct {
	ID           string     `json:"id"`
	ConnectionID string     `json:"connection_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	InputSchema  xsql.JSONB `json:"input_schema"`
	UpdatedAt    xsql.Time  `json:"updated_at"`
}

// RpcConnection is an RPC client connection configuration.
// It maps to the database table t0.rpc_connections and is used to reach an external RPC server (JSON-RPC 2.0).
type RpcConnection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`         // machine identifier, used as the tool name prefix, ASCII letters and digits only, globally unique
	DisplayName string     `json:"display_name"` // display name, may contain Chinese / spaces; falls back to Name when empty
	Description string     `json:"description"`
	URL         string     `json:"url"`       // for streamable_http; empty for stdio
	Command     string     `json:"command"`   // for stdio; the local executable command
	Args        xsql.JSONB `json:"args"`      // for stdio; the command argument array
	WorkDir     string     `json:"work_dir"`  // for stdio; the child process working directory, mapped to cmd.Dir
	Env         xsql.JSONB `json:"env"`       // for stdio; environment array ["K=V", ...], appended to the inherited os.Environ()
	Transport   string     `json:"transport"` // "streamable_http" | "stdio"
	Auth        xsql.JSONB `json:"auth"`      // auth configuration (none/bearer/oauth); ignored for stdio, where credentials go through env
	IsActive    bool       `json:"is_active"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
}

// RpcConnectionTool is a tool under an RPC connection.
// It maps to the database table t0.rpc_connection_tools. The tool list is discovered from the remote RPC server via
// JSON-RPC tools/list and persisted when the connection is enabled, for local node configuration such as the workflow
// canvas.
type RpcConnectionTool struct {
	ID           string     `json:"id"`
	ConnectionID string     `json:"connection_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	InputSchema  xsql.JSONB `json:"input_schema"`
	UpdatedAt    xsql.Time  `json:"updated_at"`
}
