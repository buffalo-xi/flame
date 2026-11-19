package agent

import (
	"testing"

	"flame/nexus/shared/models"
)

// core must leave the supervisor its "management entry": create / update / delete agents plus skill binding all live in this group.
// Without it the supervisor cannot even spin up a new agent, and the whole tenant loses its bootstrap ability.
//
// Note that this is not the old "migration marker": that one judged "has the core backfill run"
// (removed once the existing tenants were migrated), while this one judges "is the bootstrap ability still there", which holds for good.
func TestSupervisorCoreKeepsBootstrapAbility(t *testing.T) {
	const want = models.GrantRefBuiltinPrefix + "agent-admin"
	for _, ref := range models.SupervisorCoreGrantRefs {
		if ref == want {
			return
		}
	}
	t.Errorf("SupervisorCoreGrantRefs must contain %s (the supervisor's bootstrap ability)", want)
}
