package users

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/quota"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// namePattern is the login-name character set: it keeps out whitespace and special characters,
// and the unique index in the database is the second line of defence (/login fetches a single row
// with WHERE name = $1, so duplicates would make login behaviour undefined).
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RegisterRoutes registers the /users management endpoints (the caller already adds the /users
// prefix).
//
// Role model (this module does exactly two things):
//   - role: manage can sign in to the console / service cannot
//   - status: active / disabled / locked - whether the account is usable (both login and the API
//     look at it)
//
// There is no delete-user endpoint: created_by / updated_by are weak UUID references to users, so
// deleting one leaves them dangling; disabling (status=disabled) is enough and keeps the audit
// trail.
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("", listUsers)
	router.POST("/create", createUser)
	// Per-user quota overrides (session count / per-session storage). It never writes users, only user_quotas.
	router.PUT("/:id/quota", setUserQuota)
	router.PUT("/:id", updateUser)
	router.POST("/:id/password", resetPassword)
}

// authOf reads the current request's tenant auth info (the response is already written on failure).
func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	return info, true
}

// listUsers returns this tenant's users (id/name/display_name/email/phone/role/status).
// It is shared by "user management" and "service credentials" - credentials do not restrict the
// role, so the frontend filters by role.
func listUsers(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	// Include the user_quotas overrides (no row = follow the tenant).
	// An older tenant may not have that table yet: fall back to the query without quotas rather
	// than making the whole user management page unopenable.
	withQuota := true
	rows, err := db.DB.Query(
		`SELECT u.id, u.name, COALESCE(u.display_name, ''), COALESCE(u.email, ''), COALESCE(u.phone, ''),
		        COALESCE(u.role, ''), COALESCE(u.status, ''), q.max_sessions, q.max_session_storage_bytes
		   FROM ` + info.Schema + `.users u
		   LEFT JOIN ` + info.Schema + `.user_quotas q ON q.user_id = u.id
		  ORDER BY u.role, u.name`)
	if err != nil {
		withQuota = false
		rows, err = db.DB.Query(
			`SELECT id, name, COALESCE(display_name, ''), COALESCE(email, ''), COALESCE(phone, ''),
			        COALESCE(role, ''), COALESCE(status, '')
			   FROM ` + info.Schema + `.users
			  ORDER BY role, name`)
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list users: "+err.Error(), "")
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0)
	for rows.Next() {
		var id, name, displayName, email, phone, role, status string
		if withQuota {
			var maxSessions sql.NullInt32
			var maxStorage sql.NullInt64
			if err := rows.Scan(&id, &name, &displayName, &email, &phone, &role, &status, &maxSessions, &maxStorage); err != nil {
				xhttp.ErrCode(c, "common.internal", nil, "failed to scan user: "+err.Error(), "")
				return
			}
			items = append(items, gin.H{
				"id": id, "name": name, "display_name": displayName, "email": email,
				"phone": phone, "role": role, "status": status,
				// null = follow the tenant; a number = this user's override
				"max_sessions":              nullableInt(maxSessions),
				"max_session_storage_bytes": nullableInt64(maxStorage),
			})
			continue
		}
		if err := rows.Scan(&id, &name, &displayName, &email, &phone, &role, &status); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to scan user: "+err.Error(), "")
			return
		}
		items = append(items, gin.H{
			"id":           id,
			"name":         name,
			"display_name": displayName,
			"email":        email,
			"phone":        phone,
			"role":         role,
			"status":       status,
		})
	}

	lim := quota.Resolve(info.Schema)
	xhttp.OkJSON(c, "Success", gin.H{
		"items": items,
		// The defaults the frontend uses to render "follow the tenant (N)"
		"defaults": gin.H{
			"sessions":              lim.MaxUserSessions,
			"session_storage_bytes": lim.MaxSessionStorageBytes,
		},
	}, "")
}

func nullableInt(v sql.NullInt32) interface{} {
	if !v.Valid {
		return nil
	}
	return int(v.Int32)
}

