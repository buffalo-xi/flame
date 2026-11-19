// Package toolgrants 暴露工具授权体系的管理接口：
//   - GET  /admin/toolcatalog/                     可引用目录（天然组 / 预设组 / 自定义组 / 内置工具）
//   - GET  /admin/agents/:id/toolgrants            某 agent 的授权项 + 不限制开关
//   - PUT  /admin/agents/:id/toolgrants            全量替换授权项（含不限制开关）
//   - GET  /admin/agents/:id/effective-tools       实际生效工具（含来源与告警）
//   - GET  /admin/toolgroups/                      自定义组列表
//   - POST /admin/toolgroups/                      创建/更新自定义组（按 name upsert）
//   - DELETE /admin/toolgroups/:name               删除自定义组（带引用检查）
//   - GET  /admin/toolgrants/usages                某 ref 的引用明细（删除/下线前的影响面查询）
package toolgrants

import (
	"fmt"
	"sort"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/models"
	agentsrepo "flame/nexus/shared/repository/agents"
	skillsrepo "flame/nexus/shared/repository/skills"
	repo "flame/nexus/shared/repository/toolgrants"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

var (
	grantsRepo = repo.NewRepository()
	skillRepo  = skillsrepo.NewRepository()
	agentsRepo = agentsrepo.NewRepository()
)

// authOf 取当前请求的认证信息（失败时已写响应）。
func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrJSON(c, "Authentication info invalid", nil, "")
		return nil, false
	}
	return info, true
}

// agentIsSupervisor 判断某 agent 是否带超权属性（agents.is_supervisor）。
// 查不到（或出错）按「非 supervisor」处理，收紧默认 —— 目录会据此隐藏
// 「仅 supervisor 可用」的管理类工具（分类 super）。
func agentIsSupervisor(info *auth.AuthInfo, agentID string) bool {
	inst, err := agentsrepo.NewRepository().GetAgent(info, agentID)
	return err == nil && inst != nil && inst.IsSupervisor
}

// ListToolCatalog 可引用目录：天然组 / 预设组 / 自定义组（含各自展开后的成员）+ 全部内置工具。
//
// 可选 query agent_id：给定时按该 agent 的身份生成目录 —— 非超级智能体会隐藏「仅超级智能体可用」
// 的管理类工具（builtin:super 整组不产出，成员被过滤光的自定义/预设组也不产出）；
// 不给定时返回全量（skill 工具依赖挑选器、自定义组编辑等场景需要）。
func ListToolCatalog(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	isSupervisor := true
	if agentID := strings.TrimSpace(c.Query("agent_id")); agentID != "" {
		isSupervisor = agentIsSupervisor(info, agentID)
	}
	// 能力视图：平台没开通 exec / browser 时，目录里照常列出但置灰 + 原因
	// （与插件「看得见但不可用」同口径 —— 藏起来反而让人以为是工具没了）。
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, isSupervisor).
		WithCapabilities(capabilitypolicy.Snapshot(info.Schema))
	groups, err := catalog.Groups()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"groups":             groups,
		"builtin_tools":      catalog.VisibleBuiltinTools(),
		"builtin_categories": toolgrant.BuiltinCategories(),
		"presets":            toolgrant.PresetGroups,
	}, "")
}

// GetAgentToolGrants 读取某 agent 的授权项与「不限制工具」开关。
func GetAgentToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	grants, err := grantsRepo.ListAgentGrants(info.Schema, agentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	// 「不限制工具」是 agent 的属性（agents.tools_unrestricted），走 agents 仓储
	unrestricted, err := agentsRepo.GetUnrestricted(info, agentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"agent_id":     agentID,
		"grants":       grants,
		"unrestricted": unrestricted,
	}, "")
}

