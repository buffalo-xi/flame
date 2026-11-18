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

// 工作流运行参数（platform.flow_configs）：平台侧按租户查看 / 调整。
//
// 两套入口**共用同一份核心逻辑**（loadFlowBundle / saveFlowConfigs），只有权限口径不同：
//   - 管理员：/platform/tenants/:schema/flowconfigs        —— 可操作**任意**租户
//   - 「用户」：/platform/me/tenants/:schema/flowconfigs    —— 只能操作**自己名下**的租户
//
// 校验口径与租户侧 /admin/flowconfigs 完全一致（同一张表、同一套"不超过系统级"规则）。
//
// 注意：租户运行时的读取走 shared/flowconfig 的 TTL 缓存，
// 所以写完必须主动失效，否则要等一个 TTL 周期（本进程立即生效；多实例下别的实例仍等 TTL）。
var flowRepo = flowrepo.NewRepository()

func flowNullable(v sql.NullInt32) any {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// flowToJSON 把一行配置转成前端要的 map（NULL → null）。
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

// mustTenant 取路径里的 schema 并确认租户已注册（管理员接口用）。
func mustTenant(c *gin.Context) (string, bool) {
	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return "", false
	}
	if t == nil {
		xhttp.ErrJSON(c, "租户不存在", nil, "")
		return "", false
	}
	return schema, true
}

// mustOwnTenant 取路径里的 schema，并确认它是**当前平台账号名下**的租户（「用户」自助接口用）。
//
// 管理员不走这组接口 —— 他走 /platform/tenants/...（能操作任意租户），
// 而且管理员永远不是 owner（开通 / 改负责人时都要求 owner 是 role=user）。
func mustOwnTenant(c *gin.Context) (string, bool) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return "", false
	}

	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return "", false
	}
	if t == nil {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "租户不存在")
		return "", false
	}
	if t.OwnerUserID != info.ID {
		xhttp.SendErrorResponse(c, http.StatusForbidden, "forbidden", "只能操作自己名下的租户")
		return "", false
	}
	return schema, true
}

// loadFlowBundle 读该租户的配置 + 系统级（系统级只作对照，不在写入范围里）。
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

// flowNullInt32 nil → NULL（未设置）
func flowNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// flowCheckNotExceed 租户值不得超过系统级（系统级未设置或 -1 时视为不限制）。
func flowCheckNotExceed(field string, tenantValue, sysValue sql.NullInt32) error {
	if !sysValue.Valid || sysValue.Int32 == -1 || !tenantValue.Valid || tenantValue.Int32 == -1 {
		return nil
	}
	if tenantValue.Int32 > sysValue.Int32 {
		return fmt.Errorf("%s 不能超过系统级值 %d", field, sysValue.Int32)
	}
	return nil
}

// saveFlowConfigs 校验（不得超过系统级）+ 写入 + 失效缓存。
// body 里 7 个字段都可为 null（null = 未设置，回退系统级）。
func saveFlowConfigs(schema string, req updateFlowConfigsRequest) (gin.H, error) {
	// 系统级配置只读，用来做上限校验
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
	// 写后删：删掉该租户的 Redis 缓存键，下次 Resolve 回源 DB 重建。
	flowconfig.Invalidate(schema)

	return gin.H{"schema": schema, "tenant": flowToJSON(cfg)}, nil
}

// GetTenantFlowConfigs GET /platform/tenants/:schema/flowconfigs
// 管理员视角：任意租户。返回该租户的配置 + 系统级（系统级只作对照）。
func GetTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	data, err := loadFlowBundle(schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateTenantFlowConfigs PUT /platform/tenants/:schema/flowconfigs
// 管理员视角：任意租户。
func UpdateTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}

	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	data, err := saveFlowConfigs(schema, req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetMyTenantFlowConfigs GET /platform/me/tenants/:schema/flowconfigs
// 「用户」自助：只能读自己名下租户的配置（不是自己名下 → 403）。
func GetMyTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	data, err := loadFlowBundle(schema)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateMyTenantFlowConfigs PUT /platform/me/tenants/:schema/flowconfigs
// 「用户」自助：只能改自己名下租户的配置（不是自己名下 → 403）。
// 校验与返回体同管理员那条 —— 共用 saveFlowConfigs。
func UpdateMyTenantFlowConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	data, err := saveFlowConfigs(schema, req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}
