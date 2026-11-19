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

// checkSkillScope is the single criterion for "which space each of the own / shared / agent_skill tool families may touch".
//
// Orthogonal to tool categorisation (skill-own / skill-admin ...) and both are needed: categorisation decides "who may use which tool", this
// decides "which space's data the tool may touch". The own family is open to everyone, so without an ownership check an agent with discovery on
// could reach a shared skill through the visibility criterion.
//
// A non-empty target = the *_agent_skill family (explicit owner, super agent only): it must land in that owner's private space.
func TestCheckSkillScope(t *testing.T) {
	const (
		me    = "agent-me"
		other = "agent-other"
	)
	shared := &models.Skill{Name: "report"}                            // tenant level: agent_id is NULL
	mine := &models.Skill{Name: "draft", AgentID: xsql.UUID(me)}       // own private skill
	theirs := &models.Skill{Name: "secret", AgentID: xsql.UUID(other)} // another agent's private skill

	cases := []struct {
		name         string
		isSupervisor bool
		scope        string
		target       string // target owning agent (the *_agent_skill family)
		s            *models.Skill
		wantErr      bool
	}{
		// own tools: only **one's own private** skills
		{"supervisor+own+my", true, tools.SkillScopeAgent, "", mine, false},
		{"supervisor+own+shared", true, tools.SkillScopeAgent, "", shared, true},
		{"supervisor+own+theirs", true, tools.SkillScopeAgent, "", theirs, true},
		{"agent+own+my", false, tools.SkillScopeAgent, "", mine, false},
		{"agent+own+shared", false, tools.SkillScopeAgent, "", shared, true},
		{"agent+own+theirs", false, tools.SkillScopeAgent, "", theirs, true},

		// shared tools: only **tenant-level shared** skills
		{"supervisor+shared+shared", true, tools.SkillScopeTenant, "", shared, false},
		{"supervisor+shared+my", true, tools.SkillScopeTenant, "", mine, true},
		{"agent+shared+shared", false, tools.SkillScopeTenant, "", shared, true},
		{"agent+shared+theirs", false, tools.SkillScopeTenant, "", theirs, true},

		// agent_skill tools (explicit owner): super agent only, and it must be exactly that owner's private skill
		{"supervisor+target+theirs", true, tools.SkillScopeAgent, other, theirs, false},
		{"supervisor+target+targetIsMe", true, tools.SkillScopeAgent, me, mine, false},
		{"supervisor+target+wrongOwner", true, tools.SkillScopeAgent, other, mine, true},
		{"supervisor+target+shared", true, tools.SkillScopeAgent, other, shared, true},
		{"supervisor+target+nil", true, tools.SkillScopeAgent, other, nil, false},
		{"agent+target+theirs", false, tools.SkillScopeAgent, other, theirs, true},
		{"agent+target+ownPrivate", false, tools.SkillScopeAgent, me, mine, true},

		// no space declared (read_skill): no check
		{"none+shared", false, "", "", shared, false},
		{"none+theirs", false, "", "", theirs, false},

		// not located (nil): the caller reports not found
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

// ===== Update rename: move the whole directory =====

// stubSkillRepoForUpdate only implements the repository methods the Update flow uses (the rest fall back to the embedded interface).
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

// stubAgentsRepoForSkill only implements GetAgent (the status gate of the owning agent).
type stubAgentsRepoForSkill struct {
	agentsrepo.Repository
	inst *models.Agent
}

func (s stubAgentsRepoForSkill) GetAgent(_ *auth.AuthInfo, _ string) (*models.Agent, error) {
	return s.inst, nil
}

// A rename must move the **whole skill directory**.
//
// A skill directory may now hold auxiliary files and subdirectories (the "/" root addressing of the file tools, see checkWriteOwn in
// tools/gate.go). Keeping "write the new SKILL.md + delete the old SKILL.md" would leave those resources orphaned in the old directory - the
// table already points at the new name, and nobody claims the old directory any more.
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

	t.Run("rename only: auxiliary files move with the directory", func(t *testing.T) {
		m, st := newManager(t)
		write(t, st, oldDir+"/SKILL.md", "# old")
		write(t, st, oldDir+"/docs/spec.md", "spec")

		if err := m.Update(ctx, tools.SkillInput{
			Name: "old", NewName: "new", Scope: tools.SkillScopeAgent,
		}); err != nil {
			t.Fatalf("Update = %v, want nil", err)
		}
		if !exists(t, st, newDir+"/SKILL.md") {
			t.Error("the entry file should have moved with the directory")
		}
		if !exists(t, st, newDir+"/docs/spec.md") {
			t.Error("auxiliary files should move with the directory (this is the capability added here)")
		}
		if exists(t, st, oldDir+"/docs/spec.md") {
			t.Error("no auxiliary file should be left in the old directory (they would become orphans)")
		}
	})

	t.Run("rename + new body: the new body lands in the new directory, auxiliary files stay", func(t *testing.T) {
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
			t.Errorf("entry file = %q, want %q", body, "# brand new")
		}
		if !exists(t, st, newDir+"/docs/spec.md") {
			t.Error("a new body must not affect auxiliary files")
		}
	})

	t.Run("body only, no rename: the directory stays put", func(t *testing.T) {
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
			t.Errorf("entry file = %q, want %q", body, "# rewritten")
		}
		if !exists(t, st, oldDir+"/docs/spec.md") {
			t.Error("without a rename the directory must not be touched")
		}
	})
}

