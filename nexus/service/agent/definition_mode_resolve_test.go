package agent

import (
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
	"flame/nexus/shared/storage"
)

// 自己读哪份：普通 agent 按入口给的模式，supervisor 一律草稿。
// supervisor 那条是「管理能力」的地基 —— 它建的东西都在草稿里，读发布版就看不见自己的成果。
func TestResolveDefinitionMode(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		supervisor bool
		want       tools.DefinitionMode
	}{
		{"普通 + 正式入口", string(tools.ModeCurrent), false, tools.ModeCurrent},
		{"普通 + 未传（草稿）", "", false, tools.ModeDraft},
		{"supervisor + 正式入口仍草稿", string(tools.ModeCurrent), true, tools.ModeDraft},
		{"supervisor + 未传", "", true, tools.ModeDraft},
	}
	for _, tc := range cases {
		if got := resolveDefinitionMode(tc.raw, tc.supervisor); got != tc.want {
			t.Errorf("%s: resolveDefinitionMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// 正式运行绑定到版本：这一版里读不到 AGENT.md 必须报错。
// 静默回退默认提示等于「线上 agent 偷偷换了身份」，而沙箱 /agent 这时也多半是个空目录
// （docker 会把不存在的宿主路径建成空目录），两类故障会互相掩盖。
func TestLoadSystemPromptFailsWithoutVersionEntry(t *testing.T) {
	root := t.TempDir()
	a := &agentImpl{
		instance: &models.Agent{Name: "poet"},
		toolCtx: &tools.Context{
			Schema:         "t0",
			AgentName:      "poet",
			RootDir:        root,
			DefinitionMode: tools.ModeCurrent,
			AgentVersion:   3,
			Storage:        storage.NewFilesystemStorage(root),
		},
	}
	if _, err := a.loadSystemPrompt(); err == nil {
		t.Fatal("版本目录里没有 AGENT.md 时应报错，不能静默用默认提示")
	}
}

// 子调用读哪份：三条规则按顺序生效（supervisor 恒开 → 自身正式恒关 → 看开关缺省开）。
func TestResolveFollowDrafts(t *testing.T) {
	cases := []struct {
		name       string
		self       tools.DefinitionMode
		raw        any
		supervisor bool
		want       bool
	}{
		{"普通 + 正式入口 + 未传", tools.ModeCurrent, nil, false, false},
		{"普通 + 正式入口 + 传 true 也不扩散", tools.ModeCurrent, true, false, false},
		{"普通 + 草稿 + 未传（缺省开）", tools.ModeDraft, nil, false, true},
		{"普通 + 草稿 + 关", tools.ModeDraft, false, false, false},
		{"普通 + 草稿 + 显式开", tools.ModeDraft, true, false, true},
		{"supervisor + 显式关也强制开", tools.ModeDraft, false, true, true},
		{"supervisor + 正式入口也开", tools.ModeCurrent, nil, true, true},
	}
	for _, tc := range cases {
		if got := resolveFollowDrafts(tc.self, tc.raw, tc.supervisor); got != tc.want {
			t.Errorf("%s: resolveFollowDrafts = %v, want %v", tc.name, got, tc.want)
		}
	}
}
