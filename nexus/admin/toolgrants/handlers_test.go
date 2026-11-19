package toolgrants

import (
	"testing"

	"flame/nexus/service/agent/toolgrant"
)

// Merging for the super agent must **keep** the unavailable marker instead of filtering the item out:
// the built-in tools are the fixed baseline, so a platform-disabled one is still listed (marked "not enabled"),
// hiding it would make the user think the tool disappeared.
func TestMergeSupervisorBuiltinsKeepsUnavailable(t *testing.T) {
	browser := toolgrant.ToolInfo{
		Ref: "builtin:browser", Name: "browser",
		Available:         false,
		UnavailableKind:   toolgrant.UnavailableCapability,
		UnavailableReason: "platform not enabled",
	}
	read := toolgrant.ToolInfo{Ref: "builtin:read_file", Name: "read_file", Available: true}
	plugin := toolgrant.ToolInfo{Ref: "plugin:web_search", Name: "web_search", Available: true}

	out := mergeSupervisorBuiltins([]toolgrant.ToolInfo{plugin}, nil, []toolgrant.ToolInfo{browser, read})
	if len(out) != 3 {
		t.Fatalf("expected 3 merged items (plugin + 2 built-ins), got %d: %+v", len(out), out)
	}

	var got *toolgrant.ToolInfo
	for i := range out {
		if out[i].Ref == "builtin:browser" {
			got = &out[i]
		}
	}
	if got == nil {
		t.Fatal("the unavailable built-in was dropped - the preview would say \"tool not found\" instead of \"not enabled\"")
	}
	if got.Available {
		t.Error("browser was marked available: the capability gate was lost during the merge")
	}
	if got.UnavailableKind != toolgrant.UnavailableCapability {
		t.Errorf("reason = %q, want %q", got.UnavailableKind, toolgrant.UnavailableCapability)
	}
}

// deny wins: even the built-in baseline cannot override an explicit denial.
func TestMergeSupervisorBuiltinsDenyWins(t *testing.T) {
	browser := toolgrant.ToolInfo{Ref: "builtin:browser", Name: "browser", Available: false}
	out := mergeSupervisorBuiltins(nil, []string{"builtin:browser"}, []toolgrant.ToolInfo{browser})
	if len(out) != 0 {
		t.Fatalf("a denied item was merged in: %+v", out)
	}
}

// The warning lists unavailable items only, as a single line (one per item would flood the UI).
func TestUnavailableRefs(t *testing.T) {
	list := []toolgrant.ToolInfo{
		{Ref: "builtin:read_file", Available: true},
		{Ref: "builtin:browser", Available: false, UnavailableKind: toolgrant.UnavailableCapability},
		{Ref: "builtin:exec", Available: false, UnavailableKind: toolgrant.UnavailableCapability},
	}
	got := unavailableRefs(list)
	if len(got) != 2 || got[0] != "builtin:browser" || got[1] != "builtin:exec" {
		t.Fatalf("unavailable refs = %v, want [builtin:browser builtin:exec]", got)
	}
	if len(unavailableRefs(nil)) != 0 {
		t.Error("an empty list should return empty")
	}
}