// restricted only means something for tenant-level shared skills: passing it for a private skill must be refused **before any write**.
// If the check stayed after repo.Update, description / name would be written first and the error would come afterwards - a half-written state.
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
		t.Fatal("passing restricted for a private skill should error")
	}
	if !strings.Contains(err.Error(), "restricted only applies to tenant-level shared skills") {
		t.Errorf("the error should name restricted's scope, got %v", err)
	}
	if repo.updated {
		t.Error("the check must run before repo.Update: the metadata must not be written yet")
	}
}

// stubSkillRepoRestricted records whether Update was called (to assert the order of checks and writes).
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

// ===== checkOwnerAgent: the "private skills are writable" gate of the owning agent =====
//
// Only the normal state may write; deprecated (3) never may (criterion models.IsModifiable).
// The same criterion as the two places in tools/gate.go (the self gate / the super agent's cross-domain gate) and admin/skills'
// ensureAgentModifiable - all four must judge the same way, or "the meta tools can change it while the file tools error" splits apart.
func TestCheckOwnerAgentStatusGate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"normal -> allowed", models.StatusNormal, false},
		{"deprecated -> refused", models.StatusDeprecated, true},
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

	// A tenant-level skill (no owning agent): no check
	m := &skillManager{agents: stubAgentsRepoForSkill{}}
	if err := m.checkOwnerAgent(""); err != nil {
		t.Errorf("an empty ownerID should skip the check, got %v", err)
	}
}

// ===== "Can I change skills" in the prompt: the owning-agent gate must show up =====

// capabilityNotes: with create_own_skill granted but the agent deprecated, it must no longer claim "you may create / change your own private
// skills" - the prompt would otherwise promise a capability the gate always refuses, which contradicts the very reason it exists as a
// "before the fact" line.
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
		{"normal + own -> normal hint", models.StatusNormal, true, true, false},
		{"deprecated + own -> 'currently cannot'", models.StatusDeprecated, true, false, true},
		{"no skill tools -> neither line", models.StatusNormal, false, false, false},
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
				t.Errorf("contains \"may create / change own private skills\" = %v, want %v\n%s", got, tc.wantAllowed, notes)
			}
			if got := strings.Contains(notes, blockedHint); got != tc.wantBlocked {
				t.Errorf("contains \"currently cannot change\" = %v, want %v\n%s", got, tc.wantBlocked, notes)
			}
		})
	}
}

