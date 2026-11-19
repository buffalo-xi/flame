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
// 智能体运行参数（platform.agent_configs 的租户行）
//
// 与工作流运行参数（admin/flowconfigs）唯一的差别：**没有 -1（不限制）**。
// 这四个字段都是安全阀而不是配额 —— max_iterations = -1 等于取消防死循环，
// 所以校验口径是「只能是 >= 1 的整数」，而不是 flow 那边的「-1 或非负」。
// 上限校验（不得超过系统级）与 flow 完全一致。
// ============================================================================

var repo = agentconfigrepo.NewRepository()

// nullable 将 sql.NullInt32 转为 interface{}（Valid 时返回数值，否则返回 nil）
func nullable(v sql.NullInt32) interface{} {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// toJSON 将一行配置转为 JSON 友好 map（NULL 字段为 null）
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

// GetAgentConfigs 返回系统级配置 + 当前租户配置（系统级作为参考展示）
func GetAgentConfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrJSON(c, "failed to load system config: "+err.Error(), nil, "")
		return
	}
	tenantCfg, err := repo.Get(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "failed to load tenant config: "+err.Error(), nil, "")
		return
	}

	fb := agentconfig.Fallback()
	xhttp.OkJSON(c, "Success", gin.H{
		"system": toJSON(sysCfg),
		"tenant": toJSON(tenantCfg),
		// fallback 让界面能提示「留空会退到哪」，与平台侧系统设置页同一口径
		"fallback": gin.H{
			"max_iterations":   fb.MaxIterations,
			"max_call_depth":   fb.MaxCallDepth,
			"max_tools":        fb.MaxTools,
			"max_output_bytes": fb.MaxOutputBytes,
		},
	}, "")
}

// updateRequest 前端提交的租户配置（null 表示未设置，由后端存 NULL）
type updateRequest struct {
	MaxIterations  *int32 `json:"max_iterations"`
	MaxCallDepth   *int32 `json:"max_call_depth"`
	MaxTools       *int32 `json:"max_tools"`
	MaxOutputBytes *int32 `json:"max_output_bytes"`
}

// toNullInt32 将 *int32 转为 sql.NullInt32（nil 表示未设置）
func toNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// agentFields 本表的四个字段名（校验与提示共用一份，别在两个地方各写一遍）
type agentField struct {
	name  string
	value *int32
}

// checkPositive 拒绝 < 1 的值。这里**刻意不支持 -1**：
// 四个字段都是安全阀，-1 会让「防死循环 / 防递归 / 防上下文爆炸」直接失效。
func checkPositive(fields []agentField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < 1 {
			return fmt.Errorf("%s 必须是 >= 1 的整数（该配置不支持 -1 不限制）", f.name)
		}
	}
	return nil
}

// checkNotExceed 校验租户值不超过系统级（系统未设置时不限制）
func checkNotExceed(field string, tenantVal, sysVal sql.NullInt32) error {
	if !sysVal.Valid {
		return nil
	}
	if !tenantVal.Valid {
		return nil
	}
	if tenantVal.Int32 > sysVal.Int32 {
		return fmt.Errorf("%s 不能超过系统级值 %d", field, sysVal.Int32)
	}
	return nil
}

// UpdateTenantAgentConfigs 更新当前租户的运行参数（保存前校验取值范围与系统级上限）
func UpdateTenantAgentConfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if err := checkPositive([]agentField{
		{"max_iterations", req.MaxIterations},
		{"max_call_depth", req.MaxCallDepth},
		{"max_tools", req.MaxTools},
		{"max_output_bytes", req.MaxOutputBytes},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 读取系统级配置用于上限校验
	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrJSON(c, "failed to load system config: "+err.Error(), nil, "")
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
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}

	if err := repo.Upsert(cfg); err != nil {
		xhttp.ErrJSON(c, "failed to save agent config: "+err.Error(), nil, "")
		return
	}
	// 写后删：删掉该租户的 Redis 缓存键，下次 Resolve 回源 DB 重建。
	agentconfig.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"tenant": toJSON(cfg)}, "")
}
