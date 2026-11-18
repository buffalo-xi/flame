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

// listLimit 列举连接/端点时使用的上限（远大于实际数量，避免分页默认值截断）。
const listLimit = 1000

// 不可用归因（ToolInfo.UnavailableKind / GroupInfo.UnavailableKind 的取值）。
//
// 为什么不合成一句「不可用」：这几类对应**不同的修复入口** ——
// 能力与插件停用归平台管理员，插件启用 / 连接 / 端点归租户自己。
// 混成一句，用户会去改错的那一层（在租户侧反复启用，而平台压根没开通）。
const (
	UnavailableCapability     = "capability"      // 平台未开通这类能力（exec / browser …）
	UnavailablePluginPolicy   = "plugin_policy"   // 平台已对本租户停用这个插件
	UnavailablePluginInactive = "plugin_inactive" // 平台没停用，租户自己没启用
	UnavailableConnection     = "connection"      // MCP / RPC 连接未启用
	UnavailableEndpoint       = "endpoint"        // 本地 rpc 端点未对外开放
)

// ToolInfo 单个工具（展开后的叶子节点）。
type ToolInfo struct {
	Ref            string `json:"ref"`  // builtin:read_file / mcp:fs:read_file / plugin:web_search
	Name           string `json:"name"` // LLM 工具名：read_file / mcp__fs__read_file / web_search
	Label          string `json:"label"`
	Description    string `json:"description"`
	Available      bool   `json:"available"`       // 所属连接/端点是否可用（连接未启用 / 端点未对外开放 / 插件未启用 / 平台未开通能力 → false）
	Source         string `json:"source"`          // 来源组：builtin:file / mcp:fs / local:rpc / plugin:web_search / group:xxx
	SupervisorOnly bool   `json:"supervisor_only"` // 仅 supervisor 可绑定 / 执行
	// UnavailableReason 不可用的原因（给人看的，直接展示）。
	// 三种原因必须能分开：平台没开通能力 / 连接或端点没启用 / 工具本身不可用 ——
	// 混成一句「不可用」，用户会去改错的那一层。
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// UnavailableKind 归因（取值见 Unavailable* 常量）：前端据此出不同徽标文案，
	// 不必解析中文原因。空 = 可用。
	UnavailableKind string `json:"unavailable_kind,omitempty"`
	// Parameters 入参 schema（JSON Schema object）。
	//
	// 内置工具可以不带（agent 侧从 tools.List() 现取 Meta().Parameters），
	// 但外部工具（mcp / plugin）没有那个兜底 —— 插件工具必须自带 schema，
	// 否则声明给模型时入参是空的。
	Parameters map[string]any `json:"parameters,omitempty"`
}

// GroupInfo 可引用组。
type GroupInfo struct {
	Ref         string     `json:"ref"`  // builtin:file / mcp:fs / rpc:erp / local:rpc / group:xxx / preset:xxx
	Name        string     `json:"name"` // 展示名
	Kind        string     `json:"kind"` // builtin | mcp | rpc | local | custom | preset
	Description string     `json:"description"`
	Available   bool       `json:"available"`
	Items       []ToolInfo `json:"items"`
	SourceID    string     `json:"source_id"`    // 连接 id（mcp / rpc 组）：前端「刷新工具清单」等动作需要
	RefreshedAt string     `json:"refreshed_at"` // 工具清单最后刷新/更新时间（RFC3339；连接与端点类组才有）
	// UnavailableReason 组不可用的原因（同 ToolInfo.UnavailableReason）。
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// UnavailableKind 组不可用的归因（同 ToolInfo.UnavailableKind）。
	UnavailableKind string `json:"unavailable_kind,omitempty"`
	// Synthetic true = 类别容器，**不是可授权对象**（插件 / 自定义工具两类）。
	//
	// 它们成员是租户侧实体，数量随租户变化：一个工具一张卡会让组名退化成工具名
	// （"利息终值计算"组），既难认又把 item_type 误导成 group。这里一张卡装全部成员，
	// 授权落点仍是成员自己的工具 ref（容器 ref 形如 "custom:*"，ValidateRef 会拒）。
	Synthetic bool `json:"synthetic,omitempty"`
}

