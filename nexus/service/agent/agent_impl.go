package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	llmmodels "flame/nexus/shared/repository/llmmodels"
	"flame/nexus/shared/repository/sessionrecords"
	"flame/nexus/shared/repository/sessions"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/runledger"
	"flame/nexus/shared/sandboxinfo"
	"flame/nexus/shared/versioning"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// 循环最大轮次不再是常量：它来自 platform.agent_configs（租户/系统两级 + fallback，
// 见 shared/agentconfig），run 开始时快照进 agentImpl.limits。代码 fallback 仍是 80，
// 表没建时行为与改造前完全一致。

// maxSkillPromptChars system prompt 中 skill 清单的字符上限，防止 skill 过多撑爆上下文。
const maxSkillPromptChars = 2000

// maxAgentPromptChars system prompt 中「可调用 agent 清单」的字符上限。
const maxAgentPromptChars = 1000

// agentImpl 是 core.Agent 的具体实现（filesystem/builtin 类型）。
type agentImpl struct {
	instance *models.Agent
	schema   string
	args     map[string]any

	mu      sync.Mutex
	eventCh chan core.Event // 结构化事件（思考/正文/工具调用）
	result  string
	errMsg  string

	// dropped 事件通道满时丢弃的事件数。正常恒为 0；非 0 说明消费方跟不上，
	// 正文可能缺字（见 pushEvent），Stream 结束时上报一次。
	dropped atomic.Int64

	// stopped 本轮是否被主动终止（用户停止 / 会话关闭）。用于会话记录里区分
	// 「被停」与「出错」：两者的 error 文案不同，但只有 status 能让人一眼看出来。
	stopped atomic.Bool

	// 循环状态
	toolCtx  *tools.Context
	toolDefs []gateway.ToolDef
	modelIn  gateway.ModelInput

	// skillDiscovery 该 agent 是否允许发现未绑定的技能（skill_discovery）。
	// 正式运行取**当前版本**里冻结的值（versioning.AgentCurrentSwitches），草稿模式取 agents 表当前值。
	// 只影响 list_skills / read_skill / use_skill 的范围；system prompt 的注入清单固定只列「私有 + 已绑定」。
	skillDiscovery bool

	// limits 本次 run 的运行参数快照（platform.agent_configs，见 shared/agentconfig）。
	// 刻意在 run 起点取一次而不是每轮查：轮次上限、工具数、输出上限在整个 run 内
	// 必须稳定 —— 中途变更会让「为什么这次跑到第 40 轮就停」变得无法解释。
	limits agentconfig.Limits

	// 会话记忆：本次 run 的计时与落库口径。
	// startedAt 在 Stream 开头打点，elapsedMs 在写记录时冻结 —— 入口读 GetDurationMs()
	// 拿到的值必须与 session_records.duration_ms 是同一个数（刚跑完看到的秒数与
	// 刷新后历史里的秒数要对得上）。
	startedAt time.Time
	elapsedMs int64

	// runType 入口注入的调用来源（call / rpc / mcp / agent / flow），缺省 call。
	runType string
}

// newAgentImpl 创建并初始化 agentImpl。
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

	// 1. 构建工具上下文
	rootDir := config.Cfg.Agent.RootDir
	userID, _ := arguments["user_id"].(string)
	sessionID, _ := arguments["session_id"].(string)
	runID, _ := arguments["run_id"].(string)
	if runID == "" {
		runID = uuid.NewString()
	}
	// session_id 会拼进存储路径，必须先是合法 UUID 才允许往下走。
	//
	// 这里**不静默降级到 runs/**：入口层（chat / MCP / RPC / flow）都已校验过，
	// 走到这里还不合法就是内部 bug；而悄悄换掉路径的代价是「本该写进会话的文件
	// 落进一个跑完就删的目录」，事后从会话里根本找不回来。宁可这次调用直接失败。
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		if _, perr := uuid.Parse(sessionID); perr != nil {
			return nil, fmt.Errorf("invalid session_id %q: 会话 id 必须是 UUID", sessionID)
		}
	}
	// 调用深度：由上层 call_agent 注入（顶层为 0）
	callDepth, _ := arguments["_call_depth"].(int)
	// 调用链：由入口（RPC / MCP / flow / call_agent）注入，出站时原样带出。
	// 不传 = 空链（老调用方），此时只剩深度上限兜底。
	callChain := callchain.FromArguments(arguments)

	// 定义域解析模式：正式入口传 "current" → 读 is_current 那一版；
	// 空串 = 草稿（编辑页调试 / 试聊 / 单测）。
	//
	// 没有当前版本时直接失败（ErrNoReleasedVersion），不退回草稿 ——
	// 否则「正式运行读正式版本」这句话就不成立，出问题也查不清跑的是哪份定义。
	// 唯一的例外是超级智能体：它恒读草稿，见 resolveDefinitionMode。
	rawMode, _ := arguments[DefinitionModeKey].(string)
	mode := resolveDefinitionMode(rawMode, instance.IsSupervisor)

	agentVersion := 0
	if mode.IsCurrent() {
		v, _, verr := versioning.AgentCurrentVersion(schema, instance.Name)
		if verr != nil {
			return nil, verr
		}
		agentVersion = v

		// 「不限制工具」与「允许发现未绑定的技能」被冻结在版本里（见 admin/agents/versions.go），
		// 正式运行必须读版本那一份 —— 读 agents 表当前值等于发布冻结失效。
		// 复制一份实例再改，别就地改调用方持有的对象（它可能被入口层复用）。
		unrestricted, discovery, serr := versioning.AgentCurrentSwitches(schema, instance.Name)
		if serr != nil {
			return nil, serr
		}
		inst := *instance
		inst.ToolsUnrestricted = unrestricted
		inst.SkillDiscovery = discovery
		instance = &inst
		a.instance = instance
	}

	// 子调用是否跟随草稿：supervisor 恒 true（它建的东西都还没发布，必须能立刻调起来验证）；
	// 其余：自身草稿时看开关（缺省开），自身正式时恒 false —— 草稿不从正式链扩散。
	followDrafts := resolveFollowDrafts(mode, arguments[FollowDraftsKey], instance.IsSupervisor)

	// 定义域根：正式运行指向版本目录（exec 的 /agent 挂载源），草稿模式指普通目录。
	agentRoot := paths.ResolveAgentDir(rootDir, schema, instance.Name)
	if agentVersion > 0 {
		agentRoot = paths.ResolveVersionDir(rootDir, schema, paths.VersionAgents, instance.Name, agentVersion)
		// 版本行在、目录不在（被手删 / 迁移失败）：必须当场失败。
		// 否则 docker 会把不存在的宿主路径建成**空目录**挂到 /agent，
		// 配合「读不到 AGENT.md 就回退默认提示」会静默跑成一个没有身份的空 agent。
		if st, serr := os.Stat(agentRoot); serr != nil || !st.IsDir() {
			return nil, fmt.Errorf("智能体 %q 的当前版本（v%d）目录不存在：%s（可能未发布完整版本或目录被删除，请重新发布）",
				instance.Name, agentVersion, agentRoot)
		}
	}

	// skill 可见性口径：是否允许发现未绑定的技能（agents.skill_discovery）。
	// 该开关是 agent 实体字段，instance 由 agentColumns 查出即已携带，无需再单独查库。
	a.skillDiscovery = instance.SkillDiscovery

	// 运行参数快照（租户行 -> 系统行 -> fallback，系统行是硬顶）。
	// 表没建 / 没配时全部退回代码 fallback，行为与改造前一致；
	// 读失败也走 fallback，不让一次配置读失败把整次对话打挂。
	a.limits = agentconfig.Resolve(schema)

	// 运行域工作目录（文件工具默认根 = exec 的 cwd）：
	//   有 session → users/{uid}/sessions/{sid}/
	//   无 session → users/{uid}/runs/{run_id}/（临时，兜底清扫）
	//   连 user 都没有 → 租户级 runs/{run_id}/（极端兜底）
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

	a.toolCtx = &tools.Context{
		Schema:           schema,
		AgentName:        instance.Name,
		AgentID:          instance.ID,
		IsSupervisor:     instance.IsSupervisor,
		UserID:           userID,
		SessionID:        sessionID,
		RunID:            runID,
		WorkKey:          workKey,
		Workspace:        workDir,
		AgentRoot:        agentRoot,
		DefinitionMode:   mode,
		AgentVersion:     agentVersion,
		FollowDrafts:     followDrafts,
		RootDir:          rootDir,
		SkillRoot:        paths.ResolveSkillsDir(rootDir, schema),
		CustomTools:      newCustomToolSource(schema, mode),
		CustomToolsAdmin: newCustomToolAdmin(schema),
		ToolsRoot:        paths.ResolveToolsDir(rootDir, schema),
		Storage:          agentStorage,
		Agents:           NewAgentManager(schema, instance.ID, instance.Name, userID, agentStorage, instance.IsSupervisor),
		Skills:           NewSkillManager(schema, instance.ID, instance.Name, userID, agentStorage, instance.SkillDiscovery, instance.IsSupervisor, mode),
		ToolGrants:       NewToolGrantManager(schema, instance.ID, instance.Name, userID, instance.IsSupervisor, mode),
		ToolCatalog:      newToolCatalogManager(schema, instance.Name, instance.IsSupervisor),
		Flows:            NewFlowsManager(schema, userID),
		Relations:        NewAgentRelationsManager(schema, instance.ID, instance.Name, userID, instance.IsSupervisor),
		Caller:           NewAgentCaller(schema, a.pushEvent, a.limits.MaxCallDepth),
		Externals:        &externalToolRunner{},
		Endpoints:        &localEndpointRunner{},
		CallDepth:        callDepth,
		CallChain:        callChain,
		Timeout:          60 * time.Second,
		MaxOutput:        a.limits.MaxOutputBytes,
		Sandbox:          tools.CurrentSandbox(),
	}

	// 2. 查默认 LLM 模型
	model, err := llmmodels.NewRepository().GetDefault(&auth.AuthInfo{Schema: schema})
	if err != nil {
		return nil, fmt.Errorf("get default llm model: %w", err)
	}
	if model == nil {
		return nil, fmt.Errorf("no default llm model configured")
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

	// 3. 绑定工具（工具授权体系优先；未配置时仅内置超级智能体缺省全开）
	a.refreshToolDefs()
	// 平台能力快照：与工具授权是两个正交的闸（租户能授什么 × 平台允许用什么）。
	// 恒为「非 nil」——缺键即未开通，执行层据此拒绝（见 tools/registry.go）。
	a.toolCtx.Caps = a.capabilities()

	return a, nil
}

