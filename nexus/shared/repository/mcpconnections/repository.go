package mcpconnections

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListMcpConnectionsRequest holds the MCP connection list parameters (paging + filters).
type ListMcpConnectionsRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	Transport string   `json:"transport" form:"transport"`
	IsActive  *bool    `json:"is_active" form:"is_active"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// Repository is the MCP connection repository interface.
type Repository interface {
	// GetByID returns a connection by id.
	GetByID(authInfo *auth.AuthInfo, id string) (*models.McpConnection, error)

	// Create creates a connection and returns the new record id.
	Create(authInfo *auth.AuthInfo, conn *models.McpConnection) (string, error)

	// Update updates a connection by id.
	Update(authInfo *auth.AuthInfo, id string, conn *models.McpConnection) error

	// UpdateIsActive updates a connection's enabled state.
	UpdateIsActive(authInfo *auth.AuthInfo, id string, isActive bool) error

	// Delete deletes a connection (its tools cascade).
	Delete(authInfo *auth.AuthInfo, id string) error

	// List lists connections (paging + filters).
	List(authInfo *auth.AuthInfo, req *ListMcpConnectionsRequest) ([]models.McpConnection, int, error)

	// ListTools lists the tools already persisted for a connection.
	ListTools(authInfo *auth.AuthInfo, connectionID string) ([]models.McpConnectionTool, error)

	// ReplaceTools replaces a connection's tools wholesale (delete, then insert), for syncing after discovery.
	ReplaceTools(authInfo *auth.AuthInfo, connectionID string, tools []*models.McpConnectionTool) error
}
