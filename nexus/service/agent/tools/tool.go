package tools

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versioning"
	"flame/tools/sandbox"
)

// DefinitionMode decides which copy of a definition a run reads: the draft, or the released current version.
//
// The two modes only affect "reading a definition" (AGENT.md / SKILL.md / skill script directories / tool script directories); writes always go
// to the draft - editing means editing the draft, and "editing some historical version" does not exist.
type DefinitionMode string

const (
	// ModeDraft reads the draft directory (the editor, the debugger, trial chat). This is the zero value.
	ModeDraft DefinitionMode = "draft"
	// ModeCurrent reads the version marked is_current (production entries: RPC / MCP / endpoints / a production flow run).
	// Without a current version it returns versions.ErrNoReleasedVersion and **does not fall back to the draft**.
	ModeCurrent DefinitionMode = "current"
)

// IsCurrent reports whether this run reads the released version.
func (m DefinitionMode) IsCurrent() bool { return m == ModeCurrent }

// DefaultMaxOutput is the default byte cap for command output (64KB).
const DefaultMaxOutput = 64 * 1024

// globalSandbox is the command-execution sandbox client (a process-level singleton injected by main at startup).
var globalSandbox sandbox.Sandbox

// SetSandbox sets the global command sandbox (called at startup).
func SetSandbox(sb sandbox.Sandbox) { globalSandbox = sb }

// CurrentSandbox returns the sandbox client in effect.
//
// It returns nil when nothing was injected and the caller reports an error - there is no silent fallback to "unisolated passthrough", which
// would let commands run with the service process's privileges when the configuration is missing.
func CurrentSandbox() sandbox.Sandbox { return globalSandbox }

// globalInteractive is the long-lived interactive (/v1/interactive) client (a process-level singleton injected by main at startup).
var globalInteractive *sandbox.InteractiveClient

// SetInteractive sets the global interactive client (called at startup).
func SetInteractive(c *sandbox.InteractiveClient) { globalInteractive = c }

// CurrentInteractive returns the interactive client in effect; nil when nothing was injected.
func CurrentInteractive() *sandbox.InteractiveClient { return globalInteractive }

// interactiveToolIDs are the built-in tools that go through /v1/interactive (session-level resident processes).
//
// They are peers: one session occupies one tool entry each in sandboxd's registry (they share one container under docker), so reclamation must
// kill each of them - miss one and one process is left behind.
var interactiveToolIDs = []string{"shell", "browser"}

// KillInteractive destroys interactive processes; the key must be computed exactly like the shell / browser tools do:
// SessionID first (session level, reusable), RunID as the fallback (temporary, dies with the run).
//
// It is used to reclaim the resident processes an agent started when a run is stopped; a normal end **must not** call it - a session-level
// browser is meant to be reused by the next conversation, and killing it early loses page state.
//
// The last resort is sandboxd's idle timeout; a failure only affects how fast resources come back, not correctness, so errors are swallowed
// silently here.
func KillInteractive(sessionID, runID string) {
	key, ephemeral := sessionID, false
	if key == "" {
		key, ephemeral = runID, true
	}
	if globalInteractive == nil || key == "" {
		return
	}
	for _, toolID := range interactiveToolIDs {
		_ = globalInteractive.Kill(context.Background(),
			sandbox.InteractiveSession{Key: key, Ephemeral: ephemeral}, toolID)
	}
}

// KillInteractiveRun destroys a run's interactive processes (the fast path when a session-less run ends).
func KillInteractiveRun(runID string) { KillInteractive("", runID) }

// Tool categories (mapping to the natural groups builtin:<Category>): used for grouped granting and panel display in the grant system.
// Every built-in tool **should** carry an explicit category; an uncategorised tool appears in no category group
// (it can still be granted by its single-tool ref, see the Source handling in toolgrant.BuiltinTools).
//
// A category **is** a permission surface: who a group may be granted to is decided by whether it is in supervisorOnlyCategories,
// so changing a category is a serious act - moving a non-super-agent tool into an *-admin group revokes its grantability, and the reverse is an
// escalation.
//
// The thirteen groups (who may receive them is decided by toolgrant.Catalog's isSupervisor view):
//
//	file / exec / shell / browser / agent / skill      ordinary capabilities, grantable to any agent
//	skill-own                                          the private-skill trio, grantable to any agent but not granted by default
//	agent-admin / skill-admin / tool-grant / custom-tool / flow-admin
//	                                                   super agent only (the management surface)
//
// These 19 management tools used to share one CategorySuper; splitting them into five groups lets "grant on demand" be precise down to one
// class of capability (see models.SupervisorCoreGrants and docs/workspace-layout-and-supervisor-power.md §2.4).
const (
	CategoryFile    = "file"
	CategoryExec    = "exec"
	CategoryShell   = "shell"
	CategoryScript  = "script"
	CategoryBrowser = "browser"
	CategoryAgent   = "agent"
	CategorySkill   = "skill"
	// CategorySkillOwn is the private-skill trio (create/update/delete_own_skill).
	// Why it is separate from skill: skill is "look at skills", this is "write your own capability surface",
	// and the risk tiers differ (D20 deliberately keeps it out of DefaultAgentTools), so one group cannot grant them separately.
	CategorySkillOwn = "skill-own"

	// The five groups below are super agent (supervisor) only (criterion in supervisorOnlyCategories).
	// Note: the criterion is the agents.is_supervisor attribute, not the name "main" - main is just the preinstalled supervisor.
	CategoryAgentAdmin = "agent-admin" // create / update / delete agents + skill binding
	CategorySkillAdmin = "skill-admin" // create / update / delete shared skills + other agents' private skills
	CategoryToolGrant  = "tool-grant"  // tool grants + call grants
	CategoryCustomTool = "custom-tool" // create / update / delete / list custom tools
	// CategoryFlowAdmin is create / update / delete / list workflows + trial runs / run records + exposing endpoints.
	// Same tier as agent-admin: creating a workflow changes the tenant's outward-facing capability surface.
	// Read-only belongs here too - workflows have no "discover the collaborator" scenario like agents do,
	// and an ordinary agent holding list / read would only gain a grant with no visible purpose, so the group is closed as a whole.
	CategoryFlowAdmin = "flow-admin"

	// CategorySuper is a **transitional alias**: the only "super agent exclusive" category before the four-way split.
	// No tool belongs to it any more; it exists only for legacy grant rows (builtin:super) -
	// toolgrant expands it into the union of the four groups. It can go once the existing rows are cleaned up (see TODO).
	CategorySuper = "super"
)

