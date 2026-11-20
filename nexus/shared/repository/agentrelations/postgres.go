package agentrelations

import (
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// PostgresRepository is the PostgreSQL implementation.
type PostgresRepository struct{}

// relationColumns are the selected columns (caller_name / callee_name are joined results, not table columns).
func relationColumns(schema string) string {
	return "caller_id, callee_id, created_by, created_at, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = agent_relations.caller_id) as caller_name, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = agent_relations.callee_id) as callee_name"
}

func scanRelation(scanner interface{ Scan(...interface{}) error }, m *models.AgentRelation) error {
	return scanner.Scan(&m.CallerID, &m.CalleeID, &m.CreatedBy, &m.CreatedAt, &m.CallerName, &m.CalleeName)
}

// Allowed reports whether the caller may call the callee (no row = not allowed).
func (r *PostgresRepository) Allowed(schema, callerID, calleeID string) (bool, error) {
	var ok bool
	err := db.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+schema+`.agent_relations
		WHERE caller_id = $1 AND callee_id = $2)`, callerID, calleeID).Scan(&ok)
	return ok, err
}

// ListByCaller lists the targets the caller may call (out-edges).
func (r *PostgresRepository) ListByCaller(schema, callerID string) ([]models.AgentRelation, error) {
	return r.list(schema, `WHERE caller_id = $1 ORDER BY callee_name ASC`, callerID)
}

// ListCallableByCaller fetches the callable target summaries in one JOIN (description and status included).
// The state filter (runnable) is left to the caller through the models predicate, keeping the rule in one source of
// truth. The supervisor is not excluded here — it may be called, and whether it is let through depends only on whether
// this grant row exists.
func (r *PostgresRepository) ListCallableByCaller(schema, callerID string) ([]models.CallableAgent, error) {
	rows, err := db.DB.Query(`SELECT a.id, a.name, COALESCE(a.description, ''), a.status, a.is_supervisor
		FROM `+schema+`.agent_relations r
		JOIN `+schema+`.agents a ON a.id = r.callee_id
		WHERE r.caller_id = $1
		ORDER BY a.name ASC`, callerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.CallableAgent, 0)
	for rows.Next() {
		var m models.CallableAgent
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.Status, &m.IsSupervisor); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListByCallee is the reverse lookup: which agents may call the callee (in-edges).
func (r *PostgresRepository) ListByCallee(schema, calleeID string) ([]models.AgentRelation, error) {
	return r.list(schema, `WHERE callee_id = $1 ORDER BY caller_name ASC`, calleeID)
}

func (r *PostgresRepository) list(schema, where, arg string) ([]models.AgentRelation, error) {
	rows, err := db.DB.Query(`SELECT `+relationColumns(schema)+` FROM `+schema+`.agent_relations `+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AgentRelation, 0)
	for rows.Next() {
		var m models.AgentRelation
		if err := scanRelation(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddRelation appends one call grant (idempotent: one row per (caller, callee)).
//
// ReplaceByCaller / ReplaceByCallee are not used for this: they replace everything — a single operation going through a
// wholesale replace would take the caller's other out-edges with it (ReplaceByCallee is worse: it wipes the in-edges
// others configured).
func (r *PostgresRepository) AddRelation(schema, callerID, calleeID, operatorID string) error {
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.agent_relations (caller_id, callee_id, created_by)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, callerID, calleeID, xsql.UUID(operatorID))
	return err
}

// RemoveRelation removes one call grant, returning the number of rows deleted (for an idempotent report).
func (r *PostgresRepository) RemoveRelation(schema, callerID, calleeID string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.agent_relations
		WHERE caller_id = $1 AND callee_id = $2`, callerID, calleeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReplaceByCaller replaces the caller's out-edges wholesale (whom it may call).
func (r *PostgresRepository) ReplaceByCaller(schema, callerID string, calleeIDs []string, operatorID string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_relations WHERE caller_id = $1`, callerID); err != nil {
		return err
	}
	op := xsql.UUID(operatorID)
	for _, cid := range calleeIDs {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_relations (caller_id, callee_id, created_by)
			VALUES ($1, $2, $3)`, callerID, cid, op); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceByCallee replaces the callee's in-edges wholesale (who may call it).
func (r *PostgresRepository) ReplaceByCallee(schema, calleeID string, callerIDs []string, operatorID string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.agent_relations WHERE callee_id = $1`, calleeID); err != nil {
		return err
	}
	op := xsql.UUID(operatorID)
	for _, cid := range callerIDs {
		if _, err := tx.Exec(`INSERT INTO `+schema+`.agent_relations (caller_id, callee_id, created_by)
			VALUES ($1, $2, $3)`, cid, calleeID, op); err != nil {
			return err
		}
	}
	return tx.Commit()
}
