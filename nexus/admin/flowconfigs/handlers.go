package flowconfigs

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowconfig"
	flowconfigs "flame/nexus/shared/repository/flowconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

var repo = flowconfigs.NewRepository()

// nullable turns a sql.NullInt32 into an interface{} (the number when Valid, nil otherwise).
func nullable(v sql.NullInt32) interface{} {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// toJSON renders a flowconfig as a JSON-friendly map (NULL columns become null).
func toJSON(cfg *flowconfigs.FlowConfig) gin.H {
	if cfg == nil {
		return gin.H{}
	}
	return gin.H{
		"max_timeout":         nullable(cfg.MaxTimeout),
		"max_recursion_depth": nullable(cfg.MaxRecursionDepth),
		"max_node_count":      nullable(cfg.MaxNodeCount),
		"max_exec_count":      nullable(cfg.MaxExecCount),
		"max_loop_times":      nullable(cfg.MaxLoopTimes),
		"max_run_records":     nullable(cfg.MaxRunRecords),
		"max_session_records": nullable(cfg.MaxSessionRecords),
		"max_concurrency":     nullable(cfg.MaxConcurrency),
	}
}

// Returns the system-wide config plus the tenant's (the system one is shown for reference).
func Getflowconfigs(c *gin.Context) {
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

	xhttp.OkJSON(c, "Success", gin.H{
		"system": toJSON(sysCfg),
		"tenant": toJSON(tenantCfg),
	}, "")
}

// updateRequest is the tenant config submitted by the frontend (null means unset, stored as NULL).
type updateRequest struct {
	MaxTimeout        *int32 `json:"max_timeout"`
	MaxRecursionDepth *int32 `json:"max_recursion_depth"`
	MaxNodeCount      *int32 `json:"max_node_count"`
	MaxExecCount      *int32 `json:"max_exec_count"`
	MaxLoopTimes      *int32 `json:"max_loop_times"`
	MaxRunRecords     *int32 `json:"max_run_records"`
	MaxSessionRecords *int32 `json:"max_session_records"`
	MaxConcurrency    *int32 `json:"max_concurrency"`
}

// toNullInt32 turns *int32 into sql.NullInt32 (nil means unset).
func toNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// checkNotExceed keeps the tenant max_* within the system value (no limit when it is -1 or unset).
func checkNotExceed(field string, tenantVal, sysVal sql.NullInt32) error {
	if !sysVal.Valid || sysVal.Int32 == -1 {
		return nil
	}
	if !tenantVal.Valid || tenantVal.Int32 == -1 {
		return nil
	}
	if tenantVal.Int32 > sysVal.Int32 {
		return fmt.Errorf("%s must not exceed the system value %d", field, sysVal.Int32)
	}
	return nil
}

// Updates the flow config of the current tenant (checked against the system value before saving).
func UpdateTenantflowconfigs(c *gin.Context) {
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

	// Read the system config for the checks.
	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
		return
	}
	var sysZero flowconfigs.FlowConfig
	if sysCfg != nil {
		sysZero = *sysCfg
	}

	// Assemble the tenant config.
	cfg := &flowconfigs.FlowConfig{
		TenantID:          info.Schema,
		MaxTimeout:        toNullInt32(req.MaxTimeout),
		MaxRecursionDepth: toNullInt32(req.MaxRecursionDepth),
		MaxNodeCount:      toNullInt32(req.MaxNodeCount),
		MaxExecCount:      toNullInt32(req.MaxExecCount),
		MaxLoopTimes:      toNullInt32(req.MaxLoopTimes),
		MaxRunRecords:     toNullInt32(req.MaxRunRecords),
		MaxSessionRecords: toNullInt32(req.MaxSessionRecords),
		MaxConcurrency:    toNullInt32(req.MaxConcurrency),
	}

	// Check max_* against the system values.
	checks := []struct {
		field     string
		tenantVal sql.NullInt32
		sysVal    sql.NullInt32
	}{
		{"max_timeout", cfg.MaxTimeout, sysZero.MaxTimeout},
		{"max_recursion_depth", cfg.MaxRecursionDepth, sysZero.MaxRecursionDepth},
		{"max_node_count", cfg.MaxNodeCount, sysZero.MaxNodeCount},
		{"max_exec_count", cfg.MaxExecCount, sysZero.MaxExecCount},
		{"max_loop_times", cfg.MaxLoopTimes, sysZero.MaxLoopTimes},
		{"max_run_records", cfg.MaxRunRecords, sysZero.MaxRunRecords},
		{"max_session_records", cfg.MaxSessionRecords, sysZero.MaxSessionRecords},
		{"max_concurrency", cfg.MaxConcurrency, sysZero.MaxConcurrency},
	}
	for _, chk := range checks {
		if err := checkNotExceed(chk.field, chk.tenantVal, chk.sysVal); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}

	if err := repo.Upsert(cfg); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save flow config: "+err.Error(), "")
		return
	}
	// Delete after writing: drop the tenant's Redis cache key so the next Resolve rebuilds it from the database.
	flowconfig.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"tenant": toJSON(cfg)}, "")
}
