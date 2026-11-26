package tools

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
)

// The owner-targeting meta tools (create / update / delete_agent_skill, skill binding, tool grants, call grants) share one status
// gate: the target agent must be in the normal state (criterion: models.IsModifiable).
//
// It is the same source as the file gate (checkWrite / checkWriteOwn in gate.go) and skillManager.checkOwnerAgent - once the two
// diverge you get "the meta tool can change it, the file tool errors out".
func TestResolveTargetAgentStatusGate(t *testing.T) {
	c := &Context{
		Schema:    "t0",
		AgentName: "main",
		Agents: stubAgentsByName{byName: map[string]*AgentInfo{
			"gone":   {ID: "a1", Name: "gone", Status: models.StatusDeprecated},
			"normal": {ID: "a2", Name: "normal", Status: models.StatusNormal},
		}},
		Skills: stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{}},
	}
	args := func(name string) map[string]any { return map[string]any{"agent": name} }
	ctx := context.Background()

	tests := []struct {
		name    string
		agent   string
		wantErr bool
	}{
		{"deprecated: refused", "gone", true},
		{"normal: allowed", "normal", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveTargetAgent(ctx, c, args(tc.agent))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Deleting is the one exception to that gate: a private skill of a deprecated agent can be removed without reactivating
// the agent first (the agent itself is deletable in that state), so the delete-side resolver only checks existence.
func TestResolveTargetAgentForDeleteSkipsStatusGate(t *testing.T) {
	c := &Context{
		Schema: "t0",
		Agents: stubAgentsByName{byName: map[string]*AgentInfo{
			"gone": {ID: "a1", Name: "gone", Status: models.StatusDeprecated},
		}},
		Skills: stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{}},
	}
	name, id, err := resolveTargetAgentForDelete(context.Background(), c, map[string]any{"agent": "gone"})
	if err != nil {
		t.Fatalf("resolveTargetAgentForDelete(deprecated) = %v, want nil", err)
	}
	if name != "gone" || id != "a1" {
		t.Fatalf("resolved = (%q, %q), want (gone, a1)", name, id)
	}
	if _, _, err := resolveTargetAgentForDelete(context.Background(), c, map[string]any{"agent": "missing"}); err == nil {
		t.Fatal("resolveTargetAgentForDelete(missing) = nil, want an error")
	}
}
