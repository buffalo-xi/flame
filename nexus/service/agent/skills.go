package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versioning"
	"flame/tools/xsql"
)

// skillManager 实现 tools.SkillsManager：
// 元数据与归属写 skills 表，内容按归属写文件
//
//	租户级：tenants/{schema}/skills/{name}/SKILL.md
//	私有：  tenants/{schema}/agents/{agentName}/skills/{name}/SKILL.md
//
// agentID / agentName / userID 构造时固化，工具层无法覆盖（防越权）。
type skillManager struct {
	schema    string
	agentID   string // 当前 agent uuid（写 agent_id / 可见性过滤）
	agentName string // 当前 agent name（拼私有 skill 路径）
	userID    string // 当前用户 uuid（写 created_by/updated_by）
	storage   storage.Storage
	repo      skills.Repository
	agents    agents.Repository     // 定向定位 / 拼路径时反查归属 agent
	grants    toolgrants.Repository // skill 声明的工具依赖（skill_tool_grants）
	// discovery 是否允许发现未绑定的技能（agents.skill_discovery）：
	//   false（默认，严格）：可见范围 = 私有 ∪ 绑定（与 system prompt 注入清单一致）
	//   true               ：可见范围 = 私有 ∪ 绑定 ∪ 全员可见的租户级技能（渐进式披露）
	// 注意：system prompt 的注入清单**始终**只列 私有 ∪ 绑定，与此开关无关。
	discovery bool
	// isSupervisor 当前 agent 是否带超级权限（agents.is_supervisor），由构造时固化。
	isSupervisor bool
	// mode 读定义的模式：draft = 草稿（编辑 / 调试 / 试聊），current = 当前版本（正式运行）。
	// 只影响**读**：写永远落草稿（skillPath 不动）。
	mode tools.DefinitionMode

	// meta 正式运行的元数据读取器（描述 / 可见性 / 工具依赖一律取当前版本）。
	// 与 toolGrantManager 共用同一实现 —— 管理面与运行面各说各话，排查时最难辨。
	meta *skillMetaReader
}

// NewSkillManager 创建 SkillManager。
//
// mode 决定「读正文」读哪一份（写恒为草稿）。正式入口传 tools.ModeCurrent，
// 其余（编辑页、调试器、试聊、单测）留空即草稿。
func NewSkillManager(schema, agentID, agentName, userID string, st storage.Storage,
	discovery bool, isSupervisor bool, mode tools.DefinitionMode) tools.SkillsManager {
	agentRepo := agents.NewRepository()
	return &skillManager{
		schema:       schema,
		agentID:      agentID,
		agentName:    agentName,
		userID:       userID,
		storage:      st,
		repo:         skills.NewRepository(),
		agents:       agentRepo,
		grants:       toolgrants.NewRepository(),
		discovery:    discovery,
		isSupervisor: isSupervisor,
		mode:         mode,
		meta:         newSkillMetaReader(schema, agentID, agentName, agentRepo, mode),
	}
}

// skillMeta 元数据读取器。惰性构造是为了让测试里直接 new 的零值 skillManager
// 也能用（草稿口径，不碰版本表）。
func (m *skillManager) skillMeta() *skillMetaReader {
	if m.meta == nil {
		m.meta = newSkillMetaReader(m.schema, m.agentID, m.agentName, m.agents, m.mode)
	}
	return m.meta
}

// skillPath 由「归属 agent 名 + skill 名」拼内容文件路径（租户级传空 ownerName）。
//
// 与旧实现的关键区别：私有 skill 的目录名取自**归属者**，而不是当前 agent ——
// 超级智能体跨 agent 改别人的 skill 时，路径才落对地方。
func (m *skillManager) skillPath(ownerName, skillName string) string {
	if ownerName == "" {
		return tools.SkillPath(m.schema, skillName)
	}
	return tools.AgentSkillPath(m.schema, ownerName, skillName)
}

// readPath 读正文时用哪份内容：草稿 or 已发布的当前版本。
//
// 与 skillPath 分工：skillPath 恒为草稿（Create / Update / Delete 用），
// readPath 按运行模式解析（read_skill / use_skill 用）。
//
// 私有技能没有独立版本号：它的版本就是所属 agent 的版本号，路径落在
// versions/agents/<owner>/v<n>/skills/<skill>/SKILL.md。
func (m *skillManager) readPath(ownerName, skillName string) (string, error) {
	if !m.mode.IsCurrent() {
		return m.skillPath(ownerName, skillName), nil
	}
	if ownerName == "" {
		_, dir, err := versioning.TenantSkillCurrentVersion(m.schema, skillName)
		if err != nil {
			return "", err
		}
		return path.Join(dir, paths.SkillEntryFile), nil
	}
	_, dir, err := versioning.AgentCurrentVersion(m.schema, ownerName)
	if err != nil {
		return "", err
	}
	return path.Join(dir, paths.AgentSkillsSubdir, skillName, paths.SkillEntryFile), nil
}

// ownerName 反查归属 agent 名（实现见 skillmeta.go，与元数据读取共用同一套归属解析）。
func (m *skillManager) ownerName(ownerAgentID string) (string, error) {
	return ownerAgentName(m.schema, m.agentID, m.agentName, ownerAgentID, m.agents)
}

