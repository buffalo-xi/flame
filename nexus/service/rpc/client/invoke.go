package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentcore "flame/nexus/service/agent/core"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// InvokeError carries a flow error code so callers can dispatch on the category.
type InvokeError struct {
	Code string // flow.ErrCodeTimeout / ErrCodeCancel / ErrCodeAuth / ErrCodeConfig / ErrCodeConnection / ErrCodeOther
	Err  error
}

func (e *InvokeError) Error() string { return e.Err.Error() }
func (e *InvokeError) Unwrap() error { return e.Err }

// classifyInvokeError maps an error returned by Invoke to a flow error code.
// Priority: cancel > timeout > auth > config > connection > other; an already classified
// InvokeError passes through unchanged.
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

// ToolResult is a flattened tool call result.
type ToolResult struct {
	// Content is the merged textual result.
	Content string
	// Structured is the structured result (when the server returns structuredContent).
	Structured any
	// IsError reports whether the call ended with an error.
	IsError bool
}

// Invoke is the high-level entry point: it calls a remote RPC tool according to the connection config.
//   - stdio: a stateful child process, reusing the process pool (sessionPool) to avoid start/stop churn;
//   - streamable_http: a stateless short-lived connection, opened per call.
//
// arguments must be a JSON-serializable map[string]any.
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

// InvokeWithProgress calls a remote RPC tool and subscribes to its progress notifications.
// onProgress fires repeatedly while the call runs (concurrently with the main goroutine blocked on tools/call).
//
// Subscription works the way MCP does it: params._meta.progressToken carries a token unique to this call,
// and the server uses it to decide whether to push notifications/progress (without a token it must push
// nothing at all). A stdio session is serialized (one in-flight request), so a received notification can
// be passed straight to the callback without the token-keyed routing table MCP stdio needs.
//
// Note that onProgress may never be called: the peer may be an older server that does not implement the
// field yet (it ignores _meta), or the execution may finish too fast to push anything. Neither is an error.
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
		callParams(toolName, arguments, uuid.NewString(), callchain.From(ctx), pinnedLanguage(ctx)), onProgress)
	if closeSession {
		_ = session.Close()
	}
	if err != nil {
		return nil, classifyInvokeError(err)
	}
	return flattenResult(raw)
}

// pinnedLanguage returns the language to send as params._meta.language, or "".
//
// Only a **pinned** value travels: that field means "the caller demands this language", so forwarding a resolved default
// would override the callee's own default (an English-default agent would pin every downstream agent to English). The
// informational variant stays inside the process, where a flow reads it as the builtin `language`.
func pinnedLanguage(ctx context.Context) string {
	if !agentcore.LanguagePinnedFrom(ctx) {
		return ""
	}
	tag, _ := agentcore.NormalizeOutputLanguage(agentcore.LanguageHintFrom(ctx))
	return tag
}

// callParams builds the params of tools/call (same shape as MCP).
//
// _meta carries control intent only, never business arguments:
//   - a non-empty progressToken goes into params._meta — progress subscription is expressed by that
//     single field, there is no switch such as arguments.stream any more (it mixed control intent into
//     the business input);
//   - callChain is the ancestor chain of this process (carried by ctx) and is **passed through as is,
//     without appending ourselves**: the peer uses it to tell whether it is already on the chain, and
//     the inbound side is the one that appends;
//   - language is the caller-pinned output language (see pinnedLanguage); the peer treats it as a pin,
//     the same way an MCP / JSON-RPC client would.
func callParams(toolName string, arguments map[string]any, progressToken any, callChain []string, language string) map[string]any {
	if arguments == nil {
		arguments = map[string]any{}
	}
	p := map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}
	meta := make(map[string]any, 3)
	if progressToken != nil {
		meta["progressToken"] = progressToken
	}
	if len(callChain) > 0 {
		meta[callchain.MetaKey] = callChain
	}
	if language != "" {
		meta[agentcore.MetaLanguageKey] = language
	}
	if len(meta) > 0 {
		p["_meta"] = meta
	}
	return p
}

// callTool calls tools/call on the session and flattens the result (no progress subscription).
func callTool(ctx context.Context, session *ClientSession, toolName string, arguments map[string]any) (*ToolResult, error) {
	raw, err := session.Call(ctx, "tools/call", callParams(toolName, arguments, nil, callchain.From(ctx), pinnedLanguage(ctx)))
	if err != nil {
		return nil, fmt.Errorf("failed to call tool %s: %w", toolName, err)
	}
	return flattenResult(raw)
}

// flattenResult flattens the tools/call result into text and structured content.
// The result follows MCP CallToolResult: content is always an array of {type,text} plus isError.
func flattenResult(raw json.RawMessage) (*ToolResult, error) {
	var r struct {
		Content           []json.RawMessage `json:"content"`
		IsError           bool              `json:"isError"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("failed to parse the tool result: %w", err)
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
