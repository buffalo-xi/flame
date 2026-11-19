package users

import (
	"fmt"

	"flame/nexus/shared/quota"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 租户级配额自助（{platform}.quota_configs 的租户行）。
//
// 与平台「我的租户」那条 PUT /platform/me/tenants/:schema/quota 同语义，区别只有两点：
//   - 这里不传 schema，直接用当前登录租户（info.Schema），无法越租户；
//   - 权限上不需要额外判断角色：服务用户不可登录（auth/login.go 只放行 RoleManage），
//     能带着租户 token 走到这里的必然是管理用户。
//
// 五项都能改，但都不能超过系统顶（超了直接拒，不静默截断）：自助页面必须当场说清，
// 否则「保存成功但生效值没变」等于骗人。系统顶为 -1（不限）时不做上限约束。
//
// 写完由 quota.SetRow 内部失效缓存，运行期立即生效（多实例靠共享 Redis 缓存分发）。

// GetTenantQuota GET /admin/tenantquota
//
// 一次回全「本租户原始行 + 系统行 + 代码兜底 + 生效值 + 三项用量」：
//   - quota     本租户原始行（null = 未设置，绑编辑框）
//   - system    系统行原始值（null = 未设置，展示"系统顶"）
//   - fallback  代码兜底（系统行也未设置时它就是顶）
//   - effective 生效值（后端算，前端不重算截断规则）
//   - usage     智能体 / 技能 / 工作流当前数量（会话两项没有便宜的"当前用量"，由前端显示 —）
func GetTenantQuota(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	row, err := quota.GetRow(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant quota: "+err.Error(), nil, "")
		return
	}
	sysRow, err := quota.GetRow("")
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load system quota: "+err.Error(), nil, "")
		return
	}

	fb := quota.Fallback()
	eff := quota.Resolve(info.Schema)

	// 用量统计失败按 0 展示：配额页读不出来的代价不应该是一整个页面打不开
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

// setTenantQuotaRequest 缺省 / null = 清除（回退系统行）；-1 = 不限；正数 = 上限。
type setTenantQuotaRequest struct {
	MaxAgents       *int `json:"max_agents"`
	MaxSkills       *int `json:"max_skills"`
	MaxFlows        *int `json:"max_flows"`
	MaxUserSessions *int `json:"max_user_sessions"`

	// MaxSessionStorageBytes 单会话空间上限（字节）；-1 = 不限制。
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

	// 取值合法性：-1（不限）或非负数（0 = 禁止新建）
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
				f.name+" 只能是 -1（不限制）或非负数", "")
			return
		}
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes 只能是 -1（不限制）或非负数", "")
		return
	}

	// 系统顶 = 系统行（未设置 / -1 时退到代码兜底）；-1 表示不限，不做上限约束
	sys := quota.Resolve("")
	if req.MaxAgents != nil && sys.MaxAgents >= 0 && *req.MaxAgents > sys.MaxAgents {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_agents", "limit": sys.MaxAgents},
			fmt.Sprintf("智能体数量上限不能超过系统限额（%d）", sys.MaxAgents), "")
		return
	}
	if req.MaxSkills != nil && sys.MaxSkills >= 0 && *req.MaxSkills > sys.MaxSkills {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_skills", "limit": sys.MaxSkills},
			fmt.Sprintf("技能数量上限不能超过系统限额（%d）", sys.MaxSkills), "")
		return
	}
	if req.MaxFlows != nil && sys.MaxFlows >= 0 && *req.MaxFlows > sys.MaxFlows {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_flows", "limit": sys.MaxFlows},
			fmt.Sprintf("工作流数量上限不能超过系统限额（%d）", sys.MaxFlows), "")
		return
	}
	if req.MaxUserSessions != nil && sys.MaxUserSessions >= 0 && *req.MaxUserSessions > sys.MaxUserSessions {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_user_sessions", "limit": sys.MaxUserSessions},
			fmt.Sprintf("单用户会话数上限不能超过系统限额（%d）", sys.MaxUserSessions), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && sys.MaxSessionStorageBytes >= 0 &&
		*req.MaxSessionStorageBytes > sys.MaxSessionStorageBytes {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_session_storage_bytes", "limit": sys.MaxSessionStorageBytes},
			fmt.Sprintf("单会话空间上限不能超过系统限额（%d 字节）", sys.MaxSessionStorageBytes), "")
		return
	}

	if err := quota.SetRow(info.Schema, &quota.Row{
		MaxAgents:              req.MaxAgents,
		MaxSkills:              req.MaxSkills,
		MaxFlows:               req.MaxFlows,
		MaxUserSessions:        req.MaxUserSessions,
		MaxSessionStorageBytes: req.MaxSessionStorageBytes,
	}); err != nil {
		xhttp.ErrJSON(c, "Failed to set quota: "+err.Error(), nil, "")
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
