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

// newTestMcpServerURL starts a throwaway MCP server exposing only the given protocol versions
// (stateless transport).
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

// TestConnectSessionNewProtocol: the server speaks the same 2026-07-28 as this side, so connecting succeeds.
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

// TestConnectSessionLegacyServer: when the peer only supports the old protocol the SDK silently
// negotiates down; connectSession must turn that into a configuration error (the old protocol does not
// enter nexus).
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