// PresetGroup 预设组定义（代码内置，不可编辑）。
type PresetGroup struct {
	Name        string                 `json:"name"`
	Label       string                 `json:"label"`
	Description string                 `json:"description"`
	Items       []models.ToolGroupItem `json:"items"`
}

// PresetGroups 内置预设组：跨类的常用组合，便于一键授权。
//
// 当前只保留 preset:default（新建 agent 的缺省集，见 models.DefaultAgentTools）——
// 其余按类拆分的小预设（原 readonly / file-write / exec / collaborate）已移除：
// 它们可由天然组（builtin:file / builtin:exec / builtin:agent / builtin:skill）+
// 单个工具多选覆盖，留着反而与缺省集重复。
//
// 管理类工具（分类 agent-admin / skill-admin / tool-grant / custom-tool）**不**在这里单列预设 ——
// 它们由各自的天然组承载，且只对超级智能体可见（见 Catalog.builtinTools）。
//
// 注意：移除预设会让存量里引用它们的授权行解析为空（ToolsOfRef 返回空列表 + warning，
// 不报错但工具会消失）。升级时请执行 sql/agent_preset_cleanup.sql 展开旧引用。
var PresetGroups = []PresetGroup{
	{
		Name: "default", Label: "标配工具",
		Description: "新建 agent 默认带上的工具：读写自己的工作区、查看 agent 与 skill、协作调用（不含命令执行）",
		Items:       models.DefaultAgentToolGrants(),
	},
}

// Catalog 可引用目录：内置工具、天然组、预设组、自定义组、连接与本地端点、磁盘插件。
type Catalog struct {
	schema       string
	isSupervisor bool // 视角身份：非 supervisor 时目录隐藏「仅 supervisor 可用」的管理类工具（分类落在 supervisorOnlyCategories）
	grants       toolgrants.Repository
	mcp          mcpconnections.Repository
	rpc          rpcconnections.Repository
	eps          svcendpoints.SvcEndpointRepository
	plugins      pluginsrepo.Repository
	scripts      customtools.Repository
	policies     policyMapFunc // 平台策略来源（默认直连库；单测注入假实现）
	// caps 平台给本租户开通的能力（见 shared/capabilitypolicy.ResolveAll）。
	// nil = 不限制（仅无租户语义的调用方）；非 nil 时缺键视为未开通（缺省拒绝）。
	caps map[string]bool
	// mode 自定义工具的定义来源：正式运行取当前版本（与脚本目录同源），草稿模式取草稿。
	// 缺省（零值）即草稿 —— 管理端授权目录必须看到「正在编辑什么」。
	mode tools.DefinitionMode
}

// policyMapFunc 一次取某租户全部生效策略（见 shared/pluginpolicy.List）。
type policyMapFunc func(schema string) (map[string]pluginpolicy.Policy, error)

// NewCatalog 创建目录（schema 为租户 schema；isSupervisor 为视角 agent 是否带超权属性）。
//
// 非 supervisor 视角下，管理类工具（分类落在 tools.SupervisorOnlyCategories）不会出现在
// 任何组里 —— 天然组不产出，自定义组 / 预设组展开时也会被剔除（空组直接消失）。
func NewCatalog(schema string, grants toolgrants.Repository, isSupervisor bool) *Catalog {
	return &Catalog{
		schema:       schema,
		isSupervisor: isSupervisor,
		grants:       grants,
		mcp:          mcpconnections.NewRepository(),
		rpc:          rpcconnections.NewRepository(),
		eps:          svcendpoints.NewRepository(),
		plugins:      pluginsrepo.NewRepository(),
		// 自定义工具的「有哪些」以 tools/ 目录为准（customtoolstore 合并契约与策略），
		// 与执行层同源：目录里在的工具，授权目录里就能看见并授权。
		scripts:  customtoolstore.New(config.AgentRootDir(), schema),
		policies: pluginpolicy.List,
	}
}

