package tools

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
)

// stubSkillsMgr exists only to satisfy Context.Skills != nil (resolveTargetAgent's defensive check);
// any method call panics (embedded interface).
type stubSkillsMgr struct{ SkillsManager }

// stubToolGrantsMgr records calls and returns preset results.
type stubToolGrantsMgr struct {
	kind        string
	reason      string
	removed     int64
	removedKind string
	views       []AgentGrantView
	gotName     string
	gotRef      string
	gotKind     string
	called      bool
}

func (s *stubToolGrantsMgr) ListAgentGrants(context.Context, string, string) ([]AgentGrantView, error) {
	return s.views, nil
}

func (s *stubToolGrantsMgr) GrantAgentTool(_ context.Context, name, _ string, ref, kind string) (string, string, error) {
	s.called, s.gotName, s.gotRef, s.gotKind = true, name, ref, kind
	return s.kind, s.reason, nil
}

func (s *stubToolGrantsMgr) RevokeAgentTool(context.Context, string, string) (int64, string, error) {
	s.called = true
	return s.removed, s.removedKind, nil
}

// stubAgentsForSwitch records writes of the "unrestricted" switch - that switch has moved from ToolGrants to Agents.
type stubAgentsForSwitch struct {
	stubAgents
	set    bool
	gotVal bool
}

func (s *stubAgentsForSwitch) SetUnrestricted(_ context.Context, _ string, v bool) error {
	s.set, s.gotVal = true, v
	return nil
}

// newGrantCtx builds a Context with a ToolGrants stub (the cases here only test the gate and forwarding, so no Storage is needed).
func newGrantCtx(agentName string, status int, tg ToolGrantsManager) *Context {
	return newGrantCtxWithAgents(agentName, stubAgents{status: status}, tg)
}

// newGrantCtxWithAgents is the same, but with a custom Agents stub injected (the switch arguments go through Agents).
func newGrantCtxWithAgents(agentName string, ag AgentManager, tg ToolGrantsManager) *Context {
	return &Context{
		Schema:     "t0",
		AgentName:  agentName,
		AgentID:    "self",
		UserID:     "u1",
		WorkKey:    "tenants/t0/users/u1/sessions/s1",
		Agents:     ag,
		Skills:     &stubSkillsMgr{},
		ToolGrants: tg,
	}
}

// D5 (revised): main may grant itself a tool - it is the administrator, and the boundary rests on status.
// It must also mark dirty: otherwise the fresh grant is invisible to this run and gets mistaken for a failure and retried.
func TestGrantAgentToolAllowsSelfAndRefreshesTools(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	out, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "custom:interest-final-amount"})
	if err != nil {
		t.Fatalf("granting yourself a tool should be allowed: %v", err)
	}
	if !tg.called || tg.gotRef != "custom:interest-final-amount" {
		t.Fatalf("the call should be forwarded to the manager (ref unchanged): called=%v ref=%q", tg.called, tg.gotRef)
	}
	if m := out.(map[string]any); m["granted"] != true || m["grant_kind"] != models.GrantKindRequired {
		t.Errorf("unexpected result: %+v", m)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("changing your own tool surface must mark dirty (the tool set is recomputed next round)")
	}
}

// But the deprecated state still blocks yourself: the protection rests on status, and only the admin side can change status.
func TestGrantAgentToolRejectsSelfWhenDeprecated(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 3, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "builtin:exec"}); err == nil {
		t.Fatal("the deprecated state should be refused (yourself included)")
	}
	if tg.called {
		t.Error("a refused call must not reach the manager")
	}
}

// The target must be in the normal state (status == 1); deprecated is always refused.
func TestGrantAgentToolRejectsDeprecatedTarget(t *testing.T) {
	tg := &stubToolGrantsMgr{}
	c := newGrantCtx("main", 3, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec"}); err == nil {
		t.Fatal("a deprecated target should be refused")
	}
	if tg.called {
		t.Error("a refused call must not reach the manager")
	}
}

