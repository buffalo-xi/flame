package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"flame/nexus/platform/auth"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/flowconfig"
	flowrepo "flame/nexus/shared/repository/flowconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Flow run limits (platform.flow_configs): the platform side views and adjusts them per tenant.
//
// The two entry sets **share the same core logic** (loadFlowBundle / saveFlowConfigs) and differ only in permissions:
//   - administrator: /platform/tenants/:schema/flowconfigs     - any tenant
//   - "user":        /platform/me/tenants/:schema/flowconfigs  - only the tenants under their own name
//
// The validation rules match the tenant-side /admin/flowconfigs exactly (same table, same "must not exceed the system
// level" rule).
//
// Note: tenant runtime reads go through shared/flowconfig's TTL cache, so a write must invalidate it explicitly or the
// new value waits a whole TTL (immediate in this process; other instances still wait for their TTL).
var flowRepo = flowrepo.NewRepository()

func flowNullable(v sql.NullInt32) any {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// flowToJSON turns a config row into the map the frontend expects (NULL -> null).
func flowToJSON(cfg *flowrepo.FlowConfig) gin.H {
	if cfg == nil {
		return gin.H{}
	}
	return gin.H{
		"max_timeout":         flowNullable(cfg.MaxTimeout),
		"max_recursion_depth": flowNullable(cfg.MaxRecursionDepth),
		"max_node_count":      flowNullable(cfg.MaxNodeCount),
		"max_exec_count":      flowNullable(cfg.MaxExecCount),
		"max_loop_times":      flowNullable(cfg.MaxLoopTimes),
		"max_run_records":     flowNullable(cfg.MaxRunRecords),
		"max_session_records": flowNullable(cfg.MaxSessionRecords),
	}
}

// mustTenant reads the schema from the path and confirms the tenant is registered (administrator endpoints).
func mustTenant(c *gin.Context) (string, bool) {
	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return "", false
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found", "")
		return "", false
	}
	return schema, true
}

// mustOwnTenant reads the schema from the path and confirms it belongs to **the current platform account** (the
// "user" self-service endpoints).
//
// Administrators do not use this group - they use /platform/tenants/... (any tenant),
// and an administrator is never an owner (provisioning and ownership transfer both require owner to have role=user).
func mustOwnTenant(c *gin.Context) (string, bool) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return "", false
	}

	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return "", false
	}
	if t == nil {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "tenant not found")
		return "", false
	}
	if t.OwnerUserID != info.ID {
		xhttp.SendErrorResponse(c, http.StatusForbidden, "forbidden", "you can only operate on tenants under your own name")
		return "", false
	}
	return schema, true
}

// loadFlowBundle reads that tenant's config plus the system level (the system level is for comparison only, never written here).
func loadFlowBundle(schema string) (gin.H, error) {
	sysCfg, err := flowRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	tenantCfg, err := flowRepo.Get(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to load tenant config: %w", err)
	}
	return gin.H{
		"system": flowToJSON(sysCfg),
		"tenant": flowToJSON(tenantCfg),
	}, nil
}

type updateFlowConfigsRequest struct {
	MaxTimeout        *int32 `json:"max_timeout"`
	MaxRecursionDepth *int32 `json:"max_recursion_depth"`
	MaxNodeCount      *int32 `json:"max_node_count"`
	MaxExecCount      *int32 `json:"max_exec_count"`
	MaxLoopTimes      *int32 `json:"max_loop_times"`
	MaxRunRecords     *int32 `json:"max_run_records"`
	MaxSessionRecords *int32 `json:"max_session_records"`
}

// flowNullInt32 maps nil -> NULL (unset).
func flowNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// flowCheckNotExceed: a tenant value must not exceed the system level (an unset or -1 system level means unlimited).
func flowCheckNotExceed(field string, tenantValue, sysValue sql.NullInt32) error {
	if !sysValue.Valid || sysValue.Int32 == -1 || !tenantValue.Valid || tenantValue.Int32 == -1 {
		return nil
	}
	if tenantValue.Int32 > sysValue.Int32 {
		return fmt.Errorf("%s cannot exceed the system value %d", field, sysValue.Int32)
	}
	return nil
}

// saveFlowConfigs validates (must not exceed the system level), writes and invalidates the cache.
// All seven body fields may be null (null = unset, falls back to the system level).
func saveFlowConfigs(schema string, req updateFlowConfigsRequest) (gin.H, error) {
	// The system config is read-only here and only used for the upper-bound check
	sysCfg, err := flowRepo.Get("")
	if err != nil {
		return nil, fmt.Errorf("failed to load system config: %w", err)
	}
	var sys flowrepo.FlowConfig
	if sysCfg != nil {
		sys = *sysCfg
	}

	cfg := &flowrepo.FlowConfig{
		TenantID:          schema,
		MaxTimeout:        flowNullInt32(req.MaxTimeout),
		MaxRecursionDepth: flowNullInt32(req.MaxRecursionDepth),
		MaxNodeCount:      flowNullInt32(req.MaxNodeCount),
		MaxExecCount:      flowNullInt32(req.MaxExecCount),
		MaxLoopTimes:      flowNullInt32(req.MaxLoopTimes),
		MaxRunRecords:     flowNullInt32(req.MaxRunRecords),
		MaxSessionRecords: flowNullInt32(req.MaxSessionRecords),
	}

	checks := []struct {
		field       string
		tenantValue sql.NullInt32
		sysValue    sql.NullInt32
	}{
		{"max_timeout", cfg.MaxTimeout, sys.MaxTimeout},
		{"max_recursion_depth", cfg.MaxRecursionDepth, sys.MaxRecursionDepth},
		{"max_node_count", cfg.MaxNodeCount, sys.MaxNodeCount},
		{"max_exec_count", cfg.MaxExecCount, sys.MaxExecCount},
		{"max_loop_times", cfg.MaxLoopTimes, sys.MaxLoopTimes},
		{"max_run_records", cfg.MaxRunRecords, sys.MaxRunRecords},
		{"max_session_records", cfg.MaxSessionRecords, sys.MaxSessionRecords},
	}
	for _, chk := range checks {
		if err := flowCheckNotExceed(chk.field, chk.tenantValue, chk.sysValue); err != nil {
			return nil, err
		}
	}

	if err := flowRepo.Upsert(cfg); err != nil {
		return nil, fmt.Errorf("failed to save flow config: %w", err)
	}
	// Delete after write: drop that tenant's Redis key so the next Resolve goes back to the database and rebuilds it
	flowconfig.Invalidate(schema)

	return gin.H{"schema": schema, "tenant": flowToJSON(cfg)}, nil
}

// GetTenantFlowConfigs GET /platform/tenants/:schema/flowconfigs
// Administrator view: any tenant. Returns that tenant's config plus the system level (for comparison only).
func GetTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	data, err := loadFlowBundle(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateTenantFlowConfigs PUT /platform/tenants/:schema/flowconfigs
// Administrator view: any tenant.
func UpdateTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}

	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	data, err := saveFlowConfigs(schema, req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetMyTenantFlowConfigs GET /platform/me/tenants/:schema/flowconfigs
// "User" self-service: only the config of a tenant under their own name (otherwise 403).
func GetMyTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	data, err := loadFlowBundle(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateMyTenantFlowConfigs PUT /platform/me/tenants/:schema/flowconfigs
// "User" self-service: only the config of a tenant under their own name (otherwise 403).
// The validation and response body match the administrator route - both share saveFlowConfigs.
func UpdateMyTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	data, err := saveFlowConfigs(schema, req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}
