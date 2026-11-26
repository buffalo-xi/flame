package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/agentconfig"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/browserinfo"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/agentruns"
	llmmodels "flame/nexus/shared/repository/llmmodels"
	"flame/nexus/shared/repository/sessionrecords"
	"flame/nexus/shared/repository/sessions"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/runledger"
	"flame/nexus/shared/sandboxinfo"
	"flame/nexus/shared/versioning"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// The maximum loop rounds are no longer a constant: they come from platform.agent_configs (tenant / system levels plus a fallback,
// see shared/agentconfig) and are snapshotted into agentImpl.limits when a run starts. The code fallback is still 80, so behaviour is
// identical to before when the table has not been created.

// maxSkillPromptChars is the character cap for the skill list in the system prompt, keeping too many skills from blowing up the
// context.
const maxSkillPromptChars = 2000

// maxAgentPromptChars is the character cap for the "callable agents" list in the system prompt.
const maxAgentPromptChars = 1000

// agentImpl is the concrete core.Agent implementation (filesystem / builtin types).
type agentImpl struct {
	instance *models.Agent
	schema   string
	args     map[string]any

	mu      sync.Mutex
	eventCh chan core.Event // structured events (thinking / content / tool call)
	result  string
	errMsg  string

	// dropped counts events discarded when the event channel was full. Normally always 0; a non-zero value means the consumer cannot
	// keep up and the content may be missing characters (see pushEvent), reported once when the Stream ends.
	dropped atomic.Int64

	// stopped says whether this turn was terminated (user stop / session closed). It lets session records tell "stopped" from
	// "failed": their error text differs, but only status makes it obvious at a glance.
	stopped atomic.Bool

	// loop state
	toolCtx  *tools.Context
	toolDefs []gateway.ToolDef
	modelIn  gateway.ModelInput

	// toolNamesByRef maps the grant ref of every bound tool (builtin:exec) to the name the runtime dispatches to (exec).
	// A skill's command target is picked from its tool dependencies, so the stored value is a ref, while ExecuteTool and
	// AllowedTools speak in names; without this map a dispatch target could never match its own grant.
	// Filled by refreshToolDefs from the same list that fills AllowedTools.
	toolNamesByRef map[string]string

	// skillDiscovery says whether this agent may discover unbound skills (skill_discovery).
	// A production run takes the value frozen into the **current version** (versioning.AgentCurrentAttributes); draft mode takes the
	// current value in the agents table.
	// It only affects the scope of list_skills / read_skill / use_skill; the list injected into the system prompt is always
	// "private + bound" only.
	skillDiscovery bool

	// limits is the runtime parameter snapshot for this run (platform.agent_configs, see shared/agentconfig).
	// It is deliberately taken once at the start of the run rather than queried every round: the round cap, tool count and output cap
	// must stay stable for the whole run - a mid-run change would make "why did this run stop at round 40" unexplainable.
	limits agentconfig.Limits

	// languageChosen says the run's output language was actually decided (pinned by a caller or configured as this
	// agent's default), rather than falling back to the product default. Only a decided value is carried to the next
	// hop, so a workflow never reads the fallback as an intent.
	languageChosen bool

	// Session memory: this run's timing and persistence convention.
	// startedAt is stamped at the start of Stream and elapsedMs is frozen when the record is written - the value the entry point reads
	// from GetDurationMs() must be the same number as session_records.duration_ms (the seconds seen right after a run must match the
	// seconds in the history after a refresh).
	startedAt time.Time
	elapsedMs int64

	// runType is the call origin injected by the entry point (call / rpc / mcp / agent / flow), defaulting to call.
	runType string
}

// newAgentImpl creates and initialises an agentImpl.
func newAgentImpl(instance *models.Agent, schema string, arguments map[string]any) (*agentImpl, error) {
	if gateway.Current == nil {
		return nil, fmt.Errorf("llm gateway not initialized")
	}

	a := &agentImpl{
		instance: instance,
		schema:   schema,
		args:     arguments,
		eventCh:  make(chan core.Event, 256),
		runType:  stringArg(arguments, "_run_type", "call"),
	}

	// 1. Build the tool context
	rootDir := config.Cfg.Storage.RootDir
	userID, _ := arguments["user_id"].(string)
	sessionID, _ := arguments["session_id"].(string)
	runID, _ := arguments["run_id"].(string)
	if runID == "" {
		runID = uuid.NewString()
	}
	// session_id is concatenated into a storage path, so it must be a valid UUID before anything continues.
	//
	// There is deliberately **no silent fallback to runs/**: the entry layers (chat / MCP / RPC / flow) have all validated it, so an
	// invalid value here is an internal bug; quietly swapping the path would drop files that belong in the session into a directory
	// deleted when the run ends, and they could never be found in the session afterwards. Failing this call is preferable.
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		if _, perr := uuid.Parse(sessionID); perr != nil {
			return nil, fmt.Errorf("invalid session_id %q: a session id must be a UUID", sessionID)
		}
	}
	// Call depth: injected by the caller's call_agent (0 at the top level)
	callDepth, _ := arguments["_call_depth"].(int)
	// Call chain: injected by the entry point (RPC / MCP / flow / call_agent) and passed through unchanged on the way out.
	// Absent = empty chain (older callers), in which case only the depth cap remains as a guard.
	callChain := callchain.FromArguments(arguments)

	// Definition resolution mode: a production entry passes "current" -> read the is_current version;
	// an empty string = draft (editor debugging / trial chat / unit tests).
	//
	// With no current version it fails outright (ErrNoReleasedVersion) instead of falling back to the draft - otherwise "a production
	// run reads the released version" would not hold, and nobody could tell which definition actually ran when something goes wrong.
	// The only exception is the super agent: it always reads the draft, see resolveDefinitionMode.
	rawMode, _ := arguments[DefinitionModeKey].(string)
	mode := resolveDefinitionMode(rawMode, instance.IsSupervisor)

	agentVersion := 0
	if mode.IsCurrent() {
		v, _, verr := versioning.AgentCurrentVersion(schema, instance.Name)
		if verr != nil {
			return nil, verr
		}
		agentVersion = v

		// "Unrestricted tools", "allow discovering unbound skills" and the default reply language are frozen into the version
		// (see admin/agents/versions.go), and a production run must read the version's copy - reading the current
		// agents-table value would defeat the release freeze.
		// Copy the instance before changing it; never mutate the caller's object in place (the entry layer may reuse it).
		unrestricted, discovery, defaultLanguage, serr := versioning.AgentCurrentAttributes(schema, instance.Name)
		if serr != nil {
			return nil, serr
		}
		inst := *instance
		inst.ToolsUnrestricted = unrestricted
		inst.SkillDiscovery = discovery
		inst.DefaultLanguage = defaultLanguage
		instance = &inst
		a.instance = instance
	}

	// Whether sub-calls follow drafts: the supervisor is always true (nothing it builds is released yet, so it must be able to call it
	// immediately to verify); everything else looks at the switch when it is itself a draft (default on) and is always false when it is
	// itself released - drafts do not spread from a released chain.
	followDrafts := resolveFollowDrafts(mode, arguments[FollowDraftsKey], instance.IsSupervisor)

	// Definition root: a production run points at the version directory (the source of exec's /agent mount), draft mode at the regular
	// directory.
	agentRoot := paths.ResolveAgentDir(rootDir, schema, instance.Name)
	if agentVersion > 0 {
		agentRoot = paths.ResolveVersionDir(rootDir, schema, paths.VersionAgents, instance.Name, agentVersion)
		// The version row exists but the directory does not (deleted by hand / a failed migration): this must fail right here.
		// Otherwise docker creates the missing host path as an **empty directory** mounted at /agent, and together with "fall back to the
		// default prompt when AGENT.md cannot be read" it silently runs an identity-less empty agent.
		if st, serr := os.Stat(agentRoot); serr != nil || !st.IsDir() {
			return nil, fmt.Errorf("the directory of agent %q's current version (v%d) does not exist: %s (a version may have been released incompletely or the directory deleted; release it again)",
				instance.Name, agentVersion, agentRoot)
		}
	}

	// Skill visibility: whether discovering unbound skills is allowed (agents.skill_discovery).
	// The switch is an agent entity field, and instance already carries it from the agentColumns query, so no extra database read.
	a.skillDiscovery = instance.SkillDiscovery

	// Runtime parameter snapshot (tenant row -> system row -> fallback, with the system row as the hard ceiling).
	// When the table is missing or unconfigured everything falls back to the code fallback, keeping the old behaviour; a read failure
	// falls back too, so one config read failure never breaks a whole conversation.
	a.limits = agentconfig.Resolve(schema)

	// Working directory of the run domain (the default root of file tools = exec's cwd):
	//   with a session -> users/{uid}/sessions/{sid}/
	//   without one   -> users/{uid}/runs/{run_id}/ (temporary, swept as a fallback)
	//   without a user either -> tenant-level runs/{run_id}/ (extreme fallback)
	var workKey, workDir string
	switch {
	case sessionID != "" && userID != "":
		workKey = paths.UserSessionKey(schema, userID, sessionID)
		workDir = paths.ResolveUserSessionDir(rootDir, schema, userID, sessionID)
	case userID != "":
		workKey = paths.UserRunsKey(schema, userID, runID)
		workDir = paths.ResolveUserRunsDir(rootDir, schema, userID, runID)
	default:
		workKey = paths.RunsKey(schema, runID)
		workDir = paths.ResolveRunsDir(rootDir, schema, runID)
	}

	// Output language: caller-pinned (`_meta.language`) > the agent's own default > the product default. Resolved once here so the
	// prompt always has something to say; leaving it unsaid was the old behaviour and it left the model guessing between a
	// Chinese-first rule and an English-first prompt.
	outputLanguage, outputLanguagePinned := resolveOutputLanguage(arguments, instance)
	// Whether anybody actually chose this language, as opposed to it being the product fallback. Only a chosen value is
	// worth handing to the next hop - a workflow exposes it as builtin `language`, and call_agent passes it to the child as its
	// pinned language: telling either of them that a run is "en" because no one configured anything would read as a decision and
	// steer the flow's templates or the child's reply the wrong way.
	a.languageChosen = outputLanguagePinned || agentDefaultLanguage(instance) != ""

	a.toolCtx = &tools.Context{
		Schema:               schema,
		AgentName:            instance.Name,
		AgentID:              instance.ID,
		IsSupervisor:         instance.IsSupervisor,
		UserID:               userID,
		SessionID:            sessionID,
		RunID:                runID,
		WorkKey:              workKey,
		Workspace:            workDir,
		AgentRoot:            agentRoot,
		DefinitionMode:       mode,
		AgentVersion:         agentVersion,
		FollowDrafts:         followDrafts,
		RootDir:              rootDir,
		SkillRoot:            paths.ResolveSkillsDir(rootDir, schema),
		CustomTools:          newCustomToolSource(schema, mode),
		CustomToolsAdmin:     newCustomToolAdmin(schema),
		ToolsRoot:            paths.ResolveToolsDir(rootDir, schema),
		Storage:              agentStorage,
		Agents:               NewAgentManager(schema, instance.ID, instance.Name, userID, agentStorage, instance.IsSupervisor),
		Skills:               NewSkillManager(schema, instance.ID, instance.Name, userID, agentStorage, instance.SkillDiscovery, instance.IsSupervisor, mode),
		ToolGrants:           NewToolGrantManager(schema, instance.ID, instance.Name, userID, instance.IsSupervisor, mode),
		ToolCatalog:          newToolCatalogManager(schema, instance.Name, instance.IsSupervisor),
		Flows:                NewFlowsManager(schema, userID),
		Relations:            NewAgentRelationsManager(schema, instance.ID, instance.Name, userID, instance.IsSupervisor),
		Caller:               NewAgentCaller(schema, a.pushEvent, a.limits.MaxCallDepth),
		Externals:            &externalToolRunner{},
		Endpoints:            &localEndpointRunner{},
		CallDepth:            callDepth,
		CallChain:            callChain,
		OutputLanguage:       outputLanguage,
		OutputLanguagePinned: outputLanguagePinned,
		OutputLanguageChosen: a.languageChosen,
		Timeout:              60 * time.Second,
		MaxOutput:            a.limits.MaxOutputBytes,
		Sandbox:              tools.CurrentSandbox(),
	}

	// 2. Look up the default LLM model.
	//
	// "Not configured" is a configuration state, not a server fault: it is returned as a ConfigError so every entry
	// point (SSE chat / JSON-RPC / MCP / a flow node / call_agent) can report "configure a default model" in its own
	// protocol instead of an internal error carrying a raw SQL message.
	model, err := llmmodels.NewRepository().GetDefault(&auth.AuthInfo{Schema: schema})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoDefaultModel
		}
		return nil, fmt.Errorf("get default llm model: %w", err)
	}
	if model == nil {
		return nil, ErrNoDefaultModel
	}
	if string(model.GatewayID) == "" {
		return nil, ErrDefaultModelNotEnabled
	}
	maxTokens, _ := model.GetMaxTokens()
	temperature, _ := model.GetTemperature()
	topP, _ := model.GetTopP()
	a.modelIn = gateway.ModelInput{
		ID:            model.ID,
		Name:          model.Name,
		GatewayModel:  schema + ":" + string(model.GatewayModel),
		GatewayID:     string(model.GatewayID),
		ContextWindow: int(model.ContextWindow),
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		TopP:          topP,
	}

	// A "/name args" invocation picked by the user in the chat window: rewrite the message and pre-activate the skill before
	// the first tool resolution, so the tools it declares are already declared to the model (see applyCommand). A run without
	// the reserved argument is unaffected.
	a.applyCommand(context.Background())

	// 3. Bind tools (the tool grant system takes priority; when unconfigured only the built-in super agent is fully open)
	a.refreshToolDefs()
	// Platform capability snapshot: an orthogonal gate to tool grants (what the tenant may grant x what the platform allows).
	// Always non-nil - a missing key means not granted, and the execution layer refuses on that (see tools/registry.go).
	a.toolCtx.Caps = a.capabilities()

	return a, nil
}

