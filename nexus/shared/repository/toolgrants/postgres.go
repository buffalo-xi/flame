package toolgrants

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of tool grants.
type PostgresRepository struct{}

// NewRepository creates a repository instance.
func NewRepository() Repository { return &PostgresRepository{} }

// ===== custom tool groups =====

// ListGroups lists every custom group (members included).
func (r *PostgresRepository) ListGroups(schema string) ([]models.ToolGroup, error) {
	q := fmt.Sprintf(`SELECT g.id, g.name, COALESCE(g.description, ''), g.created_by, g.updated_by,
			g.created_at, g.updated_at, i.item_type, i.ref
		FROM %s.tool_groups g
		LEFT JOIN %s.tool_group_items i ON i.group_id = g.id
		ORDER BY g.name ASC, i.item_type ASC, i.ref ASC`, schema, schema)

	rows, err := db.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[string]*models.ToolGroup{}
	order := make([]string, 0)
	for rows.Next() {
		var (
			id          string
			name        string
			description sql.NullString
			createdBy   sql.NullString
			updatedBy   sql.NullString
			createdAt   sql.NullTime
			updatedAt   sql.NullTime
			itemType    sql.NullString
			ref         sql.NullString
		)
		if err := rows.Scan(&id, &name, &description, &createdBy, &updatedBy,
			&createdAt, &updatedAt, &itemType, &ref); err != nil {
			return nil, err
		}

		g, ok := byID[id]
		if !ok {
			g = &models.ToolGroup{
				ID:          id,
				Name:        name,
				Description: description.String,
				Items:       []models.ToolGroupItem{},
			}
			if createdBy.Valid {
				g.CreatedBy = xsql.UUID(createdBy.String)
			}
			if updatedBy.Valid {
				g.UpdatedBy = xsql.UUID(updatedBy.String)
			}
			if createdAt.Valid {
				g.CreatedAt = xsql.Time(createdAt.Time)
			}
			if updatedAt.Valid {
				g.UpdatedAt = xsql.Time(updatedAt.Time)
			}
			byID[id] = g
			order = append(order, id)
		}
		if itemType.Valid && ref.Valid && ref.String != "" {
			g.Items = append(g.Items, models.ToolGroupItem{ItemType: itemType.String, Ref: ref.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]models.ToolGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// GetGroup returns a custom group by name (members included); missing returns (nil, nil).
// Groups are few, so it filters ListGroups rather than maintaining a second copy of the SQL.
func (r *PostgresRepository) GetGroup(schema, name string) (*models.ToolGroup, error) {
	groups, err := r.ListGroups(schema)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, nil
}

// UpsertGroup creates / updates a custom group by name and replaces its members wholesale (transaction), returning the
// group id.
func (r *PostgresRepository) UpsertGroup(schema, name, description, userID string, items []models.ToolGroupItem) (string, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	newID := uuid.NewString()
	q := fmt.Sprintf(`INSERT INTO %s.tool_groups (id, name, description, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4, NOW(), NOW())
		ON CONFLICT (name) DO UPDATE
			SET description = EXCLUDED.description, updated_by = EXCLUDED.updated_by, updated_at = NOW()
		RETURNING id`, schema)

	var id string
	if err := tx.QueryRow(q, newID, name, description, xsql.UUID(userID)).Scan(&id); err != nil {
		return "", err
	}
	if err := replaceGroupItems(tx, schema, id, items); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// DeleteGroup deletes a custom group by name (its members cascade).
func (r *PostgresRepository) DeleteGroup(schema, name string) error {
	_, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s.tool_groups WHERE name = $1`, schema), name)
	return err
}

// replaceGroupItems replaces a group's members wholesale (deduplicated, empty items ignored).
func replaceGroupItems(tx *sql.Tx, schema, groupID string, items []models.ToolGroupItem) error {
	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.tool_group_items WHERE group_id = $1`, schema), groupID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, it := range items {
		if it.ItemType == "" || it.Ref == "" {
			continue
		}
		key := it.ItemType + "|" + it.Ref
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(
			fmt.Sprintf(`INSERT INTO %s.tool_group_items (group_id, item_type, ref) VALUES ($1, $2, $3)`, schema),
			groupID, it.ItemType, it.Ref,
		); err != nil {
			return err
		}
	}
	return nil
}

// ===== agent tool grants =====

// ListAgentGrants lists every grant of an agent.
func (r *PostgresRepository) ListAgentGrants(schema, agentID string) ([]models.AgentToolGrant, error) {
	q := fmt.Sprintf(`SELECT grant_kind, item_type, ref, updated_by, updated_at
		FROM %s.agent_tool_grants WHERE agent_id = $1 ORDER BY grant_kind ASC, ref ASC`, schema)
	rows, err := db.DB.Query(q, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AgentToolGrant, 0)
	for rows.Next() {
		var (
			g         models.AgentToolGrant
			updatedBy sql.NullString
			updatedAt sql.NullTime
		)
		if err := rows.Scan(&g.GrantKind, &g.ItemType, &g.Ref, &updatedBy, &updatedAt); err != nil {
			return nil, err
		}
		g.AgentID = agentID
		if updatedBy.Valid {
			g.UpdatedBy = xsql.UUID(updatedBy.String)
		}
		if updatedAt.Valid {
			g.UpdatedAt = xsql.Time(updatedAt.Time)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceAgentGrants replaces an agent's grants wholesale (transaction).
// allow and deny are mutually exclusive: when both appear for the same ref, deny wins.
func (r *PostgresRepository) ReplaceAgentGrants(schema, agentID, userID string, grants []models.AgentToolGrant) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.agent_tool_grants WHERE agent_id = $1`, schema), agentID); err != nil {
		return err
	}

	denied := map[string]bool{}
	for _, g := range grants {
		if g.GrantKind == models.GrantKindDeny {
			denied[g.ItemType+"|"+g.Ref] = true
		}
	}

	seen := map[string]bool{}
	for _, g := range grants {
		if g.ItemType == "" || g.Ref == "" || g.GrantKind == "" {
			continue
		}
		if g.GrantKind == models.GrantKindAllow && denied[g.ItemType+"|"+g.Ref] {
			continue // deny wins
		}
		key := g.GrantKind + "|" + g.ItemType + "|" + g.Ref
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(
			fmt.Sprintf(`INSERT INTO %s.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
				VALUES ($1, $2, $3, $4, $5, NOW())`, schema),
			agentID, g.GrantKind, g.ItemType, g.Ref, xsql.UUID(userID),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertAgentGrant adds / changes one grant of an agent (transaction: delete the old row by ref first, then insert).
//
// Why not assemble it through ReplaceAgentGrants: that is a read-modify-write with a race, and it would refresh the
// updated_by / updated_at of every other grant row of the agent (wiping the audit trail).
// Changing the kind for the same ref overwrites it — which inherently guarantees one row per ref (allow and required never
// coexist).
func (r *PostgresRepository) UpsertAgentGrant(schema, agentID, userID string, g models.AgentToolGrant) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		fmt.Sprintf(`DELETE FROM %s.agent_tool_grants WHERE agent_id = $1 AND ref = $2`, schema),
		agentID, g.Ref,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		fmt.Sprintf(`INSERT INTO %s.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())`, schema),
		agentID, g.GrantKind, g.ItemType, g.Ref, xsql.UUID(userID),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAgentGrant removes one grant of an agent precisely (by ref), returning the number of rows deleted (for an
// idempotent report).
func (r *PostgresRepository) DeleteAgentGrant(schema, agentID, ref string) (int64, error) {
	res, err := db.DB.Exec(
		fmt.Sprintf(`DELETE FROM %s.agent_tool_grants WHERE agent_id = $1 AND ref = $2`, schema),
		agentID, ref,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ===== skill tool dependencies =====

// ListSkillGrants lists the tool dependencies a skill declares.
func (r *PostgresRepository) ListSkillGrants(schema, skillID string) ([]models.SkillToolGrant, error) {
	q := fmt.Sprintf(`SELECT item_type, ref FROM %s.skill_tool_grants WHERE skill_id = $1 ORDER BY ref ASC`, schema)
	rows, err := db.DB.Query(q, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.SkillToolGrant, 0)
	for rows.Next() {
		g := models.SkillToolGrant{SkillID: skillID}
		if err := rows.Scan(&g.ItemType, &g.Ref); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceSkillGrants replaces a skill's tool dependencies wholesale (transaction).
func (r *PostgresRepository) ReplaceSkillGrants(schema, skillID string, grants []models.SkillToolGrant) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.skill_tool_grants WHERE skill_id = $1`, schema), skillID); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, g := range grants {
		if g.ItemType == "" || g.Ref == "" {
			continue
		}
		key := g.ItemType + "|" + g.Ref
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(
			fmt.Sprintf(`INSERT INTO %s.skill_tool_grants (skill_id, item_type, ref) VALUES ($1, $2, $3)`, schema),
			skillID, g.ItemType, g.Ref,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddSkillGrant appends one dependency (idempotent): it returns false when it already exists and writes nothing.
//
// ON CONFLICT rather than a read-then-write: two concurrent adds do not overwrite each other, nor do they drag in the
// read-modify-write race of a wholesale replace (the PK is (skill_id, item_type, ref)).
func (r *PostgresRepository) AddSkillGrant(schema, skillID string, g models.SkillToolGrant) (bool, error) {
	if g.ItemType == "" || g.Ref == "" {
		return false, fmt.Errorf("skill grant requires item_type and ref")
	}
	res, err := db.DB.Exec(
		fmt.Sprintf(`INSERT INTO %s.skill_tool_grants (skill_id, item_type, ref)
			VALUES ($1, $2, $3) ON CONFLICT (skill_id, item_type, ref) DO NOTHING`, schema),
		skillID, g.ItemType, g.Ref,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RemoveSkillGrant removes one dependency by ref (across item_type, idempotent), returning the number of rows deleted.
//
// It does not check that the ref can still be resolved: a dangling dependency left after a tool / connection was deleted
// must still be cleanable (the same consideration as DeleteAgentGrant).
func (r *PostgresRepository) RemoveSkillGrant(schema, skillID, ref string) (int64, error) {
	res, err := db.DB.Exec(
		fmt.Sprintf(`DELETE FROM %s.skill_tool_grants WHERE skill_id = $1 AND ref = $2`, schema),
		skillID, ref,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ===== reference reverse lookup =====

// CountRefUsages counts how many times a ref is used in agent grants, skill dependencies and custom group members.
func (r *PostgresRepository) CountRefUsages(schema, ref string) (int, error) {
	q := fmt.Sprintf(`SELECT
		(SELECT COUNT(*) FROM %s.agent_tool_grants WHERE ref = $1) +
		(SELECT COUNT(*) FROM %s.skill_tool_grants WHERE ref = $1) +
		(SELECT COUNT(*) FROM %s.tool_group_items WHERE ref = $1)`, schema, schema, schema)
	var n int
	if err := db.DB.QueryRow(q, ref).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountRefPrefixUsages counts the usages of a ref and its sub-items (ref:xxx).
// It uses starts_with rather than LIKE: a ref may contain '_', which LIKE would treat as a single-character wildcard.
func (r *PostgresRepository) CountRefPrefixUsages(schema, ref string) (int, error) {
	q := fmt.Sprintf(`SELECT
		(SELECT COUNT(*) FROM %s.agent_tool_grants WHERE ref = $1 OR starts_with(ref, $2)) +
		(SELECT COUNT(*) FROM %s.skill_tool_grants WHERE ref = $1 OR starts_with(ref, $2)) +
		(SELECT COUNT(*) FROM %s.tool_group_items WHERE ref = $1 OR starts_with(ref, $2))`, schema, schema, schema)
	var n int
	if err := db.DB.QueryRow(q, ref, ref+":").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// refUsageFilter builds the reference match condition: an exact ref hit; with prefix=true the ref:xxx sub-items match as
// well.
func refUsageFilter(alias, ref string, prefix bool) (string, []any) {
	if !prefix {
		return alias + ".ref = $1", []any{ref}
	}
	return fmt.Sprintf("(%s.ref = $1 OR starts_with(%s.ref, $2))", alias, alias), []any{ref, ref + ":"}
}

// ListRefUsages returns the usage details of a ref across agent grants / skill dependencies / custom group members.
func (r *PostgresRepository) ListRefUsages(schema, ref string, prefix bool) (*RefUsages, error) {
	out := &RefUsages{
		Ref:    ref,
		Prefix: prefix,
		Agents: []RefUsageAgent{},
		Skills: []RefUsageSkill{},
		Groups: []RefUsageGroup{},
	}

	// 1) agent grants
	cond, args := refUsageFilter("g", ref, prefix)
	q := fmt.Sprintf(`SELECT a.id, a.name, g.grant_kind, g.ref
		FROM %s.agent_tool_grants g
		JOIN %s.agents a ON a.id = g.agent_id
		WHERE %s
		ORDER BY a.name ASC, g.grant_kind ASC, g.ref ASC`, schema, schema, cond)
	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it RefUsageAgent
		if err := rows.Scan(&it.ID, &it.Name, &it.GrantKind, &it.Ref); err != nil {
			rows.Close()
			return nil, err
		}
		out.Agents = append(out.Agents, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// 2) skill dependencies
	cond, args = refUsageFilter("g", ref, prefix)
	q = fmt.Sprintf(`SELECT s.id, s.name, g.ref
		FROM %s.skill_tool_grants g
		JOIN %s.skills s ON s.id = g.skill_id
		WHERE %s
		ORDER BY s.name ASC, g.ref ASC`, schema, schema, cond)
	rows, err = db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it RefUsageSkill
		if err := rows.Scan(&it.ID, &it.Name, &it.Ref); err != nil {
			rows.Close()
			return nil, err
		}
		out.Skills = append(out.Skills, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// 3) custom group members
	cond, args = refUsageFilter("i", ref, prefix)
	q = fmt.Sprintf(`SELECT gp.name, i.ref
		FROM %s.tool_group_items i
		JOIN %s.tool_groups gp ON gp.id = i.group_id
		WHERE %s
		ORDER BY gp.name ASC, i.ref ASC`, schema, schema, cond)
	rows, err = db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it RefUsageGroup
		if err := rows.Scan(&it.Name, &it.Ref); err != nil {
			rows.Close()
			return nil, err
		}
		out.Groups = append(out.Groups, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out.Total = len(out.Agents) + len(out.Skills) + len(out.Groups)
	return out, nil
}