// refreshToolDefs 重算工具 schema 与执行闸门（两者必须同源，见 AllowedTools 的注释）。
//
// 调用点两处：① newAgentImpl 初始化；② 循环里「本轮读过新技能」时。
// 后者是「按需注入」的实现：模型 use_skill 拿到正文后，下一次 LLM 调用就能看到
// 该技能声明的工具，不必再 bind_skill；run 结束 Context 消失，工具随之消失。
func (a *agentImpl) refreshToolDefs() {
	if a.instance == nil || a.toolCtx == nil {
		return
	}
	defs := a.buildToolDefs(a.instance)
	a.toolDefs = defs
	// 执行闸门与「声明给模型的 tools」同源：非 nil（可能为空集）→ 未授权工具一律拒绝。
	// 防的是文本形态工具调用（模型把调用写进正文，如 |DSML| 标记）绕过工具授权。
	allowed := make(map[string]bool, len(defs))
	for _, d := range defs {
		allowed[d.Name] = true
	}
	a.toolCtx.AllowedTools = allowed
}

// buildToolDefs 绑定本次运行可用的工具：
//  1. 该 agent 已配置工具授权（agent_tool_grants）或开启了「不限制工具」时，
//     按工具授权体系的解析结果绑定（required 恒注入 + skill 需要且已授权的 + 不限制时的候选，再减去 deny）；
//  2. 未配置任何授权项时：只有超级智能体缺省拥有**全部内置工具**，
//     其他 agent 不绑定任何工具。注：新 agent 由 agents.CreateAgent 物化
//     models.DefaultAgentTools / models.MainCoreTools 的 required 授权，所以正常路径
//     不会走到这一支（仅存量未迁移 / 手工清空过授权的 agent）。
//  3. 执行通道：「内置工具 / 插件（tools.PluginRunner）/ mcp__ 与 rpc__ 外部连接
//     （tools.ExternalRunner，实现在 externaltool.go）/ custom__ 自定义工具 /
//     local__ 本地端点（实现在 localendpoint.go）」都是通的 —— 四条通道全接。
//
// schema 直接取解析结果的 FinalTools（叶子 ToolInfo，自带 Parameters）：
// 内置工具的名字与外部工具在同一个列表里，插件工具的 schema 也就不会在
// 「用 tools.List() 二次过滤」那一步被丢掉。
func (a *agentImpl) buildToolDefs(instance *models.Agent) []gateway.ToolDef {
	infos, err := a.resolveGrantedTools(instance)
	if err != nil {
		// 解析失败：仅内置超级智能体可缺省全开；其他 agent 一律不放行（避免异常时意外放大权限）
		xlog.Error("解析工具授权失败 schema=%s agent=%s: %v", a.schema, instance.Name, err)
		infos = nil
	}
	if infos == nil {
		if instance.IsSupervisor {
			// 保险丝：supervisor 零授权行时回落「全部内置工具」。
			//
			// 正常情况 supervisor 由 CreateAgent 或 sql/main_core_tool_grants.sql 物化
			// models.MainCoreTools，走不到这里。走到这里只有三种可能：存量租户没跑迁移、
			// 授权行被手工清空、或授权表读失败（上面已记 Error）。
			// 处理方式是**告警但仍然给全**：supervisor 是管理入口，让它失能等于整租户失去
			// 创建 / 授权能力，比短暂越权更难恢复（supervisor 本来就是全权身份）。
			xlog.Error("supervisor %s 未配置工具授权，回落缺省全开（保险丝）；请执行 sql/main_core_tool_grants.sql schema=%s",
				instance.Name, a.schema)
			return builtinToolDefs(a.capabilities())
		}
		xlog.Info("agent %s 未配置工具授权，本次不绑定任何工具 schema=%s", instance.Name, a.schema)
		return nil
	}

	defs := make([]gateway.ToolDef, 0, len(infos))
	for _, info := range infos {
		params := info.Parameters
		if params == nil {
			// 外部工具（mcp / rpc）如果没存 input_schema，这里补一个开放 object：
			// 内置工具能从 Meta() 现取，外部工具没有兜底，nil 直接被某些上游拒掉。
			params = emptyObjectSchema()
		}
		defs = append(defs, gateway.ToolDef{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  params,
		})
	}
	return defs
}

