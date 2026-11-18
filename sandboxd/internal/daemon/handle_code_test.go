package daemon

import (
	"testing"
	"time"
)

// codeDeadline 的不变量：调用方下发的超时**只能收紧**交互上限，不能放宽。
//
// 这条不变量就是踩过的坑：nexus 侧把 script.<lang>.timeout 配成 600，以为能给脚本
// 10 分钟，实际被这里的 120s 削掉 —— 而当时这段逻辑既没有测试、也没有日志，
// 只能靠读代码才能发现「配了等于没配」。
func TestScriptDeadline(t *testing.T) {
	const maxSec = 120

	cases := []struct {
		name string
		req  time.Duration
		want time.Duration
	}{
		// 没有下发超时（0 或负）：用硬闸兜底。
		{"未下发 → 硬闸", 0, maxSec * time.Second},
		{"负值（视为未设置）→ 硬闸", -3 * time.Second, maxSec * time.Second},
		// 下发值更小：收紧，这是唯一能改变实际上限的方向。
		{"下发 5s → 5s", 5 * time.Second, 5 * time.Second},
		{"下发 119s → 119s", 119 * time.Second, 119 * time.Second},
		// 下发值更大：**不放宽**（600 是历史配置里真实出现过的值）。
		{"下发 600s → 仍是硬闸", 600 * time.Second, maxSec * time.Second},
		// 相等：不制造歧义。
		{"下发 120s → 120s", 120 * time.Second, maxSec * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeDeadline(tc.req, maxSec); got != tc.want {
				t.Errorf("codeDeadline(%v, %d) = %v, want %v", tc.req, maxSec, got, tc.want)
			}
		})
	}
}

// 硬闸本身随配置走：改 code_max_sec 必须立刻改变实际上限，
// 否则「调大硬闸」这个动作就是无效的。
func TestScriptDeadlineFollowsConfig(t *testing.T) {
	// 同一个下发值，硬闸不同 → 结果不同。
	if got := codeDeadline(300*time.Second, 600); got != 300*time.Second {
		t.Errorf("硬闸 600s 时下发 300s 应为 300s, got %v", got)
	}
	if got := codeDeadline(300*time.Second, 120); got != 120*time.Second {
		t.Errorf("硬闸 120s 时下发 300s 应为 120s, got %v", got)
	}
}
