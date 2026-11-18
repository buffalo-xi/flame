package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
)

// toolGrantManager 实现 tools.ToolGrantsManager：把「给某个 agent 一条工具授权」
// 拆成三步 —— ref 校验（toolgrant.Catalog）、allow / required 自动分配、生效状态回显。
//
// 依赖方向：toolgrant 包 import 了 tools 包（用它取内置工具与类别，见 catalog.go），
// 所以校验与分配逻辑只能落在这一层，tools 包只暴露接口与只读视图
// —— 与 skillManager.buildToolDeps 同一考虑。
//
// 口径与运行时**同源**：候选只来自「本轮已激活」的技能（agentImpl 用 Context.LoadedSkills 喂
// Resolver），绑定关系在这里只用于回答「读完哪个技能它会亮」—— 生效判定不再由本层复算。
type toolGrantManager struct {
	schema       string
	agentID      string // 当前 agent uuid（写 updated_by）
	agentName    string // 当前 agent 名
	isSupervisor bool   // 身份判据（agents.is_supervisor）
	userID       string
	repo         toolgrants.Repository
	skills       skills.Repository
	// agentsRepo 反查目标 agent 身份（targetIsSupervisor 用）。单测注入桩，运行时用真仓库。
	agentsRepo agents.Repository
	// mode 读定义的模式：决定「技能声明了哪些依赖」按哪一份算（见 skillDeps）。
	mode tools.DefinitionMode
	// meta 与 skillManager 共用的版本元数据读取器：管理面与运行面必须同源。
	meta *skillMetaReader
}

// NewToolGrantManager 创建 tools.ToolGrantsManager（租户与当前 agent 身份固化）。
//
// mode 与 skillManager 传同一个值：正式运行下「技能声明了哪些依赖」取当前版本，
// 否则 grant_agent_tool 会自动判出一个运行期永远不亮的 allow（草稿里有、版本里没有）。
func NewToolGrantManager(schema, agentID, agentName, userID string, isSupervisor bool, mode tools.DefinitionMode) tools.ToolGrantsManager {
	return &toolGrantManager{
		schema:       schema,
		agentID:      agentID,
		agentName:    agentName,
		isSupervisor: isSupervisor,
		userID:       userID,
		repo:         toolgrants.NewRepository(),
		skills:       skills.NewRepository(),
		agentsRepo:   agents.NewRepository(),
		mode:         mode,
		meta:         newSkillMetaReader(schema, agentID, agentName, agents.NewRepository(), mode),
	}
}

// agentRepo 归属 agent 仓库。惰性构造：测试里直接 new 的零值 manager 不会因为漏注入而 panic，
// 需要桩的用例显式填 agentsRepo 即可。
func (m *toolGrantManager) agentRepo() agents.Repository {
	if m.agentsRepo == nil {
		m.agentsRepo = agents.NewRepository()
	}
	return m.agentsRepo
}

// metaReader 版本元数据读取器。惰性构造：测试里直接 new 的零值 manager 走草稿口径。
func (m *toolGrantManager) metaReader() *skillMetaReader {
	if m.meta == nil {
		m.meta = newSkillMetaReader(m.schema, m.agentID, m.agentName, agents.NewRepository(), m.mode)
	}
	return m.meta
}

// skillDeps 取某 skill 声明的工具依赖，口径与运行面一致：
// 正式运行读当前版本的快照（冻结的声明）；草稿 / 无版本信息才回落 skill_tool_grants。
func (m *toolGrantManager) skillDeps(s models.Skill) ([]string, error) {
	if refs, ok := m.metaReader().depsOf(s); ok {
		return refs, nil
	}
	list, err := m.repo.ListSkillGrants(m.schema, s.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(list))
	for _, g := range list {
		refs = append(refs, g.Ref)
	}
	return refs, nil
}

// injectedSkills 列出「注入给目标 agent」的 skill（私有 + 已绑定）。
// 用途已收窄为「判断某条 allow 由哪个技能撑着」——不再等于「已生效」。
func (m *toolGrantManager) injectedSkills(agentID string) ([]models.Skill, error) {
	return m.skills.ListInjected(m.schema, agentID)
}

