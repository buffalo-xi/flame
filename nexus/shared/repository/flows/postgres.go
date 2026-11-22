package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/secureval"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of workflow instance storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// sealEnvVars encrypts the type=secret values in env_vars before they are stored (see secureval.SealEnvVars).
//
// There are only two write paths (CreateFlow / UpdateFlow); the version snapshot and "load a version" copy the jsonb
// verbatim in SQL, ciphertext in and ciphertext out, so Go does not have to go through it again.
//
// The input is not modified: the caller (the handler's response, the execution context) needs the plaintext.
func sealEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	raw := jsonbBytes(j)
	sealed, err := secureval.SealEnvVars(raw)
	if err != nil {
		return j, err
	}
	if len(sealed) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(sealed)}, nil
}

// openEnvVars restores the ciphertext in env_vars back to plaintext after reading.
func openEnvVars(j xsql.JSONB) (xsql.JSONB, error) {
	raw := jsonbBytes(j)
	opened, err := secureval.OpenEnvVars(raw)
	if err != nil {
		return j, err
	}
	if len(opened) == 0 {
		return j, nil
	}
	return xsql.JSONB{Data: json.RawMessage(opened)}, nil
}

// jsonbBytes returns the raw bytes of a JSONB. xsql.JSONB.Data may be []byte / RawMessage / string, or a RawMessage
// produced by Scan; normalise all of them here.
func jsonbBytes(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case json.RawMessage:
		return []byte(v)
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(j.Data)
		if err != nil {
			return nil
		}
		return b
	}
}

// ListFlows lists every workflow instance.
func (r *PostgresRepository) ListFlows(authInfo *auth.AuthInfo, req *ListFlowsRequest) ([]models.Flow, int, error) {
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
	countQuery := "SELECT COUNT(*) FROM " + authInfo.Schema + ".flows" + whereClause
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

	// With no sort field given, default to created_at descending
	if len(req.SortBy) == 0 {
		sortClauses = append(sortClauses, "created_at desc")
	} else {
		// Multi-column sorting
		for i, sortBy := range req.SortBy {
			sortOrder := "asc"
			// Use the direction given for that column, when it is valid
			if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
				sortOrder = req.SortOrder[i]
			}
			sortClauses = append(sortClauses, sortBy+" "+sortOrder)
		}
	}

	// Build the ORDER BY clause
	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// Build the query (diagram is not selected; a NULL placeholder is returned instead)
	query := "SELECT id, name, NULL as diagram, description, created_at, updated_at, arguments, outputs, NULL as env_vars, NULL as flow_vars, timeout, max_exec_count, store_runs, session_memory, created_by, updated_by, (SELECT name FROM " + authInfo.Schema + ".users WHERE id = p.created_by) as created_by_name, (SELECT name FROM " + authInfo.Schema + ".users WHERE id = p.updated_by) as updated_by_name, endpoints, status FROM " + authInfo.Schema + ".flows p" + whereClause + " " + sortClause
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

	var results []models.Flow
	for rows.Next() {
		var model models.Flow
		var endpointsJSON xsql.UUID
		if err := rows.Scan(&model.ID, &model.Name, &model.Diagram, &model.Description, &model.CreatedAt, &model.UpdatedAt, &model.Arguments, &model.Outputs, &model.EnvVars, &model.FlowVars, &model.Timeout, &model.MaxExecCount, &model.StoreRuns, &model.SessionMemory, &model.CreatedBy, &model.UpdatedBy, &model.CreatedByName, &model.UpdatedByName, &endpointsJSON, &model.Status); err != nil {
			return nil, 0, err
		}
		if string(endpointsJSON) != "" {
			_ = json.Unmarshal([]byte(string(endpointsJSON)), &model.Endpoints)
		}
		results = append(results, model)
	}

	return results, total, nil
}