// checkOwnerAgent 归属 agent 必须存在、且处于正常态（判据 models.IsModifiable，
// 与文件闸门 / 管理端同口径）。ownerID 为空 = 租户级 skill，没有归属 agent 可查，免校验。
//
// 自身也**必须**校验。这里原先写作「自身状态由运行时保证」，但那不成立：
// 跳过自身就等于让废弃的 agent 继续改自己的私有 skill，且会与文件工具侧的闸门
// （gate.go 的 checkWriteOwn）得出相反结论。
//
// 注意这是「归属 agent」那道闸 —— 目标 skill 自身仍须是「正常」态。
func (m *skillManager) checkOwnerAgent(ownerID string) error {
	if ownerID == "" {
		return nil
	}
	inst, err := m.agents.GetAgent(&auth.AuthInfo{Schema: m.schema}, ownerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("skill owner agent %s not found", ownerID)
		}
		return err
	}
	if !inst.IsModifiable() {
		return fmt.Errorf("agent %s is deprecated; its private skills are frozen", inst.Name)
	}
	return nil
}

// prepareToolDeps 解析并校验工具依赖入参：
//   - in 为 nil（工具没传）→ (nil, nil)，调用方据此「不改动」；
//   - 非超级智能体一律拒绝 —— 依赖是 allow 授权的**生效触发器**，谁能写依赖谁就握住授权开关；
//   - 其余交给 buildToolDeps 做格式与存在性校验。
func (m *skillManager) prepareToolDeps(in *[]string) ([]models.SkillToolGrant, error) {
	if in == nil {
		return nil, nil
	}
	if !m.isSupervisor {
		return nil, errors.New("仅超级智能体可声明 skill 的工具依赖")
	}
	return m.buildToolDeps(*in)
}

// buildToolDeps 校验并规范化 skill 的工具依赖（纯数据处理，不落库）。
//
// 与管理端 admin/toolgrants.SetSkillToolGrants 同口径：
//   - allowGroup=false —— skill 侧**不允许引用工具组**（只能声明天然组或具体工具，
//     否则可借工具组间接提权），与 toolgrant.ValidateRef 的判断一致；
//   - 每个 ref 必须能展开出工具，否则拒绝 —— 防手滑写出永不生效的死 ref；
//   - 去重、忽略空白；item_type 由 ref 形态推导，调用方不用传。
func (m *skillManager) buildToolDeps(refs []string) ([]models.SkillToolGrant, error) {
	catalog := toolgrant.NewCatalog(m.schema, m.grants, true)
	cats := toolgrant.BuiltinCategories()
	out := make([]models.SkillToolGrant, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		if err := toolgrant.ValidateRef(ref, false); err != nil {
			return nil, fmt.Errorf("invalid tool ref %q: %w", ref, err)
		}
		list, err := catalog.ToolsOfRef(ref)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("tool ref %q not found or expands to no tools", ref)
		}
		itemType := models.GrantItemTool
		if p, perr := toolgrant.ParseRef(ref); perr == nil && p.IsGroup(cats) {
			itemType = models.GrantItemGroup
		}
		seen[ref] = true
		out = append(out, models.SkillToolGrant{ItemType: itemType, Ref: ref})
	}
	return out, nil
}

// saveDeps 工具依赖收尾：in 为 nil（未传）不动；非 nil 全量替换。
// deps 由 prepareToolDeps 预先校验，这里只负责落库。
func (m *skillManager) saveDeps(skillID string, in *[]string, deps []models.SkillToolGrant) error {
	if in == nil {
		return nil
	}
	return m.grants.ReplaceSkillGrants(m.schema, skillID, deps)
}

// depListRefs 取某 skill 当前声明的依赖 ref 列表（随写操作回给调用方：「改完是什么样」）。
func (m *skillManager) depListRefs(skillID string) ([]string, error) {
	list, err := m.grants.ListSkillGrants(m.schema, skillID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, g := range list {
		out = append(out, g.Ref)
	}
	return out, nil
}

// skillForDepWrite 依赖写入口的公共前置：supervisor-only + 定位 + 归属 agent 可写 + 自身正常态。
//
// 依赖是 allow 授权的**生效触发器**（谁能写依赖，谁就握住「什么时候给它工具」的开关），
// 所以与 prepareToolDeps 同口径只给超级智能体；写的又是技能定义，因此沿用定义编辑的两道状态闸。
func (m *skillManager) skillForDepWrite(ref tools.SkillRef) (*models.Skill, error) {
	if !m.isSupervisor {
		return nil, errors.New("仅超级智能体可声明 skill 的工具依赖")
	}
	s, err := m.findRef(ref)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("skill %q not found", ref.Name)
	}
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return nil, err
	}
	if !s.IsModifiable() {
		return nil, fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	if m.grants == nil {
		return nil, errors.New("tool grant repository not available")
	}
	return s, nil
}

// AddToolDep 给某个 skill 追加一条工具依赖（幂等），返回是否真的新增与变更后的完整依赖。
//
// 与 update_*_skill 的 tool_deps 参数（全量替换）互补：这里不必先读现状，
// 也不会因为漏传而静默删掉其它依赖。
func (m *skillManager) AddToolDep(_ context.Context, ref tools.SkillRef, toolRef string) (*tools.SkillDepChange, error) {
	s, err := m.skillForDepWrite(ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(toolRef) == "" {
		return nil, errors.New("tool ref is empty")
	}
	// 与创建/更新同口径：格式 + 可展开 + 不引用自定义组（见 buildToolDeps 说明）
	deps, err := m.buildToolDeps([]string{toolRef})
	if err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, errors.New("tool ref is empty")
	}
	added, err := m.grants.AddSkillGrant(m.schema, s.ID, deps[0])
	if err != nil {
		return nil, err
	}
	all, err := m.depListRefs(s.ID)
	if err != nil {
		return nil, err
	}
	return &tools.SkillDepChange{Skill: s.Name, SkillID: s.ID, Ref: deps[0].Ref, Added: added, ToolDeps: all}, nil
}