// stubSkillsForCatalog only implements ListInjected (skillCatalog's only dependency).
type stubSkillsForCatalog struct {
	tools.SkillsManager
	list []tools.SkillInfo
}

func (s stubSkillsForCatalog) ListInjected(context.Context) ([]tools.SkillInfo, error) {
	return s.list, nil
}

// skillCatalog: deprecated skills are already filtered by ListInjected, so only normal ones appear in the resident list and there is no need to
// tag each line "read only".
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
		t.Errorf("a normal skill should be listed as \"- name [ownership]: description\", got:\n%s", out)
	}
	if strings.Contains(out, "只读") {
		t.Errorf("the list must not carry a status marker any more, got:\n%s", out)
	}
}

// skill_discovery injection:
//   - with zero skills it must still speak up - that is exactly the switch's use case (no list to inject, so list_skills is the only way to find
//     them)
//   - on truncation it must not repeat list_skills after the "more omitted" line
func TestSkillCatalogDiscovery(t *testing.T) {
	newImpl := func(discovery bool, list []tools.SkillInfo) *agentImpl {
		return &agentImpl{
			skillDiscovery: discovery,
			toolCtx:        &tools.Context{Skills: stubSkillsForCatalog{list: list}},
		}
	}

	t.Run("zero skills + discovery on -> still points at list_skills", func(t *testing.T) {
		out := newImpl(true, nil).skillCatalog()
		if !strings.Contains(out, "list_skills") {
			t.Errorf("should point at list_skills, got %q", out)
		}
	})

	t.Run("zero skills + discovery off -> empty string", func(t *testing.T) {
		if out := newImpl(false, nil).skillCatalog(); out != "" {
			t.Errorf("should be an empty string, got %q", out)
		}
	})

	t.Run("truncated + discovery on -> list_skills mentioned once", func(t *testing.T) {
		list := []tools.SkillInfo{
			{Name: "big", Scope: tools.SkillScopeAgent, Status: models.StatusNormal,
				Description: strings.Repeat("长", maxSkillPromptChars+500)},
		}
		out := newImpl(true, list).skillCatalog()
		if !strings.Contains(out, "已省略") {
			t.Fatalf("truncation should trigger, got:\n%s", out)
		}
		if n := strings.Count(out, "list_skills"); n != 1 {
			t.Errorf("list_skills should appear exactly once, got %d\n%s", n, out)
		}
	})

	t.Run("not truncated + discovery on -> the pointer is there", func(t *testing.T) {
		list := []tools.SkillInfo{{Name: "draft", Scope: tools.SkillScopeAgent, Status: models.StatusNormal}}
		out := newImpl(true, list).skillCatalog()
		if !strings.Contains(out, "list_skills") {
			t.Errorf("should point at list_skills, got %q", out)
		}
	})
}

// ===== Delete / Create: a skill directory is a **directory**, not a single file =====

