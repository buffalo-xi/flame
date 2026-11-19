package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/models"
	"flame/nexus/shared/storage"
)

// Recognising the shape of an entity-like path inside the workspace (non-super agents only).
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

// Your own definition domain and someone else's need different guidance: your own entry should give an actionable root-addressed
// form, while the other one can only point at the administrator / super agent (non-super agents have no create_agent permission).
func TestEntityLikePathSelfHint(t *testing.T) {
	self, ok := entityLikePath("agents/joker/skills/foo/a.md", "joker")
	if !ok || !strings.Contains(self, "/agents/joker/skills/") {
		t.Errorf("your own definition domain should point at the root-addressed form, got %q", self)
	}
	other, ok := entityLikePath("agents/poet/skills/foo/a.md", "joker")
	if !ok || strings.Contains(other, "/agents/joker/skills/") {
		t.Errorf("someone else's definition domain must not point at your own path, got %q", other)
	}
}

// A non-super agent creating a fake entity in the workspace -> refused; strictness B: an existing path of the same name passes.
func TestResolveWriteRejectsFakeEntity(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	c := &Context{
		Schema:    "t0",
		AgentName: "joker",
		WorkKey:   "tenants/t0/users/u1/sessions/s1",
		Storage:   st,
	}
	ctx := context.Background()

	// Creating a fake entity: refused (and the error must carry the correct guidance)
	for _, p := range []string{"skills/foo/SKILL.md", "agents/poet/AGENT.md"} {
		_, _, _, err := resolveWrite(ctx, c, p)
		if err == nil {
			t.Errorf("resolveWrite(%q) = nil, want error", p)
		}
	}

	// An ordinary workspace file: allowed
	if _, _, _, err := resolveWrite(ctx, c, "notes/a.md"); err != nil {
		t.Errorf("resolveWrite(notes/a.md) = %v, want nil", err)
	}

	// Already existing (e.g. a directory the user uploaded with the same name): allowed - strictness B
	p := "skills/bar/SKILL.md"
	if err := st.WriteFile(ctx, c.StoragePath(p), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveWrite(ctx, c, p); err != nil {
		t.Errorf("resolveWrite(%q) should pass when it already exists, got %v", p, err)
	}

	// The supervisor's workspace writes are not subject to this gate (its cross-domain writes are governed by the "/" prefix plus
	// checkWrite)
	// Identity is an attribute, not the name: changing the name requires setting the flag too, or the ordinary agent branch is still
	// taken
	c.AgentName = "main"
	c.IsSupervisor = true
	if _, _, _, err := resolveWrite(ctx, c, "skills/foo/SKILL.md"); err != nil {
		t.Errorf("super agent resolveWrite = %v, want nil", err)
	}
}

// ===== The root-addressing gate (D23 / D32): a non-super agent may read its own definition directory and may only write into its
// private skill directories =====

// stubAgentsByName returns an agent by name. The cross-domain gate needs it both for the **target's** status (checkWrite) and for
// its **own** status (checkWriteOwn), so it cannot return a single fixed name the way stubAgents does.
type stubAgentsByName struct {
	AgentManager
	byName map[string]*AgentInfo
}

func (s stubAgentsByName) Get(_ context.Context, name string) (*AgentInfo, error) {
	return s.byName[name], nil
}

// stubSkillsForWrite implements FindForWrite only (the root-addressed write gate decides whether a skill exists, who owns it and what
// state it is in).
// The remaining methods fall back to the embedded interface (a call panics), so a case cannot quietly rely on unstubbed behaviour.
type stubSkillsForWrite struct {
	SkillsManager
	// byOwner: owning agent uuid -> skill name -> info ("" means tenant-level, used to prove cross-owner access is refused)
	byOwner map[string]map[string]*SkillInfo
}

func (s stubSkillsForWrite) FindForWrite(_ context.Context, ownerAgentID, skillName string) (*SkillInfo, error) {
	if m, ok := s.byOwner[ownerAgentID]; ok {
		return m[skillName], nil
	}
	return nil, nil
}

// newOwnSkillCtx builds a Context that can pass the root-addressing gate: self = self / self-status,
// and the private skills it owns = skills (keyed by skill name).
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
		// The identity criterion is an attribute, not the name: using "main" for the super agent in a test means setting it
		// explicitly
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

// The non-super agent's root-addressed write gate: the allowlist is agents/{self}/skills/{skill}/**, everything else is refused.
func TestResolveWriteOwnScope(t *testing.T) {
	ctx := context.Background()
	own := map[string]*SkillInfo{"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}}

	cases := []struct {
		name    string
		status  int
		path    string
		wantErr bool
	}{
		{"a file under your own skill", 1, "/agents/joker/skills/foo/a.md", false},
		{"a deep subdirectory of your own skill", 1, "/agents/joker/skills/foo/docs/deep/a.md", false},
		{"your own skill's entry file (content editable)", 1, "/agents/joker/skills/foo/SKILL.md", false},
		{"someone else's agent", 1, "/agents/poet/skills/foo/a.md", true},
		{"an unregistered skill name", 1, "/agents/joker/skills/bar/a.md", true},
		{"the entity root itself", 1, "/agents/joker/skills/foo", true},
		{"your own AGENT.md (out of scope here)", 1, "/agents/joker/AGENT.md", true},
		{"a tenant-level shared skill", 1, "/skills/foo/SKILL.md", true},
		{"the user domain", 1, "/users/u1/files/a.md", true},
		{"your own deprecated state", 3, "/agents/joker/skills/foo/a.md", true},
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

// A deprecated skill itself is equally unwritable (the same rule as the agent side and the meta tool side).
func TestResolveWriteOwnSkillStatusGate(t *testing.T) {
	ctx := context.Background()
	c := newOwnSkillCtx(t, "joker", 1, map[string]*SkillInfo{
		"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusDeprecated},
	})
	if _, _, _, err := resolveWrite(ctx, c, "/agents/joker/skills/foo/a.md"); err == nil {
		t.Error("it should be refused when the skill is deprecated")
	}
}

// The super agent writing a private skill owned by a **deprecated agent** is refused as well. This gate is the dual of checkWriteOwn's
// third criterion: that one checks "am I in the normal state", this one checks "is the owner in the normal state" - one criterion,
// two tiers.
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
				// The skill itself is always in the normal state, so any block can only come from the owning-agent gate
				"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: 1}},
			}},
		}
	}

	if _, _, _, err := resolveWrite(ctx, newCtx(models.StatusDeprecated), "/agents/poet/skills/foo/a.md"); err == nil {
		t.Error("writing a private skill of a deprecated owning agent should be refused")
	}
	// Regression: with the owning agent in the normal state it passes as usual - do not block the whole path
	if _, _, _, err := resolveWrite(ctx, newCtx(models.StatusNormal), "/agents/poet/skills/foo/a.md"); err != nil {
		t.Errorf("it should pass with the owning agent in the normal state, got %v", err)
	}
	// Reading is unaffected (D4: deprecated is still readable)
	if _, _, _, err := resolveRead(newCtx(models.StatusDeprecated), "/agents/poet/skills/foo/a.md"); err != nil {
		t.Errorf("reading must not depend on the owning agent's status, got %v", err)
	}
}

