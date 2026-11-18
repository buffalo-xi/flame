package tools

import (
	"context"
	"testing"

	"flame/nexus/shared/models"
)

// stubSkillsMgr 只为满足 Context.Skills != nil（resolveTargetAgent 的防御性检查）；
// 方法一旦被调用即 panic（嵌入接口）。
type stubSkillsMgr struct{ SkillsManager }

// stubToolGrantsMgr 记录调用并返回预设结果。
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

// stubAgentsForSwitch 记录「不限制工具」开关的写入 —— 该开关已从 ToolGrants 迁到 Agents。
type stubAgentsForSwitch struct {
	stubAgents
	set    bool
	gotVal bool
}

func (s *stubAgentsForSwitch) SetUnrestricted(_ context.Context, _ string, v bool) error {
	s.set, s.gotVal = true, v
	return nil
}

// newGrantCtx 造一个带 ToolGrants 桩的 Context（本组用例只测闸门与转发，不需要 Storage）。
func newGrantCtx(agentName string, status int, tg ToolGrantsManager) *Context {
	return newGrantCtxWithAgents(agentName, stubAgents{status: status}, tg)
}

// newGrantCtxWithAgents 同上，但可注入自定义的 Agents 桩（开关类参数走 Agents）。
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

// D5（修订）：main 可以给自己加工具 —— 它即管理员，边界由 status 承担。
// 同时必须置脏：否则刚授的权本 run 看不见，会被误判成失败而反复重试。
func TestGrantAgentToolAllowsSelfAndRefreshesTools(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	out, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "custom:interest-final-amount"})
	if err != nil {
		t.Fatalf("给自己加工具应被允许: %v", err)
	}
	if !tg.called || tg.gotRef != "custom:interest-final-amount" {
		t.Fatalf("应把调用转给 manager（ref 原样）: called=%v ref=%q", tg.called, tg.gotRef)
	}
	if m := out.(map[string]any); m["granted"] != true || m["grant_kind"] != models.GrantKindRequired {
		t.Errorf("返回不符：%+v", m)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("改自己的工具面必须置脏（下一轮重算工具集）")
	}
}

// 但废弃态仍然拦住自己：防护由 status 承担，而改 status 只有管理端能做。
func TestGrantAgentToolRejectsSelfWhenDeprecated(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 3, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "builtin:exec"}); err == nil {
		t.Fatal("废弃态应拒绝（含自己）")
	}
	if tg.called {
		t.Error("被拒绝的调用不应触达 manager")
	}
}

// 目标必须处于正常态（status == 1），废弃一律拒绝。
func TestGrantAgentToolRejectsDeprecatedTarget(t *testing.T) {
	tg := &stubToolGrantsMgr{}
	c := newGrantCtx("main", 3, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec"}); err == nil {
		t.Fatal("目标废弃时应拒绝")
	}
	if tg.called {
		t.Error("被拒绝的调用不应触达 manager")
	}
}

// 正常路径：目标名与 ref 原样转发，写入方式（allow / required）由 manager 决定并回报。
func TestGrantAgentToolForwardsAndReportsKind(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindAllow, reason: "被 skill 声明"}
	c := newGrantCtx("main", 1, tg)

	out, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec", "kind": "deny"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("返回类型 = %T", out)
	}
	if m["grant_kind"] != models.GrantKindAllow || m["granted"] != true {
		t.Errorf("返回不符：%+v", m)
	}
	if tg.gotName != "poet" || tg.gotRef != "builtin:exec" || tg.gotKind != "deny" {
		t.Errorf("转发不符：name=%q ref=%q kind=%q", tg.gotName, tg.gotRef, tg.gotKind)
	}
}

// 省略 kind 时原样传空串（由 manager 自动判定），不能被工具层偷偷填值。
func TestGrantAgentToolOmittedKindStaysEmpty(t *testing.T) {
	tg := &stubToolGrantsMgr{kind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	if _, err := (&grantAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "ref": "builtin:exec"}); err != nil {
		t.Fatal(err)
	}
	if tg.gotKind != "" {
		t.Errorf("省略 kind 应传空串，实际 %q", tg.gotKind)
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
		t.Errorf("返回不符：%+v", m)
	}
	if m["grant_kind"] != models.GrantKindAllow {
		t.Errorf("应回显被删行的 kind 便于审计：%+v", m)
	}
}

// 收回自己的授权同样放开，且同样要置脏（否则工具「看不见消失」，要等下次会话）。
func TestRevokeAgentToolAllowsSelfAndRefreshesTools(t *testing.T) {
	tg := &stubToolGrantsMgr{removed: 1, removedKind: models.GrantKindRequired}
	c := newGrantCtx("main", 1, tg)

	out, err := (&revokeAgentToolTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "main", "ref": "builtin:exec"})
	if err != nil {
		t.Fatalf("收回自己的授权应被允许: %v", err)
	}
	if !tg.called {
		t.Fatal("应把调用转给 manager")
	}
	if m := out.(map[string]any); m["revoked"] != true || m["removed"] != int64(1) {
		t.Errorf("返回不符：%+v", m)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("改自己的工具面必须置脏")
	}
}

// update_agent 的 unrestricted 开关：走 Agents.SetUnrestricted（agent 自身属性）。
func TestUpdateAgentSetsUnrestricted(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "unrestricted": true}); err != nil {
		t.Fatal(err)
	}
	if !ag.set || !ag.gotVal {
		t.Error("应通过 Agents.SetUnrestricted 写入 true")
	}
}

// 不传 unrestricted 就不该碰开关（与 discovery 的「不传则不改」一致）。
func TestUpdateAgentWithoutUnrestrictedIsNoop(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "description": "x"}); err != nil {
		t.Fatal(err)
	}
	if ag.set {
		t.Error("未传 unrestricted 时不应写开关")
	}
}

// update_agent 可以改自己（超级智能体即管理员）：开关类变更放开，且要置脏；
// 但**改名**仍拒 —— 那是技术约束（本轮后续文件操作会失效），与权限无关。
func TestUpdateAgentAllowsSelfButNotRename(t *testing.T) {
	ag := &stubAgentsForSwitch{}
	c := newGrantCtxWithAgents("main", ag, &stubToolGrantsMgr{})

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "main", "unrestricted": true}); err != nil {
		t.Fatalf("给自己设 unrestricted 应被允许: %v", err)
	}
	if !ag.set || !ag.gotVal {
		t.Fatalf("应写入开关: set=%v val=%v", ag.set, ag.gotVal)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("改自己的工具面必须置脏（下一轮重算）")
	}

	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "main", "new_name": "main2"}); err == nil {
		t.Fatal("给自己改名仍应被拒绝（本轮后续文件操作会失效）")
	}
}

// 三层收口的执行层判据：两个新工具必须标 super（仅超级智能体）。
func TestGrantToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"grant_agent_tool", "revoke_agent_tool"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q 必须仅超级智能体可用", name)
		}
	}
}