// ExistsFlowName reports whether a workflow of the same name already exists in the tenant (used by the agent name check).
func (r *PostgresRepository) ExistsFlowName(authInfo *auth.AuthInfo, name string) (bool, error) {
	var exists bool
	if err := db.DB.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.flows WHERE LOWER(name) = LOWER($1))`, name,
	).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// FindByName returns one row by name (used for the duplicate-name check and for "install over the same name").
// Missing returns (nil, nil).
//
// It resolves the id first and then goes through GetFlow: field mapping / endpoints decoding / version state live in one
// place only, and the id and status that an overwriting install needs are right there.
func (r *PostgresRepository) FindByName(authInfo *auth.AuthInfo, name string) (*models.Flow, error) {
	var id string
	err := db.DB.QueryRow(
		`SELECT id FROM `+authInfo.Schema+`.flows WHERE LOWER(name) = LOWER($1)`, name,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.GetFlow(authInfo, id)
}

// GetFlow returns one workflow instance by id.
func (r *PostgresRepository) GetFlow(authInfo *auth.AuthInfo, id string) (*models.Flow, error) {
	var model models.Flow
	var endpointsJSON xsql.UUID
	err := db.DB.QueryRow(`SELECT id, name, diagram, description, created_at, updated_at, arguments, outputs, env_vars, flow_vars, timeout, max_exec_count, store_runs, session_memory, created_by, updated_by, (SELECT name FROM `+authInfo.Schema+`.users WHERE id = p.created_by) as created_by_name, (SELECT name FROM `+authInfo.Schema+`.users WHERE id = p.updated_by) as updated_by_name, endpoints, status FROM `+authInfo.Schema+`.flows p WHERE id = $1`, id).Scan(&model.ID, &model.Name, &model.Diagram, &model.Description, &model.CreatedAt, &model.UpdatedAt, &model.Arguments, &model.Outputs, &model.EnvVars, &model.FlowVars, &model.Timeout, &model.MaxExecCount, &model.StoreRuns, &model.SessionMemory, &model.CreatedBy, &model.UpdatedBy, &model.CreatedByName, &model.UpdatedByName, &endpointsJSON, &model.Status)
	if err != nil {
		return nil, err
	}
	if string(endpointsJSON) != "" {
		_ = json.Unmarshal([]byte(string(endpointsJSON)), &model.Endpoints)
	}
	// The secret values in env_vars are ciphertext; restore them to plaintext here for everyone:
	// the executor, the admin response and the asset-pack export all expect plaintext, and handling the ciphertext
	// separately in each of them would eventually miss one.
	opened, oerr := openEnvVars(model.EnvVars)
	if oerr != nil {
		return nil, fmt.Errorf("failed to decrypt the env vars of workflow %s: %w", model.Name, oerr)
	}
	model.EnvVars = opened
	// Fill in "the current version + are there unreleased changes": the editor needs it to tell the user
	// "the draft you edited is not released yet, production still runs vN". A failed version lookup does not affect the
	// main flow.
	r.fillVersionState(authInfo, &model)

	return &model, nil
}

// UpdateStatus updates status only (1=active enabled | 2=disabled).
func (r *PostgresRepository) UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error {
	result, err := db.DB.Exec(`UPDATE `+authInfo.Schema+`.flows SET status = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		status, xsql.UUID(authInfo.ID), id)
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

