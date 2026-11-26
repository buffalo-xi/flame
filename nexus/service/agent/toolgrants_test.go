package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	skillsrepo "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
)

// ===== Test doubles =====
//
// The rule is always "embed the interface + override only the methods used": calling an un-overridden method panics, so when the implementation
// gains a new repository dependency the test blows up immediately instead of silently getting a zero value
// (the same shape as stubGrants in service/agent/toolgrant/resolve_test.go).

// stubGrantRepo is a fake grant repository (agent grants + skill tool dependencies).
// The "unrestricted tools" switch now belongs to the agent itself and is not here (see stubAgentsRepoForSwitch).
type stubGrantRepo struct {
	toolgrants.Repository
	skillDeps map[string][]models.SkillToolGrant // skillID -> declared tool dependencies
	grants    []models.AgentToolGrant            // what ListAgentGrants returns
	upserted  []models.AgentToolGrant            // records the arguments of UpsertAgentGrant
	removed   int64                              // what DeleteAgentGrant returns
}

func (s *stubGrantRepo) ListAgentGrants(_, _ string) ([]models.AgentToolGrant, error) {
	return s.grants, nil
}

func (s *stubGrantRepo) ListSkillGrants(_, skillID string) ([]models.SkillToolGrant, error) {
	return s.skillDeps[skillID], nil
}

func (s *stubGrantRepo) UpsertAgentGrant(_, _, _ string, g models.AgentToolGrant) error {
	s.upserted = append(s.upserted, g)
	return nil
}

func (s *stubGrantRepo) DeleteAgentGrant(_, _, _ string) (int64, error) { return s.removed, nil }

// stubAgentsRepoForSwitch is a fake agent repository: it only overrides the write of the "unrestricted tools" switch.
type stubAgentsRepoForSwitch struct {
	agents.Repository
	agentID      string
	unrestricted bool
}

func (s *stubAgentsRepoForSwitch) SetUnrestricted(_ *auth.AuthInfo, agentID string, v bool) error {
	s.agentID, s.unrestricted = agentID, v
	return nil
}

// stubSkillRepo is a fake skill repository (only the methods used are overridden).
type stubSkillRepo struct {
	skillsrepo.Repository
	injected []models.Skill             // ListInjected: decides the candidate source
	bindings []models.AgentSkillBinding // ListBindings: the visibility whitelist
}

func (s *stubSkillRepo) ListInjected(_, _ string) ([]models.Skill, error) { return s.injected, nil }

func (s *stubSkillRepo) ListBindings(_, _ string) ([]models.AgentSkillBinding, error) {
	return s.bindings, nil
}

// newGrantMgr builds a manager backed only by stub repositories (the current identity is fixed as the super agent).
func newGrantMgr(gr *stubGrantRepo, sk *stubSkillRepo) *toolGrantManager {
	if gr == nil {
		gr = &stubGrantRepo{}
	}
	if sk == nil {
		sk = &stubSkillRepo{}
	}
	return &toolGrantManager{
		schema: "t0", agentID: "main-id", agentName: "main", userID: "u1",
		// The "identity fixed as the super agent" promised above must really be set: otherwise the first identity gate rejects everything
		isSupervisor: true,
		repo:         gr, skills: sk,
		// The target identity lookup goes through a stub: without one it would hit the database (a unit test has no db.DB).
		// The target "poet" is an ordinary agent, so the catalog is computed as an ordinary identity too - matching production.
		agentsRepo: stubAgentsRepoForSkill{inst: &models.Agent{Name: "poet"}},
	}
}

// ===== GrantAgentTool: automatic kind assignment / explicit kind =====

// The target's injected skill declares the dependency -> write allow (on demand).
func TestGrantAutoAllowWhenSkillDeclares(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:exec"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "data-crunching"}}}
	m := newGrantMgr(gr, sk)

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindAllow {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindAllow)
	}
	if !strings.Contains(reason, "data-crunching") {
		t.Errorf("the reason should name the skill that backs it: %q", reason)
	}
	if len(gr.upserted) != 1 {
		t.Fatalf("should write 1 row, got %d", len(gr.upserted))
	}
	g := gr.upserted[0]
	if g.GrantKind != models.GrantKindAllow || g.Ref != "builtin:exec" || g.AgentID != "poet-id" {
		t.Errorf("written row does not match: %+v", g)
	}
	if g.ItemType != models.GrantItemGroup {
		t.Errorf("builtin:exec is a category group, item_type should be group, got %q", g.ItemType)
	}
}

