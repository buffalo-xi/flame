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

// preset:default 必须与 models.DefaultAgentTools 完全一致 —— 单一真源，防漂移。
func TestDefaultPresetMatchesModels(t *testing.T) {
	want := models.DefaultAgentToolGrants()

	var got []models.ToolGroupItem
	for _, pg := range PresetGroups {
		if pg.Name == "default" {
			got = pg.Items
		}
	}
	if got == nil {
		t.Fatal("preset:default 不存在")
	}
	if len(got) != len(want) {
		t.Fatalf("preset:default 成员数 %d != DefaultAgentTools %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 %+v != %+v", i, got[i], want[i])
		}
	}
}

// 共享层的 ref 前缀常量必须与服务层的 scope 常量一致（分层不许反向依赖，靠测试守住）。
func TestBuiltinPrefixConstantMatches(t *testing.T) {
	if models.GrantRefBuiltinPrefix != ScopeBuiltin+":" {
		t.Fatalf("GrantRefBuiltinPrefix=%q 与 ScopeBuiltin=%q 不一致",
			models.GrantRefBuiltinPrefix, ScopeBuiltin)
	}
}

// builtin:other 已删除：不再是类别组，引用它也展开不出工具。
func TestBuiltinOtherRemoved(t *testing.T) {
	if BuiltinCategories()["other"] {
		t.Fatal("builtin:other 不应再是内置类别")
	}
	list, err := NewCatalog("t0", nil, true).ToolsOfRef("builtin:other")
	if err != nil {
		t.Fatalf("ToolsOfRef = %v, want nil", err)
	}
	if len(list) != 0 {
		t.Fatalf("ToolsOfRef(builtin:other) = %d 个工具, want 0", len(list))
	}
}

// builtin:super 是拆组前的聚合分类，现在没有任何工具归属它，只作为**兼容展开入口**存在：
// 必须等价于五个管理组的并集。断掉的话，存量授权行（builtin:super）会静默解析成空 ——
// 超级智能体丢掉全部管理能力，且没有任何报错。
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
			t.Errorf("管理组 %q 下没有工具", category)
		}
		for _, ti := range list {
			want[ti.Ref] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("五个管理组的并集为空")
	}

	got := map[string]bool{}
	for _, ti := range super {
		got[ti.Ref] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Errorf("builtin:super 展开缺少 %q", ref)
		}
	}
	for ref := range got {
		if !want[ref] {
			t.Errorf("builtin:super 展开多出 %q", ref)
		}
	}
}

// builtin:super 不能出现在 Groups() 里：它没有成员、且与五个管理组重复，
// 列进 builtinGroupCategories 会让面板多一张同名空卡。
func TestSuperIsCompatibleCategoryNotGroup(t *testing.T) {
	if !BuiltinCategories()[tools.CategorySuper] {
		t.Error("builtin:super 必须是可解析的内置类别（否则存量授权行解析失败）")
	}
	for _, category := range builtinGroupCategories {
		if category == tools.CategorySuper {
			t.Error("builtin:super 不应出现在 builtinGroupCategories（会多出一张重复卡片）")
		}
	}
	// 每个出现在目录里的组都必须真的有成员，否则就是一张永远空着的卡片。
	cat := NewCatalog("t0", nil, true)
	for _, category := range builtinGroupCategories {
		list, err := cat.ToolsOfRef(ScopeBuiltin + ":" + category)
		if err != nil {
			t.Fatalf("ToolsOfRef(builtin:%s) = %v", category, err)
		}
		if len(list) == 0 {
			t.Errorf("目录里的组 builtin:%s 没有成员", category)
		}
	}
}

// stubCategorylessTool 未标注分类的内置工具（测试用）。
type stubCategorylessTool struct{}

func (s *stubCategorylessTool) Meta() tools.ToolMeta {
	return tools.ToolMeta{Name: "stub_categoryless", Description: "测试用：未标注分类"}
}

