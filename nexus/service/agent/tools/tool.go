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

// DefinitionMode 定义域解析模式：一次运行读草稿还是读已发布的当前版本。
//
// 两种模式只影响「读定义」（AGENT.md / SKILL.md / 技能脚本目录 / 工具脚本目录）；
// 写路径永远走草稿 —— 编辑就是在改草稿，没有「编辑某个历史版本」这回事。
type DefinitionMode string

const (
	// ModeDraft 读草稿目录（编辑页、调试器、试聊）。零值即此。
	ModeDraft DefinitionMode = "draft"
	// ModeCurrent 读 is_current 那一版（正式入口：RPC / MCP / 端点 / flow 正式运行）。
	// 没有当前版本时返回 versions.ErrNoReleasedVersion，**不退回草稿**。
	ModeCurrent DefinitionMode = "current"
)

// IsCurrent 是否在读已发布版本。
func (m DefinitionMode) IsCurrent() bool { return m == ModeCurrent }

// DefaultMaxOutput 命令输出默认字节上限（64KB）。
const DefaultMaxOutput = 64 * 1024

// globalSandbox 命令执行沙箱客户端（进程级单例，启动时由 main 注入）。
var globalSandbox sandbox.Sandbox

// SetSandbox 设置全局命令沙箱（启动时调用）。
func SetSandbox(sb sandbox.Sandbox) { globalSandbox = sb }

// CurrentSandbox 返回当前生效的沙箱客户端。
//
// 未注入时返回 nil，由调用方报错 —— 不再静默降级为「无隔离直通」，
// 避免配置缺失时命令以服务进程权限裸跑。
func CurrentSandbox() sandbox.Sandbox { return globalSandbox }

// globalInteractive 长交互（/v1/interactive）客户端（进程级单例，启动时由 main 注入）。
var globalInteractive *sandbox.InteractiveClient

// SetInteractive 设置全局长交互客户端（启动时调用）。
func SetInteractive(c *sandbox.InteractiveClient) { globalInteractive = c }

// CurrentInteractive 返回当前生效的长交互客户端；未注入时返回 nil。
func CurrentInteractive() *sandbox.InteractiveClient { return globalInteractive }

// interactiveToolIDs 走 /v1/interactive 的内置工具（session 级常驻进程）。
//
// 它们是并列的：同一个 session 在 sandboxd 的注册表里各占一个 tool 条目
// （docker 下共享同一个容器），回收时必须逐个杀，漏一个就留一个进程。
var interactiveToolIDs = []string{"shell", "browser"}

// KillInteractive 销毁交互进程，key 的算法必须与 shell / browser 工具一致：
// SessionID 优先（会话级，可续用），RunID 兜底（临时，随 run 结束）。
//
// 运行被终止时用它收掉 agent 拉起的常驻进程；正常结束**不该**调它 ——
// 会话级浏览器是给下一轮对话复用的，提前杀会丢页面状态。
//
// 兜底由 sandboxd 的空闲超时负责，失败只影响回收速度、不影响正确性，
// 所以这里静默吞掉错误。
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

// KillInteractiveRun 销毁某个 run 的交互进程（无 session 时 run 结束的快路径）。
func KillInteractiveRun(runID string) { KillInteractive("", runID) }

// 工具分类（对应天然组 builtin:<Category>）：用于工具授权体系的分组授权与面板展示。
// 每个内置工具都**应显式标注**分类；未标注的不会出现在任何类别组里
// （仍可按单工具 ref 授权，见 toolgrant.BuiltinTools 的 Source 处理）。
//
// 分类就是**权限面**：一个组能被授给谁，由它是不是 supervisorOnlyCategories 决定；
// 所以切分类必须慎重 —— 把一个非超级智能体工具挪进 *-admin 组等于收回它的授权能力，
// 反过来则是提权。
//
// 十三个组的分工（「谁能拿」见 toolgrant.Catalog 的 isSupervisor 视角）：
//
//	file / exec / shell / browser / agent / skill      普通能力，任何 agent 都可授
//	skill-own                                         私有技能三件套，任何 agent 可授但默认不授
//	agent-admin / skill-admin / tool-grant / custom-tool / flow-admin
//	                                                  仅超级智能体（管理面）
//
// 早先这 19 个管理类工具共用一个 CategorySuper；拆成五组是为了让「按需授权」
// 能精确到一类能力（见 models.SupervisorCoreGrants 与 docs/workspace-layout-and-supervisor-power.md §2.4）。
const (
	CategoryFile    = "file"
	CategoryExec    = "exec"
	CategoryShell   = "shell"
	CategoryScript  = "script"
	CategoryBrowser = "browser"
	CategoryAgent   = "agent"
	CategorySkill   = "skill"
	// CategorySkillOwn 私有技能三件套（create/update/delete_own_skill）。
	// 与 skill 分家的理由：skill 是「看技能」，它是「写自己的能力面」，
	// 风险档次不同（D20 特意不放进 DefaultAgentTools），放同一组没法分开授权。
	CategorySkillOwn = "skill-own"

	// 以下五组仅超级智能体（supervisor）可用（判据见 supervisorOnlyCategories）。
	// 注意：判据是 agents.is_supervisor 属性，不是名字等于 main —— main 只是预置的那个 supervisor。
	CategoryAgentAdmin = "agent-admin" // 建/改/删 agent + 技能绑定
	CategorySkillAdmin = "skill-admin" // 建/改/删共享技能 + 他 agent 的私有技能
	CategoryToolGrant  = "tool-grant"  // 工具授权 + 调用授权
	CategoryCustomTool = "custom-tool" // 自定义工具的建/改/删/列
	// CategoryFlowAdmin 工作流的建 / 改 / 删 / 列 + 试运行 / 运行记录 + 端点对外开放。
	// 与 agent-admin 同级：建工作流等于改租户的对外能力面。
	// 只读也归这一组 —— 工作流没有 agent 那种「发现协作者」的场景，
	// 普通 agent 拿到 list/read 只会多一份看不见用途的授权，不如整组收口。
	CategoryFlowAdmin = "flow-admin"

	// CategorySuper 是**过渡别名**：四组拆分前唯一的「超级智能体专属」分类。
	// 现在没有任何工具属于它，只为兼容存量授权行（builtin:super）保留 ——
	// toolgrant 会把它展开成四组的并集。等存量清干净后可以删掉（见 TODO）。
	CategorySuper = "super"
)