// emptyObjectSchema 「无参数」的 JSON Schema（形状必须合法，不能给 nil）。
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// resolveGrantedTools 解析授权体系下的工具清单（叶子 ToolInfo，含 schema）。
// 返回 nil 表示"该 agent 未配置授权"（不存在授权项且未开启不限制），
// 由调用方决定怎么处理：仅内置超级智能体缺省拥有全部内置工具，其他 agent 不绑定任何工具。
// 新 agent 由 agents.CreateAgent 物化 models.DefaultAgentTools / models.SupervisorCoreGrantRefs（required），
// 因此不会落在 nil 分支。
//
// 超级智能体的工具集**与其它 agent 完全同源**：表里有什么就是什么。曾经的「超级智能体恒为全部内置工具 +
// 授权表只回答额外还给了什么」已删除 —— 那让「超级智能体手里有什么」只有代码知道、库里查不到。
func (a *agentImpl) resolveGrantedTools(instance *models.Agent) ([]toolgrant.ToolInfo, error) {
	grantsRepo := toolgrants.NewRepository()

	grants, err := grantsRepo.ListAgentGrants(a.schema, instance.ID)
	if err != nil {
		return nil, err
	}
	unrestricted := instance.ToolsUnrestricted // 「不限制工具」属 agent 自身，直接取实例字段
	// 未配置授权时：supervisor 缺省即「全部内置工具 + 不限制工具」，交由 buildToolDefs 处理；
	// 其他 agent 同样返回 nil，由 buildToolDefs 决定（未配置授权 → 不绑定任何工具）。
	if len(grants) == 0 && (!unrestricted || instance.IsSupervisor) {
		return nil, nil
	}

	// 候选来源 = **本轮已激活**的技能（模型 use_skill 读过正文）。绑定不算。
	//
	// 绑定只决定「system prompt 里有没有这一条注释」（见 skillCatalog）；工具要等模型
	// 真的决定用它时才进 schema —— 这是「按需给能力」的唯一触发点，run 结束随 Context
	// 消失（历史只回填文本），不跨 run 残留。未激活就不给工具（未绑定的共享技能即使
	// 可被发现也一样）。上限不受影响：候选恒被 allow 收窄，激活只能点亮管理员已授权的组。
	//
	// 顺序必须稳定：Resolver 的截断优先级按「技能声明顺序」排，而 LoadedSkills 是 map。
	// 依赖声明随激活登记一起带过来（正式运行下取自当前版本）：候选只认这份快照，
	// 不再回头查 skill_tool_grants —— 否则草稿改依赖会漏进线上。
	visible := make([]toolgrant.VisibleSkill, 0)
	if a.toolCtx != nil {
		entries := a.toolCtx.LoadedSkillEntries()
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Name != entries[j].Name {
				return entries[i].Name < entries[j].Name
			}
			return entries[i].ID < entries[j].ID
		})
		for _, e := range entries {
			visible = append(visible, toolgrant.VisibleSkill{ID: e.ID, Name: e.Name, Deps: e.Deps})
		}
	}

	// 目录的定义来源跟随本次运行：正式运行只认「当前版本里发布过的」自定义工具与参数 schema，
	// 否则会出现「模型按草稿参数调用、脚本却来自版本目录」的错位。
	mode := tools.DefinitionMode("")
	if a.toolCtx != nil {
		mode = a.toolCtx.DefinitionMode
	}
	catalog := toolgrant.NewCatalog(a.schema, grantsRepo, instance.IsSupervisor).
		WithCapabilities(a.capabilities()).
		WithDefinitionMode(mode)
	// 开关取实例字段（agents.tools_unrestricted）：它属 agent 自身，不再由 Resolver 查库
	resolver := toolgrant.NewResolver(catalog, a.schema, visible, instance.ToolsUnrestricted)
	// 工具数上限来自 platform.agent_configs（fallback = toolgrant.DefaultMaxTools）
	resolver.MaxTools = a.limits.MaxTools
	res, err := resolver.Resolve(instance.ID)
	if err != nil {
		return nil, err
	}
	for _, w := range res.Warnings {
		xlog.Info("工具授权提示 schema=%s agent=%s: %s", a.schema, instance.Name, w)
	}

	out := make([]toolgrant.ToolInfo, 0, len(res.FinalTools))
	for _, info := range res.FinalTools {
		// 工具名要过上游校验：OpenAI 系限制函数名 ≤64 字符，超了可能整次请求被拒
		// （或被网关静默丢工具）。mcp__ / rpc__ 前缀 + 连接名 + 工具名很容易超，
		// 提前跳过并留下能定位的日志，比让模型收到一个看不懂的 400 好。
		if len(info.Name) > maxToolNameLen {
			xlog.Error("工具名超长（%d > %d），本次不绑定 schema=%s agent=%s name=%s",
				len(info.Name), maxToolNameLen, a.schema, instance.Name, info.Name)
			continue
		}
		out = append(out, info)
	}

	return out, nil
}

// maxToolNameLen 上游对函数名的长度上限（OpenAI 系为 64）。
// 外部工具的扁平名是「前缀 + 连接名 + 工具名」，是最容易撞上限的一类，
// 所以绑定时就拦下（见 resolveGrantedTools），而不是等上游拒掉整次请求。
const maxToolNameLen = 64

// builtinToolDefs 内置超级智能体的缺省工具集（未配置授权 / 解析失败时的兜底）。
//
// caps 是平台能力视图：没开通的 exec / browser 不把 schema 递给模型 ——
// 执行层另有硬闸，但先递出去只会诱导模型做一次必然失败的调用。
func builtinToolDefs(caps map[string]bool) []gateway.ToolDef {
	infos := toolgrant.BuiltinTools()
	defs := make([]gateway.ToolDef, 0, len(infos))
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
	}
	return defs
}

