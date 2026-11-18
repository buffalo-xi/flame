package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	skillsrepo "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
)

// ===== 测试替身 =====
//
// 一律「嵌入接口 + 只覆写用到的方法」：未覆写的方法被调用即 panic，
// 将来实现新增仓库依赖时测试会立刻炸出来，而不是静默拿到零值
// （与 service/agent/toolgrant/resolve_test.go 的 stubGrants 同款）。

// stubGrantRepo 假授权仓库（agent 授权 + skill 工具依赖）。
// 「不限制工具」开关已归 agent 自身，不在这里（见 stubAgentsRepoForSwitch）。
type stubGrantRepo struct {
	toolgrants.Repository
	skillDeps map[string][]models.SkillToolGrant // skillID → 声明的工具依赖
	grants    []models.AgentToolGrant            // ListAgentGrants 的返回
	upserted  []models.AgentToolGrant            // 记录 UpsertAgentGrant 的入参
	removed   int64                              // DeleteAgentGrant 的返回值
}

func (s *stubGrantRepo) ListAgentGrants(_, _ string) ([]models.AgentToolGrant, error) {
	return s.grants, nil
}

func (s *stubGrantRepo) ListSkillGrants(_, skillID string) ([]models.SkillToolGrant, error) {
	return s.skillDeps[skillID], nil
}

func (s *stubGrantRepo) UpsertAgentGrant(_, _, _ string, g models.AgentToolGrant) error {
	s.upserted = append(s.upserted, g)
	return nil
}

func (s *stubGrantRepo) DeleteAgentGrant(_, _, _ string) (int64, error) { return s.removed, nil }

// stubAgentsRepoForSwitch 假 agent 仓库：只覆写「不限制工具」开关的写入。
type stubAgentsRepoForSwitch struct {
	agents.Repository
	agentID      string
	unrestricted bool
}

func (s *stubAgentsRepoForSwitch) SetUnrestricted(_ *auth.AuthInfo, agentID string, v bool) error {
	s.agentID, s.unrestricted = agentID, v
	return nil
}

// stubSkillRepo 假 skill 仓库（只覆写用到的方法）。
type stubSkillRepo struct {
	skillsrepo.Repository
	injected []models.Skill             // ListInjected：决定候选来源
	bindings []models.AgentSkillBinding // ListBindings：可见性白名单
}

func (s *stubSkillRepo) ListInjected(_, _ string) ([]models.Skill, error) { return s.injected, nil }

func (s *stubSkillRepo) ListBindings(_, _ string) ([]models.AgentSkillBinding, error) {
	return s.bindings, nil
}

// newGrantMgr 造一个只依赖桩仓库的 manager（当前身份固定为超级智能体）。
func newGrantMgr(gr *stubGrantRepo, sk *stubSkillRepo) *toolGrantManager {
	if gr == nil {
		gr = &stubGrantRepo{}
	}
	if sk == nil {
		sk = &stubSkillRepo{}
	}
	return &toolGrantManager{
		schema: "t0", agentID: "main-id", agentName: "main", userID: "u1",
		// 注释说的「身份固定为超级智能体」必须真的置上：否则第一道身份闸就全拒
		isSupervisor: true,
		repo:         gr, skills: sk,
		// 目标身份反查走桩：不给桩会真查库（单测没有 db.DB）。
		// 目标 "poet" 是普通 agent，catalog 也按普通身份算 —— 与线上一致。
		agentsRepo: stubAgentsRepoForSkill{inst: &models.Agent{Name: "poet"}},
	}
}

// ===== GrantAgentTool：kind 自动分配 / 显式指定 =====

// 目标已注入的 skill 声明了该依赖 → 写 allow（按需注入）。
func TestGrantAutoAllowWhenSkillDeclares(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:exec"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "data-crunching"}}}
	m := newGrantMgr(gr, sk)

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindAllow {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindAllow)
	}
	if !strings.Contains(reason, "data-crunching") {
		t.Errorf("理由里应点出是哪个 skill 撑着的：%q", reason)
	}
	if len(gr.upserted) != 1 {
		t.Fatalf("应写入 1 行，实际 %d", len(gr.upserted))
	}
	g := gr.upserted[0]
	if g.GrantKind != models.GrantKindAllow || g.Ref != "builtin:exec" || g.AgentID != "poet-id" {
		t.Errorf("写入内容不符：%+v", g)
	}
	if g.ItemType != models.GrantItemGroup {
		t.Errorf("builtin:exec 是类别组，item_type 应为 group，实际 %q", g.ItemType)
	}
}

