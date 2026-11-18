package models

import (
	"encoding/json"
	"time"

	"flame/tools/xsql"
)

// LLMProvider LLM服务商结构体
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

// LLMProviderPreset LLM服务商预设结构体（系统级，全局共享，存在 platform schema）
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

// LLMModel LLM模型结构体
type LLMModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow xsql.Int `json:"context_window"`
	// 基础生成参数（jsonb）。键存在=使用该参数，键不存在=未使用；无多余包装结构。
	// 仅允许以下键：max_tokens(int)、temperature(float64)、top_p(float64)
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

// bpFloat 从 jsonb 解析出的 map 中读取 float64 类型的参数值。
// 键不存在或类型不匹配时返回 (0, false)。
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

// GetTemperature 返回基础参数中的 temperature；未配置时返回缺省值 0.7，ok=false。
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

// GetMaxTokens 返回基础参数中的 max_tokens；未配置时返回缺省值 40960，ok=false。
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

// GetTopP 返回基础参数中的 top_p；未配置时返回缺省值 0.6，ok=false。
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

// Agent 智能体结构体
type Agent struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description xsql.String `json:"description"`
	Config      xsql.JSONB  `json:"config"`
	Status      xsql.Int    `json:"status"` // 1:正常 3:废弃（见 StatusNormal/StatusDeprecated；agent 不设「锁定」）
	Endpoints   xsql.JSONB  `json:"endpoints"`
	// 两个 agent 级开关（列均为 NOT NULL DEFAULT false）：
	//
	//	ToolsUnrestricted：不限制工具 —— 该 agent 接受其可见 skill 声明的工具
	//	                   （跳过授权检查，显式 deny 仍生效）
	//	SkillDiscovery   ：允许发现未绑定的技能 —— 可见范围额外含「全员可见」的租户级技能（渐进式披露）
	//
	// 注意：system prompt 的注入清单**始终**只列「私有 + 已绑定」，与这些开关无关。
	ToolsUnrestricted bool `json:"tools_unrestricted"`
	SkillDiscovery    bool `json:"skill_discovery"`
	// IsSupervisor 超级权限身份（agents.is_supervisor）。
	//
	// 取代了过去的「名字等于 main」判据：main 只是建租户时预置的一个 supervisor，
	// 租户可以再配出同等权限的 agent（允许多个）。判据见 models.IsSupervisor。
	//
	// 它决定：缺省 core 工具集、管理类工具可用性、跨域写文件、不可被其它 agent 调用、
	// 授权目录走全量视角。它**不**决定删除保护 —— 那由 IsProtectedAgent（名字名单）管，
	// 两者语义正交。
	IsSupervisor  bool        `json:"is_supervisor"`
	CreatedBy     xsql.UUID   `json:"created_by"`
	UpdatedBy     xsql.UUID   `json:"updated_by"`
	UpdatedByName xsql.String `json:"updated_by_name"` // 修改人姓名（查询时关联 users 填充，非 DB 列）
	CreatedAt     xsql.Time   `json:"created_at"`
	UpdatedAt     xsql.Time   `json:"updated_at"`
}

// 生命周期状态取值（agents / flows / skills 三张表的 status 列共用，列类型 int2）。
//
//	正常(1)：可修改、可运行 —— 新建默认值
//	废弃(3)：**不可修改、不可运行** —— 只由手工设置
//
// agent / flows / skills 都是两态（CHECK (1,3)）。历史上的「锁定(2)：不可修改、
// 可运行」已全部取消：工作流由版本模型表达线上冻结，agent / skill 的「先解锁才能
// 发修复版」反而把想防的风险让了回来（解锁那一刻定义又可改）。
//
// 唯一保留 status=2 的地方是**自定义工具**：它沿用「启用(1)/停用(2)」的老口径，
// 语义是「停用」而非「锁定」。StatusLocked 常量由此保留，agent / skill 不要再用它。
//
// 唯一自动联动（实现分别在 shared/repository/svcendpoints 与各 Set*Status handler）：
//
//	状态切「废弃」→ 自动收回其全部端点的对外（exposed=false，**端点记录保留**）。
//
// 刻意**没有**的联动：端点对外开放不改状态。「对外提供入口」与「定义能不能改」
// 是两件事 —— 早期把两者绑死（对外即锁定），代价是改一行提示词要先解锁、而解锁又会收回
// 端点、断一下流量。现在端点对外后仍可编辑，改动由编辑界面在保存时提醒（有端点对外开放时二次确认）。
const (
	// StatusNormal 正常：可修改、可运行
	StatusNormal = 1
	// StatusLocked 不可修改、可运行。agent / flows / skills 均已取消该态；
	// 只剩**自定义工具**用它表示「停用」（老口径，不是冻结定义的「锁定」）。
	StatusLocked = 2
	// StatusDeprecated 废弃：不可修改、不可运行
	StatusDeprecated = 3
)