// GrantAgentTool 给目标 agent 赋一条工具授权。
//
// kind 表示「这条授权怎么生效」，不是「有多严格」：
//
//	""       自动（默认）：有已注入 skill 声明该依赖 → allow，否则 required。
//	         模型看不到 skill_tool_grants，猜错的代价是「写了 allow 却永不生效」，
//	         所以不指定时仍由系统按 Resolver 同一套口径算。
//	required 恒注入：即使有 skill 声明也钉成常驻 —— 用于「这个能力不许降级成按需」。
//	allow    按需：必须有已注入 skill 声明它，否则拒绝写入（否则该行永不生效）。
//	deny     显式拒绝：覆盖该 ref 原有的 required / allow 行（每个 ref 只留一行），
//	         即使技能声明也会被减掉 —— 按需口径下的「刹车」。
//
// targetIsSupervisor 目标 agent 是否带超权属性（可能不同于调用者）。
//
// 目录视角必须按**目标**身份算：给普通 agent 授 super 工具只会造出一条执行不了的
// 死 ref（执行层 IsSupervisorOnlyTool 会拒），露出来等于诱导一次必然失败的授权。
// agentID 为空时回落到调用者身份；查不到按 false 处理（收紧默认）。
func (m *toolGrantManager) targetIsSupervisor(agentID string) bool {
	if agentID == "" {
		return m.isSupervisor
	}
	inst, err := m.agentRepo().GetAgent(&auth.AuthInfo{Schema: m.schema, ID: m.userID}, agentID)
	if err != nil || inst == nil {
		return false
	}
	return inst.IsSupervisor
}

func (m *toolGrantManager) GrantAgentTool(_ context.Context, agentName, agentID, ref, kind string) (string, string, error) {
	// 身份兜底：工具层已过 supervisor-only 分类的执行层硬闸，这里再判一次（与 BindAgentSkill 同风格）
	if !m.isSupervisor {
		return "", "", errors.New("仅超级智能体可修改 agent 的工具授权")
	}
	ref = strings.TrimSpace(ref)
	// agent 侧允许引用工具组（allowGroup=true），与管理端 SetAgentToolGrants 同口径
	if err := toolgrant.ValidateRef(ref, true); err != nil {
		return "", "", fmt.Errorf("invalid tool ref %q: %w", ref, err)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "", models.GrantKindRequired, models.GrantKindAllow, models.GrantKindDeny:
	default:
		return "", "", fmt.Errorf("invalid grant kind %q：可选 required / allow / deny，或省略交给系统判定", kind)
	}
	// 视角按目标身份：目标非 supervisor 时 builtin:super 整组不产出 → 「仅 supervisor 可用」的工具拒收，
	// 避免造出「授权了也执行不了」的死 ref（执行层 IsSupervisorOnlyTool 会拒绝非 supervisor 执行）。
	cat := toolgrant.NewCatalog(m.schema, m.repo, m.targetIsSupervisor(agentID))
	want, err := cat.ToolsOfRef(ref)
	if err != nil {
		return "", "", err
	}
	if len(want) == 0 {
		return "", "", fmt.Errorf("tool ref %q not found or expands to no tools", ref)
	}

	declaring, err := m.declaringSkill(cat, agentID, want)
	if err != nil {
		return "", "", err
	}

	// 显式指定时先取旧行：Upsert 按 ref 覆盖，调用方需要知道哪一行被替换了。
	oldKind := ""
	if kind != "" {
		if oldKind, err = m.currentKind(agentID, ref); err != nil {
			return "", "", err
		}
	}

	auto := kind == ""
	if auto {
		if declaring != "" {
			kind = models.GrantKindAllow
		} else {
			kind = models.GrantKindRequired
		}
	}
	// 显式 allow 必须真的能被点亮：没有声明者就是一行永不生效的死授权。
	if !auto && kind == models.GrantKindAllow && declaring == "" {
		return "", "", fmt.Errorf(
			"写 allow 不会生效：目标 %q 目前没有任何已注入的 skill 声明 %q 的工具依赖；"+
				"请改用 required（恒注入），或先给它的技能补上该依赖", agentName, ref)
	}

	if err := m.repo.UpsertAgentGrant(m.schema, agentID, m.userID, models.AgentToolGrant{
		AgentID:   agentID,
		GrantKind: kind,
		ItemType:  toolgrant.ItemTypeOfRef(ref),
		Ref:       ref,
	}); err != nil {
		return "", "", err
	}
	return kind, grantReason(kind, declaring, oldKind, auto), nil
}