// SetAgentToolGrants 全量替换某 agent 的授权项（可选同时设置不限制开关）。
// body: {"unrestricted":true,"grants":[{"grant_kind":"allow","item_type":"group","ref":"builtin:file"}]}
func SetAgentToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	var body struct {
		Unrestricted *bool `json:"unrestricted"`
		Grants       []struct {
			GrantKind string `json:"grant_kind"`
			Ref       string `json:"ref"`
			// 不接受 item_type：由 ref 形态推导（toolgrant.ItemTypeOfRef），
			// 客户端额外传来的字段会被 JSON 绑定静默忽略。
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, agentIsSupervisor(info, agentID))
	grants := make([]models.AgentToolGrant, 0, len(body.Grants))
	requiredRefs := map[string]bool{}
	for _, g := range body.Grants {
		g.Ref = strings.TrimSpace(g.Ref)
		// agent 侧允许引用工具组（allowGroup=true）
		if err := toolgrant.ValidateRef(g.Ref, true); err != nil {
			xhttp.ErrJSON(c, "invalid ref "+g.Ref+": "+err.Error(), nil, "")
			return
		}
		switch g.GrantKind {
		case models.GrantKindRequired, models.GrantKindAllow, models.GrantKindDeny:
		default:
			xhttp.ErrJSON(c, "invalid grant_kind for "+g.Ref, nil, "")
			return
		}
		// 存在性校验：引用必须能展开出工具（组或单工具都适用）
		list, err := catalog.ToolsOfRef(g.Ref)
		if err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.ref_not_found", nil, "ref not found or has no tools: "+g.Ref, "")
			return
		}
		if g.GrantKind == models.GrantKindRequired {
			requiredRefs[g.Ref] = true
		}
		grants = append(grants, models.AgentToolGrant{
			AgentID:   agentID,
			GrantKind: g.GrantKind,
			// 形态由 ref 推导（与元工具 grant_agent_tool 同源）：调用方传的 item_type 不作数，
			// 否则能写出 item_type=tool + ref=group:x 这种矛盾行，静默污染前端勾选态。
			ItemType: toolgrant.ItemTypeOfRef(g.Ref),
			Ref:      g.Ref,
		})
	}
	// 已标为「必须导入」的引用不允许再设 deny（避免语义矛盾）
	for _, g := range grants {
		if g.GrantKind == models.GrantKindDeny && requiredRefs[g.Ref] {
			xhttp.ErrJSON(c, "ref is required, cannot be denied: "+g.Ref, nil, "")
			return
		}
	}

	if err := grantsRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if body.Unrestricted != nil {
		if err := agentsRepo.SetUnrestricted(info, agentID, *body.Unrestricted); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(grants)}, "")
}

