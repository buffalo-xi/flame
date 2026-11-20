package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of service endpoint storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// ErrManagedEndpoint means a managed endpoint cannot be deleted on its own.
//
// A flow's rpc endpoint is added automatically when the flow is created (see CreateManagedRPC): its name / description /
// input_schema are all derived from the entity, and the only independent state is exposed. Deleting it is not "cleaning
// up history" but turning references such as local:rpc:<name> (held in the grant tables, skill dependencies and tool
// groups) into dangling ones, and making the flow uncallable by name at the same time. To disable it, use the flow
// status (disabled / deprecated) or retract it.
var ErrManagedEndpoint = errors.New("the RPC endpoint is managed with its flow and cannot be deleted on its own; to disable it, disable or deprecate the flow")

// ErrEndpointExposed means an endpoint that is still exposed cannot be deleted.
//
// An exposed endpoint may be in use by callers outside the tenant right now, so deleting it silently is the same as the
// service disappearing; retract it first.
var ErrEndpointExposed = errors.New("the endpoint is still exposed; retract it first, then delete")

// columns are the columns selected from svc_endpoints.
const columns = "id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed"

// listColumns are the columns for a list query (prefixed with e.: after joining agents / flows, same-named columns such as
// name would be ambiguous).
const listColumns = "e.id, e.name, e.description, e.created_at, e.updated_at, e.target_id, e.target_type, e.protocol, e.input_schema, e.exposed"

// scanEndpoint scans a row into SvcEndpoint.
func scanEndpoint(scanner interface{ Scan(...interface{}) error }, e *models.SvcEndpoint) error {
	return scanner.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedAt, &e.UpdatedAt, &e.TargetID, &e.TargetType, &e.Protocol, &e.InputSchema, &e.Exposed)
}

// scopeColumns are the columns for a single-row query: columns plus one subquery for "is the target the super agent".
//
// A subquery rather than a JOIN: GetByID / FindByName match by primary key / name and carry no JOIN of their own, while
// the credential scope rule "everyone except the super agent" needs this flag (rpc's findEndpoint sits on a hot path).
func scopeColumns(schema string) string {
	return columns + fmt.Sprintf(`, CASE WHEN target_type = 'agent'
		THEN COALESCE((SELECT is_supervisor FROM %s.agents WHERE id = svc_endpoints.target_id), false)
		ELSE false END`, schema)
}

// scanEndpointScoped scans "endpoint + is the target a supervisor".
func scanEndpointScoped(scanner interface{ Scan(...interface{}) error }, e *models.SvcEndpoint) error {
	return scanner.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedAt, &e.UpdatedAt, &e.TargetID, &e.TargetType, &e.Protocol, &e.InputSchema, &e.Exposed, &e.TargetSupervisor)
}

// GetByID returns one service endpoint by id.
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error) {
	var svcEndpoint models.SvcEndpoint
	err := scanEndpointScoped(db.DB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM %s.svc_endpoints WHERE id = $1`, scopeColumns(authInfo.Schema), authInfo.Schema), id,
	), &svcEndpoint)
	if err != nil {
		return nil, err
	}
	return &svcEndpoint, nil
}

// ensureExposableTarget validates the target's state before the endpoint is exposed: it **refuses only "deprecated"**.
//
// "Offering an entrance to the outside" and "may the definition change" are two different things: the normal state can of
// course be exposed.
//
// History: this used to switch the editing state to locked automatically (exposing meant locking). The side effects were
// too large — changing one line of a prompt required unlocking first, and unlocking retracted the endpoint and cut the
// traffic on the way. It now works as "exposing an endpoint does not change the state; the editor warns when the
// definition has changed".
func ensureExposableTarget(schema, targetType, targetID string) error {
	targetTable := "flows"
	if targetType == "agent" {
		targetTable = "agents"
	}
	var status int
	if err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT status FROM %s.%s WHERE id = $1`, schema, targetTable), targetID,
	).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%s with ID '%s' not found", targetType, targetID)
		}
		return err
	}
	if status == models.StatusDeprecated {
		return fmt.Errorf("%s is deprecated (status=%d) and cannot expose an endpoint", targetType, status)
	}
	return nil
}

