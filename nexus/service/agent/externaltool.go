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

// externalToolRunner 把「外部工具名」翻译成一次真实的远程调用。
//
// 归属：外部工具的执行依赖连接仓储与 MCP/RPC 客户端，这两样放进 tools 包会把
// SDK 与表结构带进工具层；而 tools 包又不能反向 import 本包（会成环）。
// 所以执行器实现在这里、接口定义在 tools（同 PluginRunner 的做法）。
//
// 只覆盖 mcp__ / rpc__：local__（本租户端点）要进程内跑 flow，走的是
// localEndpointRunner（见 localendpoint.go），两者前缀不重叠、职责不重叠。
// 连接目录按运行实例缓存（mcpConns / rpcConns）：一次循环里可能多次调外部工具，
// 每次 List 全表是纯浪费；同一轮内连接配置不变，快照语义与「本轮工具目录在开跑时
// 冻结」一致（授权闸用的是同一份目录快照）。
type externalToolRunner struct {
	mu       sync.Mutex
	mcpConns []models.McpConnection
	rpcConns []models.RpcConnection
}

var _ tools.ExternalRunner = (*externalToolRunner)(nil)

// externalListLimit 列连接时的上限（远大于实际数量，避免分页默认值截断）。
// 与 toolgrant.Catalog 的 listLimit 同口径。
const externalListLimit = 1000

func (*externalToolRunner) Has(name string) bool {
	return strings.HasPrefix(name, tools.ExternalPrefixMCP) ||
		strings.HasPrefix(name, tools.ExternalPrefixRPC)
}

func (r *externalToolRunner) Invoke(ctx context.Context, schema, toolName string, args map[string]any, maxOutput int) (any, error) {
	if schema == "" {
		return nil, fmt.Errorf("工具 %s 属于外部连接，但缺少租户上下文", toolName)
	}
	authInfo := &auth.AuthInfo{Schema: schema}

	switch {
	case strings.HasPrefix(toolName, tools.ExternalPrefixMCP):
		conns, err := r.mcpConnections(authInfo)
		if err != nil {
			return nil, fmt.Errorf("查询 MCP 连接失败: %w", err)
		}
		return invokeMcpTool(ctx, authInfo, conns, toolName, args, maxOutput)
	case strings.HasPrefix(toolName, tools.ExternalPrefixRPC):
		conns, err := r.rpcConnections(authInfo)
		if err != nil {
			return nil, fmt.Errorf("查询 RPC 连接失败: %w", err)
		}
		return invokeRpcTool(ctx, authInfo, conns, toolName, args, maxOutput)
	}
	// Has 与这里的前缀判据必须一致：否则 Has 放行、Invoke 走空 —— 模型只会看到空结果。
	return nil, fmt.Errorf("工具 %s 不是可执行的外部连接工具", toolName)
}

// mcpConnections 返回本次运行的 MCP 连接快照（首次调用查库，之后复用）。
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

// invokeMcpTool 解析 mcp__<连接>__<工具> 并调用。
func invokeMcpTool(ctx context.Context, authInfo *auth.AuthInfo, list []models.McpConnection, name string, args map[string]any, maxOutput int) (any, error) {
	connNames := make([]string, 0, len(list))
	byName := make(map[string]*models.McpConnection, len(list))
	for i := range list {
		connNames = append(connNames, list[i].Name)
		byName[strings.ToLower(list[i].Name)] = &list[i]
	}

	connName, remoteTool, ok := splitExternalName(name, tools.ExternalPrefixMCP, connNames)
	if !ok {
		return nil, fmt.Errorf("工具 %s 找不到对应的 MCP 连接（连接可能已被删除或改名，请让管理员重新授权）", name)
	}
	conn := byName[strings.ToLower(connName)]
	if conn == nil {
		return nil, fmt.Errorf("MCP 连接 %q 不存在", connName)
	}
	if !conn.IsActive {
		return nil, fmt.Errorf("MCP 连接 %q 未启用，请在「设置 → MCP 连接」中启用后重试", connName)
	}

	res, err := mcpclient.InvokeSync(ctx, conn, remoteTool, args)
	if err != nil {
		// 掐断（超时 / 终止）后必须丢掉池里的会话：stdio 会话是串行的，
		// 被掐断的那次请求可能还挂在里面，留着会让后续同类调用一起超时。
		// 只在异常路径生效，正常调用照旧复用子进程。
		if ctx.Err() != nil && strings.EqualFold(conn.Transport, "stdio") {
			mcpclient.GetSessionPool().Invalidate(conn.ID)
		}
		return nil, fmt.Errorf("MCP 工具 %s 调用失败: %w", remoteTool, err)
	}
	xlog.Info("外部工具调用 schema=%s conn=%s tool=%s is_error=%v", authInfo.Schema, connName, remoteTool, res.IsError)
	return externalResult(res.Content, res.Structured, res.IsError, maxOutput), nil
}

