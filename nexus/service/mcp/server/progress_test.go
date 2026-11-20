package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reqWithMeta builds a minimal tool call request carrying only _meta.
func reqWithMeta(meta map[string]any) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: meta}}
}

// TestProgressToken: the subscription intent is read only from params._meta.progressToken.
func TestProgressToken(t *testing.T) {
	cases := []struct {
		name string
		req  *mcp.CallToolRequest
		want any
	}{
		{"string token", reqWithMeta(map[string]any{"progressToken": "tok-1"}), "tok-1"},
		{"integer token", reqWithMeta(map[string]any{"progressToken": float64(5)}), float64(5)},
		{"no token", reqWithMeta(map[string]any{}), nil},
		{"a non-integer token counts as not supplied", reqWithMeta(map[string]any{"progressToken": 1.5}), nil},
		{"an empty string token counts as not supplied", reqWithMeta(map[string]any{"progressToken": ""}), nil},
		{"Params is empty", &mcp.CallToolRequest{}, nil},
		{"req is nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressToken(c.req); got != c.want {
				t.Fatalf("progressToken() = %v, want %v", got, c.want)
			}
		})
	}
}
