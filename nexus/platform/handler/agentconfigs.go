package handler

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/agentconfig"
	agentrepo "flame/nexus/shared/repository/agentconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 智能体运行参数（platform.agent_configs）：平台侧按租户查看 / 调整 + 系统行。
//
// 入口分组与工作流运行参数完全对称（共用 mustTenant / mustOwnTenant）：
//   - 管理员：/platform/tenants/:schema/agentconfigs        —— 任意租户
//   - 「用户」：/platform/me/tenants/:schema/agentconfigs    —— 只能操作自己名下的租户
//   - 系统行：/platform/system/agentconfigs                 —— 全局默认 + 硬顶
//
// 与 flow_configs 的唯一差别：**没有 -1（不限制）**。四个字段都是安全阀，
// -1 等于把防线拆掉，所以取值校验是 >= 1（见 sql/agent_configs.sql）。
//
// 写完必须 Invalidate / InvalidateAll，否则运行期要等一个 TTL 周期才看得到新值。
var agentRepo = agentrepo.NewRepository()

func agentNullable(v sql.NullInt32) any {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// agentToJSON 把一行配置转成前端要的 map（NULL → null）。
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

// agentFallbackJSON 代码兜底值（界面用来提示「留空会退到哪」）。
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

// agentNullInt32 nil → NULL（未设置）
func agentNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// agentField 字段名 + 提交值，校验与提示共用一份
type agentField struct {
	name  string
	value *int32
}

// agentCheckPositive 拒绝 < 1：本表刻意不支持 -1（安全阀，不限制等于失效）。
func agentCheckPositive(fields []agentField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < 1 {
			return fmt.Errorf("%s 必须是 >= 1 的整数（该配置不支持 -1 不限制）", f.name)
		}
	}
	return nil
}

// agentCheckNotExceed 租户值不得超过系统级（系统级未设置时视为不限制）。
func agentCheckNotExceed(field string, tenantValue, sysValue sql.NullInt32) error {
	if !sysValue.Valid || !tenantValue.Valid {
		return nil
	}
	if tenantValue.Int32 > sysValue.Int32 {
		return fmt.Errorf("%s 不能超过系统级值 %d", field, sysValue.Int32)
	}
	return nil
}

// loadAgentBundle 读该租户的配置 + 系统级（系统级只作对照，不在写入范围里）。
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

// saveAgentConfigs 校验（取值范围 + 不得超过系统级）+ 写入 + 失效缓存。
func saveAgentConfigs(schema string, req updateAgentConfigsRequest) (gin.H, error) {
	if err := agentCheckPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		return nil, err
	}

	// 系统级配置只读，用来做上限校验
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
		xhttp.ErrJSON(c, err.Error(), nil, "")
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
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	data, err := saveAgentConfigs(schema, req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
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
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// UpdateMyTenantAgentConfigs PUT /platform/me/tenants/:schema/agentconfigs
// 「用户」自助：只能改自己名下租户的配置（不是自己名下 → 403）。校验与返回体同管理员那条。
func UpdateMyTenantAgentConfigs(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	var req updateAgentConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	data, err := saveAgentConfigs(schema, req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", data, "")
}

// GetSystemAgentConfigs GET /platform/system/agentconfigs
// 回系统行原始值 + 代码兜底值。
func GetSystemAgentConfigs(c *gin.Context) {
	cfg, err := agentRepo.Get("")
	if err != nil {
		xhttp.ErrJSON(c, "failed to load system config: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"config":   agentToJSON(cfg),
		"fallback": agentFallbackJSON(),
	}, "")
}

// UpdateSystemAgentConfigs PUT /platform/system/agentconfigs
// 写 tenant_id = ” 行。它就是最上级，所以只校验取值合法性，没有"不超过上级"。
func UpdateSystemAgentConfigs(c *gin.Context) {
	var req updateAgentConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if err := agentCheckPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
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
		xhttp.ErrJSON(c, "failed to save system config: "+err.Error(), nil, "")
		return
	}
	// 系统行一变，所有租户的合并结果都会变：只删系统行这一个键即可
	agentconfig.InvalidateAll()

	xhttp.OkJSON(c, "Success", gin.H{
		"config":   agentToJSON(cfg),
		"fallback": agentFallbackJSON(),
	}, "")
}