// supervisorOnlyCategoryList 「仅超级智能体可用」的分类（顺序即目录展开顺序）。
// 真源在这里，map 由它派生 —— 只有一份清单，不会出现「列表漏了但 map 有」。
//
// 新增一个管理类分类时必须加进本列表，否则它的工具对非超级智能体也会出现在授权目录里
// （执行层还有一道硬闸，但目录先露出来就等于把权限承诺给了管理员）。
var supervisorOnlyCategoryList = []string{
	CategoryAgentAdmin,
	CategorySkillAdmin,
	CategoryToolGrant,
	CategoryCustomTool,
	CategoryFlowAdmin,
}

// supervisorOnlyCategories 「仅超级智能体可用」的分类集合 —— 权限边界的**唯一判据**。
var supervisorOnlyCategories = func() map[string]bool {
	m := make(map[string]bool, len(supervisorOnlyCategoryList))
	for _, c := range supervisorOnlyCategoryList {
		m[c] = true
	}
	return m
}()

// SupervisorOnlyCategories 仅超级智能体可用的分类（稳定顺序）。供 builtin:super 的并集展开用。
func SupervisorOnlyCategories() []string { return supervisorOnlyCategoryList }

// IsSupervisorOnlyCategory 该分类是否仅超级智能体可用。
func IsSupervisorOnlyCategory(category string) bool { return supervisorOnlyCategories[category] }

// IsSupervisorOnlyTool 判断某内置工具是否仅超级智能体可用。
// 唯一判据 —— 工具在 Meta() 里标的分类落在 supervisorOnlyCategories 里。
func IsSupervisorOnlyTool(name string) bool {
	t, ok := Get(name)
	return ok && supervisorOnlyCategories[t.Meta().Category]
}

// CapabilityOf 工具分类 → 所属能力（空 = 不受平台能力策略约束）。
//
// 按**进程形态**划档，不按「能跑什么」：exec / shell / script 三档都能执行任意代码
// （脚本里 os.system 与 exec 等价），拆开的理由是生命周期与资源闸不同，不是权限边界。
//
// 早期的口径是「三合一 = sandbox.exec」（理由：只掐 shell 不掐 exec 等于没闸），
// 那个论证在权限维度上仍然成立，所以这里不再声称任何一档是「最小权限」。
//
// 注意 CategoryScript 当前**没有内置工具归属**：唯一的 run_skill_script 走 /v1/exec
// （一次性进程、无池），已归 exec 档。sandbox.code 是工作流 code 节点的闸，判据在
// flow/executors 侧（ensureCodeCapability），不经工具分类。这条映射保留，是为了
// capability ↔ category 一一对应，不因当下没有工具使用就把分支删掉。
//
// 租户自定义工具（custom__*）同理不经分类（它不在注册表里，没有 ToolMeta），
// 能力判据硬写在 tools/executeCustomTool 里，同样归 exec 档。
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

// ToolMeta 是 LLM 可见的工具定义（与 xllm.Tool 对齐）。
type ToolMeta struct {
	Name        string
	Category    string // 工具分类：file / exec / agent / skill（空 = 归入 other）
	Description string
	Parameters  map[string]any // JSON Schema object
}

// Tool 内置工具接口。
type Tool interface {
	Meta() ToolMeta
	// Execute 执行工具；args 为 LLM 传入参数；返回结构化结果（由上层序列化）。
	Execute(ctx context.Context, c *Context, args map[string]any) (any, error)
}

// PluginRunner 执行「磁盘插件」工具（由 main 注入；nil = 插件工具不可用）。
//
// 为什么是接口：插件工具的名字不注册进本包的工具表（它们是运行时发现的），
// 但 ExecuteTool 必须能把调用转出去。本包只认「名字 → 执行」，
// 不该知道插件目录、Python 进程、租户配置这些细节 —— 那些在 service/plugin。
type PluginRunner interface {
	// Has 判断某名字是不是已发现的插件工具（只看 manifest，不碰租户配置）。
	Has(name string) bool
	// Invoke 执行一次调用；schema 用于定位租户配置（哪个租户启用了哪个实现、key 是什么）。
	Invoke(ctx context.Context, schema, toolName string, args map[string]any) (any, error)
}

// globalPlugins 进程级插件执行器（启动时由 main 注入，与 SetSandbox 同一套惯例）。
var globalPlugins PluginRunner

// SetPluginRunner 设置全局插件执行器（启动时调用）。
func SetPluginRunner(r PluginRunner) { globalPlugins = r }

// CurrentPluginRunner 返回当前生效的插件执行器；未注入时为 nil。
func CurrentPluginRunner() PluginRunner { return globalPlugins }

// pluginRunner 取本上下文应使用的插件执行器：Context 上显式注入的优先（便于单测）。
func (c *Context) pluginRunner() PluginRunner {
	if c.Plugins != nil {
		return c.Plugins
	}
	return globalPlugins
}

