package mcpconnections

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListMcpConnectionsRequest MCP 连接列表请求参数（分页 + 过滤）
type ListMcpConnectionsRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	Transport string   `json:"transport" form:"transport"`
	IsActive  *bool    `json:"is_active" form:"is_active"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// Repository MCP 连接仓储接口
type Repository interface {
	// GetByID 根据 ID 获取连接
	GetByID(authInfo *auth.AuthInfo, id string) (*models.McpConnection, error)

	// Create 创建连接，返回新记录 ID
	Create(authInfo *auth.AuthInfo, conn *models.McpConnection) (string, error)

	// Update 更新连接（按 ID）
	Update(authInfo *auth.AuthInfo, id string, conn *models.McpConnection) error

	// UpdateIsActive 更新连接启用状态
	UpdateIsActive(authInfo *auth.AuthInfo, id string, isActive bool) error

	// Delete 删除连接（级联删除工具）
	Delete(authInfo *auth.AuthInfo, id string) error

	// List 列出连接（分页 + 过滤）
	List(authInfo *auth.AuthInfo, req *ListMcpConnectionsRequest) ([]models.McpConnection, int, error)

	// ListTools 列出某连接下已持久化的工具
	ListTools(authInfo *auth.AuthInfo, connectionID string) ([]models.McpConnectionTool, error)

	// ReplaceTools 全量替换某连接下的工具（先删后插），用于发现后同步
	ReplaceTools(authInfo *auth.AuthInfo, connectionID string, tools []*models.McpConnectionTool) error
}