// Stream 跑 agent 循环：LLM 推理 → 工具调用 → 结果回填 → 再推理，直到无工具调用。
func (a *agentImpl) Stream(parent context.Context) error {
	// 计时起点与落库收尾都在这里：无论成功、失败还是被终止，本轮都要留下一条记录
	// （失败时 output 为空）。改造前只有 Web 对话会写，端点调用不写 —— 同一个会话
	// 从两个入口进来，记忆一个是断的一个是连的，这正是要消除的不一致。
	a.startedAt = time.Now()
	defer a.recordSessionRound()
	// 丢弃的事件数正常恒为 0。非 0 说明消费方跟不上，正文会缺字 —— 必须留下
	// 可查的痕迹，而不是像改造前那样静默丢弃、事后无从判断「输出为何断了一截」。
	defer func() {
		if n := a.dropped.Load(); n > 0 {
			a.LogError(fmt.Sprintf("事件通道缓冲满，运行期间丢弃 %d 条事件（正文可能缺字）", n))
		}
	}()

	// 用可取消的 ctx：LLM 流与工具执行都要能被打断。终止入口见 runcontrol.go
	// （StopRun / StopRunCause / StopSession）；此前这里用的是 Background()，
	// 关掉页面或想强行停下时只能等模型自己跑完。
	//
	// 以 parent 为父：嵌套运行（call_agent）随父取消 —— 父被停时子实例立即退出，
	// 不再出现「父已停止、子还在烧 token 和沙箱」的空转。
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
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
	// 被终止时收掉本次运行拉起的常驻进程（browser / shell）。
	// 注册在 cancel 之后：LIFO 会先于 cancel(nil) 执行，所以此时
	// context.Cause 还能拿到真正的原因（用户终止 / 客户端断开）。
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
	// 上下文来源（优先级从高到低）：
	//   1. 调用方显式传入的 messages（外部注入 / 老口径）；
	//   2. 有 session_id：从 session_records 读同一 owner 的历史 —— 与 Web 对话同一份记忆；
	//   3. 无会话：单轮，只有当前输入。
	if history, ok := a.args["messages"].([]gateway.Message); ok && len(history) > 0 {
		messages = append(messages, history...)
	} else {
		messages = append(messages, a.loadSessionHistory()...)
		messages = append(messages, gateway.Message{Role: "user", Content: a.userContent()})
	}

	// 轮次上限来自 platform.agent_configs（代码 fallback 80）。到顶即收尾 ——
	// 这是防死循环的唯一防线，所以配置侧一律不接受 -1（见 sql/agent_configs.sql）。
	for i := 0; i < a.limits.MaxIterations; i++ {
		// 本轮激活了新技能 → 重算工具集：use_skill 之后，下一次推理就能看到它声明的工具。
		// 只在有变化时查库（ConsumeSkillsDirty 复位标记），普通轮次零额外开销。
		if a.toolCtx.ConsumeSkillsDirty() {
			a.refreshToolDefs()
		}
		content, toolCalls, err := a.streamOnce(ctx, messages)
		if err != nil {
			// 主动终止不是 LLM 故障：按 CancelCause 的原因给出人话，
			// 让 SSE / 调用方看到「已终止」而不是一串 context canceled。
			if cause := context.Cause(ctx); cause != nil {
				msg := cancelMessage(cause)
				a.stopped.Store(true)
				a.SetError(msg)
				return errors.New(msg)
			}
			a.SetError(fmt.Sprintf("llm generate: %v", err))
			return err
		}
		// 轮次指纹：排查「某轮返回空 / 不继续」用 —— 空正文 + 无工具调用即循环到此结束。
		a.LogInfo(fmt.Sprintf("round %d: content=%dB tools=%d head=%q",
			i, len(content), len(toolCalls), truncate(content, 120)))

		// 无工具调用：最终答案
		if len(toolCalls) == 0 {
			a.SetResult(content)
			return nil
		}

		// 回填方式由调用来源决定：
		//   - 结构化（上游返回 tool_calls）：按 OpenAI 协议回填 assistant.tool_calls + role:"tool"；
		//   - 文本形态（FromText：模型把调用写在正文标记里，如 DeepSeek 的 |DSML|）：
		//     上游既然不返回结构化调用，多半也不认这套回填，改用纯文本，否则第二轮会被拒或返回空
		//     （表现为「工具执行完就不动了」）。
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
				fmt.Fprintf(&b, "\n调用工具 %s，参数 %s", tc.Name, argJSON)
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
			// 已终止：不再发起下一个工具 —— 每个工具都可能是一次沙箱调用。
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
					Content: fmt.Sprintf("工具 %s 的返回：\n%s", tc.Name, toolOut),
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
		// 工具已跑完但期间被终止：不再进入下一轮推理。
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

// streamOnce 流式跑一轮 LLM：正文/思考边到边推事件，工具调用在本轮结束时返回。
// 返回累积的正文与工具调用列表（无工具调用即最终答案）。
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
		if chunk.ReasoningContent != "" { // 思考（推理内容）
			a.pushEvent(core.Event{Type: core.EventThinking, Delta: chunk.ReasoningContent})
		}
		if chunk.Content != "" { // 正文增量
			sb.WriteString(chunk.Content)
			a.pushEvent(core.Event{Type: core.EventText, Delta: chunk.Content})
		}
		if len(chunk.ToolCalls) > 0 { // finish_reason 时给出的完整工具调用列表
			toolCalls = chunk.ToolCalls
		}
		if chunk.Done {
			break
		}
	}
	return sb.String(), toolCalls, nil
}

// loadSystemPrompt 读 AGENT.md 作为 system prompt（缺失则用默认提示），
// 并在末尾追加当前 agent 可见的 skill 清单。
// 采用渐进式披露：清单只给 name + 归属 + description，正文按需 read_skill / use_skill 读取。
func (a *agentImpl) loadSystemPrompt() (string, error) {
	prompt := a.defaultSystemPrompt()
	if a.toolCtx != nil && a.toolCtx.Storage != nil {
		key := a.toolCtx.AgentPath(paths.AgentEntryFile)
		data, err := a.toolCtx.Storage.ReadFile(context.Background(), key)
		switch {
		case err == nil:
			// AGENT.md 里可以引用全局变量（${global_vars.X}）：资产包里只留引用名，
			// 装到目标租户后由对方的变量值填充。展开失败要硬失败 —— 留着字面量，
			// 模型会把 ${global_vars.DB_HOST} 当成一句普通说明读过去。
			expanded, xerr := globalvars.Expand(a.toolCtx.Schema, string(data))
			if xerr != nil {
				return "", fmt.Errorf("展开 %s 里的全局变量失败: %w", paths.AgentEntryFile, xerr)
			}
			prompt = expanded
		case a.toolCtx.VersionBound():
			// 正式运行绑定到某个已发布版本：这一版里读不到 AGENT.md 说明快照不完整。
			// 不能静默回退默认提示 —— 那等于线上 agent 偷偷换了身份，而没人会知道。
			// 草稿模式保持原样：新建的 agent 还没写 AGENT.md 是正常状态。
			return "", fmt.Errorf("读取当前版本的 %s 失败（%s）: %w", paths.AgentEntryFile, key, err)
		}
	}
	if catalog := a.skillCatalog(); catalog != "" {
		prompt += "\n\n" + catalog
	}
	if catalog := a.callableCatalog(); catalog != "" {
		prompt += "\n\n" + catalog
	}
	// 能力边界与环境事实放在最后：排在 AGENT.md 之后，可纠偏用户在 AGENT.md 里写的错误声明。
	if notes := a.capabilityNotes(); notes != "" {
		prompt += "\n\n" + notes
	}
	if notes := a.sandboxNotes(); notes != "" {
		prompt += "\n\n" + notes
	}
	// 回复语言放在**最末**：这一段本身就是对前面所有内容的约束（含 AGENT.md），
	// 排在能力/沙箱说明之后，模型读到的顺序才是「事实 → 约定」。
	prompt += "\n\n" + responseLanguageRule
	return prompt, nil
}

