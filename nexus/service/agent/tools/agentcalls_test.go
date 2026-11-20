package tools

import (
	"context"
	"testing"
)

// stubRelationsMgr records calls and returns preset results.
type stubRelationsMgr struct {
	removed   int64
	canCall   []string
	calledBy  []string
	gotCaller string
	gotCallee string
	called    bool
}

func (s *stubRelationsMgr) AllowCall(_ context.Context, callerID, calleeID string) error {
	s.called, s.gotCaller, s.gotCallee = true, callerID, calleeID
	return nil
}

func (s *stubRelationsMgr) RevokeCall(_ context.Context, callerID, calleeID string) (int64, error) {
	s.called, s.gotCaller, s.gotCallee = true, callerID, calleeID
	return s.removed, nil
}

func (s *stubRelationsMgr) ListRelations(context.Context, string) ([]string, []string, error) {
	return s.canCall, s.calledBy, nil
}

// stubAgentTable returns an agent by name (the call grant cases need to tell caller and target apart);
// the remaining methods fall back to the embedded interface (a call panics).
type stubAgentTable struct {
	AgentManager
	byName map[string]*AgentInfo
}

func (s stubAgentTable) Get(_ context.Context, name string) (*AgentInfo, error) {
	return s.byName[name], nil
}

// stubSkillsCapture captures Update's input (to verify restricted is forwarded).
type stubSkillsCapture struct {
	SkillsManager
	got SkillInput
}

func (s *stubSkillsCapture) Update(_ context.Context, in SkillInput) error {
	s.got = in
	return nil
}

func newCallCtx(agentName string, rel AgentRelationsManager, byName map[string]*AgentInfo) *Context {
	return &Context{
		Schema:    "t0",
		AgentName: agentName,
		AgentID:   "self",
		// The identity criterion is an attribute, not the name: using "main" for the super agent in a test means setting it
		// explicitly
		IsSupervisor: agentName == "main",
		UserID:       "u1",
		WorkKey:      "tenants/t0/users/u1/sessions/s1",
		Agents:       stubAgentTable{byName: byName},
		Skills:       &stubSkillsMgr{},
		Relations:    rel,
	}
}

// The super agent may grant itself an outgoing edge (letting the super agent call child agents - a deliberate design, see
// relations.go).
func TestAllowAgentCallFromSupervisorToOthers(t *testing.T) {
	rel := &stubRelationsMgr{}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"main": {ID: "main-id", Name: "main", Status: 1, IsSupervisor: true},
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
	})

	out, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "target": "poet"})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["allowed"] != true || m["target"] != "poet" {
		t.Errorf("unexpected result: %+v", m)
	}
	if rel.gotCaller != "main-id" || rel.gotCallee != "poet-id" {
		t.Errorf("forwarded values are wrong: caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// The callee may be the super agent: when invoked it still has the full management surface (always reading the draft), so whether
// it can be called rests entirely on this grant row - the write no longer excludes it by identity.
func TestAllowAgentCallAcceptsSupervisorTarget(t *testing.T) {
	rel := &stubRelationsMgr{}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"main": {ID: "main-id", Name: "main", Status: 1, IsSupervisor: true},
	})

	out, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "main"})
	if err != nil {
		t.Fatalf("using the super agent as the callee must not be refused: %v", err)
	}
	if m := out.(map[string]any); m["allowed"] != true || m["target"] != "main" {
		t.Errorf("unexpected result: %+v", m)
	}
	if rel.gotCaller != "poet-id" || rel.gotCallee != "main-id" {
		t.Errorf("forwarded values are wrong: caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// A self-loop is meaningless.
func TestAllowAgentCallRejectsSelf(t *testing.T) {
	rel := &stubRelationsMgr{}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
	})

	if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "poet"}); err == nil {
		t.Fatal("a self-loop should be refused")
	}
}

// A missing or deprecated target is refused (deprecation aligns with caller.go's IsRunnable check).
func TestAllowAgentCallRejectsBadTarget(t *testing.T) {
	c := newCallCtx("main", &stubRelationsMgr{}, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"dead": {ID: "dead-id", Name: "dead", Status: 3},
	})
	for _, target := range []string{"ghost", "dead"} {
		if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
			map[string]any{"agent": "poet", "target": target}); err == nil {
			t.Errorf("target=%q should be refused", target)
		}
	}
}

