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
	"flame/nexus/shared/versions"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of agent storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// agentColumns are the columns read for an agent.
// updated_by_name comes from a joined lookup (it is not a table column) and carries the display name of "who updated it".
// Note: the column order must match the Scan arguments of scanAgent exactly.
func agentColumns(schema string) string {
	return "id, name, description, config, status, endpoints, tools_unrestricted, skill_discovery, is_supervisor, " +
		"COALESCE(default_language, ''), " +
		"created_by, updated_by, created_at, updated_at, " +
		"(SELECT name FROM " + schema + ".users WHERE id = agents.updated_by) as updated_by_name"
}

// scanAgent scans one row into an Agent.
func scanAgent(scanner interface{ Scan(...interface{}) error }, m *models.Agent) error {
	return scanner.Scan(
		&m.ID, &m.Name, &m.Description,
		&m.Config, &m.Status, &m.Endpoints,
		&m.ToolsUnrestricted, &m.SkillDiscovery, &m.IsSupervisor,
		&m.DefaultLanguage,
		&m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt,
		&m.UpdatedByName,
	)
}

// ListAgents lists every agent.
func (r *PostgresRepository) ListAgents(authInfo *auth.AuthInfo, req *ListAgentsRequest) ([]models.Agent, int, error) {
	// Build the filter
	whereClause := ""
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		whereClause += " WHERE name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	// Total
	countQuery := "SELECT COUNT(*) FROM " + authInfo.Schema + ".agents" + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Paging
	page := req.Page
	if page < 1 {
		page = 1
	}

	// limit semantics: negative means "all of them" (the admin UI, no LIMIT/OFFSET appended); otherwise <1 falls back to the default
	all := req.Limit < 0
	limit := req.Limit
	if !all && limit < 1 {
		limit = 10
	}

	offset := (page - 1) * limit

	// Sorting
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

	// Build the query
	query := "SELECT " + agentColumns(authInfo.Schema) + " FROM " + authInfo.Schema + ".agents" + whereClause + " " + sortClause
	if !all {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// Run it
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

// GetAgent returns one agent by id.
func (r *PostgresRepository) GetAgent(authInfo *auth.AuthInfo, id string) (*models.Agent, error) {
	var instance models.Agent
	// Reuse scanAgent so the column order can never drift away from agentColumns
	if err := scanAgent(db.DB.QueryRow(`SELECT `+agentColumns(authInfo.Schema)+` FROM `+authInfo.Schema+`.agents WHERE id = $1`, id), &instance); err != nil {
		return nil, err
	}

	return &instance, nil
}

// CreateAgent creates an agent.
func (r *PostgresRepository) CreateAgent(authInfo *auth.AuthInfo, agent *models.Agent, creatorAgentID string) (string, error) {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Defaults
	if agent.Status == 0 {
		agent.Status = xsql.Int(models.StatusNormal)
	}
	if agent.Config.IsNil() {
		agent.Config = xsql.JSONB{Data: map[string]any{}}
	}
	if agent.Endpoints.IsNil() {
		agent.Endpoints = xsql.JSONB{Data: []any{}}
	}
	// Supervisor starting stance: it is **one set** with the core-group materialisation below — the groups carry the
	// always-on capabilities, these two switches carry "candidates are not narrowed by allow" (tools are not restricted)
	// and "unbound shared skills are visible". Missing either one leaves a new supervisor with only the six resident
	// groups, i.e. the management surface is missing a leg.
	if agent.IsSupervisor {
		agent.ToolsUnrestricted = true
		agent.SkillDiscovery = true
	}

	// created_at / updated_at
	currentTime := time.Now()
	agent.CreatedAt = xsql.Time(currentTime)
	agent.UpdatedAt = xsql.Time(currentTime)
	agent.CreatedBy = xsql.UUID(authInfo.ID)
	agent.UpdatedBy = xsql.UUID(authInfo.ID)

	// The Go backend generates the uuid primary key (agents.id has no database default)
	id := uuid.NewString()
	_, err = tx.Exec(`INSERT INTO `+authInfo.Schema+`.agents (id, name, description, config, status, endpoints, tools_unrestricted, skill_discovery, is_supervisor, default_language, created_by, updated_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10,''), $11, $12, $13, $14)`,
		id, agent.Name, agent.Description,
		agent.Config, agent.Status, agent.Endpoints,
		agent.ToolsUnrestricted, agent.SkillDiscovery, agent.IsSupervisor,
		agent.DefaultLanguage,
		agent.CreatedBy, agent.UpdatedBy, agent.CreatedAt, agent.UpdatedAt,
	)
	if err != nil {
		return "", err
	}

	// Default grant: the creator may call the new agent (same transaction, so it is atomic)
	if creatorAgentID != "" {
		if _, err := tx.Exec(`INSERT INTO `+authInfo.Schema+`.agent_relations (caller_id, callee_id, created_by)
			VALUES ($1, $2, $3) ON CONFLICT (caller_id, callee_id) DO NOTHING`,
			creatorAgentID, id, xsql.UUID(authInfo.ID)); err != nil {
			return "", err
		}
	}

	// Default tool grants: an ordinary agent gets "read-only + workspace read/write + collaboration"
	// (see models.DefaultAgentToolGrants); a supervisor gets the core set (see models.SupervisorCoreGrants) — the same
	// materialisation path, only the seed list differs. This is the single source of truth for "the supervisor's
	// always-on tools": it used to be an all-on fallback in code, invisible in the database and impossible to adjust
	// per tenant (see docs/workspace-layout-and-supervisor-power.md §2.4).
	// They are written as required (always injected): allow is narrowed by skill dependencies (candidates ∩ allow in
	// resolve.go), which would make tools vanish inexplicably; required keeps them permanently present, and an admin can
	// still replace the whole group under "tool configuration".
	// Materialised row by row rather than as one preset: snapshot semantics — later edits to the list definition must not
	// silently change existing agents. Same transaction as agent_relations, so it is atomic.
	//
	// The criterion is the entity attribute (is_supervisor), not the name: main is merely the preinstalled supervisor.
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

	// Supervisor starter skills: bind the four authoring skills (idempotent; silently skipped while a skill is not yet
	// materialised). They belong to the same set as the core groups: the management tools are injected on demand with
	// their skill, so without the binding those groups never light up.
	if agent.IsSupervisor {
		if err := seedSupervisorSkills(tx, authInfo.Schema, id, authInfo.ID); err != nil {
			return "", err
		}
	}

	// Commit
	if err := tx.Commit(); err != nil {
		return "", err
	}

	return id, nil
}

// FindByName returns an agent by name (names are unique, so at most one row); missing returns (nil, nil).
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

// ExistsName reports whether an agent of the same name already exists in the tenant (excludeID, when set, ignores that id).
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

// UpdateAgent updates an agent.
func (r *PostgresRepository) UpdateAgent(authInfo *auth.AuthInfo, id string, agent *models.Agent) error {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Defaults
	if agent.Status == 0 {
		agent.Status = xsql.Int(models.StatusNormal)
	}
	// The config / endpoints columns are NOT NULL: this function overwrites every field, so a nil would be written as
	// NULL and trip the constraint; fall back to an empty object / array here (same as CreateAgent).
	if agent.Config.IsNil() {
		agent.Config = xsql.JSONB{Data: map[string]any{}}
	}
	if agent.Endpoints.IsNil() {
		agent.Endpoints = xsql.JSONB{Data: []any{}}
	}

	// updated_at
	agent.UpdatedAt = xsql.Time(time.Now())
	agent.UpdatedBy = xsql.UUID(authInfo.ID)

	// Read the old name: after a rename this agent's endpoint names must follow (the public name is always the entity name)
	var oldName string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, id).Scan(&oldName); err != nil {
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		return err
	}

	// Run the update (full-field overwrite: the caller must read the old instance first and carry tools_unrestricted /
	// skill_discovery over, otherwise those switches are written as the zero value false)
	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.agents SET name = $1, description = $2, config = $3, status = $4, endpoints = $5, tools_unrestricted = $6, skill_discovery = $7, default_language = NULLIF($8,''), updated_by = $9, updated_at = $10 WHERE id = $11`,
		agent.Name, agent.Description,
		agent.Config, agent.Status, agent.Endpoints,
		agent.ToolsUnrestricted, agent.SkillDiscovery,
		agent.DefaultLanguage,
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

	// Renamed -> rename all of this agent's protocol endpoints (public name = entity name)
	if oldName != agent.Name {
		if _, err := tx.Exec(`UPDATE `+authInfo.Schema+`.svc_endpoints SET name = $2, updated_at = NOW() WHERE target_id = $1 AND target_type = 'agent'`,
			id, agent.Name); err != nil {
			return err
		}
	}

	// Commit
	return tx.Commit()
}

// DeleteAgent deletes an agent.
// Everything is cleaned up inside one transaction: agent_skill_bindings (agent_id has no foreign key), the private skill
// records (skills.agent_id has no foreign key either; the skills' own dependents go with ON DELETE CASCADE), the
// agent_relations in both directions, the protocol endpoints (svc_endpoints.target_id is a polymorphic column with no
// foreign key), and finally the agents row — a failure anywhere rolls the whole thing back, so it never leaves the
// self-unhealable state of "the agent is still there, its skill records are gone".
func (r *PostgresRepository) DeleteAgent(authInfo *auth.AuthInfo, id string) error {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The preinstalled main agent may not be deleted (backstop covering every caller)
	var name string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, id).Scan(&name); err != nil {
		return err
	}
	if models.IsProtectedAgent(name) {
		return errors.New("protected agent " + name + " cannot be deleted")
	}

	// A released version is what production runs: deleting the row would take the agent out of service, so it is refused
	// and the prerequisite is named (unrelease). Same rule as "a version that is in use cannot be deleted", one level up.
	var released int
	switch err := tx.QueryRow(`SELECT version FROM `+authInfo.Schema+`.agent_versions
		WHERE agent_id = $1 AND is_current`, id).Scan(&released); {
	case err == nil:
		return &versions.ErrHasReleasedVersion{Version: released}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	// Clean up this agent's grants (same transaction as the delete, so it is atomic)
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.agent_skill_bindings WHERE agent_id = $1`, id); err != nil {
		return err
	}
	// Private skill records (skills.agent_id has no foreign key, so the cascade is the application's job).
	// It has to share the transaction with the agents row: if the second half of a two-step delete failed, it would leave
	// the self-unhealable state of "the agent is still there, the skill records are all gone". The deleted skills take
	// skill_tool_grants / skill_versions and other agents' bindings to them along through ON DELETE CASCADE on skills.
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.skills WHERE agent_id = $1`, id); err != nil {
		return err
	}
	// Clean up this agent's call grants (both directions: as caller or as callee)
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.agent_relations
		WHERE caller_id = $1 OR callee_id = $1`, id); err != nil {
		return err
	}

	// The agent's protocol endpoints go with it, for the same reason the skill records do: svc_endpoints.target_id is a
	// polymorphic column (agent / flow) with no foreign key, so only the application can cascade. Left behind, they would
	// keep the names reserved (svc_endpoints_unique_1 on name + protocol, so recreating an agent with the same name would
	// hit the constraint) and keep an exposed entrance answering for an agent that no longer exists.
	if _, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.svc_endpoints
		WHERE target_id = $1 AND target_type = 'agent'`, id); err != nil {
		return err
	}

	// Delete the agent
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

	// Commit
	return tx.Commit()
}

// GetUnrestricted reads the "unrestricted tools" switch (agents.tools_unrestricted; a missing agent returns false, nil).
func (r *PostgresRepository) GetUnrestricted(authInfo *auth.AuthInfo, agentID string) (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT tools_unrestricted FROM `+authInfo.Schema+`.agents WHERE id = $1`, agentID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v, err
}

