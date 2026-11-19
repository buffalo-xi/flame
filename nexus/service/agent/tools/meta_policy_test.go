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
