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

// RefreshToolsWithRepo connects to the remote MCP server, discovers its tool list and replaces the
// local mcp_connection_tools rows with it. Usually called when a connection's is_active flips from
// false to true.
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

// DiscoverTools only discovers the tools without persisting them (for tests or previews).
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
