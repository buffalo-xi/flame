package toolgrant

import (
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// Semantics of the platform capability view (Catalog.capabilityBlocked):
//
//	nil view          -> nothing blocked (callers without tenant semantics only; at runtime it is always non-nil)
//	key = true        -> allowed
//	missing / false   -> blocked, and the reason carries the capability key (so the user knows to ask the platform)
//
// Only this pure logic is tested here: Groups() touches the connection / plugin repositories and belongs to another layer, while "the
// group's availability follows the capability" ultimately converges on this single check.
func TestCapabilityBlocked(t *testing.T) {
	cases := []struct {
		name     string
		caps     map[string]bool
		key      string
		blocked  bool
		wantWord string
	}{
		{"no view injected", nil, capability.Exec, false, ""},
		{"granted", map[string]bool{capability.Exec: true}, capability.Exec, false, ""},
		{"explicitly off", map[string]bool{capability.Exec: false}, capability.Exec, true, "sandbox.exec"},
		{"missing key means not granted", map[string]bool{capability.Browser: true}, capability.Exec, true, "sandbox.exec"},
		{"a tool with no capability is unconstrained", map[string]bool{}, "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Catalog{caps: tc.caps}
			blocked, why := c.capabilityBlocked(tc.key)
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v", blocked, tc.blocked)
			}
			if tc.blocked {
				if !strings.Contains(why.reason, tc.wantWord) {
					t.Errorf("the reason should carry the capability key %q, got %q", tc.wantWord, why.reason)
				}
				if !strings.Contains(why.reason, "platform") {
					t.Errorf("the reason should say the platform has not granted it, got %q", why.reason)
				}
				// The frontend localises from code + params: without them only the raw text is left
				if why.code != UnavailableCodeCapability {
					t.Errorf("code = %q, want %q", why.code, UnavailableCodeCapability)
				}
				if why.params["key"] != tc.key {
					t.Errorf("params[key] = %q, want %q", why.params["key"], tc.key)
				}
			} else if !why.empty() {
				t.Errorf("an allowed capability should have no reason, got %+v", why)
			}
		})
	}
}

// WithCapabilities is a chainable injection and must return the same Catalog (callers build it as a chain).
func TestWithCapabilitiesChain(t *testing.T) {
	c := &Catalog{}
	got := c.WithCapabilities(map[string]bool{capability.Exec: true})
	if got != c {
		t.Fatal("WithCapabilities should return the receiver")
	}
	if !got.caps[capability.Exec] {
		t.Error("the view was not injected")
	}
}
