package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agentsrepo "flame/nexus/shared/repository/agents"
	skillsrepo "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/storage"
	"flame/tools/xsql"
)

// checkSkillScope 是「own / shared / agent_skill 三套工具各能动哪个空间」的唯一判据。
//
// 与工具分类（skill-own / skill-admin 等）正交、缺一不可：分类管"谁能用哪个工具"，
// 它管"工具只能动哪个空间的数据"。own 系列人人可用，若不校验归属，
// 开启「可发现」的 agent 就能借可见口径改到共享 skill。
//
// target 非空 = *_agent_skill 系列（指定归属，仅超级智能体）：必须落在该归属的私有空间。
func TestCheckSkillScope(t *testing.T) {
	const (
		me    = "agent-me"
		other = "agent-other"
	)
	shared := &models.Skill{Name: "report"}                            // 租户级：agent_id 为 NULL
	mine := &models.Skill{Name: "draft", AgentID: xsql.UUID(me)}       // 自己的私有
	theirs := &models.Skill{Name: "secret", AgentID: xsql.UUID(other)} // 别人的私有

	cases := []struct {
		name         string
		isSupervisor bool
		scope        string
		target       string // 目标归属 agent（*_agent_skill 系列）
		s            *models.Skill
		wantErr      bool
	}{
		// own 工具：只能动**自己的私有**
		{"supervisor+own+my", true, tools.SkillScopeAgent, "", mine, false},
		{"supervisor+own+shared", true, tools.SkillScopeAgent, "", shared, true},
		{"supervisor+own+theirs", true, tools.SkillScopeAgent, "", theirs, true},
		{"agent+own+my", false, tools.SkillScopeAgent, "", mine, false},
		{"agent+own+shared", false, tools.SkillScopeAgent, "", shared, true},
		{"agent+own+theirs", false, tools.SkillScopeAgent, "", theirs, true},

		// shared 工具：只能动**租户级共享**
		{"supervisor+shared+shared", true, tools.SkillScopeTenant, "", shared, false},
		{"supervisor+shared+my", true, tools.SkillScopeTenant, "", mine, true},
		{"agent+shared+shared", false, tools.SkillScopeTenant, "", shared, true},
		{"agent+shared+theirs", false, tools.SkillScopeTenant, "", theirs, true},

		// agent_skill 工具（指定归属）：仅超级智能体，且必须正好是该归属的私有
		{"supervisor+target+theirs", true, tools.SkillScopeAgent, other, theirs, false},
		{"supervisor+target+targetIsMe", true, tools.SkillScopeAgent, me, mine, false},
		{"supervisor+target+wrongOwner", true, tools.SkillScopeAgent, other, mine, true},
		{"supervisor+target+shared", true, tools.SkillScopeAgent, other, shared, true},
		{"supervisor+target+nil", true, tools.SkillScopeAgent, other, nil, false},
		{"agent+target+theirs", false, tools.SkillScopeAgent, other, theirs, true},
		{"agent+target+ownPrivate", false, tools.SkillScopeAgent, me, mine, true},

		// 无空间定向（read_skill）：不校验
		{"none+shared", false, "", "", shared, false},
		{"none+theirs", false, "", "", theirs, false},

		// 未定位到（nil）：交由调用方报 not found
		{"nil+own", false, tools.SkillScopeAgent, "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSkillScope(tc.isSupervisor, me, tc.target, tc.scope, tc.s)
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

// ===== Update 改名：整目录搬迁 =====

// stubSkillRepoForUpdate 只实现 Update 流程用到的仓库方法（其余由嵌入接口兜底）。
type stubSkillRepoForUpdate struct {
	skillsrepo.Repository
	skill   *models.Skill
	updated *models.Skill
}

func (s *stubSkillRepoForUpdate) FindByName(_, name, _ string) (*models.Skill, error) {
	if s.skill == nil || s.skill.Name != name {
		return nil, nil
	}
	return s.skill, nil
}

func (s *stubSkillRepoForUpdate) Update(_ string, sk *models.Skill) error {
	s.updated = sk
	return nil
}

// stubAgentsRepoForSkill 只实现 GetAgent（归属 agent 的状态闸）。
type stubAgentsRepoForSkill struct {
	agentsrepo.Repository
	inst *models.Agent
}

func (s stubAgentsRepoForSkill) GetAgent(_ *auth.AuthInfo, _ string) (*models.Agent, error) {
	return s.inst, nil
}

// 改名必须搬**整个技能目录**。
//
// 技能目录现在可以放附属文件与子目录（文件工具的 "/" 根寻址，见 tools/gate.go 的
// checkWriteOwn）。若仍沿用「写新 SKILL.md + 删旧 SKILL.md」，附属资源会留在旧目录
// 变成孤儿 —— 表里已指向新名，旧目录再也没人认领。
func TestUpdateRenameMovesWholeSkillDir(t *testing.T) {
	const (
		schema    = "t0"
		agentID   = "agent-me"
		agentName = "joker"
		oldDir    = "tenants/t0/agents/joker/skills/old"
		newDir    = "tenants/t0/agents/joker/skills/new"
	)
	ctx := context.Background()

	write := func(t *testing.T, st storage.Storage, key, body string) {
		t.Helper()
		if err := st.WriteFile(ctx, key, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(t *testing.T, st storage.Storage, key string) bool {
		t.Helper()
		ok, err := st.Exists(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	newManager := func(t *testing.T) (*skillManager, storage.Storage) {
		t.Helper()
		st := storage.NewFilesystemStorage(t.TempDir())
		sk := &models.Skill{ID: "s1", Name: "old", AgentID: xsql.UUID(agentID)}
		sk.Status = xsql.Int(models.StatusNormal)
		return &skillManager{
			schema:    schema,
			agentID:   agentID,
			agentName: agentName,
			userID:    "u1",
			storage:   st,
			repo:      &stubSkillRepoForUpdate{skill: sk},
			agents: stubAgentsRepoForSkill{inst: &models.Agent{
				Name: agentName, Status: xsql.Int(models.StatusNormal),
			}},
		}, st
	}

	t.Run("只改名：附属文件随目录搬走", func(t *testing.T) {
		m, st := newManager(t)
		write(t, st, oldDir+"/SKILL.md", "# old")
		write(t, st, oldDir+"/docs/spec.md", "spec")

		if err := m.Update(ctx, tools.SkillInput{
			Name: "old", NewName: "new", Scope: tools.SkillScopeAgent,
		}); err != nil {
			t.Fatalf("Update = %v, want nil", err)
		}
		if !exists(t, st, newDir+"/SKILL.md") {
			t.Error("入口文件应已随目录搬到新位置")
		}
		if !exists(t, st, newDir+"/docs/spec.md") {
			t.Error("附属文件应随目录一起搬（这正是本次新增的能力）")
		}
		if exists(t, st, oldDir+"/docs/spec.md") {
			t.Error("旧目录不应残留附属文件（否则成为孤儿）")
		}
	})

	t.Run("改名+换正文：新正文落在新目录，附属文件仍在", func(t *testing.T) {
		m, st := newManager(t)
		write(t, st, oldDir+"/SKILL.md", "# old")
		write(t, st, oldDir+"/docs/spec.md", "spec")

		if err := m.Update(ctx, tools.SkillInput{
			Name: "old", NewName: "new", Content: "# brand new", Scope: tools.SkillScopeAgent,
		}); err != nil {
			t.Fatalf("Update = %v, want nil", err)
		}
		body, err := st.ReadFile(ctx, newDir+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "# brand new" {
			t.Errorf("入口文件=%q, want %q", body, "# brand new")
		}
		if !exists(t, st, newDir+"/docs/spec.md") {
			t.Error("换正文不应影响附属文件")
		}
	})

	t.Run("只改正文不改名：目录原地不动", func(t *testing.T) {
		m, st := newManager(t)
		write(t, st, oldDir+"/SKILL.md", "# old")
		write(t, st, oldDir+"/docs/spec.md", "spec")

		if err := m.Update(ctx, tools.SkillInput{
			Name: "old", Content: "# rewritten", Scope: tools.SkillScopeAgent,
		}); err != nil {
			t.Fatalf("Update = %v, want nil", err)
		}
		body, _ := st.ReadFile(ctx, oldDir+"/SKILL.md")
		if string(body) != "# rewritten" {
			t.Errorf("入口文件=%q, want %q", body, "# rewritten")
		}
		if !exists(t, st, oldDir+"/docs/spec.md") {
			t.Error("不改名时不应动目录")
		}
	})
}

// restricted 只对租户级共享 skill 有意义：私有 skill 传了必须在**任何落库动作之前**拒绝。
// 若校验留在 repo.Update 之后，description / name 会先写进库再报错 —— 留下半写状态。
func TestUpdateRestrictedOnPrivateSkillRejectedBeforeWrite(t *testing.T) {
	sk := &models.Skill{ID: "s1", Name: "draft", AgentID: xsql.UUID("agent-me")}
	sk.Status = xsql.Int(models.StatusNormal)
	repo := &stubSkillRepoRestricted{skill: sk}
	m := &skillManager{
		schema:    "t0",
		agentID:   "agent-me",
		agentName: "joker",
		userID:    "u1",
		repo:      repo,
		agents:    stubAgentsRepoForSkill{inst: &models.Agent{Name: "joker", Status: xsql.Int(models.StatusNormal)}},
	}

	restricted := true
	err := m.Update(context.Background(), tools.SkillInput{
		Name:        "draft",
		Description: "new desc",
		Restricted:  &restricted,
		Scope:       tools.SkillScopeAgent,
	})
	if err == nil {
		t.Fatal("私有 skill 传 restricted 应报错")
	}
	if !strings.Contains(err.Error(), "restricted 只对租户级共享 skill 有意义") {
		t.Errorf("错误应指明 restricted 的适用范围, got %v", err)
	}
	if repo.updated {
		t.Error("校验必须先于 repo.Update：元数据不应已落库")
	}
}

// stubSkillRepoRestricted 记录 Update 是否被调用（用于断言校验与落库的先后顺序）。
type stubSkillRepoRestricted struct {
	skillsrepo.Repository
	skill   *models.Skill
	updated bool
}

func (s *stubSkillRepoRestricted) FindByName(_, name, _ string) (*models.Skill, error) {
	if s.skill == nil || s.skill.Name != name {
		return nil, nil
	}
	return s.skill, nil
}

func (s *stubSkillRepoRestricted) Update(_ string, _ *models.Skill) error {
	s.updated = true
	return nil
}

// ===== checkOwnerAgent：归属 agent 的「私有技能可写」闸 =====
//
// 只有正常态可写，废弃(3) 一律不可写（判据 models.IsModifiable）。
// 与 tools/gate.go 的两处（自身档 / 超级智能体跨域档）、admin/skills 的 ensureAgentModifiable
// 同口径 —— 四处必须一起判，否则会出现「元工具能改、文件工具报错」的分裂。
func TestCheckOwnerAgentStatusGate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"正常 → 放行", models.StatusNormal, false},
		{"废弃 → 拒绝", models.StatusDeprecated, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &skillManager{agents: stubAgentsRepoForSkill{inst: &models.Agent{
				Name:   "joker",
				Status: xsql.Int(tc.status),
			}}}
			err := m.checkOwnerAgent("agent-me")
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}

	// 租户级 skill（无归属 agent）：免校验
	m := &skillManager{agents: stubAgentsRepoForSkill{}}
	if err := m.checkOwnerAgent(""); err != nil {
		t.Errorf("ownerID 为空应免校验, got %v", err)
	}
}

// ===== 提示词里的「能不能改技能」：归属 agent 那道闸要能体现 =====

// capabilityNotes：有 create_own_skill 但 agent 已废弃时，不能再声称「你可建/改自己的
// 私有技能」—— 否则 prompt 会承诺一个必然被闸门拒掉的能力，正好与它「事前防线」
// 的存在理由相悖。
func TestCapabilityNotesDeprecatedAgent(t *testing.T) {
	const allowedHint = "你可建/改**自己的私有**技能"
	const blockedHint = "你**当前不能**改**自己的私有**技能"

	cases := []struct {
		name        string
		status      int
		hasOwn      bool
		wantAllowed bool
		wantBlocked bool
	}{
		{"正常 + 有 own → 正常提示", models.StatusNormal, true, true, false},
		{"废弃 + 有 own → 改成「当前不能」", models.StatusDeprecated, true, false, true},
		{"无技能工具 → 两条都不该出现", models.StatusNormal, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed := map[string]bool{}
			if tc.hasOwn {
				allowed["create_own_skill"] = true
			}
			a := &agentImpl{
				instance: &models.Agent{Name: "joker", Status: xsql.Int(tc.status)},
				toolCtx:  &tools.Context{AllowedTools: allowed},
			}
			notes := a.capabilityNotes()
			if got := strings.Contains(notes, allowedHint); got != tc.wantAllowed {
				t.Errorf("含「可建/改自己的私有技能」= %v, want %v\n%s", got, tc.wantAllowed, notes)
			}
			if got := strings.Contains(notes, blockedHint); got != tc.wantBlocked {
				t.Errorf("含「当前不能改」= %v, want %v\n%s", got, tc.wantBlocked, notes)
			}
		})
	}
}

// stubSkillsForCatalog 只实现 ListInjected（skillCatalog 的唯一依赖）。
type stubSkillsForCatalog struct {
	tools.SkillsManager
	list []tools.SkillInfo
}

func (s stubSkillsForCatalog) ListInjected(context.Context) ([]tools.SkillInfo, error) {
	return s.list, nil
}

// skillCatalog：废弃的 skill 由 ListInjected 过滤，常驻清单里只出现正常态，
// 不需要再逐行打「只读」标记。
func TestSkillCatalogListsNormalSkillsOnly(t *testing.T) {
	a := &agentImpl{
		toolCtx: &tools.Context{
			Skills: stubSkillsForCatalog{list: []tools.SkillInfo{
				{Name: "draft", Scope: tools.SkillScopeAgent, Status: models.StatusNormal, Description: "草稿"},
			}},
		},
	}
	out := a.skillCatalog()
	if !strings.Contains(out, "- draft [私有]: 草稿") {
		t.Errorf("正常态 skill 应按「- 名 [归属]: 描述」列出, got:\n%s", out)
	}
	if strings.Contains(out, "只读") {
		t.Errorf("清单不应再带状态标记, got:\n%s", out)
	}
}

// skill_discovery 的注入：
//   - 零技能时必须仍然出声 —— 那正是该开关的适用场景（没有清单可注入，只能靠 list_skills 去找）
//   - 截断时不要和「更多已省略」那句重复提 list_skills
func TestSkillCatalogDiscovery(t *testing.T) {
	newImpl := func(discovery bool, list []tools.SkillInfo) *agentImpl {
		return &agentImpl{
			skillDiscovery: discovery,
			toolCtx:        &tools.Context{Skills: stubSkillsForCatalog{list: list}},
		}
	}

	t.Run("零技能 + 开着发现 → 仍给 list_skills 指引", func(t *testing.T) {
		out := newImpl(true, nil).skillCatalog()
		if !strings.Contains(out, "list_skills") {
			t.Errorf("应给出 list_skills 指引, got %q", out)
		}
	})

	t.Run("零技能 + 未开发现 → 空串", func(t *testing.T) {
		if out := newImpl(false, nil).skillCatalog(); out != "" {
			t.Errorf("应为空串, got %q", out)
		}
	})

	t.Run("截断 + 开着发现 → list_skills 只提一次", func(t *testing.T) {
		list := []tools.SkillInfo{
			{Name: "big", Scope: tools.SkillScopeAgent, Status: models.StatusNormal,
				Description: strings.Repeat("长", maxSkillPromptChars+500)},
		}
		out := newImpl(true, list).skillCatalog()
		if !strings.Contains(out, "已省略") {
			t.Fatalf("应触发截断, got:\n%s", out)
		}
		if n := strings.Count(out, "list_skills"); n != 1 {
			t.Errorf("list_skills 应只出现一次, got %d\n%s", n, out)
		}
	})

	t.Run("未截断 + 开着发现 → 有指引", func(t *testing.T) {
		list := []tools.SkillInfo{{Name: "draft", Scope: tools.SkillScopeAgent, Status: models.StatusNormal}}
		out := newImpl(true, list).skillCatalog()
		if !strings.Contains(out, "list_skills") {
			t.Errorf("应给出 list_skills 指引, got %q", out)
		}
	})
}

// ===== Delete / Create：技能目录是**目录**，不是单个文件 =====

// stubSkillRepoForDelete 只实现 Delete 流程用到的仓库方法（FindByName 定位 + Delete 记录）。
type stubSkillRepoForDelete struct {
	skillsrepo.Repository
	skill   *models.Skill
	deleted string // 记录被删的 skill id，供断言
}

func (s *stubSkillRepoForDelete) FindByName(_, name, _ string) (*models.Skill, error) {
	if s.skill == nil || s.skill.Name != name {
		return nil, nil
	}
	return s.skill, nil
}

func (s *stubSkillRepoForDelete) Delete(_, id string) error {
	s.deleted = id
	return nil
}

func writeSkillFile(t *testing.T, st storage.Storage, key, body string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func skillPathExists(t *testing.T, st storage.Storage, key string) bool {
	t.Helper()
	ok, err := st.Exists(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// Delete 必须删**整个技能目录**（SKILL.md + 附属文件 + 子目录），与管理端 admin/skills 同口径。
//
// 旧实现用 storage.Delete 删的是 skillPath —— 那是 .../skills/{name}/SKILL.md 这个**文件路径**，
// 于是附属文件与空目录全部残留（线上 poet/skills、joker/skills 下的 probe-* 空目录就是这么来的）。
// 关键：测试技能必须带附属文件，只放 SKILL.md 的话 Delete 与 DeletePrefix 表现一致，测不出缺陷。
func TestDeleteRemovesWholeSkillDir(t *testing.T) {
	const (
		schema  = "t0"
		meID    = "agent-me"
		meName  = "joker"
		otherID = "agent-other"
	)
	ctx := context.Background()

	newDeleteManager := func(t *testing.T, st storage.Storage, sk *models.Skill, agentID, agentName string) (*skillManager, *stubSkillRepoForDelete) {
		t.Helper()
		sk.Status = xsql.Int(models.StatusNormal)
		repo := &stubSkillRepoForDelete{skill: sk}
		return &skillManager{
			schema:    schema,
			agentID:   agentID,
			agentName: agentName,
			userID:    "u1",
			// 两个子用例都按「当前身份带超权属性」跑（第二个子用例名就是超级智能体）
			isSupervisor: true,
			storage:      st,
			repo:         repo,
			agents: stubAgentsRepoForSkill{inst: &models.Agent{
				Name: meName, Status: xsql.Int(models.StatusNormal),
			}},
		}, repo
	}

	t.Run("删自己的私有技能：附属文件与目录一并消失", func(t *testing.T) {
		const dir = "tenants/t0/agents/joker/skills/old"
		st := storage.NewFilesystemStorage(t.TempDir())
		writeSkillFile(t, st, dir+"/SKILL.md", "# old")
		writeSkillFile(t, st, dir+"/docs/spec.md", "spec")

		sk := &models.Skill{ID: "s1", Name: "old", AgentID: xsql.UUID(meID)}
		m, repo := newDeleteManager(t, st, sk, meID, meName)

		if err := m.Delete(ctx, tools.SkillRef{Name: "old", Scope: tools.SkillScopeAgent}); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
		if repo.deleted != "s1" {
			t.Errorf("库记录应被删, deleted=%q", repo.deleted)
		}
		if skillPathExists(t, st, dir+"/SKILL.md") {
			t.Error("入口文件应已删除")
		}
		if skillPathExists(t, st, dir+"/docs/spec.md") {
			t.Error("附属文件应随技能目录一起删除（旧实现只删 SKILL.md，这里必为 true）")
		}
		if skillPathExists(t, st, dir) {
			t.Error("技能目录本身不应残留（否则同名重建会继承旧目录）")
		}
	})

	t.Run("超级智能体删指定 agent 的私有技能：落在归属者目录上", func(t *testing.T) {
		const dir = "tenants/t0/agents/joker/skills/old"
		st := storage.NewFilesystemStorage(t.TempDir())
		writeSkillFile(t, st, dir+"/SKILL.md", "# old")
		writeSkillFile(t, st, dir+"/docs/spec.md", "spec")

		// 归属 agent 是 joker（otherID），当前身份是超级智能体：路径必须取归属者名
		sk := &models.Skill{ID: "s2", Name: "old", AgentID: xsql.UUID(otherID)}
		m, repo := newDeleteManager(t, st, sk, "agent-main", "main") // "main" = 带超权属性的预置 supervisor

		if err := m.Delete(ctx, tools.SkillRef{
			Name: "old", Scope: tools.SkillScopeAgent, OwnerID: otherID,
		}); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
		if repo.deleted != "s2" {
			t.Errorf("库记录应被删, deleted=%q", repo.deleted)
		}
		if skillPathExists(t, st, dir+"/docs/spec.md") || skillPathExists(t, st, dir) {
			t.Error("归属者目录下的技能应被整目录删除")
		}
	})
}

// ===== 工具依赖的增量维护（grant_skill_tool / revoke_skill_tool）=====

// stubSkillRepoForDep 只实现超级智能体按名定位（FindForSupervisorByName）。
type stubSkillRepoForDep struct {
	skillsrepo.Repository
	skill *models.Skill
}

func (s *stubSkillRepoForDep) FindForSupervisorByName(_, name, _ string) (*models.Skill, error) {
	if s.skill == nil || s.skill.Name != name {
		return nil, nil
	}
	return s.skill, nil
}

// ListBindings 空白名单：超级智能体读租户级 skill 时 fillVisibility 会查它（约束来自 Get 的展示路径）。
func (s *stubSkillRepoForDep) ListBindings(_, _ string) ([]models.AgentSkillBinding, error) {
	return nil, nil
}

// stubDepGrantsRepo 记录依赖的增删，并充当「当前全量」（ListSkillGrants）。
type stubDepGrantsRepo struct {
	toolgrants.Repository
	deps    []models.SkillToolGrant
	added   bool
	removed int64
	upserts []models.SkillToolGrant
	removes []string
}

func (s *stubDepGrantsRepo) ListSkillGrants(_, _ string) ([]models.SkillToolGrant, error) {
	return s.deps, nil
}

func (s *stubDepGrantsRepo) AddSkillGrant(_, _ string, g models.SkillToolGrant) (bool, error) {
	s.upserts = append(s.upserts, g)
	if s.added {
		s.deps = append(s.deps, g)
	}
	return s.added, nil
}

func (s *stubDepGrantsRepo) RemoveSkillGrant(_, _, ref string) (int64, error) {
	s.removes = append(s.removes, ref)
	return s.removed, nil
}

// newDepMgr 造一个超级智能体身份、按名定位的 manager；sk 为 nil 表示查无此技能。
func newDepMgr(sk *models.Skill, grants *stubDepGrantsRepo) *skillManager {
	if sk != nil && sk.Status == 0 {
		sk.Status = xsql.Int(models.StatusNormal)
	}
	return &skillManager{
		schema: "t0", agentID: "agent-main", agentName: "main", userID: "u1",
		isSupervisor: true, // 身份判据是属性，不是名字（D30）
		repo:         &stubSkillRepoForDep{skill: sk},
		grants:       grants,
	}
}

func TestAddToolDepAppendsAndReturnsAllDeps(t *testing.T) {
	grants := &stubDepGrantsRepo{
		deps:  []models.SkillToolGrant{{ItemType: models.GrantItemGroup, Ref: "builtin:exec"}},
		added: true,
	}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "data-crunching"}, grants)

	ch, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "data-crunching"}, "builtin:read_file")
	if err != nil {
		t.Fatalf("AddToolDep = %v", err)
	}
	if !ch.Added || ch.Ref != "builtin:read_file" || ch.Skill != "data-crunching" {
		t.Errorf("变更回显不符：%+v", ch)
	}
	// 只落一条，且 item_type 由 ref 形态推导（单工具 → tool）
	if len(grants.upserts) != 1 || grants.upserts[0].ItemType != models.GrantItemTool {
		t.Errorf("写入内容不符：%+v", grants.upserts)
	}
	// 回的是「变更后的完整依赖」，而不是只回新增的那条（补 read_skill 看不到现状的缺口）
	if len(ch.ToolDeps) != 2 || ch.ToolDeps[0] != "builtin:exec" || ch.ToolDeps[1] != "builtin:read_file" {
		t.Errorf("应回带完整依赖：%+v", ch.ToolDeps)
	}
}

// 幂等：已存在时 added=false，不重复落库。
func TestAddToolDepIsIdempotent(t *testing.T) {
	grants := &stubDepGrantsRepo{added: false}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	ch, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file")
	if err != nil {
		t.Fatalf("AddToolDep = %v", err)
	}
	if ch.Added {
		t.Error("已存在时 added 应为 false")
	}
}

// 无法展开的 ref（拼错 / 已删除）→ 拒绝，不落库（防写出永不生效的死依赖）。
func TestAddToolDepRejectsUnknownRef(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:no_such_tool"); err == nil {
		t.Fatal("无法展开的 ref 应被拒绝")
	}
	if len(grants.upserts) != 0 {
		t.Error("被拒绝时不应落库")
	}
}

// 删除只校验语法：工具/连接已被删后留下的悬空依赖必须能清掉。
func TestRemoveToolDepAllowsDanglingRef(t *testing.T) {
	grants := &stubDepGrantsRepo{removed: 1}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	ch, err := m.RemoveToolDep(context.Background(), tools.SkillRef{Name: "x"}, "mcp:gone:read")
	if err != nil {
		t.Fatalf("RemoveToolDep = %v", err)
	}
	if ch.Removed != 1 || ch.Ref != "mcp:gone:read" {
		t.Errorf("变更回显不符：%+v", ch)
	}
	if len(grants.removes) != 1 || grants.removes[0] != "mcp:gone:read" {
		t.Errorf("应按原 ref 精确删除：%+v", grants.removes)
	}
}

// 废弃的 skill 不允许改依赖（与定义编辑同一道状态闸）。
func TestToolDepWriteRejectsDeprecatedSkill(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	sk := &models.Skill{ID: "s1", Name: "x", Status: xsql.Int(models.StatusDeprecated)}
	m := newDepMgr(sk, grants)

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file"); err == nil {
		t.Fatal("废弃态应拒绝写依赖")
	}
	if len(grants.upserts) != 0 {
		t.Error("被拒绝时不应落库")
	}
}

// 非超级智能体一律拒绝（依赖是 allow 授权的生效触发器，谁能写谁就握住授权开关）。
func TestToolDepWriteRejectsNonSupervisor(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)
	m.isSupervisor = false // 降级身份：判据是属性，改名字没用
	m.agentName = "poet"

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file"); err == nil {
		t.Fatal("非超级智能体应被拒绝")
	}
	if len(grants.upserts) != 0 {
		t.Error("被拒绝时不应落库")
	}
}

