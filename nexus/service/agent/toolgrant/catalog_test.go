package toolgrant

import (
	"context"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/pluginpolicy"
	"flame/nexus/shared/repository/mcpconnections"
	"flame/nexus/shared/repository/rpcconnections"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/repository/toolgrants"
	"flame/tools/xsql"
)

// preset:default must match models.DefaultAgentTools exactly - one source of truth, guarded against drift.
func TestDefaultPresetMatchesModels(t *testing.T) {
	want := models.DefaultAgentToolGrants()

	var got []models.ToolGroupItem
	for _, pg := range PresetGroups {
		if pg.Name == "default" {
			got = pg.Items
		}
	}
	if got == nil {
		t.Fatal("preset:default does not exist")
	}
	if len(got) != len(want) {
		t.Fatalf("preset:default has %d members, DefaultAgentTools has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: %+v != %+v", i, got[i], want[i])
		}
	}
}

// The shared layer's ref-prefix constant must match the service layer's scope constant (layering forbids a reverse dependency, so a test
// guards it).
func TestBuiltinPrefixConstantMatches(t *testing.T) {
	if models.GrantRefBuiltinPrefix != ScopeBuiltin+":" {
		t.Fatalf("GrantRefBuiltinPrefix=%q does not match ScopeBuiltin=%q",
			models.GrantRefBuiltinPrefix, ScopeBuiltin)
	}
}

// builtin:other is gone: it is no longer a category group, and referencing it expands to nothing.
func TestBuiltinOtherRemoved(t *testing.T) {
	if BuiltinCategories()["other"] {
		t.Fatal("builtin:other should no longer be a built-in category")
	}
	list, err := NewCatalog("t0", nil, true).ToolsOfRef("builtin:other")
	if err != nil {
		t.Fatalf("ToolsOfRef = %v, want nil", err)
	}
	if len(list) != 0 {
		t.Fatalf("ToolsOfRef(builtin:other) = %d tools, want 0", len(list))
	}
}

// builtin:super is the aggregate category from before the split; no tool belongs to it now, and it survives only as a **compatibility
// expansion entry**: it has to equal the union of the five management groups. Break it and existing grant rows (builtin:super) silently
// resolve to nothing - the super agent loses all management capability with no error at all.
func TestBuiltinSuperExpandsToAdminGroups(t *testing.T) {
	cat := NewCatalog("t0", nil, true)

	super, err := cat.ToolsOfRef("builtin:super")
	if err != nil {
		t.Fatalf("ToolsOfRef(builtin:super) = %v", err)
	}

	want := map[string]bool{}
	for _, category := range tools.SupervisorOnlyCategories() {
		list, err := cat.ToolsOfRef(ScopeBuiltin + ":" + category)
		if err != nil {
			t.Fatalf("ToolsOfRef(%s: %s) = %v", ScopeBuiltin, category, err)
		}
		if len(list) == 0 {
			t.Errorf("management group %q contains no tools", category)
		}
		for _, ti := range list {
			want[ti.Ref] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("the union of the five management groups is empty")
	}

	got := map[string]bool{}
	for _, ti := range super {
		got[ti.Ref] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Errorf("builtin:super expansion is missing %q", ref)
		}
	}
	for ref := range got {
		if !want[ref] {
			t.Errorf("builtin:super expansion has an extra %q", ref)
		}
	}
}

// builtin:super must not appear in Groups(): it has no members of its own and duplicates the five management groups, so listing it in
// builtinGroupCategories would add an empty card under the same name.
func TestSuperIsCompatibleCategoryNotGroup(t *testing.T) {
	if !BuiltinCategories()[tools.CategorySuper] {
		t.Error("builtin:super must remain a parseable built-in category (otherwise existing grant rows fail to resolve)")
	}
	for _, category := range builtinGroupCategories {
		if category == tools.CategorySuper {
			t.Error("builtin:super must not appear in builtinGroupCategories (it would add a duplicate card)")
		}
	}
	// Every group that appears in the catalog must really have members, or it is a card that stays empty forever.
	cat := NewCatalog("t0", nil, true)
	for _, category := range builtinGroupCategories {
		list, err := cat.ToolsOfRef(ScopeBuiltin + ":" + category)
		if err != nil {
			t.Fatalf("ToolsOfRef(builtin:%s) = %v", category, err)
		}
		if len(list) == 0 {
			t.Errorf("the catalog's group builtin:%s has no members", category)
		}
	}
}

