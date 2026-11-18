package tools

import (
	"context"
	"testing"

	"flame/nexus/shared/storage"
)

// stubAgents 供跨域写闸门（checkWrite）读取目标 agent 状态；delete_file 路径只用到 Get。
type stubAgents struct{ status int }

func (s stubAgents) Create(context.Context, string, string, bool) (string, error) { return "", nil }

func (s stubAgents) List(context.Context) ([]AgentInfo, error) { return nil, nil }

func (s stubAgents) Exists(context.Context, string) (bool, error) { return true, nil }

func (s stubAgents) Get(context.Context, string) (*AgentInfo, error) {
	return &AgentInfo{ID: "aid", Name: "poet", Status: s.status}, nil
}

func (s stubAgents) Update(context.Context, string, string, string) (string, error) { return "", nil }

func (s stubAgents) SetUnrestricted(context.Context, string, bool) error { return nil }

func (s stubAgents) SetSupervisor(context.Context, string, bool) error { return nil }

func (s stubAgents) Delete(context.Context, string) error { return nil }

// newFileCtx 造最小可用 Context：Storage 落在临时目录，Agents 桩返回固定 status。
func newFileCtx(t *testing.T, agentName string, status int) (*Context, storage.Storage) {
	t.Helper()
	st := storage.NewFilesystemStorage(t.TempDir())
	return &Context{
		Schema:    "t0",
		AgentName: agentName,
		AgentID:   "self",
		// 身份判据是属性不是名字：测试里用 "main" 表示超权 agent 时须显式置位
		IsSupervisor: agentName == "main",
		UserID:       "u1",
		WorkKey:      "tenants/t0/users/u1/sessions/s1",
		Storage:      st,
		Agents:       stubAgents{status: status},
	}, st
}

func runDelete(t *testing.T, c *Context, p string) error {
	t.Helper()
	_, err := (&fileDeleteTool{}).Execute(context.Background(), c, map[string]any{"path": p})
	return err
}

func mustWrite(t *testing.T, st storage.Storage, key string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, st storage.Storage, key string) bool {
	t.Helper()
	ex, err := st.Exists(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// 工作区内：目标是目录时递归删除（非超级智能体，走普通工作区路径）。
func TestDeleteFileRecursesInWorkspace(t *testing.T) {
	c, st := newFileCtx(t, "joker", 1)
	deep := c.StoragePath("sub/deep/a.md")
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "sub"); err != nil {
		t.Fatalf("delete sub = %v, want nil", err)
	}
	if exists(t, st, deep) {
		t.Error("sub/deep/a.md 应随目录一并删除")
	}
	if exists(t, st, c.StoragePath("sub")) {
		t.Error("sub 目录应已删除")
	}
}

// 工作区内：单个文件照旧可删（回归）。
func TestDeleteFileSingleFile(t *testing.T) {
	c, st := newFileCtx(t, "joker", 1)
	key := c.StoragePath("a.md")
	mustWrite(t, st, key)

	if err := runDelete(t, c, "a.md"); err != nil {
		t.Fatalf("delete a.md = %v, want nil", err)
	}
	if exists(t, st, key) {
		t.Error("a.md 应已删除")
	}
}

// 超级智能体跨域：删实体内部的子树，目录树整棵消失。
func TestDeleteFileRecursesAcrossRoot(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	deep := "tenants/t0/agents/poet/sub/deep/a.md"
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "/agents/poet/sub"); err != nil {
		t.Fatalf("超级智能体递归删子树 = %v, want nil", err)
	}
	if exists(t, st, deep) {
		t.Error("子树内容应已递归删除")
	}
}

// 超级智能体也不能删实体根目录本身（一律递归不等于越过 D6 的边界）。
func TestDeleteFileRejectsEntityRoot(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	agentMd := "tenants/t0/agents/poet/AGENT.md"
	mustWrite(t, st, agentMd)

	if err := runDelete(t, c, "/agents/poet"); err == nil {
		t.Fatal("删除 agent 实体根目录应被拒绝")
	}
	if !exists(t, st, agentMd) {
		t.Error("被拒绝的删除不应产生任何副作用")
	}
}