// responseLanguageRule 回复语言的唯一硬约定。
//
// 为什么要显式写一句：工具说明、AGENT.md、技能正文、本文件里的能力/沙箱说明都可能是
// 中文，也可能是英文（产品主语言切换时它们会分批翻译，长期并存）。模型很容易把
// **提示词的语言**当成**回复的语言** —— 英文提示词下的中文提问于是得到英文回答，
// 这是「主语言换成英语」时用户唯一能立刻感知到的退化。
//
// 所以这条约定要把两者解耦：提示词的语言 ≠ 回复语言，回复语言跟用户走。
// 中英各写一遍：不依赖模型对另一门语言的阅读理解，也方便非中文的 AGENT.md 作者看懂。
// AGENT.md 的显式指定优先 —— 有的 agent 天生只服务一种语言（如某语种的写作老师），
// 那是有意为之，不能被这条通用约定覆盖。
const responseLanguageRule = "回复语言：跟随用户当前使用的语言（用户用中文就用中文，用英文就用英文）。" +
	"本提示词、工具说明、技能正文的语言**不**代表回复语言。" +
	"仅当 AGENT.md 明确指定了固定语言时，才按它执行。\n" +
	"Reply in the language the user writes in. The language of this prompt, of tool descriptions " +
	"and of skill docs is NOT the reply language. Follow AGENT.md only if it explicitly pins one."

// sandboxNotes 生成「沙箱环境」说明：沙箱能否联网（sandboxd 的**声明**）+ 沙箱里有什么（探测原文）。
//
// 与 capabilityNotes **分开**：那个讲「你有什么权限」（判据是 AllowedTools），这个讲「环境长什么样」
// （判据是 sandboxd 的能力快照）。位置相同（AGENT.md 之后），用来纠偏其中写错的环境声明。
//
// 三态都如实说：off / on 来自声明，未知（没拉到 / 探测失败）就明说未知 ——
// 尤其不能把「没拉到」说成「不能联网」，那会让模型白写一堆离线兜底代码。
//
// 只给「能碰沙箱」的 agent（判据见 usesSandbox）：一条通往沙箱的路都没有的 agent
// 看到这些事实没有用处，而且会长出「我在沙箱里，装不了包」这类凭空的说法。
func (a *agentImpl) sandboxNotes() string {
	// 从未注入过：宁可不提，也不要凭空告诉模型一个环境。
	if !sandboxinfo.Configured() {
		return ""
	}
	if !a.usesSandbox() {
		return ""
	}
	info := sandboxinfo.Get()

	// 一次都没成功拉到：只报未知，不猜（拉取失败时 Info 会被保留为上一次成功的快照，
	// Mode 为空即表示从无成功记录）。
	if info.Mode == "" {
		return "沙箱环境：未知。尚未从 sandboxd 取到能力快照（它可能没起来），" +
			"沙箱能否联网、有哪些模块都**不要假设**，需要时先运行命令自行确认。"
	}

	lines := []string{fmt.Sprintf("沙箱环境（隔离方式 %s；以下为 sandboxd 的声明与启动实测，未必同于其它环境的通用情况）：", info.Mode)}
	// 「配置」与「实测」分开写：langs 是 sandboxd 声明能跑哪些语言，下面那段是解释器
	// 真的在不在。两者不一致本身就是有用的信息（配了却没实测到 = 配置与事实不符），
	// 混在一句话里读起来会像两套事实。
	if len(info.Langs) > 0 {
		// 作用域必须写死在这一行里：它是 exec / 单工通道（命令执行、技能脚本、自定义工具）
		// 的语言，与工作流节点的可选语言无关。只说「可执行语言」会让模型把 js 当成
		// code 节点的可选项，而那种错误只在运行期以 unsupported_lang 暴露（见下一行）。
		lines = append(lines, "- 可执行语言（sandboxd 配置；用于命令执行与技能脚本，**不是工作流节点的可选语言**）："+strings.Join(info.Langs, "、"))
	}
	// 工作流脚本语言与上面那条**不是一回事**，必须分开说：顶层 langs 里有的语言未必
	// 开了双工池（只跑技能脚本的语言就不必开），照它去写工作流会被沙箱拒
	// （unsupported_lang），而那要等到运行期才暴露。
	if langs, ok := sandboxinfo.CodeLangs(); ok {
		if len(langs) == 0 {
			lines = append(lines, "- 沙箱没有配置任何**工作流脚本语言**（sandboxd 侧 code.pools 为空）：创建或修改工作流时不要使用脚本节点。")
		} else {
			lines = append(lines, "- 工作流脚本语言（sandboxd 配置）："+strings.Join(langs, "、")+
				"。创建/修改工作流时，脚本节点（代码、分支条件、循环条件、handler）的语言只能从这几个里选，其它语言会被沙箱拒绝（unsupported_lang）。")
		}
	}

	// 探测结果：nexus 不解析、原样透传（谁产生这份事实，谁负责把它讲清楚，见 sandboxd 的 probe.go）。
	if errText := strings.TrimSpace(info.Environment.Error); errText != "" {
		// 探测失败时说清「未知且原因」，别让模型把空清单当成「什么都没有」。
		lines = append(lines, "- 沙箱实测失败（"+errText+"），下面没有任何可信的模块清单：要用某个库时先自行确认，别假设它在。")
	} else if text := strings.TrimSpace(info.Environment.Text); text != "" {
		lines = append(lines, "- 沙箱实测（以下为 sandboxd 探测原文）：", text)
	}

	// 出网结论按**生效值**说，不能把部署上限（on-request）原样丢给模型 —— 它无法据此
	// 判断自己该不该写联网代码。这里叠加租户许可（net.egress），算出「你现在能不能联网」。
	switch info.Network.Exec {
	case sandboxinfo.NetworkOff:
		lines = append(lines, "- 沙箱内**不能**联网（network=off）：运行期 pip / npm / luarocks 都装不了 —— "+
			"只能用上面列出的模块（Python 标准库齐全，不在清单里也不代表不可用）。")
	case sandboxinfo.NetworkOn:
		lines = append(lines, "- 沙箱内**可以**联网（network=on），但 rootfs 只读：全局安装（`pip install` 到系统目录、`npm i -g`）会失败，"+
			"改用 `--user` 或项目内虚拟环境。")
	case sandboxinfo.NetworkOnRequest:
		// 部署允许按需出网：真正能不能联网取决于本租户有没有开通 net.egress（fail-closed）。
		// 这里必须给**结论**，不能把 on-request 原样丢出去 —— 模型没法据此判断自己该不该联网。
		if a.capabilities()[capability.Egress] {
			lines = append(lines, "- 沙箱内**可以**联网（network=on-request）：本租户已开通出网，`exec` 调用要带 `egress: true` "+
				"才会放行，不带就按断网执行。同样受 rootfs 只读限制（`pip install` 到系统目录会失败，用 `--user`）。")
		} else {
			lines = append(lines, "- 沙箱内**不能**联网（network=on-request 但本租户未开通 net.egress）："+
				"`exec` 传 egress 也会被拒绝。需要联网请让管理员开通，或改用工作流的 http 节点。")
		}
	default:
		lines = append(lines, "- 能否联网未知（sandboxd 未给出声明）：要联网时先跟用户确认，不要自行假设。")
	}

	// 浏览器：可用与否都是事实，分开说。
	// 不可用时必须说清「原因」，否则模型会把「工具没开」误判成「这个网站打不开」，
	// 然后在换 URL / 换选择器的方向上空转。
	if b := info.Browser; b != nil {
		switch {
		case !b.Enabled:
			reason := strings.TrimSpace(b.Error)
			if reason == "" {
				reason = "未启用"
			}
			lines = append(lines, "- 浏览器：**不可用**（"+reason+"）。这是工具能力的问题，不是目标网站的问题。")
		default:
			mode := "只能无头（本部署没有显示环境）"
			switch {
			case b.HeadedAvailable && b.HeadlessDefault:
				mode = "默认无头；需要人看见窗口时 open 传 headed=true（本部署允许）"
			case b.HeadedAvailable:
				mode = "默认有头（可见窗口）"
			}
			ver := strings.TrimSpace(b.Version)
			if ver == "" {
				ver = b.Engine
			}
			lines = append(lines, "- 浏览器：可用（"+ver+"；"+mode+"）。"+
				"看页面用 snapshot（无障碍树 + ref），点/输入用 ref；截图只落盘给人看，你自己读不到图像。")
		}
	}

	// 快照可能过期：如实标注，让模型知道该不该再信这份环境描述。
	if info.Err != "" {
		stale := "以上为最近一次成功拉取的快照"
		if !info.FetchedAt.IsZero() {
			stale = fmt.Sprintf("以上为 %s 的快照", info.FetchedAt.Format(time.RFC3339))
		}
		lines = append(lines, fmt.Sprintf("- 注意：最近一次刷新失败（%s），%s，可能已过期。", info.Err, stale))
	}

	return strings.Join(lines, "\n")
}

