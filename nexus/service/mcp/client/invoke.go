package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// InvokeError 携带 flow 错误码的调用错误，便于上层按类型分类处理。
type InvokeError struct {
	Code string // flow.ErrCodeTimeout / ErrCodeCancel / ErrCodeAuth / ErrCodeConfig / ErrCodeConnection / ErrCodeOther
	Err  error
}

func (e *InvokeError) Error() string { return e.Err.Error() }
func (e *InvokeError) Unwrap() error { return e.Err }

// classifyInvokeError 将 InvokeWithProgress 返回的错误映射到 flow 错误码。
// 优先级：cancel > timeout > auth > config > connection > other；已分类的 InvokeError 透传。
func classifyInvokeError(err error) error {
	if err == nil {
		return nil
	}
	var ie *InvokeError
	if errors.As(err, &ie) {
		return err // 已分类，透传
	}
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return &InvokeError{Code: flow.ErrCodeCancel, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &InvokeError{Code: flow.ErrCodeTimeout, Err: err}
	case isAuthError(msg):
		return &InvokeError{Code: flow.ErrCodeAuth, Err: err}
	case isConfigError(msg):
		return &InvokeError{Code: flow.ErrCodeConfig, Err: err}
	case isConnectionError(msg):
		return &InvokeError{Code: flow.ErrCodeConnection, Err: err}
	default:
		return &InvokeError{Code: flow.ErrCodeOther, Err: err}
	}
}