// WithDefinitionMode 让目录按运行模式取自定义工具的定义。
//
// 正式运行（MCP / RPC / 端点 / flow）必须看到「这一版发布了什么」：只有当前版本里的工具
// 才算存在，参数 schema 也取版本里的 TOOL.json —— 否则草稿改参数 / 新建工具会立刻影响线上。
// 管理端授权目录不要调用它（那里要看草稿）。
func (c *Catalog) WithDefinitionMode(mode tools.DefinitionMode) *Catalog {
	c.mode = mode
	return c
}

// WithCapabilities 注入平台能力视图（见 shared/capabilitypolicy.ResolveAll）。
//
// nil = 不限制；一旦传入，表里没有的能力一律视为未开通（缺省拒绝）。
// 只影响目录的可用性标记（置灰 + 原因），真正的拦截在执行层：
// 能力可能在两次调用之间被收回，已绑定工具的 agent 仍持有旧工具清单。
func (c *Catalog) WithCapabilities(allowed map[string]bool) *Catalog {
	c.caps = allowed
	return c
}

// capabilityBlocked 某能力是否被平台拦下（nil 视图 = 不拦）。
func (c *Catalog) capabilityBlocked(key string) (bool, string) {
	if c.caps == nil || key == "" || c.caps[key] {
		return false, ""
	}
	// 键名与中文名都写：前者便于在日志/接口里定位，后者给管理员看。
	return true, "平台未给本租户开通能力 " + key + "（" + capability.Label(key) + "），需平台管理员在租户能力中开通"
}

// policyMap 取本租户的生效策略表；第二个返回值 = 读取失败。
//
// 读失败不能按缺省处理：缺省是「开放」，而「读不出」不等于「没人停用过」。
// 调用方拿到 failed=true 时应把插件展示为**不可用**（保守方向），
// 真正的拦截仍在 Runner.Invoke（那里读失败直接报错）。
func (c *Catalog) policyMap() (map[string]pluginpolicy.Policy, bool) {
	if c.policies == nil {
		return map[string]pluginpolicy.Policy{}, false
	}
	all, err := c.policies(c.schema)
	if err != nil {
		xlog.Error("读取插件策略失败 schema=%s（按「已停用」展示）: %v", c.schema, err)
		return map[string]pluginpolicy.Policy{}, true
	}
	return all, false
}

func (c *Catalog) auth() *auth.AuthInfo { return &auth.AuthInfo{Schema: c.schema} }

// BuiltinCategories 内置类别集合（对应 builtin:<category> 组）。
//
// tools.CategorySuper 在列但**没有工具归属**：它是拆组前的聚合分类，只作为存量授权行
// （builtin:super）的解析入口存在，展开成五个管理组的并集（见 builtinByCategory）。
// 它不进 builtinGroupCategories，所以面板上不会多出一张与四组重复的卡片。
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

