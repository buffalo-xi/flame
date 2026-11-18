package repository

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL实现的智能体存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// agentColumns 智能体查询列。
// updated_by_name 为关联查询结果（非 DB 列），用于展示"更新人"姓名。
// 注意：列顺序必须与 scanAgent 的 Scan 参数顺序严格一致。
func agentColumns(schema string) string {
	return "id, name, description, config, status, endpoints, tools_unrestricted, skill_discovery, is_supervisor, " +
		"created_by, updated_by, created_at, updated_at, " +
		"(SELECT name FROM " + schema + ".users WHERE id = agents.updated_by) as updated_by_name"
}

// scanAgent 将一行扫描到 Agent
func scanAgent(scanner interface{ Scan(...interface{}) error }, m *models.Agent) error {
	return scanner.Scan(
		&m.ID, &m.Name, &m.Description,
		&m.Config, &m.Status, &m.Endpoints,
		&m.ToolsUnrestricted, &m.SkillDiscovery, &m.IsSupervisor,
		&m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt,
		&m.UpdatedByName,
	)
}

// ListAgents 获取所有智能体
func (r *PostgresRepository) ListAgents(authInfo *auth.AuthInfo, req *ListAgentsRequest) ([]models.Agent, int, error) {
	// 构建查询条件
	whereClause := ""
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		whereClause += " WHERE name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	// 计算总数
	countQuery := "SELECT COUNT(*) FROM " + authInfo.Schema + ".agents" + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// 处理分页
	page := req.Page
	if page < 1 {
		page = 1
	}

	// limit 语义：负数 = 取全部（管理界面用，不拼 LIMIT/OFFSET）；否则 <1 时用默认值
	all := req.Limit < 0
	limit := req.Limit
	if !all && limit < 1 {
		limit = 10
	}

	offset := (page - 1) * limit

	// 处理排序
	sortClauses := []string{}
	if len(req.SortBy) == 0 {
		sortClauses = append(sortClauses, "name asc")
	} else {
		for i, sortBy := range req.SortBy {
			sortOrder := "asc"
			if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
				sortOrder = req.SortOrder[i]
			}
			sortClauses = append(sortClauses, sortBy+" "+sortOrder)
		}
	}

	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// 构建查询语句
	query := "SELECT " + agentColumns(authInfo.Schema) + " FROM " + authInfo.Schema + ".agents" + whereClause + " " + sortClause
	if !all {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// 执行查询
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []models.Agent
	for rows.Next() {
		var instance models.Agent
		if err := scanAgent(rows, &instance); err != nil {
			return nil, 0, err
		}
		results = append(results, instance)
	}

	return results, total, nil
}

// GetAgent 根据ID获取智能体
func (r *PostgresRepository) GetAgent(authInfo *auth.AuthInfo, id string) (*models.Agent, error) {
	var instance models.Agent
	// 复用 scanAgent，保证与 agentColumns 的列顺序永不漂移
	if err := scanAgent(db.DB.QueryRow(`SELECT `+agentColumns(authInfo.Schema)+` FROM `+authInfo.Schema+`.agents WHERE id = $1`, id), &instance); err != nil {
		return nil, err
	}

	return &instance, nil
}

