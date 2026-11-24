package llmmodels

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	repository "flame/nexus/shared/repository/llmmodels"
	providerRepo "flame/nexus/shared/repository/llmproviders"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// Global repository instances.
var repo = repository.NewRepository()
var providerRepository = providerRepo.NewRepository()

// GetLLMModel returns one LLM model by id.
func GetLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	llmModel, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", llmModel, "")
}

// CreateLLMModel creates an LLM model.
func CreateLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req struct {
		Name          string     `json:"name"`
		ContextWindow xsql.Int   `json:"context_window"`
		BaseParams    xsql.JSONB `json:"base_params"`
		ProviderID    string     `json:"provider_id"`
		GatewayModel  string     `json:"gateway_model"`
		IsDefault     bool       `json:"is_default"`
		ExtraParams   xsql.JSONB `json:"extra_params"`
		Capabilities  xsql.JSONB `json:"capabilities"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	llmModel := models.LLMModel{
		Name:          req.Name,
		ContextWindow: req.ContextWindow,
		BaseParams:    req.BaseParams,
		ProviderID:    req.ProviderID,
		GatewayModel:  xsql.String(req.GatewayModel),
		IsDefault:     xsql.Bool(req.IsDefault),
		ExtraParams:   req.ExtraParams,
		Capabilities:  req.Capabilities,
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &llmModel)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	llmModel.ID = id
	xhttp.OkJSON(c, "Success", llmModel, "")
}

// UpdateLLMModel updates an LLM model.
func UpdateLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req struct {
		Name          string     `json:"name"`
		ContextWindow xsql.Int   `json:"context_window"`
		BaseParams    xsql.JSONB `json:"base_params"`
		ProviderID    string     `json:"provider_id"`
		GatewayModel  string     `json:"gateway_model"`
		IsDefault     bool       `json:"is_default"`
		ExtraParams   xsql.JSONB `json:"extra_params"`
		Capabilities  xsql.JSONB `json:"capabilities"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// An enabled model is provisioned at the gateway, with this row's values baked into litellm_params when it was
	// provisioned. Editing it in place would leave the gateway on the old values while the console shows the new ones,
	// so an enabled row is read-only apart from is_default - choosing the default model is an operation, not a
	// configuration. Disable it first: that deprovisions the gateway record and clears gateway_id, and enabling it
	// again registers it with the new values.
	existing, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if string(existing.GatewayID) != "" && !sameExceptDefault(req.Name, req.ContextWindow, req.ProviderID,
		req.GatewayModel, req.BaseParams, req.ExtraParams, req.Capabilities, existing) {
		xhttp.ErrCode(c, "llm.model_enabled_locked", nil, "the model is enabled; disable it before editing", "")
		return
	}

	llmModel := models.LLMModel{
		Name:          req.Name,
		ContextWindow: req.ContextWindow,
		BaseParams:    req.BaseParams,
		ProviderID:    req.ProviderID,
		GatewayModel:  xsql.String(req.GatewayModel),
		IsDefault:     xsql.Bool(req.IsDefault),
		ExtraParams:   req.ExtraParams,
		Capabilities:  req.Capabilities,
	}

	err = repo.Update(authInfo.(*auth.AuthInfo), id, &llmModel)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// sameExceptDefault reports whether the submitted fields match the stored row in everything but is_default.
func sameExceptDefault(name string, contextWindow xsql.Int, providerID, gatewayModel string,
	baseParams, extraParams, capabilities xsql.JSONB, existing *models.LLMModel) bool {
	return name == existing.Name &&
		contextWindow == existing.ContextWindow &&
		providerID == existing.ProviderID &&
		gatewayModel == string(existing.GatewayModel) &&
		jsonbEqual(baseParams, existing.BaseParams) &&
		jsonbEqual(extraParams, existing.ExtraParams) &&
		jsonbEqual(capabilities, existing.Capabilities)
}

// jsonbEqual compares two jsonb values by content: a value read from the database and one decoded from a request are
// different Go types ([]byte vs json.RawMessage), so a byte comparison would report a change that is not there.
func jsonbEqual(a, b xsql.JSONB) bool {
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
	return jsonValueEqual(av, bv)
}

// DeleteLLMModel deletes an LLM model.
// An enabled model (non-empty gateway_id) is deprovisioned on the gateway first, so LiteLLM keeps no orphan record;
// then the agent references are cleared and the row deleted in one transaction.
func DeleteLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// 1. Deprovision first when the model is enabled. A gateway that cannot be reached must not make the row
	// undeletable: the record left behind is cleaned up by the next sync, which removes the gateway rows this
	// tenant no longer references. The failure is still reported back so the console can say so.
	var gatewayErr string
	if gatewayID := string(m.GatewayID); gatewayID != "" {
		if gateway.Current == nil {
			gatewayErr = "gateway not initialized"
		} else if derr := gateway.Current.Deprovision(info.Schema,
			gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{}); derr != nil {
			gatewayErr = derr.Error()
			xlog.Error("DeleteLLMModel: deprovision of %q (%s) failed: %v", m.Name, gatewayID, derr)
		}
	}

	// 2. clear the agent references and delete the model row
	if err := repo.DeleteCascade(info, id); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xlog.Error("DeleteLLMModel: deleting %q (%s) failed: %v", m.Name, id, err)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "LLM model deleted successfully", gin.H{"id": id, "gateway_error": gatewayErr}, "")
}