// No skill declares the dependency -> write required (otherwise allow would never take effect).
func TestGrantAutoRequiredWithoutSkill(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindRequired)
	}
	if !strings.Contains(reason, "required") {
		t.Errorf("the reason should explain why required was written: %q", reason)
	}
	if len(gr.upserted) != 1 || gr.upserted[0].ItemType != models.GrantItemTool {
		t.Errorf("a single-tool ref should get item_type tool: %+v", gr.upserted)
	}
}

// Matching intersects the **expanded leaf tools** instead of comparing ref strings:
// the skill declares the category group builtin:file, and granting the single tool builtin:read_file must still hit allow.
func TestGrantMatchIsExpandedNotStringEqual(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:file"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "fs-skill"}}}
	m := newGrantMgr(gr, sk)

	kind, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindAllow {
		t.Fatalf("a category group expanding to the single tool should write allow, got %q", kind)
	}
}

// An allow exists and no skill declares it any more -> rewriting should upgrade it to required (Upsert overwrites).
func TestGrantUpgradesIdleAllowToRequired(t *testing.T) {
	gr := &stubGrantRepo{grants: []models.AgentToolGrant{
		{AgentID: "poet-id", GrantKind: models.GrantKindAllow, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
	}}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("an idle allow should be upgraded to required when rewritten, got %q", kind)
	}
}

// An explicit required pins it as resident even when a skill declares it, and the reason says "not downgraded to allow".
func TestGrantExplicitRequiredOverridesAuto(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:read_file"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "fs-skill"}}}
	m := newGrantMgr(gr, sk)

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", models.GrantKindRequired)
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("an explicit required must not be auto-judged as allow, got %q", kind)
	}
	if !strings.Contains(reason, "not downgraded") {
		t.Errorf("the reason should say it stays resident and was not downgraded: %q", reason)
	}
}

// An explicit allow with no declarer -> refused and not written (otherwise it is a dead grant that never takes effect).
func TestGrantExplicitAllowWithoutDeclarerIsRejected(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", models.GrantKindAllow); err == nil {
		t.Fatal("an explicit allow without a declaring skill should be refused")
	}
	if len(gr.upserted) != 0 {
		t.Errorf("a refused grant must not be written: %+v", gr.upserted)
	}
}

// deny replaces an existing required row and the reason says which row was overwritten (one row per ref).
func TestGrantDenyReplacesExistingKind(t *testing.T) {
	gr := &stubGrantRepo{grants: []models.AgentToolGrant{
		{AgentID: "poet-id", GrantKind: models.GrantKindRequired, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
	}}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", models.GrantKindDeny)
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindDeny {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindDeny)
	}
	if !strings.Contains(reason, models.GrantKindRequired) {
		t.Errorf("the reason should echo the overwritten kind: %q", reason)
	}
	if len(gr.upserted) != 1 || gr.upserted[0].GrantKind != models.GrantKindDeny {
		t.Errorf("should write one deny row: %+v", gr.upserted)
	}
}

// An invalid kind -> refused and not written.
func TestGrantRejectsBadKind(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "maybe"); err == nil {
		t.Fatal("an invalid kind should be refused")
	}
	if len(gr.upserted) != 0 {
		t.Error("a refused grant must not be written")
	}
}

// A "super agent only" tool cannot be granted to a non-super agent: the catalog filtered by the target identity expands to nothing -> refused.
func TestGrantRejectsSuperRefForNonSupervisor(t *testing.T) {
	m := newGrantMgr(nil, nil)

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:create_agent", ""); err == nil {
		t.Fatal("granting a super-only tool to a non-super agent should be refused")
	}
}

// A non-super agent caller (identity backstop) -> refused.
func TestGrantRejectsNonSupervisorCaller(t *testing.T) {
	m := newGrantMgr(nil, nil)
	// The identity criterion is the attribute (agents.is_supervisor), no longer the name - demoting an identity means changing this field
	m.isSupervisor = false
	m.agentName = "poet"

	if _, _, err := m.GrantAgentTool(context.Background(), "joker", "joker-id", "builtin:exec", ""); err == nil {
		t.Fatal("a non-super agent caller should be refused")
	}
}

// An invalid ref syntax -> refused and not written.
func TestGrantRejectsBadRef(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, nil)

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "not-a-ref", ""); err == nil {
		t.Fatal("an invalid ref should be refused")
	}
	if len(gr.upserted) != 0 {
		t.Error("a refused grant must not be written")
	}
}