// ===== read_skill / use_skill 的依赖回显（SkillDetail.ToolDeps）=====

// Get 必须把声明的依赖原样带出：不解组、不剔除悬空 ref。
// 悬空项留着才有诊断价值（revoke_skill_tool 仍能按原 ref 清掉），悄悄吞掉反而查不出问题。
func TestGetReturnsToolDeps(t *testing.T) {
	grants := &stubDepGrantsRepo{deps: []models.SkillToolGrant{
		{ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
		{ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		{ItemType: models.GrantItemTool, Ref: "mcp:gone:read", SkillID: "s1"}, // 连接已删 → 悬空
	}}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "data-crunching"}, grants)
	m.storage = storage.NewFilesystemStorage(t.TempDir()) // 内容缺失也要能返回元数据

	d, err := m.Get(context.Background(), tools.SkillRef{Name: "data-crunching"})
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	want := []string{"builtin:exec", "builtin:read_file", "mcp:gone:read"}
	if len(d.ToolDeps) != len(want) {
		t.Fatalf("tool_deps = %v, want %v", d.ToolDeps, want)
	}
	for i := range want {
		if d.ToolDeps[i] != want[i] {
			t.Fatalf("tool_deps = %v, want %v（声明顺序要原样保留）", d.ToolDeps, want)
		}
	}
}

