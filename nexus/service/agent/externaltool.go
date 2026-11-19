package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"flame/nexus/service/agent/tools"
	mcpclient "flame/nexus/service/mcp/client"
	rpcclient "flame/nexus/service/rpc/client"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	mcpconnrepo "flame/nexus/shared/repository/mcpconnections"
	rpcconnrepo "flame/nexus/shared/repository/rpcconnections"
	"flame/tools/xlog"
)

// externalToolRunner translates an "external tool name" into one real remote call.
//
// Ownership: executing an external tool needs the connection repositories and the MCP/RPC clients, and putting those in the tools package
// would drag the SDK and the table structure into the tool layer; the tools package cannot import this package back either (a cycle).
// So the runner lives here and the interface is defined in tools (the same approach as PluginRunner).
//
// It only covers mcp__ / rpc__: local__ (an endpoint of this tenant) has to run a flow in-process and goes through localEndpointRunner
// (see localendpoint.go); the prefixes do not overlap and neither do the responsibilities.
// The connection catalog is cached per run instance (mcpConns / rpcConns): one loop may call external tools several times, and listing the
// whole table every time is pure waste; connection config does not change within a round, so the snapshot semantics match "this round's tool
// catalog is frozen when the run starts" (the grant gate uses the same catalog snapshot).
type externalToolRunner struct {
	mu       sync.Mutex
	mcpConns []models.McpConnection
	rpcConns []models.RpcConnection
}

var _ tools.ExternalRunner = (*externalToolRunner)(nil)

// externalListLimit is the cap when listing connections (far above the real count, so the pagination default cannot truncate it).
// The same criterion as toolgrant.Catalog's listLimit.
const externalListLimit = 1000

func (*externalToolRunner) Has(name string) bool {
	return strings.HasPrefix(name, tools.ExternalPrefixMCP) ||
		strings.HasPrefix(name, tools.ExternalPrefixRPC)
}

func (r *externalToolRunner) Invoke(ctx context.Context, schema, toolName string, args map[string]any, maxOutput int) (any, error) {
	if schema == "" {
		return nil, fmt.Errorf("tool %s is an external connection, but the tenant context is missing", toolName)
	}
	authInfo := &auth.AuthInfo{Schema: schema}

	switch {
	case strings.HasPrefix(toolName, tools.ExternalPrefixMCP):
		conns, err := r.mcpConnections(authInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to query MCP connections: %w", err)
		}
		return invokeMcpTool(ctx, authInfo, conns, toolName, args, maxOutput)
	case strings.HasPrefix(toolName, tools.ExternalPrefixRPC):
		conns, err := r.rpcConnections(authInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to query RPC connections: %w", err)
		}
		return invokeRpcTool(ctx, authInfo, conns, toolName, args, maxOutput)
	}
	// The prefix criterion of Has and of this switch must agree: otherwise Has lets a name through while Invoke falls through empty - the
	// model just sees an empty result.
	return nil, fmt.Errorf("tool %s is not an executable external connection tool", toolName)
}

// mcpConnections returns this run's MCP connection snapshot (a database read on the first call, reused afterwards).
func (r *externalToolRunner) mcpConnections(authInfo *auth.AuthInfo) ([]models.McpConnection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mcpConns != nil {
		return r.mcpConns, nil
	}
	list, _, err := mcpconnrepo.NewRepository().List(authInfo,
		&mcpconnrepo.ListMcpConnectionsRequest{Page: 1, Limit: externalListLimit})
	if err != nil {
		return nil, err
	}
	r.mcpConns = list
	return list, nil
}

// invokeMcpTool parses mcp__<connection>__<tool> and calls it.
func invokeMcpTool(ctx context.Context, authInfo *auth.AuthInfo, list []models.McpConnection, name string, args map[string]any, maxOutput int) (any, error) {
	connNames := make([]string, 0, len(list))
	byName := make(map[string]*models.McpConnection, len(list))
	for i := range list {
		connNames = append(connNames, list[i].Name)
		byName[strings.ToLower(list[i].Name)] = &list[i]
	}

	connName, remoteTool, ok := splitExternalName(name, tools.ExternalPrefixMCP, connNames)
	if !ok {
		return nil, fmt.Errorf("tool %s has no matching MCP connection (the connection may have been deleted or renamed; ask an administrator to grant it again)", name)
	}
	conn := byName[strings.ToLower(connName)]
	if conn == nil {
		return nil, fmt.Errorf("MCP connection %q does not exist", connName)
	}
	if !conn.IsActive {
		return nil, fmt.Errorf("MCP connection %q is not enabled; enable it under \"Settings -> MCP connections\" and retry", connName)
	}

	res, err := mcpclient.InvokeSync(ctx, conn, remoteTool, args)
	if err != nil {
		// After a cut-off (timeout / termination) the pooled session must be dropped: stdio sessions are serial, the cut request may still be
		// stuck inside, and keeping it would make later calls of the same kind time out too. Only on the error path; normal calls keep
		// reusing the child process.
		if ctx.Err() != nil && strings.EqualFold(conn.Transport, "stdio") {
			mcpclient.GetSessionPool().Invalidate(conn.ID)
		}
		return nil, fmt.Errorf("calling MCP tool %s failed: %w", remoteTool, err)
	}
	xlog.Info("external tool call schema=%s conn=%s tool=%s is_error=%v", authInfo.Schema, connName, remoteTool, res.IsError)
	return externalResult(res.Content, res.Structured, res.IsError, maxOutput), nil
}

