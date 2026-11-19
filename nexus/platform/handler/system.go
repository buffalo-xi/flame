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

// System settings: the **system rows** of platform.flow_configs / quota_configs (tenant_id = '').
//
// Both system rows mean the same thing (both are "fallback + hard ceiling", with runtime clampMax taking the min):
//
//	flow_configs  fallback + hard ceiling
//	quota_configs fallback + hard ceiling (when the system row is unset, the code fallback is the ceiling)
//
// After writing a system row InvalidateAll is called: it drops the system-row cache key in Redis, and each tenant's next
// Resolve re-merges automatically; if the delete is ever missed, the TTL (5min) covers it.

// int32Field / intField are only used for the batch check "the value cannot be below -1" (-1 = unlimited).
type int32Field struct {
	name  string
	value *int32
}

type intField struct {
	name  string
	value *int
}

// checkFlowSystemValues: a value may only be -1 (unlimited) or non-negative; smaller negatives have no meaning and are
// rejected outright.
func checkFlowSystemValues(fields []int32Field) error {
	for _, f := range fields {
		if f.value != nil && *f.value < -1 {
			return fmt.Errorf("%s must be -1 (unlimited) or a non-negative number", f.name)
		}
	}
	return nil
}

// checkQuotaSystemValues is the same, with *int quotas. It returns the offending field name as well, so the caller can
// report quota.invalid_value with params.field; the message itself is only diagnostic text.
func checkQuotaSystemValues(fields []intField) (string, error) {
	for _, f := range fields {
		if f.value != nil && *f.value < -1 {
			return f.name, fmt.Errorf("%s must be -1 (unlimited) or a non-negative number", f.name)
		}
	}
	return "", nil
}

// GetSystemFlowConfigs GET /platform/system/flowconfigs
// Returns the system row's raw values plus the code fallback (the UI uses the fallback to show where an empty field falls back to).
func GetSystemFlowConfigs(c *gin.Context) {
	cfg, err := flowRepo.Get("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load system config: "+err.Error(), "")
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
// Writes the tenant_id = '' row. There is **no "must not exceed the parent" check** here (it is the top level); only the
// validity of the values is checked.
func UpdateSystemFlowConfigs(c *gin.Context) {
	var req updateFlowConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to save system config: "+err.Error(), "")
		return
	}
	flowconfig.InvalidateAll()

	xhttp.OkJSON(c, "Success", gin.H{"config": flowToJSON(cfg)}, "")
}

// GetSystemQuota GET /platform/system/quota
// A NULL quota cannot simply be echoed as null the way flowconfig can: it returns the original row's five pointers.
func GetSystemQuota(c *gin.Context) {
	row, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load system quota: "+err.Error(), "")
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
// It writes only the system row; tenant rows are not truncated (quota has no hard-ceiling semantics here - this is just
// "the default value").
func UpdateSystemQuota(c *gin.Context) {
	var req setTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	if field, qerr := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); qerr != nil {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": field, "min": -1}, qerr.Error(), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes must be -1 (unlimited) or a non-negative number", "")
		return
	}

	if err := tenantsrepo.SetSystemQuota(req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to set system quota: "+err.Error(), "")
		return
	}
	quota.InvalidateAll()

	row, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to reload system quota: "+err.Error(), "")
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
