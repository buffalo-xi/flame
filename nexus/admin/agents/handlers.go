package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agentrelations "flame/nexus/shared/repository/agentrelations"
	repository "flame/nexus/shared/repository/agents"
	flowsrepo "flame/nexus/shared/repository/flows"
	skills "flame/nexus/shared/repository/skills"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/skeleton"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var (
	repo     = repository.NewRepository()
	relRepo  = agentrelations.NewRepository()
	flowRepo = flowsrepo.NewPostgresRepository()
)

// validateAgentNameUnique 先校验 agent 名是否可用：不与其它 agent、工作流重名。
// excludeID 非空表示改名场景，忽略自身那条记录。
func validateAgentNameUnique(info *auth.AuthInfo, name string, excludeID string) error {
	exist, err := repo.ExistsName(info, name, excludeID)
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("agent %q already exists", name)
	}
	flowExist, err := flowRepo.ExistsFlowName(info, name)
	if err != nil {
		return err
	}
	if flowExist {
		return fmt.Errorf("workflow %q already exists; agent name must be unique against workflows", name)
	}
	return nil
}

// agentToMap 将 Agent 转为响应 map
func agentToMap(instance *models.Agent) gin.H {
	return gin.H{
		"id":          instance.ID,
		"name":        instance.Name,
		"description": instance.Description,
		"config":      instance.Config,
		"status":      instance.Status,
		"endpoints":   instance.Endpoints,
		// 两个 agent 级开关（同名列，均由 agents 表承载）
		"tools_unrestricted": instance.ToolsUnrestricted,
		"skill_discovery":    instance.SkillDiscovery,
		"created_by":         instance.CreatedBy,
		"updated_by":         instance.UpdatedBy,
		"updated_by_name":    instance.UpdatedByName,
		// 两个判据语义正交，前端不要互相替代（见 shared/models/protected.go）：
		// protected     = 不允许删除/改名（预置资产名单，目前只有 main）；
		// is_supervisor = 超级权限身份（缺省拥有全部内置工具、任何入口都读草稿，可配置多个）。
		"protected":     models.IsProtectedAgent(instance.Name),
		"is_supervisor": instance.IsSupervisor,
		"created_at":    instance.CreatedAt,
		"updated_at":    instance.UpdatedAt,
	}
}

