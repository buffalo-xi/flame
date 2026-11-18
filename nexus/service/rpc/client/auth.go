package client

import (
	"encoding/json"
	"fmt"
	"strings"

	"flame/tools/xsql"
)

// AuthConfig 对应 rpc_connections.auth JSONB 的结构
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
	TokenURL     string            `json:"token_url"`     // oauth 用，token endpoint
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

// buildAuthorizationHeader 根据 auth 配置构造 Authorization 请求头。
// 返回空字符串表示无需鉴权（type=none）。
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
			return "", fmt.Errorf("bearer 鉴权需要 token")
		}
		return "Bearer " + cfg.Token, nil

	case "oauth":
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return "", fmt.Errorf("oauth 鉴权需要 client_id 与 client_secret")
		}
		token, err := fetchOAuthToken(cfg)
		if err != nil {
			return "", err
		}
		return "Bearer " + token, nil

	default:
		return "", fmt.Errorf("不支持的 auth type: %s", cfg.Type)
	}
}

// customHeaders 从 auth 配置中解析自定义请求头
func customHeaders(raw xsql.JSONB) map[string]string {
	cfg, err := parseAuthConfig(raw)
	if err != nil || len(cfg.Headers) == 0 {
		return nil
	}
	return cfg.Headers
}