// usesSandbox 本 agent 有没有碰沙箱的途径 —— 判据是**工具授权**（AllowedTools），
// 与 capabilityNotes 同源；刻意不引入配置开关：开关是人手动维护的第二个真源，
// 「授了 exec 却忘了开开关」会让它没有任何环境事实、于是开始瞎猜。
//
// 只判「有权碰」，不判「一定会碰」：有这个能力就该知道环境长什么样。
//
// ⚠️ 判不了（没有工具上下文）时**照旧注入**：环境事实不像权限声明，多给比少给安全，
// 「略过」只在能确定它一条沙箱入口都没有时才做。
//
// ⚠️ 将来若出现「创建工作流」这类工具（代码片段走 /v1/code 通道），它的名字要加进来 ——
// 那时「工作流脚本语言」那行才对它有约束力（目前没有任何 agent 工具能创建流程）。
// capabilities 平台给本租户开通的沙箱能力快照（键见 shared/capability）。
//
// 读失败按**全部未开通**处理（fail-closed）：能力是成本护栏，一次 DB 抖动
// 放宽成「全员可执行」比拒绝一次调用危险得多（与插件 Runner 的选择一致）。
// 目录侧同样按未开通展示（见 toolgrant.Catalog.capabilityBlocked），两边方向相同。
// capabilitySnapshot 平台能力快照。抽成包级变量只为单测能替换 —— sandboxNotes 的
// on-request 分支需要「已开通 / 未开通 net.egress」两种结论，而真实快照要连库。
// 与 flow/executors 的 capabilitySnapshot 同款做法。
var capabilitySnapshot = capabilitypolicy.Snapshot

func (a *agentImpl) capabilities() map[string]bool {
	// 单一实现在 capabilitypolicy.Snapshot：目录侧与执行侧必须同向。
	return capabilitySnapshot(a.schema)
}

func (a *agentImpl) usesSandbox() bool {
	c := a.toolCtx
	if c == nil || c.AllowedTools == nil {
		return true
	}
	return c.AllowedTools["exec"] || c.AllowedTools["run_skill_script"] ||
		c.AllowedTools["shell"] || c.AllowedTools["browser"]
}

// capabilityNotes 按本次实际授权的工具集生成「能力边界」说明；无需提醒时返回空串。
//
// 判据是 AllowedTools（授权集），不是 is_supervisor —— 普通 agent 也可能被授权 create_own_skill。
// 但只能用它判断「缺什么能力」，**不能**据此生成「改用某工具」的指引：create_agent /
// create_shared_skill 是 super（仅超级智能体），对非超级智能体说「请用 create_agent」是无效指引。
//
// 存在的理由：文件工具的形状闸（tools/gate.go 的 entityLikePath）只在调用时兜底，
// 且拦不住换名字造目录；这一段是「事前」防线，让模型一开始就别去试。
func (a *agentImpl) capabilityNotes() string {
	c := a.toolCtx
	if c == nil || c.AllowedTools == nil {
		return ""
	}
	has := func(n string) bool { return c.AllowedTools[n] }

	// 归属 agent 那道闸：与 tools/gate.go、skillManager.checkOwnerAgent 同口径 ——
	// 只有「正常」态才能写它名下的私有技能，废弃态整片冻结。
	skillWritable := models.IsModifiable(int(a.instance.Status))

	var lines []string

	switch {
	case has("create_own_skill") && has("create_shared_skill") && skillWritable:
		// 两类都能建，无需提醒
	case has("create_own_skill") && !skillWritable:
		// 有工具、但归属闸关着：说清原因，别让模型反复试（与闸门口径一致）
		lines = append(lines, "- 你**当前不能**改**自己的私有**技能：本 agent 已「废弃」。"+
			"用户需要改动时，请告知由管理员把该 agent 切回「正常」状态。")
	case has("create_own_skill"):
		lines = append(lines, "- 你可建/改**自己的私有**技能（create_own_skill）；**没有**共享技能的权限，用户要共享技能时如实说明。")
	default:
		lines = append(lines, "- 你**没有**创建/修改技能的权限。**不要用文件工具在会话工作区里造 SKILL.md 或 skills/ 目录**——不会被系统登记、也不生效；用户需要新技能，请告知由管理员为其开启 `create_own_skill`。")
	}

	if !has("create_agent") {
		if a.instance.IsSupervisor {
			// supervisor 的写闸门允许它改任何**正常态** agent 定义域内的文件（含 AGENT.md，见 tools/gate.go
			// 的 D6：边界按「实体存在性」而非文件类型）；这里只提醒「新建 / 登记」要走元工具，
			// 别说成"不能改 AGENT.md"——否则与闸门口径互相矛盾。
			lines = append(lines, "- 新建 agent 或改 agent 名要走 `create_agent` / `update_agent`；不要用文件工具凭空造 `agents/` 目录（不会被登记）。改已有 agent 的定义域文件（含 AGENT.md）仍可用文件工具，路径带 `/` 前缀。")
		} else {
			lines = append(lines, "- 你**不能**创建/修改 agent 定义（AGENT.md），也**不要**用文件工具造 agents/ 目录（不生效）。需要新建/修改 agent，请让管理员或超级智能体操作。")
		}
	}

	// 常驻工具的成本提示：shell / browser 是 session 级常驻进程（有会话时跨对话保留），
	// 不是免费的探索手段。只对被授权了它们的 agent 说（判据同 AllowedTools），别给不相关的人灌噪音。
	// 写成「判据 + 收尾动作」而不是「请谨慎使用」：后者会让模型该用的时候也不敢用，
	// 任务直接失败 —— 成本提醒的目的是让它先试一次性手段，不是让它躲着常驻工具。
	if has("shell") || has("browser") {
		lines = append(lines, "- 常驻工具要算成本：`shell` / `browser` 会在沙箱里起**常驻进程**"+
			"（有会话时跨对话保留，占内存与进程）。默认先用一次性手段（`exec` / `run_skill_script` / 读文件）；"+
			"只有需要**跨调用的状态**（cwd / 环境变量 / 后台任务 / 登录态 / 多步页面交互）时才开，"+
			"用完立刻收掉：`shell` 传 `kill: true`，`browser` 传 `action: close`。")
	}

	if len(lines) == 0 {
		return ""
	}
	return "能力边界（按你当前授权生成，勿凭猜测承诺）：\n" + strings.Join(lines, "\n")
}

