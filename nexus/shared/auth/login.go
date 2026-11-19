package auth

import (
	"database/sql"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/tenants"
	"flame/tools/xhttp"
	"flame/tools/xjwt"
	"flame/tools/xlog"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// LoginRequest is the login request body
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" `
	Schema   string `json:"schema" binding:"required"`
}

// LoginResponse is the login response body
type LoginResponse struct {
	User  map[string]interface{} `json:"user"`
	Token string                 `json:"token"`
}

// Login handles a login request
func Login(c *gin.Context) {
	// Check the request method
	if c.Request.Method != http.MethodPost {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request method, only POST is allowed", "")
		return
	}

	// Parse the request
	var reqBody LoginRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// Read username, password and schema
	name := reqBody.Username
	password := reqBody.Password
	schema := reqBody.Schema

	// Check the required fields
	if name == "" || schema == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Missing required fields: username, password, schema", "")
		return
	}

	// Tenant allow-list: schema is interpolated into the SQL identifier position, so it must first be
	// confirmed as a "registered and active" tenant.
	// Same decision as resolveTenant in MCP/RPC; any query failure is denied (fail-closed).
	// The wording matches "invalid user name or password" - nothing about whether a tenant exists or
	// is disabled leaks to an unauthenticated caller.
	if active, terr := tenants.IsActive(schema); terr != nil || !active {
		if terr != nil {
			xlog.Error("[login] tenant allow-list query failed: %v", terr)
		} else {
			xlog.Info("[login] rejected an unregistered or disabled tenant: %s", schema)
		}
		xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
		return
	}

	// Check the database connection
	if !db.IsConnected() {
		xhttp.ErrCode(c, "common.internal", nil, "Database not connected", "")
		return
	}

	// Build the query: role and status come along
	query := fmt.Sprintf("SELECT id, name, display_name, password, email, phone, role, status FROM %s.users WHERE name = $1", schema)

	// Run the query
	row := db.DB.QueryRow(query, name)

	// Variables for the result
	var id string
	var dbName string
	var dbPassword, displayName, email, phone sql.NullString
	var role, status string

	// Scan the result
	err := row.Scan(&id, &dbName, &displayName, &dbPassword, &email, &phone, &role, &status)
	if err != nil {
		if err == sql.ErrNoRows {
			// "no such user" and "wrong password" are not distinguished, to prevent account enumeration
			xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
			return
		}
		// Other errors (for example the tenant user role migration not yet applied) are reported as they are, for diagnosis
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load user: "+err.Error(), "")
		return
	}

	// Only management users may sign in to the console: service users call the API with an API key / OAuth only
	if role != RoleManage {
		xhttp.ErrCode(c, "auth.account_not_console", nil, "this account is a service user and cannot sign in to the console", "")
		return
	}

	// Status check: both disabled and locked are rejected
	if status != UserStatusActive {
		if status == UserStatusLocked {
			xhttp.ErrCode(c, "auth.account_locked", nil, "this account is locked; ask an administrator to unlock it", "")
			return
		}
		xhttp.ErrCode(c, "auth.account_disabled", nil, "this account is disabled; ask an administrator", "")
		return
	}

	// Password check: bcrypt first, legacy MD5 still accepted (an empty value never passes).
	// (The original implementation skipped the check when dbPassword was empty, which let an account
	//  with no password be signed into with any password - service users have no password by design,
	//  and one such row was equivalent to an open login.)
	ok, needRehash := VerifyPassword(dbPassword.String, password)
	if !ok {
		xhttp.ErrCode(c, "auth.invalid_credentials", nil, "invalid user name or password", "")
		return
	}

	// Legacy MD5 hash: transparently upgrade to bcrypt on a successful login (a failure only logs and does not affect this login)
	if needRehash {
		if newHash, herr := HashPassword(password); herr == nil {
			if _, uerr := db.DB.Exec(
				fmt.Sprintf("UPDATE %s.users SET password = $1 WHERE id = $2", schema),
				newHash, id); uerr != nil {
				xlog.Error("[login] rehash password failed for %s: %v", dbName, uerr)
			}
		}
	}

	// Password verified, issue the JWT token.
	// Note: the token deliberately carries no role - the role is decided only by the check above; /login is the only issuer today.
	jwtConfig := xjwt.JWTConfig{
		SecretKey:   config.Cfg.JWT.SecretKey,
		TokenExpiry: config.Cfg.JWT.TokenExpiry,
		Issuer:      config.Cfg.JWT.Issuer,
	}
	token, err := xjwt.GenerateTokenWithMapClaims(map[string]any{"id": id, "name": dbName, "schema": schema}, jwtConfig)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to generate token: "+err.Error(), "")
		return
	}

	// Return the user info (minus the password) and the token; role / status are for the frontend
	userInfo := map[string]interface{}{
		"id":           id,
		"name":         dbName,
		"display_name": displayName.String,
		"email":        email.String,
		"phone":        phone.String,
		"schema":       schema,
		"role":         role,
		"status":       status,
	}

	xhttp.OkJSON(c, "", LoginResponse{
		User:  userInfo,
		Token: token,
	}, "")
}
