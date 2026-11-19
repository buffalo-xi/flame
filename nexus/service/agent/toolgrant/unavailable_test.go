package toolgrant

import (
	"strings"
	"testing"

	"flame/nexus/shared/pluginpolicy"
)

// Attribution for an unavailable plugin (pluginUnavailable).
//
// Key rule: the platform layer wins. When the platform has not granted a plugin, the tenant enabling it has no effect - reporting
// "not enabled by the tenant" first would send the user to click enable and still find it unusable, a wasted round trip.
func TestPluginUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		pol    pluginpolicy.Policy
		active bool
		avail  bool
		kind   string
		words  []string
	}{
		{"not disabled and enabled by the tenant", pluginpolicy.Policy{Allowed: true}, true, true, "", nil},
		{"disabled for this tenant", pluginpolicy.Policy{}, true, false, UnavailablePluginPolicy, []string{"platform", "Plugins & Capabilities"}},
		{"not enabled by the tenant", pluginpolicy.Policy{Allowed: true}, false, false, UnavailablePluginInactive, []string{"Tools & Endpoints", "plugin"}},
		// When both layers fail, report the platform: fix the upper layer first, otherwise enabling still has no effect
		{"both layers fail, platform reported first", pluginpolicy.Policy{}, false, false, UnavailablePluginPolicy, []string{"platform"}},
		// A global block must not be described as "disabled for this tenant": it affects every tenant, and the next step is to ask
		// the platform to lift the gate
		{"blocked globally by the platform", pluginpolicy.Policy{Blocked: true}, true, false, UnavailablePluginPolicy, []string{"globally"}},
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
					t.Errorf("an available tool should have no reason, got %+v", why)
				}
				return
			}
			// The reason must name names: the plugin name plus where to fix it (otherwise the user can only guess)
			if !strings.Contains(why.reason, "web_search") {
				t.Errorf("the reason should carry the plugin name, got %q", why.reason)
			}
			for _, w := range tc.words {
				if !strings.Contains(why.reason, w) {
					t.Errorf("the reason should contain %q, got %q", w, why.reason)
				}
			}
			// The code must exist so the frontend can reach its locale bundle, with the plugin name in the params (otherwise an English
			// UI cannot build the sentence)
			if why.code == "" {
				t.Error("an unavailable reason is missing its code: the frontend can only show the raw text")
			}
			if why.params["plugin"] != "web_search" {
				t.Errorf("params[plugin] = %q, want web_search", why.params["plugin"])
			}
		})
	}
}

// External tools (connections / endpoints) need attribution too: that layer is what the tenant can fix itself.
func TestExternalIssueKinds(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		code  string
		param string
		issue func(string) (string, unavail)
		words []string
	}{
		{"mcp", UnavailableConnection, UnavailableCodeMcpConnection, "connection", mcpConnIssue, []string{"MCP connection", "Settings"}},
		{"rpc", UnavailableConnection, UnavailableCodeRpcConnection, "connection", rpcConnIssue, []string{"RPC connection", "Settings"}},
		{"endpoint", UnavailableEndpoint, UnavailableCodeEndpoint, "endpoint", endpointIssue, []string{"endpoint", "exposed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, why := tc.issue("crm")
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if !strings.Contains(why.reason, "crm") {
				t.Errorf("the reason should carry the name, got %q", why.reason)
			}
			if why.code != tc.code {
				t.Errorf("code = %q, want %q", why.code, tc.code)
			}
			if why.params[tc.param] != "crm" {
				t.Errorf("params[%s] = %q, want crm", tc.param, why.params[tc.param])
			}
			for _, w := range tc.words {
				if !strings.Contains(why.reason, w) {
					t.Errorf("the reason should contain %q, got %q", w, why.reason)
				}
			}
		})
	}
}

// The attribution values are a product contract (the frontend renders badges from them); changing one silently breaks the UI -
// pin them down.
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
			t.Errorf("an attribution value changed: %q != %q (the frontend mapping must be updated)", k, v)
		}
	}
}

// The reason codes are a product contract too (the frontend looks them up via toolUnavailable.<code>):
// changing one here without the locale bundle makes that reason vanish from the UI - pin them down.
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
			t.Errorf("a reason code changed: %q != %q (the toolUnavailable locale bundle must be updated)", k, v)
		}
	}
}
