package agentrelations

import (
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// PostgresRepository PostgreSQL 实现。
type PostgresRepository struct{}

// relationColumns 查询列（caller_name / callee_name 为关联结果，非 DB 列）。
func relationColumns(schema string) string {
	return "caller_id, callee_id, created_by, created_at, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = agent_relations.caller_id) as caller_name, " +
		"(SELECT name FROM " + schema + ".agents WHERE id = agent_relations.callee_id) as callee_name"
}

func scanRelation(scanner interface{ Scan(...interface{}) error }, m *models.AgentRelation) error {
	return scanner.Scan(&m.CallerID, &m.CalleeID, &m.CreatedBy, &m.CreatedAt, &m.CallerName, &m.CalleeName)
}

// Allowed 判断 caller 是否有权调用 callee（无行 = 不允许）。
func (r *PostgresRepository) Allowed(schema, callerID, calleeID string) (bool, error) {
	var ok bool
	err := db.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+schema+`.agent_relations
		WHERE caller_id = $1 AND callee_id = $2)`, callerID, calleeID).Scan(&ok)
	return ok, err
}

// ListByCaller 列出 caller 可调用的目标（出边）。
func (r *PostgresRepository) ListByCaller(schema, callerID string) ([]models.AgentRelation, error) {
	return r.list(schema, `WHERE caller_id = $1 ORDER BY callee_name ASC`, callerID)
}

// ListCallableByCaller 一次 JOIN 取回可调用目标摘要（含描述与状态）。
// 状态过滤（可运行）留由调用方按 models 的判定函数做，保持规则单一真源。
// supervisor 不在这里排除 —— 它可被调用，是否放行只看有没有这一行授权。
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

// ListByCallee 反查：哪些 agent 可以调用 callee（入边）。
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

// AddRelation 追加一条调用授权（幂等：同一 (caller, callee) 只保留一行）。
//
// 不用 ReplaceByCaller / ReplaceByCallee 拼：它们是全量替换 —— 单条操作走全量
// 会连带清掉该 caller 的其它出边（ReplaceByCallee 更狠，会清掉别人配的入边）。
func (r *PostgresRepository) AddRelation(schema, callerID, calleeID, operatorID string) error {
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.agent_relations (caller_id, callee_id, created_by)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, callerID, calleeID, xsql.UUID(operatorID))
	return err
}

// RemoveRelation 移除一条调用授权，返回删除行数（幂等报告用）。
func (r *PostgresRepository) RemoveRelation(schema, callerID, calleeID string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.agent_relations
		WHERE caller_id = $1 AND callee_id = $2`, callerID, calleeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReplaceByCaller 全量替换 caller 的出边（它能调用谁）。
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

// ReplaceByCallee 全量替换 callee 的入边（谁能调用它）。
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
