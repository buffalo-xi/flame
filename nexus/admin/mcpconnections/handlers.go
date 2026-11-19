package mcpconnections

import (
	"fmt"

	"flame/nexus/service/mcp/client"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	mcprepo "flame/nexus/shared/repository/mcpconnections"
	"flame/nexus/shared/svcpath"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Shared repository instance
var repo = mcprepo.NewRepository()

// GetMcpConnection returns an MCP connection by ID
func GetMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")

	conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrCode(c, "mcp.connection_not_found", nil, "MCP connection not found", "")
		return
	}

	xhttp.OkJSON(c, "Success", conn, "")
}

// CreateMcpConnection creates an MCP connection
func CreateMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	var conn models.McpConnection
	if err := c.ShouldBindJSON(&conn); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	if err := validateMcpConnectionName(conn.Name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if conn.DisplayName == "" {
		conn.DisplayName = conn.Name
	}

	if err := rejectSelfLink(authInfo.(*auth.AuthInfo).Schema, conn.URL); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &conn)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	conn.ID = id

	// When enabled, discover and persist the tools right away
	if conn.IsActive {
		if created, gerr := repo.GetByID(authInfo.(*auth.AuthInfo), id); gerr == nil {
			_ = client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), created, repo)
		}
	}

	xhttp.OkJSON(c, "Success", conn, "")
}

// UpdateMcpConnection updates an MCP connection
func UpdateMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")

	var conn models.McpConnection
	if err := c.ShouldBindJSON(&conn); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	if conn.Name != "" {
		if err := validateMcpConnectionName(conn.Name); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}
	if conn.DisplayName == "" {
		conn.DisplayName = conn.Name
	}

	if err := rejectSelfLink(authInfo.(*auth.AuthInfo).Schema, conn.URL); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	if err := repo.Update(authInfo.(*auth.AuthInfo), id, &conn); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// After a stdio connection config change, invalidate the old session in the process pool;
	// the next call rebuilds the child process from the new config
	client.GetSessionPool().Invalidate(id)

	// When updated to enabled, refresh the tool list
	if conn.IsActive {
		if updated, gerr := repo.GetByID(authInfo.(*auth.AuthInfo), id); gerr == nil {
			_ = client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), updated, repo)
		}
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// SetMcpConnectionEnabled enables / disables an MCP connection (a lightweight state switch that
// does not edit other fields directly).
// Enabling runs "discover the tools first, persist only after success": a failed discovery leaves
// is_active untouched, so the card never shows disabled while the row says enabled.
func SetMcpConnectionEnabled(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")
	if id == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Connection ID is required", "")
		return
	}

	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	if body.IsActive {
		// Enable: rediscover and persist the tools first, persist the switch only on success, so
		// the card state matches the row
		conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
		if err != nil {
			xhttp.ErrCode(c, "mcp.connection_not_found", nil, "MCP connection not found", "")
			return
		}
		if err := client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), conn, repo); err != nil {
			xhttp.ErrCode(c, "mcp.discovery_failed", nil, "tool discovery failed, the connection was not enabled: "+err.Error(), "")
			return
		}
		if err := repo.UpdateIsActive(authInfo.(*auth.AuthInfo), id, true); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	} else {
		// Disable: persist first, then invalidate the stdio sessions in the process pool
		if err := repo.UpdateIsActive(authInfo.(*auth.AuthInfo), id, false); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		client.GetSessionPool().Invalidate(id)
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// validateMcpConnectionName checks the connection identifier (name): non-empty and ASCII letters and digits only
func validateMcpConnectionName(name string) error {
	if name == "" {
		return fmt.Errorf("the connection identifier (name) cannot be empty")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("the connection identifier (name) accepts ASCII letters and digits only, with no spaces or special characters")
		}
	}
	return nil
}

// rejectSelfLink stops an MCP connection from pointing at the tenant's own service endpoint
// (/service/mcp/{schema}).
// The test is whether the schema in the URL path equals the caller's tenant, regardless of
// domain / IP; pointing at another tenant (a cross-tenant link) is allowed. An empty URL (stdio
// transport) is skipped.
func rejectSelfLink(schema, rawURL string) error {
	if s := svcpath.SchemaFromURL(rawURL, "mcp"); s != "" && s == schema {
		return fmt.Errorf("a connection cannot point at the tenant itself (%s)", s)
	}
	return nil
}

// DeleteMcpConnection deletes an MCP connection
func DeleteMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")

	if err := repo.Delete(authInfo.(*auth.AuthInfo), id); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// After the connection is deleted, drop any stdio session left in the process pool
	client.GetSessionPool().Invalidate(id)

	xhttp.OkJSON(c, "MCP connection deleted successfully", id, "")
}

// ListMcpConnections returns the MCP connection list (paging plus filters)
func ListMcpConnections(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	var req mcprepo.ListMcpConnectionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	// Paging fallbacks
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	data, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	response := gin.H{
		"items": data,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// ListMcpConnectionTools lists the tools already persisted for a connection
func ListMcpConnectionTools(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")

	tools, err := repo.ListTools(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", tools, "")
}

// RefreshMcpConnectionTools rediscovers and persists a connection's tools immediately
func RefreshMcpConnectionTools(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return
	}

	id := c.Param("id")

	conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrCode(c, "mcp.connection_not_found", nil, "MCP connection not found", "")
		return
	}

	if err := client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), conn, repo); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	tools, _ := repo.ListTools(authInfo.(*auth.AuthInfo), id)
	xhttp.OkJSON(c, "Success", tools, "")
}