// skillCatalog 生成「注入给本 agent」的 skill 清单文本（name + 归属 + description）。
// 无 skill 时返回空串 —— 但若开着「可发现未绑定的技能」，仍给一句 list_skills 指引
// （那种 agent 没有清单可注入，这句指引就是它唯一的发现入口）。
// 口径固定为「私有 + 已绑定（toolgrant/repo.ListInjected）」，与 skill_discovery 开关无关：
// 只有绑定到该 agent 的技能才常驻提示词，未绑定的共享技能不会自动注入。
// 正文不进提示词（每行只有名字/归属/描述，上限 maxSkillPromptChars），模型按需 read_skill / use_skill。
// 读取失败不阻断 agent 启动，仅返回空串。
func (a *agentImpl) skillCatalog() string {
	if a.toolCtx == nil || a.toolCtx.Skills == nil {
		return ""
	}
	list, err := a.toolCtx.Skills.ListInjected(context.Background())
	if err != nil {
		return ""
	}
	// 清单为空时不能直接返回空串：开着「可发现未绑定的技能」的 agent 反而最需要这句指引
	// —— 它没有技能可注入，只能靠 list_skills 去找（那正是该开关的适用场景）。
	if len(list) == 0 {
		if a.skillDiscovery {
			return "你还未绑定任何技能。如需其它技能，可用 list_skills 查看全部可见技能；要用某个技能请 use_skill（只查看内容用 read_skill）。"
		}
		return ""
	}

	var b strings.Builder
	b.WriteString("已绑定给本 agent 的 skills（这里只有名字与描述，正文与工具都按需取）：\n" +
		"  - 要按某个技能做事：先 use_skill <名> —— 它返回正文，并让该技能声明的工具从下一轮起可用；\n" +
		"  - 只想看看内容：用 read_skill <名> —— 不会激活任何工具。\n")
	truncated := false
	for _, s := range list {
		scope := "私有"
		if s.Scope == tools.SkillScopeTenant {
			scope = "共享"
		}
		// 注：废弃的 skill 不会出现在这里 —— ListInjected 已过滤 status=3。
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "无描述"
		}
		line := fmt.Sprintf("- %s [%s]: %s\n", s.Name, scope, desc)
		if b.Len()+len(line) > maxSkillPromptChars {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("...（更多已绑定 skill 已省略，可用 list_skills 查看全部）")
	}
	// 开启发现能力时，明确告诉模型"还有别的技能可以查"，否则它不会主动调用 list_skills。
	// 截断分支已经提过一次 list_skills，就别再重复。
	if a.skillDiscovery && !truncated {
		b.WriteString("\n如需其它技能，可用 list_skills 查看全部可见技能。")
	}
	return strings.TrimRight(b.String(), "\n")
}

