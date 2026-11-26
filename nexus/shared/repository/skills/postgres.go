package skills

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/versions"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation.
type PostgresRepository struct{}

// skillColumns are the columns read for a skill (schema is used to join users / agents).
// updated_by_name and agent_name come from joined lookups (they are not table columns).
func skillColumns(schema string) string {
	return "id, name, description, created_by, updated_by, metadata, status, restricted, created_at, updated_at, agent_id, " +
		"user_invocable, command_dispatch, command_tool, " +
		"(SELECT name FROM " + schema + ".users WHERE id = skills.updated_by) as updated_by_name, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = skills.agent_id) as agent_name"
}

// scanSkill scans one row into a Skill (the column order must match skillColumns).
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

// visibleCond returns the SQL fragment for "a tenant-level skill is visible to the given agent".
// argIdx is the placeholder index ($n) of agentID. The fragment is fully parenthesised, so it is safe to combine with
// AND/OR.
//
//	restricted = false (the default) → visible to everyone
//	restricted = true                → visible only to the agents listed in agent_skill_bindings
func visibleCond(schema string, argIdx int) string {
	p := "$" + strconv.Itoa(argIdx)
	return `(NOT restricted
	         OR EXISTS (SELECT 1 FROM ` + schema + `.agent_skill_bindings b
	                     WHERE b.skill_id = skills.id AND b.agent_id = ` + p + `))`
}

// Create creates a skill (the ID is generated when empty) and returns the id.
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