// refreshToolDefs recomputes the tool schema and the execution gate (both must share one source, see the comment on AllowedTools).
//
// Two call sites: (1) newAgentImpl initialisation; (2) the loop, when "a new skill was read in this round".
// The latter implements "inject on demand": once the model reads a body through use_skill, the next LLM call already sees the tools
// that skill declares, with no bind_skill needed; when the run ends the Context disappears and the tools with it.
func (a *agentImpl) refreshToolDefs() {
	if a.instance == nil || a.toolCtx == nil {
		return
	}
	defs, namesByRef := a.buildToolDefs(a.instance)
	a.toolDefs = defs
	// The execution gate shares one source with "the tools declared to the model": non-nil (possibly empty) means any ungranted tool is
	// refused. This guards against text-form tool calls (the model writing a call into its content, for example with |DSML| markers)
	// bypassing tool grants.
	allowed := make(map[string]bool, len(defs))
	for _, d := range defs {
		allowed[d.Name] = true
	}
	a.toolCtx.AllowedTools = allowed
	a.toolNamesByRef = namesByRef
}

// buildToolDefs binds the tools available to this run:
//  1. when the agent has tool grants configured (agent_tool_grants) or "unrestricted tools" enabled, bind by the grant system's
//     resolution (required always injected + skills that are needed and granted + the candidates when unrestricted, minus denies);
//  2. when no grant item is configured: only the super agent owns **all built-in tools** by default, and other agents bind nothing.
//     Note: a new agent has its required grants materialised by agents.CreateAgent from models.DefaultAgentTools / models.MainCoreTools,
//     so the normal path never reaches this branch (only agents left unmigrated or with grants cleared by hand).
//  3. execution channels: "built-in tools / plugins (tools.PluginRunner) / mcp__ and rpc__ external connections (tools.ExternalRunner,
//     implemented in externaltool.go) / custom__ custom tools / local__ local endpoints (implemented in localendpoint.go)" all work -
//     all four channels are wired.
//
// The schema comes straight from the resolution's FinalTools (leaf ToolInfo, carrying its own Parameters): built-in tool names sit in
// the same list as external ones, so a plugin tool's schema cannot be dropped in a "second pass through tools.List()".
//
// The second return value maps each bound tool's grant ref (builtin:exec) to its runtime name (exec). It comes from the very same
// list that fills AllowedTools, so "what may run" and "what a ref means" can never disagree; see commandTargetName.
func (a *agentImpl) buildToolDefs(instance *models.Agent) ([]gateway.ToolDef, map[string]string) {
	infos, err := a.resolveGrantedTools(instance)
	if err != nil {
		// Resolution failed: only the built-in super agent may stay fully open; every other agent binds nothing (so an anomaly never
		// widens permissions by accident)
		xlog.Error("failed to resolve tool grants schema=%s agent=%s: %v", a.schema, instance.Name, err)
		infos = nil
	}
	if infos == nil {
		if instance.IsSupervisor {
			// Fuse: with zero grant rows the supervisor falls back to "all built-in tools".
			//
			// Normally the supervisor has models.MainCoreTools materialised by CreateAgent or sql/main_core_tool_grants.sql and never
			// reaches here. Reaching here means one of three things: an existing tenant missed the migration, the grant rows were cleared
			// by hand, or the grant table could not be read (already logged as an error above).
			// The response is to **warn but still grant everything**: the supervisor is the management entry point, and disabling it means
			// the whole tenant loses its create / grant ability, which is harder to recover from than a short-lived over-permission (the
			// supervisor is a fully privileged identity anyway).
			xlog.Error("supervisor %s has no tool grants configured, falling back to fully open (fuse); run sql/main_core_tool_grants.sql schema=%s",
				instance.Name, a.schema)
			return builtinToolDefs(a.capabilities())
		}
		xlog.Info("agent %s has no tool grants configured, binding no tool schema=%s", instance.Name, a.schema)
		return nil, nil
	}

	defs := make([]gateway.ToolDef, 0, len(infos))
	namesByRef := make(map[string]string, len(infos))
	for _, info := range infos {
		params := info.Parameters
		if params == nil {
			// When an external tool (mcp / rpc) stored no input_schema, supply an open object here:
			// built-in tools can be read from Meta() on the fly, while external tools have no fallback and some upstreams reject nil outright.
			params = emptyObjectSchema()
		}
		defs = append(defs, gateway.ToolDef{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  params,
		})
		if info.Ref != "" {
			namesByRef[info.Ref] = info.Name
		}
	}
	return defs, namesByRef
}

