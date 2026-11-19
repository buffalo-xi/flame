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
			avail, kind, why := pluginUnavailable("web_search", tc.pol, tc.active)
			if avail != tc.avail {
				t.Fatalf("avail = %v, want %v", avail, tc.avail)
			}
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if tc.avail {
				if !why.empty() {
					t.Errorf("可用时不该有原因，got %+v", why)
				}
				return
			}
			// 原因必须指名道姓：插件名 + 该去哪修（否则用户只能猜）
			if !strings.Contains(why.reason, "web_search") {
				t.Errorf("原因里应带插件名，got %q", why.reason)
			}
			for _, w := range tc.words {
				if !strings.Contains(why.reason, w) {
					t.Errorf("原因里应含 %q，got %q", w, why.reason)
				}
			}
			// 编码要能落到前端语言包，且参数带上插件名（否则英文界面拼不出句子）
			if why.code == "" {
				t.Error("不可用原因缺 code：前端只能显示中文原文")
			}
			if why.params["plugin"] != "web_search" {
				t.Errorf("params[plugin] = %q, want web_search", why.params["plugin"])
			}
		})
	}
}

// 外部工具（连接 / 端点）不可用也要有归因：都是租户自己能修的那一层。
func TestExternalIssueKinds(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		code  string
		param string
		issue func(string) (string, unavail)
		words []string
	}{
		{"mcp", UnavailableConnection, UnavailableCodeMcpConnection, "connection", mcpConnIssue, []string{"MCP 连接", "设置"}},
		{"rpc", UnavailableConnection, UnavailableCodeRpcConnection, "connection", rpcConnIssue, []string{"RPC 连接", "设置"}},
		{"endpoint", UnavailableEndpoint, UnavailableCodeEndpoint, "endpoint", endpointIssue, []string{"端点", "对外开放"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, why := tc.issue("crm")
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if !strings.Contains(why.reason, "crm") {
				t.Errorf("原因里应带名字，got %q", why.reason)
			}
			if why.code != tc.code {
				t.Errorf("code = %q, want %q", why.code, tc.code)
			}
			if why.params[tc.param] != "crm" {
				t.Errorf("params[%s] = %q, want crm", tc.param, why.params[tc.param])
			}
			for _, w := range tc.words {
				if !strings.Contains(why.reason, w) {
					t.Errorf("原因里应含 %q，got %q", w, why.reason)
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

// 原因编码取值同样是产品约定（前端按 toolUnavailable.<code> 查语言包）：
// 改了这里而没改语言包，界面上那段原因会整个消失 —— 钉住。
func TestUnavailableCodeValues(t *testing.T) {
	want := map[string]string{
		"capability":           UnavailableCodeCapability,
		"plugin_blocked":       UnavailableCodePluginBlocked,
		"plugin_disabled":      UnavailableCodePluginDisabled,
		"plugin_inactive":      UnavailableCodePluginInactive,
		"policy_read_failed":   UnavailableCodePolicyReadFail,
		"mcp_conn_disabled":    UnavailableCodeMcpConnection,
		"rpc_conn_disabled":    UnavailableCodeRpcConnection,
		"endpoint_not_exposed": UnavailableCodeEndpoint,
	}
	for k, v := range want {
		if k != v {
			t.Errorf("原因编码变了：%q != %q（语言包 toolUnavailable 需要同步）", k, v)
		}
	}
}