// ListLLMModels returns the LLM model list.
func ListLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Parse the request parameters
	var req repository.ListLLMModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	llmModels, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Build the response
	response := gin.H{
		"items": llmModels,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// ListLLMModelsByProviderID returns the LLM models of one provider.
func ListLLMModelsByProviderID(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	providerID := c.Param("provider_id")

	// Parse the request parameters
	var req repository.ListLLMModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	llmModels, total, err := repo.ListByProviderID(authInfo.(*auth.AuthInfo), providerID, &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Build the response
	response := gin.H{
		"items": llmModels,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// GetDefaultLLMModel returns the default model.
func GetDefaultLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	llmModel, err := repo.GetDefault(authInfo.(*auth.AuthInfo))
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "Default LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", llmModel, "")
}

// ListSelectableLLMModels returns the models selectable in the LLM node dropdown:
// provider.enabled=true with a non-empty gateway_id, deduplicated with DISTINCT
func ListSelectableLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	llmModels, err := repo.ListSelectable(authInfo.(*auth.AuthInfo))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	response := gin.H{
		"items": llmModels,
		"total": len(llmModels),
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// ProvisionLLMModel enables an LLM model: registers it on the LiteLLM gateway and writes gateway_id back to the DB
func ProvisionLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	// 1. look up the model
	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// An existing gateway_id returns idempotently
	if string(m.GatewayID) != "" {
		xhttp.OkJSON(c, "already provisioned", m, "")
		return
	}

	// 2. look up the provider to build ProviderInput
	provider, err := providerRepository.GetByID(info, m.ProviderID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the llm provider: "+err.Error(), "")
		return
	}

	// 2.1 the provider must be enabled - otherwise a model is registered on the gateway that the dropdown cannot reach
	if !bool(provider.Enabled) {
		xhttp.ErrCode(c, "llm.provider_disabled", nil, "the provider is disabled; enable it under LLM providers before enabling the model", "")
		return
	}

	// 2.2 gateway_model becomes the LiteLLM model_name, and enabled models must not share one.
	// This model's gateway_id is empty at this point, so the count excludes itself.
	gatewayModel := string(m.GatewayModel)
	if gatewayModel == "" {
		xhttp.ErrCode(c, "llm.gateway_model_required", nil, "gateway_model cannot be empty; fill it in before enabling", "")
		return
	}
	dup, err := repo.CountEnabledByGatewayModel(info, gatewayModel)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to check the gateway_model uniqueness: "+err.Error(), "")
		return
	}
	if dup > 0 {
		xhttp.ErrCode(c, "llm.gateway_model_taken", gin.H{"name": gatewayModel}, "the gateway model name "+gatewayModel+" is already used by another enabled model, which makes gateway routing ambiguous; use a different gateway_model", "")
		return
	}

	// 3. build ModelInput / ProviderInput (the LiteLLM call happens outside the lock)
	modelInput, providerInput, err := buildGatewayInputs(m, provider)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	gatewayID, err := gateway.Current.Provision(info.Schema, modelInput, providerInput)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "provision failed: "+err.Error(), "")
		return
	}

	// 4. lock the row against concurrency: if someone else provisioned it, deprovision what was just registered
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	existing, err := repo.LockAndGetGatewayID(tx, info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if existing != "" {
		// provisioned by a concurrent request: deprovision what was just registered and return the existing id
		tx.Rollback()
		tx = nil
		_ = gateway.Current.Deprovision(info.Schema, gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{})
		xhttp.OkJSON(c, "already provisioned by concurrent request", gin.H{"gateway_id": existing}, "")
		return
	}

	if err := repo.SetGatewayIDInTx(tx, info, id, gatewayID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	tx = nil

	xhttp.OkJSON(c, "Success", gin.H{"gateway_id": gatewayID}, "")
}

// DeprovisionLLMModel disables an LLM model: removes it from the LiteLLM gateway and clears gateway_id conditionally
func DeprovisionLLMModel(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	// 1. look up the model
	m, err := repo.GetByID(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "llm.model_not_found", nil, "LLM model not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	gatewayID := string(m.GatewayID)
	if gatewayID == "" {
		// idempotent return
		xhttp.OkJSON(c, "already deprovisioned", m, "")
		return
	}

	// 2. delete on LiteLLM first (a network call outside the lock)
	if err := gateway.Current.Deprovision(info.Schema,
		gateway.ModelInput{GatewayID: gatewayID}, gateway.ProviderInput{}); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "deprovision failed: "+err.Error(), "")
		return
	}

	// 3. lock the row and clear conditionally (only when gateway_id still equals gatewayID)
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	affected, err := repo.ClearGatewayIDInTx(tx, info, id, gatewayID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	tx = nil

	// affected == 0: the row was provisioned again between the LiteLLM delete and the DB clear, so the new id must not be cleared
	if affected == 0 {
		xlog.Info("DeprovisionLLMModel: gateway_id changed concurrently for model %s, skip clearing", id)
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// DeprovisionAllLLMModels deprovisions every model of this tenant registered on the gateway:
// 1. call the gateway's DeprovisionAll to remove every model under the schema prefix (a failure returns at once, DB untouched)
// 2. on success, clear gateway_id on every model of this tenant that has one
func DeprovisionAllLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	// 1. delete in bulk on the gateway, returning how many models were removed (a failure returns at once and the DB stays untouched so both sides agree)
	count, err := gateway.Current.DeprovisionAll(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "deprovision all failed: "+err.Error(), "")
		return
	}

	// 2. clear gateway_id in bulk
	if err := repo.ClearAllGatewayIDs(info); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to clear the gateway ids: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"count": count}, "")
}