// emptyObjectSchema is the JSON Schema for "no parameters" (the shape must be valid; nil is not acceptable).
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// resolveGrantedTools resolves the tool list under the grant system (leaf ToolInfo, with its schema).
// It returns nil meaning "this agent has no grants configured" (no grant item and unrestricted off); the caller decides what to do:
// only the built-in super agent owns all built-in tools by default, and other agents bind nothing.
// A new agent has models.DefaultAgentTools / models.SupervisorCoreGrantRefs (required) materialised by agents.CreateAgent, so it never
// lands in the nil branch.
//
// The super agent's tool set has **exactly the same source as every other agent**: whatever the table says is what it gets. The old
// "the super agent always has all built-in tools and the grant table only reports what else it was given" is gone - it meant nobody
// could query the database to learn what the super agent actually holds.
func (a *agentImpl) resolveGrantedTools(instance *models.Agent) ([]toolgrant.ToolInfo, error) {
	grantsRepo := toolgrants.NewRepository()

	grants, err := grantsRepo.ListAgentGrants(a.schema, instance.ID)
	if err != nil {
		return nil, err
	}
	unrestricted := instance.ToolsUnrestricted // "unrestricted tools" belongs to the agent itself, so take the instance field directly
	// With no grants configured: the supervisor defaults to "all built-in tools + unrestricted", handled by buildToolDefs;
	// other agents also return nil, and buildToolDefs decides (no grants -> bind nothing).
	if len(grants) == 0 && (!unrestricted || instance.IsSupervisor) {
		return nil, nil
	}

	// The candidate source is the skills **activated in this run** (the model read their body through use_skill). Binding does not count.
	//
	// Binding only decides "whether this entry appears in the system prompt" (see skillCatalog); tools enter the schema only when the
	// model actually decides to use the skill - that is the single trigger for "capabilities on demand", and it disappears with the
	// Context when the run ends (history refills text only), leaving nothing across runs. Without activation no tools are given (the same
	// for an unbound shared skill even when it is discoverable). The cap is unaffected: candidates are always narrowed by the allow
	// list, and activation can only light up groups the administrator already granted.
	//
	// The order is the Resolver's truncation priority, and LoadedSkillEntries already returns "most recently activated first":
	// the skill the model just read keeps its tools when the budget forces a cut, instead of losing them to a skill that was
	// activated earlier (the old order was by skill name, which made the just-used skill the first casualty).
	// Dependency declarations come along with the activation registration (taken from the current version in a production run): the
	// candidates trust only this snapshot and never re-read skill_tool_grants - otherwise a draft dependency edit would leak into
	// production.
	visible := make([]toolgrant.VisibleSkill, 0)
	if a.toolCtx != nil {
		for _, e := range a.toolCtx.LoadedSkillEntries() {
			visible = append(visible, toolgrant.VisibleSkill{ID: e.ID, Name: e.Name, Deps: e.Deps})
		}
	}

	// The catalog's definition source follows this run: a production run only recognises custom tools and parameter schemas "released in
	// the current version", otherwise the model would call with draft parameters while the script comes from the version directory.
	mode := tools.DefinitionMode("")
	if a.toolCtx != nil {
		mode = a.toolCtx.DefinitionMode
	}
	catalog := toolgrant.NewCatalog(a.schema, grantsRepo, instance.IsSupervisor).
		WithCapabilities(a.capabilities()).
		WithDefinitionMode(mode)
	// The switch comes from the instance field (agents.tools_unrestricted): it belongs to the agent, so the Resolver no longer queries it
	resolver := toolgrant.NewResolver(catalog, a.schema, visible, instance.ToolsUnrestricted)
	// The tool cap comes from platform.agent_configs (fallback = toolgrant.DefaultMaxTools)
	resolver.MaxTools = a.limits.MaxTools
	res, err := resolver.Resolve(instance.ID)
	if err != nil {
		return nil, err
	}
	for _, w := range res.Warnings {
		xlog.Info("tool grant notice schema=%s agent=%s: %s", a.schema, instance.Name, w)
	}

	out := make([]toolgrant.ToolInfo, 0, len(res.FinalTools))
	for _, info := range res.FinalTools {
		// A tool name has to pass upstream validation: OpenAI-style APIs cap function names at 64 characters, and exceeding it may reject
		// the whole request (or the gateway silently drops the tool). The mcp__ / rpc__ prefix plus connection name plus tool name exceeds
		// it easily, so skipping it here with a locatable log beats handing the model an incomprehensible 400.
		if len(info.Name) > maxToolNameLen {
			xlog.Error("tool name too long (%d > %d), not bound for this run schema=%s agent=%s name=%s",
				len(info.Name), maxToolNameLen, a.schema, instance.Name, info.Name)
			continue
		}
		out = append(out, info)
	}

	return out, nil
}

// maxToolNameLen is the upstream cap on function names (64 for OpenAI-style APIs).
// An external tool's flattened name is "prefix + connection name + tool name", the class most likely to hit the cap, so it is caught at
// bind time (see resolveGrantedTools) rather than waiting for upstream to reject the whole request.
const maxToolNameLen = 64

// builtinToolDefs is the default tool set of the built-in super agent (the fallback when no grants are configured / resolution failed).
//
// caps is the platform capability view: the schema of an ungranted exec / browser tool is not handed to the model - the execution layer
// has its own hard gate, but handing it out first only lures the model into a call that is bound to fail.
//
// The ref map is filled here too: the fuse path binds by name, but a command target may still be configured as a ref
// (builtin:exec), so dispatch has to be able to resolve it in this branch as well.
func builtinToolDefs(caps map[string]bool) ([]gateway.ToolDef, map[string]string) {
	infos := toolgrant.BuiltinTools()
	defs := make([]gateway.ToolDef, 0, len(infos))
	namesByRef := make(map[string]string, len(infos))
	for _, info := range infos {
		t, ok := tools.Get(info.Name)
		if !ok {
			continue
		}
		m := t.Meta()
		if key := tools.CapabilityOf(m.Category); key != "" && !caps[key] {
			continue
		}
		defs = append(defs, gateway.ToolDef{
			Name:        m.Name,
			Description: m.Description,
			Parameters:  m.Parameters,
		})
		if info.Ref != "" {
			namesByRef[info.Ref] = m.Name
		}
	}
	return defs, namesByRef
}

