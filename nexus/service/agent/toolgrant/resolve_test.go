package toolgrant

import (
	"reflect"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	mcpconnections "flame/nexus/shared/repository/mcpconnections"
	rpcconnections "flame/nexus/shared/repository/rpcconnections"
	"flame/nexus/shared/repository/toolgrants"
)

// ===== 测试替身 =====
//
// 一律「嵌入接口 + 只覆写 Resolve 会调到的方法」：未覆写的方法被调用即 panic，
// 这样将来 Resolve 新增仓库依赖时测试会立刻炸出来，而不是静默拿到零值。

// stubGrants 假授权仓库（agent 授权 + skill 工具依赖）。
// 「不限制工具」开关已归 agent 自身，不再从这里读 —— 由用例直接传给 NewResolver。
type stubGrants struct {
	toolgrants.Repository
	agentGrants []models.AgentToolGrant
	skillGrants map[string][]models.SkillToolGrant
}

func (s *stubGrants) ListAgentGrants(string, string) ([]models.AgentToolGrant, error) {
	return s.agentGrants, nil
}

func (s *stubGrants) ListSkillGrants(_ string, skillID string) ([]models.SkillToolGrant, error) {
	return s.skillGrants[skillID], nil
}

// stubMCPRepo 空 MCP 连接仓库：AllTools 只在连接非空时才会调 ListTools，这里返回空即可。
type stubMCPRepo struct{ mcpconnections.Repository }

func (s *stubMCPRepo) List(*auth.AuthInfo, *mcpconnections.ListMcpConnectionsRequest) ([]models.McpConnection, int, error) {
	return nil, 0, nil
}

// stubRPCRepo 空 RPC 连接仓库。
type stubRPCRepo struct{ rpcconnections.Repository }

func (s *stubRPCRepo) List(*auth.AuthInfo, *rpcconnections.ListRpcConnectionsRequest) ([]models.RpcConnection, int, error) {
	return nil, 0, nil
}

// newResolveCatalog 构造只含内置工具、不碰数据库的目录（同包可直填私有字段）。
func newResolveCatalog(g toolgrants.Repository) *Catalog {
	return &Catalog{
		schema:       "t0",
		isSupervisor: true,
		grants:       g,
		mcp:          &stubMCPRepo{},
		rpc:          &stubRPCRepo{},
		eps:          &fakeEndpointRepo{}, // 复用 catalog_test.go 的假端点仓库
	}
}