// RemoveToolDep 移除某个 skill 的一条工具依赖（幂等），返回删除行数与变更后的完整依赖。
//
// 只校验 ref 语法、不校验它还能否展开 —— 工具 / 连接被删后留下的悬空依赖必须能清掉
// （与 revoke_agent_tool 同一条考虑）。
func (m *skillManager) RemoveToolDep(_ context.Context, ref tools.SkillRef, toolRef string) (*tools.SkillDepChange, error) {
	s, err := m.skillForDepWrite(ref)
	if err != nil {
		return nil, err
	}
	toolRef = strings.TrimSpace(toolRef)
	if toolRef == "" {
		return nil, errors.New("tool ref is empty")
	}
	if err := toolgrant.ValidateRef(toolRef, false); err != nil {
		return nil, fmt.Errorf("invalid tool ref %q: %w", toolRef, err)
	}
	removed, err := m.grants.RemoveSkillGrant(m.schema, s.ID, toolRef)
	if err != nil {
		return nil, err
	}
	all, err := m.depListRefs(s.ID)
	if err != nil {
		return nil, err
	}
	return &tools.SkillDepChange{Skill: s.Name, SkillID: s.ID, Ref: toolRef, Removed: removed, ToolDeps: all}, nil
}

// findRef 按 SkillRef 定位 skill：
//
//	ID 非空     → 按 id（全局唯一）；无空间定向时属跨归属操作，仅超级智能体
//	Scope 非空  → 按「工具声明的空间」+ 名：
//	              agent=私有空间（归属见 OwnerID，空 = 自己）/ tenant=租户级共享
//	皆空        → 按当前 agent 可见口径（read_skill）
func (m *skillManager) findRef(ref tools.SkillRef) (*models.Skill, error) {
	if ref.ID != "" {
		// skills.id 是 uuid 列：把名字（或任何非 UUID）填进 skill_id，Postgres 会报 22P02
		// （invalid input syntax for type uuid）而不是「查无此行」，那句 SQL 报错对模型毫无
		// 指导意义。这里直接按形态拒掉，并把正确用法指回去（名字请用 name）。
		if !isUUID(ref.ID) {
			return nil, fmt.Errorf("skill_id %q 不是合法 id；按名字定位请用 name 参数", ref.ID)
		}
		// 无空间定向时按 id 是跨归属入口（read_skill 的 skill_id），仍仅超级智能体；
		// 有空间定向时（own / shared 工具）由 checkSkillScope 校验归属，人人可用。
		if ref.Scope == "" && !m.isSupervisor {
			return nil, errors.New("仅超级智能体可按 id 定向操作 skill")
		}
		return m.repo.Get(m.schema, ref.ID)
	}
	switch ref.Scope {
	case tools.SkillScopeTenant:
		return m.repo.FindByName(m.schema, ref.Name, "") // "" = 租户级
	case tools.SkillScopeAgent:
		target := m.agentID
		if ref.OwnerID != "" {
			// 指定归属（*_agent_skill 系列）：替别人定位它的私有技能，仅超级智能体
			if !m.isSupervisor {
				return nil, errors.New("只有超级智能体可为其它 agent 定位私有技能")
			}
			target = ref.OwnerID
		}
		if target == "" {
			return nil, errors.New("no agent context for private skill")
		}
		return m.repo.FindByName(m.schema, ref.Name, target)
	}
	return m.findByName(ref.Name)
}

// checkSkillScope 校验「工具声明的空间」与「目标 skill 的真实归属」是否一致。
func (m *skillManager) checkSkillScope(scope, targetOwnerID string, s *models.Skill) error {
	if s == nil {
		return nil
	}
	return checkSkillScope(m.isSupervisor, m.agentID, targetOwnerID, scope, s)
}

// checkSkillScope 纯函数：own 系列工具只能操作**自己的私有** skill，shared 系列只能操作
// **租户级共享** skill，agent_skill 系列（targetOwnerID 非空）只能操作**指定 agent 的私有** skill。
//
// 与工具分类（skill-own / skill-admin 等）正交，缺一不可：分类管"谁能用哪个工具"，这里管"工具只能动哪个空间的数据"。
// own 系列人人可用（且 discovery 打开时能"看见"共享 skill），若不校验归属，非超级智能体就能借
// 可见口径改到共享 skill；反过来超级智能体若把共享 skill 交给 own 工具，也在这里被纠回到对偶工具 ——
// 报错文案直接给出该用哪个工具，把模型的误选掰正。
//
//	scope 为空：无空间定向（read_skill），不校验；s 为 nil（未定位到）交由调用方报 not found。
func checkSkillScope(isSupervisor bool, selfAgentID, targetOwnerID, scope string, s *models.Skill) error {
	if scope == "" || s == nil {
		return nil
	}
	owner := string(s.AgentID)
	switch scope {
	case tools.SkillScopeTenant: // 共享空间：仅超级智能体
		if !isSupervisor {
			return errors.New("仅超级智能体可操作共享 skill")
		}
		if owner == "" {
			return nil
		}
		return errors.New("这是私有 skill，请改用 *_own_skill 系列工具")
	case tools.SkillScopeAgent: // 私有空间
		// 指定归属（*_agent_skill 系列）：仅超级智能体，且必须落在该归属的私有空间
		if targetOwnerID != "" {
			if !isSupervisor {
				return errors.New("只能操作自己的私有 skill")
			}
			if owner == "" {
				return errors.New("这是租户级共享 skill，请改用 *_shared_skill 系列工具")
			}
			if owner != targetOwnerID {
				return errors.New("skill 不属于指定的 agent，请确认归属")
			}
			return nil
		}
		if owner != "" && owner == selfAgentID {
			return nil
		}
		if isSupervisor {
			if owner == "" {
				return errors.New("这是租户级共享 skill，请改用 *_shared_skill 系列工具")
			}
			// 文案指向新工具：这条路从"只能改文件内容"升级为"可用元工具完整管理"
			return errors.New("这是其它 agent 的私有 skill；请改用 *_agent_skill 系列工具（仅超级智能体），或直接用 read_file + write_file / edit_file 改文件内容")
		}
		return errors.New("只能操作自己的私有 skill")
	}
	return fmt.Errorf("unknown skill scope %q", scope)
}

