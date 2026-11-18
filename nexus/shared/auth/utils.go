package auth

import (
	"errors"
	"flame/nexus/shared/config"
	"flame/tools/xhttp"
	"flame/tools/xjwt"
	"strings"

	"github.com/gin-gonic/gin"
)

// parseClaims 校验 token 字符串并解析出 AuthInfo。供 header 鉴权与 WebSocket 首条消息鉴权复用。
func parseClaims(tokenString string) (*AuthInfo, error) {
	// 验证token
	claims, err := xjwt.ValidateTokenWithMapClaims(tokenString, xjwt.JWTConfig(config.Cfg.JWT))
	if err != nil {
		return nil, err
	}

	schemaObj, ok := claims["schema"]
	if !ok {
		return nil, errors.New("没有schema信息")
	}
	schema, ok := schemaObj.(string)
	_ = schema
	if !ok {
		return nil, errors.New("schema is not a string")
	}

	idObj, ok := claims["id"]
	if !ok {
		return nil, errors.New("没有id信息")
	}
	id, ok := idObj.(string)
	_ = id
	if !ok {
		return nil, errors.New("id is not a string")
	}

	nameObj, ok := claims["name"]
	if !ok {
		return nil, errors.New("没有name信息")
	}
	name, ok := nameObj.(string)
	_ = name
	if !ok {
		return nil, errors.New("name is not a string")
	}

	var authInfo AuthInfo
	authInfo.ID = id
	authInfo.Name = name
	authInfo.Schema = schema

	return &authInfo, nil
}

// ValidateToken 直接校验 token 字符串，返回 AuthInfo。供 WebSocket 首条消息鉴权等无法在 header 传 token 的场景复用。
func ValidateToken(tokenString string) (*AuthInfo, error) {
	return parseClaims(tokenString)
}

// GetAuthInfo 从header中获取并解析认证信息
func GetAuthInfo(c *gin.Context) (*AuthInfo, error) {

	//从请求头中获取Authorization token
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || (!strings.HasPrefix(authHeader, "Bearer ") && !strings.HasPrefix(authHeader, "Admin ")) {
		return nil, errors.New("Authorization header is required")
	}

	// 提取token
	tokenString := ""
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		tokenString = strings.TrimPrefix(authHeader, "Admin ")
	}

	return parseClaims(tokenString)
}

// AuthMiddleware 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authInfo, err := GetAuthInfo(c)
		if err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			c.Abort()
			return
		}

		// 将认证信息存储到context
		c.Set("authInfo", authInfo)
		c.Next()
	}
}