// IsModifiable 只有「正常」允许改定义：废弃不允许（自定义工具的「停用」同样不允许）。
// 守卫统一用它判定，别再写 `status == 1` 这类字面量。
func IsModifiable(status int) bool { return status == StatusNormal }

// IsRunnable 是否允许被运行/调用：只有「废弃」被拦。
func IsRunnable(status int) bool { return status != StatusDeprecated }

// 三个实体的状态判定（包一层，调用方不必再写 int(x.Status) 转换）。
// IsModifiable：允许改定义（只有「正常」允许）。

// IsModifiable 该 agent 是否允许改定义（含它名下的私有 skill：非正常态整片冻结）。
func (a *Agent) IsModifiable() bool { return IsModifiable(int(a.Status)) }

// IsModifiable 该 flow 是否允许改定义。
func (f *Flow) IsModifiable() bool { return IsModifiable(int(f.Status)) }

// IsModifiable 该 skill 是否允许改定义。
func (s *Skill) IsModifiable() bool { return IsModifiable(int(s.Status)) }

// 说明：原先的 HasEnabledEndpoint（"有没有 exposed 端点"）已下线 —— 定义守卫只看 status
// （见 IsModifiable），与端点对外**无关**：端点对外不改状态，也有端点对外开放却仍可编辑的实体。
// 需要「有对外开放端点」这个事实的地方（编辑界面保存前提醒、本地端点目录）各自从
// entities.endpoints / svc_endpoints 读，不要拿 status 反推。

// Skill skill 定义（agent_id 为 NULL 表示租户级，非 NULL 表示该 agent 私有）。
// 元数据与归属存本表，内容存文件（路径由 skill name 与归属推导）。
type Skill struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Description     xsql.String `json:"description"`
	CreatedBy       xsql.UUID   `json:"created_by"`
	UpdatedBy       xsql.UUID   `json:"updated_by"`
	UpdatedByName   xsql.String `json:"updated_by_name"`  // 修改人姓名（查询时关联 users 填充，非 DB 列）
	AgentName       xsql.String `json:"agent_name"`       // 所属智能体名字（查询时关联 agents 填充，非 DB 列；agent_id 为空时为空）
	Metadata        xsql.JSONB  `json:"metadata"`         // 扩展元数据（DB 列 metadata）
	UserInvocable   xsql.Bool   `json:"user_invocable"`   // OpenClaw user-invocable：是否暴露为用户可直接调用的命令
	CommandDispatch xsql.String `json:"command_dispatch"` // OpenClaw command-dispatch：'tool' = 绕过模型直接分发
	CommandTool     xsql.String `json:"command_tool"`     // OpenClaw command-tool：命令分发到的目标工具名
	Status          xsql.Int    `json:"status"`
	Restricted      xsql.Bool   `json:"restricted"` // 可见性收窄：false=全员可见(默认)；true=仅授权 agent 可见（仅对租户级 skill 生效）
	CreatedAt       xsql.Time   `json:"created_at"`
	UpdatedAt       xsql.Time   `json:"updated_at"`
	AgentID         xsql.UUID   `json:"agent_id"`
}

// ===== 工具授权体系（tool_groups / agent_tool_grants / skill_tool_grants） =====

// 引用类型（tool_group_items.item_type / *_tool_grants.item_type）。
const (
	GrantItemGroup = "group" // 组：天然组（builtin:* / mcp:* / rpc:* / local:rpc）或工具组（group:*）
	GrantItemTool  = "tool"  // 单个工具：builtin:<tool> / mcp:<conn>:<tool> / rpc:<conn>:<tool> / local:rpc:<name>
	// 注：local:rpc 只收 target_type='flow' 的本地端点（agent 端点走 call_agent，不进工具目录）。
)

// 授权种类（agent_tool_grants.grant_kind）。
const (
	GrantKindRequired = "required" // 必须导入：恒注入，不受 deny 影响
	GrantKindAllow    = "allow"    // 可使用：被 deny 覆盖
	GrantKindDeny     = "deny"     // 显式拒绝：只对 allow 生效
)