// Resolve 的授权口径（本次变更的核心，锁死在这里）：
//
//	required —— 唯一能独立生效的授权：恒注入，deny 也动不了它；
//	allow    —— 只是「本轮已激活（use_skill）技能声明的工具依赖」的过滤器，**不能单独生效**；
//	deny     —— 减法，对 required 无效；
//	不限制工具 —— 关掉 allow 这道收窄，让 skill 声明的依赖全给；
//	skill 无依赖 —— allow 一律不注入（不再回落「授权即生效」），只留 required。
//
// 这条规则决定的是权限边界，改语义必须改这里 —— 否则 `case` 顺序被谁调回去都没人发现。
func TestResolveGrantSemantics(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"   // required
		allowRef    = "builtin:exec"        // allow，且被 skill 声明
		idleAllow   = "builtin:write_file"  // allow，但没被任何 skill 声明
		denyRef     = "builtin:delete_file" // deny，且被 skill 声明
		skillID     = "sk1"
	)

	grant := func(kind, ref string) models.AgentToolGrant {
		return models.AgentToolGrant{GrantKind: kind, ItemType: models.GrantItemTool, Ref: ref}
	}
	dep := func(ref string) models.SkillToolGrant {
		return models.SkillToolGrant{SkillID: skillID, ItemType: models.GrantItemTool, Ref: ref}
	}

	cases := []struct {
		name         string
		grants       []models.AgentToolGrant
		skillDeps    []models.SkillToolGrant
		unrestricted bool
		want         []string // 最终生效的工具名（required 在前，其余按技能声明顺序）
		wantDegraded bool
		wantWarn     string // warnings 里必须出现的子串（空 = 不校验）
	}{
		{
			name: "skill 无依赖：allow 一律不注入，只留 required",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, idleAllow),
			},
			want:         []string{"read_file"},
			wantDegraded: true,
			wantWarn:     "allow 授权本轮全部未生效",
		},
		{
			name: "skill 无依赖 + 不限制工具：仍然只留 required（开关也不看 allow）",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
			},
			unrestricted: true,
			want:         []string{"read_file"},
			wantDegraded: true,
			wantWarn:     "allow 授权本轮全部未生效",
		},
		{
			name: "有依赖：required ∪ (候选 ∩ allow) —— idleAllow 没被声明，不生效",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, idleAllow),
			},
			skillDeps: []models.SkillToolGrant{dep(allowRef)},
			// builtin:exec 组含 exec 与 run_skill_script（两者同走 /v1/exec，
			// 见 tools/skillrun.go 的分类说明）；shell / script 各自成组。
			want: []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name: "有依赖但不限制工具：候选全给，不经 allow 收窄",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
			},
			skillDeps:    []models.SkillToolGrant{dep(allowRef)},
			unrestricted: true,
			want:         []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name: "deny 能减掉候选，但动不了 required",
			grants: []models.AgentToolGrant{
				grant(models.GrantKindRequired, requiredRef),
				grant(models.GrantKindAllow, allowRef),
				grant(models.GrantKindAllow, denyRef),
				grant(models.GrantKindDeny, denyRef),     // 命中候选 → 减掉
				grant(models.GrantKindDeny, requiredRef), // 命中 required → 无效
			},
			skillDeps: []models.SkillToolGrant{dep(allowRef), dep(denyRef)},
			want:      []string{"read_file", "exec", "run_skill_script"},
		},
		{
			name:   "三者皆空：什么都没有",
			grants: nil,
			want:   []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &stubGrants{
				agentGrants: tc.grants,
				skillGrants: map[string][]models.SkillToolGrant{skillID: tc.skillDeps},
			}
			// 只有真的声明了依赖才把这个 skill 放进「本轮已激活」清单（对齐 LoadedSkills 口径）
			visible := []VisibleSkill{}
			if len(tc.skillDeps) > 0 {
				visible = append(visible, VisibleSkill{ID: skillID, Name: skillID})
			}

			res, err := NewResolver(newResolveCatalog(g), "t0", visible, tc.unrestricted).Resolve("agent-1")
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}

			got := res.FinalNames
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("生效工具 = %v, want %v", got, tc.want)
			}
			if res.Degraded != tc.wantDegraded {
				t.Errorf("Degraded = %v, want %v", res.Degraded, tc.wantDegraded)
			}
			if tc.wantWarn != "" {
				joined := strings.Join(res.Warnings, "\n")
				if !strings.Contains(joined, tc.wantWarn) {
					t.Errorf("warnings = %q, 期望包含 %q", joined, tc.wantWarn)
				}
			}
		})
	}
}

// 反证：allow 与「候选」必须来自「本轮已激活的 skill」。
// 没被 use_skill 激活的技能即使声明了工具依赖（哪怕是绑定给它的），也不该把工具带出来 —— 紧口径，防扩权。
func TestResolveIgnoresUnboundSkillDeps(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"
		allowRef    = "builtin:exec"
		skillID     = "sk1"
	)
	g := &stubGrants{
		agentGrants: []models.AgentToolGrant{
			{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: allowRef},
		},
		// skill 声明了 exec，但下面不把它列为「本轮已激活」
		skillGrants: map[string][]models.SkillToolGrant{
			skillID: {{SkillID: skillID, ItemType: models.GrantItemTool, Ref: allowRef}},
		},
	}

	res, err := NewResolver(newResolveCatalog(g), "t0", nil, false).Resolve("agent-1")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if want := []string{"read_file"}; !reflect.DeepEqual(res.FinalNames, want) {
		t.Errorf("生效工具 = %v, want %v（未激活的 skill 不应带出工具）", res.FinalNames, want)
	}
}