// BuiltinTools 全部内置工具（含分类）。
//
// 本函数**不做身份过滤** —— 它是「缺省全开」（超级智能体）与执行层判定的基线；
// 按视角过滤请用 (*Catalog).VisibleBuiltinTools。
func BuiltinTools() []ToolInfo {
	list := tools.List()
	out := make([]ToolInfo, 0, len(list))
	for _, t := range list {
		m := t.Meta()
		category := m.Category
		// 未标注分类：不进任何类别组（Groups 不产出、builtinByCategory 也不会命中），
		// Source 留空以免被误当成某个组名。
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

// VisibleBuiltinTools 按视角过滤后的内置工具（供管理端目录接口使用）。
func (c *Catalog) VisibleBuiltinTools() []ToolInfo { return c.builtinTools() }

// builtinTools 按视角过滤后的内置工具清单：非超级智能体剔除仅超级智能体可用的工具，
// 并按平台能力标可用性。
//
// 能力闸必须落在叶子上，而不是只在 Groups 里给组置灰：
// AllTools / ToolsOfRef / VisibleBuiltinTools 都从这里取工具，漏一个就会出现
// 「组显示未开通、工具却说可用」；Resolve 的可用性过滤也正好依赖叶子上的 Available。
func (c *Catalog) builtinTools() []ToolInfo {
	list := BuiltinTools()
	out := make([]ToolInfo, 0, len(list))
	for _, t := range list {
		if !c.isSupervisor && t.SupervisorOnly {
			continue
		}
		if blocked, why := c.capabilityBlocked(tools.CapabilityOf(builtinCategory(t))); blocked {
			t.Available = false
			t.UnavailableKind = UnavailableCapability
			t.UnavailableReason = why
		}
		out = append(out, t)
	}
	return out
}

// builtinCategory 内置工具的分类（来自 Source = "builtin:<category>"；未分类返回空串）。
func builtinCategory(t ToolInfo) string {
	return strings.TrimPrefix(t.Source, ScopeBuiltin+":")
}

// builtinByCategory 某内置类别下的工具（按视角过滤）。
//
// builtin:super 特判为「五个管理组的并集」：它是拆组前的聚合分类，现在没有工具归属它，
// 但存量授权行里可能还有 builtin:super —— 不展开的话那些 agent 会静默丢掉全部管理能力。
// 这段是过渡代码，等存量清干净后连同 tools.CategorySuper 一起删。
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

// ===== 连接与端点 =====

func (c *Catalog) mcpConns() ([]models.McpConnection, error) {
	list, _, err := c.mcp.List(c.auth(), &mcpconnections.ListMcpConnectionsRequest{Page: 1, Limit: listLimit})
	return list, err
}

func (c *Catalog) rpcConns() ([]models.RpcConnection, error) {
	list, _, err := c.rpc.List(c.auth(), &rpcconnections.ListRpcConnectionsRequest{Page: 1, Limit: listLimit})
	return list, err
}

// localRPCConns 本租户 protocol=rpc 且目标是工作流的端点。
//
// 只收 target_type=flow（见 LocalTargetFlow 的两条理由：agent 端点会绕过 call_agent
// 的闸门，且与 flow 同名时 ref / 工具名都会撞车）。
// 一处收窄，三条路径同时生效：ToolsOfRef(local:rpc[:name])、AllTools、Groups。
//
// 走 ListAll 而不是 List：这里要把整份端点点目录装进内存，List 的分页上限
// 会让第 1001 个端点起凭空消失（见 svcendpoints.ListAll 的注释）。
func (c *Catalog) localRPCConns() ([]models.SvcEndpoint, error) {
	return c.eps.ListAll(c.auth(), &svcendpoints.ListSvcEndpointsRequest{
		Protocol:   LocalProtocolRPC,
		TargetType: LocalTargetFlow,
	})
}

// ===== 磁盘插件 =====

// pluginTool 把一个已发现的磁盘插件转成工具叶子。
//
// Available 的口径 = manifest 存在 且 本租户已启用（{schema}.plugins.is_active）
// 且 平台没有停用（platform.tenant_plugins 只有显式 is_allowed=false 才算停用）。
// 没配置过的插件**也**会出现在目录里（available=false）：管理员得先看得见，
// 才知道有这个工具可以启用。
//
// 返回 ok=false 表示注册表里没这个工具（未发现 / 被重名规则丢弃）。
func (c *Catalog) pluginTool(name string) (ToolInfo, bool, error) {
	return c.pluginToolWith(name, nil)
}

// pluginToolWith 同 pluginTool，但可传入一次全取的策略表（批量路径用，避免 N 条查询）。
// pol == nil 表示自己按单个插件解析。
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
		// 读不到策略：按「已停用」展示。缺省虽是开放，但「读不出」不等于「没人停过」，
		// 此时若显示可用，用户会看到工具亮着却调不通。
		return ToolInfo{
			Ref:               ScopePlugin + ":" + m.Name,
			Name:              m.Name,
			Label:             m.DisplayName,
			Description:       m.Description,
			Available:         false,
			Source:            ScopePlugin + ":" + m.Name,
			UnavailableKind:   UnavailablePluginPolicy,
			UnavailableReason: "平台插件策略读取失败，暂按停用展示，请稍后重试",
			Parameters:        m.Parameters,
		}, true, nil
	}
	avail, kind, reason := pluginUnavailable(m.Name, policyOf(pol, m.Name), active)

	return ToolInfo{
		Ref:               ScopePlugin + ":" + m.Name,
		Name:              m.Name,
		Label:             m.DisplayName,
		Description:       m.Description,
		Available:         avail,
		Source:            ScopePlugin + ":" + m.Name,
		UnavailableKind:   kind,
		UnavailableReason: reason,
		Parameters:        m.Parameters,
	}, true, nil
}