// SetUnrestricted sets the "unrestricted tools" switch (single-column UPDATE; no other column is touched).
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

// SetOutputLanguage sets agents.default_language (single-column UPDATE; no other column is touched) and clears it when
// tag is empty.
//
// It deliberately avoids UpdateAgent: that one overwrites every column, so a caller that only wants to change the
// language would have to carry the description / config / switches over or wipe them (same reasoning as SetUnrestricted /
// SetSupervisor).
func (r *PostgresRepository) SetOutputLanguage(authInfo *auth.AuthInfo, agentID, tag string) error {
	result, err := db.DB.Exec(
		`UPDATE `+authInfo.Schema+`.agents SET default_language = NULLIF($1,''), updated_at = NOW() WHERE id = $2`,
		tag, agentID,
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

// SetSupervisor sets an agent's super-privilege attribute (single-column UPDATE).
//
// It deliberately avoids UpdateAgent: that one overwrites every field, so any caller that "only changes the description"
// and forgets to carry the switch over would silently wipe the privilege. Attribute changes must be explicit
// single-column operations (same as SetUnrestricted).
//
// When revoking, the transaction locks every supervisor row before counting — with two concurrent revocations the later
// transaction waits for the first to commit and therefore sees the real remaining count; otherwise both read 2 and end
// with 0, and the tenant permanently loses its management surface (nobody is left who could grant it back).
func (r *PostgresRepository) SetSupervisor(authInfo *auth.AuthInfo, agentID string, isSupervisor bool) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Target name: a protected agent (main) may never be demoted — it cannot be renamed or deleted either, so demoting it
	// would leave a dead corner that holds the preinstalled entrance but has no privilege, never restorable.
	var name string
	if err := tx.QueryRow(`SELECT name FROM `+authInfo.Schema+`.agents WHERE id = $1`, agentID).Scan(&name); err != nil {
		return err
	}

	if !isSupervisor {
		if !models.CanDemoteSupervisor(name) {
			return errors.New("the super privilege of a preinstalled supervisor (such as main) cannot be revoked: its name is a fixed entrance and demoting it is irreversible")
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
		// The target was not a supervisor to begin with: return idempotently and do not take part in the "last one" check.
		if !targetIsSupervisor {
			return tx.Commit()
		}
		if others == 0 {
			return errors.New("the last supervisor cannot be demoted: the tenant would be left with no management capability")
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
	// The "starter kit" on grant: the core group + the two switches + the four authoring skills.
	//
	// In practice this is the only route: the frontend cannot tick supervisor at creation time (it has to be saved
	// first), so a supervisor is 100% "created as an ordinary agent (12 single-tool seeds), then granted".
	// Without filling it in here, the result is a shell that has an identity but no hands.
	//
	// Revoking (false) deliberately **does not roll this back**: tool and skill bindings are assets, and silently
	// reclaiming them is harder to diagnose than leaving them; besides, after a revoke the builtin:agent-admin class of
	// tools is stripped at the execution layer by IsSupervisorOnlyTool, so keeping them executes nothing anyway.
	if isSupervisor {
		if err := promoteSupervisor(tx, authInfo.Schema, agentID, authInfo.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// promoteSupervisor fills in the "starter kit" when the super privilege is granted.
//
// Tool grants are handled in two ways:
//   - the existing required rows are exactly the ordinary agent's default seed (never customised) → replace the whole
//     batch with the core group, so "must import" shows a group instead of a long list of tools (a group is atomic, and
//     a wrong deletion is obvious at a glance);
//   - already customised (one row more or one row less counts) → only append the core group, never delete: that is the
//     admin's hand-made configuration.
func promoteSupervisor(tx *sql.Tx, schema, agentID, operatorID string) error {
	// 1) The two switches
	if _, err := tx.Exec(`UPDATE `+schema+`.agents
		SET tools_unrestricted = true, skill_discovery = true, updated_at = NOW()
		WHERE id = $1`, agentID); err != nil {
		return err
	}
	// 2) Default single-tool seed -> core group
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
	// 3) The four authoring skills
	return seedSupervisorSkills(tx, schema, agentID, operatorID)
}

// seedSupervisorSkills binds models.SupervisorSeedSkills to the given agent.
//
// Only tenant-level skills (agent_id IS NULL) that are not deprecated are accepted: a private skill inherently belongs to
// one agent and binding it to another is meaningless; a skill that is not materialised yet is skipped silently, so it
// never blocks creating an agent or granting.
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

// requiredGrantRefs reads every "must import" ref of an agent (groups and single tools alike).
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

// isDefaultAgentSeedOnly judges "never customised" only on strict equality (not one row more, not one row less).
// A subset is not enough: a subset may come from an admin's manual trimming, which must not be overwritten wholesale.
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

// UpdateStatus updates status only (1=active enabled | 2=disabled).
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
