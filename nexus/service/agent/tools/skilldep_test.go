package tools

import (
	"context"
	"testing"
)

// stubSkillDepMgr records argument parsing and forwarding (both name and skill_id targeting).
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
		t.Errorf("forwarded values differ: ref=%+v dep=%q", mgr.gotRef, mgr.gotDep)
	}
	if ch, ok := out.(*SkillDepChange); !ok || !ch.Added || len(ch.ToolDeps) != 1 {
		t.Errorf("the change and the full dependency list should be returned as is: %#v", out)
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
		t.Errorf("targeting by id should pass the id only: %+v", mgr.gotRef)
	}
}

// Neither a valid name nor a skill_id -> refused, and the manager is never reached (so no empty name is used for a lookup).
func TestSkillDepRequiresNameOrID(t *testing.T) {
	mgr := &stubSkillDepMgr{}
	c := &Context{Skills: mgr}

	if _, err := (&grantSkillToolTool{}).Execute(context.Background(), c, map[string]any{"ref": "builtin:file"}); err == nil {
		t.Fatal("missing name / skill_id should be refused")
	}
	if mgr.called {
		t.Error("a refused call must not reach the manager")
	}
}