// supervisorOnlyCategoryList holds the "super agent only" categories (the order is the catalog's expansion order).
// This is the single source of truth and the map is derived from it - one list only, so "missing from the list but present in the map" cannot
// happen.
//
// A new management category must be added here, or its tools show up in the grant catalog for non-super agents too
// (the execution layer has a hard gate as well, but exposing it in the catalog already promises the permission to administrators).
var supervisorOnlyCategoryList = []string{
	CategoryAgentAdmin,
	CategorySkillAdmin,
	CategoryToolGrant,
	CategoryCustomTool,
	CategoryFlowAdmin,
}

// supervisorOnlyCategories is the set of "super agent only" categories - the **single criterion** of the permission boundary.
var supervisorOnlyCategories = func() map[string]bool {
	m := make(map[string]bool, len(supervisorOnlyCategoryList))
	for _, c := range supervisorOnlyCategoryList {
		m[c] = true
	}
	return m
}()

// SupervisorOnlyCategories returns the super-agent-only categories (stable order). Used to expand the builtin:super union.
func SupervisorOnlyCategories() []string { return supervisorOnlyCategoryList }

// IsSupervisorOnlyCategory reports whether a category is super agent only.
func IsSupervisorOnlyCategory(category string) bool { return supervisorOnlyCategories[category] }

// IsSupervisorOnlyTool reports whether a built-in tool is super agent only.
// The single criterion: the category the tool declares in Meta() falls in supervisorOnlyCategories.
func IsSupervisorOnlyTool(name string) bool {
	t, ok := Get(name)
	return ok && supervisorOnlyCategories[t.Meta().Category]
}

// CapabilityOf maps a tool category to the capability it belongs to (empty = not bound by the platform capability policy).
//
// The tiers follow the **process shape**, not "what it can run": exec / shell / script can all execute arbitrary code
// (os.system inside a script is equivalent to exec), and they are split because their lifetimes and resource gates differ, not because of a
// permission boundary.
//
// The earlier position was "three in one = sandbox.exec" (the argument being that gating shell but not exec is no gate at all);
// that argument still holds on the permission axis, so no tier is claimed to be "least privilege" here.
//
// Note that CategoryScript currently has **no built-in tool**: the only run_skill_script goes through /v1/exec
// (a one-shot process, no pool) and therefore already belongs to the exec tier. sandbox.code gates the code node of a workflow, judged on the
// flow/executors side (ensureCodeCapability) rather than through tool categories. The mapping is kept so that capability <-> category stays
// one-to-one, instead of deleting a branch just because no tool uses it today.
//
// Tenant custom tools (custom__*) likewise bypass categorisation (they are not in the registry and have no ToolMeta);
// their capability criterion is hardcoded in tools/executeCustomTool and they belong to the exec tier as well.
func CapabilityOf(category string) string {
	switch category {
	case CategoryExec:
		return capability.Exec
	case CategoryShell:
		return capability.Shell
	case CategoryScript:
		return capability.Code
	case CategoryBrowser:
		return capability.Browser
	}
	return ""
}

// ToolMeta is the tool definition visible to the LLM (aligned with xllm.Tool).
type ToolMeta struct {
	Name        string
	Category    string // tool category: file / exec / agent / skill (empty = filed under other)
	Description string
	Parameters  map[string]any // JSON Schema object
}

// Tool is the built-in tool interface.
type Tool interface {
	Meta() ToolMeta
	// Execute runs the tool; args are the arguments the LLM passed; it returns a structured result (serialised by the caller).
	Execute(ctx context.Context, c *Context, args map[string]any) (any, error)
}

// PluginRunner runs "on-disk plugin" tools (injected by main; nil = plugin tools unavailable).
//
// Why an interface: plugin tool names are not registered in this package's tool table (they are discovered at runtime),
// yet ExecuteTool must be able to forward calls to them. This package only knows "name -> execution" and should not know about plugin
// directories, Python processes or tenant config - those live in service/plugin.
type PluginRunner interface {
	// Has reports whether a name is a discovered plugin tool (manifest only, tenant config untouched).
	Has(name string) bool
	// Invoke runs one call; schema locates the tenant config (which tenant enabled which implementation, and with which key).
	Invoke(ctx context.Context, schema, toolName string, args map[string]any) (any, error)
}

// globalPlugins is the process-level plugin runner (injected by main at startup, the same convention as SetSandbox).
var globalPlugins PluginRunner

// SetPluginRunner sets the global plugin runner (called at startup).
func SetPluginRunner(r PluginRunner) { globalPlugins = r }

// CurrentPluginRunner returns the plugin runner in effect; nil when nothing was injected.
func CurrentPluginRunner() PluginRunner { return globalPlugins }

// pluginRunner picks the plugin runner this context should use: one injected on the Context wins (handy for unit tests).
func (c *Context) pluginRunner() PluginRunner {
	if c.Plugins != nil {
		return c.Plugins
	}
	return globalPlugins
}

// AgentInfo is the brief information of an agent instance (returned by list_agents).
type AgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      int    `json:"status"` // 1: normal, 3: deprecated (see models.StatusXxx)
	// IsSupervisor reports whether it carries the super-privilege attribute (agents.is_supervisor).
	// read_agent displays it; call grants no longer exclude it - the supervisor can be called,
	// and resolveDefinitionMode forces it to read the draft when invoked.
	IsSupervisor bool `json:"is_supervisor"`
	// The two agent-level switches (columns of the same name in the agents table), carried along so read_agent can show the current state:
	//
	//	SkillDiscovery   : whether discovering unbound skills is allowed
	//	ToolsUnrestricted: whether tools are "unrestricted" (candidates not narrowed by allow)
	SkillDiscovery    bool `json:"skill_discovery"`
	ToolsUnrestricted bool `json:"tools_unrestricted"`
	// OutputLanguage is agents.default_language, already normalized ("" = not configured): the language this agent
	// replies in unless a caller pins another one for a single call. read_agent shows it, update_agent writes it.
	OutputLanguage string `json:"output_language"`
}

