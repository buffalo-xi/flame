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

// AuthConfig mirrors the structure of the mcp_connections.auth JSONB column.
// Examples:
//
//	{"type":"none"}
//	{"type":"bearer","token":"xxxx"}
//	{"type":"oauth","client_id":"...","client_secret":"..."}
type AuthConfig struct {
	Type         string            `json:"type"`          // none | bearer | oauth
	Token        string            `json:"token"`         // used by bearer
	ClientID     string            `json:"client_id"`     // used by oauth
	ClientSecret string            `json:"client_secret"` // used by oauth
	Headers      map[string]string `json:"headers"`       // custom request headers
}

// parseAuthConfig parses the auth JSONB of a connection.
func parseAuthConfig(raw xsql.JSONB) (*AuthConfig, error) {
	if raw.Data == nil {
		return &AuthConfig{Type: "none"}, nil
	}
	rawBytes, ok := raw.Data.(json.RawMessage)
	if !ok {
		// When Data is not a RawMessage (it may already have been decoded into a map), re-serialize it first.
		b, err := json.Marshal(raw.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse the auth config: %w", err)
		}
		rawBytes = b
	}
	var cfg AuthConfig
	if err := json.Unmarshal(rawBytes, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse the auth config: %w", err)
	}
	if cfg.Type == "" {
		cfg.Type = "none"
	}
	return &cfg, nil
}

// BuildOAuthHandler builds the SDK OAuthHandler from the auth config.
// nil means no authentication is needed (type=none).
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
			return nil, fmt.Errorf("bearer auth requires a token")
		}
		return &staticTokenHandler{
			tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token, TokenType: "Bearer"}),
		}, nil

	case "oauth":
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return nil, fmt.Errorf("oauth auth requires client_id and client_secret")
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
			return nil, fmt.Errorf("failed to build the oauth handler: %w", err)
		}
		return handler, nil

	default:
		return nil, fmt.Errorf("unsupported auth type: %s", cfg.Type)
	}
}

// staticTokenHandler implements auth.OAuthHandler for a static bearer token.
type staticTokenHandler struct {
	tokenSource oauth2.TokenSource
}

var _ auth.OAuthHandler = (*staticTokenHandler)(nil)

func (h *staticTokenHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.tokenSource, nil
}

// Authorize is a no-op: a static token needs no interactive authorization.
func (h *staticTokenHandler) Authorize(ctx context.Context, req *http.Request, res *http.Response) error {
	return nil
}