// GetAgentEffectiveTools 实际生效工具（含降级标记、来源与告警），供 UI 预览与排查。
func GetAgentEffectiveTools(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	// 与运行时同口径：候选只来自「本轮已激活」的技能（模型 use_skill 之后）。
	// 预览发生在 run 之外，没有「已激活」这个状态 → 候选恒为空，
	// 这里反映的是 **run 起点**的工具集（只有 required）。allow 行要等模型激活对应技能后才亮；
	// 每条 allow「由哪个技能撑着」见 read_agent 的 grant_views（pending + via_skill）。
	visible := []toolgrant.VisibleSkill{}

	// 顺带取实例：身份判定与「不限制工具」开关（agent 属性）都从它来，省一次查询
	agent, err := agentsRepo.GetAgent(info, agentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	isSupervisor := agent != nil && agent.IsSupervisor
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, isSupervisor).
		WithCapabilities(capabilitypolicy.Snapshot(info.Schema))
	res, err := toolgrant.NewResolver(catalog, info.Schema, visible, agent.ToolsUnrestricted).Resolve(agentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if res.Degraded {
		// Degraded = 有 allow 但没有候选。预览里这不是故障，而是「on-demand 尚未触发」：
		// 不说明的话管理员会以为授权丢了。
		res.Warnings = append(res.Warnings, "预览为 run 起点的工具集：allow 授权要等模型 use_skill 激活声明它的技能后才生效")
	}

	// 未配置任何授权项时，运行时不用上面的解析结果，预览需如实反映：
	//   - 超级智能体：缺省「不限制工具 + 全部内置工具」（不再依赖 config.tools）
	//   - 其他 agent：默认不绑定任何工具
	grants, grantsErr := grantsRepo.ListAgentGrants(info.Schema, agentID)
	noGrants := grantsErr == nil && len(grants) == 0
	if noGrants {
		if !res.Unrestricted || isSupervisor {
			res.NoGrants = true
		}
		if isSupervisor {
			res.Unrestricted = true
		}
	}

	// 超级智能体的「缺省全开」现在只剩**一条**判据：零授权行（自举保险丝）。
	// 与 agent_impl.resolveGrantedTools 的零行兜底同源，否则会出现
	// 「预览说 2 个、实际跑起来全部」的漂移。
	// 迁移闸（「授权行里没有 core 组」）已随存量迁移完成删除。
	if isSupervisor && noGrants {
		res.BuiltinDefault = true
		res.FinalTools = mergeSupervisorBuiltins(res.FinalTools, res.DenyRefs, catalog.VisibleBuiltinTools())
		res.FinalRefs = res.FinalRefs[:0]
		res.FinalNames = res.FinalNames[:0]
		for _, t := range res.FinalTools {
			res.FinalRefs = append(res.FinalRefs, t.Ref)
			res.FinalNames = append(res.FinalNames, t.Name)
		}
		// 超级智能体的内置工具是固有基线：被平台禁用的仍列出来（标「平台未开通」），
		// 但运行时不会绑定 —— 不说明的话「生效工具」会传达错的承诺。
		if blocked := unavailableRefs(res.FinalTools); len(blocked) > 0 {
			res.Warnings = append(res.Warnings, "平台未开通，运行时不会绑定："+strings.Join(blocked, "、"))
		}
	}

	xhttp.OkJSON(c, "Success", res, "")
}

// mergeSupervisorBuiltins 把内置工具并进超级智能体的工具集（deny 优先、按名去重排序）。
//
// builtins 必须由调用方从**带能力视图的目录**取（catalog.VisibleBuiltinTools），
// 不能用包级 toolgrant.BuiltinTools()：那一份的 Available 恒为 true，
// 会把平台未开通的 browser / exec 显示成可用。
func mergeSupervisorBuiltins(list []toolgrant.ToolInfo, denyRefs []string, builtins []toolgrant.ToolInfo) []toolgrant.ToolInfo {
	denied := make(map[string]bool, len(denyRefs))
	for _, ref := range denyRefs {
		denied[ref] = true
	}
	seen := make(map[string]bool, len(list))
	out := make([]toolgrant.ToolInfo, 0, len(list)+len(builtins))
	for _, t := range list {
		if denied[t.Ref] || seen[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		out = append(out, t)
	}
	for _, t := range builtins {
		if denied[t.Ref] || seen[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// unavailableRefs 不可用项的 ref（合成一条 warning，逐条加会刷屏）。
func unavailableRefs(list []toolgrant.ToolInfo) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		if !t.Available {
			out = append(out, t.Ref)
		}
	}
	return out
}

// GetSkillToolGrants 读取某 skill 声明的工具依赖（skill 只声明「需要」，不产生授权）。
func GetSkillToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	skillID := c.Param("id")

	grants, err := grantsRepo.ListSkillGrants(info.Schema, skillID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"skill_id": skillID, "items": grants}, "")
}

// SetSkillToolGrants 全量替换某 skill 的工具依赖。
// body: {"items":[{"item_type":"group","ref":"builtin:file"}]}
// 与 agent 侧不同：不允许引用自定义组（避免"自建 skill 提权"），且没有 required/allow/deny 之分。
func SetSkillToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	skillID := c.Param("id")

	var body struct {
		Items []struct {
			Ref string `json:"ref"`
			// 不接受 item_type：由 ref 形态推导
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// skill 工具依赖是租户级「声明需要」，与具体 agent 身份无关：
	// 保留全量目录（含 builtin:super），供 agent-authoring / skill-authoring 声明管理类工具。
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, true)
	grants := make([]models.SkillToolGrant, 0, len(body.Items))
	seen := map[string]bool{}
	for _, it := range body.Items {
		it.Ref = strings.TrimSpace(it.Ref)
		if seen[it.Ref] {
			continue
		}
		// allowGroup=false：skill 侧不允许引用工具组
		if err := toolgrant.ValidateRef(it.Ref, false); err != nil {
			xhttp.ErrJSON(c, "invalid ref "+it.Ref+": "+err.Error(), nil, "")
			return
		}
		list, err := catalog.ToolsOfRef(it.Ref)
		if err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.ref_not_found", nil, "ref not found or has no tools: "+it.Ref, "")
			return
		}
		seen[it.Ref] = true
		grants = append(grants, models.SkillToolGrant{SkillID: skillID, ItemType: toolgrant.ItemTypeOfRef(it.Ref), Ref: it.Ref})
	}

	if err := grantsRepo.ReplaceSkillGrants(info.Schema, skillID, grants); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(grants)}, "")
}