// CreateFlow creates a workflow instance.
func (r *PostgresRepository) CreateFlow(authInfo *auth.AuthInfo, Instance *models.Flow) (string, error) {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}

	// Check the agents table for the same name (case insensitive, the same rule the agent side uses)
	var exists bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.agents WHERE LOWER(name) = LOWER($1))`, Instance.Name).Scan(&exists)
	if err != nil {
		tx.Rollback()
		return "", err
	}

	if exists {
		tx.Rollback()
		return "", fmt.Errorf("agent instance with name '%s' already exists", Instance.Name)
	}

	// The Go backend generates the uuid primary key
	id := uuid.NewString()
	envVars, serr := sealEnvVars(Instance.EnvVars)
	if serr != nil {
		tx.Rollback()
		return "", fmt.Errorf("failed to encrypt env vars: %w", serr)
	}
	_, err = tx.Exec(`INSERT INTO `+authInfo.Schema+`.flows (id, name, diagram, description, arguments, outputs, env_vars, flow_vars, timeout, max_exec_count, store_runs, session_memory, created_by, updated_by, endpoints, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, '[]'::jsonb, NOW(), NOW())`,
		id, Instance.Name, Instance.Diagram, Instance.Description, Instance.Arguments, Instance.Outputs, envVars, Instance.FlowVars, Instance.Timeout, Instance.MaxExecCount, Instance.StoreRuns, Instance.SessionMemory, xsql.UUID(authInfo.ID), xsql.UUID(authInfo.ID))
	if err != nil {
		tx.Rollback()
		return "", err
	}

	// Overwrite the breakpoints wholesale (delete, then insert)
	if err := replaceBreakpoints(tx, authInfo.Schema, id, Instance.Breakpoints); err != nil {
		tx.Rollback()
		return "", err
	}

	// Add one not-exposed RPC endpoint automatically: a flow being callable by other flows through the endpoint name,
	// and appearing in the tool catalogue, both depend on this row; without it the flow is an island that "nobody can
	// call and nothing hints at". Creating an endpoint grants no permission — exposing it is still a separate action
	// (exposed defaults to false).
	if err := svcendpoints.CreateManagedRPC(tx, authInfo.Schema, "flow", id, Instance.Name, string(Instance.Description), Instance.Arguments); err != nil {
		tx.Rollback()
		return "", err
	}

	// Commit
	if err := tx.Commit(); err != nil {
		return "", err
	}

	return id, nil
}

// UpdateFlow updates a workflow instance.
func (r *PostgresRepository) UpdateFlow(authInfo *auth.AuthInfo, id string, Instance *models.Flow) error {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	// Only the "normal" state may change the definition; a deprecated one is always refused (the locked state is gone:
	// production is governed by versions). The criterion changed from "does it have an exposed endpoint" to reading the
	// status: exposing an endpoint does not change the status, so status is the single source of truth.
	var curStatus int
	var oldName string
	err = tx.QueryRow(`SELECT status, name FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus, &oldName)
	if err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("this workflow is deprecated and cannot be saved; switch it back to the editing state first")
	}

	// Check the agents table for the same name (excluding the current row; case insensitive)
	var exists bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+authInfo.Schema+`.agents WHERE LOWER(name) = LOWER($1))`, Instance.Name).Scan(&exists)
	if err != nil {
		tx.Rollback()
		return err
	}

	if exists {
		tx.Rollback()
		return fmt.Errorf("agent instance with name '%s' already exists", Instance.Name)
	}

	// Run the update
	envVars, serr := sealEnvVars(Instance.EnvVars)
	if serr != nil {
		tx.Rollback()
		return fmt.Errorf("failed to encrypt env vars: %w", serr)
	}
	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET name = $1, diagram = $2, description = $3, arguments = $4, outputs = $5, env_vars = $6, flow_vars = $7, timeout = $8, max_exec_count = $9, store_runs = $10, session_memory = $11, updated_by = $12, updated_at = NOW() WHERE id = $13`,
		Instance.Name, Instance.Diagram, Instance.Description, Instance.Arguments, Instance.Outputs, envVars, Instance.FlowVars, Instance.Timeout, Instance.MaxExecCount, Instance.StoreRuns, Instance.SessionMemory, xsql.UUID(authInfo.ID), id)
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

	// Sync the flow's name / description / arguments to the related svc_endpoints (name, description, input_schema).
	// Restricted to target_type='flow': the endpoint name always equals the entity name, so a rename renames the public one.
	_, err = tx.Exec(
		`UPDATE `+authInfo.Schema+`.svc_endpoints SET name = $1, description = $2, input_schema = $3, updated_at = NOW() WHERE target_id = $4 AND target_type = 'flow'`,
		Instance.Name, Instance.Description, Instance.Arguments, id,
	)
	if err != nil {
		tx.Rollback()
		return err
	}

	// A rename has to carry the granted references along: agent grants / skill dependencies / tool groups store the
	// string local:rpc:<endpoint name>, and the endpoint name always equals the workflow name. Changing only flows and
	// svc_endpoints would leave those references silently pointing at the old name — the permission is still in the
	// table, yet the runtime cannot match it, which is extremely laborious to diagnose.
	//
	// Why the flows repository reaches into the grant tables: this migration must share the transaction with the rename
	// and the endpoint rename (otherwise an intermediate state of "the flow is called the new name, the grants point at
	// the old one" is left behind), and that transaction is right here in UpdateFlow.
	// Equality replacement only: a whole-group ref is local:rpc (no member) and is unaffected; mcp: / rpc: are external
	// connections, unrelated to the workflow name.
	if newName := strings.TrimSpace(Instance.Name); newName != "" && newName != oldName {
		oldRef := "local:rpc:" + oldName
		newRef := "local:rpc:" + newName
		for _, table := range []string{"agent_tool_grants", "skill_tool_grants", "tool_group_items"} {
			if _, err := tx.Exec(
				`UPDATE `+authInfo.Schema+`.`+table+` SET ref = $1 WHERE ref = $2`, newRef, oldRef,
			); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to migrate the workflow references in %s: %w", table, err)
			}
		}

		// rpc.endpoint inside diagram is the same class of problem: it stores the endpoint name (= the flow name), and
		// leaving it alone means other diagrams still write the old name and only the runtime reports "no such RPC
		// endpoint".
		if _, err := renameDiagramRefs(tx, authInfo.Schema, id, oldName, newName); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to migrate the endpoint references inside flow diagrams: %w", err)
		}
	}

	// Overwrite the breakpoints wholesale (delete, then insert)
	if err := replaceBreakpoints(tx, authInfo.Schema, id, Instance.Breakpoints); err != nil {
		tx.Rollback()
		return err
	}

	// Commit
	return tx.Commit()
}

// renameDiagramRefs rewrites rpc endpoint references pointing at the old name inside **other** flows' diagrams, returning
// the number of flows changed.
//
// Why it has to move them: diagram's config.rpc.endpoint stores the endpoint name (= the workflow name, see
// rpc_target.go); syncing only flows.name / svc_endpoints.name / the local:rpc grant refs would leave old diagrams
// silently pointing at a nonexistent endpoint — the existing grants and endpoints are all there, and only the runtime
// reports "no such RPC endpoint", which is very hard to connect back to a rename.
//
// Why parse and write back in Go: SQL's replace(diagram::text, ...) would also rewrite the same text inside descriptions,
// prompts and argument defaults. Here only an equality match counts (config.rpc.endpoint == oldName).
//
// A diagram that fails to parse is skipped rather than failing the whole rename: that is historical dirty data and must
// not block the user's rename. excludeID is the flow being renamed — its diagram was just overwritten by the content the
// user submitted and must not be rewritten a second time.
func renameDiagramRefs(tx *sql.Tx, schema, excludeID, oldName, newName string) (int, error) {
	rows, err := tx.Query(
		`SELECT id, diagram::text FROM `+schema+`.flows
		 WHERE id <> $2 AND diagram::text LIKE $1`, "%"+oldName+"%", excludeID)
	if err != nil {
		return 0, err
	}

	type hit struct{ id, raw string }
	hits := make([]hit, 0, 4)
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.raw); err != nil {
			_ = rows.Close()
			return 0, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	changed := 0
	for _, h := range hits {
		var doc map[string]interface{}
		if json.Unmarshal([]byte(h.raw), &doc) != nil {
			continue // dirty diagram: skip it, never block the rename
		}
		nodes, _ := doc["nodes"].([]interface{})
		dirty := false
		for _, raw := range nodes {
			node, _ := raw.(map[string]interface{})
			data, _ := node["data"].(map[string]interface{})
			cfg, _ := data["config"].(map[string]interface{})
			rpcCfg, _ := cfg["rpc"].(map[string]interface{})
			if rpcCfg == nil {
				continue
			}
			if ep, _ := rpcCfg["endpoint"].(string); ep == oldName {
				rpcCfg["endpoint"] = newName
				dirty = true
			}
		}
		if !dirty {
			continue
		}
		out, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		// updated_at is refreshed along with it: this is a real definition change, and the editor uses it to report
		// "unreleased changes". Note: when the referenced flow is deprecated, bypassing the state gate of
		// UpdateFlowDiagram here is **intentional** — repairing the reference must complete, otherwise the rename leaves
		// a diagram that can never run; the change still has to be released before it goes live.
		if _, err := tx.Exec(
			`UPDATE `+schema+`.flows SET diagram = $1::jsonb, updated_at = NOW() WHERE id = $2`,
			string(out), h.id,
		); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// UpdateFlowDiagram replaces only diagram (used by the edge tool): name / description / limits / breakpoints are untouched.
// The state gate matches UpdateFlow: only the "normal" state may change it (a deprecated flow is refused).
func (r *PostgresRepository) UpdateFlowDiagram(authInfo *auth.AuthInfo, id string, diagram xsql.JSONB) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	var curStatus int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus); err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("this workflow is deprecated and cannot be saved; switch it back to the editing state first")
	}

	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET diagram = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		diagram, xsql.UUID(authInfo.ID), id)
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

	return tx.Commit()
}

// UpdateFlowArguments replaces only arguments (the entry argument declaration): name / description / diagram / limits /
// breakpoints are untouched.
//
// svc_endpoints.input_schema must be changed in the same transaction: the endpoint's public argument description is
// derived from flows.arguments (the same rule as the sync block of the same name in UpdateFlow), and a mismatch would
// hand callers a stale schema. The state gate matches UpdateFlowDiagram: only the "normal" state may change it (a
// deprecated flow is refused).
func (r *PostgresRepository) UpdateFlowArguments(authInfo *auth.AuthInfo, id string, arguments xsql.JSONB) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	var curStatus int
	if err := tx.QueryRow(`SELECT status FROM `+authInfo.Schema+`.flows WHERE id = $1`, id).Scan(&curStatus); err != nil {
		tx.Rollback()
		return err
	}
	if !models.IsModifiable(curStatus) {
		tx.Rollback()
		return fmt.Errorf("this workflow is deprecated and cannot be saved; switch it back to the editing state first")
	}

	result, err := tx.Exec(`UPDATE `+authInfo.Schema+`.flows SET arguments = $1, updated_by = $2, updated_at = NOW() WHERE id = $3`,
		arguments, xsql.UUID(authInfo.ID), id)
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

	if _, err := tx.Exec(
		`UPDATE `+authInfo.Schema+`.svc_endpoints SET input_schema = $1, updated_at = NOW() WHERE target_id = $2 AND target_type = 'flow'`,
		arguments, id,
	); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// DeleteFlow deletes a workflow instance.
func (r *PostgresRepository) DeleteFlow(authInfo *auth.AuthInfo, id string) error {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	// Only an endpoint that is still exposed blocks the delete: a public entrance may be in use by callers outside the
	// tenant and must not vanish with the flow. A not-exposed endpoint (including the RPC endpoint added automatically
	// at creation) is derived data of the entity itself and goes with the flow — otherwise, once the automatic creation
	// exists, every flow would be undeletable for "having an endpoint".
	var exposedCount int
	err = tx.QueryRow(`SELECT COUNT(*) FROM `+authInfo.Schema+`.svc_endpoints WHERE target_id = $1 AND target_type = 'flow' AND exposed`, id).Scan(&exposedCount)
	if err != nil {
		tx.Rollback()
		return err
	}

	if exposedCount > 0 {
		tx.Rollback()
		return fmt.Errorf("this workflow still has an exposed endpoint; retract it first, then delete")
	}

	// Cascade the derived endpoints away (same semantics as DeleteByTarget, in the same transaction)
	if _, delErr := tx.Exec(`DELETE FROM `+authInfo.Schema+`.svc_endpoints WHERE target_id = $1 AND target_type = 'flow'`, id); delErr != nil {
		tx.Rollback()
		return delErr
	}

	// Delete
	result, err := tx.Exec(`DELETE FROM `+authInfo.Schema+`.flows WHERE id = $1`, id)
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

	// Commit
	return tx.Commit()
}

// replaceBreakpoints overwrites a flow's breakpoints wholesale inside the transaction (delete, then insert).
func replaceBreakpoints(tx *sql.Tx, schema string, flowId string, nodeIDs []string) error {
	if _, err := tx.Exec(`DELETE FROM `+schema+`.flow_breakpoints WHERE flow_id = $1`, flowId); err != nil {
		return err
	}
	for _, nodeID := range nodeIDs {
		if nodeID == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO `+schema+`.flow_breakpoints (flow_id, node_id, created_at) VALUES ($1, $2, NOW())`,
			flowId, nodeID,
		); err != nil {
			return err
		}
	}
	return nil
}

// ListBreakpoints lists a workflow's breakpoint node_ids.
func (r *PostgresRepository) ListBreakpoints(authInfo *auth.AuthInfo, flowId string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT node_id FROM `+authInfo.Schema+`.flow_breakpoints WHERE flow_id = $1`, flowId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		out = append(out, nodeID)
	}
	return out, rows.Err()
}

// SetBreakpoint sets or clears one breakpoint.
func (r *PostgresRepository) SetBreakpoint(authInfo *auth.AuthInfo, flowId string, nodeID string, on bool) error {
	if on {
		_, err := db.DB.Exec(
			`INSERT INTO `+authInfo.Schema+`.flow_breakpoints (flow_id, node_id, created_at) VALUES ($1, $2, NOW()) ON CONFLICT (flow_id, node_id) DO NOTHING`,
			flowId, nodeID,
		)
		return err
	}
	_, err := db.DB.Exec(
		`DELETE FROM `+authInfo.Schema+`.flow_breakpoints WHERE flow_id = $1 AND node_id = $2`,
		flowId, nodeID,
	)
	return err
}
