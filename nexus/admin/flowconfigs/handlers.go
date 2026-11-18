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

// nullable 将 sql.NullInt32 转为 interface{}（Valid 时返回数值，否则返回 nil）
func nullable(v sql.NullInt32) interface{} {
	if v.Valid {
		return v.Int32
	}
	return nil
}

// toJSON 将 flowconfig 转为 JSON 友好 map（NULL 字段为 null）
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
	}
}

// Getflowconfigs 返回系统级配置 + 租户配置（系统级作为参考展示）
func Getflowconfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
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

	xhttp.OkJSON(c, "Success", gin.H{
		"system": toJSON(sysCfg),
		"tenant": toJSON(tenantCfg),
	}, "")
}

// updateRequest 前端提交的租户配置（null 表示未设置，由后端存 NULL）
type updateRequest struct {
	MaxTimeout        *int32 `json:"max_timeout"`
	MaxRecursionDepth *int32 `json:"max_recursion_depth"`
	MaxNodeCount      *int32 `json:"max_node_count"`
	MaxExecCount      *int32 `json:"max_exec_count"`
	MaxLoopTimes      *int32 `json:"max_loop_times"`
	MaxRunRecords     *int32 `json:"max_run_records"`
	MaxSessionRecords *int32 `json:"max_session_records"`
}

// toNullInt32 将 *int32 转为 sql.NullInt32（nil 表示未设置）
func toNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
}

// checkNotExceed 校验租户 max_* 不超过系统级（系统为 -1 或未设置时不限制）
func checkNotExceed(field string, tenantVal, sysVal sql.NullInt32) error {
	if !sysVal.Valid || sysVal.Int32 == -1 {
		return nil
	}
	if !tenantVal.Valid || tenantVal.Int32 == -1 {
		return nil
	}
	if tenantVal.Int32 > sysVal.Int32 {
		return fmt.Errorf("%s 不能超过系统级值 %d", field, sysVal.Int32)
	}
	return nil
}

// UpdateTenantflowconfigs 更新当前租户的 flow 配置（保存前校验不超过系统级）
func UpdateTenantflowconfigs(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	// 读取系统级配置用于校验
	sysCfg, err := repo.Get("")
	if err != nil {
		xhttp.ErrJSON(c, "failed to load system config: "+err.Error(), nil, "")
		return
	}
	var sysZero flowconfigs.FlowConfig
	if sysCfg != nil {
		sysZero = *sysCfg
	}

	// 组装租户配置
	cfg := &flowconfigs.FlowConfig{
		TenantID:          info.Schema,
		MaxTimeout:        toNullInt32(req.MaxTimeout),
		MaxRecursionDepth: toNullInt32(req.MaxRecursionDepth),
		MaxNodeCount:      toNullInt32(req.MaxNodeCount),
		MaxExecCount:      toNullInt32(req.MaxExecCount),
		MaxLoopTimes:      toNullInt32(req.MaxLoopTimes),
		MaxRunRecords:     toNullInt32(req.MaxRunRecords),
		MaxSessionRecords: toNullInt32(req.MaxSessionRecords),
	}

	// 校验 max_* 不超过系统级
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
	}
	for _, chk := range checks {
		if err := checkNotExceed(chk.field, chk.tenantVal, chk.sysVal); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}

	if err := repo.Upsert(cfg); err != nil {
		xhttp.ErrJSON(c, "failed to save flow config: "+err.Error(), nil, "")
		return
	}
	// 写后删：删掉该租户的 Redis 缓存键，下次 Resolve 回源 DB 重建。
	flowconfig.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"tenant": toJSON(cfg)}, "")
}
