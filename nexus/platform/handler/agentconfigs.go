package handler

import (
	"database/sql"
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
// guard down, so the value check is >= 1 (see sql/agent_configs.sql).
//
// A write must be followed by Invalidate / InvalidateAll, otherwise the runtime waits a whole TTL before seeing the new value.
var agentRepo = agentrepo.NewRepository()

func agentNullable(v sql.NullInt32) any {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// agentToJSON turns a config row into the map the frontend expects (NULL -> null).
func agentToJSON(cfg *agentrepo.AgentConfig) gin.H {
	if cfg == nil {
		return gin.H{}
	}
	return gin.H{
		"max_iterations":   agentNullable(cfg.MaxIterations),
		"max_call_depth":   agentNullable(cfg.MaxCallDepth),
		"max_tools":        agentNullable(cfg.MaxTools),
		"max_output_bytes": agentNullable(cfg.MaxOutputBytes),
	}
}

// agentFallbackJSON is the code fallback (the UI uses it to show where an empty field falls back to).
func agentFallbackJSON() gin.H {
	fb := agentconfig.Fallback()
	return gin.H{
		"max_iterations":   fb.MaxIterations,
		"max_call_depth":   fb.MaxCallDepth,
		"max_tools":        fb.MaxTools,
		"max_output_bytes": fb.MaxOutputBytes,
	}
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

// agentField pairs a field name with the submitted value, shared by the check and its message
type agentField struct {
	name  string
	value *int32
}

// agentCheckPositive rejects < 1: this table deliberately does not support -1 (a safety valve that does not limit is
// no valve at all).
func agentCheckPositive(fields []agentField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < 1 {
			return fmt.Errorf("%s must be an integer >= 1 (this config does not support -1 for unlimited)", f.name)
		}
	}
	return nil
}

// agentCheckNotExceed: a tenant value must not exceed the system level (an unset system level means unlimited).
func agentCheckNotExceed(field string, tenantValue, sysValue sql.NullInt32) error {
	if !sysValue.Valid || !tenantValue.Valid {
		return nil
	}
	if tenantValue.Int32 > sysValue.Int32 {
		return fmt.Errorf("%s cannot exceed the system value %d", field, sysValue.Int32)
	}
	return nil
}

// loadAgentBundle reads that tenant's config plus the system level (the system level is for comparison only, never written here).
func loadAgentBundle(schema string) (gin.H, error) {
	sysCfg, err := agentRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	tenantCfg, err := agentRepo.Get(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to load tenant config: %w", err)
	}
	return gin.H{
		"system":   agentToJSON(sysCfg),
		"tenant":   agentToJSON(tenantCfg),
		"fallback": agentFallbackJSON(),
	}, nil
}

// saveAgentConfigs validates (value range + must not exceed the system level), writes and invalidates the cache.
func saveAgentConfigs(schema string, req updateAgentConfigsRequest) (gin.H, error) {
	if err := agentCheckPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		return nil, err
	}

	// The system config is read-only here and only used for the upper-bound check
	sysCfg, err := agentRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	var sys agentrepo.AgentConfig
	if sysCfg != nil {
		sys = *sysCfg
	}

	cfg := &agentrepo.AgentConfig{
		TenantID:       schema,
		MaxIterations:  agentNullInt32(req.MaxIterations),
		MaxCallDepth:   agentNullInt32(req.MaxCallDepth),
		MaxTools:       agentNullInt32(req.MaxTools),
		MaxOutputBytes: agentNullInt32(req.MaxOutputBytes),
	}

	checks := []struct {
		field       string
		tenantValue sql.NullInt32
		sysValue    sql.NullInt32
	}{
		{"max_iterations", cfg.MaxIterations, sys.MaxIterations},
		{"max_call_depth", cfg.MaxCallDepth, sys.MaxCallDepth},
		{"max_tools", cfg.MaxTools, sys.MaxTools},
		{"max_output_bytes", cfg.MaxOutputBytes, sys.MaxOutputBytes},
	}
	for _, chk := range checks {
		if err := agentCheckNotExceed(chk.field, chk.tenantValue, chk.sysValue); err != nil {
			return nil, err
		}
	}

	if err := agentRepo.Upsert(cfg); err != nil {
		return nil, fmt.Errorf("failed to save agent config: %w", err)
	}
	agentconfig.Invalidate(schema)

	return gin.H{"schema": schema, "tenant": agentToJSON(cfg)}, nil
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetSystemAgentConfigs GET /platform/system/agentconfigs
// Returns the system row's raw values plus the code fallback.
func GetSystemAgentConfigs(c *gin.Context) {
	cfg, err := agentRepo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"config":   agentToJSON(cfg),
		"fallback": agentFallbackJSON(),
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
	if err := agentCheckPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	cfg := &agentrepo.AgentConfig{
		TenantID:       "",
		MaxIterations:  agentNullInt32(req.MaxIterations),
		MaxCallDepth:   agentNullInt32(req.MaxCallDepth),
		MaxTools:       agentNullInt32(req.MaxTools),
		MaxOutputBytes: agentNullInt32(req.MaxOutputBytes),
	}
	if err := agentRepo.Upsert(cfg); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save system config: "+err.Error(), "")
		return
	}
	// Once the system row changes, every tenant's merged result changes: dropping that one key is enough
	agentconfig.InvalidateAll()

	xhttp.OkJSON(c, "Success", gin.H{
		"config":   agentToJSON(cfg),
		"fallback": agentFallbackJSON(),
	}, "")
}
