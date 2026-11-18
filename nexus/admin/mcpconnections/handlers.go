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

// 全局Repository实例
var repo = mcprepo.NewRepository()

// GetMcpConnection 根据 ID 获取 MCP 连接
func GetMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")

	conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrJSON(c, "MCP connection not found", nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", conn, "")
}

// CreateMcpConnection 创建 MCP 连接
func CreateMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	var conn models.McpConnection
	if err := c.ShouldBindJSON(&conn); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	if err := validateMcpConnectionName(conn.Name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if conn.DisplayName == "" {
		conn.DisplayName = conn.Name
	}

	if err := rejectSelfLink(authInfo.(*auth.AuthInfo).Schema, conn.URL); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	id, err := repo.Create(authInfo.(*auth.AuthInfo), &conn)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	conn.ID = id

	// 若启用，立即发现并持久化工具
	if conn.IsActive {
		if created, gerr := repo.GetByID(authInfo.(*auth.AuthInfo), id); gerr == nil {
			_ = client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), created, repo)
		}
	}

	xhttp.OkJSON(c, "Success", conn, "")
}

// UpdateMcpConnection 更新 MCP 连接
func UpdateMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")

	var conn models.McpConnection
	if err := c.ShouldBindJSON(&conn); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	if conn.Name != "" {
		if err := validateMcpConnectionName(conn.Name); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	}
	if conn.DisplayName == "" {
		conn.DisplayName = conn.Name
	}

	if err := rejectSelfLink(authInfo.(*auth.AuthInfo).Schema, conn.URL); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	if err := repo.Update(authInfo.(*auth.AuthInfo), id, &conn); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// stdio 连接配置变更后，使进程池中的旧会话失效，下次调用将使用新配置重建子进程
	client.GetSessionPool().Invalidate(id)

	// 若更新为启用，刷新工具列表
	if conn.IsActive {
		if updated, gerr := repo.GetByID(authInfo.(*auth.AuthInfo), id); gerr == nil {
			_ = client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), updated, repo)
		}
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// SetMcpConnectionEnabled 启用/停用 MCP 连接（轻量状态切换，不直接编辑其他字段）。
// 启用顺序为「先发现工具、成功后再落库」：工具发现失败则不改变 is_active，
// 避免出现「库里已启用、卡片仍显示停用」的不一致。
func SetMcpConnectionEnabled(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")
	if id == "" {
		xhttp.ErrJSON(c, "Connection ID is required", nil, "")
		return
	}

	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	if body.IsActive {
		// 启用：先重新发现并持久化工具，成功后才落库，保证卡片状态与库一致
		conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
		if err != nil {
			xhttp.ErrJSON(c, "MCP connection not found", nil, "")
			return
		}
		if err := client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), conn, repo); err != nil {
			xhttp.ErrJSON(c, "工具发现失败，未启用该连接: "+err.Error(), nil, "")
			return
		}
		if err := repo.UpdateIsActive(authInfo.(*auth.AuthInfo), id, true); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
	} else {
		// 停用：先落库，再使进程池中的 stdio 会话失效
		if err := repo.UpdateIsActive(authInfo.(*auth.AuthInfo), id, false); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		client.GetSessionPool().Invalidate(id)
	}

	xhttp.OkJSON(c, "Success", nil, "")
}

// validateMcpConnectionName 校验连接标识(name)：非空且只允许英文字母和数字
func validateMcpConnectionName(name string) error {
	if name == "" {
		return fmt.Errorf("连接标识(name)不能为空")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("连接标识(name)只允许英文字母和数字，不含空格或特殊字符")
		}
	}
	return nil
}

// rejectSelfLink 阻止把 MCP 连接指向本租户自身的服务端点（/service/mcp/{schema}）。
// 判据是 URL 路径里的 schema 与调用方租户一致，与域名/IP 无关；
// 指向其他租户（跨租户链接）合法放行。URL 为空（stdio 传输）时跳过。
func rejectSelfLink(schema, rawURL string) error {
	if s := svcpath.SchemaFromURL(rawURL, "mcp"); s != "" && s == schema {
		return fmt.Errorf("不能把连接指向本租户自身（%s）", s)
	}
	return nil
}

// DeleteMcpConnection 删除 MCP 连接
func DeleteMcpConnection(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")

	if err := repo.Delete(authInfo.(*auth.AuthInfo), id); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 连接删除后，移除进程池中可能残留的 stdio 会话
	client.GetSessionPool().Invalidate(id)

	xhttp.OkJSON(c, "MCP connection deleted successfully", id, "")
}

// ListMcpConnections 获取 MCP 连接列表（分页 + 过滤）
func ListMcpConnections(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	var req mcprepo.ListMcpConnectionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request parameters", nil, "")
		return
	}

	// 分页兜底
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	data, total, err := repo.List(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
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

// ListMcpConnectionTools 列出某连接已持久化的工具
func ListMcpConnectionTools(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")

	tools, err := repo.ListTools(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", tools, "")
}

// RefreshMcpConnectionTools 立即重新发现并持久化某连接的工具
func RefreshMcpConnectionTools(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return
	}

	id := c.Param("id")

	conn, err := repo.GetByID(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		xhttp.ErrJSON(c, "MCP connection not found", nil, "")
		return
	}

	if err := client.RefreshToolsWithRepo(c.Request.Context(), authInfo.(*auth.AuthInfo), conn, repo); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	tools, _ := repo.ListTools(authInfo.(*auth.AuthInfo), id)
	xhttp.OkJSON(c, "Success", tools, "")
}