// Create creates a service endpoint.
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error) {
	// Validate TargetType
	if svcEndpoint.TargetType != "agent" && svcEndpoint.TargetType != "flow" {
		return "", fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

	// Validate Protocol
	if svcEndpoint.Protocol != "mcp" && svcEndpoint.Protocol != "rpc" && svcEndpoint.Protocol != "llm" {
		return "", fmt.Errorf("protocol must be one of 'mcp', 'rpc', 'llm'")
	}

	// Validate that TargetID is not empty
	if svcEndpoint.TargetID == "" {
		return "", fmt.Errorf("target_id cannot be empty")
	}

	// Validate that the target exists
	targetTable := "agents"
	if svcEndpoint.TargetType == "flow" {
		targetTable = "flows"
	}
	// The endpoint name always equals the target entity name (the public name is the entity name, so the two can never
	// disagree)
	var targetName string
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT name FROM %s.%s WHERE id = $1`, authInfo.Schema, targetTable),
		svcEndpoint.TargetID,
	).Scan(&targetName)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%s with ID '%s' not found", svcEndpoint.TargetType, svcEndpoint.TargetID)
		}
		return "", fmt.Errorf("error checking target: %v", err)
	}
	// A name passed in must equal the entity name (the endpoint name always equals the entity name; a separate name is not
	// allowed)
	if svcEndpoint.Name != "" && !strings.EqualFold(svcEndpoint.Name, targetName) {
		return "", fmt.Errorf("endpoint name must equal the %s name %q", svcEndpoint.TargetType, targetName)
	}
	svcEndpoint.Name = targetName

	// A workflow endpoint is the tool name on the outside (MCP tool.name / local__<name> on the agent side / the grant ref
	// local:rpc:<name>), and all three consumers accept only ASCII letters, digits and hyphens. An invalid name is refused
	// here outright — otherwise it would silently produce an endpoint that "is exposed but cannot be granted and cannot be
	// called by the model" (that is how workflows with legacy Chinese names leaked out).
	if svcEndpoint.TargetType == "flow" && !paths.ValidName(targetName) {
		return "", fmt.Errorf("workflow name %q cannot be used as an endpoint name: only letters, digits and hyphens are allowed "+
			"(no leading hyphen, at most 64); rename it in the editor first, then expose the endpoint", targetName)
	}

	// Created already exposed: validate the target state first (only "deprecated" is refused); exposed defaults to false,
	// so this is usually not reached
	if svcEndpoint.Exposed.Bool() {
		if err := ensureExposableTarget(authInfo.Schema, svcEndpoint.TargetType, svcEndpoint.TargetID); err != nil {
			return "", err
		}
	}

	// When creating a flow endpoint, input_schema is always overwritten with flows.arguments (matching the sync logic in
	// UpdateFlow)
	if svcEndpoint.TargetType == "flow" {
		var args xsql.JSONB
		err = db.DB.QueryRow(
			fmt.Sprintf(`SELECT arguments FROM %s.flows WHERE id = $1`, authInfo.Schema),
			svcEndpoint.TargetID,
		).Scan(&args)
		if err != nil {
			return "", fmt.Errorf("error loading flow arguments: %v", err)
		}
		svcEndpoint.InputSchema = args
	}

	// Check whether the same target + protocol already exists
	var dupCount int
	err = db.DB.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2 AND protocol = $3`, authInfo.Schema),
		svcEndpoint.TargetID, svcEndpoint.TargetType, svcEndpoint.Protocol,
	).Scan(&dupCount)
	if err != nil {
		return "", fmt.Errorf("error checking duplicate: %v", err)
	}
	if dupCount > 0 {
		return "", fmt.Errorf("endpoint already exists for %s '%s' with protocol '%s'", svcEndpoint.TargetType, svcEndpoint.TargetID, svcEndpoint.Protocol)
	}

	// Generate a standalone UUID
	endpointID := uuid.NewString()

	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	// Insert into svc_endpoints (a new endpoint defaults to exposed=false; it must be exposed explicitly to serve outside
	// the tenant)
	// created_at / updated_at are written explicitly as NOW() so they never end up empty by relying on column defaults
	_, err = tx.Exec(
		fmt.Sprintf(`INSERT INTO %s.svc_endpoints (id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed) VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, $6, $7, $8)`, authInfo.Schema),
		endpointID, svcEndpoint.Name, svcEndpoint.Description, svcEndpoint.TargetID, svcEndpoint.TargetType, svcEndpoint.Protocol, svcEndpoint.InputSchema, svcEndpoint.Exposed,
	)
	if err != nil {
		return "", err
	}

	// Sync the mirror (the endpoint name is not written back to the entity; the name was forced to the entity name above)
	if err := syncEndpoints(tx, authInfo.Schema, svcEndpoint.TargetType, svcEndpoint.TargetID); err != nil {
		return "", err
	}

	svcEndpoint.ID = endpointID
	return endpointID, nil
}