// 没有 skill 声明该依赖 → 写 required（否则 allow 永不生效）。
func TestGrantAutoRequiredWithoutSkill(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindRequired)
	}
	if !strings.Contains(reason, "required") {
		t.Errorf("理由里应说明为何写 required：%q", reason)
	}
	if len(gr.upserted) != 1 || gr.upserted[0].ItemType != models.GrantItemTool {
		t.Errorf("单工具 ref 的 item_type 应为 tool：%+v", gr.upserted)
	}
}

// 匹配按「展开后的叶子工具」求交，而不是比较 ref 字符串：
// skill 声明类别组 builtin:file，授予单工具 builtin:read_file 也应命中 allow。
func TestGrantMatchIsExpandedNotStringEqual(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:file"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "fs-skill"}}}
	m := newGrantMgr(gr, sk)

	kind, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindAllow {
		t.Fatalf("类别组展开命中单工具时应写 allow，实际 %q", kind)
	}
}

// 已存在 allow，且此时不再有 skill 声明 → 重写应升级为 required（Upsert 覆盖）。
func TestGrantUpgradesIdleAllowToRequired(t *testing.T) {
	gr := &stubGrantRepo{grants: []models.AgentToolGrant{
		{AgentID: "poet-id", GrantKind: models.GrantKindAllow, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
	}}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "")
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("空转的 allow 重写后应升级为 required，实际 %q", kind)
	}
}

// 显式 required：即使有 skill 声明也钉成常驻，理由里点明「未降级为 allow」。
func TestGrantExplicitRequiredOverridesAuto(t *testing.T) {
	gr := &stubGrantRepo{skillDeps: map[string][]models.SkillToolGrant{
		"s1": {{ItemType: models.GrantItemGroup, Ref: "builtin:read_file"}},
	}}
	sk := &stubSkillRepo{injected: []models.Skill{{ID: "s1", Name: "fs-skill"}}}
	m := newGrantMgr(gr, sk)

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:read_file", models.GrantKindRequired)
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindRequired {
		t.Fatalf("显式 required 不应被自动判成 allow，实际 %q", kind)
	}
	if !strings.Contains(reason, "未降级") {
		t.Errorf("理由应说明钉住常驻、未降级：%q", reason)
	}
}

// 显式 allow 但没有声明者 → 拒绝且不落库（否则写成一行永不生效的死授权）。
func TestGrantExplicitAllowWithoutDeclarerIsRejected(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", models.GrantKindAllow); err == nil {
		t.Fatal("没有 skill 声明时显式写 allow 应被拒绝")
	}
	if len(gr.upserted) != 0 {
		t.Errorf("被拒绝的授权不应落库：%+v", gr.upserted)
	}
}

// deny 覆盖原有 required 行，并在理由里说清「覆盖了哪一行」（每个 ref 只留一行）。
func TestGrantDenyReplacesExistingKind(t *testing.T) {
	gr := &stubGrantRepo{grants: []models.AgentToolGrant{
		{AgentID: "poet-id", GrantKind: models.GrantKindRequired, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
	}}
	m := newGrantMgr(gr, &stubSkillRepo{})

	kind, reason, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", models.GrantKindDeny)
	if err != nil {
		t.Fatal(err)
	}
	if kind != models.GrantKindDeny {
		t.Fatalf("kind = %q, want %q", kind, models.GrantKindDeny)
	}
	if !strings.Contains(reason, models.GrantKindRequired) {
		t.Errorf("理由应回显被覆盖的旧 kind：%q", reason)
	}
	if len(gr.upserted) != 1 || gr.upserted[0].GrantKind != models.GrantKindDeny {
		t.Errorf("应写入一行 deny：%+v", gr.upserted)
	}
}

// 非法 kind → 拒绝，且不落库。
func TestGrantRejectsBadKind(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, &stubSkillRepo{})

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:exec", "maybe"); err == nil {
		t.Fatal("非法 kind 应被拒绝")
	}
	if len(gr.upserted) != 0 {
		t.Error("被拒绝的授权不应落库")
	}
}

