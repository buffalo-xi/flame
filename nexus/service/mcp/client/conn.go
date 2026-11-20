package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"flame/nexus/service/mcp/mcpspec"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientImpl is the implementation info of this MCP client (sent in the Initialize handshake).
var ClientImpl = &mcp.Implementation{Name: "flame-nexus", Version: "1.0.0"}

// NewClientSession opens a session with the remote MCP server according to the connection config
// (already connected). The caller is responsible for calling session.Close() once it is done.
func NewClientSession(ctx context.Context, conn *models.McpConnection) (*mcp.ClientSession, error) {
	authHandler, err := BuildOAuthHandler(conn.Auth)
	if err != nil {
		return nil, err
	}
	transport, err := NewTransport(ctx, conn, authHandler)
	if err != nil {
		return nil, err
	}

	client := mcp.NewClient(ClientImpl, nil)
	return connectSession(ctx, client, transport, conn)
}

// connectSession connects uniformly: it only requests mcpspec.ProtocolVersion and verifies the
// negotiated result.
//
// Mind that the SDK client's ProtocolVersion is only a "preferred version": when the peer does not know
// the new protocol it falls back to initialize negotiation and accepts the old version the peer offers
// (the ClientSessionOptions comment says so as well: "The server may negotiate a different mutually
// supported version"). That would drag the old protocol back into nexus, so the version is checked hard
// once the session exists.
func connectSession(ctx context.Context, client *mcp.Client, transport mcp.Transport, conn *models.McpConnection) (*mcp.ClientSession, error) {
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{
		ProtocolVersion: mcpspec.ProtocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the MCP server (%s): %w", conn.Name, err)
	}
	got := ""
	if init := session.InitializeResult(); init != nil {
		got = init.ProtocolVersion
	}
	if got != mcpspec.ProtocolVersion {
		_ = session.Close()
		return nil, fmt.Errorf("MCP server (%s) protocol version mismatch: the peer negotiated %q, nexus only supports %q (an old-protocol server needs an mcp-bridge proxy)",
			conn.Name, got, mcpspec.ProtocolVersion)
	}
	return session, nil
}

// NewTransport builds the transport matching the connection config. authHandler authenticates the
// HTTP transport.
func NewTransport(_ context.Context, conn *models.McpConnection, authHandler auth.OAuthHandler) (mcp.Transport, error) {
	switch strings.ToLower(conn.Transport) {
	case "", "streamable_http":
		return newStreamableHTTPTransport(conn, authHandler)
	case "stdio":
		return newCommandTransport(conn)
	default:
		return nil, fmt.Errorf("unsupported transport: %s", conn.Transport)
	}
}

// newStreamableHTTPTransport builds the streaming HTTP transport.
func newStreamableHTTPTransport(conn *models.McpConnection, authHandler auth.OAuthHandler) (mcp.Transport, error) {
	if conn.URL == "" {
		return nil, fmt.Errorf("the streamable_http transport requires a url")
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:     conn.URL,
		OAuthHandler: authHandler,
	}
	// Inject the custom request headers (custom auth headers, tenant identifiers, ...).
	if headers := customHeaders(conn); len(headers) > 0 {
		transport.HTTPClient = newHTTPClientWithHeaders(headers)
	}
	return transport, nil
}

// newCommandTransport builds the local stdio transport (an exec child process).
func newCommandTransport(conn *models.McpConnection) (mcp.Transport, error) {
	if conn.Command == "" {
		return nil, fmt.Errorf("the stdio transport requires a command")
	}
	args := parseArgs(conn.Args)
	cmd := exec.Command(conn.Command, args...)
	// stdio only: working directory and environment variables.
	if conn.WorkDir != "" {
		cmd.Dir = conn.WorkDir
	}
	if env := parseEnv(conn.Env); len(env) > 0 {
		// Inherit the parent environment and append the connection-level env.
		cmd.Env = append(os.Environ(), env...)
	}
	return &mcp.CommandTransport{Command: cmd}, nil
}

// customHeaders reads the custom request headers from the auth config.
func customHeaders(conn *models.McpConnection) map[string]string {
	cfg, err := parseAuthConfig(conn.Auth)
	if err != nil || len(cfg.Headers) == 0 {
		return nil
	}
	return cfg.Headers
}

// newHTTPClientWithHeaders returns an *http.Client with the given default headers injected.
func newHTTPClientWithHeaders(headers map[string]string) *http.Client {
	base := http.DefaultClient
	return &http.Client{
		Transport: &headerRoundTripper{
			base:   base.Transport,
			header: headers,
		},
		Timeout: base.Timeout,
	}
}

// headerRoundTripper injects custom headers on every request.
type headerRoundTripper struct {
	base   http.RoundTripper
	header map[string]string
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range rt.header {
		req.Header.Set(k, v)
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// parseArgs parses a JSONB argument array into a string slice.
func parseArgs(args xsql.JSONB) []string {
	var arr []string
	if args.Data == nil {
		return arr
	}
	if b, ok := args.Data.(json.RawMessage); ok {
		_ = json.Unmarshal(b, &arr)
	}
	return arr
}

// parseEnv parses a JSONB environment array (["K=V", ...]) into a string slice.
func parseEnv(env xsql.JSONB) []string {
	var arr []string
	if env.Data == nil {
		return arr
	}
	if b, ok := env.Data.(json.RawMessage); ok {
		_ = json.Unmarshal(b, &arr)
	}
	return arr
}
