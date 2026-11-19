package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// A name (or any non-UUID) in skill_id: the "use name for a name" guidance must be given right there, instead of dumping Postgres' 22P02
// (invalid input syntax for type uuid) on the model. skills.id is a uuid column, so querying it with a name is a bare SQL error with zero
// guidance for the model.
func TestFindRefRejectsNonUUIDSkillID(t *testing.T) {
	m := &skillManager{schema: "t0", agentID: "a1", isSupervisor: true}

	_, err := m.findRef(tools.SkillRef{ID: "english-joke-writing"})
	if err == nil {
		t.Fatal("a non-UUID skill_id should be an error")
	}
	for _, want := range []string{"english-joke-writing", "name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err is missing %q: %v", want, err)
		}
	}
}
