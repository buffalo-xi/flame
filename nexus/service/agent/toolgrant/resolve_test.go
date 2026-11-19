package toolgrant

import (
	"reflect"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	mcpconnections "flame/nexus/shared/repository/mcpconnections"
	rpcconnections "flame/nexus/shared/repository/rpcconnections"
	"flame/nexus/shared/repository/toolgrants"
)

// ===== Test doubles =====
//
// Always "embed the interface + override only what Resolve calls": an unoverridden method panics when called, so a future repository
// dependency added to Resolve blows up here immediately instead of silently returning a zero value.

// stubGrants is a fake grant repository (agent grants + skill tool dependencies).
// The "tools are unrestricted" switch now lives on the agent itself and is no longer read from here - cases pass it to NewResolver
// directly.
type stubGrants struct {
	toolgrants.Repository
	agentGrants []models.AgentToolGrant
	skillGrants map[string][]models.SkillToolGrant
}

func (s *stubGrants) ListAgentGrants(string, string) ([]models.AgentToolGrant, error) {
	return s.agentGrants, nil
}

func (s *stubGrants) ListSkillGrants(_ string, skillID string) ([]models.SkillToolGrant, error) {
	return s.skillGrants[skillID], nil
}

// stubMCPRepo is an empty MCP connection repository: AllTools only calls ListTools when there is a connection, so empty is enough.
type stubMCPRepo struct{ mcpconnections.Repository }

func (s *stubMCPRepo) List(*auth.AuthInfo, *mcpconnections.ListMcpConnectionsRequest) ([]models.McpConnection, int, error) {
	return nil, 0, nil
}

// stubRPCRepo is an empty RPC connection repository.
type stubRPCRepo struct{ rpcconnections.Repository }

func (s *stubRPCRepo) List(*auth.AuthInfo, *rpcconnections.ListRpcConnectionsRequest) ([]models.RpcConnection, int, error) {
	return nil, 0, nil
}

// newResolveCatalog builds a catalog with built-in tools only that never touches the database (in-package tests may fill private fields
// directly).
func newResolveCatalog(g toolgrants.Repository) *Catalog {
	return &Catalog{
		schema:       "t0",
		isSupervisor: true,
		grants:       g,
		mcp:          &stubMCPRepo{},
		rpc:          &stubRPCRepo{},
		eps:          &fakeEndpointRepo{}, // reuses the fake endpoint repository from catalog_test.go
	}
}