// Stream runs the agent loop: LLM inference -> tool calls -> result feedback -> inference again, until there are no tool calls.
func (a *agentImpl) Stream(parent context.Context) error {
	// The timing start and the persistence tail both live here: success, failure and termination all leave a record for the turn (on
	// failure output is empty). Before the rewrite only web chat wrote one and endpoint calls did not - the same session reached through
	// two entry points had one broken and one working memory, which is exactly the inconsistency being removed.
	a.startedAt = time.Now()
	// Two independent tails: memory (session_records, written only for session-bound runs) and the audit log
	// (agent_run_records, written for every run including stateless MCP / RPC calls). Neither may fail a run
	// that has already finished, so both only log their own errors.
	// Order: LIFO puts recordSessionRound first, so the memory row (which freezes elapsedMs) lands before the log row.
	defer a.recordRun()
	defer a.recordSessionRound()
	// The dropped count is normally always 0. A non-zero value means the consumer cannot keep up and the content will be missing
	// characters - it must leave a trace to look into, rather than being dropped silently as before and leaving no way to explain "why the
	// output was cut off".
	defer func() {
		if n := a.dropped.Load(); n > 0 {
			a.LogError(fmt.Sprintf("event channel full: %d events dropped during this run (content may be incomplete)", n))
		}
	}()

	// A cancellable ctx: both the LLM stream and tool execution must be interruptible. The stop entries are in runcontrol.go (StopRun /
	// StopRunCause / StopSession); this used to be Background(), so closing the page or forcing a stop only meant waiting for the model to
	// finish on its own.
	//
	// parent is the parent context: a nested run (call_agent) is cancelled with its parent - when the parent is stopped the child exits
	// immediately, instead of the old "parent stopped, child still burning tokens and sandbox" spin.
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	// Carry the run's output language on ctx (the same mechanism as callchain.With). Everything this run calls reads it from
	// there - workflows (builtin `language`), sub-agents (call_agent) and remote endpoints (RPC / MCP tools, as
	// params._meta.language): a decision made once holds for the whole chain instead of every hop re-guessing. Only the bare
	// product fallback stays behind - sending "en" merely because nobody configured anything would override the peer's own
	// default.
	if c := a.toolCtx; c != nil && c.OutputLanguage != "" && a.languageChosen {
		ctx = core.WithPinnedLanguage(ctx, c.OutputLanguage)
	}
	if c := a.toolCtx; c != nil {
		defer registerRun(runledger.Entry{
			Kind:       runledger.KindAgent,
			RunID:      c.RunID,
			SessionID:  c.SessionID,
			Schema:     c.Schema,
			TargetID:   c.AgentID,
			TargetName: c.AgentName,
			UserID:     c.UserID,
			RunType:    a.runType,
			StartedAt:  a.startedAt,
		}, cancel)()
	}
	// A "/name args" command whose skill declares command_dispatch=tool never reaches the model: one sentence in, one tool call
	// out. Returning here still records the turn (recordSessionRound is deferred above) and still gives the entry point a
	// normal event sequence.
	if handled, derr := a.dispatchCommand(ctx); handled {
		return derr
	}

	// On termination, clean up the resident processes this run started (browser / shell).
	// It is registered after cancel: LIFO runs it before cancel(nil), so context.Cause still returns the real reason (user stop / client
	// disconnect).
	defer func() {
		if context.Cause(ctx) != nil {
			a.killInteractiveOnStop()
		}
	}()

	systemPrompt, err := a.loadSystemPrompt()
	if err != nil {
		a.SetError(err.Error())
		return err
	}

	messages := []gateway.Message{
		{Role: "system", Content: systemPrompt},
	}
	// Context sources (highest priority first):
	//   1. messages passed explicitly by the caller (external injection / the old convention);
	//   2. with a session_id: the history of the same owner read from session_records - the same memory as web chat;
	//   3. no session: a single turn with only the current input.
	if history, ok := a.args["messages"].([]gateway.Message); ok && len(history) > 0 {
		messages = append(messages, history...)
	} else {
		messages = append(messages, a.loadSessionHistory()...)
		messages = append(messages, gateway.Message{Role: "user", Content: a.userContent()})
	}

	// The round cap comes from platform.agent_configs (code fallback 80). Reaching it ends the run - it is the only guard against an
	// endless loop, which is why the configuration side never accepts -1 (see nexus/platform/sql/agent_configs.sql).
	for i := 0; i < a.limits.MaxIterations; i++ {
		// A new skill was activated this round -> recompute the tool set: after use_skill, the next inference sees the tools it declares.
		// The database is only read when something changed (ConsumeSkillsDirty resets the flag), so ordinary rounds cost nothing extra.
		if a.toolCtx.ConsumeSkillsDirty() {
			a.refreshToolDefs()
		}
		content, toolCalls, err := a.streamOnce(ctx, messages)
		if err != nil {
			// A deliberate stop is not an LLM failure: report the CancelCause reason in plain words so SSE / the caller sees "stopped"
			// rather than a string of context canceled.
			if cause := context.Cause(ctx); cause != nil {
				msg := cancelMessage(cause)
				a.stopped.Store(true)
				a.SetError(msg)
				return errors.New(msg)
			}
			a.SetError(fmt.Sprintf("llm generate: %v", err))
			return err
		}
		// Round fingerprint: for investigating "a round returned empty / did not continue" - an empty body plus no tool calls means the loop
		// ends here.
		a.LogInfo(fmt.Sprintf("round %d: content=%dB tools=%d head=%q",
			i, len(content), len(toolCalls), truncate(content, 120)))

		// No tool calls: this is the final answer
		if len(toolCalls) == 0 {
			a.SetResult(content)
			return nil
		}

		// How results are fed back depends on the call origin:
		//   - structured (upstream returned tool_calls): feed back assistant.tool_calls + role:"tool" per the OpenAI protocol;
		//   - text form (FromText: the model writes the call into content markers, such as DeepSeek's |DSML|):
		//     an upstream that returns no structured calls most likely does not accept this feedback either, so use plain text, otherwise
		//     the second round is rejected or comes back empty (which looks like "the agent stops as soon as the tool finishes").
		textMode := false
		for _, tc := range toolCalls {
			if tc.FromText {
				textMode = true
				break
			}
		}

		if textMode {
			var b strings.Builder
			b.WriteString(content)
			for _, tc := range toolCalls {
				argJSON, _ := json.Marshal(tc.Args)
				fmt.Fprintf(&b, "\nCalling tool %s with arguments %s", tc.Name, argJSON)
			}
			messages = append(messages, gateway.Message{Role: "assistant", Content: b.String()})
		} else {
			messages = append(messages, gateway.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toOpenAIToolCalls(toolCalls),
			})
		}

		for _, tc := range toolCalls {
			// Already stopped: do not start the next tool - each tool may be a sandbox call.
			if context.Cause(ctx) != nil {
				break
			}
			argJSON, _ := json.Marshal(tc.Args)
			a.pushEvent(core.Event{Type: core.EventToolCall, CallID: tc.ID, Tool: tc.Name, Args: string(argJSON)})

			started := time.Now()
			result, execErr := tools.ExecuteTool(ctx, a.toolCtx, tc.Name, tc.Args)
			toolOut := marshalToolResult(result, execErr)

			a.pushEvent(core.Event{
				Type: core.EventToolResult, CallID: tc.ID, Tool: tc.Name,
				Result: truncate(toolOut, 2000), OK: execErr == nil,
				DurationMS: time.Since(started).Milliseconds(),
			})

			if textMode {
				messages = append(messages, gateway.Message{
					Role:    "user",
					Content: fmt.Sprintf("Result of tool %s:\n%s", tc.Name, toolOut),
				})
			} else {
				messages = append(messages, gateway.Message{
					Role:       "tool",
					Content:    toolOut,
					Name:       tc.Name,
					ToolCallID: tc.ID,
				})
			}
			a.LogInfo(fmt.Sprintf("tool %s result: %s", tc.Name, truncate(toolOut, 200)))
		}
		// The tools finished but the run was stopped meanwhile: do not enter the next inference round.
		if cause := context.Cause(ctx); cause != nil {
			msg := cancelMessage(cause)
			a.stopped.Store(true)
			a.SetError(msg)
			return errors.New(msg)
		}
	}

	a.SetError("agent loop exceeded max iterations")
	return fmt.Errorf("agent loop exceeded max iterations")
}

// streamOnce runs one LLM round in streaming mode: content / thinking events are pushed as they arrive, and tool calls are returned when
// the round ends.
// It returns the accumulated content and the tool call list (no tool calls means the final answer).
func (a *agentImpl) streamOnce(ctx context.Context, messages []gateway.Message) (string, []gateway.ToolCall, error) {
	ch, err := gateway.Current.Stream(ctx, a.modelIn, messages, a.toolDefs)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	var toolCalls []gateway.ToolCall
	for chunk := range ch {
		if chunk.Error != nil {
			return sb.String(), nil, chunk.Error
		}
		if chunk.ReasoningContent != "" { // thinking (reasoning content)
			a.pushEvent(core.Event{Type: core.EventThinking, Delta: chunk.ReasoningContent})
		}
		if chunk.Content != "" { // content delta
			sb.WriteString(chunk.Content)
			a.pushEvent(core.Event{Type: core.EventText, Delta: chunk.Content})
		}
		if len(chunk.ToolCalls) > 0 { // the complete tool call list delivered with finish_reason
			toolCalls = chunk.ToolCalls
		}
		if chunk.Done {
			break
		}
	}
	return sb.String(), toolCalls, nil
}

// loadSystemPrompt reads AGENT.md as the system prompt (falling back to the default prompt when missing) and appends the skill list
// visible to this agent at the end.
// It uses progressive disclosure: the list gives only name + ownership + description, and the body is read on demand with read_skill /
// use_skill.
func (a *agentImpl) loadSystemPrompt() (string, error) {
	prompt := a.defaultSystemPrompt()
	if a.toolCtx != nil && a.toolCtx.Storage != nil {
		key := a.toolCtx.AgentPath(paths.AgentEntryFile)
		data, err := a.toolCtx.Storage.ReadFile(context.Background(), key)
		switch {
		case err == nil:
			// AGENT.md may reference global variables (${global_vars.X}): the asset package keeps only the reference name, and the target
			// tenant's variable values fill it in after install. A failed expansion must fail hard - leaving the literal in place, the model
			// would read ${global_vars.DB_HOST} as ordinary prose.
			expanded, xerr := globalvars.Expand(a.toolCtx.Schema, string(data))
			if xerr != nil {
				return "", fmt.Errorf("failed to expand the global variables in %s: %w", paths.AgentEntryFile, xerr)
			}
			prompt = expanded
		case a.toolCtx.VersionBound():
			// A production run is bound to a released version: failing to read AGENT.md here means the snapshot is incomplete.
			// It must not silently fall back to the default prompt - that would swap the live agent's identity with nobody the wiser.
			// Draft mode keeps the old behaviour: a new agent that has not written AGENT.md yet is a normal state.
			return "", fmt.Errorf("failed to read %s of the current version (%s): %w", paths.AgentEntryFile, key, err)
		}
	}
	if catalog := a.skillCatalog(); catalog != "" {
		prompt += "\n\n" + catalog
	}
	if catalog := a.callableCatalog(); catalog != "" {
		prompt += "\n\n" + catalog
	}
	// Capability boundaries and environment facts come last: after AGENT.md, so a wrong claim the user wrote there can be corrected.
	if notes := a.capabilityNotes(); notes != "" {
		prompt += "\n\n" + notes
	}
	if notes := a.sandboxNotes(); notes != "" {
		prompt += "\n\n" + notes
	}
	// The browser is its own service, so its facts are stated on their own (an agent with no sandbox access can still
	// have the browser tool).
	if notes := a.browserNotes(); notes != "" {
		prompt += "\n\n" + notes
	}
	// The reply-language rule comes **last**: it constrains everything above it (AGENT.md included), and sitting after the capability /
	// sandbox notes makes the order the model reads "facts -> convention".
	prompt += "\n\n" + responseLanguageRule
	// A language the caller pinned for this call refines that rule and never replaces it. The base rule infers the language from the
	// input, which works for chat and fails for a programmatic call whose arguments are pure JSON: there the model has nothing but
	// the prompt's language to go on.
	if hint := outputLanguageHint(a.toolCtx); hint != "" {
		prompt += "\n\n" + hint
	}
	return prompt, nil
}

