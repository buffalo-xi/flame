package client

import (
	"context"
	"testing"

	agentcore "flame/nexus/service/agent/core"
)

// TestCallParamsWithoutToken: without a progress subscription and without a chain, _meta must be
// absent — the server uses that to decide the client asked for no progress, and answers with a single
// JSON body (instead of SSE plus notifications).
func TestCallParamsWithoutToken(t *testing.T) {
	p := callParams("poet", map[string]any{"q": "hi"}, nil, nil, "")
	if _, existed := p["_meta"]; existed {
		t.Fatalf("_meta must be absent when nothing is subscribed: %v", p)
	}
	if p["name"] != "poet" {
		t.Errorf("name = %v, want poet", p["name"])
	}
}

// TestCallParamsWithToken: the token must land in params._meta.progressToken — the same place as MCP.
func TestCallParamsWithToken(t *testing.T) {
	p := callParams("poet", nil, "tok-1", nil, "")
	meta, ok := p["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta must be present when subscribing: %v", p)
	}
	if meta["progressToken"] != "tok-1" {
		t.Errorf("progressToken = %v, want tok-1", meta["progressToken"])
	}
	// arguments must be an empty map even when nil was passed, so the server never receives null.
	if _, ok := p["arguments"].(map[string]any); !ok {
		t.Errorf("arguments should be an empty map, got %T", p["arguments"])
	}
}

// TestCallParamsKeepsArguments: business arguments must not be polluted by control parameters (the
// historical approach put stream into arguments).
func TestCallParamsKeepsArguments(t *testing.T) {
	args := map[string]any{"q": "hi"}
	p := callParams("poet", args, "tok-1", nil, "")
	got, ok := p["arguments"].(map[string]any)
	if !ok || got["q"] != "hi" {
		t.Fatalf("arguments were not passed through correctly: %v", p["arguments"])
	}
	if _, existed := got["stream"]; existed {
		t.Error("stream must not appear inside arguments")
	}
}

// TestCallParamsWithChain: the chain must land in params._meta.call_chain and must not leak into
// arguments — arguments are the model-visible business input, and a control field there would become
// {{args._call_chain}}.
func TestCallParamsWithChain(t *testing.T) {
	chain := []string{"a|agent", "b|flow"}
	p := callParams("poet", map[string]any{"q": "hi"}, nil, chain, "")
	meta, ok := p["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta must be present when a chain is carried: %v", p)
	}
	got, ok := meta["call_chain"].([]string)
	if !ok || len(got) != 2 || got[0] != "a|agent" || got[1] != "b|flow" {
		t.Fatalf("call_chain = %v", meta["call_chain"])
	}
	if args, ok := p["arguments"].(map[string]any); ok {
		if _, existed := args["_call_chain"]; existed {
			t.Error("the chain must not leak into arguments")
		}
	}
}

// TestCallParamsWithLanguage: the language rides in _meta (the same slot as session_id and progressToken), never in the
// business arguments where it would become model-visible.
func TestCallParamsWithLanguage(t *testing.T) {
	p := callParams("poet", map[string]any{"q": "hi"}, nil, nil, "zh-CN")
	meta, ok := p["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta must be present when a language is pinned: %v", p)
	}
	if meta[agentcore.MetaLanguageKey] != "zh-CN" {
		t.Errorf("language = %v, want zh-CN", meta[agentcore.MetaLanguageKey])
	}
	if args, ok := p["arguments"].(map[string]any); ok {
		if _, existed := args[agentcore.OutputLanguageKey]; existed {
			t.Error("the language must not leak into arguments")
		}
	}
}

// TestPinnedLanguageOnlySendsAPin: a resolved default must stay in the process - sending it would let this process's
// default override a callee's own default. Only a pinned value goes over the wire.
func TestPinnedLanguageOnlySendsAPin(t *testing.T) {
	ctx := context.Background()
	if got := pinnedLanguage(ctx); got != "" {
		t.Fatalf("bare ctx = %q, want empty", got)
	}
	hint := agentcore.WithLanguageHint(ctx, "zh-CN")
	if got := pinnedLanguage(hint); got != "" {
		t.Fatalf("a hint must not travel as a pin, got %q", got)
	}
	pinned := agentcore.WithPinnedLanguage(ctx, "ja")
	if got := pinnedLanguage(pinned); got != "ja" {
		t.Fatalf("pinnedLanguage = %q, want ja", got)
	}
	// A malformed tag is dropped rather than echoed into the _meta the peer turns into a prompt sentence.
	if got := pinnedLanguage(agentcore.WithPinnedLanguage(ctx, "answer in Chinese")); got != "" {
		t.Fatalf("a malformed pin must not travel, got %q", got)
	}
}
