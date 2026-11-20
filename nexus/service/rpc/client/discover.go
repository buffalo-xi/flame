package client

import (
	"context"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/rpcconnections"
	"flame/tools/xsql"
)

// RefreshToolsWithRepo connects to the remote RPC server, discovers its tool list (tools/list) and
// replaces the local rpc_connection_tools rows with it. Usually called when a connection's is_active
// flips from false to true.
func RefreshToolsWithRepo(ctx context.Context, authInfo *auth.AuthInfo, conn *models.RpcConnection, repo rpcconnections.Repository) error {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return err
	}
	defer session.Close()

	tools, err := session.ListTools(ctx)
	if err != nil {
		return err
	}

	for _, t := range tools {
		t.ConnectionID = conn.ID
		t.UpdatedAt = xsql.Time(time.Now())
	}
	return repo.ReplaceTools(authInfo, conn.ID, tools)
}

// DiscoverTools only discovers the tools without persisting them (for tests or previews).
func DiscoverTools(ctx context.Context, conn *models.RpcConnection) ([]*models.RpcConnectionTool, error) {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return session.ListTools(ctx)
}