func (s *stubCategorylessTool) Execute(_ context.Context, _ *tools.Context, _ map[string]any) (any, error) {
	return nil, nil
}

// 未标注分类的工具不进任何类别组：Source 留空（不再回落为 builtin:other），
// 因而不会被任何 builtin:<category> 组命中（builtinByCategory 按 Source 比对）。
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
				t.Errorf("未分类工具被归入类别 %q", category)
			}
		}
	}
	if !found {
		t.Fatal("stub_categoryless 未出现在 BuiltinTools 中")
	}
}

// 预设组名不得重复（Groups() 里 preset:<name> 是唯一键）。
func TestPresetNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, pg := range PresetGroups {
		if seen[pg.Name] {
			t.Errorf("预设组名重复: %q", pg.Name)
		}
		seen[pg.Name] = true
	}
}

// fakeEndpointRepo 假端点仓库：只实现 List，其余方法返回零值。
// List 记录收到的查询请求，并「如实」按协议 / 目标类型过滤（真实实现在 SQL 层过滤）。
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

// newTestCatalog 构造注入了假端点仓库的目录（同包可直填私有字段）。
func newTestCatalog(eps svcendpoints.SvcEndpointRepository) *Catalog {
	return &Catalog{schema: "t0", isSupervisor: true, eps: eps}
}

// 有内置工具归属的分类必须都在 builtinGroupCategories 里 —— 漏一个，该组在授权
// 面板上整个消失，连组内工具也一起不见（前端只渲染 groups），而且不会报任何错。
//
// 历史教训：shell / script 从 exec 拆出来时，BuiltinCategories 与 builtinCategoryLabel
// 都跟着更新了，只有这份显式清单漏了，导致 builtin:shell 在 UI 里根本选不到。
func TestBuiltinGroupCategoriesCoverTools(t *testing.T) {
	listed := map[string]bool{}
	for _, cat := range builtinGroupCategories {
		listed[cat] = true
		if !BuiltinCategories()[cat] {
			t.Errorf("builtinGroupCategories 含 %q，但它不是合法内置类别（BuiltinCategories 里没有）", cat)
		}
	}

	for _, ti := range BuiltinTools() {
		cat := builtinCategory(ti)
		if cat == "" {
			continue // 未标注分类：本就不进任何类别组
		}
		if !listed[cat] {
			t.Errorf("分类 %q 有内置工具（如 %s），但不在 builtinGroupCategories 里 —— "+
				"该组在授权面板上会整个消失", cat, ti.Name)
		}
	}
}

// local:rpc 只收「工作流端点」：agent 端点既不进组，也不接受单个 ref 引用。
// 理由：agent 端点内部直调会绕过 call_agent 的闸门（agent_relations / IsRunnable /
// main 可被调用），且端点名恒等于实体名，agent 与 flow 同名会撞 ref 与工具名。
//
// 过滤发生在请求参数（SQL 层），故断言 1 是核心：「传了 target_type=flow」。
// 断言 2/3 用如实照做的假仓库，覆盖「组展开 / 单个 ref」两条路径的结果。
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
	// 1) 查询条件：协议 rpc + 目标 flow
	if repo.gotReq == nil {
		t.Fatal("端点仓库未被查询")
	}
	if repo.gotReq.Protocol != LocalProtocolRPC || repo.gotReq.TargetType != LocalTargetFlow {
		t.Fatalf("List req = {protocol:%q target_type:%q}, want {%q %q}",
			repo.gotReq.Protocol, repo.gotReq.TargetType, LocalProtocolRPC, LocalTargetFlow)
	}
	// 2) 组展开只剩工作流端点
	wantRef := ScopeLocal + ":" + LocalProtocolRPC + ":daily-report"
	if len(list) != 1 || list[0].Ref != wantRef {
		t.Fatalf("ToolsOfRef(local:rpc) = %+v, want 仅 %s", list, wantRef)
	}
	if list[0].Name != "local__daily-report" {
		t.Errorf("工具名 = %q, want %q", list[0].Name, "local__daily-report")
	}

	// 3) 单个 agent 端点 ref 展开为空
	got, err := c.ToolsOfRef("local:rpc:poet")
	if err != nil {
		t.Fatalf("ToolsOfRef(local:rpc:poet) = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("agent 端点不应成为工具，got %+v", got)
	}
}