// ===== RevokeAgentTool =====

// Deletes exactly by ref and reports the row count (idempotent: the second call is 0).
func TestRevokeReportsRemovedRows(t *testing.T) {
	gr := &stubGrantRepo{removed: 1}
	m := newGrantMgr(gr, nil)

	n, _, err := m.RevokeAgentTool(context.Background(), "poet-id", "builtin:exec")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}

	gr.removed = 0 // second call: that grant is already gone
	n, _, err = m.RevokeAgentTool(context.Background(), "poet-id", "builtin:exec")
	if err != nil {
		t.Fatalf("an idempotent revoke must not error: %v", err)
	}
	if n != 0 {
		t.Fatalf("second removed = %d, want 0", n)
	}
}

// The ref's existence is deliberately not validated: a dangling grant whose source is gone must still be revocable.
func TestRevokeAllowsDanglingRef(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, nil)

	if _, _, err := m.RevokeAgentTool(context.Background(), "poet-id", "mcp:gone:tool"); err != nil {
		t.Fatalf("revoking a dangling grant must not be blocked by ref validation: %v", err)
	}
}

// ===== Unrestricted tools switch (an attribute of the agent itself) =====

func TestSetUnrestricted(t *testing.T) {
	repo := &stubAgentsRepoForSwitch{}
	// The identity criterion is the attribute (D30): renaming alone does not make it a non-super agent
	m := &agentManager{schema: "t0", agentName: "main", userID: "u1", isSupervisor: true, repo: repo}

	if err := m.SetUnrestricted(context.Background(), "poet-id", true); err != nil {
		t.Fatal(err)
	}
	if !repo.unrestricted || repo.agentID != "poet-id" {
		t.Errorf("the switch was not written for the target: agentID=%q unrestricted=%v", repo.agentID, repo.unrestricted)
	}

	m.isSupervisor = false
	m.agentName = "poet"
	if err := m.SetUnrestricted(context.Background(), "joker-id", true); err == nil {
		t.Error("a non-super agent setting the switch should be refused")
	}
}

// ===== grantViews: effective state (pure computation, no repository) =====

func TestGrantViewsMarksEffectiveness(t *testing.T) {
	grants := []models.AgentToolGrant{
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: "builtin:list_agents"},
		// deny can only come from the admin side (the model-side grant_agent_tool can write it too, but one row per ref)
		{GrantKind: models.GrantKindDeny, ItemType: models.GrantItemTool, Ref: "builtin:write_file"},
	}
	// declaring only attributes read_file to a skill: nobody declares exec (spinning), list_agents is required
	expand := func(ref string) []string {
		if ref == "builtin:exec" {
			return []string{"builtin:exec"}
		}
		return []string{ref}
	}
	declaring := func(expanded []string) (string, bool) {
		for _, r := range expanded {
			// write_file "has a declaring skill" too: it proves deny is not mislabelled as pending because of it
			if r == "builtin:read_file" || r == "builtin:write_file" {
				return "declaring-skill", false
			}
		}
		return "", false
	}
	views := grantViews(grants, expand, declaring)

	if len(views) != 4 {
		t.Fatalf("views = %d, want 4", len(views))
	}
	// 1) allow with no declarer: never effective (spinning) plus a remedy, and it must not count as pending
	if views[0].Effective || views[0].Pending || views[0].Note == "" {
		t.Errorf("a spinning allow should be non-effective, non-pending, with a note: %+v", views[0])
	}
	// 2) allow with a declaring skill: not activated this round -> pending + via_skill (which skill lights it up)
	if views[1].Effective || !views[1].Pending || views[1].ViaSkill != "declaring-skill" {
		t.Errorf("an allow with an owner should be pending and carry via_skill: %+v", views[1])
	}
	// 3) required is always effective, independent of activation
	if !views[2].Effective || views[2].Pending {
		t.Errorf("required should always be effective and not pending: %+v", views[2])
	}
	// 4) deny: it must not be marked pending even when a skill declares it - it is "explicitly forbidden", not "waiting to take effect"
	if views[3].Effective || views[3].Pending || !strings.Contains(views[3].Note, "Explicit deny") {
		t.Errorf("deny should be non-effective, non-pending, and state that it is an explicit refusal: %+v", views[3])
	}
}

