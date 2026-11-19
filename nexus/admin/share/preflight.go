package share

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	globalvarsrepo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// globalVarRepo 全局变量存储（只用于「包里声明的依赖在不在」这一件事）
var globalVarRepo = globalvarsrepo.NewRepository()

// depItem 一条依赖的检查结论。
type depItem struct {
	Kind string `json:"kind"` // skill | tool | agent | capability | global_var
	// Scope 工具依赖的来源类型：custom | mcp | rpc | local | builtin | plugin | group | preset。
	// 技能 / 智能体 / 能力三类留空（Kind 已经说明）。
	Scope string `json:"scope,omitempty"`
	Ref   string `json:"ref"`
	// Via 这条依赖是谁要的：空 = 实体自己声明的；"skill:foo" = 技能 foo 声明的工具依赖。
	Via string `json:"via,omitempty"`
	// State 取值见 dep* 常量。
	State string `json:"state"`
	// Total / Items 组引用的展开结果（Items 只取前 8 个，前端展示用）。
	Total  int      `json:"total,omitempty"`
	Items  []string `json:"items,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

const (
	depOK         = "ok"         // 本租户已有且可用
	depBundled    = "bundled"    // 随包自带，安装时一并装上
	depDisabled   = "disabled"   // 存在但当前不可用（连接停用 / 端点未开放 / 能力未开通 / 工具停用）
	depDeprecated = "deprecated" // 存在但已废弃
	depMissing    = "missing"    // 本租户没有
)

// depItemsShown 组引用最多展示几个成员名（清单是给人看的，不是给机器读的）。
const depItemsShown = 8

// refScopeLabel ref 前缀 → 给人看的类型名（与授权面板的措辞一致）。
var refScopeLabel = map[string]string{
	toolgrant.ScopeBuiltin: "内置工具",
	toolgrant.ScopeMCP:     "MCP 工具",
	toolgrant.ScopeRPC:     "RPC 工具",
	toolgrant.ScopeLocal:   "本地端点工具",
	toolgrant.ScopePlugin:  "插件工具",
	toolgrant.ScopeCustom:  "自定义工具",
	toolgrant.ScopeGroup:   "工具组",
	toolgrant.ScopePreset:  "预设组",
	"global_var":           "全局变量",
}

// Inspect POST /admin/share/import/inspect
//
// 装之前给一份「这份包依赖了什么、本租户有没有」。安装本身只会把缺的东西写进 warnings
// （技能缺失跳过绑定、工具 ref 缺失就指向不存在的工具），用户是装完之后才看到「3 项没生效」。
// 依赖清单在包里是静态的（manifest.deps + payload），完全可以在动手前核对一遍。
//
// 只读：不铺临时目录、不落平台行、不写租户 schema。
func Inspect(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	pkg, files, kind, ok := readUploadedPackage(c)
	if !ok {
		return
	}

	payload := map[string]any{}
	if jerr := json.Unmarshal(pkg.Payload, &payload); jerr != nil {
		payload = map[string]any{}
	}
	// 与 Import 同一份脱敏：依赖检查要按「真正会装下去的 payload」看，
	// 否则预检看到的身份（比如超级智能体开关）与实际装出来的可能不是同一个。
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = pkg.Manifest.Name
	}

	deps := inspectDeps(info, pkg.Manifest, clean, files)
	deps = append(deps, inspectCapabilities(info, pkg.Manifest.Deps.Requires)...)
	missing := 0
	for _, d := range deps {
		if d.State == depMissing {
			missing++
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"kind":     string(kind),
		"name":     name,
		"deps":     deps,
		"summary":  depSummary(deps),
		"missing":  missing,
		"existing": existingEntity(info, kind, name),
		"bundled":  bundledSummary(pkg.Manifest),
		"notices":  secretWarnings,
		"notes":    inspectNotes(kind),
		// 内容指纹 / 清单语义版本：老包为空或 0，前端只在有值时展示
		"digest": pkg.Manifest.Digest,
		"spec":   pkg.Manifest.Spec,
	}, "")
}

// inspectInstallRequest 市场安装前的预检入参。
type inspectInstallRequest struct {
	AssetID string `json:"asset_id"`
	Name    string `json:"name"`
}

// InspectAsset POST /admin/share/install/inspect
//
// 与 Inspect（上传包预检）同源：装之前先摆出「本租户已有同名实体吗、包里要的东西有没有」。
// 市场点「安装」之前就该看到这些，而不是等装完去 warnings 里数缺了几项。
// 只读：不铺目录、不写租户 schema、不写安装流水。
func InspectAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var req inspectInstallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	asset, m, kind, name, rerr := resolveInstallTarget(info, req.AssetID, req.Name)
	if rerr != nil {
		xhttp.ErrJSON(c, rerr.Error(), nil, "")
		return
	}
	// 与 Import 同一份脱敏：按「真正会装下去的 payload」看，否则超级智能体之类的身份会看错
	payload, notices := assetpack.StripSecrets(kind, payloadFromAsset(asset))

	deps := inspectDeps(info, m, payload, nil)
	deps = append(deps, inspectCapabilities(info, m.Deps.Requires)...)
	missing := 0
	for _, d := range deps {
		if d.State == depMissing {
			missing++
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"kind":     string(kind),
		"name":     name,
		"deps":     deps,
		"summary":  depSummary(deps),
		"missing":  missing,
		"existing": existingEntity(info, kind, name),
		"bundled":  bundledSummary(m),
		"notices":  notices,
		"notes":    inspectNotes(kind),
		// 内容指纹 / 清单语义版本：上架时算好并跟着 manifest 走；本机旧资产可能为空
		"digest": m.Digest,
		"spec":   m.Spec,
	}, "")
}

// inspectDeps 逐条对照本租户现状。
//
// 工具依赖按 ref 前缀分四类查（见 refScopeLabel）：组引用查组级，单工具查叶子。
// 只覆盖包里声明过的依赖 —— 运行期动态选择（模型临时决定用哪个技能 / 工具）本来就
// 无法静态判定，宁可少说也不假装知道（这一点由 notes 明说）。
func inspectDeps(info *auth.AuthInfo, m assetpack.Manifest, payload map[string]any,
	files map[string][]byte) []depItem {

	items := make([]depItem, 0, len(m.Deps.Skills)*2+len(m.Deps.Tools)+len(m.Deps.Agents))
	attachments := assetpack.Attachments(m.Files)

	// 目录的视角要跟「装完之后这个实体是什么身份」一致：supervisor 才看得见管理类组，
	// 否则每个管理组都会被误判成缺失。
	cat := toolgrant.NewCatalog(info.Schema, grantRepo, flagFrom(payload, "is_supervisor", false))

	// 组级状态表：只在真有组类依赖时构建（Groups() 会逐条 MCP / RPC 连接去取工具清单）
	var groups map[string]toolgrant.GroupInfo
	if needsGroups(m.Deps.Tools) {
		if gs, gerr := cat.Groups(); gerr == nil {
			groups = make(map[string]toolgrant.GroupInfo, len(gs))
			for _, g := range gs {
				groups[g.Ref] = g
			}
		}
	}

	// 技能：先判本租户有没有，再把「这个技能自己声明的工具」摊成传递依赖 ——
	// 技能在、但它要的工具不在，运行期一样注入不出那些工具。
	for _, sname := range m.Deps.Skills {
		skill := inspectSkill(info, sname)
		items = append(items, skill)
		if skill.State != depOK {
			continue
		}
		s, serr := skillRepo.FindByName(info.Schema, sname, "")
		if serr != nil || s == nil {
			continue
		}
		grants, gerr := grantRepo.ListSkillGrants(info.Schema, string(s.ID))
		if gerr != nil {
			continue
		}
		for _, g := range grants {
			item := inspectTool(cat, groups, assetpack.ToolDep{Type: g.ItemType, Ref: g.Ref}, attachments)
			item.Via = "skill:" + sname
			items = append(items, item)
		}
	}

	// 实体自己声明的工具依赖
	for _, d := range m.Deps.Tools {
		items = append(items, inspectTool(cat, groups, d, attachments))
	}

	for _, aname := range m.Deps.Agents {
		items = append(items, inspectCalledAgent(info, aname))
	}

	// 全局变量：包里只有名字，值不可能随包走 —— 缺哪个必须装之前说清，
	// 否则表现是「装完一切正常，第一次运行节点才失败」。
	for _, name := range m.Deps.GlobalVars {
		items = append(items, inspectGlobalVar(info, name))
	}
	return items
}

// inspectGlobalVar 检查一条全局变量依赖（{schema}.global_vars 里有没有这个名字）。
//
// 只判存在性，不判值：空值也是用户的选择（比如故意的空串），
// 而「值为空」与「变量不存在」在运行期的表现完全不同（前者能跑，后者硬失败）。
func inspectGlobalVar(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "global_var", Ref: name}
	v, err := globalVarRepo.Get(info.Schema, name)
	if err != nil {
		item.State, item.Detail = depMissing, "读取全局变量失败："+err.Error()
		return item
	}
	if v == nil {
		item.State = depMissing
		item.Detail = "本租户没有这个全局变量，先在「设置 → 全局变量」创建"
		return item
	}
	item.State = depOK
	item.Detail = "本租户已有"
	if v.Kind == "secret" {
		// 说清楚「有」不等于「值对」：导入方看到的是自己的值
		item.Detail = "本租户已有（密钥类型：用的是你这里的值，不随包迁移）"
	}
	return item
}

// needsGroups 是否需要组级状态表（只有组形态的 ref 才需要）。
func needsGroups(deps []assetpack.ToolDep) bool {
	for _, d := range deps {
		if p, err := toolgrant.ParseRef(d.Ref); err == nil && p.IsGroup(toolgrant.BuiltinCategories()) {
			return true
		}
	}
	return false
}

// inspectTool 检查一条工具依赖。
//
// 分支顺序有讲究：
//  1. 随包自带的自定义工具（包里就有本体，装完即存在）—— 先认，别报成缺失；
//  2. 组形态 → 查组级状态（存在 / 启用 / 展开出几个成员）；
//  3. 单工具形态 → 查叶子（存在 / 可用 / 不可用原因）。
func inspectTool(cat *toolgrant.Catalog, groups map[string]toolgrant.GroupInfo,
	d assetpack.ToolDep, attachments map[string][]assetpack.FileEntry) depItem {

	item := depItem{Kind: "tool", Ref: d.Ref}

	p, perr := toolgrant.ParseRef(d.Ref)
	if perr != nil {
		item.State, item.Detail = depMissing, "引用无法解析："+perr.Error()
		return item
	}
	item.Scope = p.Scope

	if p.Scope == toolgrant.ScopeCustom {
		if _, ok := attachments[p.Subject]; ok {
			item.State = depBundled
			item.Detail = "随包自带：安装时一并装上（启用态），配置项需自行填写"
			return item
		}
	}

	if p.IsGroup(toolgrant.BuiltinCategories()) {
		return inspectToolGroup(groups, item, d.GrantKind)
	}

	list, err := cat.ToolsOfRef(d.Ref)
	if err != nil {
		item.State, item.Detail = depMissing, "展开失败："+err.Error()
		return item
	}
	if len(list) == 0 {
		item.State = depMissing
		item.Detail = missingRefHint(item.Scope, p.Subject)
		return item
	}
	leaf := list[0]
	if !leaf.Available {
		item.State, item.Detail = depDisabled, unavailableText(leaf)
		return item
	}
	item.State, item.Total = depOK, 1
	item.Items = []string{leaf.Name}
	item.Detail = "本租户已有" + refScopeLabel[item.Scope] + "：" + leaf.Name
	if d.GrantKind != "" {
		item.Detail += "；授权方式：" + grantKindLabel(d.GrantKind)
	}
	return item
}

// inspectToolGroup 组引用（mcp:fs / rpc:erp / local:rpc / group:x / preset:x / builtin:file）。
//
// 必须查 Groups() 而不是只看 ToolsOfRef：连接存在但未启用时，ToolsOfRef 照样返回成员
// （Available=false），而连接根本不存在时返回空列表 —— 这两件事要能分开说，
// 否则用户会去改错的那一层（改连接配置 vs 新建连接）。
func inspectToolGroup(groups map[string]toolgrant.GroupInfo, item depItem, grantKind string) depItem {
	g, ok := groups[item.Ref]
	if !ok {
		item.State = depMissing
		item.Detail = missingRefHint(item.Scope, strings.SplitN(item.Ref, ":", 2)[1])
		return item
	}

	item.Total = len(g.Items)
	for i, t := range g.Items {
		if i >= depItemsShown {
			break
		}
		item.Items = append(item.Items, t.Name)
	}
	if !g.Available {
		item.State = depDisabled
		item.Detail = strings.TrimSpace(g.UnavailableReason)
		if item.Detail == "" {
			item.Detail = "该" + refScopeLabel[item.Scope] + "当前不可用"
		}
		if item.Total > 0 {
			item.Detail += fmt.Sprintf("（组内 %d 个工具）", item.Total)
		}
		return item
	}
	if item.Total == 0 {
		item.State = depDisabled
		item.Detail = "组存在，但当前展开不出任何工具（连接里没有工具 / 组内成员都被删了）"
		return item
	}
	item.State = depOK
	item.Detail = fmt.Sprintf("本租户已有该%s，展开 %d 个工具", refScopeLabel[item.Scope], item.Total)
	// 混装组（自定义组常见）把成员来源说清楚：一部分来自 MCP 连接、一部分是租户自己写的脚本
	if src := sourceBreakdown(g.Items); src != "" {
		item.Detail += "：" + src
	}
	if grantKind != "" {
		item.Detail += "；授权方式：" + grantKindLabel(grantKind)
	}
	return item
}

func inspectSkill(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "skill", Ref: name}
	s, err := skillRepo.FindByName(info.Schema, name, "")
	if err != nil {
		item.State, item.Detail = depMissing, "查询失败："+err.Error()
		return item
	}
	if s == nil {
		item.State = depMissing
		item.Detail = "本租户没有同名租户级技能：装上后这条绑定跳过，需要先建同名技能（或装完手动绑定）"
		return item
	}
	if int(s.Status) == models.StatusDeprecated {
		item.State, item.Detail = depDeprecated, "同名技能已废弃：装上后会跳过绑定，先恢复该技能再重试"
		return item
	}
	item.State, item.Detail = depOK, "本租户已有同名技能，安装后按名字自动绑定"
	return item
}

func inspectCalledAgent(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "agent", Ref: name}
	a, err := agentRepo.FindByName(info, name)
	if err != nil {
		item.State, item.Detail = depMissing, "查询失败："+err.Error()
		return item
	}
	if a == nil {
		item.State = depMissing
		item.Detail = "本租户没有同名智能体：调用授权无法重建，装完要先建这个智能体"
		return item
	}
	if int(a.Status) == models.StatusDeprecated {
		item.State, item.Detail = depDeprecated, "同名智能体已废弃：即便重新授权也调不通"
		return item
	}
	item.State, item.Detail = depOK, "本租户已有同名智能体（调用授权要单独重配，不会自动重建）"
	return item
}

// inspectCapabilities 包声明的能力要求（sandbox.exec / net.egress）对照本租户是否开通。
func inspectCapabilities(info *auth.AuthInfo, requires []string) []depItem {
	if len(requires) == 0 {
		return nil
	}
	caps := capabilitypolicy.Snapshot(info.Schema)
	out := make([]depItem, 0, len(requires))
	for _, key := range requires {
		item := depItem{Kind: "capability", Ref: key}
		if caps[key] {
			item.State, item.Detail = depOK, "本租户已开通"
		} else {
			item.State = depMissing
			item.Detail = "本租户未开通「" + capability.Label(key) + "」：依赖它的工具装上也无法运行，需平台管理员开通"
		}
		out = append(out, item)
	}
	return out
}

// existingEntity 同名实体是否已存在（前端据此提前提示「会覆盖」还是「要改名」）。
func existingEntity(info *auth.AuthInfo, kind assetpack.Kind, name string) any {
	switch kind {
	case assetpack.KindAgent:
		if a, err := agentRepo.FindByName(info, name); err == nil && a != nil {
			return gin.H{"name": a.Name, "status": int(a.Status), "can_overwrite": true}
		}
	case assetpack.KindSkill:
		// 只认租户级同名技能：私有技能是另一个名字空间，不算冲突（覆盖也碰不到它）
		if s, err := skillRepo.FindByName(info.Schema, name, ""); err == nil && s != nil {
			return gin.H{"name": s.Name, "status": int(s.Status), "can_overwrite": supportsOverwrite(assetpack.KindSkill)}
		}
	case assetpack.KindTool:
		// 自定义工具的「存在」以目录为准（与执行层、授权目录同源）
		if _, err := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema).Get("", name); err == nil {
			return gin.H{"name": name, "can_overwrite": supportsOverwrite(assetpack.KindTool)}
		}
	case assetpack.KindWorkflow:
		if ok, err := flowRepo.ExistsFlowName(info, name); err == nil && ok {
			return gin.H{"name": name, "can_overwrite": supportsOverwrite(assetpack.KindWorkflow)}
		}
	}
	return nil
}

// bundledSummary 包内自带、安装时一并装上的东西（不是外部依赖，但用户要看得到）。
func bundledSummary(m assetpack.Manifest) gin.H {
	tools := make([]string, 0)
	for name := range assetpack.Attachments(m.Files) {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	privates := append([]string{}, m.Deps.PrivateSkills...)
	sort.Strings(privates)
	return gin.H{"custom_tools": tools, "private_skills": privates}
}

// depSummary 按类型汇总（前端可以渲染成「MCP 3 项（1 缺失）」）。
func depSummary(deps []depItem) []gin.H {
	type tally struct{ total, missing int }
	byKey := map[string]*tally{}
	order := make([]string, 0, 8)
	for _, d := range deps {
		key := d.Kind
		if d.Kind == "tool" && d.Scope != "" {
			key = d.Scope
		}
		t, ok := byKey[key]
		if !ok {
			t = &tally{}
			byKey[key] = t
			order = append(order, key)
		}
		t.total++
		if d.State == depMissing {
			t.missing++
		}
	}
	out := make([]gin.H, 0, len(order))
	for _, key := range order {
		label := refScopeLabel[key]
		if label == "" {
			label = key
		}
		t := byKey[key]
		out = append(out, gin.H{"key": key, "label": label, "total": t.total, "missing": t.missing})
	}
	return out
}

func inspectNotes(kind assetpack.Kind) []string {
	notes := []string{"依赖检查只覆盖包里声明过的项；运行期动态选择（模型临时决定用哪个技能 / 工具）无法静态判定"}
	if kind == assetpack.KindWorkflow {
		notes = append(notes,
			"工作流节点里的模型 / 工具 / 子流程引用不记在包里，装完要在编辑器里逐项核对")
	}
	return notes
}

// sourceBreakdown 组内成员按来源归类，例如 "mcp:fs×3、custom:foo×1"。
func sourceBreakdown(items []toolgrant.ToolInfo) string {
	count := map[string]int{}
	for _, t := range items {
		src := t.Source
		if src == "" {
			src = "unknown"
		}
		count[src]++
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, count[k]))
	}
	return strings.Join(parts, "、")
}

// missingRefHint 缺失时给一句能照做的话（按依赖类型分别说，别一律「不存在」）。
func missingRefHint(scope, subject string) string {
	switch scope {
	case toolgrant.ScopeMCP:
		return "MCP 连接「" + subject + "」不存在（没配 / 已删）：装上后这条授权不会生效"
	case toolgrant.ScopeRPC:
		return "RPC 连接「" + subject + "」不存在（没配 / 已删）：装上后这条授权不会生效"
	case toolgrant.ScopeLocal:
		return "本租户没有名为「" + subject + "」的 rpc 端点（工作流要先「对外开放」rpc 协议）"
	case toolgrant.ScopeCustom:
		return "本租户没有这个自定义工具：先建同名工具，或删掉这条授权"
	case toolgrant.ScopePlugin:
		return "本机插件注册表里没有「" + subject + "」（未安装 / 被重名规则丢弃）"
	case toolgrant.ScopeGroup:
		return "本租户没有这个自定义工具组（组不存在，或组内工具都已被删除）"
	case toolgrant.ScopePreset:
		return "本机版本没有这个预设组"
	default:
		return "本机版本里没有这个引用"
	}
}

// unavailableText 统一「存在但不可用」的文案（原因已经在 ToolInfo 里分好类了）。
func unavailableText(t toolgrant.ToolInfo) string {
	reason := strings.TrimSpace(t.UnavailableReason)
	if reason == "" {
		reason = "该工具当前不可用"
	}
	return t.Name + " 不可用：" + reason
}

func grantKindLabel(kind string) string {
	switch kind {
	case models.GrantKindRequired:
		return "required（必给）"
	case models.GrantKindDeny:
		return "deny（禁止）"
	default:
		return "allow（按技能声明放行）"
	}
}