// Create 创建 skill：写内容文件 + 落库登记。
func (m *skillManager) Create(ctx context.Context, in tools.SkillInput) (string, error) {
	// 名称规则兜底：只允许字母数字横线，长度≤64
	if !tools.ValidName(in.Name) {
		return "", fmt.Errorf("invalid skill name %q (只允许字母数字横线，长度≤64)", in.Name)
	}
	scope := in.Scope
	if scope == "" {
		scope = tools.SkillScopeAgent
	}
	// 纵深防御：共享 skill 的创建权由工具分类（skill-admin）收口，这里再按身份兜一道 ——
	// 即使分类被误配，非超级智能体也建不出共享 skill。
	if scope == tools.SkillScopeTenant && !m.isSupervisor {
		return "", errors.New("仅超级智能体可创建共享 skill")
	}
	ownerID := m.agentID
	switch scope {
	case tools.SkillScopeTenant:
		ownerID = "" // NULL = 租户级
	case tools.SkillScopeAgent:
		if in.OwnerID != "" {
			// 指定归属：替别的 agent 建私有技能（仅超级智能体）；分类已收口，这里再按身份兜一道
			if !m.isSupervisor {
				return "", errors.New("只有超级智能体可为其它 agent 创建私有技能")
			}
			ownerID = in.OwnerID
		}
		if ownerID == "" {
			return "", errors.New("no agent context for private skill")
		}
	}
	// 归属 agent 必须存在且可编辑（跨归属时才真的查库）
	if err := m.checkOwnerAgent(ownerID); err != nil {
		return "", err
	}

	// 工具依赖：仅超级智能体可声明；先校验（失败则一个字节都不落），登记成功后再写
	deps, err := m.prepareToolDeps(in.ToolDeps)
	if err != nil {
		return "", err
	}

	// 同名检查：按**目标空间**查重 —— 私有与共享是两个独立名字空间，
	// 各自唯一即可（用可见口径查会把自己私有的同名误判成"已存在"）。
	exist, err := m.repo.FindByName(m.schema, in.Name, ownerID)
	if err != nil {
		return "", err
	}
	if exist != nil {
		return "", fmt.Errorf("skill %q already exists", in.Name)
	}

	// 配额检查：租户内 skill 数量上限（必须在写内容文件之前，否则会留孤儿文件）
	cnt, err := quota.CountSkills(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("skill", cnt, quota.Resolve(m.schema).MaxSkills); err != nil {
		return "", err
	}

	// 先写内容文件（按归属落不同位置）：目录名取自**归属者**，替别的 agent 建技能时路径才落对地方
	ownerName, err := m.ownerName(ownerID)
	if err != nil {
		return "", err
	}
	// 自愈历史残留：走到这里说明重名检查（FindByName）已过 —— 库里没有同名 skill，
	// 磁盘上若还有同名目录，必然是旧版本"删除只删 SKILL.md"留下的残骸。先清干净，
	// 否则 WriteFile 会复用旧目录，旧附属文件会"复活"蹭进新技能。
	if err := m.purgeStaleSkillDir(ctx, ownerName, in.Name); err != nil {
		return "", err
	}
	// 租户级技能的版本域也按 name 键：同名重建时不清，发布 v1 会撞上旧目录
	// （发布前置的 EnsureAbsent 会报 version exists，错误信息完全指不到这里）。
	// 私有技能没有独立版本域，不能在这里删（那是所属 agent 的历史快照）。
	if scope == tools.SkillScopeTenant {
		verKey := paths.VersionEntityKey(m.schema, paths.VersionSkills, in.Name)
		if verr := m.storage.DeletePrefix(ctx, verKey); verr != nil {
			return "", fmt.Errorf("清理同名技能的版本目录失败（%s）: %w", verKey, verr)
		}
	}
	if err := m.storage.WriteFile(ctx, m.skillPath(ownerName, in.Name), []byte(in.Content)); err != nil {
		return "", err
	}

	// 可见性：agent 建「租户级共享」skill 时默认收窄（企业级默认拒绝：不授权即不可见），
	// 并在建后自动授权创建者本身（否则连自己都用不了）。
	// 私有 skill 的 restricted 不参与判定（可见性由 agent_id 决定），保持 false。
	restricted := scope == tools.SkillScopeTenant

	id, err := m.repo.Create(m.schema, &models.Skill{
		Name:        in.Name,
		Description: xsql.String(in.Description),
		AgentID:     xsql.UUID(ownerID),
		Restricted:  xsql.Bool(restricted),
		CreatedBy:   xsql.UUID(m.userID),
		UpdatedBy:   xsql.UUID(m.userID),
	})
	if err != nil {
		return "", err
	}
	// 共享 skill：自动授权创建者，保证"建了就能用"
	if restricted && m.agentID != "" {
		if err := m.repo.SetVisibility(m.schema, id, true, []string{m.agentID}, m.userID); err != nil {
			return "", err
		}
	}
	// 工具依赖：登记后写入（校验已在前置步骤完成；此处失败只影响依赖本身）
	if err := m.saveDeps(id, in.ToolDeps, deps); err != nil {
		return "", err
	}
	return id, nil
}

// Update 更新 skill：可改内容、描述或名字（改名会同步迁移内容文件）。
// 目标空间由调用方（own / shared 工具）声明，定位与归属校验都以它为准。
func (m *skillManager) Update(ctx context.Context, in tools.SkillInput) error {
	// 定位：按工具声明的空间 + 名（或 skill_id）；无空间定向才回落到可见口径
	s, err := m.findRef(tools.SkillRef{ID: in.ID, Name: in.Name, Scope: in.Scope, OwnerID: in.OwnerID})
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("skill %q not found", in.Name)
	}
	// 空间校验：own 只能动自己的私有、shared 只能动租户级、agent_skill 只能动指定 agent 的私有
	if err := m.checkSkillScope(in.Scope, in.OwnerID, s); err != nil {
		return err
	}
	// 归属 agent 必须存在且可编辑（与文件闸门 / 管理端同口径）
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return err
	}
	// 只有正常态允许改定义（与 agent / flow 侧口径一致：models.IsModifiable）
	if !s.IsModifiable() {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	// 工具依赖：仅超级智能体可声明；先校验，最后统一落库（三条出口都要过 saveDeps）
	deps, err := m.prepareToolDeps(in.ToolDeps)
	if err != nil {
		return err
	}

	// 归属者名（决定内容文件落点）：自身直接用当前名，跨 agent 反查
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return err
	}
	oldPath := m.skillPath(ownerName, s.Name)
	oldName := s.Name // 改名后 s.Name 会被覆盖，版本目录搬迁要用旧名

	// ① 改名：校验 + 冲突检查 + 改表字段
	if in.NewName != "" && in.NewName != s.Name {
		// 内置受保护 skill：不允许改名（描述等其它字段可改）
		if models.IsProtectedSkill(s.Name) {
			return fmt.Errorf("protected skill %q cannot be renamed", s.Name)
		}
		if !tools.ValidName(in.NewName) {
			return fmt.Errorf("invalid skill name %q (只允许字母数字横线)", in.NewName)
		}
		// 改名查重：在**目标空间**内查（私有 / 共享各自唯一）；skill_id 定向时用目标归属
		var exist *models.Skill
		if in.ID != "" || in.Scope != "" {
			exist, err = m.repo.FindByName(m.schema, in.NewName, string(s.AgentID))
		} else {
			exist, err = m.repo.FindVisibleByName(m.schema, in.NewName, m.agentID)
		}
		if err != nil {
			return err
		}
		if exist != nil {
			return fmt.Errorf("skill %q already exists", in.NewName)
		}
		s.Name = in.NewName
	}

	if in.Description != "" {
		s.Description = xsql.String(in.Description)
	}
	s.UpdatedBy = xsql.UUID(m.userID)

	// 可见性开关只对租户级共享 skill 有意义：**先校验，再落库**。
	// 放到 repo.Update 之后会把「入参不合法」变成「元数据已改、才报错」，留下半写状态。
	if in.Restricted != nil && string(s.AgentID) != "" {
		return fmt.Errorf("restricted 只对租户级共享 skill 有意义：%q 是私有 skill（可见性由归属 agent 决定）", s.Name)
	}

	// ② 更新元数据（含 name）
	if err := m.repo.Update(m.schema, s); err != nil {
		return err
	}

	// ②' 可见性开关（仅租户级共享 skill，归属已在前置校验过）：只改 skills.restricted 一列。
	// 白名单由 bind_skill / unbind_skill 维护，这里**绝不能**用 SetVisibility 代劳 ——
	// 它会在事务里清空并重建 agent_skill_bindings，只改开关会把已有绑定全删掉。
	if in.Restricted != nil {
		if err := m.repo.SetRestricted(m.schema, s.ID, *in.Restricted, m.userID); err != nil {
			return err
		}
	}

	newPath := m.skillPath(ownerName, s.Name)

	// 只改元数据、没传内容、也没改名 → 文件不动，但工具依赖仍可能变
	if len(in.Content) == 0 && oldPath == newPath {
		return m.saveDeps(s.ID, in.ToolDeps, deps)
	}

	// ③ 改名：**整个技能目录**一起搬。
	//
	// 为什么不沿用「写新 SKILL.md + 删旧 SKILL.md」：技能目录现在可以放附属文件与子目录
	// （文件工具的 "/" 根寻址，见 tools/gate.go 的 checkWriteOwn），只搬入口文件会把
	// 附属资源留在旧目录变成孤儿。Rename 是目录级操作，local / nfs / s3 通用
	// （与 paths.RenameAgentDir 同源）。
	//
	// 搬不动时（目标目录已存在 / 跨设备等）退回「读旧 + 写新 + 删旧」，至少保住入口文件。
	renamed := oldPath != newPath
	if renamed {
		// 版本域先搬：版本目录按名字键，搬不动就**不动草稿** ——
		// 否则会出现「草稿已在新名、版本还挂旧名」，正式运行按新名拼路径直接落空。
		//   租户级：整棵 versions/skills/<old>（含全部历史版本）
		//   私有：versions/agents/<owner>/v*/skills/<old>（逐个版本目录）
		if ownerName == "" {
			verOld := paths.VersionEntityKey(m.schema, paths.VersionSkills, oldName)
			verNew := paths.VersionEntityKey(m.schema, paths.VersionSkills, s.Name)
			if rerr := m.storage.Rename(ctx, verOld, verNew); rerr != nil {
				return fmt.Errorf("搬技能版本目录失败（%s → %s）: %w", verOld, verNew, rerr)
			}
		} else if rerr := paths.RenamePrivateSkillDirs(m.storage, m.schema, ownerName, oldName, s.Name); rerr != nil {
			return rerr
		}
		if rerr := m.storage.Rename(ctx, path.Dir(oldPath), path.Dir(newPath)); rerr != nil {
			if data, rerr2 := m.storage.ReadFile(ctx, oldPath); rerr2 == nil {
				if werr := m.storage.WriteFile(ctx, newPath, data); werr != nil {
					return werr
				}
			}
			_ = m.storage.Delete(ctx, oldPath)
		}
	}

	// ④ 内容：不传则沿用原文件。改名时文件已随目录搬过去，无需重读重写。
	content := []byte(in.Content)
	if len(content) == 0 {
		if renamed {
			return m.saveDeps(s.ID, in.ToolDeps, deps)
		}
		data, rerr := m.storage.ReadFile(ctx, oldPath)
		if rerr != nil {
			// 原文件读不到：内容不动（保持旧行为不报错），但依赖仍要落
			return m.saveDeps(s.ID, in.ToolDeps, deps)
		}
		content = data
	}
	if err := m.storage.WriteFile(ctx, newPath, content); err != nil {
		return err
	}
	return m.saveDeps(s.ID, in.ToolDeps, deps)
}

