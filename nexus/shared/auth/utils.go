package auth

import (
	"errors"
	"flame/nexus/shared/config"
	"flame/tools/xhttp"
	"flame/tools/xjwt"
	"strings"

	"github.com/gin-gonic/gin"
)

// parseClaims validates a token string and parses out the AuthInfo. Shared by header authentication and the
// first-message authentication of a WebSocket.
func parseClaims(tokenString string) (*AuthInfo, error) {
	// Validate the token
	claims, err := xjwt.ValidateTokenWithMapClaims(tokenString, xjwt.JWTConfig(config.Cfg.JWT))
	if err != nil {
		return nil, err
	}

	schemaObj, ok := claims["schema"]
	if !ok {
		return nil, errors.New("no schema claim")
	}
	schema, ok := schemaObj.(string)
	_ = schema
	if !ok {
		return nil, errors.New("schema is not a string")
	}

	idObj, ok := claims["id"]
	if !ok {
		return nil, errors.New("no id claim")
	}
	id, ok := idObj.(string)
	_ = id
	if !ok {
		return nil, errors.New("id is not a string")
	}

	nameObj, ok := claims["name"]
	if !ok {
		return nil, errors.New("no name claim")
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

// ValidateToken validates a token string directly and returns the AuthInfo. It is shared by the cases that
// cannot pass a token in a header, such as the first-message authentication of a WebSocket.
func ValidateToken(tokenString string) (*AuthInfo, error) {
	return parseClaims(tokenString)
}

// GetAuthInfo reads and parses the authentication information from the header
func GetAuthInfo(c *gin.Context) (*AuthInfo, error) {

	// Read the Authorization token from the request header
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || (!strings.HasPrefix(authHeader, "Bearer ") && !strings.HasPrefix(authHeader, "Admin ")) {
		return nil, errors.New("Authorization header is required")
	}

	// Extract the token
	tokenString := ""
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		tokenString = strings.TrimPrefix(authHeader, "Admin ")
	}

	return parseClaims(tokenString)
}

// AuthMiddleware is the authentication middleware
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authInfo, err := GetAuthInfo(c)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			c.Abort()
			return
		}

		// Store the authentication information in the context
		c.Set("authInfo", authInfo)
		c.Next()
	}
}