// The grant criterion of Resolve (the core of the change, pinned here):
//
//	required - the only grant that can take effect on its own: always injected, and deny cannot touch it;
//	allow    - only a filter over "tool dependencies declared by skills activated in this run"; it **never takes effect on its own**;
//	deny     - subtraction, with no power over required;
//	unrestricted - turns off the allow narrowing, so every declared dependency is granted;
//	a skill with no dependencies - allow is never injected (no fallback to "a grant takes effect"), only required remains.
//
// This rule decides the permission boundary, so a semantic change must change this test - otherwise a `case` reordering goes unnoticed.
func TestResolveGrantSemantics(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"   // required
		allowRef    = "builtin:exec"        // allow, and declared by a skill
		idleAllow   = "builtin:write_file"  // allow, but declared by no skill
		denyRef     = "builtin:delete_file" // deny, and declared by a skill
		skillID     = "sk1"
	)

	grant := func(kind, ref string) models.AgentToolGrant {
		return models.AgentToolGrant{GrantKind: kind, ItemType: models.GrantItemTool, Ref: ref}
	}
	dep := func(ref string) models.SkillToolGrant {
		return models.SkillToolGrant{SkillID: skillID, ItemType: models.GrantItemTool, Ref: ref}
	}

	cases := []struct {
		name         string
		grants       []models.AgentToolGrant
		skillDeps    []models.SkillToolGrant
		unrestricted bool
		want         []string // the effective tool names (required first, the rest in skill declaration order)
		wantDegraded bool
		wantWarn     string // substring the warnings must contain (empty = not checked; the copy itself is user-facing)
	}{
		{
			name: "skill has no dependencies: allow is never injected, only required remains",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, idleAllow),
			},
			want:         []string{"read_file"},
			wantDegraded: true,
			wantWarn:     "allow 授权本轮全部未生效",
		},
		{
			name: "skill has no dependencies + unrestricted: still required only (the switch does not look at allow either)",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
			},
			unrestricted: true,
			want:         []string{"read_file"},
			wantDegraded: true,
			wantWarn:     "allow 授权本轮全部未生效",
		},
		{
			name: "with dependencies: required ∪ (candidates ∩ allow) - idleAllow was not declared, so it stays out",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, idleAllow),
			},
			skillDeps: []models.SkillToolGrant{dep(allowRef)},
			// The builtin:exec group holds exec and run_skill_script (both go through /v1/exec, see the category notes in
			// tools/skillrun.go); shell / script each form their own group.
			want: []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name: "with dependencies but unrestricted: every candidate is granted, without allow narrowing",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
			},
			skillDeps:    []models.SkillToolGrant{dep(allowRef)},
			unrestricted: true,
			want:         []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name: "deny subtracts candidates but cannot touch required",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, denyRef),
				grant(models.GrantKindDeny, denyRef),     // hits a candidate -> subtracted
				grant(models.GrantKindDeny, requiredRef), // hits required -> no effect
			},
			skillDeps: []models.SkillToolGrant{dep(allowRef), dep(denyRef)},
			want:      []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name:   "all three empty: nothing at all",
			grants: nil,
			want:   []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &stubGrants{
				agentGrants: tc.grants,
				skillGrants: map[string][]models.SkillToolGrant{skillID: tc.skillDeps},
			}
			// Only a skill that really declares dependencies goes into the "activated in this run" list (the same criterion as
			// LoadedSkills)
			visible := []VisibleSkill{}
			if len(tc.skillDeps) > 0 {
				visible = append(visible, VisibleSkill{ID: skillID, Name: skillID})
			}

			res, err := NewResolver(newResolveCatalog(g), "t0", visible, tc.unrestricted).Resolve("agent-1")
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}

			got := res.FinalNames
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("effective tools = %v, want %v", got, tc.want)
			}
			if res.Degraded != tc.wantDegraded {
				t.Errorf("Degraded = %v, want %v", res.Degraded, tc.wantDegraded)
			}
			if tc.wantWarn != "" {
				joined := strings.Join(res.Warnings, "\n")
				if !strings.Contains(joined, tc.wantWarn) {
					t.Errorf("warnings = %q, want them to contain %q", joined, tc.wantWarn)
				}
			}
		})
	}
}

// The counter-proof: allow and the "candidates" must both come from **skills activated in this run**.
// A skill that use_skill never activated must not bring its tools in even when it declares them (bound to that agent or not) - the
// criterion is deliberately tight, to prevent privilege expansion.
func TestResolveIgnoresUnboundSkillDeps(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"
		allowRef    = "builtin:exec"
		skillID     = "sk1"
	)
	g := &stubGrants{
		agentGrants: []models.AgentToolGrant{
			{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: allowRef},
		},
		// The skill declares exec, but it is not listed as "activated in this run" below
		skillGrants: map[string][]models.SkillToolGrant{
			skillID: {{SkillID: skillID, ItemType: models.GrantItemTool, Ref: allowRef}},
		},
	}

	res, err := NewResolver(newResolveCatalog(g), "t0", nil, false).Resolve("agent-1")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if want := []string{"read_file"}; !reflect.DeepEqual(res.FinalNames, want) {
		t.Errorf("effective tools = %v, want %v (an inactive skill must not bring tools in)", res.FinalNames, want)
	}
}

