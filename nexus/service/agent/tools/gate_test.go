package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/models"
	"flame/nexus/shared/storage"
)

// 工作区内仿造实体目录的路径形状识别（非超级智能体专用）。
func TestEntityLikePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"skills/foo/SKILL.md", true},
		{"skills/foo", true},
		{"agents/poet/AGENT.md", true},
		{"agents/poet/skills/x/SKILL.md", true},
		{"notes/skills.md", false},
		{"skills", false},
		{"skills.md", false},
		{"reports/q1/agents.md", false},
		{"", false},
	}
	for _, tc := range cases {
		if _, ok := entityLikePath(tc.path, "joker"); ok != tc.want {
			t.Errorf("entityLikePath(%q) ok = %v, want %v", tc.path, ok, tc.want)
		}
	}
}

// 自己的定义域与别人的定义域，指引必须不同：自己那条要给可执行的根寻址写法，
// 别人的那条只能指向管理员 / 超级智能体（非超级智能体没有 create_agent 权限）。
func TestEntityLikePathSelfHint(t *testing.T) {
	self, ok := entityLikePath("agents/joker/skills/foo/a.md", "joker")
	if !ok || !strings.Contains(self, "/agents/joker/skills/") {
		t.Errorf("自己的定义域应指引根寻址写法, got %q", self)
	}
	other, ok := entityLikePath("agents/poet/skills/foo/a.md", "joker")
	if !ok || strings.Contains(other, "/agents/joker/skills/") {
		t.Errorf("别人的定义域不应指引自己的路径, got %q", other)
	}
}

