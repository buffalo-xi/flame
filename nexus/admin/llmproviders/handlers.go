package llmproviders

import (
	"database/sql"
	"fmt"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/llmproviders"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var repo = repository.NewRepository()

// GetLLMProvider 根据ID获取LLM服务商
func GetLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	llmProvider, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", llmProvider, "")
}

// CreateLLMProvider 创建LLM服务商
func CreateLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		Name        string     `json:"name"`
		URL         string     `json:"url"`
		Protocol    string     `json:"protocol"`
		APIKey      string     `json:"api_key"`
		Icon        string     `json:"icon"`
		Enabled     bool       `json:"enabled"`
		ExtraConfig xsql.JSONB `json:"extra_config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	llmProvider := models.LLMProvider{
		Name:        req.Name,
		URL:         xsql.String(req.URL),
		Protocol:    xsql.String(req.Protocol),
		APIKey:      xsql.String(req.APIKey),
		Icon:        xsql.String(req.Icon),
		Enabled:     xsql.Bool(req.Enabled),
		ExtraConfig: req.ExtraConfig,
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &llmProvider)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	llmProvider.ID = id
	xhttp.OkJSON(c, "Success", llmProvider, "")
}

// UpdateLLMProvider 更新LLM服务商
func UpdateLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req struct {
		Name        string     `json:"name"`
		URL         string     `json:"url"`
		Protocol    string     `json:"protocol"`
		APIKey      string     `json:"api_key"`
		Icon        string     `json:"icon"`
		Enabled     bool       `json:"enabled"`
		ExtraConfig xsql.JSONB `json:"extra_config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	llmProvider := models.LLMProvider{
		Name:        req.Name,
		URL:         xsql.String(req.URL),
		Protocol:    xsql.String(req.Protocol),
		APIKey:      xsql.String(req.APIKey),
		Icon:        xsql.String(req.Icon),
		Enabled:     xsql.Bool(req.Enabled),
		ExtraConfig: req.ExtraConfig,
	}

	err := repo.Update(authInfo.(*auth.AuthInfo), id, &llmProvider)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteLLMProvider 删除LLM服务商。
// 顺序：先取该服务商下的模型 → 逐个网关注销（网络调用，失败即返回，DB 不动）
//
//	→ 事务内解开 agent 引用、删模型、删服务商。
func DeleteLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")
	schema := authInfo.(*auth.AuthInfo).Schema

	// 0. 取出该服务商下的所有模型（含 gateway_id）
	type providerModel struct {
		id        string
		gatewayID sql.NullString
	}
	rows, err := db.DB.Query(
		fmt.Sprintf(`SELECT id, gateway_id FROM %s.llm_models WHERE provider_id = $1`, schema), id)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to get LLM models", nil, "")
		return
	}
	var providerModels []providerModel
	for rows.Next() {
		var pm providerModel
		if err := rows.Scan(&pm.id, &pm.gatewayID); err != nil {
			rows.Close()
			xhttp.ErrJSON(c, "Failed to scan LLM model", nil, "")
			return
		}
		providerModels = append(providerModels, pm)
	}
	rows.Close()

	// 1. 网关注销（在事务外）：任一条失败即返回，DB 保持原样，保证两端一致。
	//    Deprovision 幂等，网关本来就没有该记录时也算成功。
	for _, pm := range providerModels {
		if !pm.gatewayID.Valid || pm.gatewayID.String == "" {
			continue
		}
		if gateway.Current == nil {
			xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
			return
		}
		if derr := gateway.Current.Deprovision(schema,
			gateway.ModelInput{GatewayID: pm.gatewayID.String}, gateway.ProviderInput{}); derr != nil {
			xhttp.ErrJSON(c, "Deprovision failed for model "+pm.id+": "+derr.Error(), nil, "")
			return
		}
	}

	// 2. 事务：解开 agent 引用 → 删模型 → 删服务商
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to begin transaction", nil, "")
		return
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// agents.llm_model_id 无外键，必须显式清，否则删完模型会留下悬空引用
	for _, pm := range providerModels {
		if _, err := tx.Exec(
			fmt.Sprintf(`UPDATE %s.agents SET llm_model_id = NULL WHERE llm_model_id = $1`, schema), pm.id); err != nil {
			xhttp.ErrJSON(c, "Failed to update agent instances", nil, "")
			return
		}
	}
	for _, pm := range providerModels {
		if _, err := tx.Exec(
			fmt.Sprintf(`DELETE FROM %s.llm_models WHERE id = $1`, schema), pm.id); err != nil {
			xhttp.ErrJSON(c, "Failed to delete LLM model", nil, "")
			return
		}
	}

	result, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.llm_providers WHERE id = $1`, schema), id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if rowsAffected == 0 {
		xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
		return
	}

	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	tx = nil

	xhttp.OkJSON(c, "LLM provider deleted successfully", id, "")
}

// ListLLMProviders 获取LLM服务商列表
func ListLLMProviders(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// 解析请求参数
	var req repository.ListLLMProvidersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	llmProviders, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 构建响应
	response := gin.H{
		"items": llmProviders,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}
