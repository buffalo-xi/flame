package handler

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/agentconfig"
	agentrepo "flame/nexus/shared/repository/agentconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Agent run limits (platform.agent_configs): the platform side views and adjusts them per tenant, plus the system row.
//
// The entry grouping is exactly symmetrical with the flow run limits (they share mustTenant / mustOwnTenant):
//   - administrator: /platform/tenants/:schema/agentconfigs     - any tenant
//   - "user":        /platform/me/tenants/:schema/agentconfigs  - only tenants under their own name
//   - system row:    /platform/system/agentconfigs              - global default + hard ceiling
//
// The only difference from flow_configs: **no -1 (unlimited)**. All four fields are safety valves, and -1 means tearing the
// guard down, so the value check is >= 1 (see sql/agent_configs.sql). The rules themselves live in shared/agentconfig, so
// the tenant-side handler (admin/agentconfigs) cannot drift from this one.
//
// The body is a **full replacement**: a field that is absent or null is stored as NULL ("not set"), not left untouched.
// The desktop forms always submit all four fields; an API caller must do the same or it will clear the ones it omits.
//
// A write must be followed by Invalidate / InvalidateAll, otherwise the runtime waits a whole TTL before seeing the new value.
var agentRepo = agentrepo.NewRepository()

// agentRowFromStore adapts a stored row; nil stays nil ("no such row").
func agentRowFromStore(cfg *agentrepo.AgentConfig) *agentconfig.Row {
	if cfg == nil {
		return nil
	}
	return &agentconfig.Row{
		MaxIterations:  cfg.MaxIterations,
		MaxCallDepth:   cfg.MaxCallDepth,
		MaxTools:       cfg.MaxTools,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}
}

// agentRowFromRequest turns the submitted body into a row to validate / store (nil means "not set", stored as NULL).
func agentRowFromRequest(req updateAgentConfigsRequest) *agentconfig.Row {
	return &agentconfig.Row{
		MaxIterations:  agentNullInt32(req.MaxIterations),
		MaxCallDepth:   agentNullInt32(req.MaxCallDepth),
		MaxTools:       agentNullInt32(req.MaxTools),
		MaxOutputBytes: agentNullInt32(req.MaxOutputBytes),
	}
}

// agentStoreFromRow is the reverse: the row goes back to the repository shape for the upsert.
func agentStoreFromRow(tenantID string, r *agentconfig.Row) *agentrepo.AgentConfig {
	return &agentrepo.AgentConfig{
		TenantID:       tenantID,
		MaxIterations:  r.MaxIterations,
		MaxCallDepth:   r.MaxCallDepth,
		MaxTools:       r.MaxTools,
		MaxOutputBytes: r.MaxOutputBytes,
	}
}

// agentErrResponse answers a failed save: a rejected value carries its own code + params (the bundled copy is what the
// user reads), everything else is an operational failure.
func agentErrResponse(c *gin.Context, err error) {
	var fe *agentconfig.FieldError
	if errors.As(err, &fe) {
		xhttp.ErrCode(c, fe.Code, fe.Params, fe.Error(), "")
		return
	}
	xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
}

type updateAgentConfigsRequest struct {
	MaxIterations  *int32 `json:"max_iterations"`
	MaxCallDepth   *int32 `json:"max_call_depth"`
	MaxTools       *int32 `json:"max_tools"`
	MaxOutputBytes *int32 `json:"max_output_bytes"`
}

// agentNullInt32 maps nil -> NULL (unset).
func agentNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// loadAgentBundle reads that tenant's config plus the system level (the system level is for comparison only, never written
// here) and the merged result.
//
// "effective" is what a run will really use, which is not always the tenant value: with no system row the code fallback is
// the ceiling, so a stored value can be silently clamped. Returning it lets the UI show the difference instead of leaving
// the user with a saved-but-ignored number.
func loadAgentBundle(schema string) (gin.H, error) {
	sysCfg, err := agentRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	tenantCfg, err := agentRepo.Get(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to load tenant config: %w", err)
	}
	sys, tenant := agentRowFromStore(sysCfg), agentRowFromStore(tenantCfg)
	return gin.H{
		"system":    sys.JSON(),
		"tenant":    tenant.JSON(),
		"effective": agentconfig.Effective(tenant, sys).JSON(),
		"fallback":  agentconfig.Fallback().JSON(),
	}, nil
}

// saveAgentConfigs validates (value range + must not exceed the system level), writes and invalidates the cache.
// A rejected value comes back as *agentconfig.FieldError, an operational failure as a plain error.
func saveAgentConfigs(schema string, req updateAgentConfigsRequest) (gin.H, error) {
	// The system config is read-only here and only used for the upper-bound check
	sysCfg, err := agentRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	sys, cfg := agentRowFromStore(sysCfg), agentRowFromRequest(req)

	if err := agentconfig.ValidateTenant(cfg, sys); err != nil {
		return nil, err
	}

	if err := agentRepo.Upsert(agentStoreFromRow(schema, cfg)); err != nil {
		return nil, fmt.Errorf("failed to save agent config: %w", err)
	}
	agentconfig.Invalidate(schema)

	return gin.H{"schema": schema, "tenant": cfg.JSON()}, nil
}

// GetTenantAgentConfigs GET /platform/tenants/:schema/agentconfigs
func GetTenantAgentConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	data, err := loadAgentBundle(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateTenantAgentConfigs PUT /platform/tenants/:schema/agentconfigs
func UpdateTenantAgentConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	var req updateAgentConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	data, err := saveAgentConfigs(schema, req)
	if err != nil {
		agentErrResponse(c, err)
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetMyTenantAgentConfigs GET /platform/me/tenants/:schema/agentconfigs
func GetMyTenantAgentConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	data, err := loadAgentBundle(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateMyTenantAgentConfigs PUT /platform/me/tenants/:schema/agentconfigs
// "User" self-service: only a tenant under their own name (otherwise 403). The validation and response body match the
// administrator route.
func UpdateMyTenantAgentConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	var req updateAgentConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	data, err := saveAgentConfigs(schema, req)
	if err != nil {
		agentErrResponse(c, err)
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetSystemAgentConfigs GET /platform/system/agentconfigs
// Returns the system row's raw values plus the code fallback. No "effective" here: on the system level it is the same
// number as the stored value (or the fallback, which the UI already gets).
func GetSystemAgentConfigs(c *gin.Context) {
	cfg, err := agentRepo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"config":   agentRowFromStore(cfg).JSON(),
		"fallback": agentconfig.Fallback().JSON(),
	}, "")
}

// UpdateSystemAgentConfigs PUT /platform/system/agentconfigs
// Writes the tenant_id = '' row. It is the top level, so only the validity of the values is checked; there is no "must not
// exceed the parent".
func UpdateSystemAgentConfigs(c *gin.Context) {
	var req updateAgentConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	cfg := agentRowFromRequest(req)
	if err := agentconfig.ValidateValues(cfg); err != nil {
		agentErrResponse(c, err)
		return
	}

	if err := agentRepo.Upsert(agentStoreFromRow("", cfg)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save system config: "+err.Error(), "")
		return
	}
	// Once the system row changes, every tenant's merged result changes: dropping that one key is enough
	agentconfig.InvalidateAll()

	xhttp.OkJSON(c, "Success", gin.H{
		"config":   cfg.JSON(),
		"fallback": agentconfig.Fallback().JSON(),
	}, "")
}