// The dependency snapshot wins over the live table: activating a skill in a production run carries the **current version's** declaration,
// and dependencies added or removed in the draft must not leak into production (freezing the display but not what takes effect is not a
// freeze at all).
func TestResolvePrefersFrozenDeps(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"
		frozenRef   = "builtin:exec"       // in both the snapshot and the live table
		draftOnly   = "builtin:write_file" // live table only (a dependency newly added in the draft)
		skillID     = "sk1"
	)
	agentGrants := []models.AgentToolGrant{
		{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: frozenRef},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: draftOnly},
	}
	liveGrants := map[string][]models.SkillToolGrant{
		skillID: {
			{SkillID: skillID, ItemType: models.GrantItemTool, Ref: frozenRef},
			{SkillID: skillID, ItemType: models.GrantItemTool, Ref: draftOnly},
		},
	}

	cases := []struct {
		name  string
		deps  []string
		want  []string
		cause string
	}{
		{
			name:  "has a snapshot: only the snapshot counts, a dependency added in the draft stays out",
			deps:  []string{frozenRef},
			want:  []string{"read_file", "exec", "run_skill_script"}, // the builtin:exec group holds these two
			cause: "a dependency added to the skill in the draft must not light up in production",
		},
		{
			name:  "empty snapshot: this version really has no dependencies, no fallback to the live table",
			deps:  []string{},
			want:  []string{"read_file"},
			cause: "an empty slice is authoritative; falling back to the database would let the draft decide",
		},
		{
			name:  "nil snapshot: no version information, fall back to the live table (the legacy criterion)",
			deps:  nil,
			want:  []string{"read_file", "exec", "run_skill_script", "write_file"},
			cause: "the admin preview / an unversioned scenario still has to see the live declaration",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &stubGrants{agentGrants: agentGrants, skillGrants: liveGrants}
			visible := []VisibleSkill{{ID: skillID, Name: skillID, Deps: tc.deps}}
			res, err := NewResolver(newResolveCatalog(g), "t0", visible, false).Resolve("agent-1")
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}
			if !reflect.DeepEqual(res.FinalNames, tc.want) {
				t.Errorf("effective tools = %v, want %v (%s)", res.FinalNames, tc.want, tc.cause)
			}
		})
	}
}

// Truncation priority: required is always kept, the rest follow skill declaration order.
// The old implementation truncated by name, so activating one more skill could silently cut required or an earlier-declared dependency.
func TestResolveTruncationPrefersRequired(t *testing.T) {
	const skillID = "sk1"
	requiredRef := "builtin:list_agents"
	firstDep := "builtin:write_file" // declared first
	secondDep := "builtin:exec"      // earlier alphabetically, but declared later - so it should be dropped first

	g := &stubGrants{
		agentGrants: []models.AgentToolGrant{
			{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: firstDep},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: secondDep},
		},
		skillGrants: map[string][]models.SkillToolGrant{
			skillID: {
				{SkillID: skillID, ItemType: models.GrantItemTool, Ref: firstDep},
				{SkillID: skillID, ItemType: models.GrantItemTool, Ref: secondDep},
			},
		},
	}
	r := NewResolver(newResolveCatalog(g), "t0", []VisibleSkill{{ID: skillID, Name: skillID}}, false)
	r.MaxTools = 2

	res, err := r.Resolve("agent-1")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	// required must be there, and the earlier-declared write_file beats the alphabetically earlier exec
	want := []string{"list_agents", "write_file"}
	if !reflect.DeepEqual(res.FinalNames, want) {
		t.Errorf("tools after truncation = %v, want %v", res.FinalNames, want)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "截断") {
		t.Errorf("truncation should warn, got %v", res.Warnings)
	}
}

// A custom tool grant must really end up in the final tool set.
//
// The old bug: AllTools() missed the script family - ToolsOfRef expanded fine, but the last step dropped it as "the referenced tool no
// longer exists: custom:xxx". It showed up as "the grant saved fine and the agent never sees it", with the warning text pointing at the ref
// (so the ref looked wrong) when the catalog was actually missing a whole tool family.
func TestResolveInjectsCustomTool(t *testing.T) {
	const short = "interest-final-amount"
	g := &stubGrants{agentGrants: []models.AgentToolGrant{{
		GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool,
		Ref: ScopeCustom + ":" + short,
	}}}
	cat := newResolveCatalog(g)
	cat.scripts = stubScriptRepo{list: []models.CustomTool{{Name: short, Status: 1}}}

	res, err := NewResolver(cat, "t0", nil, false).Resolve("agent-joker")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	want := []string{tools.CustomToolPrefix + short}
	if !reflect.DeepEqual(res.FinalNames, want) {
		t.Fatalf("effective tools = %v, want %v (a required grant on a custom tool is always injected)", res.FinalNames, want)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "已不存在") {
			t.Errorf("it must not report \"已不存在\" (that means AllTools is missing custom tools again): %s", w)
		}
	}
}
