package core

import "testing"

// 会话 id 只从 params._meta 里取：它不是端点的业务参数。
// 这一组钉住「什么算给了」「什么算没给」。
func TestMetaSessionID(t *testing.T) {
	cases := []struct {
		name   string
		params any
		want   string
	}{
		{
			name:   "正常",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: " s-1 "}},
			want:   "s-1", // 去掉首尾空白
		},
		{
			name:   "没有 _meta",
			params: map[string]any{"name": "x"},
			want:   "",
		},
		{
			name:   "_meta 不是对象",
			params: map[string]any{"_meta": "oops"},
			want:   "",
		},
		{
			name:   "_meta 里没有这个键",
			params: map[string]any{"_meta": map[string]any{"progressToken": "t"}},
			want:   "",
		},
		{
			name:   "类型不对（数字）当作没给",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: 42}},
			want:   "",
		},
		{
			name:   "空串当作没给",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: "   "}},
			want:   "",
		},
		{
			// 旧位置（业务入参里）不算给了 —— 回落由调用方负责，这里只认 _meta
			name:   "只在 arguments 里不算",
			params: map[string]any{"arguments": map[string]any{"session_id": "s-1"}},
			want:   "",
		},
		{name: "params 不是对象", params: "oops", want: ""},
		{name: "params 为 nil", params: nil, want: ""},
	}
	for _, c := range cases {
		if got := MetaSessionID(c.params); got != c.want {
			t.Errorf("%s: MetaSessionID = %q, 期望 %q", c.name, got, c.want)
		}
	}
}

// 剥离必须同时满足：结果里没有 session_id、原 map 不被改动、没有该键时不复制。
func TestStripSessionID(t *testing.T) {
	orig := map[string]any{"q": "hi", "session_id": "s-1"}
	clean := StripSessionID(orig)

	if _, ok := clean["session_id"]; ok {
		t.Fatalf("剥离后仍有 session_id: %v", clean)
	}
	if clean["q"] != "hi" {
		t.Fatalf("业务参数丢了: %v", clean)
	}
	if orig["session_id"] != "s-1" {
		t.Fatalf("原 map 被改动: %v", orig)
	}

	// 没有该键：原样返回（不为绝大多数请求复制一遍 map）
	plain := map[string]any{"q": "hi"}
	if got := StripSessionID(plain); len(got) != 1 || got["q"] != "hi" {
		t.Fatalf("无 session_id 时应原样返回: %v", got)
	}
	if StripSessionID(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
}
