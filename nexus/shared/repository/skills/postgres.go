package skills

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL 实现。
type PostgresRepository struct{}

// skillColumns skill 查询列（schema 用于关联 users / agents）。
// updated_by_name、agent_name 为关联查询结果（非 DB 列）。
func skillColumns(schema string) string {
	return "id, name, description, created_by, updated_by, metadata, status, restricted, created_at, updated_at, agent_id, " +
		"user_invocable, command_dispatch, command_tool, " +
		"(SELECT name FROM " + schema + ".users WHERE id = skills.updated_by) as updated_by_name, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = skills.agent_id) as agent_name"
}

// scanSkill 将一行扫描到 Skill（列顺序须与 skillColumns 一致）
func scanSkill(scanner interface{ Scan(...interface{}) error }, m *models.Skill) error {
	return scanner.Scan(
		&m.ID, &m.Name, &m.Description, &m.CreatedBy, &m.UpdatedBy,
		&m.Metadata,
		&m.Status, &m.Restricted, &m.CreatedAt, &m.UpdatedAt, &m.AgentID,
		&m.UserInvocable, &m.CommandDispatch, &m.CommandTool,
		&m.UpdatedByName,
		&m.AgentName,
	)
}

// visibleCond 返回"租户级 skill 对指定 agent 可见"的 SQL 条件片段。
// argIdx 为 agentID 的占位符下标（$n）。片段整体被括号包裹，可安全用 AND/OR 拼接。
//
//	restricted = false（默认）→ 全员可见
//	restricted = true         → 仅 agent_skill_bindings 中列出的 agent 可见
func visibleCond(schema string, argIdx int) string {
	p := "$" + strconv.Itoa(argIdx)
	return `(NOT restricted
	         OR EXISTS (SELECT 1 FROM ` + schema + `.agent_skill_bindings b
	                     WHERE b.skill_id = skills.id AND b.agent_id = ` + p + `))`
}

