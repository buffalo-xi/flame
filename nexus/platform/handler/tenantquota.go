package handler

import (
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/quota"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// GetTenantQuota GET /platform/tenants/:schema/quota
//
// 管理界面需要"原始值 + 系统顶 + 生效值"三样，所以这里一次全回：
//
//	quota     该租户的**原始行**（null = 未设置）—— 编辑框绑它
//	system    系统行的原始值（null = 未设置）—— 展示"系统顶"
//	fallback  代码兜底值 —— 系统行未设置时它就是顶
//	effective 生效值 = clampMax(pick(tenant, system, fallback), pick(system, fallback))
//	          （直接取 quota.Resolve(schema)，前端**不重算**截断规则，避免两套实现漂移）
//
// 为什么不复用 tenantView 里的 quota：那是 Resolve 之后的**生效值**，
// 拿它做编辑框会让"原始行被静默压平"（行里存 500、界面显示 100，一保存就变 100）。
func GetTenantQuota(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	respondTenantQuota(c, schema)
}

// GetMyTenantQuota GET /platform/me/tenants/:schema/quota
//
// 「用户」自助：只能读自己名下租户的配额（不是自己名下 → 403）。
// 返回体与管理员那条完全一致（原始行 + 系统顶 + 兜底 + 生效值），共用 respondTenantQuota。
func GetMyTenantQuota(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}
	respondTenantQuota(c, schema)
}

// respondTenantQuota 输出「原始行 + 系统行 + 兜底 + 生效值」四份数据（管理员与「用户」共用）。
func respondTenantQuota(c *gin.Context, schema string) {
	row, err := tenantsrepo.GetQuotaRow(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant quota: "+err.Error(), nil, "")
		return
	}
	sysRow, err := tenantsrepo.GetQuotaRow("")
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load system quota: "+err.Error(), nil, "")
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