// outputLanguageHint renders the language the caller pinned for this run (AgentGrantView-style: English, because it is
// model-facing product text, not copy for a user).
//
// The precedence sentence is part of the hint on purpose: an agent whose AGENT.md pins a language (a writing tutor for one
// language, say) must not be overridden by a caller that sends a different tag. Two rules contradicting each other in one prompt
// is worse than sending no hint at all.
// defaultOutputLanguage is the last resort when neither the caller nor the agent says anything.
//
// English, because the product's primary language is English: AGENT.md, the tool descriptions and the system-entity files are
// written in it, so "answer in English" is the only default that follows from the prompt itself. A Chinese-first tenant sets
// agents.default_language (or keeps pinning per call); what this removes is the silent guess between the two.
const defaultOutputLanguage = "en"

func outputLanguageHint(c *tools.Context) string {
	if c == nil {
		return ""
	}
	tag, label := core.NormalizeOutputLanguage(c.OutputLanguage)
	if tag == "" {
		// Never echo an unvalidated value into the prompt: the field is filled from validated input today, but a future caller
		// setting it directly must not be able to inject free text this way.
		return ""
	}
	if c.OutputLanguagePinned {
		return fmt.Sprintf("The caller pinned the output language of this call: %s (%s). Answer in that language whatever language "+
			"the input and this prompt are written in, and use it for the whole output - the notes around tool calls, the progress "+
			"updates and the conclusions included, never mixing two languages. If AGENT.md pins a language explicitly, "+
			"AGENT.md still wins.", label, tag)
	}
	return fmt.Sprintf("No output language was specified for this call: reply in the language the input is written in, use the same "+
		"language for the rest of the output (notes around tool calls, progress, conclusions) instead of mixing two, and when the "+
		"input carries no language signal (pure JSON, ids, numbers), answer in %s (%s). If AGENT.md pins a language explicitly, "+
		"AGENT.md still wins.", label, tag)
}

// resolveOutputLanguage resolves the reply language of one run:
//
//	caller-pinned `_meta.language`  >  agents.default_language  >  defaultOutputLanguage
//
// The middle step exists because an agent may serve exactly one audience; it is a definition column, so a release freezes
// it into the version row, and it is validated like any other tag - a bad value falls through instead of being echoed.
func resolveOutputLanguage(arguments map[string]any, inst *models.Agent) (tag string, pinned bool) {
	if t, _ := core.OutputLanguage(arguments); t != "" {
		return t, true
	}
	if t := agentDefaultLanguage(inst); t != "" {
		return t, false
	}
	return defaultOutputLanguage, false
}

// agentDefaultLanguage reads the agent's default reply language (agents.default_language; empty = not configured, or not a
// valid tag).
//
// It reads the definition column rather than an agents.config key: the language has to be frozen into the version row at
// release time, and a production run reads that copy (see AgentCurrentAttributes).
func agentDefaultLanguage(inst *models.Agent) string {
	if inst == nil {
		return ""
	}
	tag, _ := core.NormalizeOutputLanguage(inst.DefaultLanguage)
	return tag
}

// responseLanguageRule is the single hard convention for reply language.
//
// Why state it explicitly: tool descriptions, AGENT.md, skill bodies and the capability / sandbox notes in this file may be either
// language (they are translated in batches as the product's primary language changes, coexisting for a long time). Models readily
// mistake **the prompt's language** for **the reply language** - a Chinese question under an English prompt then gets an English answer,
// the one regression a user notices immediately when the primary language switches to English.
//
// So this rule decouples the two: the prompt's language != the reply language, and the reply follows the user.
// It covers the **whole** output, not just the final answer: a model that answers in Chinese but narrates its tool calls in English reads
// as two different speakers, and the mixed text is what a user notices first.
// It is written in English only, like every other model-facing string (tool descriptions, labels, argument schemas): the rule constrains
// language, so it has to be one unambiguous instruction - and a bilingual copy would invite the model to mirror the mix it describes.
// An explicit setting in AGENT.md wins - some agents only ever serve one language (a writing tutor for one language, say), which is
// deliberate and must not be overridden by this general rule.
//
// The value below is model-facing copy: English, like the rest of the prompt's own text.
const responseLanguageRule = "Reply in the language the user writes in: Chinese input, Chinese reply; English input, English reply. " +
	"Keep the whole output in that one language - the body, the notes before and after tool calls, progress updates, " +
	"conclusions and error explanations. Never mix two languages; keep identifiers such as code, tool names and file paths " +
	"verbatim. The language of this prompt, of tool descriptions and of skill docs is NOT the reply language. " +
	"Follow AGENT.md only if it explicitly pins one language. When you call another agent (call_agent) or an external tool " +
	"(mcp__ / rpc__), write the natural-language parts of the request in that same language and report the result back in it - " +
	"a chain must not switch language halfway."

// sandboxNotes builds the "sandbox environment" note: whether the sandbox can reach the network (sandboxd's **declaration**) plus what
// is inside it (the raw probe output).
//
// Kept **separate** from capabilityNotes: that one covers "which permissions you have" (judged by AllowedTools), this one covers "what
// the environment looks like" (judged by sandboxd's capability snapshot). Same position (after AGENT.md), and it corrects environment
// claims written wrongly there.
//
// All three states are stated as they are: off / on come from the declaration, and an unknown state (nothing fetched / probe failed) is
// said to be unknown - above all, "not fetched" must never be phrased as "no network", which would make the model write a pile of
// offline fallback code for nothing.
//
// Only for agents that can touch the sandbox (see usesSandbox): an agent with no route into the sandbox gains nothing from these facts,
// and would grow claims like "I am inside the sandbox and cannot install packages".
func (a *agentImpl) sandboxNotes() string {
	// Never injected: better to stay silent than to invent an environment for the model.
	if !sandboxinfo.Configured() {
		return ""
	}
	if !a.usesSandbox() {
		return ""
	}
	info := sandboxinfo.Get()

	// No successful fetch ever: report unknown only, never guess (on a failed fetch Info keeps the last successful snapshot,
	// so an empty Mode means there has never been a successful record).
	if info.Mode == "" {
		return "Sandbox environment: unknown. No capability snapshot has been fetched from sandboxd yet (it may be down); " +
			"do **not** assume whether the sandbox has network access or which modules are installed - run a command to check when you need to know."
	}

	lines := []string{fmt.Sprintf("Sandbox environment (isolation mode %s; the following is sandboxd's declaration plus what it measured at startup, and may differ from other environments):", info.Mode)}
	// "Configured" and "measured" stay apart: langs is what sandboxd declares it can run, the block below is whether the interpreter is
	// really there. A mismatch is itself useful information (configured but never measured = the configuration does not match reality);
	// merged into one sentence they read like two sets of facts.
	if len(info.Langs) > 0 {
		// The scope has to be nailed down on this line: these are the languages of the exec / single-channel path (command execution,
		// skill scripts, custom tools), unrelated to the selectable languages of workflow nodes. Merely saying "executable languages"
		// makes the model treat js as an option for code nodes, and that mistake only surfaces at runtime as unsupported_lang (next line).
		lines = append(lines, "- Executable languages (configured in sandboxd; used for command execution and skill scripts, **not** the selectable languages of workflow nodes): "+strings.Join(info.Langs, ", "))
	}
	// Workflow script languages are **not the same thing** as the line above and must be stated separately: a language in the top-level
	// langs list may have no duplex pool (a language that only runs skill scripts needs none), and a workflow written against it is
	// rejected by the sandbox (unsupported_lang) - which again only surfaces at runtime.
	if langs, ok := sandboxinfo.CodeLangs(); ok {
		if len(langs) == 0 {
			lines = append(lines, "- Sandbox has no **workflow script language** configured (sandboxd's code.pools is empty): do not use script nodes when creating or editing workflows.")
		} else {
			lines = append(lines, "- Workflow script languages (configured in sandboxd): "+strings.Join(langs, ", ")+
				". When creating or editing a workflow, script nodes (code, branch condition, loop condition, handler) may only use one of these languages; any other language is rejected by the sandbox (unsupported_lang).")
		}
	}

	// Probe result: nexus does not parse it, it passes it through verbatim (whoever produced the fact states it - see probe.go in sandboxd).
	if errText := strings.TrimSpace(info.Environment.Error); errText != "" {
		// On a failed probe, state "unknown, and why"; do not let the model read an empty list as "nothing is installed".
		lines = append(lines, "- Sandbox probe failed ("+errText+"), so there is no trustworthy module list below: confirm a library yourself before using it, and do not assume it is there.")
	} else if text := strings.TrimSpace(info.Environment.Text); text != "" {
		lines = append(lines, "- Sandbox probe result (verbatim output from sandboxd):", text)
	}

	// The network conclusion is phrased as the **effective value**: the deployment ceiling (on-request) must not be handed to the model
	// verbatim - it cannot decide from that whether it should write networked code. Tenant permission (net.egress) is layered in here to
	// compute "whether you can reach the network right now".
	switch info.Network.Exec {
	case sandboxinfo.NetworkOff:
		lines = append(lines, "- The sandbox **cannot** reach the network (network=off): pip / npm / luarocks cannot install anything at runtime - "+
			"you can only use the modules listed above (the Python standard library is complete; not being on that list does not mean unavailable).")
	case sandboxinfo.NetworkOn:
		lines = append(lines, "- The sandbox **can** reach the network (network=on), but the rootfs is read-only: global installs (`pip install` into system directories, `npm i -g`) fail; "+
			"use `--user` or a project-local virtual environment instead.")
	case sandboxinfo.NetworkOnRequest:
		// The deployment allows on-demand egress: whether it really works depends on this tenant having net.egress (fail-closed).
		// Give the **conclusion** here; do not hand on-request out verbatim - the model cannot decide from it whether to go online.
		if a.capabilities()[capability.Egress] {
			lines = append(lines, "- The sandbox **can** reach the network (network=on-request): this tenant has egress enabled, and an `exec` call must carry `egress: true` "+
				"to actually get it; without it the call runs offline. The read-only rootfs applies here too (`pip install` into system directories fails; use `--user`).")
		} else {
			lines = append(lines, "- The sandbox **cannot** reach the network (network=on-request, but this tenant does not have net.egress): "+
				"passing egress to `exec` is rejected as well. Ask an administrator to enable it, or use the http node of a workflow.")
		}
	default:
		lines = append(lines, "- Whether the sandbox can reach the network is unknown (sandboxd gave no declaration): confirm with the user before writing networked code; do not assume.")
	}

	// The snapshot may be stale: say so, so the model knows whether to still trust this environment description.
	if info.Err != "" {
		stale := "the above is the last successfully fetched snapshot"
		if !info.FetchedAt.IsZero() {
			stale = fmt.Sprintf("the above is a snapshot from %s", info.FetchedAt.Format(time.RFC3339))
		}
		lines = append(lines, fmt.Sprintf("- Note: the latest refresh failed (%s); %s and may be out of date.", info.Err, stale))
	}

	return strings.Join(lines, "\n")
}