// GrantRefBuiltinPrefix ref 前缀：单个内置工具（builtin:<tool>）。
// 与 service/agent/toolgrant.ScopeBuiltin 同值（有测试守住）—— 共享层不反向依赖服务层。
const GrantRefBuiltinPrefix = "builtin:"

// DefaultAgentTools 新建 agent 缺省获得的工具名（单一真源）。
//
// 口径：只读元信息 + 文件读写，默认限于 agent 自己的会话工作区；另可用 "/" 前缀
// 根寻址**读自己的定义目录**（agents/{self}/**，含 AGENT.md）、**写自己的私有 skill**
// （agents/{self}/skills/{skill}/**，见 tools/gate.go 的 checkReadOwn / checkWriteOwn），
// 其余跨域路径一律不可达。
// 不含 exec（黑盒 shell + os.Environ 外泄面）；call_agent 因「工具 ≠ 权限」而包含
// —— 它的真正闸门是 agent_relations（无行即拒绝）。
//
// **不含 create/update/delete_own_skill**：自建私有 skill 等于把自己的提示词变成可写面
// （写进去的 name/description 下次调用即常驻 system prompt），被输入诱导时可形成持久化注入。
// 故默认不给，由管理员按 agent 显式授权；授权时用 `required` 物化（恒注入，避开 allow 被
// skill 依赖收窄的坑），代价是 `deny` 收不回来 —— 要收回得做全量替换（把这三行删掉）。
//
// 两处消费：① agents.CreateAgent 物化成 required 授权；② toolgrant 的 preset:default 组。
// 改这里即同时改两处，避免漂移（有测试守住）；另需同步 sql/agent_default_tool_grants.sql。
var DefaultAgentTools = []string{
	// 只读文件
	"read_file", "list_files", "file_exists",
	// 自己的会话工作区内读写
	"write_file", "edit_file", "delete_file",
	// 只读元信息 + 用技能
	// use_skill 必须在这里：绑定只让技能出现在提示词清单，工具要 use_skill 激活才会出现；
	// 缺了它，普通 agent 就没有任何入口点亮 allow 授权（read_skill 是纯查看，不带工具）。
	"list_agents", "read_agent", "list_skills", "read_skill", "use_skill",
	// 协作（实际权限由 agent_relations 决定）
	"call_agent",
}

// DefaultAgentToolGrants 返回新建 agent 的缺省授权项（item_type=tool / ref=builtin:<name>）。
// CreateAgent 用它物化授权行，toolgrant 的 preset:default 用它生成成员。
func DefaultAgentToolGrants() []ToolGroupItem {
	return toolGrantsOf(DefaultAgentTools)
}

// SupervisorCoreGrantRefs 超级智能体常驻的**组**（core 集，单一真源）。
//
// 六组，展开后 20 个工具：
//
//	builtin:file         文件读写（6）
//	builtin:exec         一次性进程（2）
//	builtin:shell        常驻会话（1）
//	builtin:agent        看 / 调 agent（3）
//	builtin:skill        看 / 用技能（3：list_skills / read_skill / use_skill）
//	builtin:agent-admin  建 / 改 / 删 agent + 绑定技能（5）
//
// 背景：超级智能体过去是「代码里全开」的特权（toolgrant.BuiltinTools 全量），
// 「超级智能体手里有什么」只有代码知道、库里查不到、也没法按租户调整。
// 现在这份基线搬进 agent_tool_grants 的 required 行，超级智能体与其它 agent 用同一套语言：
// **常驻 = 表里有 required 行**，而不是「因为你是超级智能体」。
//
// 为什么是组而不是逐个工具：组是原子的 —— 误删一个 = 少一整类，面板上一眼能看出来；
// 而 33 条单工具行里删掉 `read_file`，系统不会报错，只是静默少一个工具。
// 代价是组内新增工具会自动进 core，这正是「core = 系统能力基线」该有的语义。
//
// 不在 core 里的（按需）：
//   - builtin:browser —— 自称「能用 exec 拿到内容就不要开」、schema 全场最大（14 action /
//     18 参数）、唯一「出网 + 读任意页面 + 带登录态」的组合，属任务能力。
//     它**不跟随任何技能**，所以不能用 allow 按需（allow 只是候选的过滤器，没候选就空转）；
//     要就由管理员直接加一条 required，用完删掉。
//   - skill-own / skill-admin / tool-grant / custom-tool / flow-admin —— 跟随技能：
//     use_skill 激活 skill-authoring 后得前两组，激活 agent-authoring 得 tool-grant（+ skill-admin），
//     激活 custom-tool-authoring 得 custom-tool，激活 flow-authoring 得 flow-admin。
//     绑定只让技能出现在提示词清单里，不直接给工具。
//     超级智能体**不常驻激活**这些技能，需要时 use_skill 读正文，run 结束自动收 ——
//     默认上下文因此少一整套管理工具的 schema（用一次工具调用换）。
//
// 三处消费：① agents.CreateAgent（supervisor 分支）物化成 required；
// ② sql/main_core_tool_grants.sql 给存量回填；③ 管理端预览与文档。
// 改这里要同步 ② 的硬编码数组（有测试守住）。
var SupervisorCoreGrantRefs = []string{
	GrantRefBuiltinPrefix + "file",
	GrantRefBuiltinPrefix + "exec",
	GrantRefBuiltinPrefix + "shell",
	GrantRefBuiltinPrefix + "agent",
	GrantRefBuiltinPrefix + "skill",
	GrantRefBuiltinPrefix + "agent-admin",
}

