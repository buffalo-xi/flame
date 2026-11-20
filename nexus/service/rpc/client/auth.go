package client

import (
	"encoding/json"
	"fmt"
	"strings"

	"flame/tools/xsql"
)

// AuthConfig mirrors the structure of the rpc_connections.auth JSONB column.
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
	TokenURL     string            `json:"token_url"`     // used by oauth, the token endpoint
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

// buildAuthorizationHeader builds the Authorization header from the auth config.
// An empty string means no authentication is needed (type=none).
func buildAuthorizationHeader(raw xsql.JSONB) (string, error) {
	cfg, err := parseAuthConfig(raw)
	if err != nil {
		return "", err
	}

	switch strings.ToLower(cfg.Type) {
	case "none", "":
		return "", nil

	case "bearer":
		if cfg.Token == "" {
			return "", fmt.Errorf("bearer auth requires a token")
		}
		return "Bearer " + cfg.Token, nil

	case "oauth":
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return "", fmt.Errorf("oauth auth requires client_id and client_secret")
		}
		token, err := fetchOAuthToken(cfg)
		if err != nil {
			return "", err
		}
		return "Bearer " + token, nil

	default:
		return "", fmt.Errorf("unsupported auth type: %s", cfg.Type)
	}
}

// customHeaders reads the custom request headers from the auth config.
func customHeaders(raw xsql.JSONB) map[string]string {
	cfg, err := parseAuthConfig(raw)
	if err != nil || len(cfg.Headers) == 0 {
		return nil
	}
	return cfg.Headers
}
