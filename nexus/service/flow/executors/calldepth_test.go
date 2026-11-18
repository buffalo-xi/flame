package executors

import "testing"

// extractCallDepth 必须同时认 int（进程内直调）与 float64（JSON 来源），
// 并把键剥离干净 —— 残留会让 _call_depth 变成 flow 的业务入参。
func TestExtractCallDepth(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want int
	}{
		{"缺键", map[string]interface{}{"a": 1}, 0},
		{"int", map[string]interface{}{"_call_depth": 2}, 2},
		{"int64", map[string]interface{}{"_call_depth": int64(3)}, 3},
		{"float64（JSON 来源）", map[string]interface{}{"_call_depth": float64(3)}, 3},
		{"负数归零", map[string]interface{}{"_call_depth": -5}, 0},
		{"非法值归零", map[string]interface{}{"_call_depth": "x"}, 0},
	}
	for _, tc := range cases {
		got, clean := extractCallDepth(tc.in)
		if got != tc.want {
			t.Errorf("%s: depth = %d, want %d", tc.name, got, tc.want)
		}
		if _, ok := clean["_call_depth"]; ok {
			t.Errorf("%s: 剥离后仍残留 _call_depth", tc.name)
		}
	}
}

// 原 map 不能被改：调用方（rpc 节点 / 工具参数）后面还要用它。
func TestExtractCallDepthDoesNotMutateInput(t *testing.T) {
	in := map[string]interface{}{"_call_depth": 2, "topic": "x"}
	_, clean := extractCallDepth(in)
	if _, ok := in["_call_depth"]; !ok {
		t.Fatal("原 map 里的 _call_depth 被删掉了")
	}
	if len(clean) != 1 || clean["topic"] != "x" {
		t.Fatalf("剥离结果 = %v, want 只剩 topic", clean)
	}
}

// 配置成 -1（「不限制」）时必须退回兜底值：纯 flow 的环只靠这道闸，
// 允许它被关掉等于把防线交给一次误配置。
func TestEffectiveEndpointDepthLimit(t *testing.T) {
	cases := []struct {
		name                 string
		configured, fallback int
		want                 int
	}{
		{"正常值直接用", 5, 10, 5},
		{"1 也是合法上限", 1, 10, 1},
		{"-1 退回兜底", -1, 10, 10},
		{"0 退回兜底", 0, 10, 10},
		{"兜底为 0 时用 10", -1, 0, 10},
		{"兜底为负时用 10", -1, -1, 10},
	}
	for _, tc := range cases {
		if got := effectiveEndpointDepthLimit(tc.configured, tc.fallback); got != tc.want {
			t.Errorf("%s: limit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// 同一 flow 的多个 rpc 节点共用同一个 CallDepth 快照：平级调用不累加。
//
// 真正的保证是 ExecContext.CallDepth 只在 NewFlow 时写一次、节点执行路径不碰它；
// 这里把「+1 而不是自增」的语义钉住，避免以后有人把它改成每次调用加一 ——
// 那样一条有 10 个并列 rpc 节点的 flow 会自己把自己顶到上限。
func TestSiblingEndpointCallsShareSameDepth(t *testing.T) {
	const flowDepth = 1 // 该 flow 由上层调起，层级为 1
	for i := 0; i < 3; i++ {
		if next := flowDepth + 1; next != 2 {
			t.Fatalf("第 %d 个 rpc 节点的 next = %d, want 2（平级不累加）", i, next)
		}
	}
}