// The normal path: the target name and ref are forwarded unchanged, and the manager decides and reports the kind (allow /
// required).
func TestGrantAgentToolForwardsAndReportsKind(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindAllow, reason: "declared by a skill"}
	c := newGrantCtx("main", 1, tg)

	out, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec", "kind": "deny"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", out)
	}
	if m["grant_kind"] != models.GrantKindAllow || m["granted"] != true {
		t.Errorf("unexpected result: %+v", m)
	}
	if tg.gotName != "poet" || tg.gotRef != "builtin:exec" || tg.gotKind != "deny" {
		t.Errorf("forwarded values are wrong: name=%q ref=%q kind=%q", tg.gotName, tg.gotRef, tg.gotKind)
	}
}

// With kind omitted an empty string is forwarded (the manager decides); the tool layer must not quietly fill it in.
func TestGrantAgentToolOmittedKindStaysEmpty(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec"}); err != nil {
		t.Fatal(err)
	}
	if tg.gotKind != "" {
		t.Errorf("an omitted kind should be forwarded as an empty string, got %q", tg.gotKind)
	}
}

func TestRevokeAgentToolReportsRemoved(t *testing.T) {
	tg := &stubToolGrantsMgr{removed: 1, removedKind: models.GrantKindAllow}
	c := newGrantCtx("main", 1, tg)

	out, err := (&revokeAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["removed"] != int64(1) || m["revoked"] != true {
		t.Errorf("unexpected result: %+v", m)
	}
	if m["grant_kind"] != models.GrantKindAllow {
		t.Errorf("it should echo the kind of the deleted row for auditing: %+v", m)
	}
}

// Revoking your own grant is allowed too, and must mark dirty as well (otherwise the tools seem not to disappear until the next
// session).
func TestRevokeAgentToolAllowsSelfAndRefreshesTools(t *testing.T) {
	tg := &stubToolGrantsMgr{removed: 1, removedKind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	out, err := (&revokeAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "builtin:exec"})
	if err != nil {
		t.Fatalf("revoking your own grant should be allowed: %v", err)
	}
	if !tg.called {
		t.Fatal("the call should be forwarded to the manager")
	}
	if m := out.(map[string]any); m["revoked"] != true || m["removed"] != int64(1) {
		t.Errorf("unexpected result: %+v", m)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("changing your own tool surface must mark dirty")
	}
}

// update_agent's unrestricted switch goes through Agents.SetUnrestricted (an attribute of the agent itself).
func TestUpdateAgentSetsUnrestricted(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "unrestricted": true}); err != nil {
		t.Fatal(err)
	}
	if !ag.set || !ag.gotVal {
		t.Error("true should be written through Agents.SetUnrestricted")
	}
}

// Without unrestricted the switch must not be touched (the same "not passed = unchanged" rule as discovery).
func TestUpdateAgentWithoutUnrestrictedIsNoop(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "description": "x"}); err != nil {
		t.Fatal(err)
	}
	if ag.set {
		t.Error("the switch must not be written when unrestricted was not passed")
	}
}

// update_agent may change yourself (the super agent is the administrator): switch changes are allowed and must mark dirty;
// **renaming** is still refused - that is a technical constraint (the later file operations of this round would break), unrelated
// to permissions.
func TestUpdateAgentAllowsSelfButNotRename(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "main", "unrestricted": true}); err != nil {
		t.Fatalf("setting unrestricted on yourself should be allowed: %v", err)
	}
	if !ag.set || !ag.gotVal {
		t.Fatalf("the switch should be written: set=%v val=%v", ag.set, ag.gotVal)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("changing your own tool surface must mark dirty (recomputed next round)")
	}

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "main", "new_name": "main2"}); err == nil {
		t.Fatal("renaming yourself should still be refused (the later file operations of this round would break)")
	}
}

// The execution-layer criterion of the three-layer closure: the two new tools must be marked super (super-agent only).
func TestGrantToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"grant_agent_tool", "revoke_agent_tool"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q must be super-agent only", name)
		}
	}
}
