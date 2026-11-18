package client

import (
	"context"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/rpcconnections"
	"flame/tools/xsql"
)

// RefreshToolsWithRepo 建立与远端 RPC server 的连接，发现其工具列表（tools/list），并全量同步到本地
// rpc_connection_tools 表。通常在连接 is_active 由 false 变 true 时调用。
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

// DiscoverTools 仅发现工具而不持久化（用于测试或预览）。
func DiscoverTools(ctx context.Context, conn *models.RpcConnection) ([]*models.RpcConnectionTool, error) {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return session.ListTools(ctx)
}