// grantReason 生成「实际写了什么、为什么」的说明 —— 会原样回给模型。
func grantReason(kind, declaring, oldKind string, auto bool) string {
	var b strings.Builder
	switch kind {
	case models.GrantKindAllow:
		if auto {
			fmt.Fprintf(&b, "已按 allow 写入：目标已注入的 skill %q 声明了该工具依赖，将按需注入；"+
				"若解绑该 skill，本授权会失效（届时可用 grant_agent_tool 重写以升级为 required）", declaring)
		} else {
			fmt.Fprintf(&b, "已按 allow 写入：skill %q 声明了该工具依赖，只有在 use_skill 激活它之后才注入", declaring)
		}
	case models.GrantKindRequired:
		if declaring != "" {
			fmt.Fprintf(&b, "已按 required 写入：恒注入常驻（未降级为 allow，尽管 skill %q 声明了它）", declaring)
		} else {
			b.WriteString("已按 required 写入：恒注入常驻")
		}
		if auto {
			b.WriteString("（当前没有已注入的 skill 声明该依赖，写 allow 不会生效）")
		}
	case models.GrantKindDeny:
		b.WriteString("已写入 deny：该 ref 不会注入，即使某个技能声明了它")
	}
	if oldKind != "" && oldKind != kind {
		fmt.Fprintf(&b, "；该 ref 原有的一行是 %s，已被覆盖（每个 ref 只保留一行）", oldKind)
	}
	return b.String()
}

// declaringSkill 判断「目标已注入的 skill 里，有没有声明了 want 中任一工具依赖的」，
// 返回该 skill 名（供「靠谁点亮」的说明用）。
//
// 匹配口径与 Resolver.Resolve 完全一致：两边都**展开成叶子工具 ref** 后求交，
// 而不是比较 ref 字符串 —— 否则会出现「系统以为 allow 会生效、实际交集为空」。
func (m *toolGrantManager) declaringSkill(cat *toolgrant.Catalog, agentID string, want []toolgrant.ToolInfo) (string, error) {
	wantSet := map[string]bool{}
	for _, t := range want {
		wantSet[t.Ref] = true
	}
	list, err := m.injectedSkills(agentID)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		name, err := m.skillDeclaring(cat, s, wantSet)
		if err != nil {
			return "", err
		}
		if name != "" {
			return name, nil
		}
	}
	return "", nil
}

// currentKind 取某 ref 现有的 grant_kind（不存在返回空串）。只用于回显「覆盖了哪一行」。
func (m *toolGrantManager) currentKind(agentID, ref string) (string, error) {
	grants, err := m.repo.ListAgentGrants(m.schema, agentID)
	if err != nil {
		return "", err
	}
	for _, g := range grants {
		if g.Ref == ref {
			return g.GrantKind, nil
		}
	}
	return "", nil
}

// skillDeclaring 判断某个 skill 声明的工具依赖是否与 wantSet（叶子工具 ref）有交集；
// 有则返回该 skill 名（用于告知「靠谁点亮」），否则返回空串。
func (m *toolGrantManager) skillDeclaring(cat *toolgrant.Catalog, s models.Skill, wantSet map[string]bool) (string, error) {
	deps, err := m.skillDeps(s)
	if err != nil {
		return "", err
	}
	for _, ref := range deps {
		sub, err := cat.ToolsOfRef(ref)
		if err != nil {
			return "", err
		}
		for _, t := range sub {
			if wantSet[t.Ref] {
				return s.Name, nil
			}
		}
	}
	return "", nil
}