// Update updates name/description/metadata/status/updated_by by id,
// and user_invocable/command_dispatch/command_tool.
func (r *PostgresRepository) Update(schema string, s *models.Skill) error {
	// The same fallback as Create: the metadata column is NOT NULL, and a nil xsql.JSONB is written as SQL NULL by
	// Value(). Without this step any edit would trip the constraint.
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

// Delete deletes by id. A built-in protected skill may not be deleted (backstop covering every caller).
// Inside the transaction the grants are cleaned up first (agent_skill_bindings has no foreign key, so the cascade is the
// application's job), then the skill itself is deleted.
func (r *PostgresRepository) Delete(schema, id string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// A missing id keeps the previous semantics (skip the protection silently and let DELETE handle it)
	var name string
	if qerr := tx.QueryRow(`SELECT name FROM `+schema+`.skills WHERE id = $1`, id).Scan(&name); qerr == nil {
		if models.IsProtectedSkill(name) {
			return errors.New("protected skill " + name + " cannot be deleted")
		}
	}
	// A released version is what every agent's catalog serves: deleting the row would take the skill away from them, which
	// is an explicit unrelease, not a side effect of delete. Private skills have no version of their own (their lifecycle
	// follows the owning agent), so the check simply finds nothing for them.
	var released int
	switch err := tx.QueryRow(`SELECT version FROM `+schema+`.skill_versions
		WHERE skill_id = $1 AND is_current`, id).Scan(&released); {
	case err == nil:
		return &versions.ErrHasReleasedVersion{Version: released}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_skill_bindings WHERE skill_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+schema+`.skills WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Get fetches by id; missing returns (nil, nil).
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

// injectedCond returns the SQL fragment for "injected to the given agent" (restricted is ignored): private
// (agent_id = $n) or tenant-level and bound by that agent (agent_skill_bindings).
func injectedCond(schema string, argIdx int) string {
	p := "$" + strconv.Itoa(argIdx)
	return `(agent_id = ` + p + `
	         OR (agent_id IS NULL AND EXISTS (SELECT 1 FROM ` + schema + `.agent_skill_bindings b
	                     WHERE b.skill_id = skills.id AND b.agent_id = ` + p + `)))`
}

// FindVisibleByName looks up a name in the visible scope: private first, then tenant-level.
// Tenant-level skills are narrowed by agent_skill_bindings (see visibleCond).
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

// ListVisible lists the visible skills (tenant-level + private, status<>3 i.e. no deprecated ones), sorted by name.
// Tenant-level skills are narrowed by agent_skill_bindings (see visibleCond).
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

// List lists skills (admin side: paging + filters), returning the page and the total.
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
	// limit semantics: negative means "all of them" (the admin UI, no LIMIT/OFFSET appended); otherwise <1 falls back to
	// the default
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

// FindByName looks a name up inside a given ownership scope (every status, used for the duplicate-name check).
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

// UpdateStatus updates status only (enable / disable).
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

// DeleteByAgentID deletes every private skill record of one agent (cascade when the agent is deleted).
// Inside the transaction the grants of those skills are cleaned up first (legacy data may still have some), then the skill
// records are deleted.
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

// ListBindings lists the agents granted a skill (agent_name included), sorted by agent name.
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

// SetVisibility sets a skill's visibility: the restricted switch plus the allowlist of granted agents (replaced wholesale
// in a transaction). With restricted=false the bindings do not take part in the decision (the data is kept so the switch
// can be flipped back and forth).
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

// SetRestricted changes only the skills.restricted column (the allowlist is untouched — it is maintained by bind /
// unbind).
//
// SetVisibility is not reused for this: it DELETEs the whole agent_skill_bindings table inside a transaction and rebuilds
// it from the agentIDs passed in, so flipping just the switch would wipe every existing binding.
// With restricted=false the bindings are still kept, so the switch can be flipped back and forth (consistent with the note
// on SetVisibility).
func (r *PostgresRepository) SetRestricted(schema, skillID string, restricted bool, operatorID string) error {
	_ = operatorID // the same rule as SetVisibility: a visibility change does not refresh updated_by
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

// ListBindingCounts returns the number of granted agents for the given skill set (for the admin list; a skill that does
// not appear has 0).
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

// ListSkillIDsByAgent lists the skill ids bound to an agent, sorted by skill name.
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

// ListInjected lists the skills "injected to this agent" (private + bound tenant-level ones), sorted by name.
// An empty agentID returns an empty list (without a subject there is nothing to inject into).
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

// FindInjectedByName looks a name up inside the injected scope (the same rule as ListInjected); missing returns
// (nil, nil).
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

// ListForSupervisor lists the skills "fully visible to the super agent": its private ones ∪ every tenant-level one
// (ignoring restricted / the allowlist), sorted by name.
//
// With an empty agentID only tenant-level ones are listed (an empty string cannot be used as a uuid argument:
// pq: invalid input syntax for type uuid).
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

// FindForSupervisorByName looks a name up as the super agent: its private ones first (agent_id=$2), then any tenant-level
// one; missing returns (nil, nil).
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

// ListBindingNamesBySkills returns the allowlisted agent names of several skills in one batch (grouped by skill_id; a
// skill that does not appear has none).
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

// GetAgentSkillDiscovery reads the "unbound skills are discoverable" switch (a missing agent returns false, nil).
func (r *PostgresRepository) GetAgentSkillDiscovery(schema, agentID string) (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT skill_discovery FROM `+schema+`.agents WHERE id = $1`, agentID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v, err
}

// SetAgentSkillDiscovery sets the "unbound skills are discoverable" switch.
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

// ReplaceByAgent replaces an agent's skill bindings wholesale (only that agent's rows are touched).
// Only "existing tenant-level skills" are kept (agent_id IS NULL); a private skill is inherently visible to its owner
// only, so binding it is meaningless.
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

// AddBinding idempotently appends one skill binding.
// Only "existing tenant-level skills" are accepted (agent_id IS NULL) — a private skill is inherently visible to its owner
// only, so binding it is meaningless.
// Reports whether anything was really added (false = the skill is missing / not tenant-level / already bound).
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

// RemoveBinding idempotently removes one skill binding; reports whether anything was really removed.
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

// ListBoundSkillNames lists the skill names bound to an agent (sorted by name, only skills that still exist).
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

// ListPrivateNames lists an agent's private skill names (sorted by name, deprecated ones excluded).
// The counterpart of ListBoundSkillNames: that one reads "the shared skills bound to me", this one "the private skills I
// own".
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
