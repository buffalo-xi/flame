package handler

import (
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/quota"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// GetTenantQuota GET /platform/tenants/:schema/quota
//
// The console needs three things - raw values, the system ceiling and the effective values - so all of it is returned at once:
//
//	quota     this tenant's **raw row** (null = unset) - the edit form binds to it
//	system    the system row's raw values (null = unset) - shown as the "system ceiling"
//	fallback  the code fallback - the ceiling when the system row is unset too
//	effective effective values = clampMax(pick(tenant, system, fallback), pick(system, fallback))
//	          (taken straight from quota.Resolve(schema); the frontend **never recomputes** the truncation rules, so two
//	          implementations cannot drift apart)
//
// Why not reuse the quota inside tenantView: that is the **effective value** after Resolve, and binding it to an edit form
// would silently flatten the raw row (the row stores 500, the form shows 100, and saving turns it into 100).
func GetTenantQuota(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	respondTenantQuota(c, schema)
}

// GetMyTenantQuota GET /platform/me/tenants/:schema/quota
//
// "User" self-service: can only read the quota of a tenant under their own name (otherwise 403).
// The response body is identical to the administrator route (raw row + system ceiling + fallback + effective values) and
// shares respondTenantQuota.
func GetMyTenantQuota(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	respondTenantQuota(c, schema)
}

// respondTenantQuota emits the four blocks "raw row + system row + fallback + effective values" (shared by the administrator
// and the "user" routes).
func respondTenantQuota(c *gin.Context, schema string) {
	row, err := tenantsrepo.GetQuotaRow(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant quota: "+err.Error(), "")
		return
	}
	sysRow, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load system quota: "+err.Error(), "")
		return
	}

	fb := quota.Fallback()
	eff := quota.Resolve(schema)

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
	}, "")
}