// stubCategorylessTool is a built-in tool with no category annotation (test only).
type stubCategorylessTool struct{}

func (s *stubCategorylessTool) Meta() tools.ToolMeta {
	return tools.ToolMeta{Name: "stub_categoryless", Description: "test only: no category annotation"}
}

func (s *stubCategorylessTool) Execute(_ context.Context, _ *tools.Context, _ map[string]any) (any, error) {
	return nil, nil
}

// An uncategorised tool joins no category group: Source stays empty (it no longer falls back to builtin:other), so no builtin:<category>
// group matches it (builtinByCategory compares by Source).
func TestUncategorizedToolHasNoGroup(t *testing.T) {
	tools.Register(&stubCategorylessTool{})

	var found bool
	for _, it := range BuiltinTools() {
		if it.Name != "stub_categoryless" {
			continue
		}
		found = true
		if it.Source != "" {
			t.Errorf("Source = %q, want empty", it.Source)
		}
		for category := range BuiltinCategories() {
			if it.Source == ScopeBuiltin+":"+category {
				t.Errorf("an uncategorised tool was filed under category %q", category)
			}
		}
	}
	if !found {
		t.Fatal("stub_categoryless does not appear in BuiltinTools")
	}
}

// Preset group names must be unique (preset:<name> is the unique key in Groups()).
func TestPresetNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, pg := range PresetGroups {
		if seen[pg.Name] {
			t.Errorf("duplicate preset group name: %q", pg.Name)
		}
		seen[pg.Name] = true
	}
}

// fakeEndpointRepo is a fake endpoint repository: only List is implemented, the rest return zero values.
// List records the request it received and filters "faithfully" by protocol / target type (the real implementation filters in SQL).
type fakeEndpointRepo struct {
	gotReq *svcendpoints.ListSvcEndpointsRequest
	list   []models.SvcEndpoint
}

func (f *fakeEndpointRepo) GetByID(*auth.AuthInfo, string) (*models.SvcEndpoint, error) {
	return nil, nil
}

func (f *fakeEndpointRepo) FindByName(*auth.AuthInfo, string, string) (*models.SvcEndpoint, error) {
	return nil, nil
}

func (f *fakeEndpointRepo) Create(*auth.AuthInfo, *models.SvcEndpoint) (string, error) {
	return "", nil
}

func (f *fakeEndpointRepo) Update(*auth.AuthInfo, string, *models.SvcEndpoint) error { return nil }

func (f *fakeEndpointRepo) UpdateExposed(*auth.AuthInfo, string, bool) error { return nil }

func (f *fakeEndpointRepo) Delete(*auth.AuthInfo, string) error { return nil }

func (f *fakeEndpointRepo) DeleteByTarget(*auth.AuthInfo, string, string) error { return nil }

func (f *fakeEndpointRepo) RetractByTarget(*auth.AuthInfo, string, string) error { return nil }

func (f *fakeEndpointRepo) List(_ *auth.AuthInfo, req *svcendpoints.ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error) {
	out := f.filter(req)
	return out, len(out), nil
}

func (f *fakeEndpointRepo) ListAll(_ *auth.AuthInfo, req *svcendpoints.ListSvcEndpointsRequest) ([]models.SvcEndpoint, error) {
	return f.filter(req), nil
}