// 「仅超级智能体可用」的工具不能授给非超级智能体：目录按目标身份过滤后展开为空 → 拒绝。
func TestGrantRejectsSuperRefForNonSupervisor(t *testing.T) {
	m := newGrantMgr(nil, nil)

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "builtin:create_agent", ""); err == nil {
		t.Fatal("super 类工具授给非超级智能体应被拒绝")
	}
}

// 非超级智能体调用（身份兜底）→ 拒绝。
func TestGrantRejectsNonSupervisorCaller(t *testing.T) {
	m := newGrantMgr(nil, nil)
	// 身份判据是属性（agents.is_supervisor），不再看名字 —— 降级身份得改这个字段
	m.isSupervisor = false
	m.agentName = "poet"

	if _, _, err := m.GrantAgentTool(context.Background(), "joker", "joker-id", "builtin:exec", ""); err == nil {
		t.Fatal("非超级智能体调用应被拒绝")
	}
}

// ref 语法非法 → 拒绝，且不落库。
func TestGrantRejectsBadRef(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, nil)

	if _, _, err := m.GrantAgentTool(context.Background(), "poet", "poet-id", "not-a-ref", ""); err == nil {
		t.Fatal("非法 ref 应被拒绝")
	}
	if len(gr.upserted) != 0 {
		t.Error("被拒绝的授权不应落库")
	}
}

// ===== RevokeAgentTool =====

// 按 ref 精确删除并回报行数（幂等：第二次即为 0）。
func TestRevokeReportsRemovedRows(t *testing.T) {
	gr := &stubGrantRepo{removed: 1}
	m := newGrantMgr(gr, nil)

	n, _, err := m.RevokeAgentTool(context.Background(), "poet-id", "builtin:exec")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}

	gr.removed = 0 // 第二次：已经没有这条授权
	n, _, err = m.RevokeAgentTool(context.Background(), "poet-id", "builtin:exec")
	if err != nil {
		t.Fatalf("幂等收回不应报错: %v", err)
	}
	if n != 0 {
		t.Fatalf("第二次 removed = %d, want 0", n)
	}
}

// 刻意不校验 ref 是否存在：源已消失的悬空授权必须还能收回。
func TestRevokeAllowsDanglingRef(t *testing.T) {
	gr := &stubGrantRepo{}
	m := newGrantMgr(gr, nil)

	if _, _, err := m.RevokeAgentTool(context.Background(), "poet-id", "mcp:gone:tool"); err != nil {
		t.Fatalf("收回悬空授权不应被 ref 校验挡住: %v", err)
	}
}

// ===== 不限制工具开关（agent 自身属性） =====

func TestSetUnrestricted(t *testing.T) {
	repo := &stubAgentsRepoForSwitch{}
	// 身份判据是属性（D30）：只改名字不会被当成非超级智能体
	m := &agentManager{schema: "t0", agentName: "main", userID: "u1", isSupervisor: true, repo: repo}

	if err := m.SetUnrestricted(context.Background(), "poet-id", true); err != nil {
		t.Fatal(err)
	}
	if !repo.unrestricted || repo.agentID != "poet-id" {
		t.Errorf("开关未按目标写入：agentID=%q unrestricted=%v", repo.agentID, repo.unrestricted)
	}

	m.isSupervisor = false
	m.agentName = "poet"
	if err := m.SetUnrestricted(context.Background(), "joker-id", true); err == nil {
		t.Error("非超级智能体设置开关应被拒绝")
	}
}

// ===== grantViews：生效状态（纯计算，不碰仓库） =====

