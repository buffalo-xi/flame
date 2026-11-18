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

// LoginRequest 平台登录请求。
//
// **没有租户字段**：身份完全由 platform.users 决定（管理员 / 用户两种角色都在同一张表）。
// 前端按响应里的 role 决定进「管理员页」还是「用户页」。
type LoginRequest struct {
	Name     string `json:"name" binding:"required"`
	Password string `json:"password"`
}

// Login POST /platform/login
func Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		// 不区分「账号不存在」与「密码错误」，避免账号枚举
		xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
		return
	}
	if !db.IsConnected() {
		xhttp.ErrJSON(c, "Database not connected", nil, "")
		return
	}

	u, err := pmusers.GetByName(name)
	if err != nil {
		// 表未建 / 未执行迁移等情况如实报出，便于排查（与租户侧登录同一风格）
		xhttp.ErrJSON(c, "Failed to load platform user: "+err.Error(), nil, "")
		return
	}
	if u == nil {
		xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
		return
	}

	// 密码校验：bcrypt 优先，兼容历史 MD5（空值一律不通过）
	ok, needRehash := account.VerifyPassword(u.Password, req.Password)
	if !ok {
		xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
		return
	}
	if needRehash {
		// 平台用户理应是新建的 bcrypt 账号；命中 MD5 只记日志，不自动改库（避免登录流程写出意外值）
		xlog.Info("[platform-login] user %s password hash is legacy MD5, please reset it", u.Name)
	}

	// 状态校验：停用 / 锁定都拒绝
	if u.Status != account.StatusActive {
		if u.Status == account.StatusLocked {
			xhttp.ErrJSON(c, "该账号已被锁定，请联系管理员解锁", nil, "")
			return
		}
		xhttp.ErrJSON(c, "该账号已被停用，请联系管理员", nil, "")
		return
	}

	token, err := auth.GenerateToken(u.ID, u.Name, u.Role)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to generate token: "+err.Error(), nil, "")
		return
	}

	// 与租户登录同形态：{token, user}；user 里带 role，前端据此分流
	xhttp.OkJSON(c, "", gin.H{
		"token": token,
		"user":  userView(u),
	}, "")
}