// usesSandbox reports whether this agent has any route into the sandbox - the criterion is **tool grants** (AllowedTools), the same
// source as capabilityNotes. A config switch is deliberately not introduced: a switch is a second source of truth maintained by hand,
// and "exec granted but the switch forgotten" would leave the agent with no environment facts, so it starts guessing.
//
// It only asks "allowed to touch", not "will touch": an agent with the capability should know what the environment looks like.
//
// ⚠️ When it cannot be decided (no tool context) the notes are **still injected**: environment facts are unlike permission statements -
// giving more is safer than giving less, and "skip" is only used when it is certain the agent has no sandbox entry at all.
//
// ⚠️ If a "create workflow" tool appears later (its code snippets go through the /v1/code channel), its name must be added here - only
// then does the "workflow script languages" line bind it (today no agent tool can create a flow).
// capabilities is the snapshot of the sandbox capabilities the platform grants this tenant (keys in shared/capability).
//
// A read failure is treated as **nothing granted** (fail-closed): capabilities are a cost guardrail, and relaxing them to "everyone may
// execute" on one database hiccup is far more dangerous than refusing a single call (the same choice as the plugin Runner). The catalog
// side likewise shows them as not granted (see toolgrant.Catalog.capabilityBlocked) - both directions agree.
// capabilitySnapshot is the platform capability snapshot. It is a package-level variable only so unit tests can replace it - the
// on-request branch of sandboxNotes needs both conclusions ("net.egress granted / not granted"), while a real snapshot needs the database.
// The same approach as capabilitySnapshot in flow/executors.
var capabilitySnapshot = capabilitypolicy.Snapshot

func (a *agentImpl) capabilities() map[string]bool {
	// Single implementation in capabilitypolicy.Snapshot: the catalog side and the execution side must point the same way.
	return capabilitySnapshot(a.schema)
}

func (a *agentImpl) usesSandbox() bool {
	c := a.toolCtx
	if c == nil || c.AllowedTools == nil {
		return true
	}
	// "browser" is deliberately absent: the browser is served by its own pool (see browserNotes), so granting it says
	// nothing about whether this agent can touch a sandbox.
	return c.AllowedTools["exec"] || c.AllowedTools["run_skill_script"] || c.AllowedTools["shell"]
}

// browserNotes builds the "browser" note: whether the browser pool can run a browser, and how to use it.
//
// Separate from sandboxNotes because the browser is no longer part of the sandbox: the facts come from browserd, and an
// agent with no sandbox access can still have the browser tool.
//
// Available or not, both are facts, stated separately: when unavailable the **reason** must be stated, otherwise the
// model misreads "the tool is off" as "this site cannot be opened" and then spins on swapping URLs / selectors.
func (a *agentImpl) browserNotes() string {
	// Tool grants are the criterion, the same source as capabilityNotes: nil means undecidable, and environment facts are
	// safer to over-supply than to omit.
	if c := a.toolCtx; c != nil && c.AllowedTools != nil && !c.AllowedTools["browser"] {
		return ""
	}
	if !tools.BrowserConfigured() {
		return "- Browser: **unavailable** (this deployment has no browser pool configured). This is a tool capability problem, not a problem with the target site."
	}
	if !browserinfo.Configured() {
		return "- Browser: state unknown (the browser pool has not answered yet). Do not conclude that a page cannot be opened - " +
			"try the tool, or fetch the content with exec."
	}

	b := browserinfo.Get()
	if !b.Available {
		reason := strings.TrimSpace(b.Error)
		if reason == "" {
			reason = "not available"
		}
		return "- Browser: **unavailable** (" + reason + "). This is a tool capability problem, not a problem with the target site."
	}

	mode := "headless only (this deployment has no display environment)"
	switch {
	case b.HeadedAvailable && b.HeadlessDefault:
		mode = "headless by default; pass headed=true to open when a human needs to see the window (allowed in this deployment)"
	case b.HeadedAvailable:
		mode = "headed by default (visible window)"
	}
	ver := strings.TrimSpace(b.Version)
	if ver == "" {
		ver = b.Engine
	}
	return "- Browser: available (" + ver + "; " + mode + "). " +
		"Use snapshot to read a page (accessibility tree + refs) and refs to click/type; screenshots and downloads are written into your workspace - you cannot read images yourself."
}

// capabilityNotes builds the "capability boundaries" note from the tool set actually granted to this run; it returns "" when no reminder
// is needed.
//
// The criterion is AllowedTools (the grant set), not is_supervisor - an ordinary agent may also be granted create_own_skill.
// But it may only be used to tell "which capability is missing", **never** to produce "use this tool instead" instructions:
// create_agent / create_shared_skill are super-only (super agent), so telling a non-super agent "please use create_agent" is useless.
//
// Why it exists: the shape gate of the file tools (entityLikePath in tools/gate.go) only catches things at call time, and cannot stop
// directory creation under another name; this block is the "before the fact" line, so the model does not even try.
func (a *agentImpl) capabilityNotes() string {
	c := a.toolCtx
	if c == nil || c.AllowedTools == nil {
		return ""
	}
	has := func(n string) bool { return c.AllowedTools[n] }

	// The owner gate: the same criterion as tools/gate.go and skillManager.checkOwnerAgent - only the "normal" state may write the private
	// skills owned by it; the deprecated state is frozen wholesale.
	skillWritable := models.IsModifiable(int(a.instance.Status))

	var lines []string

	switch {
	case has("create_own_skill") && has("create_shared_skill") && skillWritable:
		// Both kinds can be created, no reminder needed
	case has("create_own_skill") && !skillWritable:
		// The tool is there but the owner gate is closed: state the reason, so the model does not keep retrying (matching the gate)
		lines = append(lines, "- You **currently cannot** modify **your own private** skills: this agent is deprecated. "+
			"If the user needs a change, ask an administrator to switch the agent back to the normal state.")
	case has("create_own_skill"):
		lines = append(lines, "- You may create/modify **your own private** skills (create_own_skill); you do **not** have the shared-skill permission, so say so plainly when the user asks for a shared skill.")
	default:
		lines = append(lines, "- You do **not** have permission to create or modify skills. **Do not use file tools to create SKILL.md or a skills/ directory inside the session workspace** - it will not be registered and has no effect; if the user needs a new skill, ask an administrator to enable `create_own_skill` for this agent.")
	}

	if !has("create_agent") {
		if a.instance.IsSupervisor {
			// The supervisor's write gate lets it edit files inside any **normal-state** agent's definition domain (AGENT.md included,
			// see D6 in tools/gate.go: the boundary follows "entity existence", not file type); here only "create / register" is reminded
			// to go through the meta tools - do not phrase it as "AGENT.md cannot be edited", which would contradict the gate.
			lines = append(lines, "- Creating an agent or renaming one goes through `create_agent` / `update_agent`; do not conjure an `agents/` directory with file tools (it will not be registered). Editing the definition-domain files of an existing agent (AGENT.md included) is still fine with file tools - use paths with a `/` prefix.")
		} else {
			lines = append(lines, "- You **cannot** create or modify agent definitions (AGENT.md), and you must **not** create an agents/ directory with file tools (it has no effect). Ask an administrator or the super agent when an agent needs to be created or changed.")
		}
	}

	// Cost reminder for resident tools: shell / browser are session-level resident processes (kept across conversations while a session
	// lives), not a free way to explore. Only told to agents granted them (same criterion as AllowedTools), to avoid noise for others.
	// Phrased as "criterion + closing action" rather than "use with care": the latter makes the model too timid to use them when it
	// should, and the task fails outright - the point of the reminder is to try one-shot means first, not to avoid resident tools.
	if has("shell") || has("browser") {
		lines = append(lines, "- Resident tools cost something: `shell` / `browser` start a **resident process** in the sandbox "+
			"(kept across conversations while a session lives, holding memory and a process). Prefer one-shot means first (`exec` / `run_skill_script` / reading files); "+
			"open them only when you need **state across calls** (cwd / environment variables / background jobs / a logged-in session / multi-step page interaction), "+
			"and close them as soon as you are done: pass `kill: true` to `shell`, `action: close` to `browser`.")
	}

	if len(lines) == 0 {
		return ""
	}
	return "Capability boundaries (generated from your current grants; never promise beyond them):\n" + strings.Join(lines, "\n")
}

