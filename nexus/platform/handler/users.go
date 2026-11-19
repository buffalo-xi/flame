package handler

import (
	"fmt"
	"regexp"
	"strings"

	"flame/nexus/platform/auth"
	"flame/nexus/platform/repository/pmusers"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/account"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// namePattern is the platform login-name character set (same as tenant users; the unique index in the
// database is the second line of defence).
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// isProtectedPlatformUser reports whether this is the **protected administrator** account (login
// name fixed to auth.BootstrapAdminName = plat).
//
// It is the same kind of thing as shared/tenants.PrimaryUserName on the tenant side: both are "the
// account the system preinstalls" - the first active platform administrator comes from environment
// seed data (nexus creates no account on startup).
// Rule: **not modifiable** (profile / role / status) and **not deletable** (this module has no
// DELETE /users/:id).
func isProtectedPlatformUser(name string) bool {
	return strings.TrimSpace(name) == auth.BootstrapAdminName
}

// ListUsers GET /platform/users?role=&status=&q=&page=&page_size=
//
// Admin view: a paged list of the platform user table (admins plus users).
// A "user" row carries tenant_count (the number of tenants under their name), which helps decide
// whether the account can be disabled.
//
// Paging follows /platform/tenants: page starts at 1; an omitted / <=0 page_size means no paging
// (return everything), capped at 100
// - every "user" row runs one CountByOwner, so a larger page_size means more database load.
// total is the number of rows matching the filter (not the size of this page).
func ListUsers(c *gin.Context) {
	role, status, q := c.Query("role"), c.Query("status"), c.Query("q")
	page, pageSize := parsePaging(c)

	users, err := pmusers.List(role, status, q, pageSize, (page-1)*pageSize)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to list platform users: "+err.Error(), "")
		return
	}

	total := len(users)
	if pageSize > 0 {
		n, cerr := pmusers.Count(role, status, q)
		if cerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to count platform users: "+cerr.Error(), "")
			return
		}
		total = n
	}

	items := make([]gin.H, 0, len(users))
	for i := range users {
		v := userView(&users[i])
		if users[i].Role == auth.RoleUser {
			if n, cerr := tenantsrepo.CountByOwner(users[i].ID); cerr == nil {
				v["tenant_count"] = n
			}
		}
		items = append(items, v)
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}, "")
}