// ListAgents 获取智能体列表
func ListAgents(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")

	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// 解析请求参数
	var req repository.ListAgentsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	// 查询数据
	instances, total, err := repo.ListAgents(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 当前版本号：一次批量查（列表卡片要显示 vN），读失败不影响列表本身
	curVers, _ := versioning.CurrentVersions(authInfo.(*auth.AuthInfo).Schema, "agent_versions")

	var items []gin.H
	for i := range instances {
		item := agentToMap(&instances[i])
		// 0 = 尚未发布正式版本（卡片显示「未发布」）
		item["current_version"] = curVers[instances[i].ID]
		items = append(items, item)
	}

	// 构建响应
	response := gin.H{
		"items": items,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// GetAgent 获取单个智能体
func GetAgent(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	s, err := repo.GetAgent(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	body := agentToMap(s)
	// 版本状态：编辑器头部要能区分「未发布 / 已发布但有改动 / 与线上一致」（与 flow 同口径）。
	// 文件之外还比私有技能元数据 —— 那是 agent 定义的一部分。
	body["current_version"], body["dirty"] = 0, false
	if st, serr := getStorage(); serr == nil {
		v, dirty := versioning.AgentVersionState(context.Background(), st, authInfo.(*auth.AuthInfo).Schema, s.ID, s.Name)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// agentRequest 创建/更新的请求结构
type agentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Config      string `json:"config"`
	Status      int    `json:"status"` // 1=正常 | 3=废弃
	Endpoints   string `json:"endpoints"`
	// 两个 agent 级开关：指针类型以区分「未传」(nil) 与「显式置 false」。
	// 更新时未传则沿用库中旧值（UpdateAgent 是全字段覆盖式 UPDATE，见其回填逻辑）。
	ToolsUnrestricted *bool `json:"tools_unrestricted"`
	SkillDiscovery    *bool `json:"skill_discovery"`
	// IsSupervisor 超级权限属性（agents.is_supervisor）。管理端就是人工管理面，
	// 是授予该属性的权威入口之一（另一个是 supervisor 自己调的 update_agent）。
	// 更新时走单列 SetSupervisor，不混进 UpdateAgent 的全字段覆盖。
	IsSupervisor *bool `json:"is_supervisor"`
	// 调用授权（仅创建时生效）：新 agent 可调用谁 / 谁能调用新 agent
	CanCallIDs  []string `json:"can_call_ids"`
	CalledByIDs []string `json:"called_by_ids"`
	// Lang 新建时用于生成 AGENT.md 骨架的语言（仅创建时生效，不进库）。
	// 不传 / 不认识 → 英文骨架（见 shared/skeleton.Normalize）。
	Lang string `json:"lang"`
}

// toModel 将请求转为 Agent
func (req *agentRequest) toModel() models.Agent {
	m := models.Agent{
		Name:        req.Name,
		Description: xsql.String(req.Description),
		Status:      xsql.Int(req.Status),
	}
	if req.Config != "" {
		m.Config = xsql.JSONB{Data: req.Config}
	}
	if req.Endpoints != "" {
		m.Endpoints = xsql.JSONB{Data: req.Endpoints}
	}
	if req.ToolsUnrestricted != nil {
		m.ToolsUnrestricted = *req.ToolsUnrestricted
	}
	if req.SkillDiscovery != nil {
		m.SkillDiscovery = *req.SkillDiscovery
	}
	if req.IsSupervisor != nil {
		m.IsSupervisor = *req.IsSupervisor
	}
	return m
}

// checkJSONPayload reports the first JSON text field that is not valid JSON.
// config and endpoints travel as strings; xsql.JSONB refuses to write anything
// that is not valid JSON, so the check keeps the failure at the request layer
// instead of letting it surface as a database error.
func (req *agentRequest) checkJSONPayload() (string, bool) {
	for _, f := range []struct {
		name string
		val  string
	}{
		{"config", req.Config},
		{"endpoints", req.Endpoints},
	} {
		if f.val != "" && !json.Valid([]byte(f.val)) {
			return f.name, false
		}
	}
	return "", true
}

// CreateAgent 创建智能体
func CreateAgent(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	info := authInfo.(*auth.AuthInfo)

	// config / endpoints are JSON text: reject malformed values here, before any
	// storage is deleted or written.
	if field, ok := req.checkJSONPayload(); !ok {
		xhttp.ErrCode(c, "common.invalid_params", gin.H{"field": field}, field+" must be valid JSON", "")
		return
	}

	// 先校验：名称合法 + 不与其它 agent、工作流重名（任何写库之前）
	req.Name = strings.TrimSpace(req.Name)
	if !tools.ValidName(req.Name) {
		xhttp.ErrCode(c, "agent.invalid_name", nil, "Invalid agent name (只允许字母数字横线，长度≤64)", "")
		return
	}
	if err := validateAgentNameUnique(info, req.Name, ""); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 同名重建：清掉上一个同名实体留下的版本目录（DB 里没有行不代表盘上没有）。
	if st, serr := getStorage(); serr == nil {
		_ = st.DeletePrefix(context.Background(),
			paths.VersionEntityKey(info.Schema, paths.VersionAgents, req.Name))
	}

	// 状态只接受两态；未传按「正常」处理（前端新建即 1，这里兜住直接调 API 的情形）
	if req.Status == 0 {
		req.Status = models.StatusNormal
	}
	if req.Status != models.StatusNormal && req.Status != models.StatusDeprecated {
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1(正常) / 3(废弃)", "")
		return
	}

	agent := req.toModel()

	// 配额检查：租户内 agent 数量上限
	schema := info.Schema
	cnt, err := quota.CountAgents(schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := models.CheckQuota("agent", cnt, quota.Resolve(schema).MaxAgents); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 插入数据（人创建 → 无"创建者 agent"，不自动授权）
	id, err := repo.CreateAgent(info, &agent, "")
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 显式授权（可选）：出边（它能调用谁）+ 入边（谁能调用它）
	if len(req.CanCallIDs) > 0 || len(req.CalledByIDs) > 0 {
		// 出边与入边一视同仁，都允许包含 supervisor（它可被调用，能不能调由这条关系决定）
		canCall := filterValidAgentIDs(info, id, req.CanCallIDs)
		calledBy := filterValidAgentIDs(info, id, req.CalledByIDs)
		if err := relRepo.ReplaceByCaller(info.Schema, id, canCall, info.ID); err != nil {
			xhttp.ErrJSON(c, "agent created but relations failed: "+err.Error(), gin.H{"id": id}, "")
			return
		}
		if err := relRepo.ReplaceByCallee(info.Schema, id, calledBy, info.ID); err != nil {
			xhttp.ErrJSON(c, "agent created but relations failed: "+err.Error(), gin.H{"id": id}, "")
			return
		}
	}

	// 确保工作区有入口文件（agent 即文件夹，便于新建后立刻进入编辑）。
	// 入口文件名取自 shared/paths.AgentEntryFile（单一真源）：与运行时
	// loadSystemPrompt / create_agent / read_agent 的约定一致，
	// 不做成可配置（否则会出现"配了却不生效"的误导）。
	entryFile := paths.AgentEntryFile
	if st, serr := getStorage(); serr == nil {
		key := path.Join(tools.AgentsKey(info.Schema, agent.Name), entryFile)
		if ex, _ := st.Exists(context.Background(), key); !ex {
			_ = st.WriteFile(context.Background(), key,
				[]byte(skeleton.AgentMD(skeleton.Normalize(req.Lang), agent.Name, req.Description)))
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// filterValidAgentIDs 过滤空值、自身与不存在的 agent，返回去重后的 id 列表。
//
// 不再排除 supervisor：它可被调用，能不能调完全由这条授权关系决定
// （与 caller.go 的判定一致 —— 有授权行才放行，与身份无关）。
func filterValidAgentIDs(info *auth.AuthInfo, selfID string, ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || id == selfID || seen[id] {
			continue
		}
		inst, err := repo.GetAgent(info, id)
		if err != nil || inst == nil {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// GetAgentRelations 获取 agent 的调用授权（出边：可调用谁；入边：谁能调用它）。
func GetAgentRelations(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	callable, err := relRepo.ListByCaller(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	callers, err := relRepo.ListByCallee(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"can_call":  callable, // 该 agent 可调用的目标
		"called_by": callers,  // 可调用该 agent 的来源
	}, "")
}

// SetAgentRelations 全量替换 agent 的调用授权（双向）。
// body: {"can_call_ids":["uuid",...],"called_by_ids":["uuid",...]}；空数组 = 清空该方向。
func SetAgentRelations(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		CanCallIDs  []string `json:"can_call_ids"`
		CalledByIDs []string `json:"called_by_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}
	// 只有正常态允许改定义（端点对外开放不会自动带入任何冻结）
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "agent 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return
	}

	// 出边与入边一视同仁，都允许包含 supervisor（它可被调用，能不能调由这条关系决定）
	canCall := filterValidAgentIDs(info, id, body.CanCallIDs)
	calledBy := filterValidAgentIDs(info, id, body.CalledByIDs)

	if err := relRepo.ReplaceByCaller(info.Schema, id, canCall, info.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := relRepo.ReplaceByCallee(info.Schema, id, calledBy, info.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"can_call": len(canCall), "called_by": len(calledBy)}, "")
}

// GetAgentSkillBindings 读取该 agent 绑定的技能（agent_skill_bindings 的 agent 侧视角）。
// 返回 {"skill_ids":[...]}；「全员可见」的技能也允许先绑定，便于后续把技能切成白名单时保留授权。
func GetAgentSkillBindings(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	skillRepo := skills.NewRepository()
	ids, err := skillRepo.ListSkillIDsByAgent(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	discovery, derr := skillRepo.GetAgentSkillDiscovery(info.Schema, id)
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"skill_ids": ids, "discovery": discovery}, "")
}

// SetAgentSkillBindings 全量替换该 agent 的 skill 绑定。
// body: {"skill_ids":["uuid",...]}；空数组 = 清空（该 agent 只能看到「全员可见」的租户级 skill）。
func SetAgentSkillBindings(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		SkillIDs []string `json:"skill_ids"`
		// Discovery 省略（nil）时保持原值；显式传入才修改「可发现未绑定的技能」开关
		Discovery *bool `json:"discovery"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}
	// 只有正常态允许改定义（与调用授权一致）
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "agent 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return
	}

	skillRepo := skills.NewRepository()
	if err := skillRepo.ReplaceByAgent(info.Schema, id, body.SkillIDs, info.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if body.Discovery != nil {
		if err := skillRepo.SetAgentSkillDiscovery(info.Schema, id, *body.Discovery); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(body.SkillIDs)}, "")
}

// UpdateAgent 更新智能体
func UpdateAgent(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)

	// 先取旧实例：拿旧名用于改名时的目录迁移
	old, err := repo.GetAgent(ai, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	// 只有正常态允许改定义（含改名 / 迁移目录）
	if old != nil && !old.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "agent 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return
	}

	// 改名场景：先做全部校验，再写库 / 迁移目录（避免部分成功）
	if old != nil && req.Name != old.Name {
		req.Name = strings.TrimSpace(req.Name)
		// 内置受保护智能体（如 main）不允许改名
		if models.IsProtectedAgent(old.Name) {
			xhttp.ErrJSON(c, "protected agent "+old.Name+" cannot be renamed", nil, "")
			return
		}
		if !tools.ValidName(req.Name) {
			xhttp.ErrCode(c, "agent.invalid_name", nil, "Invalid agent name (只允许字母数字横线，长度≤64)", "")
			return
		}
		if err := validateAgentNameUnique(ai, req.Name, id); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}

	// config / endpoints 在请求里是字符串字段，空串表示"本次未提供"。
	// UpdateAgent 是全字段覆盖式 UPDATE，直接透传空串会写成 NULL（config 列有 NOT NULL 约束）→ 报错，
	// 因此这里回落：优先沿用库里的旧值，取不到时给安全默认值（{} / []）。
	if req.Config == "" {
		req.Config = "{}"
		if old != nil && !old.Config.IsNil() {
			if b, jerr := json.Marshal(old.Config); jerr == nil {
				req.Config = string(b)
			}
		}
	}
	if req.Endpoints == "" {
		req.Endpoints = "[]"
		if old != nil && !old.Endpoints.IsNil() {
			if b, jerr := json.Marshal(old.Endpoints); jerr == nil {
				req.Endpoints = string(b)
			}
		}
	}

	// Same check as CreateAgent; the fallback above already yields valid JSON,
	// so this only rejects what the client actually sent.
	if field, ok := req.checkJSONPayload(); !ok {
		xhttp.ErrCode(c, "common.invalid_params", gin.H{"field": field}, field+" must be valid JSON", "")
		return
	}

	// 状态只接受两态：未传则沿用库中旧值（全字段覆盖式 UPDATE 不能把状态清零）
	if req.Status == 0 && old != nil {
		req.Status = int(old.Status)
	}
	if req.Status != models.StatusNormal && req.Status != models.StatusDeprecated {
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1(正常) / 3(废弃)", "")
		return
	}

	agent := req.toModel()
	// 两个 agent 级开关：请求未显式提供时沿用库中旧值，避免全字段覆盖式 UPDATE 把开关误清零。
	// 专用写入口仍可直改（技能可见性见 SetAgentSkillBindings，工具面见 toolgrants 的 SetUnrestricted）。
	if old != nil {
		if req.ToolsUnrestricted == nil {
			agent.ToolsUnrestricted = old.ToolsUnrestricted
		}
		if req.SkillDiscovery == nil {
			agent.SkillDiscovery = old.SkillDiscovery
		}
	}

	// 改名：**先搬盘、再写库**（盘是内容，库是索引；见 paths.MoveEntityStorage 的调用方约定）。
	// 版本目录按名字键：盘没搬成却先把库改了，正式调用就会按新名拼出一个空版本目录。
	renamed := old != nil && old.Name != "" && agent.Name != "" && old.Name != agent.Name
	if renamed {
		st, serr := getStorage()
		if serr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "存储未就绪，无法迁移定义域/版本目录: "+serr.Error(), "")
			return
		}
		if rerr := paths.MoveEntityStorage(st, ai.Schema, paths.VersionAgents, old.Name, agent.Name); rerr != nil {
			xhttp.ErrCode(c, "agent.rename_failed", nil, "迁移定义域/版本目录失败（改名未生效）: "+rerr.Error(), "")
			return
		}
	}

	// 更新数据
	err = repo.UpdateAgent(ai, id, &agent)
	if err != nil {
		if renamed {
			// 库没改成 → 把盘搬回去，别留「盘新名、库旧名」的半套
			if st, serr := getStorage(); serr == nil {
				if rerr := paths.MoveEntityStorage(st, ai.Schema, paths.VersionAgents, agent.Name, old.Name); rerr != nil {
					xlog.Error("回滚定义域/版本目录失败 schema=%s %s→%s: %v", ai.Schema, agent.Name, old.Name, rerr)
				}
			}
		}
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 超权属性：单列更新 —— UpdateAgent 刻意不覆盖 is_supervisor
	// （全字段覆盖会把请求里未传的 false 静默写进去，等于一次改描述就把权限清零）。
	// 降级本租户最后一个 supervisor 会被仓储拒绝，这里如实回传。
	if req.IsSupervisor != nil && (old == nil || old.IsSupervisor != *req.IsSupervisor) {
		if serr := repo.SetSupervisor(ai, id, *req.IsSupervisor); serr != nil {
			xhttp.ErrJSON(c, serr.Error(), nil, "")
			return
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// DeleteAgent 删除智能体
func DeleteAgent(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	// 先取实例：拿 name 用于清理定义域目录
	inst, err := repo.GetAgent(ai, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	// 内置受保护智能体不允许删除
	if inst != nil && models.IsProtectedAgent(inst.Name) {
		xhttp.ErrJSON(c, "protected agent "+inst.Name+" cannot be deleted", nil, "")
		return
	}
	// 废弃不允许删除（需先切回「正常」）
	if inst != nil && !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "agent 已废弃，不能删除（如需删除请先切回「正常」）", "")
		return
	}

	// 删除数据：私有 skill 记录、授权关联与 agents 行由仓储同一事务完成
	// （skills.agent_id 没有外键，不能指望 DB 级联）。
	if err := repo.DeleteAgent(ai, id); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 清理定义域目录 + 版本域（走 storage，local / s3 通用）。
	// DB 的 cascade 删不掉盘：版本目录按名字键留着，同名重建就会继承旧历史。
	if inst != nil && inst.Name != "" {
		if st, serr := getStorage(); serr == nil {
			if rerr := paths.DeleteEntityStorage(st, ai.Schema, paths.VersionAgents, inst.Name); rerr != nil {
				xlog.Error("清理定义域/版本目录失败 schema=%s name=%s: %v", ai.Schema, inst.Name, rerr)
			}
		}
	}

	xhttp.OkJSON(c, "Agent instance deleted successfully", id, "")
}

// SetAgentStatus 切换 agent 状态（1=正常 | 3=废弃，见 models.StatusXxx）。
//
// 切「废弃」时会自动收回该 agent 全部端点的对外（exposed=false，记录保留）：
// 废弃态不该再对外服务。切「正常」不动端点（端点对外与定义状态无关）。
func SetAgentStatus(c *gin.Context) {
	// 从context中获取认证信息
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	switch body.Status {
	case models.StatusNormal, models.StatusDeprecated:
	default:
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1(正常) / 3(废弃)", "")
		return
	}

	if err := repo.UpdateStatus(ai, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 只有切「废弃」才收回端点对外：废弃态不该再对外服务。
	// 切「正常」不收回 —— 端点对外与定义可编辑是两件事，恢复编辑不该把线上服务打掉。
	// 状态已改，收回失败不回滚，只记日志（下次切换或手工收回可恢复）。
	if body.Status == models.StatusDeprecated {
		if uerr := svcendpoints.NewRepository().RetractByTarget(ai, "agent", id); uerr != nil {
			xlog.Error("下架 agent 端点失败 id=%s: %v", id, uerr)
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "status": body.Status}, "")
}