// SupervisorCoreGrants 返回超级智能体的 core 授权项（item_type=group / ref=builtin:<组名>）。
func SupervisorCoreGrants() []ToolGroupItem {
	items := make([]ToolGroupItem, 0, len(SupervisorCoreGrantRefs))
	for _, ref := range SupervisorCoreGrantRefs {
		items = append(items, ToolGroupItem{ItemType: GrantItemGroup, Ref: ref})
	}
	return items
}

// SupervisorSeedSkills 授予超级权限时自动绑定的技能（supervisor 起步套件的一部分）。
//
// 语义与 protectedSkills 正交：那边回答「哪些不许删 / 不许改名」，这里回答
// 「一个新 supervisor 应该拿到哪些说明书」。两者当前取值相同，但**不要合并** ——
// 将来出现「不可删但不必默认绑定」的基石技能时，合并的写法就没法表达。
//
// 为什么是这四个：五类管理工具（agent-admin / skill-admin / tool-grant / custom-tool /
// flow-admin）都跟随技能按需注入（见 SupervisorCoreGrantRefs 注释的「不在 core 里的（按需）」段），
// 绑定只让技能进提示词清单；不绑的话新 supervisor 手里只有常驻的 6 个组，
// 「建技能」「改授权」「写自定义工具」「建工作流」那几组永远点不亮 —— 有身份、没手脚。
var SupervisorSeedSkills = []string{
	"agent-authoring",
	"skill-authoring",
	"custom-tool-authoring",
	"flow-authoring",
}

// toolGrantsOf 工具名列表 → 授权项列表（DefaultAgentToolGrants 用）。
func toolGrantsOf(names []string) []ToolGroupItem {
	items := make([]ToolGroupItem, 0, len(names))
	for _, name := range names {
		items = append(items, ToolGroupItem{ItemType: GrantItemTool, Ref: GrantRefBuiltinPrefix + name})
	}
	return items
}

// 注：这里曾有一个「core 回填是否跑过」的判据（MainCoreMigrationMarker = agent-admin +
// MainCoreMigrationPending）。存量租户迁移完成后已整体移除：
//
//   - 它唯一的用途是让**未迁移**的 supervisor 回落「缺省全开」，而那条暗路径本身就是
//     长期排查负担（表里说没有、实际全给）；
//   - 删除后 supervisor 的常驻集只剩一条真源：表里的 required 行；
//   - 「零授权行」的保险丝**没有**跟着删 —— 那不是迁移机制，是自举安全
//     （见 agent_impl 的零行兜底与 admin/toolgrants 预览的同名分支）。
//
// 重建这段逻辑前先确认：还有没有「授权表只有零散行、没有 core 组」的存量租户。
// 核查 SQL 见 sql/main_core_tool_grants.sql 文件头。

// ToolGroup 工具组（成员只能是天然组或单个工具，不允许嵌套工具组）。
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

// ToolGroupItem 工具组成员。
type ToolGroupItem struct {
	ItemType string `json:"item_type"` // group | tool
	Ref      string `json:"ref"`
}

