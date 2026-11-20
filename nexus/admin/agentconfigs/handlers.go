package agentconfigs

import (
	"database/sql"
	"errors"

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
//
// The rules and the JSON shape live in shared/agentconfig (ValidateTenant / Row.JSON / Effective), which is the same
// package the platform side (platform/handler/agentconfigs.go) uses: two HTTP layers, one set of rules.
//
// The body is a full replacement: absent or null means "not set" (NULL), not "leave it alone".
// ============================================================================

var repo = agentconfigrepo.NewRepository()

// rowFromStore adapts a stored row; nil stays nil ("no such row").
func rowFromStore(cfg *agentconfigrepo.AgentConfig) *agentconfig.Row {
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

// storeFromRow is the reverse, for the upsert.
func storeFromRow(tenantID string, r *agentconfig.Row) *agentconfigrepo.AgentConfig {
	return &agentconfigrepo.AgentConfig{
		TenantID:       tenantID,
		MaxIterations:  r.MaxIterations,
		MaxCallDepth:   r.MaxCallDepth,
		MaxTools:       r.MaxTools,
		MaxOutputBytes: r.MaxOutputBytes,
	}
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

// rowFromRequest turns the submitted body into a row (nil means unset).
func rowFromRequest(req updateRequest) *agentconfig.Row {
	return &agentconfig.Row{
		MaxIterations:  toNullInt32(req.MaxIterations),
		MaxCallDepth:   toNullInt32(req.MaxCallDepth),
		MaxTools:       toNullInt32(req.MaxTools),
		MaxOutputBytes: toNullInt32(req.MaxOutputBytes),
	}
}

// GetAgentConfigs returns the system-wide config plus the current tenant's (the system one is shown for reference), and
// the merged "effective" values that a run will really use.
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
	sys, tenant := rowFromStore(sysCfg), rowFromStore(tenantCfg)

	// fallback lets the UI say where an empty field falls back to, matching the platform system settings page;
	// effective is what the runtime applies, which differs from the tenant value when the ceiling is lower.
	xhttp.OkJSON(c, "Success", gin.H{
		"system":    sys.JSON(),
		"tenant":    tenant.JSON(),
		"effective": agentconfig.Effective(tenant, sys).JSON(),
		"fallback":  agentconfig.Fallback().JSON(),
	}, "")
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

	// Read the system config for the upper-bound checks.
	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	sys, cfg := rowFromStore(sysCfg), rowFromRequest(req)

	// A rejected value carries its own code + params (the bundled copy is what the user reads).
	if verr := agentconfig.ValidateTenant(cfg, sys); verr != nil {
		var fe *agentconfig.FieldError
		if errors.As(verr, &fe) {
			xhttp.ErrCode(c, fe.Code, fe.Params, fe.Error(), "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, verr.Error(), "")
		return
	}

	if err := repo.Upsert(storeFromRow(info.Schema, cfg)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save agent config: "+err.Error(), "")
		return
	}
	// Delete after writing: drop the tenant's Redis cache key so the next Resolve rebuilds it from the database.
	agentconfig.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"tenant": cfg.JSON()}, "")
}