// bpFloat pulls a float parameter out of the base_params map.
// A missing key or a mismatched type returns (0, false), and the caller decides whether to use the parameter.
func bpFloat(bp map[string]any, key string) (float64, bool) {
	v, ok := bp[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ---- Gateway sync ----

// syncSkip is a skipped model (only enabled ones are synced; the rest are listed so the operator knows)
type syncSkip struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// syncFailure is a model that failed to sync (one failure does not abort the whole run)
type syncFailure struct {
	Model string `json:"model"`
	Error string `json:"error"`
}

// SyncLLMModels syncs each local LLM model configuration to the LiteLLM gateway.
//
// Each model is decided by gateway_id, the LiteLLM model_id UUID (model_name can repeat and is not reliable):
//
//  1. enabled (non-empty gateway_id) but unknown to the gateway -> register again and write the gateway_id back (created);
//  2. not enabled (empty gateway_id) -> not registered, recorded in skipped;
//  3. present on the gateway with differing managed fields -> Update overwrites (updated), otherwise unchanged;
//  4. records under this schema prefix that no local gateway_id references after the sync -> deleted;
//  5. a single failure does not abort; failures are collected into errors.
//
// The delete scope is strictly the model_name prefix "<schema>:" - LiteLLM is one instance shared by many tenants,
// and without the prefix other tenants' models would be deleted.
func SyncLLMModels(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	locals, err := repo.ListAll(info)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list the llm models: "+err.Error(), "")
		return
	}

	remote, err := gateway.Current.ListProvisioned(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list the gateway models: "+err.Error(), "")
		return
	}
	remoteByID := make(map[string]gateway.ProvisionedModel, len(remote))
	for _, r := range remote {
		remoteByID[r.GatewayID] = r
	}

	providerCache := make(map[string]*models.LLMProvider)

	var (
		created   int
		updated   int
		unchanged int
		deleted   int
		skipped   = []syncSkip{}
		failures  = []syncFailure{}
		// keepIDs tracks the gateway ids still referenced locally after the sync, to decide what is stale
		keepIDs = make(map[string]bool, len(locals))
	)

	for i := range locals {
		m := &locals[i]
		name := m.Name

		// Rule 2: not enabled - not registered, not touched, just recorded so the operator knows
		gatewayID := string(m.GatewayID)
		if gatewayID == "" {
			skipped = append(skipped, syncSkip{Model: name, Reason: "not enabled, not registered"})
			continue
		}

		// Register it first: even if a later step fails, its gateway record must not be treated as stale and deleted
		keepIDs[gatewayID] = true

		provider, perr := getProviderCached(providerCache, info, m.ProviderID)
		if perr != nil {
			failures = append(failures, syncFailure{Model: name, Error: "failed to read the provider: " + perr.Error()})
			continue
		}
		if provider == nil {
			failures = append(failures, syncFailure{Model: name, Error: "the provider does not exist (provider_id=" + m.ProviderID + "）"})
			continue
		}

		modelInput, providerInput, ierr := buildGatewayInputs(m, provider)
		if ierr != nil {
			failures = append(failures, syncFailure{Model: name, Error: ierr.Error()})
			continue
		}

		remoteModel, onGateway := remoteByID[gatewayID]
		if !onGateway {
			// Rule 1: enabled but unknown to the gateway -> clear the old id to force a create, then write the gateway_id back
			modelInput.GatewayID = ""
			newID, rerr := gateway.Current.Provision(info.Schema, modelInput, providerInput)
			if rerr != nil {
				failures = append(failures, syncFailure{Model: name, Error: "registration failed: " + rerr.Error()})
				continue
			}
			if uerr := repo.UpdateGatewayID(info, m.ID, newID); uerr != nil {
				failures = append(failures, syncFailure{Model: name, Error: "registered on the gateway but writing gateway_id back failed: " + uerr.Error()})
				continue
			}
			delete(keepIDs, gatewayID)
			keepIDs[newID] = true
			created++
			continue
		}

		// Rule 3: already present - compare managed fields only and overwrite only on a difference
		if gatewayModelMatches(remoteModel, info.Schema, modelInput, providerInput) {
			unchanged++
			continue
		}
		modelInput.GatewayID = gatewayID
		if uerr := gateway.Current.Update(info.Schema, modelInput, providerInput); uerr != nil {
			failures = append(failures, syncFailure{Model: name, Error: "update failed: " + uerr.Error()})
			continue
		}
		updated++
	}

	// Rule 4: clean up stale gateway records (this schema prefix only - ListProvisioned already filtered remote).
	// remote is the snapshot taken before the loop, so records created now are not in it and cannot delete themselves.
	for _, r := range remote {
		if keepIDs[r.GatewayID] {
			continue
		}
		if derr := gateway.Current.Deprovision(info.Schema,
			gateway.ModelInput{GatewayID: r.GatewayID}, gateway.ProviderInput{}); derr != nil {
			failures = append(failures, syncFailure{Model: r.ModelName, Error: "failed to delete the stale record: " + derr.Error()})
			continue
		}
		deleted++
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"created":   created,
		"updated":   updated,
		"unchanged": unchanged,
		"deleted":   deleted,
		"skipped":   skipped,
		"errors":    failures,
	}, "")
}

