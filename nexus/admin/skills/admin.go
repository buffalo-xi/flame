// Package skills 提供 skills 的管理接口（增删改查 + 状态切换）。
// 元数据走 skills 表，内容走 agent 存储后端（driver 由 config.yaml 决定）。
package skills

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"sync"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/skeleton"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

var (
	skillRepo = skills.NewRepository()
	agentRepo = agents.NewRepository()

	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// ensureAgentModifiable 指定归属 agent 必须存在、且处于正常态；
// agentID 为空 = 租户级，免校验。新建、改、删、调可见性、写文件一概走这里
// （判据 models.IsModifiable，与 skillManager.checkOwnerAgent、
// tools/gate.go 的 checkWrite / checkWriteOwn 四处同口径 —— 缺一即会出现口径分裂）。
// 废弃(3) 一律不可写。
func ensureAgentModifiable(info *auth.AuthInfo, agentID string) error {
	if agentID == "" {
		return nil
	}
	oa, err := agentRepo.GetAgent(info, agentID)
	if err != nil {
		return err
	}
	if oa == nil {
		return fmt.Errorf("skill owner agent %s not found", agentID)
	}
	if !oa.IsModifiable() {
		return fmt.Errorf("归属 agent %q 已废弃；其私有技能不可变更", oa.Name)
	}
	return nil
}

// ensureOwnerAgentModifiable 已登记 skill 的归属 agent 必须可写（见 ensureAgentModifiable）。
func ensureOwnerAgentModifiable(info *auth.AuthInfo, s *models.Skill) error {
	if s == nil {
		return nil
	}
	return ensureAgentModifiable(info, string(s.AgentID))
}

// getStorage 懒加载 agent 存储实例（与 service/agent.InitStorage 同配置）。
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return store, storeErr
}

// contentPath 返回 skill 内容文件路径（租户级或 agent 私有）。
func contentPath(schema, agentName, name string) string {
	if agentName == "" {
		return tools.SkillPath(schema, name)
	}
	return tools.AgentSkillPath(schema, agentName, name)
}

// resolveAgentName 由 agent_id 反查 name；agent_id 为空返回空串（租户级）。
func resolveAgentName(schema, agentID string) (string, error) {
	if agentID == "" {
		return "", nil
	}
	inst, err := agentRepo.GetAgent(&auth.AuthInfo{Schema: schema}, agentID)
	if err != nil {
		return "", err
	}
	return inst.Name, nil
}

// skillToMap 将 Skill 转为响应 map（content 由调用方提供）。
func skillToMap(s *models.Skill, content string) gin.H {
	return gin.H{
		"id":               s.ID,
		"name":             s.Name,
		"description":      s.Description,
		"metadata":         s.Metadata,
		"user_invocable":   s.UserInvocable,
		"command_dispatch": s.CommandDispatch,
		"command_tool":     s.CommandTool,
		"status":           s.Status,
		"restricted":       s.Restricted,
		"agent_id":         s.AgentID,
		"updated_by_name":  s.UpdatedByName,
		"agent_name":       s.AgentName,
		"protected":        models.IsProtectedSkill(s.Name),
		"created_at":       s.CreatedAt,
		"updated_at":       s.UpdatedAt,
		"content":          content,
	}
}