// callableCatalog 生成「本 agent 可调用的 agent 清单」（name + description）；无则返回空串。
// 口径与 caller.go 的授权校验同源（agent_relations 出边 ∩ 可运行 ∩ 非超级智能体），
// 因此模型不会去填一个必然被拒的名字。渐进式披露：只给名字与描述。
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
	b.WriteString("可调用协作的 agent（需要时用 call_agent，参数 agent_name）：\n")
	for _, info := range list {
		desc := strings.TrimSpace(info.Description)
		if desc == "" {
			desc = "无描述"
		}
		line := fmt.Sprintf("- %s: %s\n", info.Name, desc)
		if b.Len()+len(line) > maxAgentPromptChars {
			b.WriteString("...（更多可调用 agent 已省略）")
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *agentImpl) defaultSystemPrompt() string {
	return fmt.Sprintf("你是 agent %s，请根据用户需求，调用可用工具完成任务。", a.instance.Name)
}

// 会话记忆的上限：读最近 50 轮、合计不超过 256 KiB；写回只保留最近 100 轮
// （与改造前 Web 对话的保留策略一致）。
const (
	maxSessionHistoryRecords = 50
	maxSessionHistoryBytes   = 256 << 10
	maxSessionRecordKeep     = 100
)

// userContent 提取用户输入（优先 args 字段）。
func (a *agentImpl) userContent() string {
	// 入口显式给了「喂给模型的这一条」就以它为准：Web 对话会把附件清单拼在这条上，
	// 而落库用的是原文（_record_input），两者不能混。
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

// loadSessionHistory 读本 agent 在本次会话里的历史（旧→新）。
//
// owner 用当前 agent：父子共用同一个 session_id 时各读各的线，互不污染；
// 取最近 N 轮（ListBySessionPage 是倒序取再翻正），不是最早 N 轮 ——
// 长会话里最早那些轮次对当前这轮几乎没有价值。
func (a *agentImpl) loadSessionHistory() []gateway.Message {
	sid, uid := a.sessionMeta()
	if sid == "" || uid == "" {
		return nil
	}
	recs, err := sessionrecords.NewRepository().ListBySessionPage(
		a.schema, models.SessionOwnerTypeAgent, a.instance.ID, sid, maxSessionHistoryRecords, nil)
	if err != nil {
		xlog.Error("读取会话历史失败 session=%s: %v", sid, err)
		return nil
	}
	return core.RecordsToMessages(trimHistoryByBudget(recs, maxSessionHistoryBytes))
}

// trimHistoryByBudget 从最新往旧累加字节预算，超出后丢掉更旧的记录。
//
// 最新一条即使在预算之外也保留：紧邻当前轮的上下文比「总量一定不超」更重要，
// 全丢掉会让模型完全接不上刚才那一轮。
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

// recordSessionRound 把本轮写进 session_records，并刷新会话标题/时间。
//
// 无会话（session_id 为空）时不写：那是一次性 run，目录跑完就清，记下来也没有归属。
// 写入失败只记日志：记忆丢失不该让一次已经跑完的调用报失败。
func (a *agentImpl) recordSessionRound() {
	sid, uid := a.sessionMeta()
	if sid == "" || uid == "" || a.toolCtx == nil {
		return
	}
	if !a.startedAt.IsZero() {
		// 与 GetDurationMs 互斥：入口可能在别的 goroutine 取本轮耗时。
		a.mu.Lock()
		a.elapsedMs = time.Since(a.startedAt).Milliseconds()
		a.mu.Unlock()
	}
	input, _ := json.Marshal(map[string]string{"content": a.recordInputText()})
	// 结果状态：被终止 / 出错 / 正常完成。
	// 没有这一列时，历史里失败的一轮就是「一行空白回答」，用户无法区分是自己
	// 点了停止、模型挂了、还是工具报错 —— 只能来问人。
	errMsg := a.GetError()
	status := models.SessionStatusOK
	switch {
	case a.stopped.Load():
		status = models.SessionStatusStopped
	case errMsg != "":
		status = models.SessionStatusFailed
	}
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
	if err := sessionrecords.NewRepository().Append(a.schema, rec, maxSessionRecordKeep); err != nil {
		xlog.Error("保存会话记录失败 session=%s: %v", sid, err)
		return
	}

	// 刷新会话：title 为空则取首条输入截断；否则仅刷新 updated_at
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

// sessionMeta 本次运行是否绑定会话：返回会话 id 与归属用户（都非空才写记忆）。
func (a *agentImpl) sessionMeta() (sid, uid string) {
	if a.toolCtx == nil {
		return "", ""
	}
	sid = strings.TrimSpace(a.toolCtx.SessionID)
	uid, _ = a.args["user_id"].(string)
	return sid, strings.TrimSpace(uid)
}

// recordInputText 落库用的用户输入正文：入口可显式指定（Web 对话传原始消息，
// 而喂给模型的那条带附件清单），没指定就退回当前提示词。
func (a *agentImpl) recordInputText() string {
	if s, ok := a.args["_record_input"].(string); ok && s != "" {
		return s
	}
	return a.userContent()
}

// GetDurationMs 本轮执行耗时。写记录时冻结，入口读到的值与落库值一致。
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

// stringArg 取字符串参数（去空白，空串回落缺省值）。
func stringArg(m map[string]any, key, def string) string {
	if s, ok := m[key].(string); ok {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return def
}

// toOpenAIToolCalls 把 gateway.ToolCall 转成 OpenAI 格式（回填 assistant 消息用）。
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

// marshalToolResult 把工具结果序列化成字符串（回填 tool 消息）。
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

// pushEvent 推送结构化事件（非阻塞；缓冲满则丢弃并计数）。
// 所有流式消费方（mcp / rpc / flow / admin SSE）统一读这个 channel：
// 思考、正文、工具调用都在这里，不再有单独的纯文本进度通道。
//
// 发送必须在**持锁**状态下完成：CloseEventChan 在同一把锁里 close，
// 「锁内取 ch → 解锁 → 对方 close → 本侧 send」会直接 panic: send on closed channel。
// 因为发送本身是非阻塞的（select/default），持锁不会挡住关闭方。
func (a *agentImpl) pushEvent(ev core.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eventCh == nil {
		return // 已 Close：运行结束后到达的事件直接丢，不 panic
	}
	select {
	case a.eventCh <- ev:
	default: // 缓冲满则丢弃，避免阻塞 agent 循环；丢弃数在 Stream 结束时上报
		a.dropped.Add(1)
	}
}

// ---- core.Agent 接口实现 ----

func (a *agentImpl) Close() error {
	a.cleanupEphemeralWorkspace()
	return a.CloseChans()
}

// cleanupEphemeralWorkspace 删除无 session 调用的临时工作目录。
//
// 有会话的场地属于会话本身（可续、可查），绝不能在这里删；只有 runs/ 下的一次性
// 场地才随实例销毁而清理。进程启动时的兜底清扫见 CleanupOrphanRuns。
func (a *agentImpl) cleanupEphemeralWorkspace() {
	c := a.toolCtx
	if c == nil || c.SessionID != "" {
		return // 有 session：场地与长交互进程都归 session 自身
	}
	// 快路径：无 session 的交互进程随 run 同生共死，这里显式销毁；
	// sandboxd 的短空闲超时只是「nexus 崩溃/漏发」时的兜底。
	tools.KillInteractiveRun(c.RunID)
	if c.WorkKey == "" || c.Storage == nil {
		return
	}
	if err := c.Storage.DeletePrefix(context.Background(), c.WorkKey); err != nil {
		xlog.Error("清理临时工作目录失败 key=%s: %v", c.WorkKey, err)
	}
}

// killInteractiveOnStop 终止运行后回收 agent 拉起的常驻进程（browser / shell）。
//
// 与 cleanupEphemeralWorkspace 的分工：
//   - 这里只在「被终止」时触发，且**会话级也收** —— 终止意味着不要再占资源；
//   - cleanupEphemeralWorkspace 在正常结束时只收无 session 的临时进程，
//     会话级进程留给下一轮对话复用（页面状态不丢）。
//
// key 的算法与 shell / browser 工具完全一致（见 tools.KillInteractive）。
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

// GetEventChan 返回结构化事件 channel（思考 / 正文 / 工具调用）。
func (a *agentImpl) GetEventChan() chan core.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.eventCh
}

// CloseEventChan 关闭事件 channel（实现内部用；不进 core.Agent 接口）。
func (a *agentImpl) CloseEventChan() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eventCh != nil {
		close(a.eventCh)
		a.eventCh = nil
	}
}

func (a *agentImpl) GetResult() string { a.mu.Lock(); defer a.mu.Unlock(); return a.result }

// SetResult 写入最终结果（实现内部用；不进 core.Agent 接口）。
func (a *agentImpl) SetResult(r string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.result = r
}

func (a *agentImpl) GetError() string { a.mu.Lock(); defer a.mu.Unlock(); return a.errMsg }

// SetError 记录执行错误（实现内部用；不进 core.Agent 接口）。
func (a *agentImpl) SetError(e string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errMsg = e
}

// LogInfo 记录信息日志（实现内部用；不进 core.Agent 接口）。
func (a *agentImpl) LogInfo(msg string) { xlog.Info("[agent:%s] %s", a.instance.Name, msg) }

func (a *agentImpl) LogError(msg string) { xlog.Error("[agent:%s] %s", a.instance.Name, msg) }
