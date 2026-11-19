package handler

import (
	"testing"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
)

// The three states must stay distinguishable: null (never set = not enabled) and false (explicitly off) are not the same thing.
// Collapsing them into one bool would let the edit form silently write "unset" as "off", and an administrator could no longer
// tell "I turned it off" from "it was never on".
func TestBuildCapabilityViews(t *testing.T) {
	on := capabilitypolicy.Policy{Allowed: true}
	off := capabilitypolicy.Policy{Allowed: false}

	// 1) Nothing configured: not enabled (deny by default)
	got := buildCapabilityViews(nil, nil)
	if len(got) != len(capability.All()) {
		t.Fatalf("should list every known capability, got %d", len(got))
	}
	for _, v := range got {
		if v.Allowed || v.Row != nil || v.Blocked {
			t.Errorf("%s with no config should be \"not enabled + no row + not blocked\", got %+v", v.Key, v)
		}
	}

	// 2) Tenant row true: enabled; Row is echoed back unchanged for the edit form
	got = buildCapabilityViews(map[string]capabilitypolicy.Policy{capability.Browser: on}, nil)
	for _, v := range got {
		switch v.Key {
		case capability.Browser:
			if !v.Allowed || v.Row == nil || !*v.Row || v.Blocked {
				t.Errorf("browser with an allowed row should be \"enabled + Row=true\", got %+v", v)
			}
		default:
			if v.Allowed {
				t.Errorf("%s was not configured and must not be enabled, got %+v", v.Key, v)
			}
		}
	}

	// 3) Explicitly off: Allowed=false but Row is not nil (the form must show "off" rather than "unset")
	got = buildCapabilityViews(map[string]capabilitypolicy.Policy{capability.Browser: off}, nil)
	for _, v := range got {
		if v.Key != capability.Browser {
			continue
		}
		if v.Allowed {
			t.Error("an explicit off must not take effect as enabled")
		}
		if v.Row == nil || *v.Row {
			t.Error("Row should reflect this tenant's false unchanged (it must not be flattened by the default)")
		}
	}

	// 4) The global gate overrides a tenant's explicit enable without rewriting the raw row - otherwise the UI would show "the
	// tenant turned it off", and once an administrator lifts the gate the capability comes back, misleading the investigation.
	got = buildCapabilityViews(
		map[string]capabilitypolicy.Policy{capability.Browser: on},
		map[string]bool{capability.Browser: true},
	)
	for _, v := range got {
		if v.Key != capability.Browser {
			continue
		}
		if v.Allowed {
			t.Error("a blocked global gate should override the tenant row's true")
		}
		if !v.Blocked {
			t.Error("Blocked should reflect the gate state truthfully (the UI marks it as blocked by the platform)")
		}
		if v.Row == nil || !*v.Row {
			t.Error("Row must still be the tenant's original true (a hard gate does not modify the raw config)")
		}
	}
}

// Dependencies only apply to net.egress, and are "exec or shell, either one" - both channels can execute arbitrary code, so
// accepting only exec would lock out tenants that enabled just shell. Every other key is independent and must not grow
// dependencies out of nowhere.
func TestCapabilityDeps(t *testing.T) {
	deps := capabilityDeps(capability.Egress)
	if len(deps) != 2 || deps[0] != capability.Exec || deps[1] != capability.Shell {
		t.Errorf("capabilityDeps(%q) = %v, want [%q %q]",
			capability.Egress, deps, capability.Exec, capability.Shell)
	}
	for _, key := range capability.All() {
		if key == capability.Egress {
			continue
		}
		if got := capabilityDeps(key); len(got) != 0 {
			t.Errorf("capabilityDeps(%q) = %v, want no dependencies", key, got)
		}
	}
}

// A new key must go into All / Valid / Label together - missing one produces a deviation that only works in half the places
// ("not listed in the UI" or "rejected by the API"), and neither reports an error.
func TestEgressKeyRegistered(t *testing.T) {
	if !capability.Valid(capability.Egress) {
		t.Error("net.egress must be a valid key (otherwise PUT rejects it outright)")
	}
	if capability.Label(capability.Egress) == capability.Egress {
		t.Error("net.egress has no Label (the UI would show the bare key name)")
	}
	var listed bool
	for _, key := range capability.All() {
		if key == capability.Egress {
			listed = true
		}
	}
	if !listed {
		t.Error("net.egress is absent from All (the capability grant card would not list this row)")
	}
}