func (f *fakeEndpointRepo) filter(req *svcendpoints.ListSvcEndpointsRequest) []models.SvcEndpoint {
	f.gotReq = req
	out := make([]models.SvcEndpoint, 0, len(f.list))
	for _, ep := range f.list {
		if req.Protocol != "" && ep.Protocol != req.Protocol {
			continue
		}
		if req.TargetType != "" && ep.TargetType != req.TargetType {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// newTestCatalog builds a catalog with the fake endpoint repository injected (the test is in-package, so private fields can be filled
// directly).
func newTestCatalog(eps svcendpoints.SvcEndpointRepository) *Catalog {
	return &Catalog{schema: "t0", isSupervisor: true, eps: eps}
}

// Every category that has built-in tools must be in builtinGroupCategories - miss one and the whole group disappears from the grant panel,
// taking its tools with it (the frontend renders groups only), and nothing errors.
//
// The historical lesson: when shell / script were split out of exec, BuiltinCategories and builtinCategoryLabel were both updated, and only
// this explicit list was missed, which made builtin:shell impossible to select in the UI.
func TestBuiltinGroupCategoriesCoverTools(t *testing.T) {
	listed := map[string]bool{}
	for _, cat := range builtinGroupCategories {
		listed[cat] = true
		if !BuiltinCategories()[cat] {
			t.Errorf("builtinGroupCategories contains %q, which is not a valid built-in category (missing from BuiltinCategories)", cat)
		}
	}

	for _, ti := range BuiltinTools() {
		cat := builtinCategory(ti)
		if cat == "" {
			continue // uncategorised: it joins no category group anyway
		}
		if !listed[cat] {
			t.Errorf("category %q has built-in tools (such as %s) but is not in builtinGroupCategories - "+
				"the group will disappear from the grant panel", cat, ti.Name)
		}
	}
}

// local:rpc only takes "workflow endpoints": agent endpoints neither join the group nor accept a single ref.
// The reason: a direct call inside an agent endpoint bypasses call_agent's gates (agent_relations / IsRunnable / main being callable), and
// an endpoint name always equals the entity name, so an agent and a flow with the same name would collide in both ref and tool name.
//
// The filter happens in the request arguments (SQL layer), so assertion 1 is the core one: "target_type=flow was passed".
// Assertions 2/3 use a faithful fake repository to cover the results of both paths (group expansion / single ref).
func TestLocalRPCToolsOnlyFlowEndpoints(t *testing.T) {
	repo := &fakeEndpointRepo{list: []models.SvcEndpoint{
		{ID: "e1", Name: "daily-report", Protocol: LocalProtocolRPC, TargetType: LocalTargetFlow},
		{ID: "e2", Name: "poet", Protocol: LocalProtocolRPC, TargetType: "agent"},
		{ID: "e3", Name: "poet-other", Protocol: "mcp", TargetType: "agent"},
	}}
	c := newTestCatalog(repo)

	list, err := c.ToolsOfRef("local:rpc")
	if err != nil {
		t.Fatalf("ToolsOfRef(local:rpc) = %v, want nil", err)
	}
	// 1) Query conditions: protocol rpc + target flow
	if repo.gotReq == nil {
		t.Fatal("the endpoint repository was never queried")
	}
	if repo.gotReq.Protocol != LocalProtocolRPC || repo.gotReq.TargetType != LocalTargetFlow {
		t.Fatalf("List req = {protocol:%q target_type:%q}, want {%q %q}",
			repo.gotReq.Protocol, repo.gotReq.TargetType, LocalProtocolRPC, LocalTargetFlow)
	}
	// 2) Group expansion leaves only the workflow endpoint
	wantRef := ScopeLocal + ":" + LocalProtocolRPC + ":daily-report"
	if len(list) != 1 || list[0].Ref != wantRef {
		t.Fatalf("ToolsOfRef(local:rpc) = %+v, want only %s", list, wantRef)
	}
	if list[0].Name != "local__daily-report" {
		t.Errorf("tool name = %q, want %q", list[0].Name, "local__daily-report")
	}

	// 3) A single agent endpoint ref expands to nothing
	got, err := c.ToolsOfRef("local:rpc:poet")
	if err != nil {
		t.Fatalf("ToolsOfRef(local:rpc:poet) = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("an agent endpoint must not become a tool, got %+v", got)
	}
}

// A local endpoint's tool arguments must come from the workflow's arguments (ep.InputSchema).
//
// Without it the model sees a "tool with no arguments": either it dares not call it, or it invents arguments and trips the required check
// (the mcp / rpc branches hit the same pitfall, see the notes on toolSchema in catalog.go).
func TestLocalRPCToolCarriesInputSchema(t *testing.T) {
	repo := &fakeEndpointRepo{list: []models.SvcEndpoint{
		{
			ID: "e1", Name: "daily-report", Protocol: LocalProtocolRPC, TargetType: LocalTargetFlow,
			Exposed: xsql.Bool(true),
			InputSchema: xsql.JSONB{Data: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{"type": "string"},
				},
				"required": []any{"topic"},
			}},
		},
	}}
	c := newTestCatalog(repo)

	list, err := c.ToolsOfRef("local:rpc:daily-report")
	if err != nil {
		t.Fatalf("ToolsOfRef = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("tool count = %d, want 1", len(list))
	}
	if list[0].Parameters == nil {
		t.Fatal("the local endpoint tool is missing Parameters - the model would see a tool with no arguments")
	}
	if list[0].Parameters["type"] != "object" {
		t.Errorf("Parameters.type = %v, want object", list[0].Parameters["type"])
	}
}

// refSet reduces a tool list to a set of names.
func refSet(list []ToolInfo) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, t := range list {
		out[t.Name] = true
	}
	return out
}

// The category is the permission: the three skill-tool groups must each sit in their own place, with no overlap.
//   - builtin:skill       view / use (list_skills / read_skill / use_skill), available to ordinary agents, resident for the super agent
//   - builtin:skill-own   build your own private ones (*_own_skill), available to ordinary agents but not granted by default
//   - builtin:skill-admin create shared ones / create private ones for other agents, super agent only
//
// The category is the only criterion of the three-layer closure (catalog does not produce it / it cannot be granted / the execution layer
// hard-gates it), so changing it moves all three layers at once.
func TestSkillToolGroups(t *testing.T) {
	c := newTestCatalog(nil) // isSupervisor=true; expanding built-in groups does not touch any repository

	expand := func(ref string) map[string]bool {
		list, err := c.ToolsOfRef(ref)
		if err != nil {
			t.Fatalf("ToolsOfRef(%s) = %v, want nil", ref, err)
		}
		return refSet(list)
	}
	skillNames := expand("builtin:skill")
	ownNames := expand("builtin:skill-own")
	adminNames := expand("builtin:skill-admin")

	// Viewing skills only: own / admin must never leak in (otherwise "let an agent view skills" also hands out write permission).
	for _, n := range []string{"list_skills", "read_skill", "use_skill"} {
		if !skillNames[n] {
			t.Errorf("builtin:skill is missing %q", n)
		}
		if ownNames[n] || adminNames[n] {
			t.Errorf("%q should belong to builtin:skill only", n)
		}
	}

	own := []string{"create_own_skill", "update_own_skill", "delete_own_skill"}
	for _, n := range own {
		if !ownNames[n] {
			t.Errorf("builtin:skill-own is missing %q (the agent could not build its own private skills)", n)
		}
		// Leaking into skill would turn "view skills" into "rewrite your own prompt surface";
		// leaking into skill-admin would deny ordinary agents the ability to build their own.
		if skillNames[n] || adminNames[n] {
			t.Errorf("%q should belong to builtin:skill-own only", n)
		}
	}

	// The three shared tools + the three explicit-owner tools: all in skill-admin (super agent only), and they must not leak into the
	// ordinary groups.
	for _, n := range []string{
		"create_shared_skill", "update_shared_skill", "delete_shared_skill",
		"create_agent_skill", "update_agent_skill", "delete_agent_skill",
	} {
		if !adminNames[n] {
			t.Errorf("builtin:skill-admin is missing %q", n)
		}
		if skillNames[n] || ownNames[n] {
			t.Errorf("%q should belong to builtin:skill-admin only (otherwise a non-super agent could build shared skills / write someone else's prompt)", n)
		}
	}
	// The old "single tool + scope argument" shape must be gone completely (otherwise grant refs and tool names live in two sets)
	for _, n := range []string{"create_skill", "update_skill", "delete_skill"} {
		if skillNames[n] || ownNames[n] || adminNames[n] {
			t.Errorf("the old tool name %q is still in a group; it should have been renamed to *_own_skill / *_shared_skill", n)
		}
	}
}

// stubScriptRepo provides only ListEnabled (which the catalog read needs); the other methods are unused.
type stubScriptRepo struct{ list []models.CustomTool }

func (s stubScriptRepo) List(string) ([]models.CustomTool, error)        { return s.list, nil }
func (s stubScriptRepo) ListEnabled(string) ([]models.CustomTool, error) { return s.list, nil }
func (s stubScriptRepo) Get(string, string) (*models.CustomTool, error)  { return nil, nil }
func (s stubScriptRepo) Create(string, *models.CustomTool) error         { return nil }
func (s stubScriptRepo) Update(string, *models.CustomTool) error         { return nil }
func (s stubScriptRepo) Delete(string, string) error                     { return nil }

// The three empty repositories below only exist so Groups() runs end to end (connections / endpoints / custom groups all come back empty).
type emptyMcpRepo struct{ mcpconnections.Repository }

func (emptyMcpRepo) List(*auth.AuthInfo, *mcpconnections.ListMcpConnectionsRequest) ([]models.McpConnection, int, error) {
	return nil, 0, nil
}

type emptyRpcRepo struct{ rpcconnections.Repository }

func (emptyRpcRepo) List(*auth.AuthInfo, *rpcconnections.ListRpcConnectionsRequest) ([]models.RpcConnection, int, error) {
	return nil, 0, nil
}

type emptyGrantsRepo struct{ toolgrants.Repository }

func (emptyGrantsRepo) ListGroups(string) ([]models.ToolGroup, error) { return nil, nil }

// A custom tool group card's ref must be exactly the ref of its member items (custom:<short name>).
//
// The old bug: the group ref was assembled as ScopeCustom+":"+t.Name, while ToolInfo.Name is the **model-side** tool name (custom tools
// carry the custom__ prefix), so the group card offered custom:custom__xxx. Clicking it wrote that into the grant draft, and on save the
// backend's ToolsOfRef expanded it to nothing -> "ref not found or has no tools: custom:custom__xxx", leaving the user no way to see which
// layer was wrong.
//
// Now it goes further: custom tools are no longer one card per tool (which would make the group name a tool name) but are merged into one
// Synthetic category card; the card is not grantable and the grant target stays the member's custom:<short name>.
func TestGroupsScriptIsSyntheticCategory(t *testing.T) {
	const short = "interest-final-amount"
	// Build the catalog directly: Groups() walks connections / endpoints / custom groups, all of which get empty repositories so no real
	// database is touched.
	c := &Catalog{
		schema: "t0", isSupervisor: true,
		grants: emptyGrantsRepo{}, mcp: emptyMcpRepo{}, rpc: emptyRpcRepo{},
		eps: &fakeEndpointRepo{}, policies: func(string) (map[string]pluginpolicy.Policy, error) { return nil, nil },
		scripts: stubScriptRepo{list: []models.CustomTool{{
			Name: short, DisplayName: "利息终值计算", Status: 1,
		}}},
	}

	groups, err := c.Groups()
	if err != nil {
		t.Fatalf("Groups = %v", err)
	}
	var g *GroupInfo
	for i := range groups {
		if groups[i].Kind == ScopeCustom {
			g = &groups[i]
			break
		}
	}
	if g == nil {
		t.Fatal("the custom-tool category card is missing (without it the frontend has nothing to grant against)")
	}

	// 1) The card is a container: named after the category (not a tool name), and marked Synthetic.
	if !g.Synthetic {
		t.Error("the custom-tool card must be Synthetic (a container cannot be granted as a whole)")
	}
	if g.Name == "利息终值计算" {
		t.Errorf("the card name must not be a tool name: %q", g.Name)
	}
	// 2) The container ref must not be a valid grant ref (otherwise whole-group grant semantics would appear unnoticed).
	if err := ValidateRef(g.Ref, true); err == nil {
		t.Errorf("the container ref %q must not pass ValidateRef", g.Ref)
	}
	// 3) The member is that tool, with ref custom:<short name>; only the model-side name carries custom__.
	if len(g.Items) != 1 {
		t.Fatalf("member count = %d, want 1", len(g.Items))
	}
	item := g.Items[0]
	if want := ScopeCustom + ":" + short; item.Ref != want {
		t.Fatalf("member ref = %q, want %q (the custom__ prefix must not appear)", item.Ref, want)
	}
	if item.Name != tools.CustomToolPrefix+short {
		t.Errorf("model-side tool name = %q, want %q", item.Name, tools.CustomToolPrefix+short)
	}
	// 4) The grant target is still the member ref: expanding it must yield the tool (that is exactly what the existence check on save does).
	list, err := c.ToolsOfRef(item.Ref)
	if err != nil {
		t.Fatalf("ToolsOfRef(%s) = %v", item.Ref, err)
	}
	if len(list) != 1 || list[0].Ref != item.Ref {
		t.Fatalf("expanding the member ref gave the wrong result: %+v", list)
	}
}

// The local endpoint constants must line up with svcendpoints' literals (that repository validates target_type ∈ {agent, flow}).
func TestLocalEndpointConstants(t *testing.T) {
	if LocalProtocolRPC != "rpc" {
		t.Errorf("LocalProtocolRPC = %q, want %q", LocalProtocolRPC, "rpc")
	}
	if LocalTargetFlow != "flow" {
		t.Errorf("LocalTargetFlow = %q, want %q", LocalTargetFlow, "flow")
	}
}
