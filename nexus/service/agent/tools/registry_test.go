package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/models"
)

// stubSchemaTool is a stub tool with an arbitrary schema, used to verify argument validation / alias normalisation.
type stubSchemaTool struct {
	meta ToolMeta
}

func (s *stubSchemaTool) Meta() ToolMeta { return s.meta }

func (s *stubSchemaTool) Execute(_ context.Context, _ *Context, _ map[string]any) (any, error) {
	return "ok", nil
}

func TestNormalizeArgs(t *testing.T) {
	meta := ToolMeta{
		Name: "read_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string"},
				"skill_id": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
	}
	args := map[string]any{"skill_name": "english-joke-writing"}
	normalizeArgs(meta, args)

	if args["name"] != "english-joke-writing" {
		t.Fatalf("name = %v, want english-joke-writing", args["name"])
	}
	if _, still := args["skill_name"]; still {
		t.Errorf("the alias key skill_name should be removed, got %#v", args)
	}
	if err := validateRequired(meta, args); err != nil {
		t.Errorf("validateRequired = %v, want nil", err)
	}
}

func TestNormalizeArgsDoesNotClobberCanonical(t *testing.T) {
	meta := ToolMeta{
		Name: "bind_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string"},
				"skill": map[string]any{"type": "string"},
			},
			"required": []any{"agent", "skill"},
		},
	}
	// agent / skill are bind_skill's canonical names and must never be rewritten by the alias rules.
	args := map[string]any{"agent": "poet", "skill": "english"}
	normalizeArgs(meta, args)

	if args["agent"] != "poet" || args["skill"] != "english" {
		t.Fatalf("canonical args mutated: %#v", args)
	}
}

func TestValidateRequiredMessage(t *testing.T) {
	meta := ToolMeta{
		Name: "read_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"owner": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
	}
	err := validateRequired(meta, map[string]any{"skill_id": "x"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	want := `tool read_skill: missing required argument(s) ["name"]; received ["skill_id"]; expected params: ["name", "owner"]`
	if err.Error() != want {
		t.Errorf("msg = %q, want %q", err.Error(), want)
	}
}

func TestExecuteToolSkillNameAlias(t *testing.T) {
	Register(&stubSchemaTool{meta: ToolMeta{
		Name: "stub_read_skill",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}})

	if _, err := ExecuteTool(context.Background(), &Context{}, "stub_read_skill", map[string]any{"skill_name": "x"}); err != nil {
		t.Fatalf("ExecuteTool = %v, want nil", err)
	}
}

// An ungranted tool must be refused by the execution layer (so a textual call cannot bypass tool grants).
func TestExecuteToolRejectsUngrantedTool(t *testing.T) {
	Register(&stubSchemaTool{meta: ToolMeta{
		Name: "stub_ungranted",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}})

	c := &Context{AgentName: "joker", AllowedTools: map[string]bool{"other_tool": true}}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err == nil {
		t.Fatal("want error for ungranted tool")
	}

	// The empty set (no grant configured): every tool is refused
	c.AllowedTools = map[string]bool{}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err == nil {
		t.Fatal("want error when grant set is empty")
	}

	// In the set: allowed
	c.AllowedTools = map[string]bool{"stub_ungranted": true}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err != nil {
		t.Fatalf("ExecuteTool = %v, want nil", err)
	}
}

// The default tool set must consist of registered ordinary built-in tools: no misspelled names, and no super category smuggled into
// the defaults.
func TestDefaultAgentToolsAreRegistered(t *testing.T) {
	if len(models.DefaultAgentTools) == 0 {
		t.Fatal("DefaultAgentTools is empty")
	}
	seen := map[string]bool{}
	for _, name := range models.DefaultAgentTools {
		if seen[name] {
			t.Errorf("default tool %q is duplicated", name)
		}
		seen[name] = true
		if _, ok := Get(name); !ok {
			t.Errorf("default tool %q is not registered", name)
		}
		if IsSupervisorOnlyTool(name) {
			t.Errorf("default tool %q is a super (super-agent only) tool and must not be in the default set", name)
		}
	}
}

// Skill meta tool categories: the three private tools are ordinary tools (usable once granted, non-super agents included), the three
// shared tools and the three explicit-owner tools are skill-admin (super agent only), and the binding tools are agent-admin.
// The old single-tool names must be gone completely.
// The category is the only criterion for "who may use which tool", so getting it wrong gets the permission boundary wrong - hence
// it is locked down explicitly here.
func TestSkillToolCategories(t *testing.T) {
	for _, name := range []string{"create_own_skill", "update_own_skill", "delete_own_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if IsSupervisorOnlyTool(name) {
			t.Errorf("%q must not be super-agent only (otherwise an agent cannot build its own private skills)", name)
		}
		// Split from builtin:skill: skill is "view skills", skill-own is "write your own capability surface", and the risk tiers
		// differ (D20 deliberately keeps it out of DefaultAgentTools) - in one group they could not be granted separately.
		if got := tool.Meta().Category; got != CategorySkillOwn {
			t.Errorf("%q category = %q, want %q", name, got, CategorySkillOwn)
		}
	}
	// The three "view / use a skill" tools must stay in the skill group - they are resident with core and must not be mixed into
	// skill-own.
	// use_skill and read_skill share a group: the former activates, the latter only views, and neither produces write permission.
	for _, name := range []string{"list_skills", "read_skill", "use_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if got := tool.Meta().Category; got != CategorySkill {
			t.Errorf("%q category = %q, want %q", name, got, CategorySkill)
		}
	}
	for _, name := range []string{"create_shared_skill", "update_shared_skill", "delete_shared_skill"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q must be super-agent only (only the super agent may create, change and delete shared skills)", name)
		}
	}
	// The three explicit-owner tools (managing another agent's private skills): category skill-admin, super agent only.
	// Symmetric to the own trio - those are "your own private", these are "a specified agent's private".
	for _, name := range []string{"create_agent_skill", "update_agent_skill", "delete_agent_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q must be super-agent only (writing another agent's private skill is rewriting its prompt)", name)
		}
		if got := tool.Meta().Category; got != CategorySkillAdmin {
			t.Errorf("%q category = %q, want %q", name, got, CategorySkillAdmin)
		}
	}
	// The binding tools (deciding "which agents may use it") remain super-agent only
	for _, name := range []string{"bind_skill", "unbind_skill"} {
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q must be super-agent only (bindings are decided by the super agent)", name)
		}
	}
	// The old single-tool shape must be gone completely (old refs are migrated by sql/skill_own_tool_rename.sql)
	for _, name := range []string{"create_skill", "update_skill", "delete_skill"} {
		if _, ok := Get(name); ok {
			t.Errorf("the old tool name %q is still registered and should have been renamed (private gets an _own_ suffix, shared is *_shared_skill)", name)
		}
	}
}

