package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flame/nexus/service/mcp/mcpspec"
	"flame/nexus/shared/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestMcpServerURL 起一个只暴露指定协议版本的临时 MCP server（无状态 transport）。
func newTestMcpServerURL(t *testing.T, versions []string) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: versions,
	})
	srv.AddTool(&mcp.Tool{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestConnectSessionNewProtocol server 与本端同为 2026-07-28，正常建连。
func TestConnectSessionNewProtocol(t *testing.T) {
	url := newTestMcpServerURL(t, []string{mcpspec.ProtocolVersion})
	conn := &models.McpConnection{Name: "new", Transport: "streamable_http", URL: url}

	sess, err := connectSession(context.Background(), mcp.NewClient(ClientImpl, nil),
		&mcp.StreamableClientTransport{Endpoint: url}, conn)
	if err != nil {
		t.Fatalf("connectSession: %v", err)
	}
	defer sess.Close()

	if got := sess.InitializeResult().ProtocolVersion; got != mcpspec.ProtocolVersion {
		t.Fatalf("negotiated version = %q, want %q", got, mcpspec.ProtocolVersion)
	}
}

// TestConnectSessionLegacyServer 对端只支持旧协议时，SDK 会悄悄降级协商，
// connectSession 必须把它判成配置类错误（老协议不进 nexus）。
func TestConnectSessionLegacyServer(t *testing.T) {
	url := newTestMcpServerURL(t, []string{"2025-11-25"})
	conn := &models.McpConnection{Name: "legacy", Transport: "streamable_http", URL: url}

	sess, err := connectSession(context.Background(), mcp.NewClient(ClientImpl, nil),
		&mcp.StreamableClientTransport{Endpoint: url}, conn)
	if err == nil {
		sess.Close()
		t.Fatalf("expected version mismatch error, got session at %q", sess.InitializeResult().ProtocolVersion)
	}
}
