package tools

import (
	"context"
	"testing"
)

// 「用过即注入」的记账：use_skill 成功后登记技能，agent 循环据此在下一轮重算工具集。
// 三个不变量必须成立，否则要么白查库、要么工具永远不出现：
//  1. 新技能才置脏（重复读同一技能不算变化）；
//  2. 脏标记只能被消费一次（否则每轮都重算）；
//  3. 快照是拷贝（调用方改它不能污染内部状态）。
func TestSkillLoadedBookkeeping(t *testing.T) {
	c := &Context{}

	if got := c.LoadedSkills(); len(got) != 0 {
		t.Fatalf("初始已读技能应为空，got %v", got)
	}
	if c.ConsumeSkillsDirty() {
		t.Fatal("初始不应为脏")
	}

	// 空 id 是无效登记（例如详情里没带回 id），不能污染状态
	c.MarkSkillLoaded("", "ghost", nil)
	if len(c.LoadedSkills()) != 0 || c.ConsumeSkillsDirty() {
		t.Fatal("空 id 不应登记，也不应置脏")
	}

	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:exec"})
	got := c.LoadedSkills()
	if got["id-1"] != "custom-tool-authoring" {
		t.Fatalf("登记失败，got %v", got)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("新登记必须置脏（否则 agent 循环不会重算工具集）")
	}
	if c.ConsumeSkillsDirty() {
		t.Fatal("脏标记只能被消费一次（否则每轮都查库）")
	}

	// 依赖声明随登记带走，且快照是拷贝（调用方改它不能污染内部状态）
	entries := c.LoadedSkillEntries()
	if len(entries) != 1 || entries[0].ID != "id-1" || len(entries[0].Deps) != 1 || entries[0].Deps[0] != "builtin:exec" {
		t.Fatalf("依赖快照未登记, got %#v", entries)
	}
	entries[0].Deps[0] = "builtin:write_file"
	if c.LoadedSkillEntries()[0].Deps[0] != "builtin:exec" {
		t.Fatal("LoadedSkillEntries 的依赖必须是拷贝")
	}

	// 重复读同一技能（同名同依赖）：不算变化
	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:exec"})
	if c.ConsumeSkillsDirty() {
		t.Fatal("重复读同一技能不应置脏")
	}

	// 同一技能被重新激活但依赖不同：必须重算，否则工具集会停在旧快照
	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:write_file"})
	if !c.ConsumeSkillsDirty() {
		t.Fatal("依赖变化必须置脏")
	}

	// nil 与空切片不是一回事：nil = 无快照（解析时回落查库），
	// 空切片 = 权威的「这一版确实没有依赖」（不得再查库，否则草稿加的依赖会漏进线上）
	c2 := &Context{}
	c2.MarkSkillLoaded("id-2", "no-deps", []string{})
	if e := c2.LoadedSkillEntries(); len(e) != 1 || e[0].Deps == nil {
		t.Fatalf("空切片快照必须保持非 nil, got %#v", e)
	}

	// 快照是拷贝
	snap := c.LoadedSkills()
	snap["id-1"] = "tampered"
	snap["id-2"] = "injected"
	if c.LoadedSkills()["id-1"] != "custom-tool-authoring" || len(c.LoadedSkills()) != 1 {
		t.Fatal("LoadedSkills 必须返回拷贝，调用方不得改到内部状态")
	}
}

// 激活入口只有一个：use_skill。read_skill 是纯查看，不能顺手把工具带出来
// —— 否则「只是想看文档」也会顶起 exec / browser 这类大 schema（占预算、撞 50 上限、扩大误选面）。
func TestReadSkillDoesNotActivate(t *testing.T) {
	const (
		skillID   = "sk1"
		skillName = "custom-tool-authoring"
		body      = "# skill"
	)
	c := &Context{Skills: stubSkillDetail{d: &SkillDetail{
		SkillInfo: SkillInfo{ID: skillID, Name: skillName},
		Content:   body,
		ToolDeps:  []string{"builtin:exec"},
	}}}
	args := map[string]any{"name": skillName}

	got, err := (&readSkillTool{}).Execute(context.Background(), c, args)
	if err != nil {
		t.Fatalf("read_skill = %v", err)
	}
	// 依赖清单照常透出（「这个技能需要什么」和「现在就给你」是两件事）
	if d, ok := got.(*SkillDetail); !ok || d.Content != body || len(d.ToolDeps) != 1 || d.ToolDeps[0] != "builtin:exec" {
		t.Fatalf("read_skill 应返回正文 + 依赖清单, got %#v", got)
	}
	if c.ConsumeSkillsDirty() || len(c.LoadedSkills()) != 0 {
		t.Fatal("read_skill 是纯查看，不应激活技能")
	}

	got, err = (&useSkillTool{}).Execute(context.Background(), c, args)
	if err != nil {
		t.Fatalf("use_skill = %v", err)
	}
	if d, ok := got.(*SkillDetail); !ok || d.Content != body || len(d.ToolDeps) != 1 {
		t.Fatalf("use_skill 必须返回正文 + 依赖清单（否则模型还得再 read 一次）, got %#v", got)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("use_skill 必须置脏（否则下一轮不会重算工具集）")
	}
	if c.LoadedSkills()[skillID] != skillName {
		t.Fatalf("use_skill 应登记技能, got %v", c.LoadedSkills())
	}
	// 依赖声明也要一起登记：工具解析只认这份快照（正式运行下 = 当前版本）
	entries := c.LoadedSkillEntries()
	if len(entries) != 1 || len(entries[0].Deps) != 1 || entries[0].Deps[0] != "builtin:exec" {
		t.Fatalf("use_skill 应登记依赖快照, got %#v", entries)
	}
}

// stubSkillDetail 只实现 Get：定位口径由 skillManager 保证，这里只关心「读」与「激活」的差异。
type stubSkillDetail struct {
	SkillsManager
	d *SkillDetail
}

func (s stubSkillDetail) Get(context.Context, SkillRef) (*SkillDetail, error) { return s.d, nil }
