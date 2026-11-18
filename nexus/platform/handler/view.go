// Package handler 平台管理台的 HTTP 层（薄：绑定 / 校验 / 回包）。
//
// 边界规则见 ../README.md：本包只依赖 nexus/shared/* 与 tools/*。
package handler

import (
	"flame/nexus/platform/repository/pmusers"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/quota"

	"github.com/gin-gonic/gin"
)

// userView 平台用户的对外视图（**不含密码**）。
//
// protected：是否是受保护管理员（固定 plat）—— 前端据此把「编辑 / 停用」置灰，**不写死名字**。
func userView(u *pmusers.User) gin.H {
	return gin.H{
		"id":           u.ID,
		"name":         u.Name,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"status":       u.Status,
		"protected":    isProtectedPlatformUser(u.Name),
		"created_at":   u.CreatedAt,
		"updated_at":   u.UpdatedAt,
	}
}

// tenantView 租户对外视图：注册表字段 + 生效配额 + 当前用量。
//
// 用量统计失败（例如租户 schema 尚未建成、已删除，或某张表不存在）按 0 计 ——
// 列表页不该因为一个 pending 租户而整体报错。
func tenantView(t *tenantsrepo.Tenant) gin.H {
	limits := quota.Resolve(t.Schema)

	return gin.H{
		"schema":        t.Schema,
		"display_name":  t.DisplayName,
		"status":        t.Status,
		"plan":          t.Plan,
		"owner_user_id": t.OwnerUserID,
		"remark":        t.Remark,
		"created_at":    t.CreatedAt,
		"updated_at":    t.UpdatedAt,
		"quota": gin.H{
			"max_agents": limits.MaxAgents,
			"max_skills": limits.MaxSkills,
			"max_flows":  limits.MaxFlows,
		},
		"usage": gin.H{
			"agents": countOrZero(quota.CountAgents(t.Schema)),
			"skills": countOrZero(quota.CountSkills(t.Schema)),
			"flows":  countOrZero(quota.CountFlows(t.Schema)),
		},
	}
}

// countOrZero 统计出错时按 0 展示（(int, error) 的便捷包装）。
func countOrZero(n int, err error) int {
	if err != nil {
		return 0
	}
	return n
}
