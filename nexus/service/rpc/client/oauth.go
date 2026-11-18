package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// fetchOAuthToken 通过 OAuth 2.0 client_credentials 流程获取 access_token。
// 使用 cfg.TokenURL 作为 token endpoint；为空时回退到 <连接 URL 所在 origin> + 常见路径的推断由调用方决定，
// 此处要求显式提供 token_url。
func fetchOAuthToken(cfg *AuthConfig) (string, error) {
	if cfg.TokenURL == "" {
		return "", fmt.Errorf("oauth 鉴权需要 token_url（token endpoint）")
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth token 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("oauth token 请求失败，状态码 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("解析 oauth token 响应失败: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("oauth 错误: %s", tokenResp.Error)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("oauth token 响应缺少 access_token")
	}
	return tokenResp.AccessToken, nil
}