// ListToolGroups 自定义组列表（含成员）。
func ListToolGroups(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	groups, err := grantsRepo.ListGroups(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": groups}, "")
}

// UpsertToolGroup 创建/更新自定义组（按 name upsert，成员全量替换）。
// body: {"name":"只读工具组","description":"...","items":[{"ref":"builtin:file"}]}
// （item_type 由 ref 推导，不接收）
func UpsertToolGroup(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Items       []struct {
			Ref string `json:"ref"`
			// 不接受 item_type：由 ref 形态推导
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		xhttp.ErrJSON(c, "name is required", nil, "")
		return
	}
	// 组名合法性（复用 ref 校验：group:<name>）
	if err := toolgrant.ValidateRef(toolgrant.ScopeGroup+":"+body.Name, true); err != nil {
		xhttp.ErrJSON(c, "invalid group name: "+err.Error(), nil, "")
		return
	}

	// 自定义组是租户级资源：校验用全量目录（运行时按 agent 身份再过滤，见 agent 侧 Resolve）。
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, true)
	items := make([]models.ToolGroupItem, 0, len(body.Items))
	for _, it := range body.Items {
		it.Ref = strings.TrimSpace(it.Ref)
		// 组成员不允许引用工具组（禁止嵌套）
		if err := toolgrant.ValidateRef(it.Ref, false); err != nil {
			xhttp.ErrJSON(c, "invalid item "+it.Ref+": "+err.Error(), nil, "")
			return
		}
		list, err := catalog.ToolsOfRef(it.Ref)
		if err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.item_not_found", nil, "item not found or has no tools: "+it.Ref, "")
			return
		}
		items = append(items, models.ToolGroupItem{ItemType: toolgrant.ItemTypeOfRef(it.Ref), Ref: it.Ref})
	}

	id, err := grantsRepo.UpsertGroup(info.Schema, body.Name, body.Description, info.ID, items)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "name": body.Name, "items": len(items)}, "")
}

// DeleteToolGroup 删除自定义组（被 agent 授权 / skill 依赖 / 其它组引用时拒绝）。
func DeleteToolGroup(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrJSON(c, "name is required", nil, "")
		return
	}

	n, err := grantsRepo.CountRefUsages(info.Schema, toolgrant.ScopeGroup+":"+name)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if n > 0 {
		xhttp.ErrJSON(c, fmt.Sprintf("group is referenced by %d place(s), remove them first", n), nil, "")
		return
	}
	if err := grantsRepo.DeleteGroup(info.Schema, name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"name": name}, "")
}

// GetRefUsages 某 ref 的引用明细（agent 授权 / skill 依赖 / 自定义组成员）。
// query：ref=<ref>（必填）；prefix=1 时连带统计子项（如 ref=mcp:fs 也匹配 mcp:fs:read_file）。
// 用途：「工具与端点」页的「被引用 N 处」、删除前的影响面预览。
func GetRefUsages(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	ref := strings.TrimSpace(c.Query("ref"))
	if ref == "" {
		xhttp.ErrJSON(c, "ref is required", nil, "")
		return
	}
	prefix := c.Query("prefix") == "1" || c.Query("prefix") == "true"

	res, err := grantsRepo.ListRefUsages(info.Schema, ref, prefix)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", res, "")
}
