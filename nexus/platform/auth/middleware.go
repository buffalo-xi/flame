package auth

import (
	"net/http"
	"strings"

	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// contextKey is the gin.Context key for the platform identity (kept apart from the tenant side's "authInfo").
const contextKey = "platformAuthInfo"

// FromContext reads the current request's platform identity (ok=false when unauthenticated).
func FromContext(c *gin.Context) (*PlatformAuthInfo, bool) {
	v, exist := c.Get(contextKey)
	if !exist {
		return nil, false
	}
	info, ok := v.(*PlatformAuthInfo)
	return info, ok
}

// bearerToken extracts the token from Authorization: Bearer <token>.
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// PlatformAuthMiddleware validates the platform identity: a valid token with scope=platform is required.
// Failure returns 401 (transport-level semantics, kept apart from the 400 business error codes).
func PlatformAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform access token")
			c.Abort()
			return
		}
		info, err := ParseToken(token)
		if err != nil {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "the platform access token is invalid or expired")
			c.Abort()
			return
		}
		c.Set(contextKey, info)
		c.Next()
	}
}

// RequirePlatformAdmin requires the administrator role (used by /platform/tenants* and /platform/users*).
// Failure returns 403 - the identity is a valid platform identity, but lacks permission.
func RequirePlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		info, ok := FromContext(c)
		if !ok {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
			c.Abort()
			return
		}
		if !info.IsAdmin() {
			xhttp.SendErrorResponse(c, http.StatusForbidden, "forbidden", "platform administrator permission is required")
			c.Abort()
			return
		}
		c.Next()
	}
}
