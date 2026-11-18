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

// ClientImpl 本 MCP 客户端的实现信息（用于握手 Initialize 请求）
var ClientImpl = &mcp.Implementation{Name: "flame-nexus", Version: "1.0.0"}

// NewClientSession 根据连接配置建立与远端 MCP server 的会话（已完成 Connect）。
// 调用方负责在会话使用完毕后调用返回的 session.Close()。
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

// connectSession 统一建连：只请求 mcpspec.ProtocolVersion，并校验最终协商结果。
//
// 注意 SDK 客户端的 ProtocolVersion 只是"首选版本"：对端不认新协议时会退回
// initialize 协商并接受对端给的旧版本（ClientSessionOptions 注释也写明
// "The server may negotiate a different mutually supported version"）。
// 那等于把老协议引回 nexus，所以这里在会话建立后硬校验一次版本。
func connectSession(ctx context.Context, client *mcp.Client, transport mcp.Transport, conn *models.McpConnection) (*mcp.ClientSession, error) {
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{
		ProtocolVersion: mcpspec.ProtocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MCP server（%s）失败: %w", conn.Name, err)
	}
	got := ""
	if init := session.InitializeResult(); init != nil {
		got = init.ProtocolVersion
	}
	if got != mcpspec.ProtocolVersion {
		_ = session.Close()
		return nil, fmt.Errorf("MCP server（%s）协议版本不匹配: 对端协商为 %q，nexus 只支持 %q（老协议 server 需经 mcp-bridge 代理）",
			conn.Name, got, mcpspec.ProtocolVersion)
	}
	return session, nil
}

// NewTransport 根据连接配置构造对应的传输层。authHandler 用于 HTTP 传输的鉴权。
func NewTransport(_ context.Context, conn *models.McpConnection, authHandler auth.OAuthHandler) (mcp.Transport, error) {
	switch strings.ToLower(conn.Transport) {
	case "", "streamable_http":
		return newStreamableHTTPTransport(conn, authHandler)
	case "stdio":
		return newCommandTransport(conn)
	default:
		return nil, fmt.Errorf("不支持的 transport: %s", conn.Transport)
	}
}

// newStreamableHTTPTransport 构造 HTTP 流式传输
func newStreamableHTTPTransport(conn *models.McpConnection, authHandler auth.OAuthHandler) (mcp.Transport, error) {
	if conn.URL == "" {
		return nil, fmt.Errorf("streamable_http 传输需要 url")
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:     conn.URL,
		OAuthHandler: authHandler,
	}
	// 注入自定义请求头（如自定义鉴权头、租户标识等）
	if headers := customHeaders(conn); len(headers) > 0 {
		transport.HTTPClient = newHTTPClientWithHeaders(headers)
	}
	return transport, nil
}

// newCommandTransport 构造本地 stdio 传输（exec 子进程）
func newCommandTransport(conn *models.McpConnection) (mcp.Transport, error) {
	if conn.Command == "" {
		return nil, fmt.Errorf("stdio 传输需要 command")
	}
	args := parseArgs(conn.Args)
	cmd := exec.Command(conn.Command, args...)
	// stdio 专属：工作目录与环境变量
	if conn.WorkDir != "" {
		cmd.Dir = conn.WorkDir
	}
	if env := parseEnv(conn.Env); len(env) > 0 {
		// 继承父进程环境变量并追加连接级 env
		cmd.Env = append(os.Environ(), env...)
	}
	return &mcp.CommandTransport{Command: cmd}, nil
}

// customHeaders 从 auth 配置中解析自定义请求头
func customHeaders(conn *models.McpConnection) map[string]string {
	cfg, err := parseAuthConfig(conn.Auth)
	if err != nil || len(cfg.Headers) == 0 {
		return nil
	}
	return cfg.Headers
}

// newHTTPClientWithHeaders 返回一个注入了默认请求头的 *http.Client
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

// headerRoundTripper 在每次请求前注入自定义头
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

// parseArgs 将 JSONB 参数数组解析为字符串切片
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

// parseEnv 将 JSONB 环境变量数组（["K=V", ...]）解析为字符串切片
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