// CreateAgent 创建智能体
func (r *PostgresRepository) CreateAgent(authInfo *auth.AuthInfo, agent *models.Agent, creatorAgentID string) (string, error) {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// 默认值
	if agent.Status == 0 {
		agent.Status = xsql.Int(models.StatusNormal)
	}
	if agent.Config.IsNil() {
		agent.Config = xsql.JSONB{Data: map[string]any{}}
	}
	if agent.Endpoints.IsNil() {
		agent.Endpoints = xsql.JSONB{Data: []any{}}
	}
	// supervisor 起步姿态：与下面的 core 组物化是**一套** —— 组负责常驻能力，
	// 这两个开关负责「候选不被 allow 收窄」（不限制工具）和「看得见未绑定的共享技能」。
	// 缺任何一个，新 supervisor 就只剩常驻的 6 个组，管理面等于缺一条腿。
	if agent.IsSupervisor {
		agent.ToolsUnrestricted = true
		agent.SkillDiscovery = true
	}

	// 设置创建时间和更新时间
	currentTime := time.Now()
	agent.CreatedAt = xsql.Time(currentTime)
	agent.UpdatedAt = xsql.Time(currentTime)
	agent.CreatedBy = xsql.UUID(authInfo.ID)
	agent.UpdatedBy = xsql.UUID(authInfo.ID)

	// Go 后端生成 UUID 作为主键 id（agents.id 无 DB 默认值）
	id := uuid.NewString()
	_, err = tx.Exec(`INSERT INTO `+authInfo.Schema+`.agents (id, name, description, config, status, endpoints, tools_unrestricted, skill_discovery, is_supervisor, created_by, updated_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, agent.Name, agent.Description,
		agent.Config, agent.Status, agent.Endpoints,
		agent.ToolsUnrestricted, agent.SkillDiscovery, agent.IsSupervisor,
		agent.CreatedBy, agent.UpdatedBy, agent.CreatedAt, agent.UpdatedAt,
	)
	if err != nil {
		return "", err
	}

	// 缺省授权：创建者自动获得对新 agent 的调用权（同一事务，保证原子）
	if creatorAgentID != "" {
		if _, err := tx.Exec(`INSERT INTO `+authInfo.Schema+`.agent_relations (caller_id, callee_id, created_by)
			VALUES ($1, $2, $3) ON CONFLICT (caller_id, callee_id) DO NOTHING`,
			creatorAgentID, id, xsql.UUID(authInfo.ID)); err != nil {
			return "", err
		}
	}

	// 缺省工具授权：普通 agent 获得「只读 + 工作区读写 + 协作」（见 models.DefaultAgentTools）；
	// supervisor 获得 core 集（见 models.SupervisorCoreGrants）—— 与普通 agent 走同一条物化路径，
	// 差别只在种子清单。这是「supervisor 的常驻工具」唯一真源：过去它由代码全开兜底，
	// 库里查不到、也没法按租户调整（见 docs/workspace-layout-and-supervisor-power.md §2.4）。
	// 用 required（恒注入）：allow 会被 skill 依赖收窄（resolve.go 的 candidates ∩ allow），
	// 会让工具莫名消失；required 保证恒在，管理员可在「工具配置」里整组替换。
	// 逐条物化而非一条 preset：快照语义 —— 事后改清单定义不会静默改变存量 agent。
	// 与 agent_relations 同事务，保证原子。
	//
	// 判据是实体属性（is_supervisor），不是名字：main 只是预置的那个 supervisor。
	seeds := models.DefaultAgentToolGrants()
	if agent.IsSupervisor {
		seeds = models.SupervisorCoreGrants()
	}
	for _, it := range seeds {
		if _, err := tx.Exec(`INSERT INTO `+authInfo.Schema+`.agent_tool_grants
			(agent_id, grant_kind, item_type, ref, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW()) ON CONFLICT DO NOTHING`,
			id, models.GrantKindRequired, it.ItemType, it.Ref, xsql.UUID(authInfo.ID)); err != nil {
			return "", err
		}
	}

	// supervisor 起步技能：四份 authoring 绑上（幂等；skill 还没物化时静默跳过）。
	// 它们与 core 组是一套：管理类工具跟随技能按需注入，不绑就点不亮那几组。
	if agent.IsSupervisor {
		if err := seedSupervisorSkills(tx, authInfo.Schema, id, authInfo.ID); err != nil {
			return "", err
		}
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return "", err
	}

	return id, nil
}

// FindByName 按名字取智能体（同名只有一行）；不存在返回 (nil, nil)。
func (r *PostgresRepository) FindByName(authInfo *auth.AuthInfo, name string) (*models.Agent, error) {
	var instance models.Agent
	err := scanAgent(db.DB.QueryRow(`SELECT `+agentColumns(authInfo.Schema)+
		` FROM `+authInfo.Schema+`.agents WHERE name = $1`, name), &instance)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &instance, nil
}

// ExistsName 判断同租户下是否已存在同名 agent（excludeID 非空时忽略该 id 自身）。
func (r *PostgresRepository) ExistsName(authInfo *auth.AuthInfo, name string, excludeID string) (bool, error) {
	q := `SELECT EXISTS(SELECT 1 FROM ` + authInfo.Schema + `.agents WHERE LOWER(name) = LOWER($1)`
	args := []interface{}{name}
	if excludeID != "" {
		q += ` AND id <> $2`
		args = append(args, excludeID)
	}
	q += `)`
	var exists bool
	if err := db.DB.QueryRow(q, args...).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// UpdateAgent 更新智能体
func (r *PostgresRepository) UpdateAgent(authInfo *auth.AuthInfo, id string, agent *models.Agent) error {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 默认值
	if agent.Status == 0 {
		agent.Status = xsql.Int(models.StatusNormal)
	}
	// config / endpoints 列均为 NOT NULL：本函数是全字段覆盖式 UPDATE，
	// nil 会被写成 NULL 触发约束错误，这里兜底成空对象 / 空数组（与 CreateAgent 一致）。
	if agent.Config.IsNil() {
		agent.Config = xsql.JSONB{Data: map[string]any{}}
	}
	if agent.Endpoints.IsNil() {
		agent.Endpoints = xsql.JSONB{Data: []any{}}
	}

	// 设置更新时间
	agent.UpdatedAt = xsql.Time(time.Now())
	agent.UpdatedBy = xsql.UUID(authInfo.ID)

	// 取旧名：改名后需同步该 agent 的端点名（对外名恒等于实体名）
	var oldName string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, id).Scan(&oldName); err != nil {
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		return err
	}

	// 执行更新操作（全字段覆盖式：调用方须先读旧实例并回填 tools_unrestricted / skill_discovery，
	// 否则这些开关会被写成零值 false）
	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.agents SET name = $1, description = $2, config = $3, status = $4, endpoints = $5, tools_unrestricted = $6, skill_discovery = $7, updated_by = $8, updated_at = $9 WHERE id = $10`,
		agent.Name, agent.Description,
		agent.Config, agent.Status, agent.Endpoints,
		agent.ToolsUnrestricted, agent.SkillDiscovery,
		agent.UpdatedBy, agent.UpdatedAt, id,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	// 改名 → 同步该 agent 的所有协议端点名（对外名 = 实体名）
	if oldName != agent.Name {
		if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.svc_endpoints SET name = $2, updated_at = NOW() WHERE target_id = $1 AND target_type = 'agent'`,
			id, agent.Name); err != nil {
			return err
		}
	}

	// 提交事务
	return tx.Commit()
}

// DeleteAgent 删除智能体。
// 事务内一并清理：agent_skill_bindings（agent_id 无外键）、私有 skill 记录
// （skills.agent_id 同样无外键，技能自身的 dependents 由 ON DELETE CASCADE 带走）、
// 双向 agent_relations，最后删 agents 行 —— 任何一步失败整体回滚，
// 不会留下「agent 还在、技能记录已没」这种不可自愈的半套状态。
func (r *PostgresRepository) DeleteAgent(authInfo *auth.AuthInfo, id string) error {
	// 开始事务
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 内置 main 智能体不允许删除（兜底防线：覆盖所有调用方）
	var name string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, id).Scan(&name); err != nil {
		return err
	}
	if models.IsProtectedAgent(name) {
		return errors.New("protected agent " + name + " cannot be deleted")
	}

	// 清理该 agent 的授权关联（与删除 agent 同一事务，保证原子性）
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.agent_skill_bindings WHERE agent_id = $1`, id); err != nil {
		return err
	}
	// 私有 skill 记录（skills.agent_id 没有外键，级联只能由应用层保证）。
	// 必须与 agents 行同事务：两段式删除一旦后半失败，就会留下「agent 还在、
	// 技能记录全没」的不可自愈状态。删掉的技能由 skills 上的 ON DELETE CASCADE
	// 带走 skill_tool_grants / skill_versions 以及其它 agent 指向它们的绑定。
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.skills WHERE agent_id = $1`, id); err != nil {
		return err
	}
	// 清理该 agent 的调用授权（双向：作为 caller 或 callee）
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.agent_relations
		WHERE caller_id = $1 OR callee_id = $1`, id); err != nil {
		return err
	}

	// 删除智能体
	result, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.agents WHERE id = $1`, id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	// 提交事务
	return tx.Commit()
}

// GetUnrestricted 读取「不限制工具」开关（agents.tools_unrestricted；agent 不存在返回 false, nil）。
func (r *PostgresRepository) GetUnrestricted(authInfo *auth.AuthInfo, agentID string) (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT tools_unrestricted FROM `+authInfo.Schema+`.agents WHERE id = $1`, agentID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v, err
}

