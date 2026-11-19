// Package handler is the HTTP layer of the platform console (thin: bind / validate / respond).
//
// Boundary rules are in ../README.md: this package only depends on nexus/shared/* and tools/*.
package handler

import (
	"flame/nexus/platform/repository/pmusers"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/quota"

	"github.com/gin-gonic/gin"
)

// userView is the external view of a platform user (**without the password**).
//
// protected: whether this is the protected administrator (fixed plat) - the frontend greys out "edit / disable" from it and
// **never hardcodes the name**.
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

// tenantView is the external tenant view: registry fields + effective quota + current usage.
//
// A failed usage count (for example the tenant schema is not built yet, already deleted, or a table is missing) counts as 0 -
// the list page must not fail as a whole because of one pending tenant.
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

// countOrZero shows 0 when the count fails (a convenience wrapper around (int, error)).
func countOrZero(n int, err error) int {
	if err != nil {
		return 0
	}
	return n
}