// AgentManager registers and queries agent instances (used by the meta tools create_agent / list_agents).
// It is injected by the agent runtime, and the implementation carries the tenant schema and repositories itself.
type AgentManager interface {
	// Create creates an agent instance. isSupervisor=true materialises the supervisor starter pack
	// (the core tool group + unrestricted tools + skill discovery + the four authoring skills),
	// with the same result as "SetSupervisor(true) after creation" (the same helpers).
	// defaultLanguage is the default reply language (agents.default_language, a BCP-47 tag; empty = follow the caller).
	// It is written by the same INSERT, so a malformed tag fails the whole creation instead of leaving a half-made agent.
	Create(ctx context.Context, name, description, defaultLanguage string, isSupervisor bool) (string, error)
	List(ctx context.Context) ([]AgentInfo, error)
	Exists(ctx context.Context, name string) (bool, error)
	// Get takes an agent's brief information (id / status included) by exact name; missing returns (nil, nil).
	// The cross-domain write gate (tools/gate.go) uses it to read the target's status and owning id.
	Get(ctx context.Context, name string) (*AgentInfo, error)
	// Update renames / re-describes an agent by name (a rename migrates the definition-domain directory too) and returns the final name.
	Update(ctx context.Context, name, newName, description string) (string, error)
	// SetUnrestricted sets an agent's (by uuid) "unrestricted tools" switch (agents.tools_unrestricted).
	// It is an attribute of the agent itself, unrelated to tool grants (agent_tool_grants); only the super agent may change it,
	// and it may target itself (D5 revision: the boundary is carried by status, and only the admin side can change status).
	SetUnrestricted(ctx context.Context, agentID string, unrestricted bool) error
	// SetOutputLanguage sets an agent's (by uuid) default reply language (agents.default_language, a normalized
	// BCP-47 tag); an empty tag clears it. It is a single-column write, not the full-row Update: the caller must never have
	// to carry the other definition columns over (same reasoning as SetUnrestricted / SetSupervisor).
	SetOutputLanguage(ctx context.Context, agentID, tag string) error
	// SetSupervisor sets an agent's (by uuid) super-privilege attribute (agents.is_supervisor).
	// Only a supervisor may change it, and it may target itself; demoting the "last supervisor" is refused by the repository.
	// The target must be in the normal state (a deprecated agent accepts no attribute change).
	SetSupervisor(ctx context.Context, agentID string, isSupervisor bool) error
	// Delete deletes an agent by name (cleaning up its definition-domain directory too).
	Delete(ctx context.Context, name string) error
}

// CallRequest is one agent-call request.
type CallRequest struct {
	CallerID   string         // uuid of the calling agent (permission check)
	CallerName string         // name of the caller (for error messages)
	CalleeName string         // name of the target agent
	Args       map[string]any // arguments passed to the target agent
	Depth      int            // current call depth (the callee runs at Depth+1)
	UserID     string         // current user uuid (passed through to the callee)
	SessionID  string         // current session id (passed through: the child agent shares the parent's workspace)
	// Chain is the caller's current ancestor chain (the callee excluded): the caller appends "itself" and passes it down,
	// and the callee uses it to detect cycles. The same "derive, do not accumulate" pass-through as Depth.
	Chain []string
	// DefinitionMode is this run's definition-reading mode, **passed through to the callee**:
	// a production parent -> the child reads the released version too; a debugging parent -> the child reads the draft.
	// Without the pass-through, "while debugging the child agent runs the live version" appears, and that is the easiest thing to be fooled by
	// during triage.
	DefinitionMode DefinitionMode
	// OutputLanguage is the language the caller pinned for this chain (already validated), and only that: a resolved default must
	// not travel, or it would become a pin for the callee. The pass-through matters most here - the callee's input is a JSON
	// argument map, so without it the callee's model has no language signal at all.
	OutputLanguage string
}

// AgentCaller calls other agents (implemented and injected by service/agent, to avoid an import cycle).
type AgentCaller interface {
	Call(ctx context.Context, req CallRequest) (string, error)
	// Callable lists the agents the caller may currently call (name + description), for injection into the system prompt.
	// The criterion is the same source as Call's authorization check: an agent_relations out-edge exists (no row = refused) ∩ runnable ∩ not
	// the super agent.
	Callable(ctx context.Context, callerID string) ([]AgentInfo, error)
}

// AgentGrantView is a read-only view of one agent tool grant (read_agent shows the current state with it).
//
// Effective is not a guess: the implementation reuses the runtime Resolver (the same one behind
// agentImpl.resolveGrantedToolNames and the admin-side GetAgentEffectiveTools), so "what the super agent sees" and "what the
// agent actually gets" cannot drift apart.
type AgentGrantView struct {
	Kind      string `json:"grant_kind"` // required | allow
	Ref       string `json:"ref"`
	ItemType  string `json:"item_type"` // group | tool
	Effective bool   `json:"effective"` // required always applies; allow is not activated at run start
	// Pending means an allow grant some skill declares, but that skill has not been use_skill'ed in this run yet: it applies once activated.
	Pending  bool   `json:"pending,omitempty"`
	ViaSkill string `json:"via_skill,omitempty"` // the skill that backs it (while pending = which skill lights it up)
	// Note is why it is inert / not activated, plus how to fix it.
	//
	// English: it is produced inside a tool call, where nothing carries the caller's language (the reply-language rule is what
	// restates it for the user). The UI does not render this field - if it ever does, it must become a code plus params
	// resolved from the locale bundle, like the `error` envelope.
	Note string `json:"note,omitempty"`
}

// ToolGrantsManager reads and writes agent tool grants (implemented and injected by package service/agent).
//
// Why validation is not in this package: toolgrant imports tools (to read the built-in tools and categories), and the reverse
// import would be a cycle - so ref validation (Catalog / ValidateRef) and the allow / required allocation logic live in the
// implementation; this package only declares the interface and the read-only views.
type ToolGrantsManager interface {
	// ListAgentGrants lists an agent's grants and their current effectiveness (as of the run start):
	// required always applies; an allow applies only when a skill activated by this run's use_skill declares it,
	// otherwise it is either pending (which skill lights it up) or inert (never applies).
	ListAgentGrants(ctx context.Context, agentName, agentID string) ([]AgentGrantView, error)
	// GrantAgentTool assigns one grant to an agent (idempotent: the same ref is overwritten).
	// An empty kind means "let the implementation decide": an injected skill of the target declares the dependency -> allow, else required.
	// An explicit required / allow / deny is validated by its semantics (an allow needs a declarer, or the row never applies).
	// It returns the kind actually written and the reason (the reason says whether an existing row was overwritten).
	GrantAgentTool(ctx context.Context, agentName, agentID, ref, kind string) (kindOut, reason string, err error)
	// RevokeAgentTool removes one grant of an agent by ref (idempotent) and returns the number of deleted rows plus that row's kind.
	// It deliberately does not validate that the ref exists: a dangling grant whose source is gone (the connection / endpoint
	// was deleted) must still be revocable.
	RevokeAgentTool(ctx context.Context, agentID, ref string) (removed int64, kind string, err error)
}

