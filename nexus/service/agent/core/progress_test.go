package core

import "testing"

// TestEventToProgress covers the mapping of the four agent event types to streaming output:
// thinking/text go through Message as deltas, tool_call/tool_result through the structured Meta fields.
func TestEventToProgress(t *testing.T) {
	t.Run("thinking", func(t *testing.T) {
		item, ok := Event{Type: EventThinking, Delta: "let me think"}.ToProgress()
		if !ok {
			t.Fatal("a thinking event must not be dropped")
		}
		if item.Category != CategoryThinking {
			t.Fatalf("category = %q, want %q", item.Category, CategoryThinking)
		}
		if item.Message != "let me think" {
			t.Fatalf("message = %q", item.Message)
		}
	})

	t.Run("text", func(t *testing.T) {
		item, ok := Event{Type: EventText, Delta: "hello"}.ToProgress()
		if !ok || item.Category != CategoryText {
			t.Fatalf("text mapping failed: ok=%v item=%+v", ok, item)
		}
		if item.Message != "hello" {
			t.Fatalf("message = %q", item.Message)
		}
	})

	t.Run("text that is empty after cleaning is dropped", func(t *testing.T) {
		if item, ok := (Event{Type: EventText, Delta: "[[think]]hidden[[/think]]"}).ToProgress(); ok {
			t.Fatalf("expected an empty text delta to be dropped, got %+v", item)
		}
	})

	t.Run("tool_call", func(t *testing.T) {
		item, ok := Event{
			Type:   EventToolCall,
			CallID: "c1",
			Tool:   "file_read",
			Args:   `{"path":"a"}`,
		}.ToProgress()
		if !ok || item.Category != CategoryToolCall {
			t.Fatalf("tool_call mapping failed: ok=%v item=%+v", ok, item)
		}
		if item.Meta["call_id"] != "c1" || item.Meta["tool"] != "file_read" {
			t.Fatalf("meta fields are missing: %+v", item.Meta)
		}
		if item.Message != "file_read" {
			t.Fatalf("message = %q, want the tool name", item.Message)
		}
	})

	t.Run("tool_result", func(t *testing.T) {
		item, ok := Event{
			Type:       EventToolResult,
			CallID:     "c1",
			Tool:       "file_read",
			Result:     "ok",
			OK:         true,
			DurationMS: 12,
		}.ToProgress()
		if !ok || item.Category != CategoryToolResult {
			t.Fatalf("tool_result mapping failed: ok=%v item=%+v", ok, item)
		}
		if item.Meta["ok"] != true || item.Meta["duration_ms"] != int64(12) {
			t.Fatalf("meta fields are missing: %+v", item.Meta)
		}
		if item.Message != "ok" {
			t.Fatalf("message = %q", item.Message)
		}
	})
}