// 本地端点的工具参数必须取自工作流的 arguments（ep.InputSchema）。
//
// 缺了它模型看到的是「无参数工具」：要么不敢调，要么瞎编参数撞 required 校验
// （mcp / rpc 分支踩过同一个坑，见 catalog.go 里 toolSchema 的说明）。
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
		t.Fatalf("工具数 = %d, want 1", len(list))
	}
	if list[0].Parameters == nil {
		t.Fatal("本地端点工具缺 Parameters —— 模型会看到无参数工具")
	}
	if list[0].Parameters["type"] != "object" {
		t.Errorf("Parameters.type = %v, want object", list[0].Parameters["type"])
	}
}

// refSet 把工具清单收成名字集合。
func refSet(list []ToolInfo) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, t := range list {
		out[t.Name] = true
	}
	return out
}

// 分类即权限：三组技能工具必须各就各位，且互不重叠。
//   - builtin:skill      看 / 用（list_skills / read_skill / use_skill），普通 agent 可用，超级智能体常驻
//   - builtin:skill-own  自建私有（*_own_skill），普通 agent 可用但默认不授
//   - builtin:skill-admin 建共享 / 替别的 agent 建私有，仅超级智能体
//
// 分类是三层收口的唯一判据（目录不产出 / 授不了权 / 执行层硬闸），改一处会同时影响三层。
func TestSkillToolGroups(t *testing.T) {
	c := newTestCatalog(nil) // isSupervisor=true；内置组展开不碰仓库

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

	// 只看技能：绝不能被 own / admin 混进来（否则「给某 agent 看技能」会顺带给出写权限）。
	for _, n := range []string{"list_skills", "read_skill", "use_skill"} {
		if !skillNames[n] {
			t.Errorf("builtin:skill 缺少 %q", n)
		}
		if ownNames[n] || adminNames[n] {
			t.Errorf("%q 只应属于 builtin:skill", n)
		}
	}

	own := []string{"create_own_skill", "update_own_skill", "delete_own_skill"}
	for _, n := range own {
		if !ownNames[n] {
			t.Errorf("builtin:skill-own 缺少 %q（agent 将无法自建私有 skill）", n)
		}
		// 混进 skill 会让「只看技能」变成「能写自己的提示词面」；
		// 混进 skill-admin 会让普通 agent 永远拿不到自建能力。
		if skillNames[n] || adminNames[n] {
			t.Errorf("%q 只应属于 builtin:skill-own", n)
		}
	}

	// 共享三件套 + 指定归属三件套：都在 skill-admin（仅超级智能体），且不得漏到普通组。
	for _, n := range []string{
		"create_shared_skill", "update_shared_skill", "delete_shared_skill",
		"create_agent_skill", "update_agent_skill", "delete_agent_skill",
	} {
		if !adminNames[n] {
			t.Errorf("builtin:skill-admin 缺少 %q", n)
		}
		if skillNames[n] || ownNames[n] {
			t.Errorf("%q 只应属于 builtin:skill-admin（否则非超级智能体就能建共享 skill / 写别人的提示词）", n)
		}
	}
	// 旧的「单工具 + scope 参数」形态必须彻底消失（否则授权 ref 与工具名两套并存）
	for _, n := range []string{"create_skill", "update_skill", "delete_skill"} {
		if skillNames[n] || ownNames[n] || adminNames[n] {
			t.Errorf("旧工具名 %q 仍在组里，应已改名为 *_own_skill / *_shared_skill", n)
		}
	}
}

// stubScriptRepo 只提供目录读取所需的 ListEnabled（其余方法用不到）。
type stubScriptRepo struct{ list []models.CustomTool }