// purgeStaleSkillDir 清掉「同名技能目录」的历史残留（目录不存在则无操作）。
//
// 调用时机是 Create 的重名检查（FindByName）已过之后：库里既无同名 skill，磁盘上若还有
// 同名目录，就只能是旧版本"删除只删 SKILL.md"留下的残骸。留着它，WriteFile 会复用旧目录，
// 旧附属文件会"复活"蹭进新技能。抽成独立方法是为了能在无 DB 的单元测试里直接覆盖
// （Create 本身要先查配额，跑不起纯内存测试）。
func (m *skillManager) purgeStaleSkillDir(ctx context.Context, ownerName, name string) error {
	dir := path.Dir(m.skillPath(ownerName, name))
	ok, err := m.storage.Exists(ctx, dir)
	if err != nil || !ok {
		// stat 失败不在这里下结论：没有残留就继续写，真有问题让 WriteFile 报错
		return nil
	}
	return m.storage.DeletePrefix(ctx, dir)
}

// Delete 删除 skill（库 + 文件）。
func (m *skillManager) Delete(ctx context.Context, ref tools.SkillRef) error {
	// 定位：按工具声明的空间 + 名（或 skill_id）；无空间定向才回落到可见口径
	s, err := m.findRef(ref)
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("skill %q not found", ref.Name)
	}
	// 空间校验：own 只能删自己的私有、shared 只能删租户级、agent_skill 只能删指定 agent 的私有
	if err := m.checkSkillScope(ref.Scope, ref.OwnerID, s); err != nil {
		return err
	}
	// 归属 agent 必须存在且可编辑（与文件闸门 / 管理端同口径）
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return err
	}
	// 内置受保护 skill 不允许删除
	if models.IsProtectedSkill(s.Name) {
		return fmt.Errorf("protected skill %q cannot be deleted", s.Name)
	}
	// 只有正常态允许删除
	if !s.IsModifiable() {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return err
	}
	if err := m.repo.Delete(m.schema, s.ID); err != nil {
		return err
	}
	// 删**整个技能目录**（含 SKILL.md 与附属文件 / 子目录），与管理端 admin/skills 同口径。
	// skillPath 指向的是入口文件 .../skills/{name}/SKILL.md，只删它会把附属文件与空目录留下；
	// 且删除后重建同名技能时，Create 的 WriteFile 会复用旧目录，旧附属文件会"复活"到新技能里。
	// DeletePrefix 目录不存在时返回 nil，天然幂等。
	if err := m.storage.DeletePrefix(ctx, path.Dir(m.skillPath(ownerName, s.Name))); err != nil {
		return err
	}
	// 租户级技能有独立版本域（按 name 键）：不清会让「删除后同名重建」在发布 v1 时
	// 撞上旧目录（EnsureAbsent 报 version exists）。私有技能的版本属于所属 agent 的快照，
	// 删草稿技能不该改动历史版本。
	if string(s.AgentID) == "" {
		if verr := m.storage.DeletePrefix(ctx, paths.VersionEntityKey(m.schema, paths.VersionSkills, s.Name)); verr != nil {
			return fmt.Errorf("清理技能版本目录失败: %w", verr)
		}
	}
	return nil
}

