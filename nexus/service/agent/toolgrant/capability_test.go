package toolgrant

import (
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// 平台能力视图的语义（Catalog.capabilityBlocked）：
//
//	nil 视图       → 不拦（仅无租户语义的调用方；运行时恒为「非 nil」）
//	键 = true      → 放行
//	缺键 / false   → 拦住，且原因里带上能力名（用户据此去平台开通）
//
// 只测这一段纯逻辑：Groups() 会碰连接/插件仓储，属于另一层的事；
// 而「组可用性由能力决定」最终都收敛到这一个判据上。
func TestCapabilityBlocked(t *testing.T) {
	cases := []struct {
		name     string
		caps     map[string]bool
		key      string
		blocked  bool
		wantWord string
	}{
		{"未注入视图", nil, capability.Exec, false, ""},
		{"已开通", map[string]bool{capability.Exec: true}, capability.Exec, false, ""},
		{"显式关闭", map[string]bool{capability.Exec: false}, capability.Exec, true, "sandbox.exec"},
		{"缺键即未开通", map[string]bool{capability.Browser: true}, capability.Exec, true, "sandbox.exec"},
		{"无能力归属的工具不受约束", map[string]bool{}, "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Catalog{caps: tc.caps}
			blocked, reason := c.capabilityBlocked(tc.key)
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v", blocked, tc.blocked)
			}
			if tc.blocked {
				if !strings.Contains(reason, tc.wantWord) {
					t.Errorf("原因里应带能力名 %q，got %q", tc.wantWord, reason)
				}
				if !strings.Contains(reason, "平台") {
					t.Errorf("原因要说明是平台未开通，got %q", reason)
				}
			} else if reason != "" {
				t.Errorf("放行时不该有原因，got %q", reason)
			}
		})
	}
}

// WithCapabilities 是链式注入，必须返回同一个 Catalog（调用方写成链式构造）。
func TestWithCapabilitiesChain(t *testing.T) {
	c := &Catalog{}
	got := c.WithCapabilities(map[string]bool{capability.Exec: true})
	if got != c {
		t.Fatal("WithCapabilities 应返回自身")
	}
	if !got.caps[capability.Exec] {
		t.Error("视图没被注入")
	}
}