// The caller must be in the normal state (it reuses resolveTargetAgent's status gate).
func TestAllowAgentCallRejectsDeprecatedCaller(t *testing.T) {
	c := newCallCtx("main", &stubRelationsMgr{}, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 3},
		"bard": {ID: "bard-id", Name: "bard", Status: 1},
	})

	if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "bard"}); err == nil {
		t.Fatal("a deprecated caller should be refused")
	}
}

// Revoking does not apply the write guard: stale data (such as main having been granted by mistake) must still be cleanable.
func TestRevokeAgentCallIsLenient(t *testing.T) {
	rel := &stubRelationsMgr{removed: 1}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"main": {ID: "main-id", Name: "main", Status: 1, IsSupervisor: true},
	})

	out, err := (&revokeAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "main"})
	if err != nil {
		t.Fatalf("revoking a dangling / stale grant must not be blocked by the write guard: %v", err)
	}
	if m := out.(map[string]any); m["removed"] != int64(1) || m["revoked"] != true {
		t.Errorf("unexpected result: %+v", m)
	}
	if rel.gotCaller != "poet-id" || rel.gotCallee != "main-id" {
		t.Errorf("forwarded values are wrong: caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// The two new tools must also be super-agent only (the execution-layer criterion of the three-layer closure).
func TestAgentCallToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"allow_agent_call", "revoke_agent_call"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q must be super-agent only", name)
		}
	}
}

// update_shared_skill accepts restricted (forwarded into SkillInput).
func TestUpdateSharedSkillPassesRestricted(t *testing.T) {
	sk := &stubSkillsCapture{}
	c := &Context{Schema: "t0", AgentName: "main", AgentID: "self", UserID: "u1", Skills: sk}

	if _, err := (&updateSharedSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "report-format", "restricted": false}); err != nil {
		t.Fatal(err)
	}
	if sk.got.Restricted == nil {
		t.Fatal("restricted was not forwarded (it should be non-nil)")
	}
	if *sk.got.Restricted {
		t.Error("restricted should be false")
	}
	// Not passing it leaves it unchanged (nil)
	sk.got = SkillInput{}
	if _, err := (&updateSharedSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "report-format"}); err != nil {
		t.Fatal(err)
	}
	if sk.got.Restricted != nil {
		t.Error("without restricted it should be nil (unchanged)")
	}
}

// stubCaller records the CallRequest so the language pass-through can be asserted.
type stubCaller struct {
	AgentCaller
	got CallRequest
}

func (s *stubCaller) Call(_ context.Context, req CallRequest) (string, error) {
	s.got = req
	return "ok", nil
}

// A decided language travels to the callee - a caller pin or the agent's own default_language, so a chain keeps one language.
// The bare product fallback does not travel: as a pin it would force "en" onto the children of a Chinese conversation.
func TestCallAgentPassesDecidedLanguage(t *testing.T) {
	cases := []struct {
		name string
		ctx  Context
		want string
	}{
		{"caller pin", Context{OutputLanguage: "zh-CN", OutputLanguagePinned: true, OutputLanguageChosen: true}, "zh-CN"},
		{"agent default", Context{OutputLanguage: "zh-CN", OutputLanguageChosen: true}, "zh-CN"},
		{"product fallback", Context{OutputLanguage: "en"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := &stubCaller{}
			c := tc.ctx
			c.Schema, c.AgentName, c.AgentID, c.Caller = "t0", "caller", "self", caller
			if _, err := (&callAgentTool{}).Execute(context.Background(), &c,
				map[string]any{"agent_name": "poet"}); err != nil {
				t.Fatal(err)
			}
			if caller.got.OutputLanguage != tc.want {
				t.Fatalf("OutputLanguage = %q, want %q", caller.got.OutputLanguage, tc.want)
			}
		})
	}
}

// A private skill does not accept restricted: its visibility is decided by the owning agent, and ignoring the argument silently
// would make the model believe it changed something.
func TestUpdateOwnSkillRejectsRestricted(t *testing.T) {
	sk := &stubSkillsCapture{}
	c := &Context{Schema: "t0", AgentName: "joker", AgentID: "self", UserID: "u1", Skills: sk}

	if _, err := (&updateOwnSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "my-skill", "restricted": true}); err == nil {
		t.Fatal("the own series should refuse the restricted argument")
	}
}