// CreateManagedRPC adds an RPC endpoint (exposed=false) to an entity inside the caller's transaction, and syncs the
// mirror.
//
// A flow being callable by other flows through the endpoint name (an rpc node stores the endpoint name) and appearing in
// the tool catalogue both depend on this row; and its name / description / input_schema are all derived from the entity —
// so it is a protocol slot of the entity rather than a user-made asset (deleting it on its own is covered by
// ErrManagedEndpoint).
//
// An entity whose name is invalid (a Chinese name and the like) gets none: the endpoint name is also the tool name and
// the grant ref local:rpc:<name>, so what it would produce is a dead endpoint the model cannot call and nobody can grant
// (Create refuses the very same thing).
func CreateManagedRPC(tx *sql.Tx, schema, targetType, targetID, name, description string, inputSchema xsql.JSONB) error {
	if !paths.ValidName(name) {
		return nil
	}
	_, err := tx.Exec(
		fmt.Sprintf(`INSERT INTO %s.svc_endpoints (id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed) VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, 'rpc', $6, false)`, schema),
		uuid.NewString(), name, description, targetID, targetType, inputSchema,
	)
	if err != nil {
		return err
	}
	return syncEndpoints(tx, schema, targetType, targetID)
}

// FindByName looks an endpoint up exactly by protocol + name (used to locate one by name from outside, case insensitive);
// missing returns (nil, nil). exposed is not filtered: a direct call inside the tenant may use a not-exposed endpoint.
func (r *PostgresRepository) FindByName(authInfo *auth.AuthInfo, protocol, name string) (*models.SvcEndpoint, error) {
	var e models.SvcEndpoint
	err := scanEndpointScoped(db.DB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM %s.svc_endpoints WHERE protocol = $1 AND LOWER(name) = LOWER($2) LIMIT 1`, scopeColumns(authInfo.Schema), authInfo.Schema),
		protocol, name,
	), &e)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// UpdateExposed updates the endpoint's exposed state (no other field is affected).
func (r *PostgresRepository) UpdateExposed(authInfo *auth.AuthInfo, id string, exposed bool) error {
	// Read target_type / target_id first
	var targetType, targetID string
	if err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT target_type, target_id FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id,
	).Scan(&targetType, &targetID); err != nil {
		return err
	}

	// Validate the target state before exposing (retracting is not validated: any state may be turned off)
	if exposed {
		if err := ensureExposableTarget(authInfo.Schema, targetType, targetID); err != nil {
			return err
		}
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	result, err := tx.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET exposed = $1, updated_at = NOW() WHERE id = $2`, authInfo.Schema),
		exposed, id,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}
	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Update updates a service endpoint.
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error {
	// The name does not take part in an update: the endpoint name always equals the target entity name (a rename goes
	// through the entity rename flow plus the repository-level sync)
	result, err := db.DB.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET description = $1, input_schema = $2, updated_at = NOW() WHERE id = $3`, authInfo.Schema),
		svcEndpoint.Description, svcEndpoint.InputSchema, id,
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

	return nil
}

// Delete deletes one service endpoint by id.
//
// Two refusals (DeleteByTarget is unaffected: that is the cascade when the entity is deleted):
//   - a flow's rpc endpoint is managed by its flow, see ErrManagedEndpoint;
//   - an endpoint that is still exposed must be retracted first, see ErrEndpointExposed.
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	// Read this endpoint's target_type / target_id / protocol / exposed first
	var targetType, targetID string
	var protocol sql.NullString
	var exposed bool
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT target_type, target_id, protocol, exposed FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id,
	).Scan(&targetType, &targetID, &protocol, &exposed)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	if targetType == "flow" && protocol.String == "rpc" {
		return ErrManagedEndpoint
	}
	if exposed {
		return ErrEndpointExposed
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	// Delete the endpoint
	_, err = tx.Exec(fmt.Sprintf(`DELETE FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id)
	if err != nil {
		return err
	}

	// Sync the target's endpoints (the same mechanism for agent and flow)
	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		return err
	}

	return nil
}