// rpcConnections returns this run's RPC connection snapshot (a database read on the first call, reused afterwards).
func (r *externalToolRunner) rpcConnections(authInfo *auth.AuthInfo) ([]models.RpcConnection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rpcConns != nil {
		return r.rpcConns, nil
	}
	list, _, err := rpcconnrepo.NewRepository().List(authInfo,
		&rpcconnrepo.ListRpcConnectionsRequest{Page: 1, Limit: externalListLimit})
	if err != nil {
		return nil, err
	}
	r.rpcConns = list
	return list, nil
}

// invokeRpcTool parses rpc__<connection>__<tool> and calls it.
func invokeRpcTool(ctx context.Context, authInfo *auth.AuthInfo, list []models.RpcConnection, name string, args map[string]any, maxOutput int) (any, error) {
	connNames := make([]string, 0, len(list))
	byName := make(map[string]*models.RpcConnection, len(list))
	for i := range list {
		connNames = append(connNames, list[i].Name)
		byName[strings.ToLower(list[i].Name)] = &list[i]
	}

	connName, remoteTool, ok := splitExternalName(name, tools.ExternalPrefixRPC, connNames)
	if !ok {
		return nil, fmt.Errorf("tool %s has no matching RPC connection (the connection may have been deleted or renamed; ask an administrator to grant it again)", name)
	}
	conn := byName[strings.ToLower(connName)]
	if conn == nil {
		return nil, fmt.Errorf("RPC connection %q does not exist", connName)
	}
	if !conn.IsActive {
		return nil, fmt.Errorf("RPC connection %q is not enabled; enable it under \"Settings -> RPC connections\" and retry", connName)
	}

	res, err := rpcclient.Invoke(ctx, conn, remoteTool, args)
	if err != nil {
		if ctx.Err() != nil && strings.EqualFold(conn.Transport, "stdio") {
			rpcclient.GetSessionPool().Invalidate(conn.ID)
		}
		return nil, fmt.Errorf("calling RPC tool %s failed: %w", remoteTool, err)
	}
	xlog.Info("external tool call schema=%s conn=%s tool=%s is_error=%v", authInfo.Schema, connName, remoteTool, res.IsError)
	return externalResult(res.Content, res.Structured, res.IsError, maxOutput), nil
}

// splitExternalName splits mcp__<connection>__<tool> into the connection name and the remote tool name.
//
// It cannot blindly cut at "__": connection names may contain underscores (see toolgrant's namePattern), and a connection like fs__prod would
// put the split point inside the name. Connection names are unique within a tenant, so "the longest matching connection name" is the correct
// split point; comparison is case-insensitive, the same criterion as the catalog side (EqualFold in Catalog.toolsOfConnection).
func splitExternalName(name, prefix string, connNames []string) (conn, tool string, ok bool) {
	rest := strings.TrimPrefix(name, prefix)
	if rest == name || rest == "" {
		return "", "", false
	}
	for _, cn := range connNames {
		if cn == "" {
			continue
		}
		head := cn + "__"
		// len(rest) must be strictly greater than head: a tool name has at least one character.
		if len(rest) <= len(head) {
			continue
		}
		if !strings.EqualFold(rest[:len(head)], head) {
			continue
		}
		if len(cn) > len(conn) {
			conn, tool = cn, rest[len(head):]
		}
	}
	if conn == "" || tool == "" {
		return "", "", false
	}
	return conn, tool, true
}

// externalResult shapes a remote result for the model.
//
// is_error=true still returns the result and no Go error: the same convention as the mcp/rpc nodes of a workflow (a business error is not a
// call failure), and only by seeing the error body can the model correct itself; connection failures / disabled connections are raised as
// errors.
//
// Truncation: external tools often dump a whole file or a long list, and without a cap it would eat the entire context in one go.
func externalResult(content string, structured any, isError bool, maxOutput int) map[string]any {
	out := map[string]any{
		"content":  content,
		"is_error": isError,
	}
	if structured != nil {
		out["structured"] = structured
	}
	if clamped, truncated := clampText(content, maxOutput); truncated {
		out["content"] = clamped
		out["truncated"] = true
	}
	return out
}

// clampText truncates by the byte limit without cutting in the middle of a UTF-8 character (half a Chinese character is invalid in JSON).
func clampText(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}