// getProviderCached returns a provider by provider_id (nil means it does not exist).
// The sync walks every model, so each provider is queried once.
func getProviderCached(cache map[string]*models.LLMProvider, info *auth.AuthInfo, providerID string) (*models.LLMProvider, error) {
	if p, ok := cache[providerID]; ok {
		return p, nil
	}
	p, err := providerRepository.GetByID(info, providerID)
	if err != nil {
		if err == sql.ErrNoRows {
			cache[providerID] = nil
			return nil, nil
		}
		return nil, err
	}
	cache[providerID] = p
	return p, nil
}

// buildGatewayInputs turns a DB model + provider into gateway inputs.
// Provision / Update / sync all share it, so what reaches the gateway is always identical.
//
// Note the meaning of GatewayModel: it is the model field of a chat request and must be "<schema>:<gateway_model>".
// This function only maps DB -> inputs; the caller adds the prefix.
func buildGatewayInputs(m *models.LLMModel, provider *models.LLMProvider) (gateway.ModelInput, gateway.ProviderInput, error) {
	// The gateway model name must come from gateway_model in the DB and cannot be empty
	gatewayModel := string(m.GatewayModel)
	if gatewayModel == "" {
		return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("gateway_model cannot be empty; fill it in before enabling")
	}

	var extraParams map[string]any
	if !m.ExtraParams.IsNil() {
		if err := m.ExtraParams.Unmarshal(&extraParams); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse extra_params: %w", err)
		}
	}

	// Unpack base_params into parameters (a key present = used, absent = unused)
	var bp map[string]any
	if !m.BaseParams.IsNil() {
		if err := m.BaseParams.Unmarshal(&bp); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse base_params: %w", err)
		}
	}
	var maxTokens int
	if v, ok := bpFloat(bp, "max_tokens"); ok {
		maxTokens = int(v)
	}
	var temperature float64
	if v, ok := bpFloat(bp, "temperature"); ok {
		temperature = v
	}
	var topP float64
	if v, ok := bpFloat(bp, "top_p"); ok {
		topP = v
	}

	// The provider's extra configuration is passed through to LiteLLM as is, for api_version (Azure),
	// vertex_project / vertex_location / vertex_credentials (Vertex AI), timeout / max_retries and so on
	var providerExtra map[string]any
	if !provider.ExtraConfig.IsNil() {
		if err := provider.ExtraConfig.Unmarshal(&providerExtra); err != nil {
			return gateway.ModelInput{}, gateway.ProviderInput{}, fmt.Errorf("Failed to parse provider extra_config: %w", err)
		}
	}

	return gateway.ModelInput{
		ID:            m.ID,
		Name:          m.Name,
		GatewayModel:  gatewayModel,
		GatewayID:     string(m.GatewayID),
		ContextWindow: int(m.ContextWindow),
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		TopP:          topP,
		ExtraParams:   extraParams,
	}, gateway.ProviderInput{
		ID:          provider.ID,
		Name:        provider.Name,
		URL:         string(provider.URL),
		APIKey:      string(provider.APIKey),
		Protocol:    string(provider.Protocol),
		ExtraConfig: providerExtra,
	}, nil
}