// DeleteByTarget deletes every protocol endpoint of a target and clears the endpoint marker on the target.
func (r *PostgresRepository) DeleteByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error {
	if targetType != "agent" && targetType != "flow" {
		return fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	// Delete every endpoint of the target
	_, err = tx.Exec(
		fmt.Sprintf(`DELETE FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2`, authInfo.Schema),
		targetID, targetType,
	)
	if err != nil {
		return err
	}

	// Clear the target's endpoint record: empty endpoints (the same mechanism for agent and flow)
	targetTable := "flows"
	if targetType != "flow" {
		targetTable = "agents"
	}
	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.%s SET endpoints = '[]'::jsonb WHERE id = $1`, authInfo.Schema, targetTable),
		targetID,
	)
	if err != nil {
		return err
	}

	return nil
}

// RetractByTarget retracts every protocol endpoint of a target (only exposed=false; the endpoint records and their
// configuration are kept).
func (r *PostgresRepository) RetractByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error {
	if targetType != "agent" && targetType != "flow" {
		return fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET exposed = false, updated_at = NOW() WHERE target_id = $1 AND target_type = $2`, authInfo.Schema),
		targetID, targetType,
	)
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// endpointWhere builds the WHERE clause and its arguments from the filters (no paging or ordering).
// Column names always carry the e. prefix: after joining agents / flows, same-named columns such as name would be
// ambiguous.
func endpointWhere(req *ListSvcEndpointsRequest) (string, []interface{}) {
	whereClause := ""
	args := []interface{}{}
	addCond := func(cond string, v interface{}) {
		if whereClause == "" {
			whereClause = " WHERE " + fmt.Sprintf(cond, len(args)+1)
		} else {
			whereClause += " AND " + fmt.Sprintf(cond, len(args)+1)
		}
		args = append(args, v)
	}

	if req.Name != "" {
		addCond("e.name ILIKE $%d", "%"+req.Name+"%")
	}
	if req.TargetType != "" {
		addCond("e.target_type = $%d", req.TargetType)
	}
	if req.TargetID != "" {
		addCond("e.target_id = $%d", req.TargetID)
	}
	if req.Protocol != "" {
		addCond("e.protocol = $%d", req.Protocol)
	}
	if req.Exposed != nil {
		addCond("e.exposed = $%d", *req.Exposed)
	}
	return whereClause, args
}

// endpointOrderBy builds the ORDER BY clause (name ascending by default); the result carries its own leading space.
func endpointOrderBy(req *ListSvcEndpointsRequest) string {
	sortClauses := []string{}
	if len(req.SortBy) == 0 {
		sortClauses = append(sortClauses, "e.name asc")
	} else {
		for i, sortBy := range req.SortBy {
			sortOrder := "asc"
			if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
				sortOrder = req.SortOrder[i]
			}
			sortClauses = append(sortClauses, "e."+sortBy+" "+sortOrder)
		}
	}
	return " ORDER BY " + strings.Join(sortClauses, ", ")
}