// AgentToolGrant agent 的工具授权（required / allow / deny）。
type AgentToolGrant struct {
	AgentID   string    `json:"agent_id"`
	GrantKind string    `json:"grant_kind"` // required | allow | deny
	ItemType  string    `json:"item_type"`  // group | tool
	Ref       string    `json:"ref"`
	UpdatedBy xsql.UUID `json:"updated_by"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// SkillToolGrant skill 的工具依赖（只表达「需要」，不产生授权）。
type SkillToolGrant struct {
	SkillID  string `json:"skill_id"`
	ItemType string `json:"item_type"` // group | tool
	Ref      string `json:"ref"`
}

// Skill 的 status 与 agents 共用两态取值（见上方 StatusNormal/StatusDeprecated）：
// 只有「正常」可改、「废弃」不可改不可运行。skill 没有端点，所以「废弃自动收回端点」不涉及它。

// AgentSkillBinding agent 与 skill 的授权关联（对应 agent_skill_bindings 表，纯白名单）。
// 仅当 skills.restricted = true 时生效：该 skill 只对本表列出的 agent 可见。
type AgentSkillBinding struct {
	AgentID   string      `json:"agent_id"`
	SkillID   string      `json:"skill_id"`
	CreatedBy xsql.UUID   `json:"created_by"`
	CreatedAt xsql.Time   `json:"created_at"`
	AgentName xsql.String `json:"agent_name"` // 关联查询填充（非 DB 列）
}

// AgentRelation agent 之间的调用授权（对应 agent_relations 表）。
// 语义：无行 = caller 不可调用 callee（默认拒绝）；有行 = 允许。
type AgentRelation struct {
	CallerID   string      `json:"caller_id"`
	CalleeID   string      `json:"callee_id"`
	CreatedBy  xsql.UUID   `json:"created_by"`
	CreatedAt  xsql.Time   `json:"created_at"`
	CallerName xsql.String `json:"caller_name"` // 关联查询填充（非 DB 列）
	CalleeName xsql.String `json:"callee_name"` // 关联查询填充（非 DB 列）
}

// CallableAgent 可被调用的 agent 摘要（system prompt 的「可调用 agent 清单」用）。
// 一次 JOIN（agent_relations ⋈ agents）取回，避免逐目标二次查询。
type CallableAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      int    `json:"status"`
	// IsSupervisor 目标是否带超权属性。supervisor 可被调用（被调起时仍读草稿），
	// 是否放行由 agent_relations 授权行决定；这里带出来只为展示与审计，不做业务判定。
	IsSupervisor bool `json:"is_supervisor"`
}

// SvcEndpoint 服务端点结构体
type SvcEndpoint struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
	TargetID    string     `json:"target_id"`
	TargetType  string     `json:"target_type"`  // "agent" 或 "flow"
	Protocol    string     `json:"protocol"`     // "mcp" | "rpc"（llm 仅建库校验放行，无运行时）
	InputSchema xsql.JSONB `json:"input_schema"` // 工具入参 JSON Schema：flow 端点恒等于 flows.arguments
	Exposed     xsql.Bool  `json:"exposed"`      // 对外开关：true=租户外可用（外部凭证 / MCP 注册），false=仅租户内部可用（新建默认 false）

	// TargetName 目标实体名（非 DB 列）：列表查询时 JOIN agents / flows 填充，避免前端再做 id→名称映射
	TargetName xsql.String `json:"target_name"`

	// TargetSupervisor 目标是不是超级智能体（非 DB 列，查询时带出）。
	// 凭证的「除超级智能体外」范围要靠它判定 —— 判定点只有 target_type + target_id，
	// 再回查一次 agents 会把热路径（每次工具调用）变成两次查询。
	TargetSupervisor bool `json:"target_supervisor"`
}

// EndpointProtocol 端点协议信息（agents / flows 的 endpoints jsonb 数组元素）
type EndpointProtocol struct {
	Protocol string `json:"protocol"`
	Exposed  bool   `json:"exposed"`
}

// Flow 工作流定义结构体（沿用 flows 表存储）
type Flow struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Diagram           xsql.JSONB         `json:"diagram"`
	Description       xsql.String        `json:"description"`
	CreatedAt         xsql.Time          `json:"created_at"`
	UpdatedAt         xsql.Time          `json:"updated_at"`
	CreatedBy         xsql.UUID          `json:"created_by"`      // 创建人用户ID（uuid）
	UpdatedBy         xsql.UUID          `json:"updated_by"`      // 修改人用户ID（uuid）
	CreatedByName     xsql.String        `json:"created_by_name"` // 创建人姓名（查询时关联 users 填充，非 DB 列）
	UpdatedByName     xsql.String        `json:"updated_by_name"` // 修改人姓名（查询时关联 users 填充，非 DB 列）
	Arguments         xsql.JSONB         `json:"arguments"`
	Outputs           xsql.JSONB         `json:"outputs"`
	EnvVars           xsql.JSONB         `json:"env_vars"`
	FlowVars          xsql.JSONB         `json:"flow_vars"`
	Timeout           xsql.Int           `json:"timeout"`
	MaxRecursionDepth xsql.Int           `json:"max_recursion_depth"`
	MaxNodeCount      xsql.Int           `json:"max_node_count"`
	MaxExecCount      xsql.Int           `json:"max_exec_count"`
	MaxRunRecords     xsql.Int           `json:"max_run_records"`
	MaxSessionRecords xsql.Int           `json:"max_session_records"`
	Status            xsql.Int           `json:"status"`      // 1:正常 3:废弃（工作流不设锁定，见 StatusNormal/StatusDeprecated）
	Endpoints         []EndpointProtocol `json:"endpoints"`   // 端点协议+对外状态（jsonb 列）
	Breakpoints       []string           `json:"breakpoints"` // 断点 node_id 列表（非 DB 列，保存时全量覆盖 flow_breakpoints）

	// 版本状态（非 DB 列，查询时计算）：
	//   CurrentVersion = 0 → 尚未发布（正式调用不可用，只能调试）
	//   Dirty = 工作副本与当前版本的定义不一致（改名不算：名字不是版本内容）
	CurrentVersion int  `json:"current_version"`
	Dirty          bool `json:"dirty"`
}

// FlowRunRecord 工作流运行记录（对应 flow_run_records 表）
type FlowRunRecord struct {
	ID         string     `json:"id"`
	FlowID     string     `json:"flow_id"`
	Status     string     `json:"status"`   // success/failed/cancelled
	RunType    string     `json:"run_type"` // debug（调试器）/ preview（试跑）/ task / call
	Result     string     `json:"result"`
	Progress   []byte     `json:"progress"` // jsonb，{"progress":[...],"result":[...]}
	ErrMsg     string     `json:"err_msg"`
	NodeCount  int        `json:"node_count"`
	DurationMs int64      `json:"duration_ms"`
	StartedAt  xsql.Time  `json:"started_at"`
	FinishedAt *xsql.Time `json:"finished_at"`
	// 运行配置快照
	RunArgs              []byte `json:"run_args"`
	RunLimits            []byte `json:"run_limits"`
	RunFlowVars          []byte `json:"run_flow_vars"`
	RunFlowVarsTruncated bool   `json:"run_flow_vars_truncated"`
	RunFlowVarsSize      int    `json:"run_flow_vars_size"`
	ProgressTruncated    bool   `json:"progress_truncated"`
	ProgressSize         int    `json:"progress_size"`
	// Drafts 本次运行跟随过的草稿（jsonb 数组，空数组 = 纯正式运行）。
	// 调试跑的是工作副本，内容会变，这份清单是「当时跑的是哪几份」的唯一凭据。
	Drafts []byte `json:"drafts"`
}

// SessionOwnerType 会话记忆的属主类型
const (
	SessionOwnerTypeFlow  = "flow"  // 工作流
	SessionOwnerTypeAgent = "agent" // 智能体
)

// Session 会话（对应 sessions 表，泛化：agent + flow）
type Session struct {
	ID        string    `json:"id"`
	OwnerType string    `json:"owner_type"` // flow / agent
	OwnerID   string    `json:"owner_id"`
	UserID    string    `json:"user_id"` // 会话归属用户（可空，NULL=租户/系统级）
	Title     string    `json:"title"`
	CreatedAt xsql.Time `json:"created_at"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// VisibleToUser 会话是否对该用户可见 —— 外部入口（HTTP chat / MCP / RPC）的统一判据。
//
// 会话归属的是「用户」，owner_type / owner_id 只记录创建者（谁建的会话、记录挂谁名下），
// 不再参与访问控制：子 agent 与父 agent 共用同一个 session 时，owner 必然对不上，
// 用 owner 收口会把合法的级联调用全部拒掉。
//
// userID 为空（身份缺失）一律判否；会话 user_id 为空表示租户级会话（列表里从不出现），
// 它不存在「别人」，放行。
func (s *Session) VisibleToUser(userID string) bool {
	if s == nil || userID == "" {
		return false
	}
	if s.UserID == "" {
		return true
	}
	return s.UserID == userID
}

// OwnedBy 会话是否由 (ownerType, ownerID) 这个实体创建。
//
// 仅用于「记录归属 / 展示 / 审计」：session_records 按 owner 分组，父子 agent 共用
// 同一 session 时各自的行互不可见。**不要**拿它做访问控制，那是 VisibleToUser 的职责。
func (s *Session) OwnedBy(ownerType, ownerID string) bool {
	if s == nil || ownerID == "" {
		return false
	}
	return s.OwnerType == ownerType && s.OwnerID == ownerID
}

// SessionRecord 会话记录（对应 session_records 表）
// 按 (owner_type, owner_id, session_id) 分组，每个 session 保留最近 max_session_records 条。
type SessionRecord struct {
	ID         string    `json:"id"`
	OwnerType  string    `json:"owner_type"` // flow / agent
	OwnerID    string    `json:"owner_id"`
	SessionID  string    `json:"session_id"`
	RunID      string    `json:"run_id"`
	Input      []byte    `json:"input"`       // jsonb，本轮入参 arguments
	Output     string    `json:"output"`      // text，本轮最终结果
	RunType    string    `json:"run_type"`    // call / debug
	DurationMs int64     `json:"duration_ms"` // 本轮执行耗时（毫秒）；0=未记录（存量行 / flow 侧记忆写入）
	Status     string    `json:"status"`      // ok / stopped / failed；存量行由列默认值补成 ok
	Error      string    `json:"error"`       // 失败或终止的人话原因；status=ok 时为空
	CreatedAt  xsql.Time `json:"created_at"`
}

// 会话记录 status 取值（对应 session_records.status，DB 里是 varchar(16)）。
//
// 为什么单独记一列而不是把错误混进 output：历史里一行空白回答，用户无法区分
// 「我点了停止」「模型挂了」「工具报错」，只能来问人。
const (
	SessionStatusOK      = "ok"      // 正常完成
	SessionStatusStopped = "stopped" // 被主动终止（用户停止 / 会话关闭 / 运行超时）
	SessionStatusFailed  = "failed"  // 出错（原因见 error）
)

// SvcAuthBinding 服务端认证身份绑定（对应 svc_auth_bindings 表，MCP/RPC 共用）
// 把外部认证身份映射到系统用户：apikey 用明文 secret，oauth 用 iss + sub。
type SvcAuthBinding struct {
	ID              string `json:"id"`
	AuthType        string `json:"auth_type"`        // apikey | oauth
	ExternalIssuer  string `json:"external_issuer"`  // oauth: token.iss；apikey: ''
	ExternalSubject string `json:"external_subject"` // oauth: token.sub；apikey: 明文 secret
	UserID          string `json:"user_id"`          // users.id
	IsActive        bool   `json:"is_active"`
	// RestrictTargets=true 时，该凭证只能调用白名单里的端点目标（见 svc_auth_binding_targets）；
	// 白名单为空表示一个都不允许。新建绑定默认 true（安全默认：忘了配 = 一个都调不到），
	// false 表示不限制，需显式关闭；存量绑定在迁移时保持 false。
	RestrictTargets bool `json:"restrict_targets"`
	// ExcludeSupervisor 只在 RestrictTargets=false 时有意义：不限制全部端点，但**排除超级智能体**
	// （supervisor 的端点等于对外开放租户管理能力）。是「不限制」的中间档，不用维护白名单。
	// RestrictTargets=true 时忽略本字段（白名单已经精确表达）。
	ExcludeSupervisor bool `json:"exclude_supervisor"`
	// TargetCount 白名单条数（列表展示用，不参与鉴权判定）。
	TargetCount int       `json:"target_count"`
	CreatedAt   xsql.Time `json:"created_at"`
	UpdatedAt   xsql.Time `json:"updated_at"`
}

// FlowNodeRecord 工作流节点运行记录（对应 flow_node_records 表）
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
	Extra               []byte     `json:"extra"` // jsonb，节点类型特有的运行结果
}