// ToolSearchItem is one entry in the catalog: a grantable group, or a grantable single tool.
//
// Ref is a **grant ref that can be pasted straight into grant_agent_tool / revoke_agent_tool**
// (mcp:fs / mcp:fs:read_file / builtin:file), not a model-side tool name -
// this tool is for "search, then grant", not "search, then call".
type ToolSearchItem struct {
	Ref   string `json:"ref"`
	Name  string `json:"name,omitempty"` // model-side tool name (single tools only): mcp__fs__read_file
	Label string `json:"label"`
	Kind  string `json:"kind"` // builtin | mcp | rpc | local | plugin | script | group | preset
	// Description comes verbatim from the tool / group definition (may be empty).
	Description string `json:"description,omitempty"`
	// Count is the member count for a group; always 0 for a single tool.
	Count int `json:"count,omitempty"`
	// Members are the member grant refs for a group (builtin:read_file / mcp:fs:query ...); always empty for a single tool.
	//
	// Why the model sees them: a grant operates on a ref, and "what is actually inside a group called fs-basic" is in
	// neither the tool name nor the description - without a member summary the model can only grant a whole group blindly
	// and only finds out about over- or under-granting afterwards.
	// Only the first few are listed (Count still reports the real total); use a query match to expand the full list.
	Members   []string `json:"members,omitempty"`
	Available bool     `json:"available"`
	// UnavailableKind/Reason attributes unavailability (values in toolgrant.Unavailable*):
	// platform capability not enabled / connection disabled / endpoint not exposed - each has a different fix.
	UnavailableKind   string `json:"unavailable_kind,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// SupervisorOnly means only the super agent may bind / run it.
	SupervisorOnly bool `json:"supervisor_only,omitempty"`
	// Granted is the kind this ref already holds on the target agent (required / allow / deny; empty = not granted).
	// It is filled only when Search receives a target agent, so that re-granting is avoided and "what is already granted" is visible.
	Granted string `json:"granted,omitempty"`
}

// ToolSearchResult is the result of one catalog search.
type ToolSearchResult struct {
	Query     string           `json:"query"`
	Groups    []ToolSearchItem `json:"groups"`
	Tools     []ToolSearchItem `json:"tools"`
	Truncated bool             `json:"truncated,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
}

// ToolCatalogManager is the read-only search over the grantable tool catalog (used by search_tools; nil = unavailable).
//
// Why it is injected instead of implemented in this package: toolgrant imports this one (for the built-in tools and
// categories), so the reverse import would be a cycle - the same reason as ToolGrantsManager.
type ToolCatalogManager interface {
	// Search looks up grantable groups and tools by keyword.
	//
	// agentName/agentID is the **target** agent (which may differ from the caller): the catalog is generated for its
	// identity ("super agent only" tools do not appear in a non-super agent's candidates - granting them would still not
	// run, and showing them only lures the caller into a grant that must fail), and its existing grants are filled in.
	Search(ctx context.Context, query, agentName, agentID string) (*ToolSearchResult, error)
}

// AgentRelationsManager reads and writes agent-to-agent call grants (agent_relations out-edges; implemented by service/agent).
//
// The same source as caller.go: a grant is one row in agent_relations, and every agent is treated alike (the super agent gets
// no exemption).
// The super agent is allowed to add out-edges for itself (see the implementation's comment) - that widens "whom the super agent
// may call", not its own tool surface.
type AgentRelationsManager interface {
	// AllowCall appends a "caller may call callee" grant (idempotent).
	AllowCall(ctx context.Context, callerID, calleeID string) error
	// RevokeCall removes one call grant (idempotent) and returns the number of deleted rows.
	RevokeCall(ctx context.Context, callerID, calleeID string) (int64, error)
	// ListRelations lists an agent's out-edges (whom it may call) and in-edges (who may call it), returning agent names.
	ListRelations(ctx context.Context, agentID string) (canCall, calledBy []string, err error)
}

// Skill ownership scopes.
const (
	SkillScopeTenant = "tenant" // tenant-level (agent_id is NULL, visible to every agent)
	SkillScopeAgent  = "agent"  // agent-private (agent_id = the current agent)
)