// endpointSelectSQL builds the body of the query: a LEFT JOIN onto the target entity for its name (empty when the entity
// is gone). An empty whereClause means no WHERE; the caller appends ordering and paging itself.
func endpointSelectSQL(schema, whereClause string) string {
	return fmt.Sprintf(`SELECT %s, COALESCE(a.name, f.name, '') AS target_name,
			COALESCE(a.is_supervisor, false) AS target_supervisor
		FROM %s.svc_endpoints e
		LEFT JOIN %s.agents a ON e.target_type = 'agent' AND a.id = e.target_id
		LEFT JOIN %s.flows f ON e.target_type = 'flow' AND f.id = e.target_id`,
		listColumns, schema, schema, schema) + whereClause
}

// scanEndpointRows scans the "endpoint + target_name" result set.
func scanEndpointRows(rows *sql.Rows) ([]models.SvcEndpoint, error) {
	var results []models.SvcEndpoint
	for rows.Next() {
		var svcEndpoint models.SvcEndpoint
		if err := rows.Scan(&svcEndpoint.ID, &svcEndpoint.Name, &svcEndpoint.Description,
			&svcEndpoint.CreatedAt, &svcEndpoint.UpdatedAt, &svcEndpoint.TargetID, &svcEndpoint.TargetType,
			&svcEndpoint.Protocol, &svcEndpoint.InputSchema, &svcEndpoint.Exposed,
			&svcEndpoint.TargetName, &svcEndpoint.TargetSupervisor); err != nil {
			return nil, err
		}
		results = append(results, svcEndpoint)
	}
	return results, rows.Err()
}

// List lists service endpoints (LEFT JOIN onto the target entity, carrying target_name).
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error) {
	whereClause, args := endpointWhere(req)

	// Total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.svc_endpoints e", authInfo.Schema) + whereClause
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
	limit := req.Limit
	if limit < 1 {
		limit = 10
	}
	offset := (page - 1) * limit

	query := endpointSelectSQL(authInfo.Schema, whereClause) + endpointOrderBy(req) +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	results, err := scanEndpointRows(rows)
	if err != nil {
		return nil, 0, err
	}

	return results, total, nil
}

// ListAll takes every endpoint matching the filters (no paging, no total count).
//
// The only difference from List is the absence of paging: List's default limit=10 silently truncates, so every scenario
// that "loads the whole catalogue into memory" (tool registration for MCP / RPC) must come through here, otherwise
// everything from the 11th endpoint on vanishes for no reason.
func (r *PostgresRepository) ListAll(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, error) {
	whereClause, args := endpointWhere(req)
	query := endpointSelectSQL(authInfo.Schema, whereClause) + endpointOrderBy(req)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEndpointRows(rows)
}

// syncEndpoints re-aggregates a target's endpoint protocols from svc_endpoints and writes them back to its endpoints
// jsonb column. agent and flow share the mechanism (target_type picks the table).
func syncEndpoints(tx *sql.Tx, schema, targetType, targetID string) error {
	targetTable := "flows"
	if targetType == "agent" {
		targetTable = "agents"
	}

	rows, err := tx.Query(
		fmt.Sprintf(`SELECT protocol, exposed FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2 ORDER BY created_at ASC`, schema),
		targetID, targetType,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	endpoints := []models.EndpointProtocol{}
	for rows.Next() {
		var p models.EndpointProtocol
		if err := rows.Scan(&p.Protocol, &p.Exposed); err != nil {
			return err
		}
		endpoints = append(endpoints, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}

	// Only the endpoints column is written back, **status is left alone**: exposing an endpoint and "may the definition
	// change" are decoupled now. The old rule "an exposed endpoint implies locking" is gone (changing one line of a
	// prompt required unlocking, and unlocking retracted the endpoint — the cost outweighed the benefit). The locked
	// state has been removed from flows / agents / skills alike.
	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.%s SET endpoints = $2 WHERE id = $1`, schema, targetTable),
		targetID, endpointsJSON,
	)
	return err
}