// stubSkillRepoForDelete only implements the repository methods the Delete flow uses (FindByName to locate + Delete to record).
type stubSkillRepoForDelete struct {
	skillsrepo.Repository
	skill   *models.Skill
	deleted string // the deleted skill id, for assertions
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

// Delete must remove the **whole skill directory** (SKILL.md + auxiliary files + subdirectories), the same criterion as admin/skills.
//
// The old implementation deleted skillPath with storage.Delete - that is the **file path** .../skills/{name}/SKILL.md, so auxiliary files and
// empty directories survived (that is where the probe-* empty directories under poet/skills and joker/skills in production came from).
// The point: the test skill must carry auxiliary files; with only SKILL.md, Delete and DeletePrefix behave the same and the defect stays hidden.
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
			// Both sub-cases run as "the current identity carries the super-privilege attribute" (the second one is named after the super agent)
			isSupervisor: true,
			storage:      st,
			repo:         repo,
			agents: stubAgentsRepoForSkill{inst: &models.Agent{
				Name: meName, Status: xsql.Int(models.StatusNormal),
			}},
		}, repo
	}

	t.Run("delete own private skill: auxiliary files and the directory disappear", func(t *testing.T) {
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
			t.Errorf("the table row should be deleted, deleted=%q", repo.deleted)
		}
		if skillPathExists(t, st, dir+"/SKILL.md") {
			t.Error("the entry file should be gone")
		}
		if skillPathExists(t, st, dir+"/docs/spec.md") {
			t.Error("auxiliary files should be deleted with the skill directory (the old implementation only removed SKILL.md, so this must be true)")
		}
		if skillPathExists(t, st, dir) {
			t.Error("the skill directory itself must not be left behind (recreating with the same name would inherit it)")
		}
	})

	t.Run("super agent deletes a named agent's private skill: it lands in the owner's directory", func(t *testing.T) {
		const dir = "tenants/t0/agents/joker/skills/old"
		st := storage.NewFilesystemStorage(t.TempDir())
		writeSkillFile(t, st, dir+"/SKILL.md", "# old")
		writeSkillFile(t, st, dir+"/docs/spec.md", "spec")

		// The owning agent is joker (otherID) while the current identity is the super agent: the path must use the owner's name
		sk := &models.Skill{ID: "s2", Name: "old", AgentID: xsql.UUID(otherID)}
		m, repo := newDeleteManager(t, st, sk, "agent-main", "main") // "main" = the preinstalled supervisor carrying the super privilege

		if err := m.Delete(ctx, tools.SkillRef{
			Name: "old", Scope: tools.SkillScopeAgent, OwnerID: otherID,
		}); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
		if repo.deleted != "s2" {
			t.Errorf("the table row should be deleted, deleted=%q", repo.deleted)
		}
		if skillPathExists(t, st, dir+"/docs/spec.md") || skillPathExists(t, st, dir) {
			t.Error("the skill under the owner's directory should be deleted as a whole directory")
		}
	})
}

// ===== Incremental maintenance of tool dependencies (grant_skill_tool / revoke_skill_tool) =====

// stubSkillRepoForDep only implements the super agent's lookup by name (FindForSupervisorByName).
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

// ListBindings returns an empty whitelist: fillVisibility queries it when the super agent reads a tenant-level skill (the constraint comes from
// the display path of Get).
func (s *stubSkillRepoForDep) ListBindings(_, _ string) ([]models.AgentSkillBinding, error) {
	return nil, nil
}

// stubDepGrantsRepo records dependency additions / removals and doubles as "the current full set" (ListSkillGrants).
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

// newDepMgr builds a manager with the super agent identity that locates by name; sk == nil means no such skill.
func newDepMgr(sk *models.Skill, grants *stubDepGrantsRepo) *skillManager {
	if sk != nil && sk.Status == 0 {
		sk.Status = xsql.Int(models.StatusNormal)
	}
	return &skillManager{
		schema: "t0", agentID: "agent-main", agentName: "main", userID: "u1",
		isSupervisor: true, // the identity criterion is the attribute, not the name (D30)
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
		t.Errorf("the echoed change does not match: %+v", ch)
	}
	// Only one row lands, and item_type is derived from the ref shape (a single tool -> tool)
	if len(grants.upserts) != 1 || grants.upserts[0].ItemType != models.GrantItemTool {
		t.Errorf("written content does not match: %+v", grants.upserts)
	}
	// What comes back is "the complete dependency list after the change", not just the added row (that fills read_skill's blind spot)
	if len(ch.ToolDeps) != 2 || ch.ToolDeps[0] != "builtin:exec" || ch.ToolDeps[1] != "builtin:read_file" {
		t.Errorf("should return the complete dependency list: %+v", ch.ToolDeps)
	}
}

// Idempotent: when it already exists, added=false and nothing is written again.
func TestAddToolDepIsIdempotent(t *testing.T) {
	grants := &stubDepGrantsRepo{added: false}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	ch, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file")
	if err != nil {
		t.Fatalf("AddToolDep = %v", err)
	}
	if ch.Added {
		t.Error("added should be false when it already exists")
	}
}

