package tools

import (
	"context"
	"testing"
)

// stubSkillDepMgr 记录参数解析与转发（name / skill_id 两种定位）。
type stubSkillDepMgr struct {
	SkillsManager
	gotRef SkillRef
	gotDep string
	change *SkillDepChange
	called bool
}

func (s *stubSkillDepMgr) AddToolDep(_ context.Context, ref SkillRef, tool string) (*SkillDepChange, error) {
	s.called, s.gotRef, s.gotDep = true, ref, tool
	return s.change, nil
}

func (s *stubSkillDepMgr) RemoveToolDep(_ context.Context, ref SkillRef, tool string) (*SkillDepChange, error) {
	s.called, s.gotRef, s.gotDep = true, ref, tool
	return s.change, nil
}

func TestGrantSkillToolForwardsNameAndRef(t *testing.T) {
	mgr := &stubSkillDepMgr{change: &SkillDepChange{
		Skill: "data-crunching", Ref: "builtin:exec", Added: true, ToolDeps: []string{"builtin:exec"},
	}}
	c := &Context{Skills: mgr}

	out, err := (&grantSkillToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "data-crunching", "ref": "builtin:exec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !mgr.called || mgr.gotRef.Name != "data-crunching" || mgr.gotDep != "builtin:exec" {
		t.Errorf("转发不符：ref=%+v dep=%q", mgr.gotRef, mgr.gotDep)
	}
	if ch, ok := out.(*SkillDepChange); !ok || !ch.Added || len(ch.ToolDeps) != 1 {
		t.Errorf("应原样返回变更与完整依赖：%#v", out)
	}
}

func TestRevokeSkillToolAcceptsSkillID(t *testing.T) {
	mgr := &stubSkillDepMgr{change: &SkillDepChange{
		Skill: "old", SkillID: "sid-1", Ref: "mcp:gone:read", Removed: 1,
	}}
	c := &Context{Skills: mgr}

	if _, err := (&revokeSkillToolTool{}).Execute(context.Background(), c, map[string]any{
		"skill_id": "sid-1", "ref": "mcp:gone:read",
	}); err != nil {
		t.Fatal(err)
	}
	if mgr.gotRef.ID != "sid-1" || mgr.gotRef.Name != "" {
		t.Errorf("按 id 定位应只传 id：%+v", mgr.gotRef)
	}
}

// 既没有合法 name 也没有 skill_id → 拒绝，且不触达 manager（避免拿空名去查库）。
func TestSkillDepRequiresNameOrID(t *testing.T) {
	mgr := &stubSkillDepMgr{}
	c := &Context{Skills: mgr}

	if _, err := (&grantSkillToolTool{}).Execute(context.Background(), c, map[string]any{"ref": "builtin:file"}); err == nil {
		t.Fatal("缺 name / skill_id 应被拒绝")
	}
	if mgr.called {
		t.Error("被拒绝的调用不应触达 manager")
	}
}
