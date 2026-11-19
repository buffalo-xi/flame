package agentconfigs

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/agentconfig"
	"flame/nexus/shared/auth"
	agentconfigrepo "flame/nexus/shared/repository/agentconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// Agent run limits (the tenant rows of platform.agent_configs).
//
// The one difference from the flow limits (admin/flowconfigs): **there is no -1 (unlimited)**.
// All four are safety valves rather than quotas - max_iterations = -1 would disable the loop guard,
// so the rule is "an integer >= 1", not the flow side's "-1 or non-negative".
// The upper-bound check (never above the system value) matches flows exactly.
// ============================================================================

var repo = agentconfigrepo.NewRepository()

// nullable turns a sql.NullInt32 into an interface{} (the number when Valid, nil otherwise).
func nullable(v sql.NullInt32) interface{} {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// toJSON renders a config row as a JSON-friendly map (NULL columns become null).
func toJSON(cfg *agentconfigrepo.AgentConfig) gin.H {
	if cfg == nil {
		return gin.H{}
	}
	return gin.H{
		"max_iterations":   nullable(cfg.MaxIterations),
		"max_call_depth":   nullable(cfg.MaxCallDepth),
		"max_tools":        nullable(cfg.MaxTools),
		"max_output_bytes": nullable(cfg.MaxOutputBytes),
	}
}

// GetAgentConfigs returns the system-wide config plus the current tenant's (the system one is shown for reference).
func GetAgentConfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	tenantCfg, err := repo.Get(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load tenant config: "+err.Error(), "")
		return
	}

	fb := agentconfig.Fallback()
	xhttp.OkJSON(c, "Success", gin.H{
		"system": toJSON(sysCfg),
		"tenant": toJSON(tenantCfg),
		// fallback lets the UI say where an empty field falls back to, matching the platform system settings page.
		"fallback": gin.H{
			"max_iterations":   fb.MaxIterations,
			"max_call_depth":   fb.MaxCallDepth,
			"max_tools":        fb.MaxTools,
			"max_output_bytes": fb.MaxOutputBytes,
		},
	}, "")
}

// updateRequest is the tenant config submitted by the frontend (null means unset, stored as NULL).
type updateRequest struct {
	MaxIterations  *int32 `json:"max_iterations"`
	MaxCallDepth   *int32 `json:"max_call_depth"`
	MaxTools       *int32 `json:"max_tools"`
	MaxOutputBytes *int32 `json:"max_output_bytes"`
}

// toNullInt32 turns *int32 into sql.NullInt32 (nil means unset).
func toNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// agentFields lists the four column names so validation and the UI notes share one copy.
type agentField struct {
	name  string
	value *int32
}

// checkPositive rejects values below 1. **-1 is deliberately unsupported**:
// all four are safety valves, and -1 would switch off the loop, recursion and context guards at once.
func checkPositive(fields []agentField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < 1 {
			return fmt.Errorf("%s must be an integer >= 1 (this setting does not support -1 for unlimited)", f.name)
		}
	}
	return nil
}

// checkNotExceed keeps the tenant value within the system one (unlimited when it is unset).
func checkNotExceed(field string, tenantVal, sysVal sql.NullInt32) error {
	if !sysVal.Valid {
		return nil
	}
	if !tenantVal.Valid {
		return nil
	}
	if tenantVal.Int32 > sysVal.Int32 {
		return fmt.Errorf("%s cannot exceed the system value %d", field, sysVal.Int32)
	}
	return nil
}

// UpdateTenantAgentConfigs updates the run limits of the current tenant (range and system bound checked before saving).
func UpdateTenantAgentConfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	if err := checkPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Read the system config for the upper-bound checks.
	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	var sysZero agentconfigrepo.AgentConfig
	if sysCfg != nil {
		sysZero = *sysCfg
	}

	cfg := &agentconfigrepo.AgentConfig{
		TenantID:       info.Schema,
		MaxIterations:  toNullInt32(req.MaxIterations),
		MaxCallDepth:   toNullInt32(req.MaxCallDepth),
		MaxTools:       toNullInt32(req.MaxTools),
		MaxOutputBytes: toNullInt32(req.MaxOutputBytes),
	}

	checks := []struct {
		field     string
		tenantVal sql.NullInt32
		sysVal    sql.NullInt32
	}{
		{"max_iterations", cfg.MaxIterations, sysZero.MaxIterations},
		{"max_call_depth", cfg.MaxCallDepth, sysZero.MaxCallDepth},
		{"max_tools", cfg.MaxTools, sysZero.MaxTools},
		{"max_output_bytes", cfg.MaxOutputBytes, sysZero.MaxOutputBytes},
	}
	for _, chk := range checks {
		if err := checkNotExceed(chk.field, chk.tenantVal, chk.sysVal); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}

	if err := repo.Upsert(cfg); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save agent config: "+err.Error(), "")
		return
	}
	// Delete after writing: drop the tenant's Redis cache key so the next Resolve rebuilds it from the database.
	agentconfig.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"tenant": toJSON(cfg)}, "")
}
