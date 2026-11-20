package rpcconnections

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListRpcConnectionsRequest holds the RPC connection list parameters (paging + filters).
type ListRpcConnectionsRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	Transport string   `json:"transport" form:"transport"`
	IsActive  *bool    `json:"is_active" form:"is_active"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// Repository is the RPC connection repository interface.
type Repository interface {
	// GetByID returns a connection by id.
	GetByID(authInfo *auth.AuthInfo, id string) (*models.RpcConnection, error)

	// Create creates a connection and returns the new record id (the id is generated in the program, not by a database
	// default).
	Create(authInfo *auth.AuthInfo, conn *models.RpcConnection) (string, error)

	// Update updates a connection by id.
	Update(authInfo *auth.AuthInfo, id string, conn *models.RpcConnection) error

	// UpdateIsActive updates a connection's enabled state.
	UpdateIsActive(authInfo *auth.AuthInfo, id string, isActive bool) error

	// Delete deletes a connection (its tools cascade).
	Delete(authInfo *auth.AuthInfo, id string) error

	// List lists connections (paging + filters).
	List(authInfo *auth.AuthInfo, req *ListRpcConnectionsRequest) ([]models.RpcConnection, int, error)

	// ListTools lists the tools already persisted for a connection.
	ListTools(authInfo *auth.AuthInfo, connectionID string) ([]models.RpcConnectionTool, error)

	// ReplaceTools replaces a connection's tools wholesale (delete, then insert), for syncing after discovery.
	ReplaceTools(authInfo *auth.AuthInfo, connectionID string, tools []*models.RpcConnectionTool) error
}
