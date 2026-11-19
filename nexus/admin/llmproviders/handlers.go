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

// Package-level repository instance.
var repo = repository.NewRepository()

// GetLLMProvider returns a provider by id.
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", llmProvider, "")
}

// CreateLLMProvider creates a provider.
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	llmProvider.ID = id
	xhttp.OkJSON(c, "Success", llmProvider, "")
}

// UpdateLLMProvider updates a provider.
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteLLMProvider deletes a provider.
// Order: load the provider's models -> deregister each at the gateway (a network call; on failure return at once, DB untouched)
//
//	-> then in one transaction: unlink the agent references, delete the models, delete the provider.
func DeleteLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")
	schema := authInfo.(*auth.AuthInfo).Schema

	// 0. Load every model of the provider (with gateway_id).
	type providerModel struct {
		id        string
		gatewayID sql.NullString
	}
	rows, err := db.DB.Query(
		fmt.Sprintf(`SELECT id, gateway_id FROM %s.llm_models WHERE provider_id = $1`, schema), id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to get LLM models", "")
		return
	}
	var providerModels []providerModel
	for rows.Next() {
		var pm providerModel
		if err := rows.Scan(&pm.id, &pm.gatewayID); err != nil {
			rows.Close()
			xhttp.ErrCode(c, "common.internal", nil, "Failed to scan LLM model", "")
			return
		}
		providerModels = append(providerModels, pm)
	}
	rows.Close()

	// 1. Deregister at the gateway (outside the transaction): any failure returns early and leaves the DB as it was, keeping both sides consistent.
	//    Deprovision is idempotent, so a record the gateway never had also counts as success.
	for _, pm := range providerModels {
		if !pm.gatewayID.Valid || pm.gatewayID.String == "" {
			continue
		}
		if gateway.Current == nil {
			xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
			return
		}
		if derr := gateway.Current.Deprovision(schema,
			gateway.ModelInput{GatewayID: pm.gatewayID.String}, gateway.ProviderInput{}); derr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Deprovision failed for model "+pm.id+": "+derr.Error(), "")
			return
		}
	}

	// 2. Transaction: unlink the agent references -> delete the models -> delete the provider.
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to begin transaction", "")
		return
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// agents.llm_model_id has no foreign key, so it must be cleared explicitly or deleting the model leaves a dangling reference.
	for _, pm := range providerModels {
		if _, err := tx.Exec(
			fmt.Sprintf(`UPDATE %s.agents SET llm_model_id = NULL WHERE llm_model_id = $1`, schema), pm.id); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to update agent instances", "")
			return
		}
	}
	for _, pm := range providerModels {
		if _, err := tx.Exec(
			fmt.Sprintf(`DELETE FROM %s.llm_models WHERE id = $1`, schema), pm.id); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to delete LLM model", "")
			return
		}
	}

	result, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.llm_providers WHERE id = $1`, schema), id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if rowsAffected == 0 {
		xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
		return
	}

	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	tx = nil

	xhttp.OkJSON(c, "LLM provider deleted successfully", id, "")
}

// ListLLMProviders lists providers.
func ListLLMProviders(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Decode the request parameters.
	var req repository.ListLLMProvidersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	llmProviders, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Build the response.
	response := gin.H{
		"items": llmProviders,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}