func (s stubScriptRepo) List(string) ([]models.CustomTool, error)        { return s.list, nil }
func (s stubScriptRepo) ListEnabled(string) ([]models.CustomTool, error) { return s.list, nil }
func (s stubScriptRepo) Get(string, string) (*models.CustomTool, error)  { return nil, nil }
func (s stubScriptRepo) Create(string, *models.CustomTool) error         { return nil }
func (s stubScriptRepo) Update(string, *models.CustomTool) error         { return nil }
func (s stubScriptRepo) Delete(string, string) error                     { return nil }

// 下面三个空仓库只为让 Groups() 走完整流程（连接 / 端点 / 自定义组都返回空）。
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

// 自定义工具组卡片的 ref 必须与它成员项的 ref 完全相同（custom:<短名>）。
//
// 曾经的 bug：组的 ref 用 ScopeCustom+":"+t.Name 现拼，而 ToolInfo.Name 是**模型侧**
// 工具名（自定义工具带 custom__ 前缀），于是组卡片给出 custom:custom__xxx。前端点组卡片
// 把它写进授权草稿，保存时后端 ToolsOfRef 展开为空 → 报
// 「ref not found or has no tools: custom:custom__xxx」，用户完全看不出错在哪一层。
//
// 现在更进一步：自定义工具不再一工具一卡（那会让组名变成工具名），而是合成一张
// Synthetic 类别卡；卡片不可授权，授权落点仍是成员的 custom:<短名>。
func TestGroupsScriptIsSyntheticCategory(t *testing.T) {
	const short = "interest-final-amount"
	// 直构目录：Groups() 会走连接 / 端点 / 自定义组，全部给空仓库，避免碰真库。
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
		t.Fatal("自定义工具类别卡缺失（目录里没有它，前端就无从授权）")
	}

	// 1) 卡片是容器：名字按类别（不是工具名），且标了 Synthetic。
	if !g.Synthetic {
		t.Error("自定义工具卡片必须是 Synthetic（容器不可整组授权）")
	}
	if g.Name == "利息终值计算" {
		t.Errorf("卡片名不该是工具名：%q", g.Name)
	}
	// 2) 容器 ref 不能是有效授权 ref（否则整组授权语义会悄悄出现）。
	if err := ValidateRef(g.Ref, true); err == nil {
		t.Errorf("容器 ref %q 不该通过 ValidateRef", g.Ref)
	}
	// 3) 成员就是那个工具，ref 为 custom:<短名>，模型侧名字才带 custom__。
	if len(g.Items) != 1 {
		t.Fatalf("成员数 = %d, want 1", len(g.Items))
	}
	item := g.Items[0]
	if want := ScopeCustom + ":" + short; item.Ref != want {
		t.Fatalf("成员 ref = %q, want %q（不能带 custom__ 前缀）", item.Ref, want)
	}
	if item.Name != tools.CustomToolPrefix+short {
		t.Errorf("模型侧工具名 = %q, want %q", item.Name, tools.CustomToolPrefix+short)
	}
	// 4) 授权落点仍是成员 ref：拿它展开必须取到工具（保存授权的存在性校验就是这么做的）。
	list, err := c.ToolsOfRef(item.Ref)
	if err != nil {
		t.Fatalf("ToolsOfRef(%s) = %v", item.Ref, err)
	}
	if len(list) != 1 || list[0].Ref != item.Ref {
		t.Fatalf("成员 ref 展开结果不对：%+v", list)
	}
}

// 本地端点常量必须与 svcendpoints 的字面量对齐（该仓校验 target_type ∈ {agent, flow}）。
func TestLocalEndpointConstants(t *testing.T) {
	if LocalProtocolRPC != "rpc" {
		t.Errorf("LocalProtocolRPC = %q, want %q", LocalProtocolRPC, "rpc")
	}
	if LocalTargetFlow != "flow" {
		t.Errorf("LocalTargetFlow = %q, want %q", LocalTargetFlow, "flow")
	}
}
