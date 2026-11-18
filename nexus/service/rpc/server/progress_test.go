package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flame/nexus/service/agent/core"
)

// parseProgressFrames 从 SSE 响应体里取出所有 notifications/progress 的 params。
func parseProgressFrames(t *testing.T, body string) []rpcProgressMsg {
	t.Helper()
	var out []rpcProgressMsg
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var prog rpcProgress
		if err := json.Unmarshal([]byte(payload), &prog); err != nil {
			t.Fatalf("解析帧失败: %v (%s)", err, payload)
		}
		if prog.Method != "notifications/progress" {
			continue
		}
		out = append(out, prog.Params)
	}
	return out
}

// TestExtractProgressToken 订阅意图只看 params._meta.progressToken，
// 且类型不合规（非 string / 非整数）一律视为没订阅。
func TestExtractProgressToken(t *testing.T) {
	cases := []struct {
		name   string
		params any
		want   any
	}{
		{
			name:   "字符串 token",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": "tok-1"}},
			want:   "tok-1",
		},
		{
			name:   "整数 token",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": float64(7)}},
			want:   float64(7),
		},
		{
			name:   "没有 _meta",
			params: map[string]any{"name": "x"},
			want:   nil,
		},
		{
			name:   "_meta 不是对象",
			params: map[string]any{"name": "x", "_meta": "oops"},
			want:   nil,
		},
		{
			name:   "非整数 token 视为未提供",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": 1.5}},
			want:   nil,
		},
		{
			name:   "空串 token 视为未提供",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": ""}},
			want:   nil,
		},
		{
			name:   "params 不是对象",
			params: "oops",
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractProgressToken(c.params)
			if got != c.want {
				t.Fatalf("extractProgressToken() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestNotifyProgressFrames 通知必须回显 token，且 progress 严格递增；
// 空内容不推送，也不能白白消耗序号（否则序号会出现跳号）。
func TestNotifyProgressFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	pump := core.NewProgressPump("tok-1")

	notifyProgress(rec, pump, core.CategoryText, "a", nil)
	notifyProgress(rec, pump, core.CategoryThinking, "", nil) // 空内容：不发，也不吃序号
	notifyProgress(rec, pump, core.CategoryToolCall, "b", map[string]any{"call_id": "c1"})

	frames := parseProgressFrames(t, rec.Body.String())
	if len(frames) != 2 {
		t.Fatalf("帧数 = %d, want 2（空内容不该发）: %s", len(frames), rec.Body.String())
	}
	for i, f := range frames {
		if f.ProgressToken != "tok-1" {
			t.Errorf("第 %d 帧 token = %v, want tok-1", i, f.ProgressToken)
		}
		if want := float64(i + 1); f.Progress != want {
			t.Errorf("第 %d 帧 progress = %v, want %v（必须单调递增）", i, f.Progress, want)
		}
	}
	if frames[0].Message != "a" || frames[1].Message != "b" {
		t.Errorf("message 透传不对: %+v", frames)
	}
	if frames[0].Meta["category"] != core.CategoryText {
		t.Errorf("_meta.category = %v, want %v", frames[0].Meta["category"], core.CategoryText)
	}
	if frames[1].Meta["call_id"] != "c1" {
		t.Errorf("结构化字段应合并进 _meta: %+v", frames[1].Meta)
	}
}

// TestNotifyProgressWithoutToken 没有订阅就一帧都不发 ——
// 规范要求通知只能引用活跃请求里提供过的 token，凭空生成等于对空气推送。
func TestNotifyProgressWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	notifyProgress(rec, core.NewProgressPump(nil), core.CategoryText, "a", nil)
	if rec.Body.Len() != 0 {
		t.Fatalf("未订阅时不该写任何帧: %s", rec.Body.String())
	}
}
