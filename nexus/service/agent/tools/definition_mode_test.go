package tools

import (
	"path/filepath"
	"testing"
)

// 草稿模式必须与改造前完全一致：编辑、调试、试聊都靠它，
// 任何路径漂移都会表现为「读不到 AGENT.md / 跑不到技能脚本」。
func TestDraftModePathsUnchanged(t *testing.T) {
	c := &Context{
		Schema:    "t0",
		AgentName: "poet",
		RootDir:   "/root",
		AgentRoot: "/root/tenants/t0/agents/poet",
		SkillRoot: "/root/tenants/t0/skills",
		ToolsRoot: "/root/tenants/t0/tools",
	}
	if c.VersionBound() {
		t.Fatal("零值 DefinitionMode 不应绑定版本（默认必须是草稿）")
	}
	if got, want := c.AgentPath("AGENT.md"), "tenants/t0/agents/poet/AGENT.md"; got != want {
		t.Fatalf("AgentPath = %q, want %q", got, want)
	}

	key, host, err := c.SkillReadDir("", "verse")
	if err != nil {
		t.Fatalf("租户级技能目录解析失败: %v", err)
	}
	if want := "tenants/t0/skills/verse"; key != want {
		t.Fatalf("tenant skill key = %q, want %q", key, want)
	}
	if want := filepath.Join("/root", "tenants/t0/skills/verse"); host != want {
		t.Fatalf("tenant skill host = %q, want %q", host, want)
	}

	key, _, err = c.SkillReadDir("poet", "verse")
	if err != nil {
		t.Fatalf("私有技能目录解析失败: %v", err)
	}
	if want := "tenants/t0/agents/poet/skills/verse"; key != want {
		t.Fatalf("private skill key = %q, want %q", key, want)
	}

	dir, err := c.ToolDir("calc")
	if err != nil {
		t.Fatalf("工具目录解析失败: %v", err)
	}
	if want := filepath.Join("/root/tenants/t0/tools", "calc"); dir != want {
		t.Fatalf("tool dir = %q, want %q", dir, want)
	}
}

// 正式运行读版本目录：AGENT.md 落在 versions/agents/<name>/v<n>/，
// 私有技能落在同一份版本快照的 skills/ 下（私有技能没有独立版本号）。
func TestCurrentModePathsUseVersionDir(t *testing.T) {
	c := &Context{
		Schema:         "t0",
		AgentName:      "poet",
		DefinitionMode: ModeCurrent,
		AgentVersion:   3,
		RootDir:        "/root",
	}
	if !c.VersionBound() {
		t.Fatal("current + 有版本号应视为已绑定")
	}
	if got, want := c.AgentPath("AGENT.md"), "tenants/t0/versions/agents/poet/v3/AGENT.md"; got != want {
		t.Fatalf("AgentPath = %q, want %q", got, want)
	}

	// 自己的私有技能：走本 agent 的版本快照，不需要查库
	key, host, err := c.SkillReadDir("poet", "verse")
	if err != nil {
		t.Fatalf("私有技能版本目录解析失败: %v", err)
	}
	if want := "tenants/t0/versions/agents/poet/v3/skills/verse"; key != want {
		t.Fatalf("private skill version key = %q, want %q", key, want)
	}
	if want := filepath.Join("/root", "tenants/t0/versions/agents/poet/v3/skills/verse"); host != want {
		t.Fatalf("private skill version host = %q, want %q", host, want)
	}
}

// 没有存储根（对象存储 / 单测）时，正式运行必须明确失败 ——
// 拿一个空路径去挂沙箱只会得到「目录不存在」这种更难查的错误。
func TestCurrentModeWithoutRootDirFails(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "poet", DefinitionMode: ModeCurrent, AgentVersion: 1}
	if _, _, err := c.SkillReadDir("poet", "verse"); err == nil {
		t.Fatal("缺 RootDir 时应报错，而不是返回空宿主路径")
	}
}

// 子调用模式：唯一的公式是「子草稿 = 自身草稿 && FollowDrafts」。
// 这张真值表就是「正式入口不扩散草稿」的护栏 —— 任何一格改了都得先想清楚这里。
func TestChildMode(t *testing.T) {
	cases := []struct {
		name   string
		self   DefinitionMode
		follow bool
		want   DefinitionMode
	}{
		{"聊天/调试 + 开关开", ModeDraft, true, ModeDraft},
		{"聊天/调试 + 开关关", ModeDraft, false, ModeCurrent},
		{"正式入口 + 开关开（不生效）", ModeCurrent, true, ModeCurrent},
		{"正式入口 + 开关关", ModeCurrent, false, ModeCurrent},
	}
	for _, tc := range cases {
		c := &Context{DefinitionMode: tc.self, FollowDrafts: tc.follow}
		if got := c.ChildMode(); got != tc.want {
			t.Errorf("%s: ChildMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}