// 非超级智能体在工作区里新建假实体 → 拒绝；严格度 B：已存在的同名路径放行。
func TestResolveWriteRejectsFakeEntity(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	c := &Context{
		Schema:    "t0",
		AgentName: "joker",
		WorkKey:   "tenants/t0/users/u1/sessions/s1",
		Storage:   st,
	}
	ctx := context.Background()

	// 新建假实体：拒（且错误里必须带正确指引）
	for _, p := range []string{"skills/foo/SKILL.md", "agents/poet/AGENT.md"} {
		_, _, _, err := resolveWrite(ctx, c, p)
		if err == nil {
			t.Errorf("resolveWrite(%q) = nil, want error", p)
		}
	}

	// 普通工作区文件：放行
	if _, _, _, err := resolveWrite(ctx, c, "notes/a.md"); err != nil {
		t.Errorf("resolveWrite(notes/a.md) = %v, want nil", err)
	}

	// 已存在（如用户上传的同名目录）：放行 —— 严格度 B
	p := "skills/bar/SKILL.md"
	if err := st.WriteFile(ctx, c.StoragePath(p), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveWrite(ctx, c, p); err != nil {
		t.Errorf("resolveWrite(%q) 已存在时应放行, got %v", p, err)
	}

	// supervisor 的工作区写不受此闸限制（它的跨域写由 "/" 前缀 + checkWrite 管）
	// 身份是属性不是名字：改名字的同时必须置位，否则走的还是普通 agent 分支
	c.AgentName = "main"
	c.IsSupervisor = true
	if _, _, _, err := resolveWrite(ctx, c, "skills/foo/SKILL.md"); err != nil {
		t.Errorf("超级智能体 resolveWrite = %v, want nil", err)
	}
}

// ===== 根寻址闸门（D23 / D32）：非超级智能体可读自己的定义目录，只能写私有 skill 目录 =====

// stubAgentsByName 按名返回 agent。跨域闸门既要用它读**目标**状态（checkWrite），
// 也要用它读**自身**状态（checkWriteOwn），所以不能像 stubAgents 那样固定返回一个名字。
type stubAgentsByName struct {
	AgentManager
	byName map[string]*AgentInfo
}

func (s stubAgentsByName) Get(_ context.Context, name string) (*AgentInfo, error) {
	return s.byName[name], nil
}

// stubSkillsForWrite 只实现 FindForWrite（根寻址写闸判定 skill 是否存在 / 属于谁 / 什么状态）。
// 其余方法由嵌入接口兜底（被调用即 panic），保证用例不会悄悄依赖未桩行为。
type stubSkillsForWrite struct {
	SkillsManager
	// byOwner：归属 agent uuid → skill 名 → 信息（"" 表示租户级，用于反证越权）
	byOwner map[string]map[string]*SkillInfo
}

func (s stubSkillsForWrite) FindForWrite(_ context.Context, ownerAgentID, skillName string) (*SkillInfo, error) {
	if m, ok := s.byOwner[ownerAgentID]; ok {
		return m[skillName], nil
	}
	return nil, nil
}

// newOwnSkillCtx 造一个能走根寻址闸门的 Context：自己 = self / self-status，
// 名下私有 skill = skills（key 为 skill 名）。
func newOwnSkillCtx(t *testing.T, self string, selfStatus int, skills map[string]*SkillInfo) *Context {
	t.Helper()
	byOwner := map[string]map[string]*SkillInfo{}
	if skills != nil {
		byOwner[self+"-id"] = skills
	}
	return &Context{
		Schema:    "t0",
		AgentName: self,
		AgentID:   self + "-id",
		// 身份判据是属性不是名字：测试里用 "main" 表示超权 agent 时须显式置位
		IsSupervisor: self == "main",
		UserID:       "u1",
		WorkKey:      "tenants/t0/users/u1/sessions/s1",
		Storage:      storage.NewFilesystemStorage(t.TempDir()),
		Agents: stubAgentsByName{byName: map[string]*AgentInfo{
			self: {ID: self + "-id", Name: self, Status: selfStatus},
		}},
		Skills: stubSkillsForWrite{byOwner: byOwner},
	}
}

// 非超级智能体的根寻址写闸：白名单 = agents/{self}/skills/{skill}/**，白名单外一律拒。
func TestResolveWriteOwnScope(t *testing.T) {
	ctx := context.Background()
	own := map[string]*SkillInfo{"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}}

	cases := []struct {
		name    string
		status  int
		path    string
		wantErr bool
	}{
		{"自己的 skill 子文件", 1, "/agents/joker/skills/foo/a.md", false},
		{"自己的 skill 深层子目录", 1, "/agents/joker/skills/foo/docs/deep/a.md", false},
		{"自己的 skill 入口文件（可改内容）", 1, "/agents/joker/skills/foo/SKILL.md", false},
		{"别人的 agent", 1, "/agents/poet/skills/foo/a.md", true},
		{"未登记的 skill 名", 1, "/agents/joker/skills/bar/a.md", true},
		{"实体根目录本身", 1, "/agents/joker/skills/foo", true},
		{"自己的 AGENT.md（不在本次范围）", 1, "/agents/joker/AGENT.md", true},
		{"租户级共享 skill", 1, "/skills/foo/SKILL.md", true},
		{"用户域", 1, "/users/u1/files/a.md", true},
		{"自身废弃态", 3, "/agents/joker/skills/foo/a.md", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOwnSkillCtx(t, "joker", tc.status, own)
			_, _, _, err := resolveWrite(ctx, c, tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("resolveWrite(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("resolveWrite(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// skill 自身废弃时同样不可改（与 agent 侧、元工具侧同口径）。
func TestResolveWriteOwnSkillStatusGate(t *testing.T) {
	ctx := context.Background()
	c := newOwnSkillCtx(t, "joker", 1, map[string]*SkillInfo{
		"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusDeprecated},
	})
	if _, _, _, err := resolveWrite(ctx, c, "/agents/joker/skills/foo/a.md"); err == nil {
		t.Error("skill 废弃时应拒绝")
	}
}

// 超级智能体写「废弃 agent」名下的私有 skill 同样拒绝。这条闸与 checkWriteOwn 第 3 条对偶：
// 那里判「自己是不是正常态」，这里判「归属者是不是正常态」—— 同一判据，两档都得有。
func TestResolveWriteCrossRejectsDeprecatedOwnerAgent(t *testing.T) {
	ctx := context.Background()
	newCtx := func(ownerStatus int) *Context {
		return &Context{
			Schema:       "t0",
			AgentName:    "main",
			AgentID:      "main-id",
			IsSupervisor: true,
			UserID:       "u1",
			WorkKey:      "tenants/t0/users/u1/sessions/s1",
			Storage:      storage.NewFilesystemStorage(t.TempDir()),
			Agents: stubAgentsByName{byName: map[string]*AgentInfo{
				"main": {ID: "main-id", Name: "main", Status: 1},
				"poet": {ID: "poet-id", Name: "poet", Status: ownerStatus},
			}},
			Skills: stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{
				// skill 自身恒为正常态：保证拦截只可能来自归属 agent 那道闸
				"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}},
			}},
		}
	}

	if _, _, _, err := resolveWrite(ctx, newCtx(models.StatusDeprecated), "/agents/poet/skills/foo/a.md"); err == nil {
		t.Error("归属 agent 废弃时应拒绝写它的私有 skill")
	}
	// 回归：归属 agent 正常态时照常放行，别把整条路堵死
	if _, _, _, err := resolveWrite(ctx, newCtx(models.StatusNormal), "/agents/poet/skills/foo/a.md"); err != nil {
		t.Errorf("归属 agent 正常态时应放行, got %v", err)
	}
	// 读不受影响（D4：废弃也能读）
	if _, _, _, err := resolveRead(newCtx(models.StatusDeprecated), "/agents/poet/skills/foo/a.md"); err != nil {
		t.Errorf("读不应受归属 agent 状态影响, got %v", err)
	}
}

// AgentID 缺失时必须拒绝：FindForWrite 的 agentID=="" 表示「租户级」，
// 若不拦，共享 skill 会被误当私有放行。
func TestResolveWriteOwnRejectsEmptyAgentID(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, nil)
	c.AgentID = ""
	if _, _, _, err := resolveWrite(context.Background(), c, "/agents/joker/skills/foo/a.md"); err == nil {
		t.Fatal("AgentID 缺失时应拒绝（防共享 skill 被误判为私有）")
	}
}

// 非超级智能体的根寻址读闸：可读自己的定义目录（含私有 skill），但**不设状态闸**（读不受限，D4）。
func TestResolveReadOwnScope(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		status  int
		path    string
		wantErr bool
	}{
		{"自己的 skill 文件", "joker", 1, "/agents/joker/skills/foo/a.md", false},
		{"自己的 skill 入口", "joker", 1, "/agents/joker/skills/foo/SKILL.md", false},
		{"未登记的 skill 名（读不查库）", "joker", 1, "/agents/joker/skills/nope/a.md", false},
		{"废弃态仍可读自己的 skill", "joker", 3, "/agents/joker/skills/foo/a.md", false},
		{"别人的 AGENT.md", "joker", 1, "/agents/poet/AGENT.md", true},
		{"自己的 AGENT.md（可读）", "joker", 1, "/agents/joker/AGENT.md", false},
		{"自己的定义域附属文件（可读）", "joker", 1, "/agents/joker/notes/plan.md", false},
		{"自己的定义根目录（可列）", "joker", 1, "/agents/joker", false},
		{"自己的 AGENT.md（废弃态也可读）", "joker", 3, "/agents/joker/AGENT.md", false},
		{"别人的 skill", "joker", 1, "/agents/poet/skills/foo/a.md", true},
		{"租户级共享 skill", "joker", 1, "/skills/foo/SKILL.md", true},
		{"用户域", "joker", 1, "/users/u1/files/a.txt", true},
		{"超级智能体全租户可读", "main", 1, "/agents/poet/AGENT.md", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOwnSkillCtx(t, tc.agent, tc.status, nil)
			_, _, _, err := resolveRead(c, tc.path)
			if (err != nil) != tc.wantErr {
				t.Errorf("resolveRead(%q) = %v, wantErr %v", tc.path, err, tc.wantErr)
			}
		})
	}
}

// 读放开不能顺带放开写：自己的 AGENT.md 仍然只能由元工具改（D10 / D32）。
func TestResolveWriteOwnAgentMDStillDenied(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, nil)
	if _, _, _, err := resolveWrite(context.Background(), c, "/agents/joker/AGENT.md"); err == nil {
		t.Fatal("非超级智能体不应能写自己的 AGENT.md")
	}
}

// 版本域（tenants/<schema>/versions/**）必须对模型完全不可达 —— 连超级智能体也不行。
//
// 它落在租户根下，保护完全依赖 gate.go 路径 switch 的 default 分支：将来有人加 case
// （比如做「租户根文件浏览」）时，这条断言会立刻红，提醒他版本历史不能被模型读/改。
func TestVersionDomainUnreachable(t *testing.T) {
	paths := []string{
		"/versions/agents/poet/v1/AGENT.md",
		"/versions/agents/poet/v1/skills/verse/SKILL.md",
		"/versions/skills/foo/v1/SKILL.md",
		"/versions/tools/calc/v1/main.py",
	}
	for _, p := range paths {
		ctx := context.Background()
		c := newOwnSkillCtx(t, "main", 1, nil) // main = supervisor：连它也必须被拒
		if _, _, _, err := resolveRead(c, p); err == nil {
			t.Errorf("resolveRead(%q) 应拒绝：版本域不是模型可读的内容", p)
		}
		if _, _, _, err := resolveWrite(ctx, c, p); err == nil {
			t.Errorf("resolveWrite(%q) 应拒绝：版本不可由模型修改", p)
		}
	}
}

// ===== 归属 agent 的状态闸：只有正常态才能写它名下的私有 skill =====
//
// skill 自身状态那道闸不受影响（见 TestResolveWriteOwnSkillStatusGate）。
// 自身档与超级智能体跨域档必须同口径。

// 自身档：checkWriteOwn 第 3 条。
func TestResolveWriteOwnSkillOwnerStatus(t *testing.T) {
	ctx := context.Background()
	own := map[string]*SkillInfo{"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusNormal}}

	cases := []struct {
		name       string
		selfStatus int
		wantErr    bool
	}{
		{"正常 → 放行", models.StatusNormal, false},
		{"废弃 → 拒绝", models.StatusDeprecated, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOwnSkillCtx(t, "joker", tc.selfStatus, own)
			c.Agents = stubAgentsByName{byName: map[string]*AgentInfo{
				"joker": {ID: "joker-id", Name: "joker", Status: tc.selfStatus},
			}}
			_, _, _, err := resolveWrite(ctx, c, "/agents/joker/skills/foo/a.md")
			if (err != nil) != tc.wantErr {
				t.Errorf("resolveWrite = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// 超级智能体跨域档：checkWrite 的 skill 分支（判归属者），与自身档对偶。
func TestResolveWriteCrossSkillOwnerStatus(t *testing.T) {
	ctx := context.Background()
	newCtx := func(ownerStatus int) *Context {
		return &Context{
			Schema:       "t0",
			AgentName:    "main",
			AgentID:      "main-id",
			IsSupervisor: true,
			UserID:       "u1",
			WorkKey:      "tenants/t0/users/u1/sessions/s1",
			Storage:      storage.NewFilesystemStorage(t.TempDir()),
			Agents: stubAgentsByName{byName: map[string]*AgentInfo{
				"main": {ID: "main-id", Name: "main", Status: models.StatusNormal},
				"poet": {ID: "poet-id", Name: "poet", Status: ownerStatus},
			}},
			Skills: stubSkillsForWrite{byOwner: map[string]map[string]*SkillInfo{
				// skill 自身恒为正常态：保证结论只可能来自归属 agent 那道闸
				"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusNormal}},
			}},
		}
	}

	cases := []struct {
		name        string
		ownerStatus int
		wantErr     bool
	}{
		{"归属者正常 → 放行", models.StatusNormal, false},
		{"归属者废弃 → 拒绝", models.StatusDeprecated, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := resolveWrite(ctx, newCtx(tc.ownerStatus), "/agents/poet/skills/foo/a.md")
			if (err != nil) != tc.wantErr {
				t.Errorf("resolveWrite = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