func nullableInt64(v sql.NullInt64) interface{} {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// setUserQuota PUT /users/:id/quota
//
// body: {"max_sessions": 20|null, "max_session_storage_bytes": 268435456|null}
// null = follow the tenant; both null = delete the override row.
//
// The guards match updateUser:
//   - primary is the root account created at provisioning and is read-only on the tenant side;
//   - your own quota cannot be set to 0 (that locks you out as read-only, and you cannot undo it);
//   - no value may exceed the tenant's per-user ceiling (clamped in the backend, not relied on from
//     the frontend).
func setUserQuota(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid user id", "")
		return
	}

	var body struct {
		MaxSessions            *int   `json:"max_sessions"`
		MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	var curName string
	err := db.DB.QueryRow(`SELECT COALESCE(name, '') FROM `+info.Schema+`.users WHERE id = $1`, id).Scan(&curName)
	if err == sql.ErrNoRows {
		xhttp.ErrCode(c, "user.not_found", nil, "User not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get user: "+err.Error(), "")
		return
	}
	if curName == sharedtenants.PrimaryUserName {
		xhttp.ErrCode(c, "user.primary_readonly", nil, "the primary account's quota can only be adjusted by the platform", "")
		return
	}

	sessionsCeil := quota.UserSessionsCeiling(info.Schema)
	if v := body.MaxSessions; v != nil {
		if *v < -1 {
			xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_sessions", "min": -1}, "the session count cannot be below -1 (-1 means unlimited)", "")
			return
		}
		if id == info.ID && *v == 0 {
			xhttp.ErrCode(c, "quota.self_lockout", gin.H{"field": "max_sessions"}, "you cannot set your own session quota to 0", "")
			return
		}
		if sessionsCeil != -1 && *v != -1 && *v > sessionsCeil {
			xhttp.ErrCode(c, "quota.exceeds_tenant_limit", gin.H{"field": "max_sessions", "limit": sessionsCeil},
				fmt.Sprintf("the session count cannot exceed the tenant ceiling (%d)", sessionsCeil), "")
			return
		}
	}
	storageCeil := quota.UserSessionStorageCeiling(info.Schema)
	if v := body.MaxSessionStorageBytes; v != nil {
		if *v < -1 {
			xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1}, "storage cannot be below -1 (-1 means unlimited)", "")
			return
		}
		if id == info.ID && *v == 0 {
			xhttp.ErrCode(c, "quota.self_lockout", gin.H{"field": "max_session_storage_bytes"}, "you cannot set your own storage quota to 0", "")
			return
		}
		if storageCeil != -1 && *v != -1 && *v > storageCeil {
			xhttp.ErrCode(c, "quota.exceeds_tenant_limit", gin.H{"field": "max_session_storage_bytes", "limit": storageCeil},
				fmt.Sprintf("storage cannot exceed the tenant ceiling (%d bytes)", storageCeil), "")
			return
		}
	}

	in := quota.UserQuotaInput{MaxSessions: body.MaxSessions, MaxSessionStorageBytes: body.MaxSessionStorageBytes}
	if err := quota.SetUserQuota(info.Schema, id, in, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set quota: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"max_sessions":              body.MaxSessions,
		"max_session_storage_bytes": body.MaxSessionStorageBytes,
		"effective_sessions":        quota.ResolveUserSessions(info.Schema, id),
		"effective_storage_bytes":   quota.ResolveUserSessionStorage(info.Schema, id),
	}, "")
}

type createUserRequest struct {
	Name        string `json:"name" binding:"required"` // login name: unique and immutable after creation
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Role        string `json:"role" binding:"required"` // manage | service
	Password    string `json:"password"`                // required for role=manage; must be empty for role=service
}