// McpConnection MCP 客户端连接配置
// 对应数据库表 t0.mcp_connections。
type McpConnection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`         // 机器标识，作为工具名前缀，仅含英文字母和数字，全局唯一
	DisplayName string     `json:"display_name"` // 展示名，可含中文/空格；为空时回退为 Name
	Description string     `json:"description"`
	URL         string     `json:"url"`       // streamable_http 用；stdio 为空
	Command     string     `json:"command"`   // stdio 用；本地可执行命令
	Args        xsql.JSONB `json:"args"`      // stdio 用；命令参数数组
	WorkDir     string     `json:"work_dir"`  // stdio 用；子进程工作目录，对应 cmd.Dir
	Env         xsql.JSONB `json:"env"`       // stdio 用；环境变量数组 ["K=V", ...]，继承并追加到 os.Environ()
	Transport   string     `json:"transport"` // "streamable_http" | "stdio"
	Auth        xsql.JSONB `json:"auth"`      // 鉴权配置（none/bearer/oauth）；stdio 下语义忽略，凭证走 env
	IsActive    bool       `json:"is_active"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
}

// Plugin 某租户对某个磁盘插件的启用与实现配置。
// 对应数据库表 t0.plugins。
//
// 插件本体在磁盘（{root_dir}/plugins），这里只记「本租户用哪个实现、key 是多少」：
//   - Kind      插件类型（当前恒为 tools；与唯一键 (kind, name) 一起构成行标识）
//   - Name      插件名（= 插件目录名 = 授权 ref 的 subject），如 web_search
//   - Impl      主实现名（tavily / searxng / bing_rss ...），必须存在于 manifest
//   - Fallbacks 兜底链（有序数组）：主实现失败时按顺序再试
//   - Configs   按实现分键存配置：{"tavily":{"api_key":"..."},"searxng":{"endpoint":"..."}}
//     分键是刻意的 —— 切换主实现不该把另一家的 key 洗掉
//
// 没有 「是否安装」这回事：磁盘上有的插件都会出现在管理端，这里的一行代表「已启用」。
type Plugin struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // 插件类型；写库时必须显式给出（空值会被仓储拒绝）
	Name      string     `json:"name"`
	Impl      string     `json:"impl"`
	Fallbacks xsql.JSONB `json:"fallbacks"`
	Configs   xsql.JSONB `json:"configs"`
	IsActive  bool       `json:"is_active"`
	CreatedAt xsql.Time  `json:"created_at"`
	UpdatedAt xsql.Time  `json:"updated_at"`

	// 以下三列是可观测信息，与「配置」无关：调用不会碰 updated_at
	// （那列表示配置版本，被调用频率污染就没法判断「配置什么时候改过」）。
	LastUsedAt  *time.Time `json:"last_used_at"`  // 最近一次成功调用
	LastError   string     `json:"last_error"`    // 最近一次失败的一句话原因（已截断）
	LastErrorAt *time.Time `json:"last_error_at"` // 上述错误发生的时刻
}

