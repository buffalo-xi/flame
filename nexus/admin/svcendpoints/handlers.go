package svcendpoints

import (
	"database/sql"
	"flame/tools/xsql"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var repo = repository.NewRepository()

// GetSvcEndpoint 根据ID获取服务端点
func GetSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	svcEndpoint, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "Service endpoint not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", svcEndpoint, "")
}

// CreateSvcEndpoint 创建服务端点
func CreateSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	var req struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		TargetID    string     `json:"target_id"`
		TargetType  string     `json:"target_type"`
		Protocol    string     `json:"protocol"`
		InputSchema xsql.JSONB `json:"input_schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	svcEndpoint := models.SvcEndpoint{
		Name:        req.Name,
		Description: req.Description,
		TargetID:    req.TargetID,
		TargetType:  req.TargetType,
		Protocol:    req.Protocol,
		InputSchema: req.InputSchema,
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &svcEndpoint)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	svcEndpoint.ID = id
	xhttp.OkJSON(c, "Success", svcEndpoint, "")
}

// UpdateSvcEndpoint 更新服务端点
func UpdateSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	var req struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		InputSchema xsql.JSONB `json:"input_schema"`
		Exposed     *bool      `json:"exposed"` // 指针区分未传；用于对外开放 / 收回切换
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 仅切换对外状态（不影响 name / description / input_schema）
	if req.Exposed != nil {
		if err := repo.UpdateExposed(authInfo.(*auth.AuthInfo), id, *req.Exposed); err != nil {
			if err == sql.ErrNoRows {
				xhttp.ErrJSON(c, "Service endpoint not found", nil, "")
				return
			}
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		xhttp.OkJSON(c, "Success", nil, "")
		return
	}

	svcEndpoint := models.SvcEndpoint{
		Name:        req.Name,
		Description: req.Description,
		InputSchema: req.InputSchema,
	}

	err := repo.Update(authInfo.(*auth.AuthInfo), id, &svcEndpoint)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrJSON(c, "Service endpoint not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSvcEndpointsByTarget 删除指定目标的所有协议端点
func DeleteSvcEndpointsByTarget(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	err := repo.DeleteByTarget(authInfo.(*auth.AuthInfo), req.TargetType, req.TargetID)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// RetractSvcEndpoints 收回指定目标所有协议端点的对外（保留记录，仅置 exposed=false）
func RetractSvcEndpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	if err := repo.RetractByTarget(authInfo.(*auth.AuthInfo), req.TargetType, req.TargetID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSvcEndpoint 删除服务端点
func DeleteSvcEndpoint(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	id := c.Param("id")

	err := repo.Delete(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Service endpoint deleted successfully", id, "")
}

// ListSvcEndpoints 获取服务端点列表
func ListSvcEndpoints(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}

	// 解析请求参数
	var req repository.ListSvcEndpointsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	svcEndpoints, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 构建响应
	response := gin.H{
		"items": svcEndpoints,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}