// A default grant entry must be a shape that required semantics can carry (item_type=tool / ref=builtin:<name>).
func TestDefaultAgentToolGrantsShape(t *testing.T) {
	for _, it := range models.DefaultAgentToolGrants() {
		if it.ItemType != models.GrantItemTool {
			t.Errorf("item_type = %q, want %q", it.ItemType, models.GrantItemTool)
		}
		if !strings.HasPrefix(it.Ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("ref = %q is missing the prefix %q", it.Ref, models.GrantRefBuiltinPrefix)
		}
	}
}

// The "super-agent only" criterion must be closed in both directions: a tool is supervisor-only <=> its category is in
// supervisorOnlyCategoryList, and every category in that list really has tools (an empty category is a group that does not exist,
// which silently vanishes from the panel).
// Missing one category means privilege escalation; an extra empty category means a missing group in the grant panel - both ends
// must be blocked.
func TestSupervisorOnlyCategoriesAreClosed(t *testing.T) {
	byCategory := map[string][]string{}
	for _, tool := range List() {
		m := tool.Meta()
		byCategory[m.Category] = append(byCategory[m.Category], m.Name)
		if got, want := IsSupervisorOnlyTool(m.Name), IsSupervisorOnlyCategory(m.Category); got != want {
			t.Errorf("%q has IsSupervisorOnlyTool=%v but its category %q has IsSupervisorOnlyCategory=%v (the two must share one source)",
				m.Name, got, m.Category, want)
		}
	}
	for _, category := range SupervisorOnlyCategories() {
		if len(byCategory[category]) == 0 {
			t.Errorf("supervisor-only category %q has no tools (an empty group never shows up in the grant panel)", category)
		}
	}
	// The reverse: no tool outside a supervisor-only category should be judged supervisor-only.
	if IsSupervisorOnlyCategory(CategorySuper) {
		t.Error("CategorySuper is a transitional alias and must not be treated as a supervisor-only category")
	}
	if IsSupervisorOnlyCategory(CategorySkill) {
		t.Error("CategorySkill (viewing skills) is open to ordinary agents and must not be supervisor-only")
	}
}