// An allow whose declaring skill was already use_skill'ed in this run is live, not pending: read_agent used to keep saying
// "it applies after use_skill ..." even after the skill had been activated.
func TestGrantViewsActivatedDeclarerIsEffective(t *testing.T) {
	grants := []models.AgentToolGrant{
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: "builtin:list_agents"},
	}
	expand := func(ref string) []string { return []string{ref} }
	declaring := func(expanded []string) (string, bool) {
		if expanded[0] == "builtin:read_file" {
			return "flow-authoring", true
		}
		return "agent-authoring", false
	}
	views := grantViews(grants, expand, declaring)

	if len(views) != 2 {
		t.Fatalf("views = %d, want 2", len(views))
	}
	if !views[0].Effective || views[0].Pending || views[0].ViaSkill != "flow-authoring" {
		t.Errorf("an activated declarer should make the row effective and not pending: %+v", views[0])
	}
	if views[1].Effective || !views[1].Pending || !strings.Contains(views[1].Note, "not activated yet") {
		t.Errorf("a declarer that was not activated stays pending: %+v", views[1])
	}
}

// read_agent passes this run's activation set only for the calling agent: the activated declarer wins over an inactive one, and
// without the set (inspecting another agent) the same row is pending again.
func TestListAgentGrantsUsesActivationForSelf(t *testing.T) {
	gr := &stubGrantRepo{
		grants: []models.AgentToolGrant{
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		},
		skillDeps: map[string][]models.SkillToolGrant{
			"s1": {{ItemType: models.GrantItemTool, Ref: "builtin:read_file"}},
			"s2": {{ItemType: models.GrantItemTool, Ref: "builtin:read_file"}},
		},
	}
	sk := &stubSkillRepo{injected: []models.Skill{
		{ID: "s1", Name: "agent-authoring"},
		{ID: "s2", Name: "flow-authoring"},
	}}
	m := newGrantMgr(gr, sk)

	views, err := m.ListAgentGrants(context.Background(), "main", "main-id", map[string]string{"s2": "flow-authoring"})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !views[0].Effective || views[0].Pending || views[0].ViaSkill != "flow-authoring" {
		t.Fatalf("the activated declarer should win and be effective: %+v", views)
	}

	other, err := m.ListAgentGrants(context.Background(), "main", "main-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Effective || !other[0].Pending || other[0].ViaSkill != "agent-authoring" {
		t.Fatalf("another agent's run has no activation set, so the row stays pending: %+v", other)
	}
}

// ===== fillVisibility: the visibility backfill of read_skill =====

// Tenant level + super agent: both restricted and the whitelist are filled in.
func TestFillVisibilityTenantForSupervisor(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{
		{AgentID: "a1", AgentName: "poet"},
		{AgentID: "a2", AgentName: "bard"},
	}}
	// The case name says "super agent": the identity criterion must really be set, or the whitelist is not filled in at all
	m := &skillManager{schema: "t0", agentName: "main", isSupervisor: true, repo: repo}

	s := &models.Skill{ID: "s1", Name: "report-format"} // empty AgentID = tenant level
	s.Restricted = true
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if !d.Restricted {
		t.Error("a tenant-level skill should get restricted filled in")
	}
	if len(d.BoundAgents) != 2 || d.BoundAgents[0] != "poet" {
		t.Errorf("the whitelist should be filled in as agent names: %+v", d.BoundAgents)
	}
}

// Tenant level + non-super agent: only restricted, no whitelist (other agents' bindings are not exposed).
func TestFillVisibilityTenantForNonSupervisor(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{{AgentID: "a1", AgentName: "poet"}}}
	m := &skillManager{schema: "t0", agentName: "joker", repo: repo}

	s := &models.Skill{ID: "s1", Name: "report-format"}
	s.Restricted = true
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if !d.Restricted {
		t.Error("a tenant-level skill should get restricted filled in")
	}
	if d.BoundAgents != nil {
		t.Errorf("a non-super agent must not see the whitelist: %+v", d.BoundAgents)
	}
}

// Private skill: restricted does not participate, and neither field is filled in.
func TestFillVisibilityPrivateSkipped(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{{AgentID: "a1", AgentName: "poet"}}}
	m := &skillManager{schema: "t0", agentName: "main", repo: repo}

	s := &models.Skill{ID: "s1", Name: "my-skill"}
	s.AgentID = "a1" // private
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if d.Restricted || d.BoundAgents != nil {
		t.Errorf("a private skill should not get visibility filled in: %+v", d)
	}
}