func isAuthError(msg string) bool {
	l := strings.ToLower(msg)
	for _, k := range []string{"unauthorized", "authentication", "401", "403", "invalid token", "oauth", "forbidden", "access denied"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func isConfigError(msg string) bool {
	l := strings.ToLower(msg)
	for _, k := range []string{"config", "unsupported transport", "invalid transport", "invalid url", "invalid argument", "unknown tool", "tool not found", "method not found", "not found"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func isConnectionError(msg string) bool {
	l := strings.ToLower(msg)
	for _, k := range []string{"connection", "connect", "dial", "refused", "timeout", "unreachable", "network", "eof", "closed", "reset", "no such host", "dns"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// ToolResult 工具调用结果（已展平）
type ToolResult struct {
	// Content 文本形式的合并结果
	Content string
	// Structured 结构化结果（若 server 返回 structuredContent）
	Structured any
	// IsError 调用是否以错误结束
	IsError bool
	// Raw 原始 SDK 结果，便于上层按需处理
	Raw *mcp.CallToolResult
}

// CallToolOnce 建立一次性连接并调用远端工具（适合低频、无连接池场景）。
// arguments 应为可 JSON 化的 map[string]any。
func CallToolOnce(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return callToolOnSession(ctx, session, toolName, arguments)
}

// Invoke 高层调用入口：根据已建立会话或直接基于连接配置调用工具。
// 本函数优先复用传入的 session（若非 nil），否则临时建连。
// - streamable_http：无状态短连接，使用一次性建连（CallToolOnce）。
// - stdio：有状态子进程，复用进程池（sessionPool）以减少频繁启停开销。
func Invoke(ctx context.Context, conn *models.McpConnection, session *mcp.ClientSession, toolName string, arguments map[string]any) (*ToolResult, error) {
	if session != nil {
		return callToolOnSession(ctx, session, toolName, arguments)
	}
	if strings.EqualFold(conn.Transport, "stdio") {
		return CallToolPooled(ctx, conn, toolName, arguments)
	}
	return CallToolOnce(ctx, conn, toolName, arguments)
}

// CallToolPooled 基于进程池（sessionPool）调用 stdio 工具：复用同一子进程，不主动关闭。
func CallToolPooled(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := sessionPool.Get(ctx, conn)
	if err != nil {
		return nil, err
	}
	return callToolOnSession(ctx, session, toolName, arguments)
}

// InvokeWithProgress 调用远端工具，并在执行期间流式接收 progress 通知。
// onProgress 会被 SDK 后台读协程边调边推多次触发（与阻塞在 CallTool 的主协程并发）。
// 按 transport 分流：
//   - stdio：复用进程池，设置 progressToken 并注册到池上的路由表，由 newStdioSession
//     注入的分发 handler 按 token 回传。
//   - streamable_http：一次性建连，直接绑定 onProgress。
func InvokeWithProgress(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any, onProgress func(*mcp.ProgressNotificationParams)) (*ToolResult, error) {
	var res *ToolResult
	var err error
	if strings.EqualFold(conn.Transport, "stdio") {
		res, err = invokeStdioWithProgress(ctx, conn, toolName, arguments, onProgress)
	} else {
		res, err = invokeHTTPWithProgress(ctx, conn, toolName, arguments, onProgress)
	}
	if err != nil {
		return nil, classifyInvokeError(err)
	}
	return res, nil
}

// InvokeSync 调用远端工具但不订阅进度（不设 progressToken）。
//
// 服务端据此不推 notifications/progress —— progress 是「客户端要，服务端才给」，
// 而不是端点自带的属性。连接策略与 InvokeWithProgress 完全一致
// （stdio 复用进程池、http 一次性建连），差别只是不挂回调、不带 token。
func InvokeSync(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	var (
		res *ToolResult
		err error
	)
	if strings.EqualFold(conn.Transport, "stdio") {
		res, err = invokeStdioSync(ctx, conn, toolName, arguments)
	} else {
		res, err = invokeHTTPSync(ctx, conn, toolName, arguments)
	}
	if err != nil {
		return nil, classifyInvokeError(err)
	}
	return res, nil
}

// invokeStdioSync stdio 分支：复用进程池，不注册进度路由。
func invokeStdioSync(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := sessionPool.Get(ctx, conn)
	if err != nil {
		return nil, err
	}
	return callToolOnSession(ctx, session, toolName, arguments)
}

// invokeHTTPSync streamable_http 分支：一次性建连，不挂 ProgressNotificationHandler。
func invokeHTTPSync(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	authHandler, err := BuildOAuthHandler(conn.Auth)
	if err != nil {
		return nil, err
	}
	transport, err := NewTransport(ctx, conn, authHandler)
	if err != nil {
		return nil, err
	}

	client := mcp.NewClient(ClientImpl, &mcp.ClientOptions{})
	session, err := connectSession(ctx, client, transport, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return callToolOnSession(ctx, session, toolName, arguments)
}

// invokeStdioWithProgress stdio 分支：复用进程池 + token 路由。
func invokeStdioWithProgress(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any, onProgress func(*mcp.ProgressNotificationParams)) (*ToolResult, error) {
	session, err := sessionPool.Get(ctx, conn)
	if err != nil {
		return nil, err
	}

	token := uuid.NewString()
	if onProgress != nil {
		sessionPool.RegisterProgress(token, onProgress)
		defer sessionPool.UnregisterProgress(token)
	}

	return callToolOnSessionWithToken(ctx, session, toolName, arguments, token)
}

// invokeHTTPWithProgress streamable_http 分支：一次性建连 + 直接绑 onProgress。
func invokeHTTPWithProgress(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any, onProgress func(*mcp.ProgressNotificationParams)) (*ToolResult, error) {
	authHandler, err := BuildOAuthHandler(conn.Auth)
	if err != nil {
		return nil, err
	}
	transport, err := NewTransport(ctx, conn, authHandler)
	if err != nil {
		return nil, err
	}

	opts := &mcp.ClientOptions{}
	if onProgress != nil {
		opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			onProgress(req.Params)
		}
	}

	client := mcp.NewClient(ClientImpl, opts)
	session, err := connectSession(ctx, client, transport, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	token := uuid.NewString()
	return callToolOnSessionWithToken(ctx, session, toolName, arguments, token)
}

// callToolOnSessionWithToken 在会话上调用工具并设置 progressToken，展平结果。
func callToolOnSessionWithToken(ctx context.Context, session *mcp.ClientSession, toolName string, arguments map[string]any, token string) (*ToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}
	params.SetProgressToken(token)
	withCallChain(params, callchain.From(ctx))
	result, err := session.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("调用工具 %s 失败: %w", toolName, err)
	}
	return flattenResult(result), nil
}

// callToolOnSession 在已有会话上调用工具并展平结果
func callToolOnSession(ctx context.Context, session *mcp.ClientSession, toolName string, arguments map[string]any) (*ToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}
	withCallChain(params, callchain.From(ctx))
	result, err := session.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("调用工具 %s 失败: %w", toolName, err)
	}
	return flattenResult(result), nil
}

// withCallChain 把祖先链写进 params._meta.call_chain（与 RPC 侧同一键名）。
//
// 出站只带不追加：追加发生在「即将执行某个实体」的服务端 ——
// 否则链里会出现重复的自己，日志与错误信息都会变脏。
func withCallChain(params *mcp.CallToolParams, chain []string) {
	if params == nil || len(chain) == 0 {
		return
	}
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[callchain.MetaKey] = chain
}

// flattenResult 将 SDK 的 CallToolResult 展平为文本与结构化内容
func flattenResult(result *mcp.CallToolResult) *ToolResult {
	out := &ToolResult{Raw: result, IsError: result.IsError}
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		} else if c != nil {
			if b, err := json.Marshal(c); err == nil {
				sb.Write(b)
			}
		}
	}
	out.Content = sb.String()
	out.Structured = result.StructuredContent
	return out
}