// A missing AgentID must be refused: FindForWrite treats agentID=="" as "tenant-level",
// and without this check a shared skill would be mistaken for a private one and allowed.
func TestResolveWriteOwnRejectsEmptyAgentID(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, nil)
	c.AgentID = ""
	if _, _, _, err := resolveWrite(context.Background(), c, "/agents/joker/skills/foo/a.md"); err == nil {
		t.Fatal("a missing AgentID should be refused (so a shared skill is not mistaken for a private one)")
	}
}

// The non-super agent's root-addressed read gate: it may read its own definition directory (private skills included), but there is
// **no status gate** (reading is unrestricted, D4).
func TestResolveReadOwnScope(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		status  int
		path    string
		wantErr bool
	}{
		{"your own skill file", "joker", 1, "/agents/joker/skills/foo/a.md", false},
		{"your own skill entry", "joker", 1, "/agents/joker/skills/foo/SKILL.md", false},
		{"an unregistered skill name (reading does not query the database)", "joker", 1, "/agents/joker/skills/nope/a.md", false},
		{"a deprecated agent can still read its own skill", "joker", 3, "/agents/joker/skills/foo/a.md", false},
		{"someone else's AGENT.md", "joker", 1, "/agents/poet/AGENT.md", true},
		{"your own AGENT.md (readable)", "joker", 1, "/agents/joker/AGENT.md", false},
		{"an attached file in your definition domain (readable)", "joker", 1, "/agents/joker/notes/plan.md", false},
		{"your own definition root (listable)", "joker", 1, "/agents/joker", false},
		{"your own AGENT.md (readable when deprecated too)", "joker", 3, "/agents/joker/AGENT.md", false},
		{"someone else's skill", "joker", 1, "/agents/poet/skills/foo/a.md", true},
		{"a tenant-level shared skill", "joker", 1, "/skills/foo/SKILL.md", true},
		{"the user domain", "joker", 1, "/users/u1/files/a.txt", true},
		{"the super agent can read the whole tenant", "main", 1, "/agents/poet/AGENT.md", false},
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

// Opening up reading must not open up writing: your own AGENT.md is still editable only through the meta tool (D10 / D32).
func TestResolveWriteOwnAgentMDStillDenied(t *testing.T) {
	c := newOwnSkillCtx(t, "joker", 1, nil)
	if _, _, _, err := resolveWrite(context.Background(), c, "/agents/joker/AGENT.md"); err == nil {
		t.Fatal("a non-super agent must not be able to write its own AGENT.md")
	}
}

// The version domain (tenants/<schema>/versions/**) must be completely unreachable for the model - not even for the super agent.
//
// It lives under the tenant root and its protection rests entirely on gate.go's default branch of the path switch: should anyone add
// a case later (say "browse files in the tenant root"), this assertion goes red immediately and reminds them that version history
// must never be read or changed by the model.
func TestVersionDomainUnreachable(t *testing.T) {
	paths := []string{
		"/versions/agents/poet/v1/AGENT.md",
		"/versions/agents/poet/v1/skills/verse/SKILL.md",
		"/versions/skills/foo/v1/SKILL.md",
		"/versions/tools/calc/v1/main.py",
	}
	for _, p := range paths {
		ctx := context.Background()
		c := newOwnSkillCtx(t, "main", 1, nil) // main = supervisor: even it must be refused
		if _, _, _, err := resolveRead(c, p); err == nil {
			t.Errorf("resolveRead(%q) should be refused: the version domain is not model-readable", p)
		}
		if _, _, _, err := resolveWrite(ctx, c, p); err == nil {
			t.Errorf("resolveWrite(%q) should be refused: versions are not model-writable", p)
		}
	}
}

// ===== The owning agent's status gate: a private skill may only be written when its owner is in the normal state =====
//
// The gate on the skill's own status is unaffected (see TestResolveWriteOwnSkillStatusGate).
// The own tier and the super agent's cross-domain tier must share one rule.

// The own tier: checkWriteOwn's third criterion.
func TestResolveWriteOwnSkillOwnerStatus(t *testing.T) {
	ctx := context.Background()
	own := map[string]*SkillInfo{"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusNormal}}

	cases := []struct {
		name       string
		selfStatus int
		wantErr    bool
	}{
		{"normal -> allowed", models.StatusNormal, false},
		{"deprecated -> refused", models.StatusDeprecated, true},
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

// The super agent's cross-domain tier: checkWrite's skill branch (judging the owner), the dual of the own tier.
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
				// The skill itself is always in the normal state, so the conclusion can only come from the owning-agent gate
				"poet-id": {"foo": {ID: "sk1", Name: "foo", Scope: SkillScopeAgent, Status: models.StatusNormal}},
			}},
		}
	}

	cases := []struct {
		name        string
		ownerStatus int
		wantErr     bool
	}{
		{"owner normal -> allowed", models.StatusNormal, false},
		{"owner deprecated -> refused", models.StatusDeprecated, true},
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
