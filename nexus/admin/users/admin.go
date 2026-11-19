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

// namePattern 登录名字符集：避免空白/特殊字符；DB 侧还有唯一索引兜底
// （/login 用 WHERE name = $1 取单行，重名会让登录行为不确定）。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RegisterRoutes 注册 /users 管理接口（外层已带 /users 前缀）
//
// 角色口径（本模块只做两件事）：
//   - role：manage 可登录控制台 / service 不可登录
//   - status：active / disabled / locked —— 账号是否可用（登录与 API 都看它）
//
// 不提供删除用户：created_by / updated_by 是对用户的弱引用 UUID，删掉会留悬空；
// 停用（status=disabled）即可，且能保留审计线索。
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("", listUsers)
	router.POST("/create", createUser)
	// 单用户配额覆盖（会话数 / 每会话空间）。不写 users，只动 user_quotas。
	router.PUT("/:id/quota", setUserQuota)
	router.PUT("/:id", updateUser)
	router.POST("/:id/password", resetPassword)
}

// authOf 取当前请求的租户认证信息（失败时已写响应）。
func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrJSON(c, "Invalid tenant auth info", nil, "")
		return nil, false
	}
	return info, true
}

// listUsers 返回本租户用户列表（id/name/display_name/email/phone/role/status）。
// 供「用户管理」与「服务凭证」共用 —— 凭证不限制角色，因此按角色过滤。
func listUsers(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	// 带上 user_quotas 的覆盖值（无行 = 跟随租户）。
	// 老租户可能还没补这张表：JOIN 失败时退回不带配额的查询，别让整个用户管理页打不开。
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
		xhttp.ErrJSON(c, "failed to list users: "+err.Error(), nil, "")
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
				xhttp.ErrJSON(c, "failed to scan user: "+err.Error(), nil, "")
				return
			}
			items = append(items, gin.H{
				"id": id, "name": name, "display_name": displayName, "email": email,
				"phone": phone, "role": role, "status": status,
				// null = 跟随租户；数字 = 该用户的覆盖值
				"max_sessions":              nullableInt(maxSessions),
				"max_session_storage_bytes": nullableInt64(maxStorage),
			})
			continue
		}
		if err := rows.Scan(&id, &name, &displayName, &email, &phone, &role, &status); err != nil {
			xhttp.ErrJSON(c, "failed to scan user: "+err.Error(), nil, "")
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
		// 前端显示「跟随租户（N）」时用的默认值
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
// null = 跟随租户；两个都 null = 删除覆盖行。
//
// 守卫与 updateUser 同口径：
//   - primary 是平台开通时创建的根账号，租户侧不可改；
//   - 不能把自己的配额设为 0（等于把自己锁成只读，且自己再改不回来）；
//   - 任何值都不得超过租户给每用户的上限（后端 clamp，不靠前端）。
func setUserQuota(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		xhttp.ErrJSON(c, "invalid user id", nil, "")
		return
	}

	var body struct {
		MaxSessions            *int   `json:"max_sessions"`
		MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	var curName string
	err := db.DB.QueryRow(`SELECT COALESCE(name, '') FROM `+info.Schema+`.users WHERE id = $1`, id).Scan(&curName)
	if err == sql.ErrNoRows {
		xhttp.ErrCode(c, "user.not_found", nil, "User not found", "")
		return
	}
	if err != nil {
		xhttp.ErrJSON(c, "failed to get user: "+err.Error(), nil, "")
		return
	}
	if curName == sharedtenants.PrimaryUserName {
		xhttp.ErrCode(c, "user.primary_readonly", nil, "primary 账号的配额只能由平台调整", "")
		return
	}

	sessionsCeil := quota.UserSessionsCeiling(info.Schema)
	if v := body.MaxSessions; v != nil {
		if *v < -1 {
			xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_sessions", "min": -1}, "会话数不能小于 -1（-1 表示不限）", "")
			return
		}
		if id == info.ID && *v == 0 {
			xhttp.ErrCode(c, "quota.self_lockout", gin.H{"field": "max_sessions"}, "不能把自己的会话配额设为 0", "")
			return
		}
		if sessionsCeil != -1 && *v != -1 && *v > sessionsCeil {
			xhttp.ErrCode(c, "quota.exceeds_tenant_limit", gin.H{"field": "max_sessions", "limit": sessionsCeil},
				fmt.Sprintf("会话数不能超过租户上限（%d）", sessionsCeil), "")
			return
		}
	}
	storageCeil := quota.UserSessionStorageCeiling(info.Schema)
	if v := body.MaxSessionStorageBytes; v != nil {
		if *v < -1 {
			xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1}, "空间不能小于 -1（-1 表示不限）", "")
			return
		}
		if id == info.ID && *v == 0 {
			xhttp.ErrCode(c, "quota.self_lockout", gin.H{"field": "max_session_storage_bytes"}, "不能把自己的空间配额设为 0", "")
			return
		}
		if storageCeil != -1 && *v != -1 && *v > storageCeil {
			xhttp.ErrCode(c, "quota.exceeds_tenant_limit", gin.H{"field": "max_session_storage_bytes", "limit": storageCeil},
				fmt.Sprintf("空间不能超过租户上限（%d 字节）", storageCeil), "")
			return
		}
	}

	in := quota.UserQuotaInput{MaxSessions: body.MaxSessions, MaxSessionStorageBytes: body.MaxSessionStorageBytes}
	if err := quota.SetUserQuota(info.Schema, id, in, info.ID); err != nil {
		xhttp.ErrJSON(c, "failed to set quota: "+err.Error(), nil, "")
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
	Name        string `json:"name" binding:"required"` // 登录名：唯一、创建后不可改
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Role        string `json:"role" binding:"required"` // manage | service
	Password    string `json:"password"`                // role=manage 必填；role=service 必须为空
}

