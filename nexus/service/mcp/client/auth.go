package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"flame/tools/xsql"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/auth/extauth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// AuthConfig 对应 mcp_connections.auth JSONB 的结构
// 示例：
//
//	{"type":"none"}
//	{"type":"bearer","token":"xxxx"}
//	{"type":"oauth","client_id":"...","client_secret":"..."}
type AuthConfig struct {
	Type         string            `json:"type"`          // none | bearer | oauth
	Token        string            `json:"token"`         // bearer 用
	ClientID     string            `json:"client_id"`     // oauth 用
	ClientSecret string            `json:"client_secret"` // oauth 用
	Headers      map[string]string `json:"headers"`       // 自定义请求头
}

// parseAuthConfig 解析连接上的 auth JSONB
func parseAuthConfig(raw xsql.JSONB) (*AuthConfig, error) {
	if raw.Data == nil {
		return &AuthConfig{Type: "none"}, nil
	}
	rawBytes, ok := raw.Data.(json.RawMessage)
	if !ok {
		// 若 Data 非 RawMessage（如已被解析为 map），先回序列化
		b, err := json.Marshal(raw.Data)
		if err != nil {
			return nil, fmt.Errorf("解析 auth 配置失败: %w", err)
		}
		rawBytes = b
	}
	var cfg AuthConfig
	if err := json.Unmarshal(rawBytes, &cfg); err != nil {
		return nil, fmt.Errorf("解析 auth 配置失败: %w", err)
	}
	if cfg.Type == "" {
		cfg.Type = "none"
	}
	return &cfg, nil
}

// BuildOAuthHandler 根据 auth 配置构造 SDK 的 OAuthHandler
// 返回 nil 表示无需鉴权（type=none）
func BuildOAuthHandler(raw xsql.JSONB) (auth.OAuthHandler, error) {
	cfg, err := parseAuthConfig(raw)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(cfg.Type) {
	case "none", "":
		return nil, nil

	case "bearer":
		if cfg.Token == "" {
			return nil, fmt.Errorf("bearer 鉴权需要 token")
		}
		return &staticTokenHandler{
			tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token, TokenType: "Bearer"}),
		}, nil

	case "oauth":
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return nil, fmt.Errorf("oauth 鉴权需要 client_id 与 client_secret")
		}
		handler, err := extauth.NewClientCredentialsHandler(&extauth.ClientCredentialsHandlerConfig{
			Credentials: &oauthex.ClientCredentials{
				ClientID: cfg.ClientID,
				ClientSecretAuth: &oauthex.ClientSecretAuth{
					ClientSecret: cfg.ClientSecret,
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("构造 oauth handler 失败: %w", err)
		}
		return handler, nil

	default:
		return nil, fmt.Errorf("不支持的 auth type: %s", cfg.Type)
	}
}

// staticTokenHandler 实现 auth.OAuthHandler，用于静态 Bearer Token
type staticTokenHandler struct {
	tokenSource oauth2.TokenSource
}

var _ auth.OAuthHandler = (*staticTokenHandler)(nil)

func (h *staticTokenHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.tokenSource, nil
}

// Authorize 静态 token 不需要交互式授权，直接返回即可
func (h *staticTokenHandler) Authorize(ctx context.Context, req *http.Request, res *http.Response) error {
	return nil
}
