package litellm

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"flame/nexus/service/llm/gateway"
)

// The markers are built by concatenation, so no contiguous run of the special characters appears in the
// test source.
var (
	barHalf = "|" + "DSML" + "|"
	barFull = "｜" + "DSML" + "｜"
	barDbl  = "｜｜" + "DSML" + "｜｜"
)

func openTag(bar, kind, attrs string) string { return "<" + bar + kind + attrs + ">" }
func closeTag(bar, kind string) string       { return "</" + bar + kind + ">" }

func strParam(bar, name, val string) string {
	return openTag(bar, "parameter", ` name="`+name+`" string="true"`) + val + closeTag(bar, "parameter")
}

func rawParam(bar, name, val string) string {
	return openTag(bar, "parameter", ` name="`+name+`"`) + val + closeTag(bar, "parameter")
}

func invoke(bar, tool string, params ...string) string {
	return openTag(bar, "invoke", ` name="`+tool+`"`) + strings.Join(params, "") + closeTag(bar, "invoke")
}

func fnCalls(bar string, invokes ...string) string {
	return openTag(bar, "function_calls", "") + strings.Join(invokes, "") + closeTag(bar, "function_calls")
}

func TestParseDSMLToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantCalls int
		wantName  string
		wantArgs  map[string]any
		wantClean string
	}{
		{
			name:      "half-width bars plus a string parameter",
			in:        fnCalls(barHalf, invoke(barHalf, "read_skill", strParam(barHalf, "name", "english-joke-writing"))),
			wantCalls: 1,
			wantName:  "read_skill",
			wantArgs:  map[string]any{"name": "english-joke-writing"},
			wantClean: "",
		},
		{
			name:      "full-width bars with surrounding text",
			in:        "好的" + fnCalls(barFull, invoke(barFull, "read_skill", strParam(barFull, "name", "poet"))) + "。",
			wantCalls: 1,
			wantName:  "read_skill",
			wantArgs:  map[string]any{"name": "poet"},
			wantClean: "好的。",
		},
		{
			name:      "doubled full-width bars",
			in:        fnCalls(barDbl, invoke(barDbl, "read_agent", strParam(barDbl, "name", "poet"))),
			wantCalls: 1,
			wantName:  "read_agent",
			wantArgs:  map[string]any{"name": "poet"},
			wantClean: "",
		},
		{
			name: "non-string parameters are decoded as JSON",
			in: fnCalls(barHalf, invoke(barHalf, "f",
				rawParam(barHalf, "n", "42"), rawParam(barHalf, "flag", "true"))),
			wantCalls: 1,
			wantName:  "f",
			wantArgs:  map[string]any{"n": float64(42), "flag": true},
			wantClean: "",
		},
		{
			name:      "no markers: returned unchanged",
			in:        "这是一段普通回答",
			wantCalls: 0,
			wantClean: "这是一段普通回答",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, clean := parseDSMLToolCalls(tt.in)
			if len(calls) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(calls), tt.wantCalls)
			}
			if tt.wantCalls > 0 {
				if calls[0].Name != tt.wantName {
					t.Errorf("name = %q, want %q", calls[0].Name, tt.wantName)
				}
				for k, v := range tt.wantArgs {
					if calls[0].Args[k] != v {
						t.Errorf("arg %q = %#v, want %#v", k, calls[0].Args[k], v)
					}
				}
			}
			if clean != tt.wantClean {
				t.Errorf("clean = %q, want %q", clean, tt.wantClean)
			}
		})
	}
}

func TestParseDSMLToolCallsMultipleInvokes(t *testing.T) {
	in := fnCalls(barHalf,
		invoke(barHalf, "read_skill", strParam(barHalf, "name", "a")),
		invoke(barHalf, "read_agent", strParam(barHalf, "name", "b")),
	)
	calls, clean := parseDSMLToolCalls(in)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Name != "read_skill" || calls[0].Args["name"] != "a" {
		t.Errorf("call[0] = %+v", calls[0])
	}
	if calls[1].Name != "read_agent" || calls[1].Args["name"] != "b" {
		t.Errorf("call[1] = %+v", calls[1])
	}
	if clean != "" {
		t.Errorf("clean = %q, want empty", clean)
	}
}

func TestParseDSMLToolCallsIDSkips(t *testing.T) {
	in := fnCalls(barHalf,
		invoke(barHalf, "x", strParam(barHalf, "name", "1")),
		invoke(barHalf, "y", strParam(barHalf, "name", "2")),
	)
	calls, _ := parseDSMLToolCalls(in)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("call ids should differ, both = %q", calls[0].ID)
	}
}

