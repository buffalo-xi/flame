package svcauthbindings

import (
	"database/sql"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	repo "flame/nexus/shared/repository/svcauthbindings"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

var repository = repo.NewRepository()

// RegisterRoutes 注册 /svcauthbindings 管理接口（外层已带 /svcauthbindings 前缀）
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("", listBindings)
	router.POST("/create", createBinding)
	router.PUT("/:id", updateBinding)
	router.DELETE("/:id", deleteBinding)
	// 端点访问范围（按 agent / flow 目标授权，覆盖该目标的全部协议端点）
	router.GET("/target-candidates", listTargetCandidates)
	router.GET("/:id/targets", getBindingTargets)
	router.PUT("/:id/targets", setBindingTargets)
}

// authInfoOf 取出当前请求的租户认证信息。
func authInfoOf(c *gin.Context) (*auth.AuthInfo, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := authInfo.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrJSON(c, "Invalid tenant auth info", nil, "")
		return nil, false
	}
	return info, true
}

// ensureUserExists 校验绑定目标用户存在。
// 绑定只是给服务调用一个身份（审计用），因此不校验角色 —— 管理用户同样可以绑定；
// 也不拦停用账号（真被停用时由服务侧按 status 拒掉，账号恢复后绑定自动生效）。
func ensureUserExists(schema, userID string) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	var ok bool
	err := db.DB.QueryRow(`SELECT true FROM `+schema+`.users WHERE id = $1`, userID).Scan(&ok)
	if err == sql.ErrNoRows {
		return fmt.Errorf("user not found: %s", userID)
	}
	return err
}

// bindingToMap 将绑定转为响应 map。
func bindingToMap(b *models.SvcAuthBinding) gin.H {
	return gin.H{
		"id":                 b.ID,
		"auth_type":          b.AuthType,
		"external_issuer":    b.ExternalIssuer,
		"external_subject":   b.ExternalSubject,
		"user_id":            b.UserID,
		"is_active":          b.IsActive,
		"restrict_targets":   b.RestrictTargets,
		"exclude_supervisor": b.ExcludeSupervisor,
		"target_count":       b.TargetCount,
		"created_at":         b.CreatedAt,
		"updated_at":         b.UpdatedAt,
	}
}

