package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMetaLanguage: the output language is read from params._meta.language only, and an unusable value counts as "not supplied"
// (a mistyped tag must not fail the call - _meta is advisory).
func TestMetaLanguage(t *testing.T) {
	cases := []struct {
		name string
		req  *mcp.CallToolRequest
		want string
	}{
		{"simplified chinese", reqWithMeta(map[string]any{"language": "zh-CN"}), "zh-CN"},
		{"padded", reqWithMeta(map[string]any{"language": " ja "}), "ja"},
		{"absent", reqWithMeta(map[string]any{}), ""},
		{"a non-string counts as not supplied", reqWithMeta(map[string]any{"language": 42}), ""},
		{"a sentence is not a tag", reqWithMeta(map[string]any{"language": "answer in Chinese"}), ""},
		{"Params is empty", &mcp.CallToolRequest{}, ""},
		{"req is nil", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := metaLanguage(c.req); got != c.want {
				t.Fatalf("metaLanguage = %q, want %q", got, c.want)
			}
		})
	}
}