// rpcConnections 返回本次运行的 RPC 连接快照（首次调用查库，之后复用）。
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

// invokeRpcTool 解析 rpc__<连接>__<工具> 并调用。
func invokeRpcTool(ctx context.Context, authInfo *auth.AuthInfo, list []models.RpcConnection, name string, args map[string]any, maxOutput int) (any, error) {
	connNames := make([]string, 0, len(list))
	byName := make(map[string]*models.RpcConnection, len(list))
	for i := range list {
		connNames = append(connNames, list[i].Name)
		byName[strings.ToLower(list[i].Name)] = &list[i]
	}

	connName, remoteTool, ok := splitExternalName(name, tools.ExternalPrefixRPC, connNames)
	if !ok {
		return nil, fmt.Errorf("工具 %s 找不到对应的 RPC 连接（连接可能已被删除或改名，请让管理员重新授权）", name)
	}
	conn := byName[strings.ToLower(connName)]
	if conn == nil {
		return nil, fmt.Errorf("RPC 连接 %q 不存在", connName)
	}
	if !conn.IsActive {
		return nil, fmt.Errorf("RPC 连接 %q 未启用，请在「设置 → RPC 连接」中启用后重试", connName)
	}

	res, err := rpcclient.Invoke(ctx, conn, remoteTool, args)
	if err != nil {
		if ctx.Err() != nil && strings.EqualFold(conn.Transport, "stdio") {
			rpcclient.GetSessionPool().Invalidate(conn.ID)
		}
		return nil, fmt.Errorf("RPC 工具 %s 调用失败: %w", remoteTool, err)
	}
	xlog.Info("外部工具调用 schema=%s conn=%s tool=%s is_error=%v", authInfo.Schema, connName, remoteTool, res.IsError)
	return externalResult(res.Content, res.Structured, res.IsError, maxOutput), nil
}

// splitExternalName 把 mcp__<连接>__<工具> 拆成连接名与远端工具名。
//
// 不能按 "__" 盲切：连接名允许含下划线（见 toolgrant 的 namePattern），
// 形如 fs__prod 的连接会让切分点落到名字内部。连接名在租户内唯一，
// 所以取「最长的可匹配连接名」即正确切分点；大小写按不敏感比对处理，
// 与目录侧（Catalog.toolsOfConnection 的 EqualFold）同口径。
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
		// len(rest) 必须严格大于 head：工具名至少一个字符。
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

// externalResult 把远程结果整理成给模型的形状。
//
// is_error=true 仍然返回结果、不返回 Go error：与工作流 mcp/rpc 节点的口径一致
// （业务错误不等于调用异常），模型看得到错误正文才有机会自我纠正；
// 连接失败 / 未启用才上升为 error。
//
// 截断：外部工具常直接吐整份文件或长列表，不设上限会一次吃掉整个上下文。
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

// clampText 按字节上限截断，且不切在 UTF-8 字符中间（半个汉字在 JSON 里是坏字符）。
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
