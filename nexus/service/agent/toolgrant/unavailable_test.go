package toolgrant

import (
	"strings"
	"testing"

	"flame/nexus/shared/pluginpolicy"
)

// 插件不可用的归因（pluginUnavailable）。
//
// 关键约定：平台层优先。平台没开通插件时，租户启用也不会生效 ——
// 先报「租户未启用」会让用户去点启用，点完仍然不可用，白绕一圈。
func TestPluginUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		pol    pluginpolicy.Policy
		active bool
		avail  bool
		kind   string
		words  []string
	}{
		{"平台未停用且租户已启用", pluginpolicy.Policy{Allowed: true}, true, true, "", nil},
		{"平台已对本租户停用", pluginpolicy.Policy{}, true, false, UnavailablePluginPolicy, []string{"平台", "插件与能力"}},
		{"租户未启用", pluginpolicy.Policy{Allowed: true}, false, false, UnavailablePluginInactive, []string{"工具与端点", "插件"}},
		// 两层都不满足时报平台层：先解决上层，否则用户启用完还是不生效
		{"两层都不满足优先报平台", pluginpolicy.Policy{}, false, false, UnavailablePluginPolicy, []string{"平台"}},
		// 被全局停用时不许说成「本租户停用」：影响面是全租户，下一步是找平台解闸
		{"被平台全局停用", pluginpolicy.Policy{Blocked: true}, true, false, UnavailablePluginPolicy, []string{"全局停用"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			avail, kind, reason := pluginUnavailable("web_search", tc.pol, tc.active)
			if avail != tc.avail {
				t.Fatalf("avail = %v, want %v", avail, tc.avail)
			}
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if tc.avail {
				if reason != "" {
					t.Errorf("可用时不该有原因，got %q", reason)
				}
				return
			}
			// 原因必须指名道姓：插件名 + 该去哪修（否则用户只能猜）
			if !strings.Contains(reason, "web_search") {
				t.Errorf("原因里应带插件名，got %q", reason)
			}
			for _, w := range tc.words {
				if !strings.Contains(reason, w) {
					t.Errorf("原因里应含 %q，got %q", w, reason)
				}
			}
		})
	}
}

// 外部工具（连接 / 端点）不可用也要有归因：都是租户自己能修的那一层。
func TestExternalIssueKinds(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		issue func(string) (string, string)
		words []string
	}{
		{"mcp", UnavailableConnection, mcpConnIssue, []string{"MCP 连接", "设置"}},
		{"rpc", UnavailableConnection, rpcConnIssue, []string{"RPC 连接", "设置"}},
		{"endpoint", UnavailableEndpoint, endpointIssue, []string{"端点", "对外开放"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, reason := tc.issue("crm")
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if !strings.Contains(reason, "crm") {
				t.Errorf("原因里应带名字，got %q", reason)
			}
			for _, w := range tc.words {
				if !strings.Contains(reason, w) {
					t.Errorf("原因里应含 %q，got %q", w, reason)
				}
			}
		})
	}
}

// 归因取值是产品约定（前端按它出徽标文案），改动即 UI 静默失效 —— 钉住。
func TestUnavailableKindValues(t *testing.T) {
	want := map[string]string{
		"capability":      UnavailableCapability,
		"plugin_policy":   UnavailablePluginPolicy,
		"plugin_inactive": UnavailablePluginInactive,
		"connection":      UnavailableConnection,
		"endpoint":        UnavailableEndpoint,
	}
	for k, v := range want {
		if k != v {
			t.Errorf("归因取值变了：%q != %q（前端 mapping 需要同步）", k, v)
		}
	}
}
