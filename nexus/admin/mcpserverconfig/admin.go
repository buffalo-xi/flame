package mcpserverconfig

import (
	"strings"

	"flame/nexus/service/mcp/server"
	"flame/nexus/shared/auth"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires the /mcpserverconfig admin endpoints (the outer router adds the prefix).
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("", getServerConfig)
	router.PUT("", putServerConfig)
}

// validAuthType accepts apikey / oauth only.
func validAuthType(t string) bool {
	return t == "apikey" || t == "oauth"
}

func getServerConfig(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info, ok := authInfo.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrJSON(c, "Invalid tenant auth info", nil, "")
		return
	}

	authType := strings.TrimSpace(c.Query("auth_type"))
	if authType == "" {
		xhttp.ErrJSON(c, "auth_type query param required", nil, "")
		return
	}
	if !validAuthType(authType) {
		xhttp.ErrJSON(c, "auth_type must be 'apikey' or 'oauth'", nil, "")
		return
	}

	cfg, err := server.GetServerConfig(info, authType)
	if err != nil {
		xhttp.ErrJSON(c, "failed to load server config: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"auth_type":   cfg.AuthType,
		"auth_config": cfg.AuthConfig, // xsql.JSONB, gin serialises it as an object
		"is_active":   cfg.IsActive,
	}, "")
}

// putRequest matches the frontend form: the client assembles auth_config for apikey / oauth.
type putRequest struct {
	AuthType   string                 `json:"auth_type" binding:"required"`
	IsActive   bool                   `json:"is_active"`
	AuthConfig map[string]interface{} `json:"auth_config" binding:"required"`
}

func putServerConfig(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info, ok := authInfo.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrJSON(c, "Invalid tenant auth info", nil, "")
		return
	}

	var req putRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}
	if !validAuthType(req.AuthType) {
		xhttp.ErrJSON(c, "auth_type must be 'apikey' or 'oauth'", nil, "")
		return
	}

	cfg := &server.McpServerConfig{
		AuthType:   req.AuthType,
		AuthConfig: xsql.JSONB{Data: req.AuthConfig},
		IsActive:   req.IsActive,
	}
	if err := server.UpsertServerConfig(info, cfg); err != nil {
		xhttp.ErrJSON(c, "failed to save server config: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{"auth_type": req.AuthType, "is_active": req.IsActive}, "")
}