// 没有依赖时必须是空数组而不是 nil（JSON 会变成 null，模型读成「不知道」而非「确实没声明」）。
func TestGetToolDepsAlwaysArray(t *testing.T) {
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, &stubDepGrantsRepo{})
	m.storage = storage.NewFilesystemStorage(t.TempDir())

	d, err := m.Get(context.Background(), tools.SkillRef{Name: "x"})
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	if d.ToolDeps == nil || len(d.ToolDeps) != 0 {
		t.Fatalf("无依赖应是空切片（非 nil）: %#v", d.ToolDeps)
	}
}

// 依赖仓库缺失（纯读场景）时不 panic：正文照常返回，依赖降级为空。
func TestGetWithoutGrantRepoSkipsToolDeps(t *testing.T) {
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, nil)
	// 注意：newDepMgr 把 (*stubDepGrantsRepo)(nil) 塞进了接口 —— 那仍是非 nil 接口，
	// 调用时才会在解引用处炸。要测「没有仓库」必须显式置 nil。
	m.grants = nil
	m.storage = storage.NewFilesystemStorage(t.TempDir())

	d, err := m.Get(context.Background(), tools.SkillRef{Name: "x"})
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	if len(d.ToolDeps) != 0 {
		t.Fatalf("无仓库时 tool_deps 应为空: %v", d.ToolDeps)
	}
}

