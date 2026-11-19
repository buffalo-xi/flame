package users

import (
	"fmt"

	"flame/nexus/shared/quota"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Tenant-level self-service quota (the tenant row of {platform}.quota_configs).
//
// Same semantics as the platform's PUT /platform/me/tenants/:schema/quota, with two differences:
//   - no schema is passed here; the signed-in tenant (info.Schema) is used, so no cross-tenant
//     access is possible;
//   - no extra role check is needed: service users cannot sign in (auth/login.go only admits
//     RoleManage), so anyone reaching here with a tenant token is a management user.
//
// All five values are editable but none may exceed the system ceiling (an excess is rejected
// outright, never silently truncated): a self-service page must say so on the spot, otherwise
// "saved successfully but the effective value did not change" is a lie. A system ceiling of -1
// (unlimited) imposes no upper bound.
//
// After writing, quota.SetRow invalidates the cache internally, so it takes effect at runtime
// immediately (multiple instances receive it through the shared Redis cache).

// GetTenantQuota GET /admin/tenantquota
//
// It returns everything at once: this tenant's raw row + the system row + the code fallback + the
// effective values + three usage counters:
//   - quota     this tenant's raw row (null = unset, bound to the edit form)
//   - system    the system row's raw values (null = unset, shown as the "system ceiling")
//   - fallback  the code fallback (the ceiling when the system row is unset too)
//   - effective the effective values (computed by the backend; the frontend never redoes truncation)
//   - usage     current agent / skill / flow counts (the two session values have no cheap "current
//     usage" and are rendered as "-" by the frontend)
func GetTenantQuota(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	row, err := quota.GetRow(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load the tenant quota: "+err.Error(), "")
		return
	}
	sysRow, err := quota.GetRow("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load the system quota: "+err.Error(), "")
		return
	}

	fb := quota.Fallback()
	eff := quota.Resolve(info.Schema)

	// Usage counts fall back to 0 on failure: the cost of an unreadable counter must not be an
	// unopenable page
	agents, _ := quota.CountAgents(info.Schema)
	skills, _ := quota.CountSkills(info.Schema)
	flows, _ := quota.CountFlows(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{
		"quota": gin.H{
			"max_agents":                row.MaxAgents,
			"max_skills":                row.MaxSkills,
			"max_flows":                 row.MaxFlows,
			"max_session_storage_bytes": row.MaxSessionStorageBytes,
			"max_user_sessions":         row.MaxUserSessions,
		},
		"system": gin.H{
			"max_agents":                sysRow.MaxAgents,
			"max_skills":                sysRow.MaxSkills,
			"max_flows":                 sysRow.MaxFlows,
			"max_session_storage_bytes": sysRow.MaxSessionStorageBytes,
			"max_user_sessions":         sysRow.MaxUserSessions,
		},
		"fallback": gin.H{
			"max_agents":                fb.MaxAgents,
			"max_skills":                fb.MaxSkills,
			"max_flows":                 fb.MaxFlows,
			"max_session_storage_bytes": fb.MaxSessionStorageBytes,
			"max_user_sessions":         fb.MaxUserSessions,
		},
		"effective": gin.H{
			"max_agents":                eff.MaxAgents,
			"max_skills":                eff.MaxSkills,
			"max_flows":                 eff.MaxFlows,
			"max_session_storage_bytes": eff.MaxSessionStorageBytes,
			"max_user_sessions":         eff.MaxUserSessions,
		},
		"usage": gin.H{
			"agents": agents,
			"skills": skills,
			"flows":  flows,
		},
	}, "")
}

// setTenantQuotaRequest: absent / null clears the value (falls back to the system row); -1 means
// unlimited; a positive number is the ceiling.
type setTenantQuotaRequest struct {
	MaxAgents       *int `json:"max_agents"`
	MaxSkills       *int `json:"max_skills"`
	MaxFlows        *int `json:"max_flows"`
	MaxUserSessions *int `json:"max_user_sessions"`

	// MaxSessionStorageBytes is the per-session storage ceiling in bytes; -1 means unlimited.
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
}

// UpdateTenantQuota PUT /admin/tenantquota
func UpdateTenantQuota(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	var req setTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Value validity: -1 (unlimited) or a non-negative number (0 = creation forbidden)
	for _, f := range []struct {
		name string
		v    *int
	}{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	} {
		if f.v != nil && *f.v < -1 {
			xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": f.name, "min": -1},
				f.name+" must be -1 (unlimited) or a non-negative number", "")
			return
		}
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes must be -1 (unlimited) or a non-negative number", "")
		return
	}

	// The system ceiling comes from the system row (falling back to the code fallback when unset or
	// -1); -1 means unlimited, so no upper bound applies
	sys := quota.Resolve("")
	if req.MaxAgents != nil && sys.MaxAgents >= 0 && *req.MaxAgents > sys.MaxAgents {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_agents", "limit": sys.MaxAgents},
			fmt.Sprintf("the agent ceiling cannot exceed the system limit (%d)", sys.MaxAgents), "")
		return
	}
	if req.MaxSkills != nil && sys.MaxSkills >= 0 && *req.MaxSkills > sys.MaxSkills {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_skills", "limit": sys.MaxSkills},
			fmt.Sprintf("the skill ceiling cannot exceed the system limit (%d)", sys.MaxSkills), "")
		return
	}
	if req.MaxFlows != nil && sys.MaxFlows >= 0 && *req.MaxFlows > sys.MaxFlows {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_flows", "limit": sys.MaxFlows},
			fmt.Sprintf("the flow ceiling cannot exceed the system limit (%d)", sys.MaxFlows), "")
		return
	}
	if req.MaxUserSessions != nil && sys.MaxUserSessions >= 0 && *req.MaxUserSessions > sys.MaxUserSessions {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_user_sessions", "limit": sys.MaxUserSessions},
			fmt.Sprintf("the per-user session ceiling cannot exceed the system limit (%d)", sys.MaxUserSessions), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && sys.MaxSessionStorageBytes >= 0 &&
		*req.MaxSessionStorageBytes > sys.MaxSessionStorageBytes {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_session_storage_bytes", "limit": sys.MaxSessionStorageBytes},
			fmt.Sprintf("the per-session storage ceiling cannot exceed the system limit (%d bytes)", sys.MaxSessionStorageBytes), "")
		return
	}

	if err := quota.SetRow(info.Schema, &quota.Row{
		MaxAgents:              req.MaxAgents,
		MaxSkills:              req.MaxSkills,
		MaxFlows:               req.MaxFlows,
		MaxUserSessions:        req.MaxUserSessions,
		MaxSessionStorageBytes: req.MaxSessionStorageBytes,
	}); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the quota: "+err.Error(), "")
		return
	}

	eff := quota.Resolve(info.Schema)
	xhttp.OkJSON(c, "Success", gin.H{
		"quota": gin.H{
			"max_agents":                eff.MaxAgents,
			"max_skills":                eff.MaxSkills,
			"max_flows":                 eff.MaxFlows,
			"max_session_storage_bytes": eff.MaxSessionStorageBytes,
			"max_user_sessions":         eff.MaxUserSessions,
		},
	}, "")
}