// skillCatalog builds the skill list text injected into this agent (name + ownership + description).
// Returns "" when there is no skill - but when "discover unbound skills" is on, a list_skills hint is still given (such an agent has no
// list to inject, so that hint is its only discovery entry point).
// The scope is fixed as "private + bound (toolgrant/repo.ListInjected)", independent of the skill_discovery switch: only skills bound to
// this agent stay in the prompt; unbound shared skills are not injected automatically.
// Bodies are not part of the prompt (each line is name / ownership / description only, capped at maxSkillPromptChars), and the model reads
// them on demand via read_skill / use_skill.
// A read failure does not block agent start, it only returns "".
func (a *agentImpl) skillCatalog() string {
	if a.toolCtx == nil || a.toolCtx.Skills == nil {
		return ""
	}
	list, err := a.toolCtx.Skills.ListInjected(context.Background())
	if err != nil {
		return ""
	}
	// An empty list must not return "" outright: an agent with "discover unbound skills" on is exactly the one that needs this hint most -
	// it has no skill to inject and can only find them through list_skills (which is what the switch is for).
	if len(list) == 0 {
		if a.skillDiscovery {
			return "No skill is bound to you yet. To find others, use list_skills to see every visible skill; to work by a skill use use_skill (use read_skill to only read its body)."
		}
		return ""
	}

	var b strings.Builder
	b.WriteString("Skills bound to this agent (names and descriptions only; bodies and tools are fetched on demand):\n" +
		"  - To work by a skill: use use_skill <name> first - it returns the body and makes the tools that skill declares available from the next round on;\n" +
		"  - To only read it: use read_skill <name> - no tool is activated.\n")
	truncated := false
	for _, s := range list {
		scope := "private"
		if s.Scope == tools.SkillScopeTenant {
			scope = "shared"
		}
		// Note: deprecated skills never appear here - ListInjected already filters status=3.
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "(no description)"
		}
		line := fmt.Sprintf("- %s [%s]: %s\n", s.Name, scope, desc)
		if b.Len()+len(line) > maxSkillPromptChars {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("...(more bound skills omitted; use list_skills to see them all)")
	}
	// With discovery on, tell the model plainly that "there are other skills to look up", otherwise it will not call list_skills.
	// The truncation branch already mentioned list_skills once, so do not repeat it.
	if a.skillDiscovery && !truncated {
		b.WriteString("\nTo find other skills, use list_skills to see every visible one.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// callableCatalog builds the "agents this agent may call" list (name + description); returns "" when empty.
// Same source as the authorization check in caller.go (agent_relations out-edges ∩ runnable ∩ not the super agent), so the model never
// fills in a name that is bound to be refused. Progressive disclosure: names and descriptions only.
func (a *agentImpl) callableCatalog() string {
	c := a.toolCtx
	if c == nil || c.Caller == nil || c.AgentID == "" {
		return ""
	}
	list, err := c.Caller.Callable(context.Background(), c.AgentID)
	if err != nil || len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Agents you may call for collaboration (use call_agent with the agent_name argument when needed):\n")
	for _, info := range list {
		desc := strings.TrimSpace(info.Description)
		if desc == "" {
			desc = "(no description)"
		}
		line := fmt.Sprintf("- %s: %s\n", info.Name, desc)
		if b.Len()+len(line) > maxAgentPromptChars {
			b.WriteString("...(more callable agents omitted)")
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *agentImpl) defaultSystemPrompt() string {
	return fmt.Sprintf("You are agent %s. Call the available tools to complete the task the user asks for.", a.instance.Name)
}

// maxSessionHistoryBytes caps how much of the stored history is fed back to the model (the newest round is always kept,
// see trimHistoryByBudget).
//
// How many rounds are stored and read is NOT a constant: agent_configs.max_session_records (see a.limits) is the
// agent-side counterpart of flow_configs.max_session_records, so both owner types read one number from one place. It used
// to be a hard-coded 50 / 100 here, which meant the tenant could tune the flow's memory but not the agent's.
const maxSessionHistoryBytes = 256 << 10

// userContent extracts the user input (preferring the args field).
func (a *agentImpl) userContent() string {
	// If the entry point explicitly provided "the line fed to the model", that wins: web chat appends the attachment list to it, while
	// persistence uses the raw text (_record_input); the two must not be mixed up.
	if s, ok := a.args["_user_message"].(string); ok && s != "" {
		return s
	}
	if v, ok := a.args["args"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	if b, err := json.Marshal(a.args); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", a.args)
}

// loadSessionHistory reads this agent's history in the current session (old -> new).
//
// The owner is the current agent: when a parent and child share one session_id, each reads its own line without polluting the other;
// the latest N rounds are taken (ListBySessionPage reads in reverse then flips), not the earliest N - in a long conversation the earliest
// rounds are nearly worthless for the current one.
func (a *agentImpl) loadSessionHistory() []gateway.Message {
	sid, uid := a.sessionMeta()
	if sid == "" || uid == "" {
		return nil
	}
	recs, err := sessionrecords.NewRepository().ListBySessionPage(
		a.schema, models.SessionOwnerTypeAgent, a.instance.ID, sid, a.limits.MaxSessionRecords, nil)
	if err != nil {
		xlog.Error("failed to read session history session=%s: %v", sid, err)
		return nil
	}
	return core.RecordsToMessages(trimHistoryByBudget(recs, maxSessionHistoryBytes))
}

// trimHistoryByBudget accumulates the byte budget from the newest backwards and drops older records once it is exceeded.
//
// The newest record is kept even when it is over budget: context adjacent to the current round matters more than "the total never
// exceeds", and dropping everything would leave the model unable to follow the round just finished.
func trimHistoryByBudget(records []models.SessionRecord, budget int) []models.SessionRecord {
	total, cut := 0, 0
	for i := len(records) - 1; i >= 0; i-- {
		n := len(records[i].Input) + len(records[i].Output)
		if total+n > budget && total > 0 {
			cut = i + 1
			break
		}
		total += n
	}
	return records[cut:]
}

// recordSessionRound writes this round into session_records and refreshes the session title / time.
//
// Nothing is written without a session (empty session_id): that is a one-shot run whose directory is swept once it finishes, so the
// record would have no owner.
// A write failure only logs: losing memory must not fail a call that has already finished.
func (a *agentImpl) recordSessionRound() {
	sid, uid := a.sessionMeta()
	if sid == "" || uid == "" || a.toolCtx == nil {
		return
	}
	if !a.startedAt.IsZero() {
		// Mutually exclusive with GetDurationMs: the entry point may read this round's elapsed time from another goroutine.
		a.mu.Lock()
		a.elapsedMs = time.Since(a.startedAt).Milliseconds()
		a.mu.Unlock()
	}
	input, _ := json.Marshal(map[string]string{"content": a.recordInputText()})
	// Result status: stopped / failed / completed normally.
	// Without this column a failed round in the history is "a line with an empty answer", and the user cannot tell whether they pressed
	// stop, the model died, or a tool errored - they have to ask someone.
	status, errMsg := a.outcome()
	rec := &models.SessionRecord{
		OwnerType:  models.SessionOwnerTypeAgent,
		OwnerID:    a.instance.ID,
		SessionID:  sid,
		RunID:      a.toolCtx.RunID,
		Input:      input,
		Output:     a.GetResult(),
		RunType:    a.runType,
		DurationMs: a.elapsedMs,
		Status:     status,
		Error:      errMsg,
	}
	if err := sessionrecords.NewRepository().Append(a.schema, rec, a.limits.MaxSessionRecords); err != nil {
		xlog.Error("failed to save session record session=%s: %v", sid, err)
		return
	}

	// Refresh the session: an empty title takes the first input, truncated; otherwise only updated_at is refreshed
	sessRepo := sessions.NewRepository()
	sess, err := sessRepo.Get(a.schema, sid)
	if err != nil || sess == nil {
		return
	}
	if sess.Title == "" {
		title := []rune(a.recordInputText())
		if len(title) > 20 {
			title = title[:20]
		}
		_ = sessRepo.UpdateTitle(a.schema, sid, string(title))
		return
	}
	_ = sessRepo.Touch(a.schema, sid)
}

// Audit log caps (agent_run_records): the question text and the answer preview are bounded, and the sizes are
// stored alongside so the page can say "truncated" instead of silently showing a cut. The full answer stays in
// session_records and is read there by the chat page.
//
// How many rows one agent keeps is NOT a constant here: it comes from platform.agent_configs.max_run_records
// (see a.limits, code fallback 100, the same number as flow_configs), the same place as the other agent run parameters.
const (
	maxAgentRunQuestionRunes = 2000
	maxAgentRunAnswerRunes   = 4000
)

// outcome reports this run's result. Memory and the run log share it so the two can never disagree on whether
// a run counted as stopped or failed.
func (a *agentImpl) outcome() (status, errMsg string) {
	errMsg = a.GetError()
	status = models.SessionStatusOK
	switch {
	case a.stopped.Load():
		status = models.SessionStatusStopped
	case errMsg != "":
		status = models.SessionStatusFailed
	}
	return status, errMsg
}

// recordRun writes this run into agent_run_records, the audit log behind the admin console's agent history.
//
// Unlike recordSessionRound it does not require a session: a stateless MCP / RPC call (no _meta.session_id) is
// exactly the kind of run an operator needs to trace, and session_records cannot hold it because its
// session_id is NOT NULL. A write failure only logs - losing a log line must never fail a finished call.
func (a *agentImpl) recordRun() {
	if a.toolCtx == nil || a.toolCtx.RunID == "" || a.instance == nil || a.instance.ID == "" {
		return
	}
	status, errMsg := a.outcome()
	question := a.recordInputText()
	answer := a.GetResult()
	answerSize := len([]rune(answer))
	answerPreview := truncateRunes(answer, maxAgentRunAnswerRunes)
	finished := xsql.Time(time.Now())

	rec := &models.AgentRunRecord{
		// The run id is the row id: session_records.run_id holds the same value, so one run has one identity.
		ID:              a.toolCtx.RunID,
		AgentID:         a.instance.ID,
		SessionID:       strings.TrimSpace(a.toolCtx.SessionID),
		UserID:          strings.TrimSpace(a.toolCtx.UserID),
		RunType:         a.runType,
		Status:          status,
		ErrMsg:          errMsg,
		DurationMs:      a.GetDurationMs(),
		StartedAt:       xsql.Time(a.startedAt),
		FinishedAt:      &finished,
		Question:        truncateRunes(question, maxAgentRunQuestionRunes),
		QuestionSize:    len([]rune(question)),
		Answer:          answerPreview,
		AnswerTruncated: answerSize > len([]rune(answerPreview)),
		AnswerSize:      answerSize,
	}
	if err := agentruns.NewRepository().SaveRun(a.schema, rec, a.limits.MaxRunRecords); err != nil {
		xlog.Error("failed to save the agent run record run=%s agent=%s: %v", a.toolCtx.RunID, a.instance.ID, err)
	}
}

// truncateRunes cuts s to at most n runes, never in the middle of a multi-byte character.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// sessionMeta reports whether this run is bound to a session: it returns the session id and the owning user (memory is written only when
// both are non-empty).
func (a *agentImpl) sessionMeta() (sid, uid string) {
	if a.toolCtx == nil {
		return "", ""
	}
	sid = strings.TrimSpace(a.toolCtx.SessionID)
	uid, _ = a.args["user_id"].(string)
	return sid, strings.TrimSpace(uid)
}

// recordInputText is the user input text written to storage: the entry point may specify it (web chat passes the raw message, while the
// line fed to the model carries the attachment list); without it the current prompt is used.
func (a *agentImpl) recordInputText() string {
	if s, ok := a.args["_record_input"].(string); ok && s != "" {
		return s
	}
	return a.userContent()
}

// GetDurationMs is this round's execution time. It is frozen when the record is written, so the entry point reads the value that was
// persisted.
func (a *agentImpl) GetDurationMs() int64 {
	a.mu.Lock()
	elapsed := a.elapsedMs
	a.mu.Unlock()
	if elapsed > 0 {
		return elapsed
	}
	if a.startedAt.IsZero() {
		return 0
	}
	return time.Since(a.startedAt).Milliseconds()
}

// stringArg reads a string argument (trimmed; an empty string falls back to the default).
func stringArg(m map[string]any, key, def string) string {
	if s, ok := m[key].(string); ok {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return def
}

// toOpenAIToolCalls converts gateway.ToolCall to the OpenAI format (for filling back assistant messages).
func toOpenAIToolCalls(calls []gateway.ToolCall) []gateway.OpenAIToolCall {
	out := make([]gateway.OpenAIToolCall, 0, len(calls))
	for _, c := range calls {
		oc := gateway.OpenAIToolCall{ID: c.ID, Type: "function"}
		oc.Function.Name = c.Name
		argsBytes, _ := json.Marshal(c.Args)
		oc.Function.Arguments = string(argsBytes)
		out = append(out, oc)
	}
	return out
}

// marshalToolResult serialises a tool result into a string (for filling back tool messages).
func marshalToolResult(result any, err error) string {
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	b, e := json.Marshal(result)
	if e != nil {
		return fmt.Sprintf("%v", result)
	}
	return string(b)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// pushEvent pushes a structured event (non-blocking; drops and counts them when the buffer is full).
// Every streaming consumer (mcp / rpc / flow / admin SSE) reads this one channel: thinking, content and tool calls all live here, and
// there is no separate plain-text progress channel any more.
//
// The send must happen **while holding the lock**: CloseEventChan closes under the same lock, and
// "take ch inside the lock -> unlock -> the other side closes -> this side sends" would panic outright: send on closed channel.
// Since the send itself is non-blocking (select/default), holding the lock never blocks the closer.
func (a *agentImpl) pushEvent(ev core.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eventCh == nil {
		return // already closed: events arriving after the run ends are dropped, never panicking
	}
	select {
	case a.eventCh <- ev:
	default: // buffer full -> drop, so the agent loop is not blocked; the dropped count is reported when Stream returns
		a.dropped.Add(1)
	}
}

// ---- core.Agent interface implementation ----

func (a *agentImpl) Close() error {
	a.cleanupEphemeralWorkspace()
	return a.CloseChans()
}

// cleanupEphemeralWorkspace deletes the temporary working directory of a session-less call.
//
// A session's workspace belongs to the session (resumable, inspectable) and must never be deleted here; only one-shot workspaces under
// runs/ are cleaned up when the instance is destroyed. The fallback sweep at process start is CleanupOrphanRuns.
func (a *agentImpl) cleanupEphemeralWorkspace() {
	c := a.toolCtx
	if c == nil || c.SessionID != "" {
		return // with a session: the workspace and long-lived processes belong to the session itself
	}
	// Fast path: interactive processes without a session live and die with the run, so destroy them explicitly here;
	// sandboxd's short idle timeout is only a fallback for "nexus crashed / missed the signal".
	tools.KillInteractiveRun(c.RunID)
	if c.WorkKey == "" || c.Storage == nil {
		return
	}
	if err := c.Storage.DeletePrefix(context.Background(), c.WorkKey); err != nil {
		xlog.Error("failed to clean up the temporary workspace key=%s: %v", c.WorkKey, err)
	}
}

// killInteractiveOnStop reclaims the resident processes the agent started (browser / shell) after the run is stopped.
//
// How it splits work with cleanupEphemeralWorkspace:
//   - this one only fires on "stopped", and it **covers session level too** - being stopped means no more resources may be held;
//   - cleanupEphemeralWorkspace only reclaims session-less processes on a normal end, leaving session-level ones for the next round
//     (so page state survives).
//
// The key algorithm is exactly the same as the shell / browser tools (see tools.KillInteractive).
func (a *agentImpl) killInteractiveOnStop() {
	c := a.toolCtx
	if c == nil {
		return
	}
	tools.KillInteractive(c.SessionID, c.RunID)
}

func (a *agentImpl) CloseChans() error {
	a.CloseEventChan()
	return nil
}

// GetEventChan returns the structured event channel (thinking / content / tool calls).
func (a *agentImpl) GetEventChan() chan core.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.eventCh
}

// CloseEventChan closes the event channel (internal to the implementation; not part of the core.Agent interface).
func (a *agentImpl) CloseEventChan() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eventCh != nil {
		close(a.eventCh)
		a.eventCh = nil
	}
}

func (a *agentImpl) GetResult() string { a.mu.Lock(); defer a.mu.Unlock(); return a.result }

// SetResult stores the final result (internal to the implementation; not part of the core.Agent interface).
func (a *agentImpl) SetResult(r string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.result = r
}

func (a *agentImpl) GetError() string { a.mu.Lock(); defer a.mu.Unlock(); return a.errMsg }

// SetError records an execution error (internal to the implementation; not part of the core.Agent interface).
func (a *agentImpl) SetError(e string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errMsg = e
}

// LogInfo logs an info message (internal to the implementation; not part of the core.Agent interface).
func (a *agentImpl) LogInfo(msg string) { xlog.Info("[agent:%s] %s", a.instance.Name, msg) }

func (a *agentImpl) LogError(msg string) { xlog.Error("[agent:%s] %s", a.instance.Name, msg) }
