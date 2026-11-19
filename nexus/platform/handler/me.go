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
// 供前端刷新页面后恢复身份（前端只存 token，不自己解 JWT）。
// 每次都回查数据库：账号被停用/删除后，即便 token 仍在有效期内也立即失效。
func Me(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}

	u, err := pmusers.GetByID(info.ID)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if u == nil || u.Status != account.StatusActive {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "账号不存在或已停用")
		return
	}

	data := gin.H{"user": userView(u)}
	// 用户身份补一个"名下租户数"，供管理台顶栏徽标使用
	if u.Role == auth.RoleUser {
		n, cerr := tenantsrepo.CountByOwner(u.ID)
		if cerr != nil {
			xhttp.ErrJSON(c, "Failed to count tenants: "+cerr.Error(), nil, "")
			return
		}
		data["tenant_count"] = n
	}

	xhttp.OkJSON(c, "Success", data, "")
}

// MyTenants GET /platform/me/tenants
//
// 「用户」身份的唯一数据面：我名下（owner_user_id = 我）的租户。
// 管理员不走这里（他走 /platform/tenants 看全部）。
func MyTenants(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}

	list, err := tenantsrepo.ListByOwner(info.ID)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list tenants: "+err.Error(), nil, "")
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
// 「用户」为**自己名下**租户的根账号（primary）重置密码。
//
// 存在的意义：平台开通租户时交付的是 primary 的初始密码，一旦忘了/丢了，租户侧自己进不去
// （改密要旧密码）、平台管理台也没有触碰租户 users 的接口 —— 所以由"负责人"在此自助重置。
// 它等价于租户侧管理员的「重置密码」，只是不需要先拿到旧密码。
//
// **只改密码**：role / status / display_name 一律不碰（primary 在租户侧本就受"不可修改"守卫），
// 也不签发 token、不作废已有会话（与 /me/password 等改密接口同语义）；无缓存需要失效。
func ResetMyTenantPrimaryPassword(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req resetPrimaryPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength},
			fmt.Sprintf("密码至少 %d 位", account.MinPasswordLength), "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrJSON(c, account.ErrPasswordTooLong.Error(), nil, "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
		return
	}

	// schema 来自注册表（已注册的租户名）且 mustOwnTenant 已确认归属，这里仍转义标识符（纵深防御）
	tbl := pq.QuoteIdentifier(schema) + ".users"
	var id string
	err := db.DB.QueryRow(
		`SELECT id::text FROM `+tbl+` WHERE name = $1`, sharedtenants.PrimaryUserName).Scan(&id)
	if err == sql.ErrNoRows {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "租户根账号 primary 不存在")
		return
	}
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant user: "+err.Error(), nil, "")
		return
	}

	if _, err := db.DB.Exec(
		`UPDATE `+tbl+` SET password = $2, updated_at = NOW() WHERE name = $1`,
		sharedtenants.PrimaryUserName, hash); err != nil {
		xhttp.ErrJSON(c, "Failed to reset password: "+err.Error(), nil, "")
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
// 平台账号自助改密：必须提供旧密码，只改自己（不接受 id）。
// 与管理员「重置密码」（POST /platform/users/:id/password）不同：后者是代人重置。
// 与租户侧 /admin/me/password 同语义：**不签发新 token、不作废其它端会话**。
func ChangeMyPassword(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}

	var req changeMyPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	switch {
	case len(req.NewPassword) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "新密码至少 8 位", "")
		return
	case len(req.NewPassword) > account.MaxPasswordBytes:
		xhttp.ErrJSON(c, account.ErrPasswordTooLong.Error(), nil, "")
		return
	case req.NewPassword == req.OldPassword:
		xhttp.ErrCode(c, "user.password_unchanged", nil, "新密码不能与当前密码相同", "")
		return
	}

	// 每次回查数据库：账号被停用/删除后，即便 token 仍在有效期内也不许改密
	u, err := pmusers.GetByID(info.ID)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if u == nil || u.Status != account.StatusActive {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "账号不存在或已停用")
		return
	}

	// 校验旧密码（兼容历史 MD5；通过后下面写入 bcrypt，等于顺手完成升级）
	if okOld, _ := account.VerifyPassword(u.Password, req.OldPassword); !okOld {
		xhttp.ErrCode(c, "user.password_incorrect", nil, "当前密码不正确", "")
		return
	}

	hash, herr := account.HashPassword(req.NewPassword)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
		return
	}
	if err := pmusers.UpdatePassword(info.ID, hash); err != nil {
		xhttp.ErrJSON(c, "Failed to update password: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": info.ID}, "")
}
