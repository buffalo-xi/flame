package client

import (
	"context"
	"encoding/json"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/mcpconnections"
	"flame/tools/xsql"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RefreshToolsWithRepo 建立与远端 MCP server 的连接，发现其工具列表，并全量同步到本地
// mcp_connection_tools 表。通常在连接 is_active 由 false 变 true 时调用。
func RefreshToolsWithRepo(ctx context.Context, authInfo *auth.AuthInfo, conn *models.McpConnection, repo mcpconnections.Repository) error {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return err
	}
	defer session.Close()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}

	tools := make([]*models.McpConnectionTool, 0, len(result.Tools))
	for _, t := range result.Tools {
		if t == nil {
			continue
		}
		var schema xsql.JSONB
		if t.InputSchema != nil {
			b, err := json.Marshal(t.InputSchema)
			if err == nil {
				schema = xsql.JSONB{Data: json.RawMessage(b)}
			}
		}
		tools = append(tools, &models.McpConnectionTool{
			ID:           uuid.NewString(),
			ConnectionID: conn.ID,
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  schema,
			UpdatedAt:    xsql.Time(time.Now()),
		})
	}
	return repo.ReplaceTools(authInfo, conn.ID, tools)
}

// DiscoverTools 仅发现工具而不持久化（用于测试或预览）。
func DiscoverTools(ctx context.Context, conn *models.McpConnection) ([]*models.McpConnectionTool, error) {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	var tools []*models.McpConnectionTool
	for _, t := range result.Tools {
		if t == nil {
			continue
		}
		tools = append(tools, &models.McpConnectionTool{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return tools, nil
}

// ensure mcp import used even if DiscoverTools path changes
var _ = mcp.TextContent{}
