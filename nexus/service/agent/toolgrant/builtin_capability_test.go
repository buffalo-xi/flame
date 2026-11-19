package toolgrant

import (
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/capability"
)

// The capability gate must sit on the leaf list (builtinTools): the super agent's merge, Resolve's availability filter and the catalog
// interface all take tools from it. Miss this layer and you get "the group says not enabled while the effective tool says available".
func TestBuiltinToolsCapabilityGate(t *testing.T) {
	c := &Catalog{caps: map[string]bool{capability.Browser: true}}
	var blocked, allowed int
	for _, ti := range c.builtinTools() {
		switch tools.CapabilityOf(builtinCategory(ti)) {
		case capability.Browser:
			allowed++
			if !ti.Available {
				t.Errorf("%s has its capability granted but is marked unavailable", ti.Ref)
			}
			if ti.UnavailableKind != "" || ti.UnavailableReason != "" {
				t.Errorf("%s is available and should carry no attribution: kind=%q reason=%q", ti.Ref, ti.UnavailableKind, ti.UnavailableReason)
			}
		case capability.Exec:
			blocked++
			if ti.Available {
				t.Errorf("%s lacks its capability but is still available", ti.Ref)
			}
			if ti.UnavailableKind != UnavailableCapability {
				t.Errorf("%s attribution = %q, want %q", ti.Ref, ti.UnavailableKind, UnavailableCapability)
			}
			if ti.UnavailableReason == "" {
				t.Errorf("%s is missing the unavailable reason", ti.Ref)
			}
		}
	}
	if blocked == 0 || allowed == 0 {
		t.Fatalf("the test premise does not hold: the registry has no exec/browser tools (exec=%d browser=%d)", blocked, allowed)
	}
}

// A nil view = no gate (only for callers with no tenant semantics; at runtime it is always non-nil).
func TestBuiltinToolsWithoutCapabilityView(t *testing.T) {
	c := &Catalog{}
	for _, ti := range c.builtinTools() {
		if !ti.Available {
			t.Errorf("%s should not be gated (no capability view injected)", ti.Ref)
		}
	}
}

// Every built-in tool's category must be in BuiltinCategories, otherwise Groups() does not produce that group and the tool simply
// disappears from the grant catalog (the easiest step to miss when splitting shell / script into tiers).
func TestBuiltinCategoriesCoverAllTools(t *testing.T) {
	cats := BuiltinCategories()
	for _, tl := range tools.List() {
		m := tl.Meta()
		if !cats[m.Category] {
			t.Errorf("tool %s has category %q which is not in BuiltinCategories, so the grant catalog will not show it", m.Name, m.Category)
		}
	}
}

// The viewpoint filter and the capability gate apply together: a non-super agent does not see the super category, and capabilities are
// still marked.
func TestBuiltinToolsSupervisorOnlyFilter(t *testing.T) {
	c := &Catalog{caps: map[string]bool{}}
	for _, ti := range c.builtinTools() {
		if ti.SupervisorOnly {
			t.Errorf("a non-super-agent viewpoint must not contain %s", ti.Ref)
		}
	}

	sup := &Catalog{isSupervisor: true, caps: map[string]bool{}}
	var superSeen bool
	for _, ti := range sup.builtinTools() {
		if ti.SupervisorOnly {
			superSeen = true
		}
	}
	if !superSeen {
		t.Fatal("a supervisor viewpoint should see the super-category tools")
	}
}