// listBindings GET /svcauthbindings?auth_type=apikey
func listBindings(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	authType := strings.TrimSpace(c.Query("auth_type"))
	list, err := repository.List(info.Schema, authType)
	if err != nil {
		xhttp.ErrJSON(c, "failed to list bindings: "+err.Error(), nil, "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, bindingToMap(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

type createRequest struct {
	AuthType        string `json:"auth_type" binding:"required"` // apikey | oauth
	ExternalIssuer  string `json:"external_issuer"`              // oauth 用（iss）
	ExternalSubject string `json:"external_subject"`             // oauth: sub；apikey 留空则后端生成
	UserID          string `json:"user_id" binding:"required"`   // users.id
}

// createBinding POST /svcauthbindings/create
func createBinding(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if req.AuthType != "apikey" && req.AuthType != "oauth" {
		xhttp.ErrJSON(c, "auth_type must be 'apikey' or 'oauth'", nil, "")
		return
	}

	subject := strings.TrimSpace(req.ExternalSubject)
	issuer := strings.TrimSpace(req.ExternalIssuer)
	if req.AuthType == "apikey" {
		// apikey：issuer 固定为空；未提供 subject 时后端生成明文 secret。
		// key 形如 sk-<随机串>，不再拼租户前缀：租户由请求路径 /service/{mcp|rpc}/{schema} 决定，
		// key 里那一段既不参与校验也不参与路由，留着只会让人以为换租户得换 key。
		issuer = ""
		if subject == "" {
			generated, err := auth.GenerateAPIKey("")
			if err != nil {
				xhttp.ErrJSON(c, "failed to generate secret: "+err.Error(), nil, "")
				return
			}
			subject = generated
		}
	} else {
		// oauth：必须提供 sub，iss 可空（不强制校验 issuer）
		if subject == "" {
			xhttp.ErrJSON(c, "external_subject (sub) is required for oauth", nil, "")
			return
		}
	}

	userID := strings.TrimSpace(req.UserID)
	if err := ensureUserExists(info.Schema, userID); err != nil {
		xhttp.ErrJSON(c, "invalid user_id: "+err.Error(), nil, "")
		return
	}

	b := &models.SvcAuthBinding{
		AuthType:        req.AuthType,
		ExternalIssuer:  issuer,
		ExternalSubject: subject,
		UserID:          userID,
		IsActive:        true,
	}
	if err := repository.Create(info.Schema, b); err != nil {
		xhttp.ErrJSON(c, "failed to create binding: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", bindingToMap(b), "")
}

type updateRequest struct {
	UserID          string `json:"user_id" binding:"required"`
	ExternalSubject string `json:"external_subject"` // 可选：apikey 轮换 secret 时提供
	IsActive        bool   `json:"is_active"`
}

// updateBinding PUT /svcauthbindings/:id
func updateBinding(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	id := c.Param("id")

	existing, err := repository.GetByID(info.Schema, id)
	if err != nil {
		xhttp.ErrJSON(c, "failed to get binding: "+err.Error(), nil, "")
		return
	}
	if existing == nil {
		xhttp.ErrCode(c, "binding.not_found", nil, "Binding not found", "")
		return
	}

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	// 换绑用户时校验目标存在（同 create 的口径：只校验存在，不限制角色）
	if newUserID := strings.TrimSpace(req.UserID); newUserID != existing.UserID {
		if err := ensureUserExists(info.Schema, newUserID); err != nil {
			xhttp.ErrJSON(c, "invalid user_id: "+err.Error(), nil, "")
			return
		}
	}

	existing.UserID = strings.TrimSpace(req.UserID)
	existing.IsActive = req.IsActive
	if s := strings.TrimSpace(req.ExternalSubject); s != "" {
		existing.ExternalSubject = s
	}

	if err := repository.Update(info.Schema, existing); err != nil {
		xhttp.ErrJSON(c, "failed to update binding: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", bindingToMap(existing), "")
}

// deleteBinding DELETE /svcauthbindings/:id
func deleteBinding(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := repository.Delete(info.Schema, id); err != nil {
		xhttp.ErrJSON(c, "failed to delete binding: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", id, "")
}

// listTargetCandidates GET /svcauthbindings/target-candidates
//
// 可授权的候选 = 有**对外开放端点**的 agent / flow。没端点的目标即使勾上也调不到，
// 列出来只会让人以为配好了。
func listTargetCandidates(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	items, err := repository.ListTargetCandidates(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "failed to list target candidates: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

// getBindingTargets GET /svcauthbindings/:id/targets
func getBindingTargets(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	restrict, excludeSupervisor, targets, err := repository.GetTargets(info.Schema, c.Param("id"))
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "binding.not_found", nil, "binding not found", "")
			return
		}
		xhttp.ErrJSON(c, "failed to load targets: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"restrict":           restrict,
		"exclude_supervisor": excludeSupervisor,
		"targets":            targets,
	}, "")
}

type setTargetsRequest struct {
	Restrict bool `json:"restrict"`
	// ExcludeSupervisor 仅在 Restrict=false 时生效：不限制全部端点，但排除超级智能体。
	ExcludeSupervisor bool `json:"exclude_supervisor"`
	Targets           []struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	} `json:"targets"`
}

// setBindingTargets PUT /svcauthbindings/:id/targets
//
// 全量替换。语义：restrict=false 时白名单不生效（保留数据，便于再打开时原样恢复）；
// restrict=true 且列表为空 = 该凭证一个端点都调不到。
func setBindingTargets(c *gin.Context) {
	info, ok := authInfoOf(c)
	if !ok {
		return
	}
	var req setTargetsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	// 目标必须存在且至少有一个对外开放端点 —— 否则配了也是永远调不到的死项
	candidates, err := repository.ListTargetCandidates(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "failed to load target candidates: "+err.Error(), nil, "")
		return
	}
	valid := make(map[string]bool, len(candidates))
	for _, t := range candidates {
		valid[t.TargetType+":"+t.TargetID] = true
	}

	targets := make([]repo.BindingTarget, 0, len(req.Targets))
	seen := make(map[string]bool, len(req.Targets))
	for _, t := range req.Targets {
		tt := strings.TrimSpace(t.TargetType)
		tid := strings.TrimSpace(t.TargetID)
		if tt != "agent" && tt != "flow" {
			xhttp.ErrJSON(c, "target_type must be 'agent' or 'flow'", nil, "")
			return
		}
		if tid == "" {
			xhttp.ErrJSON(c, "target_id is required", nil, "")
			return
		}
		key := tt + ":" + tid
		if !valid[key] {
			xhttp.ErrCode(c, "binding.target_not_exposed", gin.H{"target": key}, fmt.Sprintf("target %s 没有对外开放端点，无法授权", key), "")
			return
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, repo.BindingTarget{TargetType: tt, TargetID: tid})
	}

	// 两档互斥：限制档下「排除超级智能体」没有意义（白名单已经精确表达），repo 会落成 false
	if err := repository.SetTargets(info.Schema, c.Param("id"), req.Restrict, req.ExcludeSupervisor, targets); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "binding.not_found", nil, "binding not found", "")
			return
		}
		xhttp.ErrJSON(c, "failed to save targets: "+err.Error(), nil, "")
		return
	}
	excludeSupervisor := req.ExcludeSupervisor && !req.Restrict
	xhttp.OkJSON(c, "Success", gin.H{
		"restrict":           req.Restrict,
		"exclude_supervisor": excludeSupervisor,
		"count":              len(targets),
	}, "")
}