// createUser POST /users/create
func createUser(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	if !namePattern.MatchString(name) {
		xhttp.ErrCode(c, "user.invalid_name", nil, "登录名只允许字母/数字/下划线/横线，长度 1-64", "")
		return
	}
	if role != auth.RoleManage && role != auth.RoleService {
		xhttp.ErrJSON(c, "role must be 'manage' or 'service'", nil, "")
		return
	}
	switch role {
	case auth.RoleManage:
		if len(req.Password) < 8 {
			xhttp.ErrCode(c, "user.password_required", nil, "管理用户必须设置密码（至少 8 位）", "")
			return
		}
	case auth.RoleService:
		// 服务用户不可登录 —— 不留密码，避免将来改成 manage 时"突然获得"登录能力
		if req.Password != "" {
			xhttp.ErrCode(c, "user.password_not_allowed", nil, "服务用户不可登录，不应设置密码", "")
			return
		}
	}

	// 登录名查重（唯一索引也会拦，这里先给出可读报错）
	var existID string
	err := db.DB.QueryRow(
		`SELECT id FROM `+info.Schema+`.users WHERE name = $1`, name).Scan(&existID)
	if err == nil {
		xhttp.ErrCode(c, "user.name_taken", gin.H{"name": name}, "登录名已存在: "+name, "")
		return
	}
	if err != sql.ErrNoRows {
		xhttp.ErrJSON(c, "failed to check name: "+err.Error(), nil, "")
		return
	}

	passwordHash := ""
	if role == auth.RoleManage {
		h, herr := auth.HashPassword(req.Password)
		if herr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
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
		xhttp.ErrJSON(c, "failed to create user: "+err.Error(), nil, "")
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
	// Password 可选：留空表示不改密码。service → manage 且库中无密码时必须提供。
	Password string `json:"password"`
}

// updateUser PUT /users/:id
// 可改：display_name / email / phone / role / status（name 不可改，它是审计与绑定的锚点）
func updateUser(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	role := strings.TrimSpace(req.Role)
	status := strings.TrimSpace(req.Status)
	if role != auth.RoleManage && role != auth.RoleService {
		xhttp.ErrJSON(c, "role must be 'manage' or 'service'", nil, "")
		return
	}
	if status != auth.UserStatusActive && status != auth.UserStatusDisabled && status != auth.UserStatusLocked {
		xhttp.ErrJSON(c, "status must be 'active' / 'disabled' / 'locked'", nil, "")
		return
	}

	// 读现状
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
		xhttp.ErrJSON(c, "failed to load user: "+err.Error(), nil, "")
		return
	}

	// 守卫 0：primary 是平台开通租户时创建的**根账号**（登录名由平台固定交付），任何字段都不许改 ——
	// 一旦被改名/降级/停用，租户就失去了"平台交付的那个可登录 manage 账号"。
	// 删除更不存在：本模块不提供删除接口（见 RegisterRoutes 注释），所以"不得删除"无需额外守卫。
	if curName == sharedtenants.PrimaryUserName {
		xhttp.ErrCode(c, "user.primary_readonly", nil, "primary 用户不可修改（平台开通时创建的根账号）", "")
		return
	}

	// 守卫 1：不能停用/降级当前登录账号（否则当场把自己踢出去）
	if id == info.ID && (role != auth.RoleManage || status != auth.UserStatusActive) {
		xhttp.ErrCode(c, "user.cannot_modify_self", nil, "不能停用或降级当前登录账号", "")
		return
	}

	// 守卫 2：不能把最后一个「可登录的管理用户」降级或停用（否则租户自锁，只能改库救场）
	losingLogin := curRole == auth.RoleManage && curStatus == auth.UserStatusActive &&
		(role != auth.RoleManage || status != auth.UserStatusActive)
	if losingLogin {
		var n int
		if err := db.DB.QueryRow(
			`SELECT count(*) FROM `+info.Schema+`.users WHERE role = $1 AND status = $2`,
			auth.RoleManage, auth.UserStatusActive).Scan(&n); err != nil {
			xhttp.ErrJSON(c, "failed to count admins: "+err.Error(), nil, "")
			return
		}
		if n <= 1 {
			xhttp.ErrCode(c, "user.last_admin_undeletable", nil, "不能停用或降级最后一个可登录的管理用户", "")
			return
		}
	}

	// 密码处理：
	//   - 改成 service：清空密码（不可登录，不该留凭据）
	//   - 保持/改成 manage：留空则不改；但库里没有密码时必须本次提供（否则建了个登不进去的管理员）
	setPassword := false
	newHash := ""
	if role == auth.RoleService {
		setPassword = true
		newHash = ""
	} else if pw := strings.TrimSpace(req.Password); pw != "" {
		if len(pw) < 8 {
			xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": 8}, "密码至少 8 位", "")
			return
		}
		h, herr := auth.HashPassword(pw)
		if herr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
			return
		}
		setPassword = true
		newHash = h
	} else if !curPassword.Valid || curPassword.String == "" {
		xhttp.ErrCode(c, "user.password_required", nil, "该用户当前没有密码，切换为管理用户必须设置密码（至少 8 位）", "")
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
		xhttp.ErrJSON(c, "failed to update user: "+execErr.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "role": role, "status": status}, "")
}

type changeMyPasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangeMyPassword POST /me/password
// 当前登录用户自助修改密码：必须提供旧密码，只改自己（不接受 user_id）。
// 与管理员「重置密码」不同：后者是代人重置，这里是本人凭旧密码更换。
func ChangeMyPassword(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	var req changeMyPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	switch {
	case len(req.NewPassword) < auth.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": auth.MinPasswordLength}, "新密码至少 8 位", "")
		return
	case len(req.NewPassword) > auth.MaxPasswordBytes:
		xhttp.ErrJSON(c, auth.ErrPasswordTooLong.Error(), nil, "")
		return
	case req.NewPassword == req.OldPassword:
		xhttp.ErrCode(c, "user.password_unchanged", nil, "新密码不能与当前密码相同", "")
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
		xhttp.ErrJSON(c, "failed to load user: "+err.Error(), nil, "")
		return
	}

	// 校验旧密码（兼容历史 MD5；通过后下面写入 bcrypt，等于顺手完成升级）
	if okOld, _ := auth.VerifyPassword(curPassword.String, req.OldPassword); !okOld {
		xhttp.ErrCode(c, "user.password_incorrect", nil, "当前密码不正确", "")
		return
	}

	newHash, herr := auth.HashPassword(req.NewPassword)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
		return
	}
	if _, err := db.DB.Exec(
		`UPDATE `+info.Schema+`.users SET password=$2, updated_at=NOW() WHERE id=$1`,
		info.ID, newHash); err != nil {
		xhttp.ErrJSON(c, "failed to update password: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": info.ID}, "")
}

type resetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// resetPassword POST /users/:id/password
// 也是"空密码账号"的解锁入口（迁移后 password 为空的行登不进去）。
func resetPassword(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if len(req.Password) < 8 {
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": 8}, "密码至少 8 位", "")
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
		xhttp.ErrJSON(c, "failed to load user: "+err.Error(), nil, "")
		return
	}
	if role != auth.RoleManage {
		xhttp.ErrCode(c, "user.password_not_allowed", nil, "服务用户不可登录，无需设置密码", "")
		return
	}

	h, herr := auth.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
		return
	}
	if _, err := db.DB.Exec(
		`UPDATE `+info.Schema+`.users SET password=$2, updated_at=NOW() WHERE id=$1`,
		id, h); err != nil {
		xhttp.ErrJSON(c, "failed to reset password: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}