// SkillInfo is a skill's brief information (returned by list_skills).
type SkillInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`  // tenant / agent
	Status      int    `json:"status"` // 1: normal, 3: deprecated (the cross-domain write gate reads it)
	// Restricted only matters for tenant-level shared skills: true = visible to allowlisted agents only (always false for private skills).
	Restricted bool `json:"restricted"`
	// BoundAgents are the allowlisted agent names of this skill (= the skill-side view of agent_skill_bindings).
	// Filled only from the super agent's viewpoint (a non-super agent has no need, and no right, to see other agents' bindings);
	// always empty for a private skill.
	BoundAgents []string `json:"bound_agents,omitempty"`
}

// SkillDetail is a skill's full information (returned by read_skill / use_skill), content included.
// Restricted / BoundAgents come from the embedded SkillInfo and are not redeclared here (to avoid shadowing fields of the same name).
type SkillDetail struct {
	SkillInfo
	Content string `json:"content"`
	// ToolDeps are the tool dependencies this skill declares, **as raw refs** (e.g. builtin:exec / mcp:fs:read_file).
	//   - groups are not expanded: this shows the definition as written, and the expansion (leaf tools) changes with grants and
	//     connections, which is the Resolver's job;
	//   - dangling entries are not filtered out: a dead ref left behind by a deleted tool is handed over as is, which actually
	//     helps diagnosis (revoke can still clear it);
	//   - read_skill only displays them, use_skill displays and activates them (activation is still narrowed by allow, see D28).
	// Always an array ([] when there are no dependencies), so that null cannot be confused with "unreadable".
	ToolDeps []string `json:"tool_deps"`
}

// SkillDepChange is the return of grant_skill_tool / revoke_skill_tool: this change plus the full dependency list afterwards.
//
// ToolDeps is echoed back so that "what an incremental edit produced" is visible on the spot: the model need not call
// read_skill again just to confirm the result, and therefore will not keep editing blindly in the next step.
type SkillDepChange struct {
	Skill    string   `json:"skill"`
	SkillID  string   `json:"skill_id,omitempty"`
	Ref      string   `json:"ref"`
	Added    bool     `json:"added,omitempty"`
	Removed  int64    `json:"removed,omitempty"`
	ToolDeps []string `json:"tool_deps"`
}

// SkillRef locates one skill.
//
// Precedence: ID -> Scope (space targeting) -> Name (the current agent's visibility).
//
//	ID   : globally unique, may cross ownership; without a Scope it is a "targeted lookup" and needs the super agent;
//	Scope: the tool itself declares which space to look in (own series = agent / shared series = tenant);
//	       the tool name is the criterion and the model cannot pass it in - so a shared skill cannot be mistaken for a
//	       private one and edited by accident;
//	both empty: the visibility criterion is used (what read_skill does).
type SkillRef struct {
	ID    string // non-empty: locate by id
	Name  string // skill name
	Scope string // agent = private space; tenant = tenant-level shared; empty = the current agent's visibility
	// OwnerID is the **owning agent uuid** of the private space (empty = the current agent). It only matters when Scope=agent:
	// non-empty means "an explicit owner", and only the *_agent_skill series (super agent only) uses it.
	OwnerID string
}

// SkillInput is the input for creating / updating a skill.
type SkillInput struct {
	ID          string // targeted lookup: non-empty means by id; without a Scope it needs the super agent
	Name        string // current name (used to locate the skill on update)
	NewName     string // new name when renaming (optional, empty = no rename)
	Description string
	Content     string
	// OwnerID is the target owning agent uuid (empty = private to the current agent). It only matters when Scope=agent;
	// non-empty means "create / edit that agent's private skill on its behalf", and only the *_agent_skill series
	// (super agent only) passes it.
	OwnerID string
	// ToolDeps are the tool dependencies this skill declares (a list of refs: builtin:exec / builtin:file / mcp:fs ...).
	// nil = leave untouched (create = declare none; update = keep as is); non-nil and empty = clear.
	//
	// Only the super agent may set it: under the "inject on demand" rule an allow grant applies only when some
	// **injected skill** declares it (see toolgrant.Resolve), so whoever can write dependencies holds the grant switch.
	// Only the *_agent_skill / *_shared_skill series (category skill-admin) pass it.
	ToolDeps *[]string
	Scope    string // target space: tenant = tenant-level shared / agent = private space; empty on create means agent
	// Restricted is the visibility switch (nil = leave untouched): true = visible to allowlisted agents only, false = everyone.
	// It only matters for **tenant-level shared** skills (a private skill's visibility follows AgentID, so passing it is refused).
	// Only the *_shared_skill series passes it (see withRestricted); the allowlist is maintained by bind_skill / unbind_skill.
	Restricted *bool
}

// SkillsManager registers and queries skills (used by the meta tools).
// It is injected by the agent runtime; the implementation pins the current agent's identity internally, so the tool layer cannot
// pass it in, which rules out privilege escalation by construction.
type SkillsManager interface {
	Create(ctx context.Context, in SkillInput) (string, error)
	Update(ctx context.Context, in SkillInput) error
	List(ctx context.Context) ([]SkillInfo, error)
	// ListInjected lists the skills "injected into the current agent" (private + bound ones),
	// for the resident system-prompt list: only name/description, the body is loaded on demand with read_skill.
	ListInjected(ctx context.Context) ([]SkillInfo, error)
	Get(ctx context.Context, ref SkillRef) (*SkillDetail, error)
	// FindForWrite looks a skill up by "owner + name" (not narrowed by the current agent's visibility), so that the
	// cross-domain write gate can decide whether it exists: an empty ownerAgentID means tenant-level; missing returns (nil, nil).
	FindForWrite(ctx context.Context, ownerAgentID, skillName string) (*SkillInfo, error)
	Delete(ctx context.Context, ref SkillRef) error
	// BindAgentSkill appends one skill binding to an agent (by uuid; idempotent); tenant-level skill names only.
	BindAgentSkill(ctx context.Context, agentID, skillName string) error
	// UnbindAgentSkill removes one skill binding from an agent (by uuid; idempotent); tenant-level skill names only.
	UnbindAgentSkill(ctx context.Context, agentID, skillName string) error
	// ListAgentSkills lists the skill names an agent (by uuid) currently has bound (read-only; read_agent shows the current state).
	ListAgentSkills(ctx context.Context, agentID string) ([]string, error)
	// ListAgentPrivateSkills lists the names of an agent's (by uuid) **private** skills (read-only; read_agent shows the current state).
	ListAgentPrivateSkills(ctx context.Context, agentID string) ([]string, error)
	// SetAgentSkillDiscovery sets an agent's (by uuid) "discover unbound skills" switch.
	SetAgentSkillDiscovery(ctx context.Context, agentID string, discovery bool) error
	// AddToolDep appends one tool dependency to a skill (idempotent; super agent only).
	AddToolDep(ctx context.Context, ref SkillRef, toolRef string) (*SkillDepChange, error)
	// RemoveToolDep removes one tool dependency from a skill (idempotent; super agent only).
	RemoveToolDep(ctx context.Context, ref SkillRef, toolRef string) (*SkillDepChange, error)
}

// Context provides the tenant / instance context and the security boundary.
type Context struct {
	Schema    string // tenant schema
	AgentName string // instance name (the directory key)
	AgentID   string // instance uuid (used for skill ownership, call grants and so on)
	// IsSupervisor reports whether this instance carries the super privilege (agents.is_supervisor).
	//
	// The **single entry point** for the identity criterion: it is no longer decided by name (main is merely a
	// preinstalled supervisor).
	// newAgentImpl fills it from the instance; unit tests may set it to true directly to simulate a privileged agent.
	// It decides: whether management tools may run, the cross-domain file-write gate, and whether the grant catalog is
	// seen in full.
	IsSupervisor bool
	UserID       string // current user uuid (passed through to the callee)
	SessionID    string // session id (empty = a call without a session)
	RunID        string // id of this call (used as the temporary directory name when there is no session)
	WorkKey      string // storage-relative path of the working directory (the default root of the file tools)
	Workspace    string // host absolute path of the working directory (the host source mounted at /workspace for exec)
	AgentRoot    string // host absolute path of this agent's definition domain (the host source mounted at /agent for exec)
	SkillRoot    string // host absolute path of the tenant-level skill directory (the host source mounted at /skill for run_skill_script)
	// DefinitionMode decides whether to read the draft or the current version (zero value = draft, see DefinitionMode).
	//
	// Every construction site must choose explicitly: forgetting it at a production entry means "still reading the draft",
	// and the whole versioning scheme is wasted.
	DefinitionMode DefinitionMode
	// AgentVersion is this agent's current version number when DefinitionMode=current (0 = not bound).
	AgentVersion int
	// FollowDrafts decides whether the **child entities this run starts** (agent / flow) follow the draft.
	//
	// The split with DefinitionMode: that one governs "which copy I read", this one only "which copy the ones I start read".
	// The chat window exposes it as a switch (default true, the same name and meaning as the flow debugger's FollowDrafts);
	// production entries (MCP / RPC / endpoints / a production flow run) are always false - a draft must not spread along a
	// production chain.
	// The single entry point for the criterion is ChildMode; do not recompute it at each call site.
	FollowDrafts bool
	// RootDir is the storage root (agent.root_dir), used to turn a storage key into a host path for sandbox mounts.
	RootDir    string
	Storage    storage.Storage   // file storage backend (root = agent.root_dir)
	Agents     AgentManager      // agent instance management (for create_agent/list_agents; may be nil)
	Skills     SkillsManager     // skill registration and lookup (for the meta tools; may be nil)
	ToolGrants ToolGrantsManager // agent tool grant read/write (for grant/revoke_agent_tool; may be nil)
	// ToolCatalog is the read-only search over the grantable tool catalog (for search_tools; nil = that tool is unavailable).
	ToolCatalog ToolCatalogManager
	// Flows is workflow management (for create_flow / list_flows / read_flow / delete_flow; may be nil).
	// The same convention as Agents / Skills: the tenant schema and the current identity live inside the implementation, so
	// the tool layer cannot pass them in.
	Flows     FlowsManager
	Relations AgentRelationsManager // agent call grant read/write (for allow/revoke_agent_call; may be nil)
	Caller    AgentCaller           // calls other agents (for call_agent; nil = calling is unsupported)
	Plugins   PluginRunner          // on-disk plugin runner (nil = use the process-level one; if neither exists, plugin tools are unavailable)
	// Externals runs external connection tools (mcp__<connection>__<tool> / rpc__<connection>__<tool>).
	// nil = use the process-level one; if neither exists, external tools are unavailable (the name falls through to ErrToolNotFound).
	Externals ExternalRunner
	// Endpoints runs local endpoint tools (local__<endpoint name>, running the flow in-process).
	// nil = that capability is unavailable (the name falls through to ErrToolNotFound).
	Endpoints EndpointRunner
	// CustomTools is the custom tool source (nil = this tenant has no custom tool system, so every custom__* is not found).
	CustomTools CustomToolSource
	// CustomToolsAdmin creates / updates / deletes custom tools (for the meta tools; nil = that capability is unavailable).
	// It is separate from CustomTools: that one is the read-only view on the execution hot path, this one is admin-only semantics.
	CustomToolsAdmin CustomToolAdmin
	// ToolsRoot is the host absolute path of the tenant's custom tool directory ({root}/tenants/{schema}/tools),
	// mounted read-only at /tool while a script runs.
	ToolsRoot string
	CallDepth int // current agent call depth (0 at the top; the callee runs at +1)
	// CallChain is the ancestor chain (see shared/callchain): the caller injects it when entering this agent,
	// and it is carried outbound (rpc__ external connection / call_agent) unchanged - going outbound does not append
	// itself; the append happens on the side that is about to execute an entity.
	CallChain []string
	// OutputLanguage is the reply language resolved for this run, already validated by core.NormalizeOutputLanguage: a caller-pinned
	// `_meta.language`, else the agent's `default_language`, else the product default (English). It is carried into
	// sub-calls so a chain does not lose it.
	OutputLanguage string
	// OutputLanguagePinned says whether that language came from the caller (true) or is only a default (false). The prompt words
	// the two differently: a pin overrides the language of the input, a default only applies when the input carries no signal.
	OutputLanguagePinned bool
	Timeout              time.Duration     // timeout of one tool call
	Sandbox              sandbox.Sandbox   // command sandbox (nil = unisolated "none")
	Env                  map[string]string // extra environment variables for command execution
	MaxOutput            int               // command output byte cap (0 = default)
	// AllowedTools is the set of tool names this run may execute (= the tools array declared to the model).
	// When non-nil, ExecuteTool refuses any tool outside the set - the same criterion as "the grant catalog is produced per
	// identity".
	// Why it is needed: a text-form tool call (the model writes the call into its reply, e.g. the |DSML| marker) never
	// goes through the "is the tool granted" check; without closing that hole here, an ungranted agent could execute any
	// non-super tool just by writing the marker.
	// nil means the restriction is off (only for callers with no grant semantics, such as unit tests); the agent runtime
	// always sets it non-nil.
	AllowedTools map[string]bool
	// Caps are the sandbox capabilities the platform has enabled for this tenant (keys in shared/capability; a missing key
	// means not enabled).
	//
	// When non-nil, ExecuteTool refuses tools that "need a capability which is not enabled", from the same source as catalog
	// availability.
	// Why it is needed: a capability is platform-side policy and may be revoked between two calls; greying the catalog out
	// alone does not stop an agent that already bound it (the model still calls it from the old tool list).
	// nil means the restriction is off (only for callers with no tenant semantics, such as unit tests); the agent runtime
	// always sets it non-nil.
	Caps map[string]bool

	// The skills "activated" within this run (skillID -> name), registered after use_skill succeeds.
	// read_skill is pure viewing and does not write here - viewing and activation must stay decoupled.
	//
	// Purpose: declared tool dependencies used to count only "already bound" (persisted across runs); with this extra
	// registry "read once = injected for this run" - after reading SKILL.md, the model sees the tools it needs on the next
	// LLM call, and everything disappears with the Context when the run ends. Thus "whether to use a skill" is decided by
	// the model on demand, and nothing lingers (neither the body nor the tools survive across runs; see
	// core.RecordsToMessages, which refills text only).
	//
	// It is only read and written during serial tool execution within a run; the lock is there to prevent clobbering if
	// tools are ever executed concurrently.
	loadedMu     sync.Mutex
	loadedSkills map[string]loadedSkill
	skillsDirty  bool
}

// loadedSkill is one "skill activated in this run" record: the name plus a snapshot of the dependency declaration at
// activation time.
type loadedSkill struct {
	name string
	deps []string
}

// LoadedSkill is a snapshot of an activated skill, used by tool resolution to assemble the candidates (see
// toolgrant.VisibleSkill).
type LoadedSkill struct {
	ID   string
	Name string
	// Deps is the dependency declaration obtained at activation; nil = the caller passed no snapshot (resolution falls
	// back to the database).
	Deps []string
}

// MarkSkillLoaded registers a skill read in this run together with its dependency snapshot, and sets the dirty flag so the
// tool set is recomputed for the next round.
//
// deps is the ToolDeps returned by use_skill - in a production run it comes from the current version and is part of the
// **definition**; passing nil means there is no snapshot, and tool resolution falls back to skill_tool_grants (the older
// criterion used by the admin preview and so on).
func (c *Context) MarkSkillLoaded(id, name string, deps []string) {
	if id == "" {
		return
	}
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	if c.loadedSkills == nil {
		c.loadedSkills = make(map[string]loadedSkill, 4)
	}
	if prev, ok := c.loadedSkills[id]; ok && prev.name == name && sameDeps(prev.deps, deps) {
		return // reading the same skill again: no change, no need to recompute
	}
	c.loadedSkills[id] = loadedSkill{name: name, deps: copyDeps(deps)}
	c.skillsDirty = true
}

// LoadedSkills returns a snapshot of the skills read in this run (skillID -> name).
func (c *Context) LoadedSkills() map[string]string {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	out := make(map[string]string, len(c.loadedSkills))
	for id, s := range c.loadedSkills {
		out[id] = s.name
	}
	return out
}

// LoadedSkillEntries returns a snapshot of the skills activated in this run (dependency declarations included).
// The agent loop assembles toolgrant.VisibleSkill from it; the caller decides the order, this method guarantees none.
func (c *Context) LoadedSkillEntries() []LoadedSkill {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	out := make([]LoadedSkill, 0, len(c.loadedSkills))
	for id, s := range c.loadedSkills {
		out = append(out, LoadedSkill{ID: id, Name: s.name, Deps: copyDeps(s.deps)})
	}
	return out
}

// copyDeps copies a dependency list while **preserving the difference between nil and an empty slice**:
// nil = no snapshot (fall back to the database), an empty slice = the snapshot really has no dependencies (do not query).
// It must not be written as append([]string(nil), deps...) - that degrades an empty slice into nil.
func copyDeps(deps []string) []string {
	if deps == nil {
		return nil
	}
	out := make([]string, len(deps))
	copy(out, deps)
	return out
}

// sameDeps compares two dependency snapshots (nil and an empty slice count as different, see copyDeps).
func sameDeps(a, b []string) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ConsumeSkillsDirty reads and resets the "the skill set changed" flag.
// The caller (the agent loop) uses it to decide whether to recompute the tool set - no change means no database query.
func (c *Context) ConsumeSkillsDirty() bool {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	dirty := c.skillsDirty
	c.skillsDirty = false
	return dirty
}

// MarkToolsDirty only sets the "the tool set needs recomputing" flag; it registers no skill.
//
// It covers the case of the super agent granting or revoking a tool for itself: this run's tool schema was fixed at the
// start of the run, so unless the model sees the change before the next session, it would think the operation failed and
// retry over and over.
// It reuses the same dirty bit (ConsumeSkillsDirty -> refreshToolDefs at the top of the loop).
func (c *Context) MarkToolsDirty() {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	c.skillsDirty = true
}

// MaxOutputLimit returns the output cap, falling back to the default when unset.
func (c *Context) MaxOutputLimit() int {
	if c.MaxOutput <= 0 {
		return DefaultMaxOutput
	}
	return c.MaxOutput
}

// StoragePath returns the storage-relative path of a file under the file tools' default root (this session's working
// directory).
//
// Defence in depth: the result must always stay inside this tenant's directory. safePath is the first line (rejecting
// absolute paths and ..), this is the second - so even a future tool that forgets safePath cannot cross tenants.
func (c *Context) StoragePath(rel string) string {
	if c.WorkKey == "" {
		panic("storage path: WorkKey is empty (the run domain is not initialized)")
	}
	p := path.Join(c.WorkKey, rel)
	if c.Schema != "" && !strings.HasPrefix(p, tenantsSubdir+"/"+c.Schema+"/") {
		panic("storage path escapes tenant: " + p)
	}
	return p
}

// AgentPath returns the storage-relative path of a file under this agent's definition domain:
// tenants/{schema}/agents/{name}/{rel}.
//
// Definition-domain reads and writes (the system prompt loading AGENT.md and so on) go through it, separately from the run
// domain's StoragePath - otherwise, once StoragePath is rerooted to the session working directory, AGENT.md becomes
// unreadable.
func (c *Context) AgentPath(rel string) string {
	if c.VersionBound() {
		return path.Join(AgentVersionKey(c.Schema, c.AgentName, c.AgentVersion), rel)
	}
	return path.Join(AgentsKey(c.Schema, c.AgentName), rel)
}

// VersionBound reports whether this run is bound to a released version.
func (c *Context) VersionBound() bool {
	return c.DefinitionMode.IsCurrent() && c.AgentVersion > 0
}

// ChildMode is the definition-reading mode the child entities this run starts (agent / flow) should use.
//
// The single formula: **child draft = own draft && FollowDrafts**.
// It is a method rather than a per-call-site calculation, to avoid splits like "call_agent follows the switch, local__ does
// not"; it also guarantees that a production entry never spreads a draft out (when the run itself is current the child is
// current too, regardless of the switch).
func (c *Context) ChildMode() DefinitionMode {
	if c.DefinitionMode.IsCurrent() || !c.FollowDrafts {
		return ModeCurrent
	}
	return ModeDraft
}

// HostPath turns a storage-relative key into a host absolute path (a sandbox mount source).
//
// Only local / nfs storage can serve as a mount source; under S3 there is no host path - so it returns an empty string and
// lets the caller report an error, instead of mounting a mangled path.
func (c *Context) HostPath(key string) string {
	if c.RootDir == "" || key == "" {
		return ""
	}
	return filepath.Join(c.RootDir, filepath.FromSlash(key))
}

// SkillReadDir is the directory used to read a skill's content / run its scripts: the storage key plus the host path.
//
// The split with the write path: writes always go to the draft (skillManager.skillPath), reads are resolved by mode.
// Both modes return the **skill directory** (not SKILL.md), because the caller still has to join a script's relative path.
//
// A private skill's version belongs to its owning agent: ownerName is that agent's name, and the version number is that
// agent's current version - a private skill has no version of its own (see nexus/docs/terminology.md).
func (c *Context) SkillReadDir(ownerName, skillName string) (key, hostDir string, err error) {
	if !c.DefinitionMode.IsCurrent() {
		// Draft mode keeps the pre-refactor way of computing the host path (joining SkillRoot / AgentRoot directly) and does
		// not depend on RootDir - unit tests and callers that inject only these two roots rely on it.
		if ownerName == "" {
			key = path.Dir(SkillPath(c.Schema, skillName))
			return key, filepath.Join(c.SkillRoot, skillName), nil
		}
		key = path.Dir(AgentSkillPath(c.Schema, ownerName, skillName))
		return key, filepath.Join(c.AgentRoot, agentSkillsSubdir, skillName), nil
	}

	switch {
	case ownerName == "":
		_, key, err = versioning.TenantSkillCurrentVersion(c.Schema, skillName)
	case ownerName == c.AgentName && c.VersionBound():
		key = c.AgentPath(path.Join(agentSkillsSubdir, skillName))
	default:
		var dir string
		_, dir, err = versioning.AgentCurrentVersion(c.Schema, ownerName)
		key = path.Join(dir, agentSkillsSubdir, skillName)
	}
	if err != nil {
		return "", "", err
	}
	hostDir = c.HostPath(key)
	if hostDir == "" {
		return "", "", fmt.Errorf(
			"skill %q version directory has no host path (the storage root is not initialized, or the current backend is object storage): %s", skillName, key)
	}
	return key, hostDir, nil
}

// ToolDir is the host directory of a custom tool's scripts (mounted read-only at /tool in the sandbox).
//
// A production run reads the tool's current version directory; without a version it reports ErrNoReleasedVersion - it must
// not fall back to the draft, or a "production run" would execute a script that was never released.
func (c *Context) ToolDir(name string) (string, error) {
	if !c.DefinitionMode.IsCurrent() {
		if c.ToolsRoot == "" {
			return "", fmt.Errorf("custom tool root is not initialized (tools dir)")
		}
		return filepath.Join(c.ToolsRoot, name), nil
	}
	_, key, err := versioning.ToolCurrentVersion(c.Schema, name)
	if err != nil {
		return "", err
	}
	dir := c.HostPath(key)
	if dir == "" {
		return "", fmt.Errorf("tool %q version directory has no host path (the storage root is not initialized, or the current backend is object storage)", name)
	}
	// The version row exists but the directory does not (hand-deleted / migration failure): say explicitly that the
	// version directory is missing, otherwise os.Stat only produces a vague "unreachable" that triage easily mistakes for a
	// storage configuration problem.
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return "", fmt.Errorf("tool %q current version (%s) directory does not exist: the version directory may have been removed or the migration failed; release it again before calling", name, key)
	}
	return dir, nil
}

// TenantPath returns the storage-relative path of a file under the tenant root: tenants/{schema}/{rel}.
//
// It serves root addressing (the fromRoot branch of SafePathEx): the super agent reaches the whole tenant, a non-super agent
// reaches only its own definition directory (read the whole tree, write only private skills; see checkReadOwn /
// checkWriteOwn in gate.go).
// Like StoragePath, the result always stays inside this tenant. rel is relative to the tenant root (e.g.
// agents/poet/AGENT.md).
func (c *Context) TenantPath(rel string) string {
	if c.Schema == "" {
		panic("tenant path: schema is empty (the tenant is not initialized)")
	}
	return path.Join(tenantsSubdir, c.Schema, rel)
}

// argString takes a string argument from args; absent yields an empty string.
func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// argBool takes a boolean argument from args; absent yields false.
func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// argInt takes an integer argument from args (tolerating the float64 JSON decoding produces); absent yields 0.
func argInt(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

// argObject takes an argument in object form; absent / null yields an empty map, a wrong type is an error right away.
//
// Type errors are not ignored silently: when the model writes an object as a JSON string (or the reverse), letting it pass
// as an empty object would fail tens of seconds later, during execution, as "the variable has no value" - far more
// expensive to triage than one rejection here.
func argObject(args map[string]any, key string) (map[string]any, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object (key/value pairs), got %T", key, raw)
	}
	return m, nil
}

// argStringSlice parses a string-array argument, tolerating []any / []string / a single comma-separated string.
// It returns (value, present): absent -> ok=false.
// tool_deps means "nil = leave untouched, an empty array = clear", so the two cases must stay distinguishable.
func argStringSlice(args map[string]any, key string) ([]string, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, false
	}
	pick := func(vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, s := range vals {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	switch v := raw.(type) {
	case string:
		return pick(strings.Split(v, ",")), true
	case []string:
		return pick(v), true
	case []any:
		vals := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				vals = append(vals, s)
			}
		}
		return pick(vals), true
	}
	return nil, false // unrecognised shape -> treat as "not passed": better to leave it untouched than to clear it by mistake
}

// limitedBuffer is an output buffer with a byte cap; past the cap it drops the excess and flags truncation.
type limitedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

// Write implements io.Writer. Past the cap it drops the excess but returns len(p),
// so that io.Copy / the child process does not fail on a short write.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	remaining := b.limit - len(b.buf)
	if remaining <= 0 {
		b.truncated = true
		return n, nil
	}
	if n > remaining {
		b.buf = append(b.buf, p[:remaining]...)
		b.truncated = true
		return n, nil
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *limitedBuffer) String() string {
	return string(b.buf)
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
}
