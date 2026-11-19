package handler

import (
	"fmt"

	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/quota"
	flowrepo "flame/nexus/shared/repository/flowconfigs"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 系统设置：platform.flow_configs / quota_configs 的**系统行**（tenant_id = ''）。
//
// 两套系统行的语义一致（都是"兜底 + 硬顶"，运行期 clampMax 取 min）：
//
//	flow_configs  兜底 + 硬顶
//	quota_configs 兜底 + 硬顶（系统行未设置时，用代码 fallback 当顶）
//
// 写系统行之后调用 InvalidateAll：删掉 Redis 里的系统行缓存键，各租户下次
// Resolve 会自动重新合并；写后漏删时由 TTL（5min）兜底。

// int32Field / intField 只用于"值不能小于 -1"的批量校验（-1 = 不限制）。
type int32Field struct {
	name  string
	value *int32
}

type intField struct {
	name  string
	value *int
}

// checkFlowSystemValues 值只能是 -1（不限制）或非负数；更小的负值没有语义，直接拒。
func checkFlowSystemValues(fields []int32Field) error {
	for _, f := range fields {
		if f.value != nil && *f.value < -1 {
			return fmt.Errorf("%s 只能是 -1（不限制）或非负数", f.name)
		}
	}
	return nil
}

// checkQuotaSystemValues 同上，配额是 *int。
func checkQuotaSystemValues(fields []intField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < -1 {
			return fmt.Errorf("%s 只能是 -1（不限制）或非负数", f.name)
		}
	}
	return nil
}

// GetSystemFlowConfigs GET /platform/system/flowconfigs
// 回系统行原始值 + 代码兜底值（界面用兜底值提示"留空会退到哪"）。
func GetSystemFlowConfigs(c *gin.Context) {
	cfg, err := flowRepo.Get("")
	if err != nil {
		xhttp.ErrJSON(c, "failed to load system config: "+err.Error(), nil, "")
		return
	}
	fb := flowconfig.Fallback()

	xhttp.OkJSON(c, "Success", gin.H{
		"config": flowToJSON(cfg),
		"fallback": gin.H{
			"max_timeout":         fb.MaxTimeout,
			"max_recursion_depth": fb.MaxRecursionDepth,
			"max_node_count":      fb.MaxNodeCount,
			"max_exec_count":      fb.MaxExecCount,
			"max_loop_times":      fb.MaxLoopTimes,
			"max_run_records":     fb.MaxRunRecords,
			"max_session_records": fb.MaxSessionRecords,
		},
	}, "")
}

// UpdateSystemFlowConfigs PUT /platform/system/flowconfigs
// 写 tenant_id = ” 行。这里**没有"不超过上级"的校验**（它就是最上级），只校验取值合法性。
func UpdateSystemFlowConfigs(c *gin.Context) {
	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if err := checkFlowSystemValues([]int32Field{
		{"max_timeout", req.MaxTimeout},
		{"max_recursion_depth", req.MaxRecursionDepth},
		{"max_node_count", req.MaxNodeCount},
		{"max_exec_count", req.MaxExecCount},
		{"max_loop_times", req.MaxLoopTimes},
		{"max_run_records", req.MaxRunRecords},
		{"max_session_records", req.MaxSessionRecords},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	cfg := &flowrepo.FlowConfig{
		TenantID:          "",
		MaxTimeout:        flowNullInt32(req.MaxTimeout),
		MaxRecursionDepth: flowNullInt32(req.MaxRecursionDepth),
		MaxNodeCount:      flowNullInt32(req.MaxNodeCount),
		MaxExecCount:      flowNullInt32(req.MaxExecCount),
		MaxLoopTimes:      flowNullInt32(req.MaxLoopTimes),
		MaxRunRecords:     flowNullInt32(req.MaxRunRecords),
		MaxSessionRecords: flowNullInt32(req.MaxSessionRecords),
	}
	if err := flowRepo.Upsert(cfg); err != nil {
		xhttp.ErrJSON(c, "failed to save system config: "+err.Error(), nil, "")
		return
	}
	flowconfig.InvalidateAll()

	xhttp.OkJSON(c, "Success", gin.H{"config": flowToJSON(cfg)}, "")
}

// GetSystemQuota GET /platform/system/quota
// quota 的 NULL 不能像 flowconfig 那样"原样回 null 就行"——它回的是原始行的三个指针。
func GetSystemQuota(c *gin.Context) {
	row, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load system quota: "+err.Error(), nil, "")
		return
	}
	fb := quota.Fallback()

	xhttp.OkJSON(c, "Success", gin.H{
		"quota": gin.H{
			"max_agents":                row.MaxAgents,
			"max_skills":                row.MaxSkills,
			"max_flows":                 row.MaxFlows,
			"max_session_storage_bytes": row.MaxSessionStorageBytes,
			"max_user_sessions":         row.MaxUserSessions,
		},
		"fallback": gin.H{
			"max_agents":                fb.MaxAgents,
			"max_skills":                fb.MaxSkills,
			"max_flows":                 fb.MaxFlows,
			"max_session_storage_bytes": fb.MaxSessionStorageBytes,
			"max_user_sessions":         fb.MaxUserSessions,
		},
	}, "")
}

// UpdateSystemQuota PUT /platform/system/quota
// 只写系统行；租户行不会被截断（quota 没有硬顶语义，这里只是"默认值"）。
func UpdateSystemQuota(c *gin.Context) {
	var req setTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if err := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes 只能是 -1（不限制）或非负数", "")
		return
	}

	if err := tenantsrepo.SetSystemQuota(req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrJSON(c, "Failed to set system quota: "+err.Error(), nil, "")
		return
	}
	quota.InvalidateAll()

	row, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrJSON(c, "Failed to reload system quota: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"quota": gin.H{
			"max_agents":                row.MaxAgents,
			"max_skills":                row.MaxSkills,
			"max_flows":                 row.MaxFlows,
			"max_session_storage_bytes": row.MaxSessionStorageBytes,
			"max_user_sessions":         row.MaxUserSessions,
		},
	}, "")
}