// AgentInfo agent 实例的简要信息（list_agents 返回）。
type AgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      int    `json:"status"` // 1:正常 3:废弃（见 models.StatusXxx）
	// IsSupervisor 是否带超权属性（agents.is_supervisor）。
	// read_agent 展示它；调用授权不再据此排除 —— supervisor 可被调用，
	// 被调起时 resolveDefinitionMode 会强制它读草稿。
	IsSupervisor bool `json:"is_supervisor"`
	// 两个 agent 级开关（agents 表同名列），随实例一并带出，供 read_agent 展示现状：
	//
	//	SkillDiscovery   ：是否允许发现未绑定的技能
	//	ToolsUnrestricted：是否「不限制工具」（候选不被 allow 收窄）
	SkillDiscovery    bool `json:"skill_discovery"`
	ToolsUnrestricted bool `json:"tools_unrestricted"`
}

// AgentManager agent 实例的注册与查询（供元工具 create_agent / list_agents 使用）。
// 由 agent 运行时注入，实现内部自带租户 schema 与 repository。
type AgentManager interface {
	// Create 创建 agent 实例。isSupervisor=true 时按 supervisor 起步套件物化
	// （core 工具组 + 不限制工具 + 技能发现 + 四份 authoring 技能），
	// 与「创建后 SetSupervisor(true)」得到的结果一致（同一套 helper）。
	Create(ctx context.Context, name, description string, isSupervisor bool) (string, error)
	List(ctx context.Context) ([]AgentInfo, error)
	Exists(ctx context.Context, name string) (bool, error)
	// Get 按 name 精确取一个 agent 的简要信息（含 id/status）；不存在返回 (nil, nil)。
	// 供跨域写闸门（tools/gate.go）读取目标状态与归属 id。
	Get(ctx context.Context, name string) (*AgentInfo, error)
	// Update 按 name 改名/改描述（改名同步迁移定义域目录），返回最终名。
	Update(ctx context.Context, name, newName, description string) (string, error)
	// SetUnrestricted 设置某 agent（按 uuid）的「不限制工具」开关（agents.tools_unrestricted）。
	// 它是 agent 自身的属性，与工具授权项（agent_tool_grants）无关；仅超级智能体可改，
	// 且可以是自己（D5 修订：边界由 status 承担，改 status 只有管理端能做）。
	SetUnrestricted(ctx context.Context, agentID string, unrestricted bool) error
	// SetSupervisor 设置某 agent（按 uuid）的超级权限属性（agents.is_supervisor）。
	// 仅 supervisor 可改，可以是自己；降级「最后一个 supervisor」会被仓储拒绝。
	// 目标须处于正常态（废弃不接受属性变更）。
	SetSupervisor(ctx context.Context, agentID string, isSupervisor bool) error
	// Delete 按 name 删除 agent（同时清理其定义域目录）。
	Delete(ctx context.Context, name string) error
}

// CallRequest 一次 agent 调用的请求。
type CallRequest struct {
	CallerID   string         // 发起方 agent uuid（权限判断）
	CallerName string         // 发起方 name（错误信息展示）
	CalleeName string         // 目标 agent name
	Args       map[string]any // 传给目标 agent 的参数
	Depth      int            // 当前调用深度（被调方在 Depth+1 上运行）
	UserID     string         // 当前用户 uuid（透传给被调方）
	SessionID  string         // 当前会话 id（透传给被调方，子 agent 与父共用同一场地）
	// Chain 调用方当前的祖先链（不含被调方）：caller 追加「调用方自己」后下传，
	// 被调方据此判断是否成环。与 Depth 同一套「派生而非累加」的下传方式。
	Chain []string
	// DefinitionMode 本次运行的读定义模式，**透传给被调方**：
	// 父在正式运行 → 子也读已发布版本；父在调试 → 子读草稿。
	// 不透传会出现「调试时子 agent 跑的是线上版本」，排查时最容易被这一点骗。
	DefinitionMode DefinitionMode
}

// AgentCaller 调用其他 agent（由 service/agent 包实现并注入，避免包循环依赖）。
type AgentCaller interface {
	Call(ctx context.Context, req CallRequest) (string, error)
	// Callable 列出 caller 当前可调用的 agent（name + description），供注入 system prompt。
	// 口径与 Call 的授权校验同源：agent_relations 出边有记录（无行 = 拒绝）∩ 可运行 ∩ 非超级智能体。
	Callable(ctx context.Context, callerID string) ([]AgentInfo, error)
}

// AgentGrantView 一条 agent 工具授权的只读视图（read_agent 展示现状用）。
//
// Effective 不是猜的：实现方复用运行时的 Resolver（与 agentImpl.resolveGrantedToolNames、
// 管理端 GetAgentEffectiveTools 同一套），所以「超级智能体看到的」与「agent 实际拿到的」不会漂移。
type AgentGrantView struct {
	Kind      string `json:"grant_kind"` // required | allow
	Ref       string `json:"ref"`
	ItemType  string `json:"item_type"` // group | tool
	Effective bool   `json:"effective"` // required 恒生效；allow 在 run 起点未激活
	// Pending allow 已被某个技能声明、但该技能本轮还没 use_skill：激活后即生效。
	Pending  bool   `json:"pending,omitempty"`
	ViaSkill string `json:"via_skill,omitempty"` // 撑起它的 skill 名（pending 时 = 用哪个技能会亮）
	Note     string `json:"note,omitempty"`      // 空转 / 未激活的说明与补救建议
}