// pluginUnavailable 插件工具的可用性与归因（纯逻辑，便于单测）。
//
// 两层闸分开报，且平台在前：平台停用时，租户启用也不会生效 ——
// 先报「未启用」会让租户去点启用，点完仍然不可用，白绕一圈。
// 「本租户被停用」与「全局停用」也要分开：前者只影响这一家，后者要等平台解闸。
func pluginUnavailable(name string, pol pluginpolicy.Policy, active bool) (avail bool, kind, reason string) {
	switch {
	case pol.Blocked:
		return false, UnavailablePluginPolicy,
			"插件 " + name + " 已被平台全局停用，需平台管理员在「插件与能力」中解除"
	case !pol.Allowed:
		return false, UnavailablePluginPolicy,
			"插件 " + name + " 已被平台对本租户停用，需平台管理员在「插件与能力」中恢复"
	case !active:
		return false, UnavailablePluginInactive,
			"租户未启用插件 " + name + "，请在「工具与端点 → 插件」中启用并选择实现"
	}
	return true, "", ""
}

// policyOf 从「一次全取」的结果里取策略；表里没有 = 开放（与 pluginpolicy.Default 一致）。
func policyOf(all map[string]pluginpolicy.Policy, name string) pluginpolicy.Policy {
	if p, ok := all[name]; ok {
		return p
	}
	return pluginpolicy.Default()
}

// pluginTools 全部已发现的插件工具（按名排序，注册表本身已排序）。
func (c *Catalog) pluginTools() ([]ToolInfo, error) {
	reg := plugin.DefaultSource()
	if reg == nil {
		return nil, nil
	}
	snap := reg.Snapshot()
	pols, failed := c.policyMap()
	if failed {
		// 一次全取失败时逐个插件补「已停用」，避免空表 + 缺省开放 = 全部显示可用
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

// mcpRefreshedAt / rpcRefreshedAt 该连接工具清单的最后刷新时间（无工具时为空串）。
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

// maxTime 返回最新时间（RFC3339）；空列表 / 零值返回空串。
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

// toolSchema 把连接工具的 input_schema 转成可递给模型的参数对象。
//
// 必须在这里透传：内置工具的参数不给也没事（agent 侧现取 Meta().Parameters），
// 但 mcp / rpc 工具没有那个兜底 —— 缺了它模型看到的是一个「无参数」的工具，
// 要么不敢调，要么瞎编参数撞 required 校验。
//
// 两种载体都要认：管理端经 gin 绑定写库的是 map[string]any，
// rpc/client 的发现逻辑写的是 json.RawMessage（见 conn.go 的 tools/list 解析）。
// xsql.JSONB.Unmarshal 对两者都按 JSON 处理。
//
// 顶层不是 object 的 schema 直接回退（上游会拒，或让模型以为工具不吃参数）：
// 与 rpc/server listTools 的 isObjectSchema 判据同向。
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
	// 缺 type 时补上：模型侧要求的形状是 object，缺了部分上游会拒整次请求。
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	return m
}

// customTools 本租户可用的自定义工具（status=1）。
//
// 读失败**降级为空**并记日志：目录是展示/绑定面，读失败不该让整棵工具树消失；
// 真正的拦截在执行层（ExecuteTool → executeCustomTool），那里读失败直接报错。
func (c *Catalog) customTools() []ToolInfo {
	if c.scripts == nil || c.schema == "" {
		return nil
	}
	// 正式运行走「版本视图」：没有当前版本的工具不算存在，参数 schema 取版本里的契约。
	// 与执行层（ToolDir / CustomTools.Get）同源，否则会出现「模型按草稿参数调用、脚本是版本里的」。
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
		xlog.Error("读取自定义工具失败 schema=%s（按无工具处理）: %v", c.schema, err)
		return nil
	}
	// 能力闸提到叶子上：自定义工具走 /v1/exec（一次性进程），归 sandbox.exec 档，
	// 平台没开通就置灰 —— 判据必须与执行层（tools/executeCustomTool）同向，
	// 否则会出现「目录说可用、调用时被拒」。
	// 组级原因由 Groups 从叶子抄过去，判据只留这一处。
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
			ti.Available = false
			ti.UnavailableKind, ti.UnavailableReason = UnavailableCapability, why
		}
		out = append(out, ti)
	}
	return out
}

