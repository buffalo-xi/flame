package core

import (
	"encoding/json"
	"testing"
)

// TestValidProgressToken 规范约束：progressToken 必须是 string 或 integer。
// 其他形态（含 1.5 这种「浮点但非整数」）一律视为未提供 —— 当作没订阅，
// 而不是带着一个非法 token 去推送。
func TestValidProgressToken(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"string", "tok-1", true},
		{"整数 float64（JSON 解码后的常态）", float64(7), true},
		{"int", 7, true},
		{"int64", int64(7), true},
		{"json.Number", json.Number("7"), true},
		{"空串", "", false},
		{"非整数", 1.5, false},
		{"bool", true, false},
		{"nil", nil, false},
		{"对象", map[string]any{"a": 1}, false},
		{"数组", []any{1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidProgressToken(c.in); got != c.want {
				t.Fatalf("ValidProgressToken(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestProgressPumpEnabled 没有 token 就是「没订阅」：调用方据此走同步路径。
func TestProgressPumpEnabled(t *testing.T) {
	if NewProgressPump(nil).Enabled() {
		t.Fatal("无 token 时不该 Enabled")
	}
	var nilPump *ProgressPump
	if nilPump.Enabled() {
		t.Fatal("nil pump 不该 Enabled（且不应 panic）")
	}
	if !NewProgressPump("tok").Enabled() {
		t.Fatal("有 token 时应 Enabled")
	}
}

// TestStripLegacyStream 废弃的 arguments.stream 必须被剥掉：
// 既不再影响判定（判定只看 progressToken），也不该作为业务变量流进 flow / agent。
// 返回值用于「要不要记日志」，所以两种情形要分得开。
func TestStripLegacyStream(t *testing.T) {
	input := map[string]any{"stream": true, "city": "sh"}
	if !StripLegacyStream(input) {
		t.Fatal("存在 stream 时应返回 true")
	}
	if _, existed := input["stream"]; existed {
		t.Fatal("stream 应被剥掉")
	}
	if input["city"] != "sh" {
		t.Fatal("其它业务参数不该被动过")
	}

	// 没有这个键：无操作，且不能凭空塞一个
	clean := map[string]any{"city": "sh"}
	if StripLegacyStream(clean) {
		t.Fatal("不存在 stream 时应返回 false")
	}
	if len(clean) != 1 {
		t.Fatalf("无 stream 时不该改动 map: %v", clean)
	}
}

// TestProgressPumpNext 序号严格从 1 递增：规范要求 progress 每次 MUST 递增。
func TestProgressPumpNext(t *testing.T) {
	p := NewProgressPump("tok")
	for want := 1.0; want <= 3; want++ {
		if got := p.Next(); got != want {
			t.Fatalf("Next() = %v, want %v", got, want)
		}
	}
}
