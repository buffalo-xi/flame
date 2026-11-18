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
)

// InvokeError 携带 flow 错误码的调用错误，便于上层按类型分类处理。
type InvokeError struct {
	Code string // flow.ErrCodeTimeout / ErrCodeCancel / ErrCodeAuth / ErrCodeConfig / ErrCodeConnection / ErrCodeOther
	Err  error
}

func (e *InvokeError) Error() string { return e.Err.Error() }
func (e *InvokeError) Unwrap() error { return e.Err }

// classifyInvokeError 将 Invoke 返回的错误映射到 flow 错误码。
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
	for _, k := range []string{"config", "unsupported transport", "invalid transport", "invalid url", "invalid argument", "unknown tool", "tool not found", "method not found", "not found", "disabled", "unsupported protocol version"} {
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
}

// Invoke 高层调用入口：根据连接配置调用远端 RPC 工具。
// - stdio：有状态子进程，复用进程池（sessionPool）以减少频繁启停开销。
// - streamable_http：无状态短连接，使用一次性建连。
// arguments 应为可 JSON 化的 map[string]any。
func Invoke(ctx context.Context, conn *models.RpcConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	var session *ClientSession
	var err error
	closeSession := false

	if strings.EqualFold(conn.Transport, "stdio") {
		session, err = sessionPool.Get(ctx, conn)
		if err != nil {
			return nil, classifyInvokeError(err)
		}
	} else {
		session, err = NewClientSession(ctx, conn)
		if err != nil {
			return nil, classifyInvokeError(err)
		}
		closeSession = true
	}

	result, err := callTool(ctx, session, toolName, arguments)
	if closeSession {
		_ = session.Close()
	}
	if err != nil {
		return nil, classifyInvokeError(err)
	}
	return result, nil
}

// InvokeWithProgress 调用远端 RPC 工具，并订阅执行期间的 progress 通知。
// onProgress 会被边推边调多次触发（与阻塞在 tools/call 上的主协程并发）。
//
// 订阅的方式与 MCP 一致：在 params._meta.progressToken 里放一个本次调用唯一的 token，
// 服务端据此决定是否推 notifications/progress（没有 token 就一帧都不该推）。
// stdio 会话本身串行（一次一个 in-flight 请求），所以收到通知即可直接透传回调，
// 不需要像 MCP stdio 那样按 token 建路由表。
//
// 注意 onProgress 可能一次都不被调用：对端可能是尚未实现该字段的旧服务端
// （它会忽略 _meta），也可能执行太快没推送。这不是错误。
func InvokeWithProgress(ctx context.Context, conn *models.RpcConnection, toolName string, arguments map[string]any, onProgress func(*ProgressNotificationParams)) (*ToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}

	var session *ClientSession
	var err error
	closeSession := false

	if strings.EqualFold(conn.Transport, "stdio") {
		session, err = sessionPool.Get(ctx, conn)
		if err != nil {
			return nil, classifyInvokeError(err)
		}
	} else {
		session, err = NewClientSession(ctx, conn)
		if err != nil {
			return nil, classifyInvokeError(err)
		}
		closeSession = true
	}

	raw, err := session.CallWithProgress(ctx, "tools/call",
		callParams(toolName, arguments, uuid.NewString(), callchain.From(ctx)), onProgress)
	if closeSession {
		_ = session.Close()
	}
	if err != nil {
		return nil, classifyInvokeError(err)
	}
	return flattenResult(raw)
}

// callParams 构造 tools/call 的 params（与 MCP 同一形状）。
//
// _meta 只承载控制意图，不承载业务参数：
//   - progressToken 非空时放进 params._meta —— 订阅进度只由这一个字段表达，
//     不再有 arguments.stream 之类的开关（它会把控制意图混进业务入参）；
//   - callChain 是本进程的祖先链（ctx 携带），**原样带出、不追加自身**：
//     对端据此判断「自己是否已在链上」，入站那一侧才负责追加。
func callParams(toolName string, arguments map[string]any, progressToken any, callChain []string) map[string]any {
	if arguments == nil {
		arguments = map[string]any{}
	}
	p := map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}
	meta := make(map[string]any, 2)
	if progressToken != nil {
		meta["progressToken"] = progressToken
	}
	if len(callChain) > 0 {
		meta[callchain.MetaKey] = callChain
	}
	if len(meta) > 0 {
		p["_meta"] = meta
	}
	return p
}

// callTool 在会话上调用 tools/call 并展平结果（不订阅进度）。
func callTool(ctx context.Context, session *ClientSession, toolName string, arguments map[string]any) (*ToolResult, error) {
	raw, err := session.Call(ctx, "tools/call", callParams(toolName, arguments, nil, callchain.From(ctx)))
	if err != nil {
		return nil, fmt.Errorf("调用工具 %s 失败: %w", toolName, err)
	}
	return flattenResult(raw)
}

// flattenResult 将 tools/call 的 result 展平为文本与结构化内容。
// result 结构对齐 MCP CallToolResult：content 恒为数组 [{type,text}] + isError。
func flattenResult(raw json.RawMessage) (*ToolResult, error) {
	var r struct {
		Content           []json.RawMessage `json:"content"`
		IsError           bool              `json:"isError"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析工具结果失败: %w", err)
	}

	out := &ToolResult{IsError: r.IsError}
	var sb strings.Builder
	for _, c := range r.Content {
		var item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(c, &item); err != nil {
			continue
		}
		if item.Type == "text" {
			sb.WriteString(item.Text)
		} else {
			sb.Write(c)
		}
	}
	out.Content = sb.String()
	if len(r.StructuredContent) > 0 {
		var structured any
		if err := json.Unmarshal(r.StructuredContent, &structured); err == nil {
			out.Structured = structured
		}
	}
	return out, nil
}