// A ref that cannot expand (typo / deleted) -> refused, nothing written (no dead dependency that never takes effect).
func TestAddToolDepRejectsUnknownRef(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:no_such_tool"); err == nil {
		t.Fatal("a ref that cannot expand should be refused")
	}
	if len(grants.upserts) != 0 {
		t.Error("a refused dependency must not be written")
	}
}

// Removal only validates the syntax: a dangling dependency left behind by a deleted tool / connection must still be removable.
func TestRemoveToolDepAllowsDanglingRef(t *testing.T) {
	grants := &stubDepGrantsRepo{removed: 1}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)

	ch, err := m.RemoveToolDep(context.Background(), tools.SkillRef{Name: "x"}, "mcp:gone:read")
	if err != nil {
		t.Fatalf("RemoveToolDep = %v", err)
	}
	if ch.Removed != 1 || ch.Ref != "mcp:gone:read" {
		t.Errorf("the echoed change does not match: %+v", ch)
	}
	if len(grants.removes) != 1 || grants.removes[0] != "mcp:gone:read" {
		t.Errorf("it should delete exactly by the original ref: %+v", grants.removes)
	}
}

// A deprecated skill may not change its dependencies (the same state gate as definition editing).
func TestToolDepWriteRejectsDeprecatedSkill(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	sk := &models.Skill{ID: "s1", Name: "x", Status: xsql.Int(models.StatusDeprecated)}
	m := newDepMgr(sk, grants)

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file"); err == nil {
		t.Fatal("the deprecated state should refuse dependency writes")
	}
	if len(grants.upserts) != 0 {
		t.Error("a refused dependency must not be written")
	}
}

// A non-super agent is always refused (dependencies are the trigger that makes an allow grant take effect, so whoever writes them holds the
// grant switch).
func TestToolDepWriteRejectsNonSupervisor(t *testing.T) {
	grants := &stubDepGrantsRepo{added: true}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, grants)
	m.isSupervisor = false // demoted identity: the criterion is the attribute, renaming does nothing
	m.agentName = "poet"

	if _, err := m.AddToolDep(context.Background(), tools.SkillRef{Name: "x"}, "builtin:file"); err == nil {
		t.Fatal("a non-super agent should be refused")
	}
	if len(grants.upserts) != 0 {
		t.Error("a refused dependency must not be written")
	}
}

// ===== Dependency echo of read_skill / use_skill (SkillDetail.ToolDeps) =====

// Get must return the declared dependencies as they are: no group expansion, no dangling refs dropped.
// A dangling entry is only useful for diagnosis if it stays (revoke_skill_tool can still remove it by the original ref), and swallowing it
// quietly makes the problem unfindable.
func TestGetReturnsToolDeps(t *testing.T) {
	grants := &stubDepGrantsRepo{deps: []models.SkillToolGrant{
		{ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
		{ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		{ItemType: models.GrantItemTool, Ref: "mcp:gone:read", SkillID: "s1"}, // connection deleted -> dangling
	}}
	m := newDepMgr(&models.Skill{ID: "s1", Name: "data-crunching"}, grants)
	m.storage = storage.NewFilesystemStorage(t.TempDir()) // the metadata must be returned even when the content is missing

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
			t.Fatalf("tool_deps = %v, want %v (the declared order must be preserved)", d.ToolDeps, want)
		}
	}
}

// With no dependencies it must be an empty array rather than nil (nil serialises to null, which the model reads as "unknown" instead of
// "genuinely declares none").
func TestGetToolDepsAlwaysArray(t *testing.T) {
	m := newDepMgr(&models.Skill{ID: "s1", Name: "x"}, &stubDepGrantsRepo{})
	m.storage = storage.NewFilesystemStorage(t.TempDir())

	d, err := m.Get(context.Background(), tools.SkillRef{Name: "x"})
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	if d.ToolDeps == nil || len(d.ToolDeps) != 0 {
		t.Fatalf("no dependencies should be an empty slice (not nil): %#v", d.ToolDeps)
	}
}