// SetAgentSkillDiscovery 设置某 agent 的「可发现未绑定的技能」开关（仅超级智能体）。
//
// true：该 agent 还能看到「全员可见」但未显式绑定的租户级技能（渐进式披露）；
// false：严格口径，只认私有 ∪ 已绑定。
func (m *skillManager) SetAgentSkillDiscovery(ctx context.Context, agentID string, discovery bool) error {
	if !m.isSupervisor {
		return errors.New("仅超级智能体可修改他 agent 的技能可见性")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	return m.repo.SetAgentSkillDiscovery(m.schema, agentID, discovery)
}

// resolveTenantSkill 按名解析一个**租户级** skill（agent_id IS NULL）。
// 绑定只认租户级 skill：私有 skill 天然只对归属 agent 可见，绑定无意义。
func (m *skillManager) resolveTenantSkill(name string) (*models.Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("skill name is empty")
	}
	s, err := m.repo.FindByName(m.schema, name, "") // "" = 租户级
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("skill %q not found（只支持租户级 skill）", name)
	}
	return s, nil
}

// BindAgentSkill 给某 agent 追加一条 skill 绑定（幂等：已绑定则无变化）。
// 仅超级智能体可用；目标 agent 的存在性与可改状态由工具层先判，这里再判一次身份兜底。
func (m *skillManager) BindAgentSkill(ctx context.Context, agentID, skillName string) error {
	if !m.isSupervisor {
		return errors.New("仅超级智能体可为他 agent 绑定 skill")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	s, err := m.resolveTenantSkill(skillName)
	if err != nil {
		return err
	}
	_, err = m.repo.AddBinding(m.schema, agentID, s.ID, m.userID)
	return err
}

// UnbindAgentSkill 移除某 agent 的一条 skill 绑定（幂等：未绑定则无变化）。仅超级智能体可用。
func (m *skillManager) UnbindAgentSkill(ctx context.Context, agentID, skillName string) error {
	if !m.isSupervisor {
		return errors.New("仅超级智能体可为他 agent 解绑 skill")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	s, err := m.resolveTenantSkill(skillName)
	if err != nil {
		return err
	}
	_, err = m.repo.RemoveBinding(m.schema, agentID, s.ID)
	return err
}

// ListAgentSkills 列出某 agent 当前绑定的 skill 名（只读）；agentID 为空返回空列表。
func (m *skillManager) ListAgentSkills(ctx context.Context, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	return m.repo.ListBoundSkillNames(m.schema, agentID)
}

// ListAgentPrivateSkills 列出某 agent（按 uuid）的私有 skill 名（只读）。
// 仅超级智能体可查他人 —— 别人的私有技能，非超级智能体连名字都不该看到。
func (m *skillManager) ListAgentPrivateSkills(ctx context.Context, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	if agentID != m.agentID && !m.isSupervisor {
		return nil, errors.New("仅超级智能体可查看他 agent 的私有技能")
	}
	return m.repo.ListPrivateNames(m.schema, agentID)
}

// List 列出当前 agent 可见的 skill（list_skills 工具用）：
//
//	超级智能体：自己私有 ∪ **全部租户级**（忽略 restricted / 白名单）——它是 skill 的管理者，
//	                  必须能枚举全量，否则会出现「能改却看不见」（update_shared_skill / read_skill(by id)
//	                  都能动，list_skills 却列不出）；并批量回填白名单供管理。
//	其余 agent：discovery=false（严格）→ 私有 ∪ 绑定；discovery=true → 再并入「全员可见」的租户级技能。
func (m *skillManager) List(ctx context.Context) ([]tools.SkillInfo, error) {
	if m.isSupervisor {
		skills, err := m.repo.ListForSupervisor(m.schema, m.agentID)
		if err != nil {
			return nil, err
		}
		infos := toSkillInfos(skills)
		m.fillBoundAgents(infos)
		return infos, nil
	}
	var (
		list []models.Skill
		err  error
	)
	if m.discovery {
		list, err = m.repo.ListVisible(m.schema, m.agentID)
	} else {
		list, err = m.repo.ListInjected(m.schema, m.agentID)
	}
	if err != nil {
		return nil, err
	}
	return toSkillInfos(list), nil
}

// fillBoundAgents 批量回填租户级 skill 的白名单 agent 名（仅超级智能体视角；私有 skill 跳过）。
// 一次查询取全部（避免逐条 N+1）；回填失败不影响主流程 —— 列表本身仍可用，白名单缺省为空。
func (m *skillManager) fillBoundAgents(infos []tools.SkillInfo) {
	ids := make([]string, 0, len(infos))
	for i := range infos {
		if infos[i].Scope == tools.SkillScopeTenant {
			ids = append(ids, infos[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	names, err := m.repo.ListBindingNamesBySkills(m.schema, ids)
	if err != nil {
		return
	}
	for i := range infos {
		if infos[i].Scope != tools.SkillScopeTenant {
			continue
		}
		if v := names[infos[i].ID]; len(v) > 0 {
			infos[i].BoundAgents = v
		}
	}
}

// ListInjected 列出「注入给当前 agent」的 skill（私有 + 已绑定）。
// 与 discovery 开关无关：system prompt 的常驻清单固定用这个口径。
func (m *skillManager) ListInjected(ctx context.Context) ([]tools.SkillInfo, error) {
	list, err := m.repo.ListInjected(m.schema, m.agentID)
	if err != nil {
		return nil, err
	}
	// 正式运行：描述 / 可见性 / 依赖按当前版本给（只冻正文不冻描述等于没冻）
	return toSkillInfos(m.skillMeta().runtimeMeta(list)), nil
}

// findByName 按当前「可见口径」查单条。
//
//	超级智能体：自己的私有优先，其次**任意租户级**（忽略白名单），与 List 的全可见口径对齐 ——
//	          保证「list 列得出 ⇒ read 按名读得到」，不再出现「能改却看不见」。
//	其余 agent：discovery=false 时未绑定的共享技能视为不存在。
func (m *skillManager) findByName(name string) (*models.Skill, error) {
	if m.isSupervisor {
		return m.repo.FindForSupervisorByName(m.schema, name, m.agentID)
	}
	if m.discovery {
		return m.repo.FindVisibleByName(m.schema, name, m.agentID)
	}
	return m.repo.FindInjectedByName(m.schema, name, m.agentID)
}

// toSkillInfos 批量转换（保持入参顺序）。
func toSkillInfos(list []models.Skill) []tools.SkillInfo {
	out := make([]tools.SkillInfo, 0, len(list))
	for _, s := range list {
		out = append(out, toSkillInfo(s))
	}
	return out
}

// Get 读取 skill 详情（含内容）。可见口径同 List（未绑定的共享技能在严格模式下读不到）。
func (m *skillManager) Get(ctx context.Context, ref tools.SkillRef) (*tools.SkillDetail, error) {
	s, err := m.findRef(ref)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return nil, err
	}
	// 正式运行：元数据与工具依赖取当前版本（正文也读版本目录，见 readPath）
	meta, hasMeta := m.skillMeta().versionMeta(s, ownerName)
	if hasMeta {
		applySkillMeta(s, meta)
	}
	var content []byte
	readKey, rerr := m.readPath(ownerName, s.Name)
	if rerr != nil {
		// 正式运行但技能没有已发布版本：正文拿不到，元数据仍返回。
		// 不退回草稿 —— 见 readPath 的注释。
		content = nil
	} else {
		content, err = m.storage.ReadFile(ctx, readKey)
		if err != nil {
			content = nil // 内容缺失不报错，元数据仍返回
		}
	}
	// 技能正文里的 ${global_vars.X} 同样展开：技能也是随资产包走的定义，
	// 里面写死的接口地址 / 表名正是要避免的东西。展开失败要报错 ——
	// 否则模型会照着 ${global_vars.X} 这个字面量去干活。
	body := string(content)
	if body != "" {
		expanded, xerr := globalvars.Expand(m.schema, body)
		if xerr != nil {
			return nil, fmt.Errorf("技能 %s 的 %s 展开全局变量失败: %w", s.Name, paths.SkillEntryFile, xerr)
		}
		body = expanded
	}
	detail := &tools.SkillDetail{
		SkillInfo: toSkillInfo(*s),
		Content:   body,
		ToolDeps:  []string{}, // 恒为数组：无依赖时必须序列化成 []，不能是 null
	}
	if hasMeta {
		// 依赖是声明，属于定义（能不能真调到仍取当前授权）
		if deps := meta.ToolRefs(); len(deps) > 0 {
			detail.ToolDeps = deps
		}
	} else {
		m.fillToolDeps(s, detail)
	}
	m.fillVisibility(s, detail)
	return detail, nil
}

// fillToolDeps 回填该 skill 声明的工具依赖（read_skill / use_skill 展示用）。
//
//   - 原样返回声明的 ref（不解组、不剔除悬空项）：展示的是定义原貌，组展开属 Resolver 的活；
//     悬空 ref 留着反而利于诊断（revoke_skill_tool 仍能清掉它）。
//   - 读失败只降级为空数组：依赖读不到不该让正文也拿不到（与「内容缺失不报错」同口径）。
//
// 不区分身份：依赖是技能定义的一部分，非超级智能体也照常给 —— 它回答的正是「我这个 agent
// 下一轮会多出什么」，而激活结果恒被自己那几条 allow 收窄，看到也点不亮授权外的东西。
func (m *skillManager) fillToolDeps(s *models.Skill, detail *tools.SkillDetail) {
	if m.grants == nil {
		return
	}
	refs, err := m.depListRefs(s.ID)
	if err != nil {
		return
	}
	detail.ToolDeps = refs // depListRefs 已保证非 nil（无依赖 = 空切片）
}

// fillVisibility 回填可见性现状（read_skill 展示用）：
//   - Restricted 仅对租户级共享 skill 有意义（私有 skill 的可见性由归属 agent 决定）；
//   - BoundAgents = 白名单（agent_skill_bindings 的 skill 侧视角），仅超级智能体回填。
func (m *skillManager) fillVisibility(s *models.Skill, detail *tools.SkillDetail) {
	if string(s.AgentID) != "" {
		return
	}
	detail.Restricted = bool(s.Restricted)
	if !m.isSupervisor {
		return
	}
	bindings, err := m.repo.ListBindings(m.schema, s.ID)
	if err != nil {
		return
	}
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names = append(names, string(b.AgentName))
	}
	detail.BoundAgents = names
}

// FindForWrite 按「归属 + 名」查 skill（不受当前 agent 可见性收口），供跨域写闸门判定存在性。
// ownerAgentID 为空表示租户级；不存在返回 (nil, nil)。用全状态的 FindByName（与重名检查同款）。
func (m *skillManager) FindForWrite(_ context.Context, ownerAgentID, skillName string) (*tools.SkillInfo, error) {
	s, err := m.repo.FindByName(m.schema, skillName, ownerAgentID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	info := toSkillInfo(*s)
	return &info, nil
}

// toSkillInfo 转换并推导 scope（agent_id 为空即租户级）；
// restricted 只对租户级共享 skill 有意义（私有 skill 的可见性由归属 agent 决定，恒为 false）。
func toSkillInfo(s models.Skill) tools.SkillInfo {
	scope := tools.SkillScopeAgent
	restricted := false
	if s.AgentID == "" {
		scope = tools.SkillScopeTenant
		restricted = bool(s.Restricted)
	}
	return tools.SkillInfo{
		ID:          s.ID,
		Name:        s.Name,
		Description: string(s.Description),
		Scope:       scope,
		Status:      int(s.Status),
		Restricted:  restricted,
	}
}
