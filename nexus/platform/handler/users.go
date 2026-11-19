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

// namePattern 平台登录名字符集（与租户用户一致；DB 侧还有唯一索引兜底）。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// isProtectedPlatformUser 是否是**受保护管理员**账号（登录名固定为 auth.BootstrapAdminName = plat）。
//
// 它与租户侧的 shared/tenants.PrimaryUserName 同性质：都是"系统预置的那个账号"——
// 平台首个 active 管理员由环境初始数据提供（nexus 启动不再创建任何账号）。
// 规则：**不可修改**（资料 / 角色 / 状态），也**不可删除**（本模块没有 DELETE /users/:id）。
func isProtectedPlatformUser(name string) bool {
	return strings.TrimSpace(name) == auth.BootstrapAdminName
}

// ListUsers GET /platform/users?role=&status=&q=&page=&page_size=
//
// 管理员视角：平台用户表（管理员 + 用户）的分页列表。
// 「用户」行附带 tenant_count（名下租户数），便于判断能否停用。
//
// 分页口径与 /platform/tenants 一致：page 从 1 起；page_size 省略 / <=0 = 不分页（返回全部），上限 100
// —— 每个「用户」行都要跑一次 CountByOwner，放大 page_size 等于放大 DB 压力。
// total 是满足筛选条件的总数（不是本页条数）。
func ListUsers(c *gin.Context) {
	role, status, q := c.Query("role"), c.Query("status"), c.Query("q")
	page, pageSize := parsePaging(c)

	users, err := pmusers.List(role, status, q, pageSize, (page-1)*pageSize)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list platform users: "+err.Error(), nil, "")
		return
	}

	total := len(users)
	if pageSize > 0 {
		n, cerr := pmusers.Count(role, status, q)
		if cerr != nil {
			xhttp.ErrJSON(c, "Failed to count platform users: "+cerr.Error(), nil, "")
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
// 某平台用户名下的租户（管理台详情卡「名下租户」块）。
// 与 /platform/me/tenants 同一个数据源（tenantsrepo.ListByOwner，已排除 status=deleted），
// 区别只是这里由管理员指定 id。
func UserTenants(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))

	u, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if u == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "用户不存在", "")
		return
	}

	list, err := tenantsrepo.ListByOwner(id)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list tenants: "+err.Error(), nil, "")
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
	DisplayName string `json:"display_name"` // 可空（为空时界面回退显示账号名）
	Password    string `json:"password" binding:"required"`
	Role        string `json:"role" binding:"required"` // admin | user
}

// CreateUser POST /platform/users
func CreateUser(c *gin.Context) {
	var req createPlatformUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	displayName := strings.TrimSpace(req.DisplayName)
	if !namePattern.MatchString(name) {
		xhttp.ErrCode(c, "user.invalid_name", gin.H{"name": name}, "登录名只允许字母/数字/下划线/横线，长度 1-64", "")
		return
	}
	if role != auth.RoleAdmin && role != auth.RoleUser {
		xhttp.ErrCode(c, "user.invalid_role", gin.H{"role": role}, "role 只能是 admin 或 user", "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "密码至少 8 位", "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrJSON(c, account.ErrPasswordTooLong.Error(), nil, "")
		return
	}

	exist, err := pmusers.GetByName(name)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to check name: "+err.Error(), nil, "")
		return
	}
	if exist != nil {
		xhttp.ErrCode(c, "user.name_taken", gin.H{"name": name}, "登录名已存在: "+name, "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
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
		xhttp.ErrJSON(c, "Failed to create platform user: "+err.Error(), nil, "")
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
// 守卫（与租户侧用户管理同口径）：
//  1. 不能停用/降级当前登录账号（否则当场把自己踢出去）
//  2. 不能停用/降级最后一个 active 的管理员（否则平台自锁，只能改库救场）
//
// 停用一个名下有租户的「用户」账号：允许，但返回 warning 提示先转移负责人（不级联、不自动转移）。
func UpdateUser(c *gin.Context) {
	me, ok := auth.FromContext(c)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "平台身份缺失", "")
		return
	}
	id := strings.TrimSpace(c.Param("id"))

	var req updatePlatformUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	role := strings.TrimSpace(req.Role)
	status := strings.TrimSpace(req.Status)
	if role != auth.RoleAdmin && role != auth.RoleUser {
		xhttp.ErrCode(c, "user.invalid_role", gin.H{"role": role}, "role 只能是 admin 或 user", "")
		return
	}
	switch status {
	case account.StatusActive, account.StatusDisabled, account.StatusLocked:
	default:
		xhttp.ErrCode(c, "user.invalid_status", gin.H{"status": status}, "status 只能是 active / disabled / locked", "")
		return
	}

	target, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if target == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "用户不存在", "")
		return
	}

	// 守卫 0：受保护管理员账号（固定 plat）不许修改 —— 与租户侧 primary 同理，它是"系统预置的那个账号"。
	// 放在"不能对自己动手"之前，所以 plat 本人登录也改不了自己（含改显示名、降级、停用）。
	// 删除不存在：本模块没有 DELETE /users/:id（账号由环境初始数据预置、之后只能停用/改资料）。
	if isProtectedPlatformUser(target.Name) {
		xhttp.ErrCode(c, "user.protected_account", gin.H{"name": target.Name},
			"受保护管理员账号 "+target.Name+" 不可修改（由环境初始数据预置）", "")
		return
	}

	// 守卫 1：不能对自己动手
	if target.ID == me.ID && (role != auth.RoleAdmin || status != account.StatusActive) {
		xhttp.ErrCode(c, "user.cannot_modify_self", nil, "不能停用或降级当前登录账号", "")
		return
	}

	// 守卫 2：最后一个可登录的管理员
	demotingAdmin := target.Role == auth.RoleAdmin && target.Status == account.StatusActive &&
		(role != auth.RoleAdmin || status != account.StatusActive)
	if demotingAdmin {
		n, cerr := pmusers.CountActiveAdmins()
		if cerr != nil {
			xhttp.ErrJSON(c, "Failed to count admins: "+cerr.Error(), nil, "")
			return
		}
		if n <= 1 {
			xhttp.ErrCode(c, "user.last_admin_undeletable", nil, "不能停用或降级最后一个平台管理员", "")
			return
		}
	}

	warning := ""
	if role == auth.RoleUser && status != account.StatusActive {
		if n, cerr := tenantsrepo.CountByOwner(target.ID); cerr == nil && n > 0 {
			warning = fmt.Sprintf("该账号名下还有 %d 个租户，建议先把租户转给其他负责人", n)
		}
	}

	if err := pmusers.UpdateProfile(id, strings.TrimSpace(req.DisplayName), role, status); err != nil {
		xhttp.ErrJSON(c, "Failed to update platform user: "+err.Error(), nil, "")
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
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	switch {
	case len(req.Password) < account.MinPasswordLength:
		xhttp.ErrCode(c, "user.password_too_short", gin.H{"min": account.MinPasswordLength}, "密码至少 8 位", "")
		return
	case len(req.Password) > account.MaxPasswordBytes:
		xhttp.ErrJSON(c, account.ErrPasswordTooLong.Error(), nil, "")
		return
	}

	u, err := pmusers.GetByID(id)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if u == nil {
		xhttp.ErrCode(c, "user.not_found", gin.H{"id": id}, "用户不存在", "")
		return
	}

	hash, herr := account.HashPassword(req.Password)
	if herr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "设置密码失败: "+herr.Error(), "")
		return
	}
	if err := pmusers.UpdatePassword(id, hash); err != nil {
		xhttp.ErrJSON(c, "Failed to reset password: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}
