package client

import "testing"

// TestCallParamsWithoutToken 不订阅进度、也没有链时不得带 _meta：
// 服务端据此判定「客户端没要进度」，响应回落成单条 JSON（而不是 SSE + notifications）。
func TestCallParamsWithoutToken(t *testing.T) {
	p := callParams("poet", map[string]any{"q": "hi"}, nil, nil)
	if _, existed := p["_meta"]; existed {
		t.Fatalf("未订阅时不该带 _meta: %v", p)
	}
	if p["name"] != "poet" {
		t.Errorf("name = %v, want poet", p["name"])
	}
}

// TestCallParamsWithToken token 必须落在 params._meta.progressToken —— 与 MCP 同一位置。
func TestCallParamsWithToken(t *testing.T) {
	p := callParams("poet", nil, "tok-1", nil)
	meta, ok := p["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("订阅时应带 _meta: %v", p)
	}
	if meta["progressToken"] != "tok-1" {
		t.Errorf("progressToken = %v, want tok-1", meta["progressToken"])
	}
	// arguments 即使传 nil 也要给出空 map，避免服务端拿到 null
	if _, ok := p["arguments"].(map[string]any); !ok {
		t.Errorf("arguments 应为空 map, got %T", p["arguments"])
	}
}

// TestCallParamsKeepsArguments 业务参数不能被控制参数污染（历史做法是把 stream 塞进 arguments）。
func TestCallParamsKeepsArguments(t *testing.T) {
	args := map[string]any{"q": "hi"}
	p := callParams("poet", args, "tok-1", nil)
	got, ok := p["arguments"].(map[string]any)
	if !ok || got["q"] != "hi" {
		t.Fatalf("arguments 透传不对: %v", p["arguments"])
	}
	if _, existed := got["stream"]; existed {
		t.Error("arguments 里不该出现 stream")
	}
}

// TestCallParamsWithChain 链必须落在 params._meta.call_chain，
// 且不能混进 arguments —— 那是模型可见的业务入参，控制字段进去会变成 {{args._call_chain}}。
func TestCallParamsWithChain(t *testing.T) {
	chain := []string{"a|agent", "b|flow"}
	p := callParams("poet", map[string]any{"q": "hi"}, nil, chain)
	meta, ok := p["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("带链时应带 _meta: %v", p)
	}
	got, ok := meta["call_chain"].([]string)
	if !ok || len(got) != 2 || got[0] != "a|agent" || got[1] != "b|flow" {
		t.Fatalf("call_chain = %v", meta["call_chain"])
	}
	if args, ok := p["arguments"].(map[string]any); ok {
		if _, existed := args["_call_chain"]; existed {
			t.Error("链不能混进 arguments")
		}
	}
}
