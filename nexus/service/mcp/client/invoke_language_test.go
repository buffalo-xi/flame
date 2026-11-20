package client

import (
	"context"
	"testing"

	agentcore "flame/nexus/service/agent/core"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A pinned language travels as params._meta.language - the same key the RPC client uses - so an MCP tool is told which
// language the run decided on.
func TestWithLanguagePinned(t *testing.T) {
	params := &mcp.CallToolParams{}
	withLanguage(params, agentcore.WithPinnedLanguage(context.Background(), "zh-CN"))
	if got := params.Meta[agentcore.MetaLanguageKey]; got != "zh-CN" {
		t.Fatalf("params._meta.language = %v, want zh-CN", got)
	}
}

// An unpinned hint stays in-process: sending it would override the peer's own default.
func TestWithLanguageHintDoesNotTravel(t *testing.T) {
	params := &mcp.CallToolParams{}
	withLanguage(params, agentcore.WithLanguageHint(context.Background(), "zh-CN"))
	if len(params.Meta) != 0 {
		t.Fatalf("a hint must not reach the wire: %+v", params.Meta)
	}
}

// A malformed tag is dropped rather than echoed, and a nil params must not panic.
func TestWithLanguageRejectsMalformed(t *testing.T) {
	params := &mcp.CallToolParams{}
	withLanguage(params, agentcore.WithPinnedLanguage(context.Background(), "answer in Chinese"))
	if len(params.Meta) != 0 {
		t.Fatalf("a malformed tag must not reach the wire: %+v", params.Meta)
	}
	withLanguage(nil, agentcore.WithPinnedLanguage(context.Background(), "ja"))
}