// RevokeAgentTool 移除某 agent 的一条授权（按 ref，幂等），返回删除行数与被删行的 kind。
//
// 回显 kind 是为了审计：同一个 ref 只有一行，但那一行可能是 required / allow / deny，
// 「收回了什么」和「收回了一条 allow」在语义上差很远。
func (m *toolGrantManager) RevokeAgentTool(_ context.Context, agentID, ref string) (int64, string, error) {
	if !m.isSupervisor {
		return 0, "", errors.New("仅超级智能体可修改 agent 的工具授权")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, "", errors.New("tool ref is empty")
	}
	// 不校验 ref 是否仍然存在：源已消失（连接 / 端点被删）的悬空授权必须还能收回
	oldKind, err := m.currentKind(agentID, ref)
	if err != nil {
		return 0, "", err
	}
	removed, err := m.repo.DeleteAgentGrant(m.schema, agentID, ref)
	if err != nil {
		return 0, "", err
	}
	return removed, oldKind, nil
}

// ListAgentGrants 列出目标 agent 的授权项及其当前生效状态（供 read_agent 展示现状）。
//
// 三档口径，以「run 起点」为准：
//
//	required → 恒生效；
//	allow    → 绑定/私有 skill 声明了它，但**尚未激活**（pending）：模型 use_skill 之后才亮；
//	allow    → 没有任何 skill 声明它：永不生效（空转），给补救建议。
//
// 绑定关系只用于回答「读完哪个技能它会亮」，不再等于「已生效」——
// 生效时点是 use_skill，本函数无法预知，故一律标 pending 而不是 effective。
func (m *toolGrantManager) ListAgentGrants(_ context.Context, agentName, agentID string) ([]tools.AgentGrantView, error) {
	grants, err := m.repo.ListAgentGrants(m.schema, agentID)
	if err != nil {
		return nil, err
	}
	cat := toolgrant.NewCatalog(m.schema, m.repo, m.targetIsSupervisor(agentID))
	list, err := m.injectedSkills(agentID)
	if err != nil {
		return nil, err
	}
	expand := func(ref string) []string {
		sub, eerr := cat.ToolsOfRef(ref)
		if eerr != nil {
			return nil
		}
		out := make([]string, 0, len(sub))
		for _, t := range sub {
			out = append(out, t.Ref)
		}
		return out
	}
	declaring := func(expanded []string) string {
		want := map[string]bool{}
		for _, r := range expanded {
			want[r] = true
		}
		for _, s := range list {
			if name, serr := m.skillDeclaring(cat, s, want); serr == nil && name != "" {
				return name
			}
		}
		return ""
	}
	return grantViews(grants, expand, declaring), nil
}

// grantViews 纯计算：授权项 → 只读视图（不碰仓库，便于单测）。
//
//	required → 恒生效（与激活无关）；
//	deny     → 显式拒绝：技能声明也不注入（对 required 无效；同一 ref 只留一行，管理端禁止并存）；
//	allow + 有 skill 声明它 → pending：未生效，use_skill 激活该技能后才亮（via_skill 指向它）；
//	allow + 没人声明       → 空转：永不生效，给 note 与补救建议。
func grantViews(grants []models.AgentToolGrant,
	expand func(ref string) []string, declaring func(expanded []string) string) []tools.AgentGrantView {
	out := make([]tools.AgentGrantView, 0, len(grants))
	for _, g := range grants {
		v := tools.AgentGrantView{Kind: g.GrantKind, Ref: g.Ref, ItemType: g.ItemType}
		switch {
		case g.GrantKind == models.GrantKindRequired:
			v.Effective = true
		case g.GrantKind == models.GrantKindDeny:
			// deny 只减候选：不能标成 pending / 空转，否则会把「明确禁止」说成「等着生效」。
			v.Note = "显式拒绝：即使某个技能声明该依赖也不会注入"
		case declaring != nil:
			if name := declaring(expand(g.Ref)); name != "" {
				v.Pending = true
				v.ViaSkill = name
				v.Note = "已绑定/私有技能声明了它，但本轮尚未激活：use_skill " + name + " 之后生效"
				break
			}
			v.Note = "allow 未生效：没有任何已绑定或私有的 skill 声明该依赖；" +
				"可用 grant_agent_tool 重写该 ref（会自动升级为 required）"
		default:
			v.Note = "allow 未生效：没有任何已绑定或私有的 skill 声明该依赖"
		}
		out = append(out, v)
	}
	return out
}
