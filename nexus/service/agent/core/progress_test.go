package core

import "testing"

// TestEventToProgress 覆盖 agent 四类事件到流式输出的映射：
// thinking/text 走 Message 增量，tool_call/tool_result 走 Meta 结构化字段。
func TestEventToProgress(t *testing.T) {
	t.Run("thinking", func(t *testing.T) {
		item, ok := Event{Type: EventThinking, Delta: "let me think"}.ToProgress()
		if !ok {
			t.Fatal("thinking 事件不应被丢弃")
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
			t.Fatalf("text 映射失败: ok=%v item=%+v", ok, item)
		}
		if item.Message != "hello" {
			t.Fatalf("message = %q", item.Message)
		}
	})

	t.Run("text 清理后为空则丢弃", func(t *testing.T) {
		if item, ok := (Event{Type: EventText, Delta: "[[think]]hidden[[/think]]"}).ToProgress(); ok {
			t.Fatalf("期望丢弃空文本增量，实际 %+v", item)
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
			t.Fatalf("tool_call 映射失败: ok=%v item=%+v", ok, item)
		}
		if item.Meta["call_id"] != "c1" || item.Meta["tool"] != "file_read" {
			t.Fatalf("meta 字段缺失: %+v", item.Meta)
		}
		if item.Message != "file_read" {
			t.Fatalf("message = %q, want 工具名", item.Message)
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
			t.Fatalf("tool_result 映射失败: ok=%v item=%+v", ok, item)
		}
		if item.Meta["ok"] != true || item.Meta["duration_ms"] != int64(12) {
			t.Fatalf("meta 字段缺失: %+v", item.Meta)
		}
		if item.Message != "ok" {
			t.Fatalf("message = %q", item.Message)
		}
	})
}