// ===== ref 展开 =====

// ToolsOfRef 把 ref 展开为工具清单（组 → 成员；单个工具 → 自身）。
// 引用不存在（连接/端点/组/工具已删除）时返回空列表，不报错。
// 非超级智能体视角下，分类为 super 的内置工具不会出现在结果里（含单个 ref 与组展开）。
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
				// 工作流入参就是工具入参：缺了它模型看到的是「无参数工具」，
				// 要么不敢调，要么瞎编参数（与 mcp / rpc 分支同口径）。
				Parameters: toolSchema(ep.InputSchema),
			}
			if !ti.Available {
				ti.UnavailableKind, ti.UnavailableReason = endpointIssue(ep.Name)
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

// expandItems 展开组成员 / 预设组成员（成员只能是天然组或单个工具，不会嵌套自定义组）。
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
			t.Source = source // 记录来源组，便于 UI 展示"该工具来自哪个组"
			out = append(out, t)
		}
	}
	return out, nil
}

// toolsOfConnection 展开 mcp:<conn>[:tool] / rpc:<conn>[:tool]。
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
					ti.UnavailableKind, ti.UnavailableReason = mcpConnIssue(conn.Name)
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
				ti.UnavailableKind, ti.UnavailableReason = rpcConnIssue(conn.Name)
			}
			out = append(out, ti)
		}
		return out
	}
	return out
}

// AllTools 全部可用工具（内置 + 插件 + 自定义工具 + 外部连接 + 本地 rpc 端点），按 ref 索引。
//
// 这里是 Resolver 生成最终工具集的唯一来源：**任何一类工具漏进来，即使授权解析成功，
// 也会在最后一步被丢掉并报「引用的工具已不存在」**（自定义工具就踩过：ToolsOfRef 能展开，
// 但 AllTools 缺这一类，于是授权看似保存成功、agent 永远看不见）。
func (c *Catalog) AllTools() (map[string]ToolInfo, error) {
	out := map[string]ToolInfo{}
	for _, t := range c.builtinTools() {
		out[t.Ref] = t
	}

	// 内置类组不单独入表（组不是工具），这里只放叶子工具

	plugins, err := c.pluginTools()
	if err != nil {
		return nil, err
	}
	for _, t := range plugins {
		out[t.Ref] = t
	}

	// 自定义工具：归属判据与执行层（executeCustomTool）同向 —— 一律按叶子入表，
	// 平台未开通 exec 时由 customTools() 标成不可用（Resolver 会按「平台未开通」告警并剔除）。
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
				ti.UnavailableKind, ti.UnavailableReason = mcpConnIssue(conn.Name)
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
				ti.UnavailableKind, ti.UnavailableReason = rpcConnIssue(conn.Name)
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
			ti.UnavailableKind, ti.UnavailableReason = endpointIssue(ep.Name)
		}
		out[ref] = ti
	}
	return out, nil
}

