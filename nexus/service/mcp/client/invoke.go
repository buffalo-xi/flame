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

// InvokeError carries a flow error code so callers can dispatch on the category.
type InvokeError struct {
	Code string // flow.ErrCodeTimeout / ErrCodeCancel / ErrCodeAuth / ErrCodeConfig / ErrCodeConnection / ErrCodeOther
	Err  error
}

func (e *InvokeError) Error() string { return e.Err.Error() }
func (e *InvokeError) Unwrap() error { return e.Err }

// classifyInvokeError maps an error returned by InvokeWithProgress to a flow error code.
// Priority: cancel > timeout > auth > config > connection > other; an already classified InvokeError
// passes through unchanged.
func classifyInvokeError(err error) error {
	if err == nil {
		return nil
	}
	var ie *InvokeError
	if errors.As(err, &ie) {
		return err // already classified, pass through
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

// ToolResult is a flattened tool call result.
type ToolResult struct {
	// Content is the merged textual result.
	Content string
	// Structured is the structured result (when the server returns structuredContent).
	Structured any
	// IsError reports whether the call ended with an error.
	IsError bool
	// Raw is the original SDK result, for callers that need more.
	Raw *mcp.CallToolResult
}

// CallToolOnce opens a one-shot connection and calls a remote tool (suited to low-frequency use without
// a pool). arguments must be a JSON-serializable map[string]any.
func CallToolOnce(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := NewClientSession(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return callToolOnSession(ctx, session, toolName, arguments)
}

// Invoke is the high-level entry point: it calls a tool through an existing session or straight from
// the connection config. The session passed in is preferred when it is not nil, otherwise a connection
// is opened on the fly.
//   - streamable_http: a stateless short-lived connection, opened per call (CallToolOnce);
//   - stdio: a stateful child process, reusing the process pool (sessionPool) to avoid start/stop churn.
func Invoke(ctx context.Context, conn *models.McpConnection, session *mcp.ClientSession, toolName string, arguments map[string]any) (*ToolResult, error) {
	if session != nil {
		return callToolOnSession(ctx, session, toolName, arguments)
	}
	if strings.EqualFold(conn.Transport, "stdio") {
		return CallToolPooled(ctx, conn, toolName, arguments)
	}
	return CallToolOnce(ctx, conn, toolName, arguments)
}

// CallToolPooled calls a stdio tool through the process pool (sessionPool): the same child process is
// reused and not closed here.
func CallToolPooled(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := sessionPool.Get(ctx, conn)
	if err != nil {
		return nil, err
	}
	return callToolOnSession(ctx, session, toolName, arguments)
}

// InvokeWithProgress calls a remote tool and streams its progress notifications.
// onProgress fires repeatedly from the SDK's background read goroutine (concurrently with the main
// goroutine blocked on CallTool). Dispatch by transport:
//   - stdio: reuse the process pool, set a progressToken and register it in the pool's routing table,
//     so the dispatcher injected by newStdioSession routes frames back by token;
//   - streamable_http: a one-shot connection with onProgress bound directly.
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

// InvokeSync calls a remote tool without subscribing to progress (no progressToken).
//
// The server therefore pushes no notifications/progress — progress is "the client asks, the server
// gives", not a property every endpoint has. The connection strategy matches InvokeWithProgress exactly
// (stdio reuses the pool, http opens a one-shot connection); the only difference is that no callback is
// attached and no token is sent.
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

// invokeStdioSync is the stdio branch: reuse the pool, register no progress route.
func invokeStdioSync(ctx context.Context, conn *models.McpConnection, toolName string, arguments map[string]any) (*ToolResult, error) {
	session, err := sessionPool.Get(ctx, conn)
	if err != nil {
		return nil, err
	}
	return callToolOnSession(ctx, session, toolName, arguments)
}

// invokeHTTPSync is the streamable_http branch: a one-shot connection with no ProgressNotificationHandler.
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

// invokeStdioWithProgress is the stdio branch: pool reuse plus token routing.
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

// invokeHTTPWithProgress is the streamable_http branch: a one-shot connection with onProgress bound directly.
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

// callToolOnSessionWithToken calls a tool on the session with a progressToken set and flattens the result.
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
		return nil, fmt.Errorf("failed to call tool %s: %w", toolName, err)
	}
	return flattenResult(result), nil
}

// callToolOnSession calls a tool on an existing session and flattens the result.
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
		return nil, fmt.Errorf("failed to call tool %s: %w", toolName, err)
	}
	return flattenResult(result), nil
}

// withCallChain writes the ancestor chain into params._meta.call_chain (the same key as on the RPC side).
//
// Outbound only carries it, never appends: appending happens in the server that is about to execute an
// entity — otherwise the chain would contain duplicates of itself and both logs and error messages
// would get dirty.
func withCallChain(params *mcp.CallToolParams, chain []string) {
	if params == nil || len(chain) == 0 {
		return
	}
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[callchain.MetaKey] = chain
}

// flattenResult flattens the SDK CallToolResult into text and structured content.
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
