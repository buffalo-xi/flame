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

// LoginRequest 登录请求结构体
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" `
	Schema   string `json:"schema" binding:"required"`
}

// LoginResponse 登录响应结构体
type LoginResponse struct {
	User  map[string]interface{} `json:"user"`
	Token string                 `json:"token"`
}

// Login 处理登录请求
func Login(c *gin.Context) {
	// 检查请求方法
	if c.Request.Method != http.MethodPost {
		xhttp.ErrJSON(c, "Invalid request method, only POST is allowed", nil, "")
		return
	}

	// 解析请求
	var reqBody LoginRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 获取username、password、schema三个字段
	name := reqBody.Username
	password := reqBody.Password
	schema := reqBody.Schema

	// 检查字段是否存在
	if name == "" || schema == "" {
		xhttp.ErrJSON(c, "Missing required fields: username, password, schema", nil, "")
		return
	}

	// 租户白名单：schema 会被拼进 SQL 的标识符位置，必须先确认它是「已注册且 active」的租户。
	// 与 MCP/RPC 的 resolveTenant 同一判定；查询失败一律拒绝（fail-closed）。
	// 文案与「用户名或密码错误」合并 —— 不向未认证方泄露租户是否存在 / 是否停用。
	if active, terr := tenants.IsActive(schema); terr != nil || !active {
		if terr != nil {
			xlog.Error("[login] 租户白名单查询失败: %v", terr)
		} else {
			xlog.Info("[login] 拒绝未注册或已停用的租户: %s", schema)
		}
		xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
		return
	}

	// 检查数据库连接
	if !db.IsConnected() {
		xhttp.ErrJSON(c, "Database not connected", nil, "")
		return
	}

	// 构建SQL查询语句：一并取出 role / status
	query := fmt.Sprintf("SELECT id, name, display_name, password, email, phone, role, status FROM %s.users WHERE name = $1", schema)

	// 执行查询
	row := db.DB.QueryRow(query, name)

	// 定义变量存储查询结果
	var id string
	var dbName string
	var dbPassword, displayName, email, phone sql.NullString
	var role, status string

	// 扫描查询结果
	err := row.Scan(&id, &dbName, &displayName, &dbPassword, &email, &phone, &role, &status)
	if err != nil {
		if err == sql.ErrNoRows {
			// 不区分「用户不存在」与「密码错误」，避免账号枚举
			xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
			return
		}
		// 其它错误（例如尚未执行租户用户角色迁移）如实报出，便于排查
		xhttp.ErrJSON(c, "Failed to load user: "+err.Error(), nil, "")
		return
	}

	// 只允许管理用户登录控制台：服务用户只用 API Key / OAuth 调接口
	if role != RoleManage {
		xhttp.ErrJSON(c, "该账号为服务用户，不能登录控制台", nil, "")
		return
	}

	// 状态校验：停用 / 锁定都拒绝
	if status != UserStatusActive {
		if status == UserStatusLocked {
			xhttp.ErrJSON(c, "该账号已被锁定，请联系管理员解锁", nil, "")
			return
		}
		xhttp.ErrJSON(c, "该账号已被停用，请联系管理员", nil, "")
		return
	}

	// 密码校验：bcrypt 优先，兼容历史 MD5（空值一律不通过）。
	//（原实现是「dbPassword 为空则跳过校验」，会让无密码账号被任意密码登录 ——
	//  服务用户天然无密码，一旦存在这种行就等同于开放登录。）
	ok, needRehash := VerifyPassword(dbPassword.String, password)
	if !ok {
		xhttp.ErrJSON(c, "用户名或密码错误", nil, "")
		return
	}

	// 旧 MD5 哈希：本次登录成功后透明升级为 bcrypt（失败只记日志，不影响本次登录）
	if needRehash {
		if newHash, herr := HashPassword(password); herr == nil {
			if _, uerr := db.DB.Exec(
				fmt.Sprintf("UPDATE %s.users SET password = $1 WHERE id = $2", schema),
				newHash, id); uerr != nil {
				xlog.Error("[login] rehash password failed for %s: %v", dbName, uerr)
			}
		}
	}

	// 密码验证成功，生成JWT token
	// 注意：token 刻意不带 role —— 角色只在上面这一关判定；当前 /login 是唯一签发点。
	jwtConfig := xjwt.JWTConfig{
		SecretKey:   config.Cfg.JWT.SecretKey,
		TokenExpiry: config.Cfg.JWT.TokenExpiry,
		Issuer:      config.Cfg.JWT.Issuer,
	}
	token, err := xjwt.GenerateTokenWithMapClaims(map[string]any{"id": id, "name": dbName, "schema": schema}, jwtConfig)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to generate token: "+err.Error(), nil, "")
		return
	}

	// 返回用户信息（除了password）和token；role / status 供前端展示
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
