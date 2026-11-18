package tools

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
)

// 归属类元工具（create / update / delete_agent_skill、技能绑定、工具授权、调用授权）
// 共用同一条状态闸：目标 agent 必须是正常态（判据 models.IsModifiable）。
//
// 与文件闸门（gate.go 的 checkWrite / checkWriteOwn）和 skillManager.checkOwnerAgent
// 同源 —— 两处一旦分叉，就会出现「元工具能改、文件工具报错」的分裂。
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
		{"废弃：拒绝", "gone", true},
		{"正常态：放行", "normal", false},
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