// UserTenants GET /platform/users/:id/tenants
//
// The tenants under one platform user (the "tenants under this name" block of the admin detail card).
// Same data source as /platform/me/tenants (tenantsrepo.ListByOwner, which already excludes
// status=deleted); the only difference is that an administrator names the id here.
func UserTenants(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	u, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if u == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "user not found", "")
		return
	}

	list, err := tenantsrepo.ListByOwner(id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to list tenants: "+err.Error(), "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, tenantView(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

type createPlatformUserRequest struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name"` // optional (the UI falls back to the account name when empty)
	Password    string `json:"password" binding:"required"`
	Role        string `json:"role" binding:"required"` // admin | user
}

// CreateUser POST /platform/users
func CreateUser(c *gin.Context) {
	var req createPlatformUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	displayName := strings.TrimSpace(req.DisplayName)
	if !namePattern.MatchString(name) {
		xhttp.ErrCode(c, "user.invalid_name", gin.H{"name": name}, "the login name accepts letters, digits, underscores and hyphens only, 1-64 characters", "")
		return
	}
	if role != auth.RoleAdmin && role != auth.RoleUser {
		xhttp.ErrCode(c, "user.invalid_role", gin.H{"role": role}, "role must be admin or user", "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "the password must be at least 8 characters", "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrCode(c, "user.password_too_long", nil, account.ErrPasswordTooLong.Error(), "")
		return
	}

	exist, err := pmusers.GetByName(name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to check name: "+err.Error(), "")
		return
	}
	if exist != nil {
		xhttp.ErrCode(c, "user.name_taken", gin.H{"name": name}, "the login name already exists: "+name, "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}
	u := &pmusers.User{
		ID:          uuid.NewString(),
		Name:        name,
		DisplayName: displayName,
		Password:    hash,
		Role:        role,
		Status:      account.StatusActive,
	}
	if err := pmusers.Create(u); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to create platform user: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", userView(u), "")
}

type updatePlatformUserRequest struct {
	DisplayName string `json:"display_name"`
	Role        string `json:"role" binding:"required"`   // admin | user
	Status      string `json:"status" binding:"required"` // active | disabled | locked
}

// UpdateUser PUT /platform/users/:id
//
// Guards (the same rule as tenant-side user management):
//  1. the signed-in account cannot disable or demote itself (that would kick itself out on the spot)
//  2. the last active administrator cannot be disabled or demoted (that would lock the platform out,
//     recoverable only by editing the database)
//
// Disabling a "user" account that owns tenants is allowed, but returns a warning to transfer the
// ownership first (no cascade, no automatic transfer).
func UpdateUser(c *gin.Context) {
	me, ok := auth.FromContext(c)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "missing platform identity", "")
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req updatePlatformUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	role := strings.TrimSpace(req.Role)
	status := strings.TrimSpace(req.Status)
	if role != auth.RoleAdmin && role != auth.RoleUser {
		xhttp.ErrCode(c, "user.invalid_role", gin.H{"role": role}, "role must be admin or user", "")
		return
	}
	switch status {
	case account.StatusActive, account.StatusDisabled, account.StatusLocked:
	default:
		xhttp.ErrCode(c, "user.invalid_status", gin.H{"status": status}, "status must be active / disabled / locked", "")
		return
	}

	target, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if target == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "user not found", "")
		return
	}

	// Guard 0: the protected administrator account (fixed name plat) must not be modified - like
	// primary on the tenant side, it is "the account the system preinstalls".
	// This comes before "cannot act on yourself", so even plat signing in cannot change itself
	// (including the display name, a demotion or a disable).
	// Deletion does not exist: this module has no DELETE /users/:id (the account is seeded by the
	// environment and can only be disabled or have its profile edited).
	if isProtectedPlatformUser(target.Name) {
		xhttp.ErrCode(c, "user.protected_account", gin.H{"name": target.Name},
			"the protected administrator account "+target.Name+" cannot be modified (seeded by the environment)", "")
		return
	}

	// Guard 1: cannot act on yourself
	if target.ID == me.ID && (role != auth.RoleAdmin || status != account.StatusActive) {
		xhttp.ErrCode(c, "user.cannot_modify_self", nil, "you cannot disable or demote the account you are signed in with", "")
		return
	}

	// Guard 2: the last administrator who can sign in
	demotingAdmin := target.Role == auth.RoleAdmin && target.Status == account.StatusActive &&
		(role != auth.RoleAdmin || status != account.StatusActive)
	if demotingAdmin {
		n, cerr := pmusers.CountActiveAdmins()
		if cerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to count admins: "+cerr.Error(), "")
			return
		}
		if n <= 1 {
			xhttp.ErrCode(c, "user.last_admin_undeletable", nil, "the last platform administrator cannot be disabled or demoted", "")
			return
		}
	}

	warning := ""
	if role == auth.RoleUser && status != account.StatusActive {
		if n, cerr := tenantsrepo.CountByOwner(target.ID); cerr == nil && n > 0 {
			warning = fmt.Sprintf("this account still owns %d tenant(s); transfer them to another owner first", n)
		}
	}

	if err := pmusers.UpdateProfile(id, strings.TrimSpace(req.DisplayName), role, status); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update platform user: "+err.Error(), "")
		return
	}

	data := gin.H{"id": id, "role": role, "status": status}
	if warning != "" {
		data["warning"] = warning
	}
	xhttp.OkJSON(c, "Success", data, "")
}

type resetPlatformPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ResetUserPassword POST /platform/users/:id/password
func ResetUserPassword(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	var req resetPlatformPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "the password must be at least 8 characters", "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrCode(c, "user.password_too_long", nil, account.ErrPasswordTooLong.Error(), "")
		return
	}

	u, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if u == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "user not found", "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}
	if err := pmusers.UpdatePassword(id, hash); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to reset password: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}