// Create 的前置清理（purgeStaleSkillDir）：重名检查已过 → 磁盘上的同名目录必是历史残骸。
//
// 这里直接测该方法而非 Create 全流程：Create 要先过配额（quota.CountSkills 直接查库），
// 跑不起纯内存测试；而 WriteFile 会复用已存在的目录，留着残骸就等于让旧附属文件"复活"蹭进新技能。
func TestCreatePurgesStaleSkillDir(t *testing.T) {
	const (
		schema   = "t0"
		agentID  = "agent-me"
		dir      = "tenants/t0/agents/joker/skills/old"
		skillKey = dir + "/SKILL.md"
	)
	ctx := context.Background()

	newManager := func(t *testing.T) (*skillManager, storage.Storage) {
		t.Helper()
		st := storage.NewFilesystemStorage(t.TempDir())
		return &skillManager{schema: schema, agentID: agentID, agentName: "joker", storage: st}, st
	}

	t.Run("有残留：旧附属文件不进入新技能", func(t *testing.T) {
		m, st := newManager(t)
		writeSkillFile(t, st, skillKey, "# 上一版")
		writeSkillFile(t, st, dir+"/docs/spec.md", "上一版的附属文件")

		// 复刻 Create 的步骤：清残留 → 写入口文件
		if err := m.purgeStaleSkillDir(ctx, "joker", "old"); err != nil {
			t.Fatalf("purgeStaleSkillDir = %v, want nil", err)
		}
		writeSkillFile(t, st, skillKey, "# 这一版")

		if skillPathExists(t, st, dir+"/docs/spec.md") {
			t.Error("残留的附属文件应被清掉，不能继承到同名新建的技能里")
		}
		body, err := st.ReadFile(ctx, skillKey)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "# 这一版" {
			t.Errorf("入口文件=%q, want %q", body, "# 这一版")
		}
	})

	t.Run("无残留：不报错（幂等）", func(t *testing.T) {
		m, _ := newManager(t)
		if err := m.purgeStaleSkillDir(ctx, "joker", "old"); err != nil {
			t.Fatalf("目录不存在时应静默跳过, got %v", err)
		}
	})
}