// FallbackImpls 兜底实现链；JSON 解析失败按空链处理（不让脏数据中断调用）。
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

// ImplConfig 取某实现的配置；不存在时返回空 map（不是 nil，调用方可直接读 key）。
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

// AllConfigs 全部实现的配置（按实现分键）。管理端据此渲染「切实现时保留另一家 key」。
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

// jsonbRaw 把 xsql.JSONB 的内部值还原成 JSON 字节；空值返回 nil。
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

// McpConnectionTool MCP 连接下的工具
// 对应数据库表 t0.mcp_connection_tools。工具列表在启用连接时从远端发现并持久化，
// 供工作流画布等本地节点配置使用。
type McpConnectionTool struct {
	ID           string     `json:"id"`
	ConnectionID string     `json:"connection_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	InputSchema  xsql.JSONB `json:"input_schema"`
	UpdatedAt    xsql.Time  `json:"updated_at"`
}

// RpcConnection RPC 客户端连接配置
// 对应数据库表 t0.rpc_connections。用于连接外部 RPC server（JSON-RPC 2.0）。
type RpcConnection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`         // 机器标识，作为工具名前缀，仅含英文字母和数字，全局唯一
	DisplayName string     `json:"display_name"` // 展示名，可含中文/空格；为空时回退为 Name
	Description string     `json:"description"`
	URL         string     `json:"url"`       // streamable_http 用；stdio 为空
	Command     string     `json:"command"`   // stdio 用；本地可执行命令
	Args        xsql.JSONB `json:"args"`      // stdio 用；命令参数数组
	WorkDir     string     `json:"work_dir"`  // stdio 用；子进程工作目录，对应 cmd.Dir
	Env         xsql.JSONB `json:"env"`       // stdio 用；环境变量数组 ["K=V", ...]，继承并追加到 os.Environ()
	Transport   string     `json:"transport"` // "streamable_http" | "stdio"
	Auth        xsql.JSONB `json:"auth"`      // 鉴权配置（none/bearer/oauth）；stdio 下语义忽略，凭证走 env
	IsActive    bool       `json:"is_active"`
	CreatedAt   xsql.Time  `json:"created_at"`
	UpdatedAt   xsql.Time  `json:"updated_at"`
}

// RpcConnectionTool RPC 连接下的工具
// 对应数据库表 t0.rpc_connection_tools。工具列表在启用连接时从远端 RPC server
// 通过 JSON-RPC tools/list 发现并持久化，供工作流画布等本地节点配置使用。
type RpcConnectionTool struct {
	ID           string     `json:"id"`
	ConnectionID string     `json:"connection_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	InputSchema  xsql.JSONB `json:"input_schema"`
	UpdatedAt    xsql.Time  `json:"updated_at"`
}