func TestParseDSMLToolCallsBareInvoke(t *testing.T) {
	in := "前缀" + invoke(barHalf, "read_skill", strParam(barHalf, "name", "a"))
	calls, clean := parseDSMLToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if clean != "前缀" {
		t.Errorf("clean = %q, want %q", clean, "前缀")
	}
}

// A text-shaped call must carry the FromText flag, which agentImpl uses to choose plain-text echoing.
func TestParseDSMLToolCallsMarksFromText(t *testing.T) {
	in := fnCalls(barHalf, invoke(barHalf, "read_skill", strParam(barHalf, "name", "a")))
	calls, _ := parseDSMLToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if !calls[0].FromText {
		t.Fatalf("FromText = false, want true: %+v", calls[0])
	}
}

func TestSafeEmitLenAlignsToRuneBoundary(t *testing.T) {
	s := "abc中文" // 3 ASCII bytes plus 2 CJK characters (3 bytes each)
	if got := safeEmitLen(s, len(s)); got != len(s) {
		t.Fatalf("full = %d, want %d", got, len(s))
	}
	// A want landing inside "中" (bytes 4 and 5) rounds down to 3.
	for _, want := range []int{4, 5} {
		if got := safeEmitLen(s, want); got != 3 {
			t.Errorf("safeEmitLen(%q, %d) = %d, want 3", s, want, got)
		}
	}
	if got := safeEmitLen(s, 0); got != 0 {
		t.Errorf("safeEmitLen(s, 0) = %d, want 0", got)
	}
}

// sseChunk builds one SSE data line (finish_reason is included when finish is not nil).
func sseChunk(content string, finish any) string {
	choice := map[string]any{"index": 0, "delta": map[string]any{"content": content}}
	if finish != nil {
		choice["finish_reason"] = finish
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{choice}})
	return "data: " + string(b) + "\n\n"
}

// collectStream drives readSSEStream and returns the accumulated text plus the parsed tool calls.
func collectStream(t *testing.T, sse string) (string, []gateway.ToolCall) {
	t.Helper()
	g := &ProxyGateway{URL: "http://test"}
	ch := make(chan gateway.StreamChunk, 64)
	go g.readSSEStream(io.NopCloser(strings.NewReader(sse)), ch)

	var got strings.Builder
	var calls []gateway.ToolCall
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		got.WriteString(c.Content)
		if len(c.ToolCalls) > 0 {
			calls = c.ToolCalls
		}
	}
	return got.String(), calls
}

// Demonstrates where the garbage comes from: slicing CJK text by byte produces invalid UTF-8, which
// json.Marshal then renders as U+FFFD.
func TestByteSliceWouldCorrupt(t *testing.T) {
	s := "中文"                   // 6 bytes
	b, _ := json.Marshal(s[:4]) // 4 lands inside "文"
	if !strings.ContainsRune(string(b), '\uFFFD') {
		t.Fatalf("expected the replacement character U+FFFD, got %q", string(b))
	}
}

// Sends CJK text one rune at a time to prove the streaming fragments never cut a multi-byte character
// (byte slicing used to produce U+FFFD garbage).
func TestReadSSEStreamDoesNotSplitUTF8(t *testing.T) {
	const want = "你好世界，这是一段用于验证流式分片不会切断多字节汉字的中文测试文本，长度需要超过 holdback 阈值。"
	var sb strings.Builder
	for _, r := range want {
		sb.WriteString(sseChunk(string(r), nil))
	}
	sb.WriteString("data: [DONE]\n\n")

	got, _ := collectStream(t, sb.String())
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("output contains the replacement character U+FFFD (garbage): %q", got)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// CJK text mixed with a |DSML| call: neither garbled nor leaking the markers as text.
func TestReadSSEStreamChineseWithDSMLCall(t *testing.T) {
	head := "好的，我先读一下技能。"
	tail := "读完继续作答。"
	full := head + fnCalls(barHalf, invoke(barHalf, "read_skill", strParam(barHalf, "name", "english-joke-writing"))) + tail

	var sb strings.Builder
	runes := []rune(full)
	for i, r := range runes {
		var finish any
		if i == len(runes)-1 {
			finish = "stop"
		}
		sb.WriteString(sseChunk(string(r), finish))
	}

	got, calls := collectStream(t, sb.String())
	if len(calls) != 1 || calls[0].Name != "read_skill" {
		t.Fatalf("calls = %+v, want one read_skill", calls)
	}
	if calls[0].Args["name"] != "english-joke-writing" {
		t.Errorf("arg name = %v", calls[0].Args["name"])
	}
	if strings.Contains(got, "DSML") {
		t.Fatalf("the output leaked DSML markers: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("output contains the replacement character U+FFFD (garbage): %q", got)
	}
	if want := head + tail; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