// ListSkills 列出 skill（分页 + 过滤）。
func ListSkills(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req struct {
		Page    int    `json:"page"`
		Limit   int    `json:"limit"`
		AgentID string `json:"agent_id"`
		Status  *int   `json:"status"`
		Keyword string `json:"keyword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	list, total, err := skillRepo.List(info.Schema, &skills.ListRequest{
		Page:    req.Page,
		Limit:   req.Limit,
		AgentID: req.AgentID,
		Status:  req.Status,
		Keyword: req.Keyword,
	})
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 批量取授权数量（一次查询）——供列表展示"仅 N 个 agent / 无人可见"
	ids := make([]string, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	counts, err := skillRepo.ListBindingCounts(info.Schema, ids)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 当前版本号：一次批量查两张表 —— 租户级技能看自己的版本，私有技能没有独立版本，
	// 正文冻结在所属 agent 的当前版本里，所以取 agent 的版本号（version_source 标明来源）。
	skillVers, _ := versioning.CurrentVersions(info.Schema, "skill_versions")
	agentVers, _ := versioning.CurrentVersions(info.Schema, "agent_versions")

	items := make([]gin.H, 0, len(list))
	for i := range list {
		m := skillToMap(&list[i], "")
		m["bound_count"] = counts[list[i].ID]
		// 0 = 尚未发布正式版本（卡片显示「未发布」；私有技能随所属 agent，agent 没发布也就是 0）
		if string(list[i].AgentID) != "" {
			m["current_version"] = agentVers[string(list[i].AgentID)]
			m["version_source"] = "agent"
		} else {
			m["current_version"] = skillVers[list[i].ID]
			m["version_source"] = "skill"
		}
		items = append(items, m)
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": total, "page": req.Page, "limit": req.Limit}, "")
}

// GetSkill 获取单个 skill（含内容）。
func GetSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	agentName, err := resolveAgentName(info.Schema, string(s.AgentID))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	content := ""
	if st, serr := getStorage(); serr == nil {
		if data, rerr := st.ReadFile(context.Background(), contentPath(info.Schema, agentName, s.Name)); rerr == nil {
			content = string(data)
		}
	}
	body := skillToMap(s, content)
	// 版本状态：编辑器头部要能区分「未发布 / 已发布但有改动 / 与线上一致」（与 flow 同口径）。
	// 私有技能恒为 0 —— 它的版本随所属 agent，前端据此显示「随 agent 版本」。
	body["current_version"], body["dirty"] = 0, false
	if st, serr := getStorage(); serr == nil {
		v, dirty := versioning.SkillVersionState(context.Background(), st, info.Schema, s)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// skillRequest 创建/更新请求结构。
type skillRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	AgentID         string `json:"agent_id"` // 空 = 租户级
	Content         string `json:"content"`
	Restricted      *bool  `json:"restricted"` // nil = 缺省 true（企业级默认拒绝：不授权即不可见）
	UserInvocable   bool   `json:"user_invocable"`
	CommandDispatch string `json:"command_dispatch"`
	CommandTool     string `json:"command_tool"`
	// Lang 正文为空时用于生成 SKILL.md 骨架的语言（不进库）。
	// 不传 / 不认识 → 英文骨架（见 shared/skeleton.Normalize）。
	Lang string `json:"lang"`
}

// CreateSkill 创建 skill。
func CreateSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req skillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !tools.ValidName(req.Name) {
		xhttp.ErrCode(c, "skill.invalid_name", nil, "Invalid skill name (只允许字母数字横线，长度≤64)", "")
		return
	}
	// 归属范围内重名检查（全状态）
	exist, err := skillRepo.FindByName(info.Schema, req.Name, req.AgentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if exist != nil {
		xhttp.ErrJSON(c, "skill already exists", nil, "")
		return
	}

	// 同名重建：清掉上一个同名实体留下的版本目录（DB 里没有行不代表盘上没有）。
	// 私有技能没有独立版本目录，不需要这一步。
	if req.AgentID == "" {
		removeSkillVersions(info.Schema, req.Name)
	}

	// 归属 agent 非编辑态 → 不给它新建私有技能（与 Update / Delete / 元工具同口径）
	if verr := ensureAgentModifiable(info, req.AgentID); verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return
	}
	agentName, err := resolveAgentName(info.Schema, req.AgentID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 配额检查：租户内 skill 数量上限（必须在写内容文件之前，否则会留孤儿文件）
	cnt, err := quota.CountSkills(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := models.CheckQuota("skill", cnt, quota.Resolve(info.Schema).MaxSkills); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	st, err := getStorage()
	if err != nil {
		xhttp.ErrJSON(c, "storage not ready: "+err.Error(), nil, "")
		return
	}
	// 先写入口文件，再落库；落库失败则清理文件。
	// 内容为空时写入默认骨架（与 agent 新建时写 AGENT.md 一致），保证"新建后即可编辑"。
	entry := req.Content
	if strings.TrimSpace(entry) == "" {
		entry = skeleton.SkillMD(skeleton.Normalize(req.Lang), req.Name, req.Description)
	}
	if err := st.WriteFile(context.Background(), contentPath(info.Schema, agentName, req.Name), []byte(entry)); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 可见性缺省 true（企业级默认拒绝：不显式授权即不可见）；仅租户级 skill 生效
	restricted := true
	if req.Restricted != nil {
		restricted = *req.Restricted
	}

	id, err := skillRepo.Create(info.Schema, &models.Skill{
		Name:            req.Name,
		Description:     xsql.String(req.Description),
		AgentID:         xsql.UUID(req.AgentID),
		Restricted:      xsql.Bool(restricted),
		CreatedBy:       xsql.UUID(info.ID),
		UpdatedBy:       xsql.UUID(info.ID),
		UserInvocable:   xsql.Bool(req.UserInvocable),
		CommandDispatch: xsql.String(req.CommandDispatch),
		CommandTool:     xsql.String(req.CommandTool),
	})
	if err != nil {
		_ = st.Delete(context.Background(), contentPath(info.Schema, agentName, req.Name))
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// UpdateSkill 更新 skill（name/description/content；改名同步迁移内容文件）。
func UpdateSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var req skillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	// 归属 agent 非编辑态 → 它的私有 skill 整片冻结（与 skills.go / tools/gate.go 同口径）
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return
	}
	// 只有正常态允许改定义，与 agent / flow 同口径
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "skill 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return
	}

	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		newName = s.Name
	}
	if newName != s.Name {
		// 内置受保护 skill：不允许改名（描述等其它字段可改）
		if models.IsProtectedSkill(s.Name) {
			xhttp.ErrJSON(c, "protected skill "+s.Name+" cannot be renamed", nil, "")
			return
		}
		if !tools.ValidName(newName) {
			xhttp.ErrCode(c, "skill.invalid_name", nil, "Invalid skill name (只允许字母数字横线，长度≤64)", "")
			return
		}
		exist, err := skillRepo.FindByName(info.Schema, newName, string(s.AgentID))
		if err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if exist != nil && exist.ID != s.ID {
			xhttp.ErrJSON(c, "skill already exists", nil, "")
			return
		}
	}

	agentName, err := resolveAgentName(info.Schema, string(s.AgentID))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	oldPath := contentPath(info.Schema, agentName, s.Name)

	oldName := s.Name
	s.Name = newName
	if req.Description != "" {
		s.Description = xsql.String(req.Description)
	}
	// 命令相关字段全量覆盖（空值即清空，便于取消命令分发）
	s.UserInvocable = xsql.Bool(req.UserInvocable)
	s.CommandDispatch = xsql.String(req.CommandDispatch)
	s.CommandTool = xsql.String(req.CommandTool)
	s.UpdatedBy = xsql.UUID(info.ID)

	if err := skillRepo.Update(info.Schema, s); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	newPath := contentPath(info.Schema, agentName, newName)
	// 只改元数据、没传内容、也没改名 → 文件不动
	if req.Content == "" && oldPath == newPath {
		xhttp.OkJSON(c, "Success", nil, "")
		return
	}

	st, err := getStorage()
	if err != nil {
		xhttp.ErrJSON(c, "storage not ready: "+err.Error(), nil, "")
		return
	}
	content := []byte(req.Content)
	// 改名：**整个技能目录**一起搬。
	//
	// 技能目录可以放附属文件与子目录（见本包 files.go 的文件管理接口），只搬 SKILL.md
	// 会把附属资源留在旧目录变成孤儿。与 service/agent/skills.go 的 Update 同口径。
	// 搬不动时（目标目录已存在 / 跨设备等）退回「读旧 + 写新 + 删旧」，至少保住入口文件。
	if oldPath != newPath {
		if rerr := st.Rename(context.Background(), path.Dir(oldPath), path.Dir(newPath)); rerr != nil {
			if data, rerr2 := st.ReadFile(context.Background(), oldPath); rerr2 == nil {
				if werr := st.WriteFile(context.Background(), newPath, data); werr != nil {
					xhttp.ErrJSON(c, werr.Error(), nil, "")
					return
				}
			}
			_ = st.Delete(context.Background(), oldPath)
		}
		// 租户级技能：版本目录按名字键，必须跟着草稿一起搬，
		// 否则历史版本挂在旧名下，按当前 name 拼路径就找不到了。
		// 私有技能没有独立版本目录（随所属 agent），不走这里。
		if string(s.AgentID) == "" {
			verOld := paths.VersionEntityKey(info.Schema, paths.VersionSkills, oldName)
			verNew := paths.VersionEntityKey(info.Schema, paths.VersionSkills, newName)
			if rerr := st.Rename(context.Background(), verOld, verNew); rerr != nil {
				xhttp.ErrCode(c, "skill.rename_failed", nil, "技能已改名，但版本目录迁移失败（请重试或手工搬运）: "+rerr.Error(), "")
				return
			}
		}
	}

	if len(content) == 0 {
		if oldPath != newPath {
			xhttp.OkJSON(c, "Success", nil, "") // 文件已随目录搬过去
			return
		}
		if data, rerr := st.ReadFile(context.Background(), oldPath); rerr == nil {
			content = data
		}
	}
	if err := st.WriteFile(context.Background(), newPath, content); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSkill 删除 skill（行 + 内容文件）。
func DeleteSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	// 归属 agent 非编辑态 → 它的私有 skill 整片冻结（与 skills.go / tools/gate.go 同口径）
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return
	}
	// 废弃不允许删除（与 agent / flow 同口径）
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "skill 已废弃，不能删除（如需删除请先切回「正常」）", "")
		return
	}

	// 内置受保护 skill 不允许删除
	if models.IsProtectedSkill(s.Name) {
		xhttp.ErrJSON(c, "protected skill "+s.Name+" cannot be deleted", nil, "")
		return
	}

	if err := skillRepo.Delete(info.Schema, id); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 尽力清理整个内容目录（含 SKILL.md 与其它文件）；归属 agent 已不存在时跳过
	if agentName, rerr := resolveAgentName(info.Schema, string(s.AgentID)); rerr == nil {
		if st, serr := getStorage(); serr == nil {
			_ = st.DeletePrefix(context.Background(), skillDirKey(info.Schema, agentName, s.Name))
		}
	}
	// 租户级技能：版本域一并清。版本目录按名字键，留着会让同名重建「继承」旧历史。
	// 私有技能没有独立版本目录（内容随所属 agent 的版本目录），这里不处理。
	if string(s.AgentID) == "" {
		removeSkillVersions(info.Schema, s.Name)
	}
	xhttp.OkJSON(c, "Skill deleted successfully", id, "")
}

// SetSkillStatus 切换 skill 状态（1=正常 | 3=废弃，与 agents / flows 同一套两态）。
// skill 没有服务端点，所以没有"下架联动"：废弃后不再被 agent 看到/注入。
func SetSkillStatus(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
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
		xhttp.ErrCode(c, "skill.invalid_status", nil, "status must be 1(正常) / 3(废弃)", "")
		return
	}

	// 归属 agent 非正常态 → 它名下私有 skill 的状态也不受理（与其他管理端入口同口径）。
	// 刻意**不判** skill 自身状态：那正是本次要改的字段，判它会让废弃后无法恢复（死锁）。
	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return
	}

	if err := skillRepo.UpdateStatus(info.Schema, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}

// GetSkillVisibility 获取某 skill 的可见性配置（restricted 开关 + 授权 agent 列表）。
// 仅租户级 skill 有意义；私有 skill 的可见性由归属决定。
func GetSkillVisibility(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	list, err := skillRepo.ListBindings(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":      list,
		"restricted": s.Restricted, // 直接读 skills.restricted
		"agent_id":   s.AgentID,    // 非空 = 私有 skill
	}, "")
}

// SetSkillVisibility 设置可见性：restricted 开关 + 授权 agent 白名单（事务内全量替换）。
// body: {"restricted": true, "agent_ids": ["uuid-A","uuid-B"]}
// restricted=false 时 bindings 不参与判定（数据保留，便于来回切换开关）。
func SetSkillVisibility(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Restricted bool     `json:"restricted"`
		AgentIDs   []string `json:"agent_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	// 归属 agent 非编辑态 → 它的私有 skill 整片冻结（与 skills.go / tools/gate.go 同口径）
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return
	}
	// 可见性是定义的一部分：废弃期间不接受调整
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "skill 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return
	}
	// 私有 skill 的可见性由归属决定，不接受收窄配置
	if s.AgentID != "" {
		xhttp.ErrJSON(c, "private skill does not support visibility config", nil, "")
		return
	}

	// 去重 + 校验 agent 存在
	seen := map[string]bool{}
	agentIDs := make([]string, 0, len(body.AgentIDs))
	for _, aid := range body.AgentIDs {
		if aid == "" || seen[aid] {
			continue
		}
		if _, aerr := agentRepo.GetAgent(&auth.AuthInfo{Schema: info.Schema}, aid); aerr != nil {
			xhttp.ErrCode(c, "agent.not_found", nil, "agent not found: "+aid, "")
			return
		}
		seen[aid] = true
		agentIDs = append(agentIDs, aid)
	}

	if err := skillRepo.SetVisibility(info.Schema, id, body.Restricted, agentIDs, info.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"restricted": body.Restricted, "count": len(agentIDs)}, "")
}