// 目标废弃时一律拒绝（状态闸优先于删除动作）。
func TestDeleteFileRejectsDeprecatedTarget(t *testing.T) {
	c, st := newFileCtx(t, "main", 3)
	deep := "tenants/t0/agents/poet/sub/a.md"
	mustWrite(t, st, deep)

	if err := runDelete(t, c, "/agents/poet/sub"); err == nil {
		t.Fatal("目标废弃时应拒绝删除")
	}
	if !exists(t, st, deep) {
		t.Error("被拒绝的删除不应产生任何副作用")
	}
}

// skills 容器层（agents/{a}/skills）不是可写目标，报"缺少 skill 名"。
func TestDeleteFileRejectsSkillsContainer(t *testing.T) {
	c, st := newFileCtx(t, "main", 1)
	mustWrite(t, st, "tenants/t0/agents/poet/skills/x/SKILL.md")

	if err := runDelete(t, c, "/agents/poet/skills"); err == nil {
		t.Fatal("skills 容器层应被拒绝")
	}
}

// 删除不存在的路径是幂等成功（与 S3 口径一致；固化契约，避免被改回严格模式）。
func TestDeleteFileMissingPathIsIdempotent(t *testing.T) {
	c, _ := newFileCtx(t, "joker", 1)

	if err := runDelete(t, c, "nope.md"); err != nil {
		t.Fatalf("删除不存在的路径应为幂等成功, got %v", err)
	}
}

// SKILL.md 是技能入口文件，根寻址下不允许单独删除：删掉会留下「登记在库、没有正文」
// 的空技能（运行时静默降级为空内容）。与管理端 isSkillEntry 同口径。
func TestDeleteFileRejectsSkillEntryOwn(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, map[string]*SkillInfo{
		"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1},
	})
	entry := c.TenantPath("agents/joker/skills/foo/SKILL.md")
	mustWrite(t, c.Storage, entry)

	if err := runDelete(t, c, "/agents/joker/skills/foo/SKILL.md"); err == nil {
		t.Fatal("单独删除 SKILL.md 应被拒绝")
	}
	if !exists(t, c.Storage, entry) {
		t.Error("被拒绝的删除不应产生任何副作用")
	}

	// 附属文件不受影响（这正是本次放开的能力）
	extra := c.TenantPath("agents/joker/skills/foo/docs/a.md")
	mustWrite(t, c.Storage, extra)
	if err := runDelete(t, c, "/agents/joker/skills/foo/docs/a.md"); err != nil {
		t.Errorf("附属文件应可删, got %v", err)
	}
	if exists(t, c.Storage, extra) {
		t.Error("附属文件应已删除")
	}
}

// 超级智能体同样受 SKILL.md 保护 —— 三个入口（管理端 / 超级智能体 / 非超级智能体）口径一致。
func TestDeleteFileRejectsSkillEntryForSupervisor(t *testing.T) {
	c := newOwnSkillCtx(t, "main", 1, nil)
	c.Agents = stubAgentsByName{byName: map[string]*AgentInfo{
		"main": {ID: "main-id", Name: "main", Status: 1},
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
	}}
	c.Skills = stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{
		"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}},
	}}
	entry := c.TenantPath("agents/poet/skills/foo/SKILL.md")
	mustWrite(t, c.Storage, entry)

	if err := runDelete(t, c, "/agents/poet/skills/foo/SKILL.md"); err == nil {
		t.Fatal("超级智能体单独删除 SKILL.md 也应被拒绝")
	}
	if !exists(t, c.Storage, entry) {
		t.Error("被拒绝的删除不应产生任何副作用")
	}

	// 整个技能目录仍可删（走元工具的场景不在此，但实体内部子树要放行）
	sub := c.TenantPath("agents/poet/skills/foo/docs")
	mustWrite(t, c.Storage, c.TenantPath("agents/poet/skills/foo/docs/a.md"))
	if err := runDelete(t, c, "/agents/poet/skills/foo/docs"); err != nil {
		t.Errorf("实体内部子树应可删, got %v", err)
	}
	if exists(t, c.Storage, sub) {
		t.Error("子树应已递归删除")
	}
}