// The super agent's core set must be six "group" refs, all of them known built-in categories.
// A misspelled group name does not raise an error, it just leaves the super agent short of a whole class of tools (a silent
// downgrade), so it is blocked here.
func TestSupervisorCoreGrantRefsAreGroups(t *testing.T) {
	if len(models.SupervisorCoreGrantRefs) == 0 {
		t.Fatal("SupervisorCoreGrantRefs is empty")
	}
	cats := map[string]bool{}
	for _, tool := range List() {
		cats[tool.Meta().Category] = true
	}
	seen := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		if seen[ref] {
			t.Errorf("core group %q is duplicated", ref)
		}
		seen[ref] = true
		if !strings.HasPrefix(ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("core group %q is missing the prefix %q", ref, models.GrantRefBuiltinPrefix)
			continue
		}
		category := strings.TrimPrefix(ref, models.GrantRefBuiltinPrefix)
		if !cats[category] {
			t.Errorf("core group %q is not a known built-in category", ref)
		}
		// Every core group must have tools, otherwise an empty group is resident.
		found := false
		for _, tool := range List() {
			if tool.Meta().Category == category {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("core group %q has no registered tools", ref)
		}
	}
}

// The core trade-off must be pinned, otherwise the conclusion "browser / the four on-demand groups must not be resident" gets
// silently overturned.
// These are the entire source of the "save the super agent's context" gain (37 -> 20 tools); think it through before changing them.
func TestSupervisorCoreExcludesOnDemandGroups(t *testing.T) {
	core := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		core[ref] = true
	}
	// browser: self-described as low frequency + the largest schema + the only "network egress + read any page + carry a login
	// session" combination.
	// It follows no skill; if it is needed, an administrator adds a required row (see the notes on models.SupervisorCoreGrantRefs).
	if core["builtin:browser"] {
		t.Error("builtin:browser must not be resident in core")
	}
	// Of the five management groups only agent-admin is resident (the super agent's bootstrap: being able to create agents is what
	// lets it raise other working surfaces).
	// The other four follow skills: they appear only after use_skill activates a skill that declares them (a binding itself produces
	// no tools).
	if !core["builtin:agent-admin"] {
		t.Error("builtin:agent-admin must be resident (the super agent's bootstrap capability)")
	}
	for _, ref := range []string{"builtin:skill-own", "builtin:skill-admin",
		"builtin:tool-grant", "builtin:custom-tool", "builtin:flow-admin"} {
		if core[ref] {
			t.Errorf("%q must not be resident in core (it follows skills and is activated on demand)", ref)
		}
	}
}

// The shape of SupervisorCoreGrants: item_type=group + a builtin: prefix (unlike DefaultAgentToolGrants' single tools).
func TestSupervisorCoreGrantsShape(t *testing.T) {
	for _, it := range models.SupervisorCoreGrants() {
		if it.ItemType != models.GrantItemGroup {
			t.Errorf("item_type = %q, want %q", it.ItemType, models.GrantItemGroup)
		}
		if !strings.HasPrefix(it.Ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("ref = %q is missing the prefix %q", it.Ref, models.GrantRefBuiltinPrefix)
		}
	}
}

// The refs array in sql/main_core_tool_grants.sql must match models.SupervisorCoreGrantRefs item by item.
// That SQL is **hard-coded**, and a drift between the two shows up as "new tenants get 6 groups, existing tenants 5" - a difference
// visible only on old environments.
func TestSupervisorCoreGrantRefsMatchSQL(t *testing.T) {
	path := filepath.Join("..", "..", "..", "sql", "main_core_tool_grants.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	src := string(raw)
	start := strings.Index(src, "refs text[] := ARRAY[")
	if start < 0 {
		t.Fatal("the refs array declaration was not found in the SQL")
	}
	body := src[start:]
	if end := strings.Index(body, "];"); end >= 0 {
		body = body[:end]
	}
	// The array declaration holds only string literals and comments, so splitting on single quotes and taking the odd segments is
	// enough.
	parts := strings.Split(body, "'")
	inSQL := map[string]bool{}
	for i := 1; i < len(parts); i += 2 {
		inSQL[parts[i]] = true
	}
	if len(inSQL) == 0 {
		t.Fatal("the refs array in the SQL parsed to nothing (has the format changed?)")
	}

	inGo := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		inGo[ref] = true
		if !inSQL[ref] {
			t.Errorf("%q from SupervisorCoreGrantRefs is not in the SQL refs array", ref)
		}
	}
	for ref := range inSQL {
		if !inGo[ref] {
			t.Errorf("%q from the SQL is not in SupervisorCoreGrantRefs", ref)
		}
	}
}
