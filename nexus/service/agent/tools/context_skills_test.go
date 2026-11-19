package tools

import (
	"context"
	"testing"
)

// The "inject once used" bookkeeping: use_skill registers the skill after success, and the agent loop recomputes the tool set on
// the next round from that. Three invariants must hold, or else you either query the database for nothing or the tools never
// appear:
//  1. only a new skill marks dirty (reading the same skill again is not a change);
//  2. the dirty flag can be consumed only once (otherwise every round recomputes);
//  3. the snapshot is a copy (a caller mutating it must not pollute internal state).
func TestSkillLoadedBookkeeping(t *testing.T) {
	c := &Context{}

	if got := c.LoadedSkills(); len(got) != 0 {
		t.Fatalf("the initially loaded skills should be empty, got %v", got)
	}
	if c.ConsumeSkillsDirty() {
		t.Fatal("it should not be dirty initially")
	}

	// An empty id is an invalid registration (e.g. the detail came back without an id) and must not pollute the state
	c.MarkSkillLoaded("", "ghost", nil)
	if len(c.LoadedSkills()) != 0 || c.ConsumeSkillsDirty() {
		t.Fatal("an empty id should register nothing and mark nothing dirty")
	}

	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:exec"})
	got := c.LoadedSkills()
	if got["id-1"] != "custom-tool-authoring" {
		t.Fatalf("registration failed, got %v", got)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("a new registration must mark dirty (otherwise the agent loop will not recompute the tool set)")
	}
	if c.ConsumeSkillsDirty() {
		t.Fatal("the dirty flag can only be consumed once (otherwise every round queries the database)")
	}

	// The dependency declaration travels with the registration, and the snapshot is a copy (a caller mutating it must not pollute
	// internal state)
	entries := c.LoadedSkillEntries()
	if len(entries) != 1 || entries[0].ID != "id-1" || len(entries[0].Deps) != 1 || entries[0].Deps[0] != "builtin:exec" {
		t.Fatalf("the dependency snapshot was not registered, got %#v", entries)
	}
	entries[0].Deps[0] = "builtin:write_file"
	if c.LoadedSkillEntries()[0].Deps[0] != "builtin:exec" {
		t.Fatal("LoadedSkillEntries' dependencies must be a copy")
	}

	// Reading the same skill again (same name and dependencies): not a change
	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:exec"})
	if c.ConsumeSkillsDirty() {
		t.Fatal("re-reading the same skill should not mark dirty")
	}

	// The same skill reactivated with different dependencies: must recompute, otherwise the tool set stays on the old snapshot
	c.MarkSkillLoaded("id-1", "custom-tool-authoring", []string{"builtin:write_file"})
	if !c.ConsumeSkillsDirty() {
		t.Fatal("a dependency change must mark dirty")
	}

	// nil and an empty slice are not the same thing: nil = no snapshot (fall back to the database during resolution),
	// empty slice = the authoritative "this version really has no dependencies" (never query the database again, otherwise a
	// dependency added in the draft leaks into production)
	c2 := &Context{}
	c2.MarkSkillLoaded("id-2", "no-deps", []string{})
	if e := c2.LoadedSkillEntries(); len(e) != 1 || e[0].Deps == nil {
		t.Fatalf("an empty-slice snapshot must stay non-nil, got %#v", e)
	}

	// The snapshot is a copy
	snap := c.LoadedSkills()
	snap["id-1"] = "tampered"
	snap["id-2"] = "injected"
	if c.LoadedSkills()["id-1"] != "custom-tool-authoring" || len(c.LoadedSkills()) != 1 {
		t.Fatal("LoadedSkills must return a copy; a caller must not be able to change internal state")
	}
}

// There is only one activation entry point: use_skill. read_skill is pure viewing and must not drag the tools along -
// otherwise "I just wanted to read the document" also pulls in large schemas such as exec / browser (consuming budget, hitting the
// 50 limit, widening the surface of wrong picks).
func TestReadSkillDoesNotActivate(t *testing.T) {
	const (
		skillID   = "sk1"
		skillName = "custom-tool-authoring"
		body      = "# skill"
	)
	c := &Context{Skills: stubSkillDetail{d: &SkillDetail{
		SkillInfo: SkillInfo{ID: skillID, Name: skillName},
		Content:   body,
		ToolDeps:  []string{"builtin:exec"},
	}}}
	args := map[string]any{"name": skillName}

	got, err := (&readSkillTool{}).Execute(context.Background(), c, args)
	if err != nil {
		t.Fatalf("read_skill = %v", err)
	}
	// The dependency list is still reported (what the skill needs and what you get right now are two different things)
	if d, ok := got.(*SkillDetail); !ok || d.Content != body || len(d.ToolDeps) != 1 || d.ToolDeps[0] != "builtin:exec" {
		t.Fatalf("read_skill should return the body plus the dependency list, got %#v", got)
	}
	if c.ConsumeSkillsDirty() || len(c.LoadedSkills()) != 0 {
		t.Fatal("read_skill is pure viewing and must not activate the skill")
	}

	got, err = (&useSkillTool{}).Execute(context.Background(), c, args)
	if err != nil {
		t.Fatalf("use_skill = %v", err)
	}
	if d, ok := got.(*SkillDetail); !ok || d.Content != body || len(d.ToolDeps) != 1 {
		t.Fatalf("use_skill must return the body plus the dependency list (otherwise the model has to read it again), got %#v", got)
	}
	if !c.ConsumeSkillsDirty() {
		t.Fatal("use_skill must mark dirty (otherwise the next round will not recompute the tool set)")
	}
	if c.LoadedSkills()[skillID] != skillName {
		t.Fatalf("use_skill should register the skill, got %v", c.LoadedSkills())
	}
	// The dependency declaration is registered as well: tool resolution only trusts this snapshot (in a production run = the
	// current version)
	entries := c.LoadedSkillEntries()
	if len(entries) != 1 || len(entries[0].Deps) != 1 || entries[0].Deps[0] != "builtin:exec" {
		t.Fatalf("use_skill should register the dependency snapshot, got %#v", entries)
	}
}

// stubSkillDetail implements Get only: targeting is the skillManager's job, and all that matters here is the difference between
// "read" and "activate".
type stubSkillDetail struct {
	SkillsManager
	d *SkillDetail
}

func (s stubSkillDetail) Get(context.Context, SkillRef) (*SkillDetail, error) { return s.d, nil }