// ToolGrantsManager agent 工具授权的读写（由 service/agent 包实现并注入）。
//
// 为什么校验不在本包：toolgrant 包 import 了 tools 包（用它取内置工具与类别），
// 反向 import 会成环 —— 所以 ref 校验（Catalog / ValidateRef）与 allow / required 的
// 分配逻辑都落在实现方，本包只定义接口与只读视图。
type ToolGrantsManager interface {
	// ListAgentGrants 列出某 agent 的授权项及其当前生效状态（以 run 起点为准）：
	// required 恒生效；allow 只有被本轮 use_skill 激活的技能声明才生效，
	// 否则标 pending（用哪个技能会亮）或空转（永不生效）。
	ListAgentGrants(ctx context.Context, agentName, agentID string) ([]AgentGrantView, error)
	// GrantAgentTool 给某 agent 赋一条授权（幂等：同 ref 覆盖）。
	// kind 传空表示「交给实现方判定」：目标已注入的 skill 声明该依赖 → allow，否则 required。
	// 显式传 required / allow / deny 时按语义校验（allow 必须有声明者，否则该行永不生效）。
	// 返回实际写入的 kind 与理由（理由会说明是否覆盖了旧行）。
	GrantAgentTool(ctx context.Context, agentName, agentID, ref, kind string) (kindOut, reason string, err error)
	// RevokeAgentTool 按 ref 移除某 agent 的一条授权（幂等），返回删除行数与那一行的 kind。
	// 刻意不校验 ref 是否存在：源已消失（连接/端点被删）的悬空授权必须还能收回。
	RevokeAgentTool(ctx context.Context, agentID, ref string) (removed int64, kind string, err error)
}

// ToolSearchItem 目录里的一项：一个可授权的组，或一个可授权的单个工具。
//
// Ref 是**直接可粘进 grant_agent_tool / revoke_agent_tool 的授权 ref**
// （mcp:fs / mcp:fs:read_file / builtin:file），不是模型侧工具名 ——
// 这个工具的用途是「查了去授权」，不是「查了去调用」。
type ToolSearchItem struct {
	Ref   string `json:"ref"`
	Name  string `json:"name,omitempty"` // 模型侧工具名（仅单工具）：mcp__fs__read_file
	Label string `json:"label"`
	Kind  string `json:"kind"` // builtin | mcp | rpc | local | plugin | script | group | preset
	// Description 原样来自工具/组定义（可能为空）。
	Description string `json:"description,omitempty"`
	// Count 组形态的成员数；单工具恒为 0。
	Count int `json:"count,omitempty"`
	// Members 组形态的成员授权 ref（builtin:read_file / mcp:fs:query …）；单工具恒为空。
	//
	// 为什么给模型看：授权是对 ref 的操作，而「一个叫 fs-basic 的组里到底有什么」既不在
	// 工具名里也不在描述里 —— 不给成员摘要，模型只能盲授整组，事后才发现多开或少了。
	// 只列前若干条（Count 仍是真实总数）；要看全量用 query 匹配展开。
	Members   []string `json:"members,omitempty"`
	Available bool     `json:"available"`
	// UnavailableKind/Reason 不可用的归因（取值见 toolgrant.Unavailable*）：
	// 平台未开通 / 连接未启用 / 端点未对外开放，修复入口各不相同。
	UnavailableKind   string `json:"unavailable_kind,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// SupervisorOnly 仅超级智能体可绑定 / 执行。
	SupervisorOnly bool `json:"supervisor_only,omitempty"`
	// Granted 该 ref 在目标 agent 上已授权的 kind（required / allow / deny；空 = 未授权）。
	// 只在 Search 传了目标 agent 时回填，用于避免重复授权、并让「已经授过什么」可见。
	Granted string `json:"granted,omitempty"`
}

// ToolSearchResult 一次目录检索的结果。
type ToolSearchResult struct {
	Query     string           `json:"query"`
	Groups    []ToolSearchItem `json:"groups"`
	Tools     []ToolSearchItem `json:"tools"`
	Truncated bool             `json:"truncated,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
}

// ToolCatalogManager 可授权工具目录的只读检索（供 search_tools 用；nil = 该能力不可用）。
//
// 为什么要注入而不是直接在 tools 包实现：toolgrant 包 import 了本包（取内置工具与分类），
// 反向 import 会成环 —— 与 ToolGrantsManager 同一个原因。
type ToolCatalogManager interface {
	// Search 按关键字检索可授权的组与工具。
	//
	// agentName/agentID 是**目标** agent（可能与调用者不同）：目录按它的身份生成
	// （「仅超级智能体可用」的工具不会出现在非超级智能体的候选里 —— 授了也执行不了，
	// 露出来只会诱导一次必然失败的授权），并回填它已授权的状态。
	Search(ctx context.Context, query, agentName, agentID string) (*ToolSearchResult, error)
}

// AgentRelationsManager agent 间调用授权（agent_relations 出边）的读写（由 service/agent 实现）。//
// 与 caller.go 同源：授权靠 agent_relations 里的一行，所有 agent 一视同仁（超级智能体也无豁免）。
// 允许超级智能体给自己加出边（见实现方注释）—— 它扩的是「超级智能体能调谁」，不是它自己的工具面。
type AgentRelationsManager interface {
	// AllowCall 追加一条「caller 可调用 callee」的授权（幂等）。
	AllowCall(ctx context.Context, callerID, calleeID string) error
	// RevokeCall 移除一条调用授权（幂等），返回删除行数。
	RevokeCall(ctx context.Context, callerID, calleeID string) (int64, error)
	// ListRelations 列出某 agent 的出边（能调用谁）与入边（谁能调用它），返回 agent 名。
	ListRelations(ctx context.Context, agentID string) (canCall, calledBy []string, err error)
}

// Skill 归属范围。
const (
	SkillScopeTenant = "tenant" // 租户级（agent_id 为 NULL，所有 agent 可见）
	SkillScopeAgent  = "agent"  // agent 私有（agent_id = 当前 agent）
)

