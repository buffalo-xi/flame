package auth

import (
	"net/http"
	"strings"

	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// contextKey 平台身份在 gin.Context 中的键（与租户侧的 "authInfo" 区分开）。
const contextKey = "platformAuthInfo"

// FromContext 取出当前请求的平台身份（未认证时 ok=false）。
func FromContext(c *gin.Context) (*PlatformAuthInfo, bool) {
	v, exist := c.Get(contextKey)
	if !exist {
		return nil, false
	}
	info, ok := v.(*PlatformAuthInfo)
	return info, ok
}

// bearerToken 从 Authorization: Bearer <token> 中取 token。
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// PlatformAuthMiddleware 平台身份校验：必须是 scope=platform 的有效 token。
// 失败返回 401（传输层语义，与业务错误码 400 区分）。
func PlatformAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台访问令牌")
			c.Abort()
			return
		}
		info, err := ParseToken(token)
		if err != nil {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "平台访问令牌无效或已过期")
			c.Abort()
			return
		}
		c.Set(contextKey, info)
		c.Next()
	}
}

// RequirePlatformAdmin 要求管理员身份（/platform/tenants*、/platform/users* 用）。
// 失败返回 403 —— 身份是有效的平台身份，但权限不足。
func RequirePlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		info, ok := FromContext(c)
		if !ok {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
			c.Abort()
			return
		}
		if !info.IsAdmin() {
			xhttp.SendErrorResponse(c, http.StatusForbidden, "forbidden", "需要平台管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}
