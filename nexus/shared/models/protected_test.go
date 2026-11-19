package models

import "testing"

// The identity criterion (the is_supervisor attribute) and the protection list (preinstalled assets) are orthogonal, but they do combine on
// "may main be demoted": main is the preinstalled entry, so its attribute must stay constant.
func TestCanDemoteSupervisor(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"main", false}, // preinstalled supervisor: not demotable (otherwise a dead corner holding the entrance with no privilege)
		{"poet", true},  // an ordinary supervisor: demotable, subject only to the "last one" constraint
		{"", true},      // an empty name is not on the list; validation is the layer above
		{"Main", true},  // the list is case sensitive, consistent with IsProtectedAgent
	}
	for _, tc := range cases {
		if got := CanDemoteSupervisor(tc.name); got != tc.want {
			t.Errorf("CanDemoteSupervisor(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The protection list and the identity criterion must each mind its own business: adding a name to the list does not make it a supervisor, and
// switching is_supervisor on does not make an agent undeletable.
func TestProtectedIsNotSupervisor(t *testing.T) {
	if !IsProtectedAgent("main") {
		t.Fatal("main should be on the protection list (no delete / no rename)")
	}
	if IsProtectedAgent("poet") {
		t.Fatal("an ordinary agent must not be on the protection list")
	}
	// The supervisor criterion looks at the entity field only
	if IsSupervisor(&Agent{Name: "poet", IsSupervisor: true}) != true {
		t.Error("an agent carrying the attribute should be judged a supervisor")
	}
	if IsSupervisor(&Agent{Name: "main"}) {
		t.Error("an entity without the attribute is not a supervisor - the criterion never falls back to the name")
	}
	if IsSupervisor(nil) {
		t.Error("nil should safely return false")
	}
}

// The supervisor seed skills must all be cornerstones that may not be deleted or renamed: if the manuals bound automatically on grant could
// themselves be deleted, a new supervisor's bootstrap path would break at "anyone can delete the skill it bootstraps from".
func TestSupervisorSeedSkillsAreProtected(t *testing.T) {
	if len(SupervisorSeedSkills) == 0 {
		t.Fatal("SupervisorSeedSkills is empty")
	}
	for _, name := range SupervisorSeedSkills {
		if !IsProtectedSkill(name) {
			t.Errorf("%q is a supervisor seed skill but not on the protection list (it could be deleted / renamed)", name)
		}
	}
}
