package tools

import (
	"context"
	"testing"
)

// stubRelationsMgr 记录调用并返回预设结果。
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

// stubAgentTable 按名字返回 agent（调用授权用例需要区分 caller / target）；
// 其余方法由嵌入接口兜底（被调用即 panic）。
type stubAgentTable struct {
	AgentManager
	byName map[string]*AgentInfo
}

func (s stubAgentTable) Get(_ context.Context, name string) (*AgentInfo, error) {
	return s.byName[name], nil
}

// stubSkillsCapture 捕获 Update 的入参（验证 restricted 透传）。
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
		// 身份判据是属性不是名字：测试里用 "main" 表示超权 agent 时须显式置位
		IsSupervisor: agentName == "main",
		UserID:       "u1",
		WorkKey:      "tenants/t0/users/u1/sessions/s1",
		Agents:       stubAgentTable{byName: byName},
		Skills:       &stubSkillsMgr{},
		Relations:    rel,
	}
}

// 超级智能体可以给自己加出边（允许超级智能体调用子 agent —— 这是刻意的设计，见 relations.go）。
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
		t.Errorf("返回不符：%+v", m)
	}
	if rel.gotCaller != "main-id" || rel.gotCallee != "poet-id" {
		t.Errorf("转发不符：caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// 被调用方可以是超级智能体：它被调起时仍是完整的管理面（恒读草稿），
// 能不能调完全由这条授权行决定 —— 写时不再按身份排除。
func TestAllowAgentCallAcceptsSupervisorTarget(t *testing.T) {
	rel := &stubRelationsMgr{}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"main": {ID: "main-id", Name: "main", Status: 1, IsSupervisor: true},
	})

	out, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "main"})
	if err != nil {
		t.Fatalf("把超级智能体作为被调用方不应被拒绝：%v", err)
	}
	if m := out.(map[string]any); m["allowed"] != true || m["target"] != "main" {
		t.Errorf("返回不符：%+v", m)
	}
	if rel.gotCaller != "poet-id" || rel.gotCallee != "main-id" {
		t.Errorf("转发不符：caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// 自环无意义。
func TestAllowAgentCallRejectsSelf(t *testing.T) {
	rel := &stubRelationsMgr{}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
	})

	if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "poet"}); err == nil {
		t.Fatal("自环应被拒绝")
	}
}

// 目标不存在 / 已废弃都拒绝（废弃与 caller.go 的 IsRunnable 判定对齐）。
func TestAllowAgentCallRejectsBadTarget(t *testing.T) {
	c := newCallCtx("main", &stubRelationsMgr{}, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"dead": {ID: "dead-id", Name: "dead", Status: 3},
	})
	for _, target := range []string{"ghost", "dead"} {
		if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
			map[string]any{"agent": "poet", "target": target}); err == nil {
			t.Errorf("target=%q 应被拒绝", target)
		}
	}
}

// 调用方须处于正常态（复用 resolveTargetAgent 的状态闸）。
func TestAllowAgentCallRejectsDeprecatedCaller(t *testing.T) {
	c := newCallCtx("main", &stubRelationsMgr{}, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 3},
		"bard": {ID: "bard-id", Name: "bard", Status: 1},
	})

	if _, err := (&allowAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "bard"}); err == nil {
		t.Fatal("调用方废弃时应拒绝")
	}
}

// 收回不套写时守卫：陈数据（比如曾被误授的 main）必须还能清理。
func TestRevokeAgentCallIsLenient(t *testing.T) {
	rel := &stubRelationsMgr{removed: 1}
	c := newCallCtx("main", rel, map[string]*AgentInfo{
		"poet": {ID: "poet-id", Name: "poet", Status: 1},
		"main": {ID: "main-id", Name: "main", Status: 1, IsSupervisor: true},
	})

	out, err := (&revokeAgentCallTool{}).Execute(context.Background(), c,
		map[string]any{"agent": "poet", "target": "main"})
	if err != nil {
		t.Fatalf("收回悬空 / 陈授权不应被写时守卫挡住: %v", err)
	}
	if m := out.(map[string]any); m["removed"] != int64(1) || m["revoked"] != true {
		t.Errorf("返回不符：%+v", m)
	}
	if rel.gotCaller != "poet-id" || rel.gotCallee != "main-id" {
		t.Errorf("转发不符：caller=%q callee=%q", rel.gotCaller, rel.gotCallee)
	}
}

// 两个新工具同样必须仅超级智能体可用（三层收口的执行层判据）。
func TestAgentCallToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"allow_agent_call", "revoke_agent_call"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q 必须仅超级智能体可用", name)
		}
	}
}

// update_shared_skill 接受 restricted（透传到 SkillInput）。
func TestUpdateSharedSkillPassesRestricted(t *testing.T) {
	sk := &stubSkillsCapture{}
	c := &Context{Schema: "t0", AgentName: "main", AgentID: "self", UserID: "u1", Skills: sk}

	if _, err := (&updateSharedSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "report-format", "restricted": false}); err != nil {
		t.Fatal(err)
	}
	if sk.got.Restricted == nil {
		t.Fatal("restricted 未透传（应为非 nil）")
	}
	if *sk.got.Restricted {
		t.Error("restricted 应为 false")
	}
	// 不传则不改（nil）
	sk.got = SkillInput{}
	if _, err := (&updateSharedSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "report-format"}); err != nil {
		t.Fatal(err)
	}
	if sk.got.Restricted != nil {
		t.Error("未传 restricted 时应为 nil（不改动）")
	}
}

// 私有 skill 不接受 restricted：可见性由归属 agent 决定，静默忽略会让模型以为改了。
func TestUpdateOwnSkillRejectsRestricted(t *testing.T) {
	sk := &stubSkillsCapture{}
	c := &Context{Schema: "t0", AgentName: "joker", AgentID: "self", UserID: "u1", Skills: sk}

	if _, err := (&updateOwnSkillTool{}).Execute(context.Background(), c,
		map[string]any{"name": "my-skill", "restricted": true}); err == nil {
		t.Fatal("own 系列应拒绝 restricted 参数")
	}
}