// builtinGroupCategories Groups() 产出的内置类别（顺序即展示顺序）。
//
// 必须是**显式清单**：漏掉一个类别，该组就在授权面板上整个消失
// （前端只渲染 groups，工具是作为组内成员出现的），而且不会有任何报错。
// builtinByCategory 按 Source 比对，不遍历 BuiltinCategories 的键，所以清单即真源；
// TestBuiltinGroupCategoriesCoverTools 守住「有工具的类别不能漏」。
//
// CategoryScript 不在此列：它没有任何内置工具归属（run_skill_script 已归 exec），
// 列了也会因空成员被跳过。将来该分类有了工具，上面那条测试会先失败提醒补进来。
//
// CategorySuper 也不在此列：它没有工具归属，只是 builtin:super 的兼容展开入口
// （见 builtinByCategory）。列进来会多出一张与五个管理组重复的卡片。
var builtinGroupCategories = []string{
	tools.CategoryFile, tools.CategoryExec, tools.CategoryShell, tools.CategoryBrowser,
	tools.CategoryAgent, tools.CategorySkill, tools.CategorySkillOwn,
	tools.CategoryAgentAdmin, tools.CategorySkillAdmin, tools.CategoryToolGrant, tools.CategoryCustomTool,
	tools.CategoryFlowAdmin,
}

// Groups 可引用组目录（内置类 / 连接 / 本地端点 / 预设组 / 自定义组）。
//
// 按视角产出：非超级智能体视角下，内置类中的 builtin:super（含其成员）整组不出现，
// 成员被过滤光的预设组 / 自定义组也一并消失。
func (c *Catalog) Groups() ([]GroupInfo, error) {
	out := make([]GroupInfo, 0)

	// 内置类
	for _, category := range builtinGroupCategories {
		items := c.builtinByCategory(category)
		if len(items) == 0 {
			continue
		}
		// 平台能力闸：exec / shell / browser 三类按租户能力开关决定可用性。
		// 叶子已经由 builtinTools 标好（判据只有一处，避免组与工具漂移），
		// 这里只把结论提到组上，供前端卡片直接读。
		available, kind, reason := true, "", ""
		if blocked, why := c.capabilityBlocked(tools.CapabilityOf(category)); blocked {
			available, kind, reason = false, UnavailableCapability, why
		}
		out = append(out, GroupInfo{
			Ref: ScopeBuiltin + ":" + category, Name: "内置-" + category, Kind: ScopeBuiltin,
			Description: builtinCategoryLabel(category), Available: available, Items: items,
			UnavailableKind: kind, UnavailableReason: reason,
		})
	}

	// 预设组
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

	// 磁盘插件：同类合成一张「类别卡」，容器本身不可授权（Synthetic）。
	// 一个工具一张卡会把组名退化成工具名（"搜索"组），既难认也让 item_type 被记成 group。
	pluginItems, err := c.pluginTools()
	if err != nil {
		return nil, err
	}
	if g, ok := categoryGroup(ScopePlugin, "插件",
		"磁盘上的 Python 工具插件（一工具多实现，按租户启用配置）", pluginItems); ok {
		out = append(out, g)
	}

	// 自定义工具：同上（租户自己的代码，数量随租户变化，尤其不适合一个工具一张卡）。
	if g, ok := categoryGroup(ScopeCustom, "自定义工具",
		"本租户自建的代码工具（授权 ref = custom:<名>，模型侧工具名 custom__<名>）", c.customTools()); ok {
		out = append(out, g)
	}

	// 外部 MCP 连接
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
			g.UnavailableKind, g.UnavailableReason = mcpConnIssue(conn.Name)
		}
		out = append(out, g)
	}

	// 外部 RPC 连接
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
			g.UnavailableKind, g.UnavailableReason = rpcConnIssue(conn.Name)
		}
		out = append(out, g)
	}

	// 本地 rpc 端点
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
			Ref: ScopeLocal + ":" + LocalProtocolRPC, Name: "本地 RPC 端点", Kind: ScopeLocal,
			Description: "本租户工作流的 rpc 协议端点（agent 之间的调用请走 call_agent）",
			Available:   true, Items: items, RefreshedAt: maxTime(times),
		})
	}

	// 工具组（租户自定义）
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

