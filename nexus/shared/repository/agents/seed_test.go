package repository

import (
	"testing"

	"flame/nexus/shared/models"
)

// The "not customised" criterion must be strict equality.
//
// Granting the supervisor privilege uses it to decide whether the existing required rows should be deleted wholesale and
// replaced by the core groups. Loosening it to a subset test would also overwrite grants an administrator trimmed by hand
// — that is losing configuration, not upgrading it.
func TestIsDefaultAgentSeedOnly(t *testing.T) {
	seed := models.DefaultAgentToolGrants()
	refs := make([]string, 0, len(seed))
	for _, it := range seed {
		refs = append(refs, it.Ref)
	}

	if !isDefaultAgentSeedOnly(refs) {
		t.Fatal("an exact match with the default seed should count as not customised")
	}

	if isDefaultAgentSeedOnly(refs[:len(refs)-1]) {
		t.Error("one row short must not count as not customised (an administrator may have trimmed it)")
	}

	more := append(append([]string{}, refs...), models.GrantRefBuiltinPrefix+"exec")
	if isDefaultAgentSeedOnly(more) {
		t.Error("one row extra must not count as not customised")
	}

	if isDefaultAgentSeedOnly(nil) {
		t.Error("no grants is not \"equal to the seed\", it is \"never configured\"")
	}

	// The core groups (6) and the default seed (12 single tools) have completely different shapes and must not be confused.
	core := make([]string, 0, len(models.SupervisorCoreGrantRefs))
	core = append(core, models.SupervisorCoreGrantRefs...)
	if isDefaultAgentSeedOnly(core) {
		t.Error("the core groups must not count as the default seed")
	}
}