// 依赖快照优先于实时表：正式运行激活技能时带的是**当前版本**的声明，
// 草稿里增删依赖不能漏进线上（只冻展示不冻生效，等于冻结没冻到底）。
func TestResolvePrefersFrozenDeps(t *testing.T) {
	const (
		requiredRef = "builtin:read_file"
		frozenRef   = "builtin:exec"       // 快照与实时表都有
		draftOnly   = "builtin:write_file" // 只在实时表里（草稿新加的依赖）
		skillID     = "sk1"
	)
	agentGrants := []models.AgentToolGrant{
		{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: frozenRef},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: draftOnly},
	}
	liveGrants := map[string][]models.SkillToolGrant{
		skillID: {
			{SkillID: skillID, ItemType: models.GrantItemTool, Ref: frozenRef},
			{SkillID: skillID, ItemType: models.GrantItemTool, Ref: draftOnly},
		},
	}

	cases := []struct {
		name  string
		deps  []string
		want  []string
		cause string
	}{
		{
			name:  "有快照：只认快照，草稿新增的依赖不生效",
			deps:  []string{frozenRef},
			want:  []string{"read_file", "exec", "run_skill_script"}, // builtin:exec 组含这两个
			cause: "草稿里给技能加了一条依赖，线上不该被点亮",
		},
		{
			name:  "空快照：这一版确实没有依赖，不回落实时表",
			deps:  []string{},
			want:  []string{"read_file"},
			cause: "空切片是权威值，回落查库就等于让草稿说了算",
		},
		{
			name:  "nil 快照：无版本信息，回落实时表（老口径）",
			deps:  nil,
			want:  []string{"read_file", "exec", "run_skill_script", "write_file"},
			cause: "管理端预览 / 未绑定版本的场景仍要能看到实时声明",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &stubGrants{agentGrants: agentGrants, skillGrants: liveGrants}
			visible := []VisibleSkill{{ID: skillID, Name: skillID, Deps: tc.deps}}
			res, err := NewResolver(newResolveCatalog(g), "t0", visible, false).Resolve("agent-1")
			if err != nil {
				t.Fatalf("Resolve = %v, want nil", err)
			}
			if !reflect.DeepEqual(res.FinalNames, tc.want) {
				t.Errorf("生效工具 = %v, want %v（%s）", res.FinalNames, tc.want, tc.cause)
			}
		})
	}
}

// 截断优先级：required 恒保留，其余按技能声明顺序。
// 旧实现按名字字母序截断，多激活一个技能就可能把 required 或先声明的依赖静默切掉。
func TestResolveTruncationPrefersRequired(t *testing.T) {
	const skillID = "sk1"
	requiredRef := "builtin:list_agents"
	firstDep := "builtin:write_file" // 声明在前
	secondDep := "builtin:exec"      // 字母序更靠前，但声明在后 —— 应当先被丢弃

	g := &stubGrants{
		agentGrants: []models.AgentToolGrant{
			{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: requiredRef},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: firstDep},
			{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: secondDep},
		},
		skillGrants: map[string][]models.SkillToolGrant{
			skillID: {
				{SkillID: skillID, ItemType: models.GrantItemTool, Ref: firstDep},
				{SkillID: skillID, ItemType: models.GrantItemTool, Ref: secondDep},
			},
		},
	}
	r := NewResolver(newResolveCatalog(g), "t0", []VisibleSkill{{ID: skillID, Name: skillID}}, false)
	r.MaxTools = 2

	res, err := r.Resolve("agent-1")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	// required 必须在，且先声明的 write_file 优先于字母序更靠前的 exec
	want := []string{"list_agents", "write_file"}
	if !reflect.DeepEqual(res.FinalNames, want) {
		t.Errorf("截断后工具 = %v, want %v", res.FinalNames, want)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "截断") {
		t.Errorf("截断应有告警, got %v", res.Warnings)
	}
}

// 自定义工具的授权必须真的落进最终工具集。
//
// 曾经的 bug：AllTools() 漏了 script 这一类 —— ToolsOfRef 能展开，最后一步却按
// 「引用的工具已不存在：custom:xxx」丢掉。表现为「授权保存成功、agent 永远看不见」，
// 而告警文本指向 ref（让人以为 ref 写错），实际是目录缺了一整类工具。
func TestResolveInjectsCustomTool(t *testing.T) {
	const short = "interest-final-amount"
	g := &stubGrants{agentGrants: []models.AgentToolGrant{{
		GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool,
		Ref: ScopeCustom + ":" + short,
	}}}
	cat := newResolveCatalog(g)
	cat.scripts = stubScriptRepo{list: []models.CustomTool{{Name: short, Status: 1}}}

	res, err := NewResolver(cat, "t0", nil, false).Resolve("agent-joker")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	want := []string{tools.CustomToolPrefix + short}
	if !reflect.DeepEqual(res.FinalNames, want) {
		t.Fatalf("生效工具 = %v, want %v（自定义工具的 required 授权应恒注入）", res.FinalNames, want)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "已不存在") {
			t.Errorf("不该报「已不存在」（说明 AllTools 又漏了自定义工具）: %s", w)
		}
	}
}