// Create 新建 skill（ID 为空时自动生成），返回 id。
func (r *PostgresRepository) Create(schema string, s *models.Skill) (string, error) {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.Status == 0 {
		s.Status = 1
	}
	if s.Metadata.IsNil() {
		s.Metadata = xsql.JSONB{Data: map[string]any{}}
	}
	now := time.Now()
	s.CreatedAt = xsql.Time(now)
	s.UpdatedAt = xsql.Time(now)

	_, err := db.DB.Exec(`INSERT INTO `+schema+`.skills
		(id, name, description, created_by, updated_by, metadata, status, restricted, created_at, updated_at, agent_id, user_invocable, command_dispatch, command_tool)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		s.ID, s.Name, s.Description, s.CreatedBy, s.UpdatedBy,
		s.Metadata,
		s.Status, s.Restricted, s.CreatedAt, s.UpdatedAt, s.AgentID,
		s.UserInvocable, s.CommandDispatch, s.CommandTool,
	)
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// Update 按 id 更新 name/description/metadata/status/updated_by，
// 以及 user_invocable/command_dispatch/command_tool。
func (r *PostgresRepository) Update(schema string, s *models.Skill) error {
	// 与 Create 同口径的兜底：xsql.JSONB 的 Scan 会把库里的 '{}' 读成 nil（见 tools/xsql/sql.go），
	// 而 Value() 又把 nil 写回成 SQL NULL。少了这一步，任何一次编辑都会让 metadata 从 '{}' 退化成 NULL。
	if s.Metadata.IsNil() {
		s.Metadata = xsql.JSONB{Data: map[string]any{}}
	}
	result, err := db.DB.Exec(`UPDATE `+schema+`.skills
		SET name = $1, description = $2, metadata = $3, updated_by = $4, updated_at = $5,
		    user_invocable = $6, command_dispatch = $7, command_tool = $8
		WHERE id = $9`,
		s.Name, s.Description, s.Metadata, s.UpdatedBy, xsql.Time(time.Now()),
		s.UserInvocable, s.CommandDispatch, s.CommandTool, s.ID,
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

// Delete 按 id 删除。内置受保护 skill 不允许删除（兜底防线：覆盖所有调用方）。
// 事务内先清理授权关联（agent_skill_bindings 无外键，级联由应用层保证），再删 skill。
func (r *PostgresRepository) Delete(schema, id string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 查不到该 id 时保持原有语义（静默跳过保护，交由 DELETE 处理）
	var name string
	if qerr := tx.QueryRow(`SELECT name FROM `+schema+`.skills WHERE id = $1`, id).Scan(&name); qerr == nil {
		if models.IsProtectedSkill(name) {
			return errors.New("protected skill " + name + " cannot be deleted")
		}
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_skill_bindings WHERE skill_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.skills WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Get 按 id 获取；不存在返回 (nil, nil)。
func (r *PostgresRepository) Get(schema, id string) (*models.Skill, error) {
	var s models.Skill
	err := scanSkill(db.DB.QueryRow(`SELECT `+skillColumns(schema)+` FROM `+schema+`.skills WHERE id = $1`, id), &s)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// injectedCond 返回「注入给指定 agent」的 SQL 条件片段（忽略 restricted）：
// 私有（agent_id = $n）或 租户级且被该 agent 绑定（agent_skill_bindings）。
func injectedCond(schema string, argIdx int) string {
	p := "$" + strconv.Itoa(argIdx)
	return `(agent_id = ` + p + `
	         OR (agent_id IS NULL AND EXISTS (SELECT 1 FROM ` + schema + `.agent_skill_bindings b
	                     WHERE b.skill_id = skills.id AND b.agent_id = ` + p + `)))`
}

// FindVisibleByName 在可见范围内按 name 查：优先私有，其次租户级。
// 租户级 skill 受 agent_skill_bindings 收窄（见 visibleCond）。
func (r *PostgresRepository) FindVisibleByName(schema, name, agentID string) (*models.Skill, error) {
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE LOWER(name) = LOWER($1) AND status <> 3 AND (agent_id IS NULL`
	args := []interface{}{name}
	if agentID != "" {
		q += ` AND ` + visibleCond(schema, 2) + ` OR agent_id = $2`
		args = append(args, agentID)
	}
	q += `) ORDER BY (agent_id IS NOT NULL) DESC LIMIT 1`

	var s models.Skill
	err := scanSkill(db.DB.QueryRow(q, args...), &s)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// ListVisible 列出可见 skill（租户级 + 私有，status<>3 即不含废弃），按 name 排序。
// 租户级 skill 受 agent_skill_bindings 收窄（见 visibleCond）。
func (r *PostgresRepository) ListVisible(schema, agentID string) ([]models.Skill, error) {
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE status <> 3 AND (agent_id IS NULL`
	args := []interface{}{}
	if agentID != "" {
		q += ` AND ` + visibleCond(schema, 1) + ` OR agent_id = $1`
		args = append(args, agentID)
	}
	q += `) ORDER BY name ASC`

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Skill, 0)
	for rows.Next() {
		var s models.Skill
		if err := scanSkill(rows, &s); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// List 列出 skill（管理端：分页 + 过滤），返回列表与总数。
func (r *PostgresRepository) List(schema string, req *ListRequest) ([]models.Skill, int, error) {
	where := ""
	args := []interface{}{}
	idx := 1

	addCond := func(cond string) {
		if where == "" {
			where = " WHERE " + cond
		} else {
			where += " AND " + cond
		}
	}

	if req.AgentID != "" {
		addCond("agent_id = $" + strconv.Itoa(idx))
		args = append(args, req.AgentID)
		idx++
	}
	if req.Status != nil {
		addCond("status = $" + strconv.Itoa(idx))
		args = append(args, *req.Status)
		idx++
	}
	if req.Keyword != "" {
		p := "$" + strconv.Itoa(idx)
		addCond("(name ILIKE " + p + " OR description ILIKE " + p + ")")
		args = append(args, "%"+req.Keyword+"%")
		idx++
	}

	var total int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+schema+`.skills`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	page := req.Page
	if page < 1 {
		page = 1
	}
	// limit 语义：负数 = 取全部（管理界面用，不拼 LIMIT/OFFSET）；否则 <1 时用默认值
	all := req.Limit < 0
	limit := req.Limit
	if !all && limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills` + where +
		" ORDER BY name ASC"
	if !all {
		q += " LIMIT $" + strconv.Itoa(idx) + " OFFSET $" + strconv.Itoa(idx+1)
		args = append(args, limit, offset)
	}

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	list := make([]models.Skill, 0)
	for rows.Next() {
		var s models.Skill
		if err := scanSkill(rows, &s); err != nil {
			return nil, 0, err
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// FindByName 在指定归属范围内按 name 查（全状态，用于重名检查）。
func (r *PostgresRepository) FindByName(schema, name, agentID string) (*models.Skill, error) {
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills WHERE LOWER(name) = LOWER($1) AND agent_id IS NULL`
	args := []interface{}{name}
	if agentID != "" {
		q = `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills WHERE LOWER(name) = LOWER($1) AND agent_id = $2`
		args = append(args, agentID)
	}

	var s models.Skill
	err := scanSkill(db.DB.QueryRow(q, args...), &s)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// UpdateStatus 仅更新 status（启用/停用）。
func (r *PostgresRepository) UpdateStatus(schema, id string, status int) error {
	result, err := db.DB.Exec(`UPDATE `+schema+`.skills SET status = $1, updated_at = $2 WHERE id = $3`,
		status, xsql.Time(time.Now()), id)
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

// DeleteByAgentID 删除某个 agent 的全部私有 skill 记录（agent 删除时级联清理）。
// 事务内先清理这些 skill 的授权关联（可能存在的历史数据），再删 skill 记录。
func (r *PostgresRepository) DeleteByAgentID(schema, agentID string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_skill_bindings
		WHERE skill_id IN (SELECT id FROM `+schema+`.skills WHERE agent_id = $1)`, agentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.skills WHERE agent_id = $1`, agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListBindings 列出某 skill 的授权 agent（含 agent_name），按 agent 名排序。
func (r *PostgresRepository) ListBindings(schema, skillID string) ([]models.AgentSkillBinding, error) {
	rows, err := db.DB.Query(`SELECT b.agent_id, b.skill_id, b.created_by, b.created_at,
		(SELECT name FROM `+schema+`.agents WHERE id = b.agent_id) as agent_name
		FROM `+schema+`.agent_skill_bindings b WHERE b.skill_id = $1
		ORDER BY agent_name ASC`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.AgentSkillBinding, 0)
	for rows.Next() {
		var b models.AgentSkillBinding
		if err := rows.Scan(&b.AgentID, &b.SkillID, &b.CreatedBy, &b.CreatedAt, &b.AgentName); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

// SetVisibility 设置某 skill 的可见性：restricted 开关 + 授权 agent 白名单（事务内全量替换）。
// restricted=false 时 bindings 不参与判定（数据保留，便于来回切换开关）。
func (r *PostgresRepository) SetVisibility(schema, skillID string, restricted bool, agentIDs []string, operatorID string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.Exec(`UPDATE `+schema+`.skills SET restricted = $1, updated_at = $2 WHERE id = $3`,
		restricted, xsql.Time(time.Now()), skillID)
	if err != nil {
		return err
	}
	if n, rerr := result.RowsAffected(); rerr == nil && n == 0 {
		return sql.ErrNoRows
	}

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_skill_bindings WHERE skill_id = $1`, skillID); err != nil {
		return err
	}
	op := xsql.UUID(operatorID)
	for _, aid := range agentIDs {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_skill_bindings (agent_id, skill_id, created_by)
			VALUES ($1, $2, $3)`, aid, skillID, op); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetRestricted 只改 skills.restricted 一列（不碰白名单 —— 白名单由 bind / unbind 维护）。
//
// 不用 SetVisibility 代劳：后者在事务里 DELETE 整张 agent_skill_bindings 再按传入的
// agentIDs 重建，只改开关会把已有绑定全清掉。
// restricted=false 时 bindings 仍保留，便于来回切换开关（与 SetVisibility 的注释一致）。
func (r *PostgresRepository) SetRestricted(schema, skillID string, restricted bool, operatorID string) error {
	_ = operatorID // 与 SetVisibility 同口径：可见性变更不刷 updated_by
	result, err := db.DB.Exec(`UPDATE `+schema+`.skills SET restricted = $1, updated_at = $2 WHERE id = $3`,
		restricted, xsql.Time(time.Now()), skillID)
	if err != nil {
		return err
	}
	if n, rerr := result.RowsAffected(); rerr == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListBindingCounts 返回指定 skill 集合的授权 agent 数量（管理端列表展示用；未出现的即为 0）。
func (r *PostgresRepository) ListBindingCounts(schema string, skillIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(skillIDs))
	if len(skillIDs) == 0 {
		return out, nil
	}
	ph := make([]string, 0, len(skillIDs))
	args := make([]interface{}, 0, len(skillIDs))
	for i, id := range skillIDs {
		ph = append(ph, "$"+strconv.Itoa(i+1))
		args = append(args, id)
	}
	rows, err := db.DB.Query(`SELECT skill_id, count(*) FROM `+schema+`.agent_skill_bindings
		WHERE skill_id IN (`+strings.Join(ph, ",")+`) GROUP BY skill_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			return nil, err
		}
		out[sid] = n
	}
	return out, rows.Err()
}

// ListSkillIDsByAgent 列出某 agent 绑定的 skill id，按 skill 名排序。
func (r *PostgresRepository) ListSkillIDsByAgent(schema, agentID string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT b.skill_id FROM `+schema+`.agent_skill_bindings b
		JOIN `+schema+`.skills s ON s.id = b.skill_id
		WHERE b.agent_id = $1 ORDER BY s.name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListInjected 列出「注入给该 agent」的 skill（私有 + 租户级已绑定），按 name 排序。
// agentID 为空时返回空列表（没有主体就谈不上注入）。
func (r *PostgresRepository) ListInjected(schema, agentID string) ([]models.Skill, error) {
	if agentID == "" {
		return nil, nil
	}
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE status <> 3 AND ` + injectedCond(schema, 1) + ` ORDER BY name ASC`
	rows, err := db.DB.Query(q, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Skill, 0)
	for rows.Next() {
		var s models.Skill
		if err := scanSkill(rows, &s); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// FindInjectedByName 在注入范围内按 name 查（口径同 ListInjected），不存在返回 (nil, nil)。
func (r *PostgresRepository) FindInjectedByName(schema, name, agentID string) (*models.Skill, error) {
	if agentID == "" {
		return nil, nil
	}
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE LOWER(name) = LOWER($1) AND status <> 3 AND ` + injectedCond(schema, 2) + ` LIMIT 1`
	var s models.Skill
	if err := scanSkill(db.DB.QueryRow(q, name, agentID), &s); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// ListForSupervisor 列出「超级智能体全可见」的 skill：其私有 ∪ 全部租户级（忽略 restricted / 白名单），按 name 排序。
//
// agentID 为空时只列租户级（空串不能当 uuid 参数用：pq: invalid input syntax for type uuid）。
func (r *PostgresRepository) ListForSupervisor(schema, agentID string) ([]models.Skill, error) {
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE status <> 3 AND agent_id IS NULL ORDER BY name ASC`
	args := []interface{}{}
	if agentID != "" {
		q = `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
			WHERE status <> 3 AND (agent_id IS NULL OR agent_id = $1) ORDER BY name ASC`
		args = append(args, agentID)
	}
	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.Skill, 0)
	for rows.Next() {
		var s models.Skill
		if err := scanSkill(rows, &s); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// FindForSupervisorByName 超级智能体按 name 查：优先其私有（agent_id=$2），其次任意租户级；不存在返回 (nil, nil)。
func (r *PostgresRepository) FindForSupervisorByName(schema, name, agentID string) (*models.Skill, error) {
	q := `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
		WHERE LOWER(name) = LOWER($1) AND status <> 3 AND agent_id IS NULL LIMIT 1`
	args := []interface{}{name}
	if agentID != "" {
		q = `SELECT ` + skillColumns(schema) + ` FROM ` + schema + `.skills
			WHERE LOWER(name) = LOWER($1) AND status <> 3 AND (agent_id IS NULL OR agent_id = $2)
			ORDER BY (agent_id IS NOT NULL) DESC LIMIT 1`
		args = append(args, agentID)
	}
	var s models.Skill
	if err := scanSkill(db.DB.QueryRow(q, args...), &s); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// ListBindingNamesBySkills 批量返回若干 skill 的白名单 agent 名（按 skill_id 归组；未出现的为空）。
func (r *PostgresRepository) ListBindingNamesBySkills(schema string, skillIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(skillIDs))
	if len(skillIDs) == 0 {
		return out, nil
	}
	ph := make([]string, 0, len(skillIDs))
	args := make([]interface{}, 0, len(skillIDs))
	for i, id := range skillIDs {
		ph = append(ph, "$"+strconv.Itoa(i+1))
		args = append(args, id)
	}
	rows, err := db.DB.Query(`SELECT b.skill_id, a.name FROM `+schema+`.agent_skill_bindings b
		JOIN `+schema+`.agents a ON a.id = b.agent_id
		WHERE b.skill_id IN (`+strings.Join(ph, ",")+`) ORDER BY a.name ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid, agentName string
		if err := rows.Scan(&sid, &agentName); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], agentName)
	}
	return out, rows.Err()
}

// GetAgentSkillDiscovery 读取「可发现未绑定的技能」开关（agent 不存在返回 false, nil）。
func (r *PostgresRepository) GetAgentSkillDiscovery(schema, agentID string) (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT skill_discovery FROM `+schema+`.agents WHERE id = $1`, agentID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v, err
}

// SetAgentSkillDiscovery 设置「可发现未绑定的技能」开关。
func (r *PostgresRepository) SetAgentSkillDiscovery(schema, agentID string, discovery bool) error {
	result, err := db.DB.Exec(
		`UPDATE `+schema+`.agents SET skill_discovery = $1, updated_at = NOW() WHERE id = $2`,
		discovery, agentID,
	)
	if err != nil {
		return err
	}
	if n, rerr := result.RowsAffected(); rerr != nil {
		return rerr
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ReplaceByAgent 全量替换某 agent 的 skill 绑定（只动该 agent 的行）。
// 只保留「已存在的租户级 skill」（agent_id IS NULL）；私有 skill 天然只对归属 agent 可见，绑定无意义。
func (r *PostgresRepository) ReplaceByAgent(schema, agentID string, skillIDs []string, operatorID string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	valid := make([]string, 0, len(skillIDs))
	if len(skillIDs) > 0 {
		ph := make([]string, 0, len(skillIDs))
		args := make([]interface{}, 0, len(skillIDs))
		for i, id := range skillIDs {
			ph = append(ph, "$"+strconv.Itoa(i+1))
			args = append(args, id)
		}
		rows, qerr := tx.Query(`SELECT id FROM `+schema+`.skills
			WHERE agent_id IS NULL AND id IN (`+strings.Join(ph, ",")+`)`, args...)
		if qerr != nil {
			return qerr
		}
		for rows.Next() {
			var id string
			if serr := rows.Scan(&id); serr != nil {
				rows.Close()
				return serr
			}
			valid = append(valid, id)
		}
		rerr := rows.Err()
		rows.Close()
		if rerr != nil {
			return rerr
		}
	}

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_skill_bindings WHERE agent_id = $1`, agentID); err != nil {
		return err
	}
	op := xsql.UUID(operatorID)
	for _, sid := range valid {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_skill_bindings (agent_id, skill_id, created_by)
			VALUES ($1, $2, $3) ON CONFLICT (agent_id, skill_id) DO NOTHING`, agentID, sid, op); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddBinding 幂等追加一条 skill 绑定。
// 只接受「已存在的租户级 skill」（agent_id IS NULL）—— 私有 skill 天然只对归属 agent 可见，绑定无意义。
// 返回是否真的新增（false = skill 不存在 / 非租户级 / 已绑定）。
func (r *PostgresRepository) AddBinding(schema, agentID, skillID, operatorID string) (bool, error) {
	res, err := db.DB.Exec(`INSERT INTO `+schema+`.agent_skill_bindings (agent_id, skill_id, created_by)
		SELECT $1, id, $3 FROM `+schema+`.skills WHERE id = $2 AND agent_id IS NULL
		ON CONFLICT (agent_id, skill_id) DO NOTHING`,
		agentID, skillID, xsql.UUID(operatorID))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RemoveBinding 幂等移除一条 skill 绑定；返回是否真的移除。
func (r *PostgresRepository) RemoveBinding(schema, agentID, skillID string) (bool, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.agent_skill_bindings
		WHERE agent_id = $1 AND skill_id = $2`, agentID, skillID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListBoundSkillNames 列出某 agent 绑定的 skill 名（按名排序，只含仍存在的 skill）。
func (r *PostgresRepository) ListBoundSkillNames(schema, agentID string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT s.name FROM `+schema+`.agent_skill_bindings b
		JOIN `+schema+`.skills s ON s.id = b.skill_id
		WHERE b.agent_id = $1 ORDER BY s.name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ListPrivateNames 列出某 agent 私有 skill 的名字（按名排序，不含废弃）。
// 与 ListBoundSkillNames 相对：那个看「绑定给我的共享技能」，这个看「我自己拥有的私有技能」。
func (r *PostgresRepository) ListPrivateNames(schema, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	rows, err := db.DB.Query(`SELECT name FROM `+schema+`.skills
		WHERE agent_id = $1 AND status <> 3 ORDER BY name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