// SkillInfo skill 简要信息（list_skills 返回）。
type SkillInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`  // tenant / agent
	Status      int    `json:"status"` // 1:正常 3:废弃（跨域写闸门读它）
	// Restricted 仅对租户级共享 skill 有意义：true = 只有白名单内 agent 可见（私有 skill 恒 false）。
	Restricted bool `json:"restricted"`
	// BoundAgents 该 skill 的白名单 agent 名（= agent_skill_bindings 的 skill 侧视角）。
	// 仅超级智能体视角回填（非超级智能体无需、也不该看到他人的绑定情况）；私有 skill 恒为空。
	BoundAgents []string `json:"bound_agents,omitempty"`
}

// SkillDetail skill 完整信息（read_skill / use_skill 返回），含内容。
// Restricted / BoundAgents 由内嵌的 SkillInfo 承载，不在此重复声明（避免同名字段遮蔽）。
type SkillDetail struct {
	SkillInfo
	Content string `json:"content"`
	// ToolDeps 该 skill 声明的工具依赖，**原样 ref**（如 builtin:exec / mcp:fs:read_file）。
	//   - 不解组：展示的是定义原貌，展开结果（叶子工具）随授权/连接变化，属 Resolver 的活；
	//   - 不剔除悬空项：工具被删后留下的死 ref 照原样给，反而利于诊断（revoke 仍能清掉）；
	//   - read_skill 只展示，use_skill 展示并激活（激活仍被 allow 收窄，见 D28）。
	// 恒为数组（无依赖时 []），避免 null 与「读不到」混淆。
	ToolDeps []string `json:"tool_deps"`
}

// SkillDepChange grant_skill_tool / revoke_skill_tool 的返回：这条变更 + 变更后的完整依赖。
//
// 回带 ToolDeps 是为了让「增量改完是什么样」当场可见：模型不必再 read_skill 一次
// 才能确认结果，也就不会在下一步继续盲改。
type SkillDepChange struct {
	Skill    string   `json:"skill"`
	SkillID  string   `json:"skill_id,omitempty"`
	Ref      string   `json:"ref"`
	Added    bool     `json:"added,omitempty"`
	Removed  int64    `json:"removed,omitempty"`
	ToolDeps []string `json:"tool_deps"`
}

// SkillRef 定位一个 skill。
//
// 优先级：ID → Scope（空间定向）→ Name（当前 agent 可见口径）。
//
//	ID   ：全局唯一，可跨归属；无 Scope 时属「定向定位」，仅超级智能体可用；
//	Scope：由工具本身声明在哪个空间里找（own 系列 = agent / shared 系列 = tenant），
//	       工具名即口径，模型传不进来 —— 也就不存在手滑把共享 skill 当成自己私有改掉的可能；
//	皆空 ：走可见口径（read_skill 用）。
type SkillRef struct {
	ID    string // 非空：按 id 定位
	Name  string // skill 名
	Scope string // agent=私有空间；tenant=租户级共享；空=当前 agent 可见口径
	// OwnerID 私有空间的**归属 agent uuid**（空 = 当前 agent）。仅 Scope=agent 时有意义：
	// 非空表示「指定归属」，仅供 *_agent_skill 系列（仅超级智能体）使用。
	OwnerID string
}

// SkillInput 创建/更新 skill 的入参。
type SkillInput struct {
	ID          string // 定向定位：非空按 id；无 Scope 时仅超级智能体可用
	Name        string // 当前名（更新时定位用）
	NewName     string // 改名时的新名字（可选，空表示不改名）
	Description string
	Content     string
	// OwnerID 目标归属 agent uuid（空 = 当前 agent 私有）。仅 Scope=agent 时有意义；
	// 非空表示「替该 agent 建/改它的私有技能」，仅供 *_agent_skill 系列（仅超级智能体）传入。
	OwnerID string
	// ToolDeps 该 skill 声明的工具依赖（ref 列表：builtin:exec / builtin:file / mcp:fs ...）。
	// nil = 不改动（创建 = 不声明；更新 = 保持原样）；非 nil 且为空 = 清空。
	//
	// 仅超级智能体可设置：按「按需注入」口径，一条 allow 授权只有被某个**已注入 skill**
	// 声明过才会生效（见 toolgrant.Resolve），所以谁能写依赖，谁就握住了授权开关。
	// 只有 *_agent_skill / *_shared_skill 系列（分类 skill-admin）会传。
	ToolDeps *[]string
	Scope    string // 目标空间：tenant=租户级共享 / agent=私有空间；创建时空视为 agent
	// Restricted 可见性开关（nil = 不改动）：true = 仅白名单内 agent 可见，false = 全员可见。
	// 仅对**租户级共享** skill 有意义（私有 skill 的可见性由 AgentID 决定，传了会被拒）。
	// 只有 *_shared_skill 系列会传（见 withRestricted）；白名单由 bind_skill / unbind_skill 维护。
	Restricted *bool
}

// SkillsManager skill 的注册与查询（供元工具使用）。
// 由 agent 运行时注入，实现内部固化当前 agent 身份，工具层无法传入，天然防越权。
type SkillsManager interface {
	Create(ctx context.Context, in SkillInput) (string, error)
	Update(ctx context.Context, in SkillInput) error
	List(ctx context.Context) ([]SkillInfo, error)
	// ListInjected 列出「注入给当前 agent」的 skill（私有 + 已绑定），
	// 用于 system prompt 的常驻清单：只给 name/description，正文按需 read_skill。
	ListInjected(ctx context.Context) ([]SkillInfo, error)
	Get(ctx context.Context, ref SkillRef) (*SkillDetail, error)
	// FindForWrite 按「归属 + 名」查 skill（不受当前 agent 可见性收口），供跨域写闸门
	// 判定存在性：ownerAgentID 为空表示租户级；不存在返回 (nil, nil)。
	FindForWrite(ctx context.Context, ownerAgentID, skillName string) (*SkillInfo, error)
	Delete(ctx context.Context, ref SkillRef) error
	// BindAgentSkill 给某 agent（按 uuid）追加一条 skill 绑定（幂等）；只接受租户级 skill 名。
	BindAgentSkill(ctx context.Context, agentID, skillName string) error
	// UnbindAgentSkill 移除某 agent（按 uuid）的一条 skill 绑定（幂等）；只接受租户级 skill 名。
	UnbindAgentSkill(ctx context.Context, agentID, skillName string) error
	// ListAgentSkills 列出某 agent（按 uuid）当前绑定的 skill 名（只读；供 read_agent 展示现状）。
	ListAgentSkills(ctx context.Context, agentID string) ([]string, error)
	// ListAgentPrivateSkills 列出某 agent（按 uuid）**私有** skill 的名字（只读；供 read_agent 展示现状）。
	ListAgentPrivateSkills(ctx context.Context, agentID string) ([]string, error)
	// SetAgentSkillDiscovery 设置某 agent（按 uuid）的「可发现未绑定的技能」开关。
	SetAgentSkillDiscovery(ctx context.Context, agentID string, discovery bool) error
	// AddToolDep 给某 skill 追加一条工具依赖（幂等；仅超级智能体）。
	AddToolDep(ctx context.Context, ref SkillRef, toolRef string) (*SkillDepChange, error)
	// RemoveToolDep 移除某 skill 的一条工具依赖（幂等；仅超级智能体）。
	RemoveToolDep(ctx context.Context, ref SkillRef, toolRef string) (*SkillDepChange, error)
}

// Context 提供租户/实例上下文与安全边界。
type Context struct {
	Schema    string // 租户 schema
	AgentName string // 实例 name（目录 key）
	AgentID   string // 实例 uuid（skill 归属、调用授权等用）
	// IsSupervisor 本实例是否拥有超级权限（agents.is_supervisor）。
	//
	// 身份判据的**唯一入口**：不再按名字判断（main 只是预置的一个 supervisor）。
	// 由 newAgentImpl 从实例填入；单测里可直接置 true 来模拟超权 agent。
	// 它决定：管理类工具能否执行、跨域写文件的闸门、授权目录的全量视角。
	IsSupervisor bool
	UserID       string // 当前用户 uuid（透传给被调 agent）
	SessionID    string // 会话 id（空 = 无 session 调用）
	RunID        string // 本次调用 id（无 session 时作临时目录名）
	WorkKey      string // 工作目录的存储相对路径（文件工具默认根）
	Workspace    string // 工作目录的宿主绝对路径（exec 挂 /workspace 的宿主源）
	AgentRoot    string // 本 agent 定义域的宿主绝对路径（exec 挂 /agent 的宿主源）
	SkillRoot    string // 租户级技能目录的宿主绝对路径（run_skill_script 挂 /skill 的宿主源）
	// DefinitionMode 读草稿还是读当前版本（零值 = 草稿，见 DefinitionMode）。
	//
	// 构造点必须显式选：正式入口漏设就是「还在读草稿」，版本体系就白做了。
	DefinitionMode DefinitionMode
	// AgentVersion DefinitionMode=current 时本 agent 的当前版本号（0 = 未绑定）。
	AgentVersion int
	// FollowDrafts 本次运行**调起的子实体**（agent / flow）是否跟随草稿。
	//
	// 与 DefinitionMode 分工：那个管「我自己读哪份」，这个只管「我调起来的那些读哪份」。
	// 聊天窗口把它做成开关（缺省 true，与 flow 调试器的 FollowDrafts 同名同义）；
	// 正式入口（MCP / RPC / 端点 / flow 正式运行）恒为 false —— 草稿不允许从正式链上扩散。
	// 判据唯一入口是 ChildMode，不要在调用点各算各的。
	FollowDrafts bool
	// RootDir 存储根（agent.root_dir），把存储 key 换算成宿主机路径给沙箱挂载用。
	RootDir    string
	Storage    storage.Storage   // 文件存储后端（root 为 agent.root_dir）
	Agents     AgentManager      // agent 实例管理（元工具 create_agent/list_agents 用；可为 nil）
	Skills     SkillsManager     // skill 注册与查询（元工具用；可为 nil）
	ToolGrants ToolGrantsManager // agent 工具授权读写（元工具 grant/revoke_agent_tool 用；可为 nil）
	// ToolCatalog 可授权工具目录的只读检索（元工具 search_tools 用；nil = 该工具不可用）。
	ToolCatalog ToolCatalogManager
	// Flows 工作流管理（元工具 create_flow / list_flows / read_flow / delete_flow 用；可为 nil）。
	// 与 Agents / Skills 同一套惯例：租户 schema 与当前身份固化在实现里，工具层传不进来。
	Flows     FlowsManager
	Relations AgentRelationsManager // agent 调用授权读写（元工具 allow/revoke_agent_call 用；可为 nil）
	Caller    AgentCaller           // 调用其他 agent（call_agent 用；nil = 不支持调用）
	Plugins   PluginRunner          // 磁盘插件执行器（nil = 用进程级注入的那个；都没有则插件工具不可用）
	// Externals 外部连接工具执行器（mcp__<连接>__<工具> / rpc__<连接>__<工具>）。
	// nil = 用进程级注入的那个；都没有则外部工具不可用（名字落到 ErrToolNotFound）。
	Externals ExternalRunner
	// Endpoints 本地端点工具执行器（local__<端点名>，进程内跑 flow）。
	// nil = 该能力不可用（名字落到 ErrToolNotFound）。
	Endpoints EndpointRunner
	// CustomTools 自定义工具来源（nil = 本租户没有自定义工具体系，custom__* 一律 not found）。
	CustomTools CustomToolSource
	// CustomToolsAdmin 自定义工具的增删改（元工具用；nil = 该能力不可用）。
	// 与 CustomTools 分开：那是执行热路径的只读视图，这里只有管理端语义。
	CustomToolsAdmin CustomToolAdmin
	// ToolsRoot 租户自定义工具目录的宿主绝对路径（{root}/tenants/{schema}/tools），
	// 脚本执行时以只读方式挂到 /tool。
	ToolsRoot string
	CallDepth int // 当前 agent 调用深度（顶层 0；被调方 +1）
	// CallChain 祖先链（见 shared/callchain）：进入本 agent 时由上层注入，
	// 出站（rpc__ 外部连接 / call_agent）时原样带出 —— 出站不追加自身，
	// 追加发生在「即将执行某个实体」的那一侧。
	CallChain []string
	Timeout   time.Duration     // 单次工具调用超时
	Sandbox   sandbox.Sandbox   // 命令执行沙箱（nil = 直通 none）
	Env       map[string]string // 命令执行附加环境变量
	MaxOutput int               // 命令输出字节上限（0=默认）
	// AllowedTools 本次运行允许执行的工具名集合（= 声明给模型的 tools 数组）。
	// 非 nil 时 ExecuteTool 会拒绝集合外的工具 —— 与「授权目录按身份产出」同口径。
	// 必要性：文本形态工具调用（模型把调用写进正文，如 |DSML| 标记）没经过
	// 「工具是否被授权」的校验；若不在此收口，未授权的 agent 只要写出标记就能执行任意非 super 工具。
	// nil 表示不启用该限制（仅限无授权语义的调用方，如单测）；agent 运行时恒为「非 nil」。
	AllowedTools map[string]bool
	// Caps 平台给本租户开通的沙箱能力（键见 shared/capability；缺键 = 未开通）。
	//
	// 非 nil 时 ExecuteTool 会拒绝「需要未开通能力」的工具，与目录可用性同源。
	// 必要性：能力是平台侧策略，可能在两次调用之间被收回；只靠目录置灰挡不住
	// 已经绑定它的 agent（模型仍按旧工具清单发起调用）。
	// nil 表示不启用该限制（仅限无租户语义的调用方，如单测）；agent 运行时恒为「非 nil」。
	Caps map[string]bool

	// 本轮 run 内「已激活」的技能（skillID -> name），由 use_skill 成功后登记。
	// read_skill 是纯查看，不写这里 —— 查看与激活必须解耦。
	//
	// 用途：技能声明的工具依赖原本只认「已绑定」（跨 run 持久），加入这一份之后
	// 「读过即视为本轮注入」—— 模型读 SKILL.md 之后，下一次 LLM 调用就能看到它需要的工具，
	// run 结束随 Context 一起消失。这样「用不用技能」由模型按需决定，
	// 不留长期驻留（跨 run 的正文与工具都不保留，见 core.RecordsToMessages 只回填文本）。
	//
	// 只在同一个 run 的串行工具执行里读写，加锁是为了防将来并发执行工具时踩踏。
	loadedMu     sync.Mutex
	loadedSkills map[string]loadedSkill
	skillsDirty  bool
}

// loadedSkill 一条「本轮已激活技能」的登记：名字 + 激活时的依赖声明快照。
type loadedSkill struct {
	name string
	deps []string
}

// LoadedSkill 已激活技能的快照，供工具解析组装候选（见 toolgrant.VisibleSkill）。
type LoadedSkill struct {
	ID   string
	Name string
	// Deps 激活时拿到的依赖声明；nil = 调用方没给快照（解析时回落查库）。
	Deps []string
}

// MarkSkillLoaded 登记「本轮已读」的技能与依赖声明快照，并置脏标记供下一轮重算工具集。
//
// deps 传 use_skill 返回的 ToolDeps —— 正式运行下它取自当前版本，是**定义**的一部分；
// 传 nil 表示没有快照，工具解析会回落查 skill_tool_grants（管理端预览等老口径）。
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
		return // 重复读同一技能：不算变化，不必重算
	}
	c.loadedSkills[id] = loadedSkill{name: name, deps: copyDeps(deps)}
	c.skillsDirty = true
}

// LoadedSkills 返回本轮已读技能的快照（skillID -> name）。
func (c *Context) LoadedSkills() map[string]string {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	out := make(map[string]string, len(c.loadedSkills))
	for id, s := range c.loadedSkills {
		out[id] = s.name
	}
	return out
}

// LoadedSkillEntries 返回本轮已激活技能的登记快照（含依赖声明）。
// agent 循环据此组装 toolgrant.VisibleSkill：顺序由调用方排，这里不保证。
func (c *Context) LoadedSkillEntries() []LoadedSkill {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	out := make([]LoadedSkill, 0, len(c.loadedSkills))
	for id, s := range c.loadedSkills {
		out = append(out, LoadedSkill{ID: id, Name: s.name, Deps: copyDeps(s.deps)})
	}
	return out
}

// copyDeps 拷贝依赖列表，并**保住 nil 与空切片的区别**：
// nil = 无快照（回落查库），空切片 = 快照里确实没有依赖（不再查库）。
// 不能写成 append([]string(nil), deps...) —— 那会把空切片退化成 nil。
func copyDeps(deps []string) []string {
	if deps == nil {
		return nil
	}
	out := make([]string, len(deps))
	copy(out, deps)
	return out
}

// sameDeps 比较两份依赖快照（nil 与空切片视为不同，理由见 copyDeps）。
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

// ConsumeSkillsDirty 读取并复位「技能集合有变化」标记。
// 调用方（agent 循环）据此决定是否重算工具集 —— 没变化就不查库。
func (c *Context) ConsumeSkillsDirty() bool {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	dirty := c.skillsDirty
	c.skillsDirty = false
	return dirty
}

// MarkToolsDirty 只置「工具集需重算」标记，不登记技能。
//
// 用于超级智能体给自己增删工具授权的场景：本 run 的工具 schema 在 run 起点算定，
// 若不等下一次会话，模型刚授完权、自己却看不见，会以为操作失败而反复重试。
// 复用的仍是同一个 dirty 位（循环顶部 ConsumeSkillsDirty → refreshToolDefs）。
func (c *Context) MarkToolsDirty() {
	c.loadedMu.Lock()
	defer c.loadedMu.Unlock()
	c.skillsDirty = true
}

// MaxOutputLimit 返回输出上限，未设置时用默认值。
func (c *Context) MaxOutputLimit() int {
	if c.MaxOutput <= 0 {
		return DefaultMaxOutput
	}
	return c.MaxOutput
}

// StoragePath 返回文件工具默认根（本会话工作目录）下某文件的存储相对路径。
//
// 纵深防御：结果必须始终落在本租户目录内。safePath 是第一道防线（拒绝绝对
// 路径与 ..），这里是第二道 —— 即使将来新增工具忘了过 safePath，也无法跨租户。
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

// AgentPath 返回本 agent 定义域下某文件的存储相对路径：tenants/{schema}/agents/{name}/{rel}。
//
// 定义域读写（system prompt 取 AGENT.md 等）走它，与运行域的 StoragePath 分开 ——
// 否则 StoragePath 换根到会话语目录后会读不到 AGENT.md。
func (c *Context) AgentPath(rel string) string {
	if c.VersionBound() {
		return path.Join(AgentVersionKey(c.Schema, c.AgentName, c.AgentVersion), rel)
	}
	return path.Join(AgentsKey(c.Schema, c.AgentName), rel)
}

// VersionBound 本次运行是否已绑定到某个已发布版本。
func (c *Context) VersionBound() bool {
	return c.DefinitionMode.IsCurrent() && c.AgentVersion > 0
}

// ChildMode 本运行调起的子实体（agent / flow）应使用的读定义模式。
//
// 唯一的公式：**子草稿 = 自身草稿 && FollowDrafts**。
// 写成方法而不是各调用点各算一次，避免「call_agent 跟开关、local__ 不跟」这类分裂；
// 也保证正式入口不会把草稿扩散出去（自身 current 时子必 current，与开关无关）。
func (c *Context) ChildMode() DefinitionMode {
	if c.DefinitionMode.IsCurrent() || !c.FollowDrafts {
		return ModeCurrent
	}
	return ModeDraft
}

// HostPath 把存储相对 key 换算成宿主绝对路径（沙箱挂载源）。
//
// 只有 local / nfs 存储能做挂载源，S3 下没有宿主路径 —— 返回空串让调用方报错，
// 而不是拿一个拼错的路径去挂。
func (c *Context) HostPath(key string) string {
	if c.RootDir == "" || key == "" {
		return ""
	}
	return filepath.Join(c.RootDir, filepath.FromSlash(key))
}

// SkillReadDir 读技能内容 / 跑技能脚本时的目录：存储 key + 宿主路径。
//
// 与写路径的分工：写永远走草稿（skillManager.skillPath），读按模式解析。
// 两种模式的返回值都是**技能目录**（不是 SKILL.md），因为调用方还要拼脚本相对路径。
//
// 私有技能的版本归所属 agent：ownerName 就是它的 agent 名，版本号取那个 agent 的
// 当前版本 —— 私有技能没有独立版本号（见 nexus/docs/terminology.md）。
func (c *Context) SkillReadDir(ownerName, skillName string) (key, hostDir string, err error) {
	if !c.DefinitionMode.IsCurrent() {
		// 草稿模式沿用改造前算宿主路径的口径（SkillRoot / AgentRoot 直接拼），
		// 不依赖 RootDir —— 单测与只注入这两个根的调用方都靠它。
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

// ToolDir 自定义工具脚本的宿主目录（沙箱只读挂到 /tool）。
//
// 正式运行读该工具的当前版本目录；没有版本就报 ErrNoReleasedVersion ——
// 不能退回草稿，否则「正式运行」里跑的是没发布过的脚本。
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
	// 版本行在、目录不在（被手删 / 迁移失败）：明确说是版本目录缺失，
	// 否则 os.Stat 只会给出一个含糊的「不可达」，排查时容易误判成存储配置问题。
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return "", fmt.Errorf("tool %q current version (%s) directory does not exist: the version directory may have been removed or the migration failed; release it again before calling", name, key)
	}
	return dir, nil
}

// TenantPath 返回租户根下某文件的存储相对路径：tenants/{schema}/{rel}。
//
// 供根寻址（SafePathEx 的 fromRoot 分支）使用：超级智能体可及全租户，非超级智能体只可及
// 自己的定义目录（读整棵、写仅私有 skill，见 gate.go 的 checkReadOwn / checkWriteOwn）。
// 与 StoragePath 一样，结果恒落在本租户内。rel 为租户根相对路径（如 agents/poet/AGENT.md）。
func (c *Context) TenantPath(rel string) string {
	if c.Schema == "" {
		panic("tenant path: schema is empty (the tenant is not initialized)")
	}
	return path.Join(tenantsSubdir, c.Schema, rel)
}

// argString 从 args 中取字符串参数，缺省返回空串。
func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// argBool 从 args 中取布尔参数，缺省返回 false。
func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// argInt 从 args 中取整数参数（兼容 JSON 解码后的 float64），缺省返回 0。
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

// argObject 取对象形态的参数；缺省 / null 返回空 map，类型不对直接报错。
//
// 不静默忽略类型错误：模型把对象写成 JSON 字符串（或反过来）时，若当成空对象放行，
// 参数会在几十秒后的执行期以「变量取不到值」的形式失败，排查成本远高于这里拒一次。
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

// argStringSlice 解析字符串数组参数，兼容 []any / []string / 逗号分隔的单个字符串。
// 返回 (值, 是否存在)：不存在 → ok=false。
// tool_deps 的语义是「nil = 不改，空数组 = 清空」，所以这两件事必须分得开。
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
	return nil, false // 形态无法识别 → 当作「没传」：宁可不改，也不要误清空
}

// limitedBuffer 带字节上限的输出缓冲，超限丢弃并标记截断。
type limitedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

// Write 实现 io.Writer。超限后丢弃多余内容但返回 len(p)，
// 避免 io.Copy / 子进程因短写而报错。
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