// gatewayModelMatches reports whether the gateway record matches the local configuration.
// Only the fields we manage are compared: model_name, litellm_params.model/api_base/api_key,
// the keys provider extra_config expands into litellm_params,
// and model_info.context_window/max_tokens plus the keys model extra_params expands into.
// LiteLLM adds keys such as tpm / rpm / use_in_pass_through itself, and comparing the whole JSON would read them as differences,
// making every sync Update for no reason; hence the managed subset only.
func gatewayModelMatches(r gateway.ProvisionedModel, schema string, model gateway.ModelInput, provider gateway.ProviderInput) bool {
	if r.ModelName != schema+":"+model.GatewayModel {
		return false
	}

	// litellm_params.model must be "<protocol>/<model.Name>"
	if want := provider.Protocol + "/" + model.Name; r.LitellmParams["model"] != want {
		return false
	}
	// api_base must match provider.URL
	if provider.URL != "" {
		if got, _ := r.LitellmParams["api_base"].(string); got != provider.URL {
			return false
		}
	}
	// api_key must match. When the gateway references os.environ/ or returns a masked value (containing *), a direct comparison is impossible, so it is skipped
	// to avoid turning every sync into an overwrite.
	if provider.APIKey != "" {
		if got, _ := r.LitellmParams["api_key"].(string); got != "" &&
			!strings.HasPrefix(got, "os.environ/") && !strings.Contains(got, "*") {
			if got != provider.APIKey {
				return false
			}
		}
	}

	// provider extra_config is merged into litellm_params key by key, so only the keys we write are compared.
	// Missing this would let a changed extra_config (api_version, say) pass as no difference and never be pushed.
	for k, want := range provider.ExtraConfig {
		if !jsonValueEqual(r.LitellmParams[k], want) {
			return false
		}
	}

	// model_info: unused parameters count as 0 (buildModelInfoMap only writes the key when >0)
	if !infoNumEqual(r.ModelInfo, "context_window", float64(model.ContextWindow)) {
		return false
	}
	if !infoNumEqual(r.ModelInfo, "max_tokens", float64(model.MaxTokens)) {
		return false
	}
	// extra_params is merged into model_info key by key, so only the keys we write are compared
	for k, want := range model.ExtraParams {
		if !jsonValueEqual(r.ModelInfo[k], want) {
			return false
		}
	}
	return true
}

// infoNumEqual compares a numeric key in model_info: when the key is missing, an expected 0 counts as equal
func infoNumEqual(info map[string]any, key string, want float64) bool {
	v, ok := info[key]
	if !ok || v == nil {
		return want == 0
	}
	f, ok := toFloat(v)
	if !ok {
		return false
	}
	return f == want
}

// jsonValueEqual compares two JSON values; numbers are compared as float64 so int/float differences are not misread
func jsonValueEqual(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

// toFloat turns a number decoded from JSON into float64
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
