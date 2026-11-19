package handler

import (
	"strings"

	"flame/nexus/platform/auth"
	"flame/nexus/platform/repository/pmusers"
	"flame/nexus/shared/account"
	"flame/nexus/shared/db"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// LoginRequest is the platform login request.
//
// **There is no tenant field**: the identity is decided entirely by platform.users (both the admin
// and the user role live in that one table). The frontend uses the role in the response to decide
// whether to enter the admin pages or the user pages.
type LoginRequest struct {
	Name     string `json:"name" binding:"required"`
	Password string `json:"password"`
}

// Login POST /platform/login
func Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		// "no such account" and "wrong password" are not distinguished, to prevent account enumeration
		xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
		return
	}
	if !db.IsConnected() {
		xhttp.ErrCode(c, "common.internal", nil, "Database not connected", "")
		return
	}

	u, err := pmusers.GetByName(name)
	if err != nil {
		// A missing table or unapplied migration is reported as it is, for diagnosis (same style as tenant login)
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load platform user: "+err.Error(), "")
		return
	}
	if u == nil {
		xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
		return
	}

	// Password check: bcrypt first, legacy MD5 still accepted (an empty value never passes)
	ok, needRehash := account.VerifyPassword(u.Password, req.Password)
	if !ok {
		xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
		return
	}
	if needRehash {
		// Platform users should be freshly created bcrypt accounts; on an MD5 hit only log it and never
		// rewrite the row here (so the login flow cannot write unexpected values)
		xlog.Info("[platform-login] user %s password hash is legacy MD5, please reset it", u.Name)
	}

	// Status check: both disabled and locked are rejected
	if u.Status != account.StatusActive {
		if u.Status == account.StatusLocked {
			xhttp.ErrCode(c, "auth.account_locked", nil, "this account is locked; ask an administrator to unlock it", "")
			return
		}
		xhttp.ErrCode(c, "auth.account_disabled", nil, "this account is disabled; ask an administrator", "")
		return
	}

	token, err := auth.GenerateToken(u.ID, u.Name, u.Role)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to generate token: "+err.Error(), "")
		return
	}

	// Same shape as tenant login: {token, user}; user carries the role, which the frontend branches on
	xhttp.OkJSON(c, "", gin.H{
		"token": token,
		"user":  userView(u),
	}, "")
}
