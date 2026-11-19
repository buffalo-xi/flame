package handler

import (
	"database/sql"
	"fmt"
	"net/http"

	"flame/nexus/platform/auth"
	"flame/nexus/platform/repository/pmusers"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/account"
	"flame/nexus/shared/db"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// Me GET /platform/me
//
// Lets the frontend restore the identity after a page refresh (the frontend only stores the token
// and never decodes the JWT itself).
// It re-reads the database every time: once the account is disabled or deleted, the token stops
// working immediately even if it has not expired.
func Me(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}

	u, err := pmusers.GetByID(info.ID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if u == nil || u.Status != account.StatusActive {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "the account does not exist or is disabled")
		return
	}

	data := gin.H{"user": userView(u)}
	// For a user identity, add the tenant count under their name, used by the admin console top-bar badge
	if u.Role == auth.RoleUser {
		n, cerr := tenantsrepo.CountByOwner(u.ID)
		if cerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to count tenants: "+cerr.Error(), "")
			return
		}
		data["tenant_count"] = n
	}

	xhttp.OkJSON(c, "Success", data, "")
}

// MyTenants GET /platform/me/tenants
//
// The only data surface of the "user" identity: the tenants under my name (owner_user_id = me).
// Administrators do not come here (they use /platform/tenants to see everything).
func MyTenants(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}

	list, err := tenantsrepo.ListByOwner(info.ID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to list tenants: "+err.Error(), "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, tenantView(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

type resetPrimaryPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ResetMyTenantPrimaryPassword POST /platform/me/tenants/:schema/primary/password
//
// Lets a "user" reset the root account (primary) password of a tenant **under their own name**.
//
// Why this exists: provisioning delivers the primary account's initial password, and once it is
// forgotten or lost the tenant cannot get in itself (a password change needs the old one) and the
// platform console has no endpoint that touches tenant users - so the "owner" resets it here.
// It is equivalent to a tenant administrator's "reset password", except no old password is needed
// first.
//
// **It only changes the password**: role / status / display_name are never touched (primary is
// already under the "immutable" guard on the tenant side), no token is issued and no existing
// session is revoked (same semantics as /me/password); no cache needs invalidating.
func ResetMyTenantPrimaryPassword(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req resetPrimaryPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength},
			fmt.Sprintf("the password must be at least %d characters", account.MinPasswordLength), "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrCode(c, "user.password_too_long", nil, account.ErrPasswordTooLong.Error(), "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}

	// schema comes from the registry (a registered tenant name) and mustOwnTenant already confirmed
	// ownership, but the identifier is still quoted (defence in depth)
	tbl := pq.QuoteIdentifier(schema) + ".users"
	var id string
	err := db.DB.QueryRow(
		`SELECT id::text FROM `+tbl+` WHERE name = $1`, sharedtenants.PrimaryUserName).Scan(&id)
	if err == sql.ErrNoRows {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "the tenant root account primary does not exist")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant user: "+err.Error(), "")
		return
	}

	if _, err := db.DB.Exec(
		`UPDATE `+tbl+` SET password = $2, updated_at = NOW() WHERE name = $1`,
		sharedtenants.PrimaryUserName, hash); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to reset password: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "name": sharedtenants.PrimaryUserName, "id": id}, "")
}

type changeMyPasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangeMyPassword POST /platform/me/password
//
// Self-service password change for a platform account: the old password is required and it only
// changes your own (no id accepted).
// Unlike the admin "reset password" (POST /platform/users/:id/password), which resets on someone's
// behalf.
// Same semantics as the tenant-side /admin/me/password: **no new token is issued and no session on
// other devices is revoked**.
func ChangeMyPassword(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}

	var req changeMyPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	switch {
	case len(req.NewPassword) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "the new password must be at least 8 characters", "")
		return
	case len(req.NewPassword) > account.MaxPasswordBytes:
		xhttp.ErrCode(c, "user.password_too_long", nil, account.ErrPasswordTooLong.Error(), "")
		return
	case req.NewPassword == req.OldPassword:
		xhttp.ErrCode(c, "user.password_unchanged", nil, "the new password must differ from the current one", "")
		return
	}

	// Re-read the database every time: once the account is disabled or deleted, the password change is
	// refused even if the token has not expired
	u, err := pmusers.GetByID(info.ID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if u == nil || u.Status != account.StatusActive {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "the account does not exist or is disabled")
		return
	}

	// Verify the old password (legacy MD5 is accepted; the bcrypt write below upgrades it on the way)
	if okOld, _ := account.VerifyPassword(u.Password, req.OldPassword); !okOld {
		xhttp.ErrCode(c, "user.password_incorrect", nil, "the current password is incorrect", "")
		return
	}

	hash, herr := account.HashPassword(req.NewPassword)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}
	if err := pmusers.UpdatePassword(info.ID, hash); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update password: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": info.ID}, "")
}