// SetUnrestricted 设置「不限制工具」开关（单列 UPDATE，不触碰其它字段）。
func (r *PostgresRepository) SetUnrestricted(authInfo *auth.AuthInfo, agentID string, unrestricted bool) error {
	result, err := db.DB.Exec(
		`UPDATE `+authInfo.Schema+`.agents SET tools_unrestricted = $1, updated_at = NOW() WHERE id = $2`,
		unrestricted, agentID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetSupervisor 设置 agent 的超级权限属性（单列 UPDATE）。
//
// 刻意不走 UpdateAgent：那是全字段覆盖式，任何一个「只改描述」的调用方忘了回填
// 都会把超权静默清零。属性变更必须是显式单列操作（同 SetUnrestricted）。
//
// 降级时在同一事务里锁住全部 supervisor 行再计数 —— 并发降级两个 supervisor 时
// 后到的事务会等前者提交，从而看到真实剩余数；否则两边都读到 2，最后变成 0 个，
// 租户会永久失去管理面（再没有任何人能授予回来）。
func (r *PostgresRepository) SetSupervisor(authInfo *auth.AuthInfo, agentID string, isSupervisor bool) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 目标名字：受保护 agent（main）永远不允许降级 —— 它不可改名、不可删除，
	// 降级后会留下一个占着预置入口却没有管理权的死角，再也恢复不回来。
	var name string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, agentID).Scan(&name); err != nil {
		return err
	}

	if !isSupervisor {
		if !models.CanDemoteSupervisor(name) {
			return errors.New("不能撤销预置 supervisor 的超级权限（如 main）：它的名字是固定入口，降级后无法恢复")
		}
		rows, err := tx.Query(`SELECT id FROM ` + authInfo.Schema + `.agents WHERE is_supervisor = true FOR UPDATE`)
		if err != nil {
			return err
		}
		others := 0
		targetIsSupervisor := false
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				rows.Close()
				return err
			}
			if sid == agentID {
				targetIsSupervisor = true
				continue
			}
			others++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// 目标本来就不是 supervisor：幂等返回，不参与「最后一个」判定。
		if !targetIsSupervisor {
			return tx.Commit()
		}
		if others == 0 {
			return errors.New("不能降级最后一个 supervisor：降级后本租户将失去全部管理能力")
		}
	}

	result, err := tx.Exec(
		`UPDATE `+authInfo.Schema+`.agents SET is_supervisor = $1, updated_at = NOW() WHERE id = $2`,
		isSupervisor, agentID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	// 授予时的「起步套件」：core 组 + 两个开关 + 四份 authoring 技能。
	//
	// 现实入口只有这一条：前端新建时不能勾 supervisor（要保存后才能授予），
	// 所以 supervisor 100% 是「先建成普通 agent（12 条单工具种子），再授予」。
	// 不在这里补齐，拿到的就是一个有身份却没手脚的空壳。
	//
	// 撤销（false）**不回滚**这套：工具与技能绑定是资产，静默回收比留着难排查；
	// 且撤销后 builtin:agent-admin 类工具会在执行层被 IsSupervisorOnlyTool 剥掉，留着也执行不了。
	if isSupervisor {
		if err := promoteSupervisor(tx, authInfo.Schema, agentID, authInfo.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// promoteSupervisor 授予超级权限时补齐「起步套件」。
//
// 工具授权分两种处理：
//   - 现有 required 行恰好等于普通 agent 默认种子（从未定制）→ 整批替换成 core 组，
//     让「必须导入」里显示的是组而不是一长串工具（组是原子的，删错一眼可见）；
//   - 已经定制过（多一条 / 少一条都算）→ 只追加 core 组、绝不删除，那是管理员的手工配置。
func promoteSupervisor(tx *sql.Tx, schema, agentID, operatorID string) error {
	// ① 两个开关
	if _, err := tx.Exec(`UPDATE `+schema+`.agents
		SET tools_unrestricted = true, skill_discovery = true, updated_at = NOW()
		WHERE id = $1`, agentID); err != nil {
		return err
	}
	// ② 默认单工具种子 → core 组
	refs, err := requiredGrantRefs(tx, schema, agentID)
	if err != nil {
		return err
	}
	if isDefaultAgentSeedOnly(refs) {
		if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_tool_grants
			WHERE agent_id = $1 AND grant_kind = $2`, agentID, models.GrantKindRequired); err != nil {
			return err
		}
	}
	for _, it := range models.SupervisorCoreGrants() {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_tool_grants
			(agent_id, grant_kind, item_type, ref, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW()) ON CONFLICT DO NOTHING`,
			agentID, models.GrantKindRequired, it.ItemType, it.Ref, xsql.UUID(operatorID)); err != nil {
			return err
		}
	}
	// ③ 四份 authoring 技能
	return seedSupervisorSkills(tx, schema, agentID, operatorID)
}

// seedSupervisorSkills 把 models.SupervisorSeedSkills 绑到指定 agent。
//
// 只认租户级 skill（agent_id IS NULL）且未废弃：私有 skill 天然归属某个 agent，
// 绑给别的 agent 没有意义；skill 还没物化时静默跳过，不阻断建 agent / 授予。
func seedSupervisorSkills(tx *sql.Tx, schema, agentID, operatorID string) error {
	op := xsql.UUID(operatorID)
	for _, name := range models.SupervisorSeedSkills {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_skill_bindings (agent_id, skill_id, created_by)
			SELECT $1, id, $2 FROM `+schema+`.skills
			WHERE agent_id IS NULL AND LOWER(name) = LOWER($3) AND status <> 3
			ON CONFLICT DO NOTHING`, agentID, op, name); err != nil {
			return err
		}
	}
	return nil
}

// requiredGrantRefs 读取某 agent 全部「必须导入」的 ref（组与单工具都算）。
func requiredGrantRefs(tx *sql.Tx, schema, agentID string) ([]string, error) {
	rows, err := tx.Query(`SELECT ref FROM `+schema+`.agent_tool_grants
		WHERE agent_id = $1 AND grant_kind = $2`, agentID, models.GrantKindRequired)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// isDefaultAgentSeedOnly 严格相等（一条不多、一条不少）才判为「未定制」。
// 不用子集：子集可能来自管理员手工删减，那种情况不该被整批覆盖。
func isDefaultAgentSeedOnly(refs []string) bool {
	seed := models.DefaultAgentToolGrants()
	if len(refs) != len(seed) {
		return false
	}
	want := make(map[string]bool, len(seed))
	for _, it := range seed {
		want[it.Ref] = true
	}
	for _, ref := range refs {
		if !want[ref] {
			return false
		}
	}
	return true
}

// UpdateStatus 仅更新 status（1=active 启用 | 2=disabled 停用）。
func (r *PostgresRepository) UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error {
	result, err := db.DB.Exec(`UPDATE `+authInfo.Schema+`.agents SET status = $1, updated_by = $2, updated_at = $3 WHERE id = $4`,
		status, xsql.UUID(authInfo.ID), xsql.Time(time.Now()), id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