func TestGrantViewsMarksEffectiveness(t *testing.T) {
	grants := []models.AgentToolGrant{
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemGroup, Ref: "builtin:exec"},
		{GrantKind: models.GrantKindAllow, ItemType: models.GrantItemTool, Ref: "builtin:read_file"},
		{GrantKind: models.GrantKindRequired, ItemType: models.GrantItemTool, Ref: "builtin:list_agents"},
		// deny 只能来自管理端（模型侧 grant_agent_tool 也可写，但同 ref 只留一行）
		{GrantKind: models.GrantKindDeny, ItemType: models.GrantItemTool, Ref: "builtin:write_file"},
	}
	// declaring 只把 read_file 归到某个技能名下：exec 没人声明（空转），list_agents 是 required
	expand := func(ref string) []string {
		if ref == "builtin:exec" {
			return []string{"builtin:exec"}
		}
		return []string{ref}
	}
	declaring := func(expanded []string) string {
		for _, r := range expanded {
			// write_file 也「有技能声明」：用来证明 deny 不会因此被误标成 pending
			if r == "builtin:read_file" || r == "builtin:write_file" {
				return "declaring-skill"
			}
		}
		return ""
	}
	views := grantViews(grants, expand, declaring)

	if len(views) != 4 {
		t.Fatalf("views = %d, want 4", len(views))
	}
	// 1) allow 没人声明：永不生效（空转）+ 补救建议，且不能算 pending
	if views[0].Effective || views[0].Pending || views[0].Note == "" {
		t.Errorf("空转的 allow 应标为未生效、非 pending，并给出 note：%+v", views[0])
	}
	// 2) allow 有技能声明：本轮未激活 → pending + via_skill（用哪个技能会亮）
	if views[1].Effective || !views[1].Pending || views[1].ViaSkill != "declaring-skill" {
		t.Errorf("有归属的 allow 应为 pending 并带 via_skill：%+v", views[1])
	}
	// 3) required 恒生效，与激活无关
	if !views[2].Effective || views[2].Pending {
		t.Errorf("required 应恒生效且非 pending：%+v", views[2])
	}
	// 4) deny：即使有技能声明也不能标成 pending —— 它是「明确禁止」，不是「等着生效」
	if views[3].Effective || views[3].Pending || !strings.Contains(views[3].Note, "拒绝") {
		t.Errorf("deny 应标为未生效、非 pending，并说明是显式拒绝：%+v", views[3])
	}
}

// ===== fillVisibility：read_skill 的可见性回填 =====

// 租户级 + 超级智能体：restricted 与白名单都给。
func TestFillVisibilityTenantForSupervisor(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{
		{AgentID: "a1", AgentName: "poet"},
		{AgentID: "a2", AgentName: "bard"},
	}}
	// 用例名就是「超级智能体」：身份判据要真的置上，否则白名单根本不会回填
	m := &skillManager{schema: "t0", agentName: "main", isSupervisor: true, repo: repo}

	s := &models.Skill{ID: "s1", Name: "report-format"} // AgentID 空 = 租户级
	s.Restricted = true
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if !d.Restricted {
		t.Error("租户级应回填 restricted")
	}
	if len(d.BoundAgents) != 2 || d.BoundAgents[0] != "poet" {
		t.Errorf("白名单应回填为 agent 名：%+v", d.BoundAgents)
	}
}

// 租户级 + 非超级智能体：只给 restricted，不给白名单（不暴露他人绑定）。
func TestFillVisibilityTenantForNonSupervisor(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{{AgentID: "a1", AgentName: "poet"}}}
	m := &skillManager{schema: "t0", agentName: "joker", repo: repo}

	s := &models.Skill{ID: "s1", Name: "report-format"}
	s.Restricted = true
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if !d.Restricted {
		t.Error("租户级应回填 restricted")
	}
	if d.BoundAgents != nil {
		t.Errorf("非超级智能体不应看到白名单：%+v", d.BoundAgents)
	}
}

// 私有 skill：restricted 不参与判定，两项都不填。
func TestFillVisibilityPrivateSkipped(t *testing.T) {
	repo := &stubSkillRepo{bindings: []models.AgentSkillBinding{{AgentID: "a1", AgentName: "poet"}}}
	m := &skillManager{schema: "t0", agentName: "main", repo: repo}

	s := &models.Skill{ID: "s1", Name: "my-skill"}
	s.AgentID = "a1" // 私有
	var d tools.SkillDetail
	m.fillVisibility(s, &d)

	if d.Restricted || d.BoundAgents != nil {
		t.Errorf("私有 skill 不应回填可见性：%+v", d)
	}
}
