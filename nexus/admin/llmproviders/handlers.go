package llmproviders

import (
	"database/sql"
	"fmt"
	"reflect"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/llmproviders"
	"flame/tools/xhttp"
	"flame/tools/xlog"
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
// UpdateLLMProvider updates a provider.
//
// An enabled provider is read-only apart from being disabled. Its url / protocol / api_key / extra_config were copied
// into every model's litellm_params when that model was provisioned, so editing them in place would leave the gateway
// on the old values while the console shows the new ones - the exact failure that looks like "I saved it and nothing
// changed". Disabling is what makes an edit take effect, and disabling cascades: the provider's enabled models are
// deprovisioned at the gateway and lose their gateway_id, so re-enabling them registers them again with the new values.
func UpdateLLMProvider(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")
	schema := authInfo.(*auth.AuthInfo).Schema

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

	current, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// An enabled provider only accepts the transition to disabled; any other change has to wait for that.
	if bool(current.Enabled) && req.Enabled {
		if req.Name != current.Name ||
			req.URL != string(current.URL) ||
			req.Protocol != string(current.Protocol) ||
			req.APIKey != string(current.APIKey) ||
			req.Icon != string(current.Icon) ||
			!jsonValueEqual(req.ExtraConfig, current.ExtraConfig) {
			xhttp.ErrCode(c, "llm.provider_locked", nil,
				"the provider is enabled; disable it before editing", "")
			return
		}
	}

	// Disabling cascades. The gateway call comes first: a failure returns here and leaves both sides untouched.
	enabledModels, err := listEnabledModels(schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load the provider's models: "+err.Error(), "")
		return
	}
	cascade := bool(current.Enabled) && !req.Enabled
	if cascade {
		if gateway.Current == nil {
			xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
			return
		}
		for _, m := range enabledModels {
			if derr := gateway.Current.Deprovision(schema,
				gateway.ModelInput{GatewayID: m.gatewayID}, gateway.ProviderInput{}); derr != nil {
				xhttp.ErrCode(c, "common.internal", nil,
					"Deprovision failed for model "+m.name+": "+derr.Error(), "")
				return
			}
		}
	}

	// One transaction: the provider row and the cleared gateway ids move together, so a model can never point at a
	// record the gateway no longer has.
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

	if cascade {
		for _, m := range enabledModels {
			if _, err := tx.Exec(fmt.Sprintf(
				`UPDATE %s.llm_models SET gateway_id = NULL WHERE id = $1 AND gateway_id = $2`, schema),
				m.id, m.gatewayID); err != nil {
				xhttp.ErrCode(c, "common.internal", nil, "Failed to clear gateway id: "+err.Error(), "")
				return
			}
		}
	}

	result, err := tx.Exec(fmt.Sprintf(`UPDATE %s.llm_providers SET name = $1, url = $2, protocol = $3, api_key = $4,
		icon = $5, enabled = $6, extra_config = $7 WHERE id = $8`, schema),
		req.Name, req.URL, req.Protocol, req.APIKey, req.Icon, req.Enabled, req.ExtraConfig, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		xhttp.ErrCode(c, "llm.provider_not_found", nil, "LLM provider not found", "")
		return
	}

	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	tx = nil

	xhttp.OkJSON(c, "Success", gin.H{"deprovisioned": len(enabledModels)}, "")
}

// providerModel is the slice of a model row this file needs when a provider is disabled.
type providerModel struct {
	id        string
	name      string
	gatewayID string
}

// listEnabledModels returns the provider's models that are currently provisioned at the gateway.
func listEnabledModels(schema, providerID string) ([]providerModel, error) {
	rows, err := db.DB.Query(fmt.Sprintf(
		`SELECT id, name, gateway_id FROM %s.llm_models WHERE provider_id = $1 AND gateway_id IS NOT NULL AND gateway_id <> ''`,
		schema), providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []providerModel
	for rows.Next() {
		var m providerModel
		if err := rows.Scan(&m.id, &m.name, &m.gatewayID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// jsonValueEqual compares two JSONB values by content: a value read from the database and one decoded from a request
// are different Go types ([]byte vs json.RawMessage), so a byte comparison would report a change that is not there.
func jsonValueEqual(a, b xsql.JSONB) bool {
	if a.IsNil() && b.IsNil() {
		return true
	}
	var av, bv any
	if err := a.Unmarshal(&av); err != nil {
		return false
	}
	if err := b.Unmarshal(&bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
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

	// 1. Deregister at the gateway (outside the transaction). Deprovision is idempotent, so a record the gateway
	// never had also counts as success. A gateway that cannot be reached must not make the provider undeletable:
	// the records left behind are cleaned up by the next sync, which removes the gateway rows this tenant no
	// longer references. Failures are collected and reported back so the console can say so.
	var gatewayErrors []string
	for _, pm := range providerModels {
		if !pm.gatewayID.Valid || pm.gatewayID.String == "" {
			continue
		}
		if gateway.Current == nil {
			gatewayErrors = append(gatewayErrors, "gateway not initialized")
			break
		}
		if derr := gateway.Current.Deprovision(schema,
			gateway.ModelInput{GatewayID: pm.gatewayID.String}, gateway.ProviderInput{}); derr != nil {
			gatewayErrors = append(gatewayErrors, pm.id+": "+derr.Error())
			xlog.Error("DeleteLLMProvider: deprovision of model %s (%s) failed: %v", pm.id, pm.gatewayID.String, derr)
		}
	}

	// 2. Transaction: delete the models -> delete the provider. Nothing references a model row by id: agents have no
	// llm_model_id column, they run on the tenant's default model.
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

	xhttp.OkJSON(c, "LLM provider deleted successfully", gin.H{"id": id, "gateway_errors": gatewayErrors}, "")
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