// createUser POST /users/create
func createUser(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	if !namePattern.MatchString(name) {
		xhttp.ErrCode(c, "user.invalid_name", nil, "the login name accepts letters, digits, underscores and hyphens only, 1-64 characters", "")
		return
	}
	if role != auth.RoleManage && role != auth.RoleService {
		xhttp.ErrCode(c, "user.invalid_role", nil, "role must be 'manage' or 'service'", "")
		return
	}
	switch role {
	case auth.RoleManage:
		if len(req.Password) < 8 {
			xhttp.ErrCode(c, "user.password_required", nil, "a management user must have a password (at least 8 characters)", "")
			return
		}
	case auth.RoleService:
		// A service user cannot sign in - it keeps no password, so it never "suddenly gains" login
		// ability if switched to manage later
		if req.Password != "" {
			xhttp.ErrCode(c, "user.password_not_allowed", nil, "a service user cannot sign in and must not have a password", "")
			return
		}
	}

	// Duplicate login name (the unique index catches it too; this gives a readable error first)
	var existID string
	err := db.DB.QueryRow(
		`SELECT id FROM `+info.Schema+`.users WHERE name = $1`, name).Scan(&existID)
	if err == nil {
		xhttp.ErrCode(c, "user.name_taken", gin.H{"name": name}, "the login name already exists: "+name, "")
		return
	}
	if err != sql.ErrNoRows {
		xhttp.ErrCode(c, "common.internal", nil, "failed to check name: "+err.Error(), "")
		return
	}

	passwordHash := ""
	if role == auth.RoleManage {
		h, herr := auth.HashPassword(req.Password)
		if herr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
			return
		}
		passwordHash = h
	}
	id := uuid.NewString()
	_, err = db.DB.Exec(`INSERT INTO `+info.Schema+`.users
		(id, name, display_name, email, phone, password, role, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, NOW(), NOW())`,
		id, name, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Email),
		strings.TrimSpace(req.Phone), passwordHash, role, auth.UserStatusActive)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create user: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"id":     id,
		"name":   name,
		"role":   role,
		"status": auth.UserStatusActive,
	}, "")
}

type updateUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Role        string `json:"role" binding:"required"`   // manage | service
	Status      string `json:"status" binding:"required"` // active | disabled | locked
	// Password is optional: empty means unchanged. It is required when switching service -> manage
	// and the row has no password.
	Password string `json:"password"`
}

// updateUser PUT /users/:id
// Editable: display_name / email / phone / role / status (name is immutable - it anchors the audit
// trail and bindings)
func updateUser(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	role := strings.TrimSpace(req.Role)
	status := strings.TrimSpace(req.Status)
	if role != auth.RoleManage && role != auth.RoleService {
		xhttp.ErrCode(c, "user.invalid_role", nil, "role must be 'manage' or 'service'", "")
		return
	}
	if status != auth.UserStatusActive && status != auth.UserStatusDisabled && status != auth.UserStatusLocked {
		xhttp.ErrCode(c, "user.invalid_status", nil, "status must be 'active' / 'disabled' / 'locked'", "")
		return
	}

	// Read the current row
	var curName, curRole, curStatus string
	var curPassword sql.NullString
	err := db.DB.QueryRow(
		`SELECT COALESCE(name, ''), COALESCE(role, ''), COALESCE(status, ''), password
		   FROM `+info.Schema+`.users WHERE id = $1`, id).
		Scan(&curName, &curRole, &curStatus, &curPassword)
	if err == sql.ErrNoRows {
		xhttp.ErrCode(c, "user.not_found", nil, "User not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load user: "+err.Error(), "")
		return
	}

	// Guard 0: primary is the **root account** created when the platform provisions a tenant (its
	// login name is delivered by the platform), so no field may change - once it is renamed,
	// downgraded or disabled the tenant loses "the signable manage account the platform delivered".
	// Deletion does not even exist: this module has no delete endpoint (see the RegisterRoutes
	// comment), so "must not be deleted" needs no extra guard.
	if curName == sharedtenants.PrimaryUserName {
		xhttp.ErrCode(c, "user.primary_readonly", nil, "the primary user is read-only (the root account created at provisioning)", "")
		return
	}

	// Guard 1: the signed-in account cannot disable or downgrade itself (that would kick itself out on the spot)
	if id == info.ID && (role != auth.RoleManage || status != auth.UserStatusActive) {
		xhttp.ErrCode(c, "user.cannot_modify_self", nil, "you cannot disable or downgrade the account you are signed in with", "")
		return
	}

	// Guard 2: the last "signable management user" cannot be downgraded or disabled (that would
	// lock the tenant out, recoverable only by editing the database)
	losingLogin := curRole == auth.RoleManage && curStatus == auth.UserStatusActive &&
		(role != auth.RoleManage || status != auth.UserStatusActive)
	if losingLogin {
		var n int
		if err := db.DB.QueryRow(
			`SELECT count(*) FROM `+info.Schema+`.users WHERE role = $1 AND status = $2`,
			auth.RoleManage, auth.UserStatusActive).Scan(&n); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to count admins: "+err.Error(), "")
			return
		}
		if n <= 1 {
			xhttp.ErrCode(c, "user.last_admin_undeletable", nil, "the last signable management user cannot be disabled or downgraded", "")
			return
		}
	}

	// Password handling:
	//   - switching to service: clear the password (cannot sign in, should keep no credential)
	//   - staying / becoming manage: empty means unchanged; but if the row has no password it must
	//     be provided now (otherwise the new admin cannot sign in)
	setPassword := false
	newHash := ""
	if role == auth.RoleService {
		setPassword = true
		newHash = ""
	} else if pw := strings.TrimSpace(req.Password); pw != "" {
		if len(pw) < 8 {
			xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": 8}, "the password must be at least 8 characters", "")
			return
		}
		h, herr := auth.HashPassword(pw)
		if herr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
			return
		}
		setPassword = true
		newHash = h
	} else if !curPassword.Valid || curPassword.String == "" {
		xhttp.ErrCode(c, "user.password_required", nil, "this user has no password; switching to a management user requires setting one (at least 8 characters)", "")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	email := strings.TrimSpace(req.Email)
	phone := strings.TrimSpace(req.Phone)
	var execErr error
	if setPassword {
		_, execErr = db.DB.Exec(`UPDATE `+info.Schema+`.users
			SET display_name=$2, email=$3, phone=$4, role=$5, status=$6, password=$7, updated_at=NOW()
			WHERE id=$1`, id, displayName, email, phone, role, status, newHash)
	} else {
		_, execErr = db.DB.Exec(`UPDATE `+info.Schema+`.users
			SET display_name=$2, email=$3, phone=$4, role=$5, status=$6, updated_at=NOW()
			WHERE id=$1`, id, displayName, email, phone, role, status)
	}
	if execErr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to update user: "+execErr.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "role": role, "status": status}, "")
}

type changeMyPasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangeMyPassword POST /me/password
// Self-service password change for the signed-in user: the old password is required, and it only
// changes your own (no user_id accepted).
// Unlike the admin "reset password", which resets on someone's behalf, this replaces the password
// with the old one as proof.
func ChangeMyPassword(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	var req changeMyPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	switch {
	case len(req.NewPassword) < auth.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": auth.MinPasswordLength}, "the new password must be at least 8 characters", "")
		return
	case len(req.NewPassword) > auth.MaxPasswordBytes:
		xhttp.ErrCode(c, "user.password_too_long", nil, auth.ErrPasswordTooLong.Error(), "")
		return
	case req.NewPassword == req.OldPassword:
		xhttp.ErrCode(c, "user.password_unchanged", nil, "the new password must differ from the current one", "")
		return
	}

	var curPassword sql.NullString
	err := db.DB.QueryRow(
		`SELECT password FROM `+info.Schema+`.users WHERE id = $1`, info.ID).Scan(&curPassword)
	if err == sql.ErrNoRows {
		xhttp.ErrCode(c, "user.not_found", nil, "User not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load user: "+err.Error(), "")
		return
	}

	// Verify the old password (legacy MD5 is accepted; the bcrypt write below upgrades it on the way)
	if okOld, _ := auth.VerifyPassword(curPassword.String, req.OldPassword); !okOld {
		xhttp.ErrCode(c, "user.password_incorrect", nil, "the current password is incorrect", "")
		return
	}

	newHash, herr := auth.HashPassword(req.NewPassword)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}
	if _, err := db.DB.Exec(
		`UPDATE `+info.Schema+`.users SET password=$2, updated_at=NOW() WHERE id=$1`,
		info.ID, newHash); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to update password: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": info.ID}, "")
}

type resetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// resetPassword POST /users/:id/password
// It is also the unlock entry for "password-less accounts" (rows whose password is empty after a
// migration cannot sign in).
func resetPassword(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}
	if len(req.Password) < 8 {
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": 8}, "the password must be at least 8 characters", "")
		return
	}

	var role string
	err := db.DB.QueryRow(
		`SELECT COALESCE(role, '') FROM `+info.Schema+`.users WHERE id = $1`, id).Scan(&role)
	if err == sql.ErrNoRows {
		xhttp.ErrCode(c, "user.not_found", nil, "User not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to load user: "+err.Error(), "")
		return
	}
	if role != auth.RoleManage {
		xhttp.ErrCode(c, "user.password_not_allowed", nil, "a service user cannot sign in and needs no password", "")
		return
	}

	h, herr := auth.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to set the password: "+herr.Error(), "")
		return
	}
	if _, err := db.DB.Exec(
		`UPDATE `+info.Schema+`.users SET password=$2, updated_at=NOW() WHERE id=$1`,
		id, h); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to reset password: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}
