package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reqWithMeta 构造一个只带 _meta 的最小工具调用请求。
func reqWithMeta(meta map[string]any) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: meta}}
}

// TestProgressToken 订阅意图只看 params._meta.progressToken。
func TestProgressToken(t *testing.T) {
	cases := []struct {
		name string
		req  *mcp.CallToolRequest
		want any
	}{
		{"字符串 token", reqWithMeta(map[string]any{"progressToken": "tok-1"}), "tok-1"},
		{"整数 token", reqWithMeta(map[string]any{"progressToken": float64(5)}), float64(5)},
		{"没有 token", reqWithMeta(map[string]any{}), nil},
		{"非整数 token 视为未提供", reqWithMeta(map[string]any{"progressToken": 1.5}), nil},
		{"空串 token 视为未提供", reqWithMeta(map[string]any{"progressToken": ""}), nil},
		{"Params 为空", &mcp.CallToolRequest{}, nil},
		{"req 为 nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressToken(c.req); got != c.want {
				t.Fatalf("progressToken() = %v, want %v", got, c.want)
			}
		})
	}
}