// ===== 超级智能体的 list_skills 全可见口径 =====

// stubSkillRepoForSupervisor 只实现超级智能体全可见口径用到的仓库方法（其余由嵌入接口兜底）。
type stubSkillRepoForSupervisor struct {
	skillsrepo.Repository
	list     []models.Skill
	bindings map[string][]string
}

func (s *stubSkillRepoForSupervisor) ListForSupervisor(_, _ string) ([]models.Skill, error) {
	return s.list, nil
}

func (s *stubSkillRepoForSupervisor) ListBindingNamesBySkills(_ string, _ []string) (map[string][]string, error) {
	return s.bindings, nil
}

// 超级智能体是 skill 的管理者：list_skills 必须能列出「未绑定它的租户级 skill」——否则会出现
// 「能改却看不见」（update_shared_skill / read_skill(by id) 都能动，list 却列不出），
// 并批量回填白名单供管理。非超级智能体不走这条分支（口径不变）。
func TestListForSupervisorSeesUnboundTenantSkills(t *testing.T) {
	repo := &stubSkillRepoForSupervisor{
		list: []models.Skill{
			{ID: "s1", Name: "report", Restricted: xsql.Bool(true)},             // 租户级（agent_id 为 NULL）
			{ID: "s2", Name: "draft", AgentID: xsql.UUID("main-id"), Status: 1}, // 超级智能体自己的私有
		},
		bindings: map[string][]string{"s1": {"poet"}},
	}
	// 用例名就是「超级智能体」：身份判据要真的置上，否则走的是普通 agent 分支
	m := &skillManager{schema: "t0", agentID: "main-id", agentName: "main", isSupervisor: true, repo: repo}

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应列出全部可见 skill, got %+v", got)
	}
	byName := map[string]tools.SkillInfo{}
	for _, s := range got {
		byName[s.Name] = s
	}
	shared, ok := byName["report"]
	if !ok {
		t.Fatal("超级智能体应看到未绑定它的租户级 skill")
	}
	if !shared.Restricted {
		t.Error("租户级 skill 应回填 restricted=true")
	}
	if len(shared.BoundAgents) != 1 || shared.BoundAgents[0] != "poet" {
		t.Errorf("应回填白名单 agent 名, got %+v", shared.BoundAgents)
	}
	if private, ok := byName["draft"]; !ok || private.Restricted || private.BoundAgents != nil {
		t.Errorf("私有 skill 不应回填可见性, got %+v", byName["draft"])
	}
}