// categoryGroup 把同一 scope 的若干工具合成一张类别卡。
//
// 为什么需要它：授权面板只渲染组卡片，工具必须作为「组内成员」出现；而插件 / 自定义工具
// 的成员是**租户侧实体**（数量随租户变化），一工具一卡会让组名退化成工具名
// （"利息终值计算"组），既难认，又把 item_type 误导成 group —— 看着像授权了一个组，
// 实际只是那一个工具。
//
// 这张卡是**容器**：Synthetic=true，前端不给整组勾选，勾选落点是成员自己的工具 ref。
// ref 用 "<scope>:*" 只当前端 key / 展示用 —— ParseRef 会拒 "*"，不可能是有效授权 ref。
func categoryGroup(scope, name, desc string, items []ToolInfo) (GroupInfo, bool) {
	if len(items) == 0 {
		return GroupInfo{}, false
	}
	g := GroupInfo{
		Ref: scope + ":*", Name: name, Kind: scope, Description: desc,
		Available: true, Items: items, Synthetic: true,
	}
	// 组级可用性从叶子抄（判据只留叶子一处）：任一成员不可用 → 卡片也标不可用，
	// 原因取第一条，与内置类组的做法一致。
	for _, t := range items {
		if !t.Available {
			g.Available = false
			g.UnavailableKind, g.UnavailableReason = t.UnavailableKind, t.UnavailableReason
			break
		}
	}
	return g, true
}

func builtinCategoryLabel(category string) string {
	switch category {
	case tools.CategoryFile:
		return "文件类：读写与浏览工作区文件"
	case tools.CategoryExec:
		return "一次性执行：每条命令 / 脚本新起进程、起完即退（无状态；含技能脚本 run_skill_script）"
	case tools.CategoryShell:
		return "常驻会话：复用长驻进程，cwd / 环境变量 / 后台任务跨调用保留"
	case tools.CategoryScript:
		return "技能脚本：只跑注入给你的技能目录内的脚本（/skill 只读挂载）"
	case tools.CategoryBrowser:
		return "浏览器类：在沙箱内操作网页（可出网、能读页面内容，高风险）"
	case tools.CategoryAgent:
		return "智能体类：查看 / 调用其它 agent"
	case tools.CategorySkill:
		return "技能类：列出 / 查看注入给你的 skill（正文用 read_skill 或 use_skill 按需读；use_skill 会激活其工具依赖）"
	case tools.CategorySkillOwn:
		return "私有技能：创建 / 修改 / 删除**自己的** skill（写的是自己的能力面，默认不授）"
	case tools.CategoryAgentAdmin:
		return "智能体管理（仅超级智能体）：创建 / 修改 / 删除 agent，以及给它绑定技能"
	case tools.CategorySkillAdmin:
		return "技能管理（仅超级智能体）：创建 / 修改 / 删除**共享（租户级）** skill，以及为他 agent 创建 / 改 / 删**私有** skill"
	case tools.CategoryToolGrant:
		return "授权管理（仅超级智能体）：授予 / 收回 agent 的工具，以及 agent 之间的调用关系"
	case tools.CategoryCustomTool:
		return "自定义工具管理（仅超级智能体）：创建 / 修改 / 删除 / 列出本租户的自定义工具"
	case tools.CategoryFlowAdmin:
		return "工作流管理（仅超级智能体）：创建 / 列出 / 读取 / 删除 / 试运行工作流、查运行记录，并可把流程对外开放成 mcp / rpc 端点"
	case tools.CategorySuper:
		return "超级智能体专属（过渡聚合组，等价于五个管理组的并集）"
	default:
		return "未分类的内置工具"
	}
}

func displayName(display, fallback string) string {
	if strings.TrimSpace(display) != "" {
		return display
	}
	return fallback
}

// 外部工具的不可用归因：三者的修复入口都在租户自己手里（连接 / 端点各自的管理页），
// 所以原因里直接写「该去哪开」。这类工具最常见的困惑就是看不出连接是停用的。
func mcpConnIssue(name string) (kind, reason string) {
	return UnavailableConnection, "MCP 连接 " + name + " 未启用，请在「设置 → MCP 连接」中启用"
}

func rpcConnIssue(name string) (kind, reason string) {
	return UnavailableConnection, "RPC 连接 " + name + " 未启用，请在「设置 → RPC 连接」中启用"
}

func endpointIssue(name string) (kind, reason string) {
	return UnavailableEndpoint, "端点 " + name + " 未对外开放，请在「端点」页把它的「对外」打开"
}
