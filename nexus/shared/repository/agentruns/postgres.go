package agentruns

import (
	"database/sql"
	"fmt"
	"strings"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
)

// PostgresRepository is the PostgreSQL implementation of agent run log storage.
type PostgresRepository struct{}

// agentRunColumns is the read column list; the order must match scanAgentRun one to one.
//
// session_id / user_id / run_type / err_msg are nullable (a stateless run has no session, an anonymous
// caller has no user). They are folded to the empty string in SQL for the same reason as
// sessionrecords.sessionRecordColumns: scanning a NULL into a plain string fails with
// "converting NULL to string is unsupported" and would turn the whole page into a 500.
//
// The run identity is the row id; session_records.run_id carries the same value.
//
// session_id::text is required: COALESCE on a uuid column and an empty string literal does not type-check.
// id is the run identity (one run is one row), so there is no separate run_id column to read.
const agentRunColumns = `id, agent_id, COALESCE(session_id::text, ''), COALESCE(user_id::text, ''),
	COALESCE(run_type, ''), status, COALESCE(err_msg, ''), duration_ms, started_at, finished_at,
	COALESCE(question, ''), question_size, COALESCE(answer, ''), answer_truncated, answer_size, created_at`

// SaveRun runs in one transaction: insert the record, then trim this agent's oldest rows beyond maxN.
//
// Retention is per agent, not per session: a stateless run has no session to group by, and the log is read
// as "what did this agent do", never as "what happened in this conversation".
func (r *PostgresRepository) SaveRun(schema string, rec *models.AgentRunRecord, maxN int) error {
	if rec == nil {
		return nil
	}
	// rec.ID is the run id, supplied by the caller: it is what session_records.run_id points at, so it must never be
	// generated here - a second identity is exactly what this table used to suffer from.
	if rec.ID == "" {
		return fmt.Errorf("agent run record is missing its run id (agent=%s)", rec.AgentID)
	}
	if rec.AgentID == "" {
		return fmt.Errorf("agent run record is missing agent_id (run=%s)", rec.ID)
	}
	if rec.Status == "" {
		rec.Status = models.SessionStatusOK
	}

	// Empty session / user must be sent as NULL: both columns are uuid, and '' fails with 22P02.
	var sessionID, userID interface{}
	if v := strings.TrimSpace(rec.SessionID); v != "" {
		sessionID = v
	}
	if v := strings.TrimSpace(rec.UserID); v != "" {
		userID = v
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO `+schema+`.agent_run_records
			(id, agent_id, session_id, user_id, run_type, status, err_msg, duration_ms,
			 started_at, finished_at, question, question_size, answer, answer_truncated, answer_size)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		rec.ID, rec.AgentID, sessionID, userID, rec.RunType, rec.Status, rec.ErrMsg, rec.DurationMs,
		rec.StartedAt, rec.FinishedAt, rec.Question, rec.QuestionSize, rec.Answer, rec.AnswerTruncated, rec.AnswerSize,
	); err != nil {
		return err
	}

	if maxN > 0 {
		// Trim from a boundary row instead of "id NOT IN (the newest maxN ids)": a tenant can raise the retention
		// well above the default, and the NOT IN form would then build a huge id list on every run. The boundary is
		// the maxN-th newest row
		// (OFFSET maxN-1) and everything strictly older goes, which keeps exactly maxN. The row comparison keeps the
		// same ordering as the queries (started_at DESC, id DESC), so rows sharing a timestamp are not over-deleted,
		// and it walks the (agent_id, started_at) index. Fewer rows than maxN -> the subquery yields NULL -> no delete.
		if _, err := tx.Exec(`
			DELETE FROM `+schema+`.agent_run_records
			WHERE agent_id = $1
			  AND (started_at, id) < (
					SELECT started_at, id FROM `+schema+`.agent_run_records
					WHERE agent_id = $1
					ORDER BY started_at DESC, id DESC
					OFFSET $2 - 1 LIMIT 1
			  )`, rec.AgentID, maxN); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListRuns lists past runs across agents and sessions (newest start first), with the total.
func (r *PostgresRepository) ListRuns(schema string, f Filter, limit int, offset int) ([]*models.AgentRunRecord, int, error) {
	where, args := f.clause(1)

	var total int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+schema+`.agent_run_records`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT ` + agentRunColumns + `
		FROM ` + schema + `.agent_run_records` + where +
		fmt.Sprintf(" ORDER BY started_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	records := make([]*models.AgentRunRecord, 0)
	for rows.Next() {
		m, err := scanAgentRun(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, m)
	}
	return records, total, rows.Err()
}

// GetRun returns one run record by id; a missing row returns (nil, nil) so the handler can word it.
func (r *PostgresRepository) GetRun(schema, id string) (*models.AgentRunRecord, error) {
	row := db.DB.QueryRow(`SELECT `+agentRunColumns+` FROM `+schema+`.agent_run_records WHERE id = $1`, id)
	m, err := scanAgentRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// DeleteRuns deletes records by criteria and returns the number removed.
//
// Unconditional deletion is refused outright: the admin console's bulk clean-up must carry a filter,
// otherwise one mistaken call wipes the tenant's run log.
func (r *PostgresRepository) DeleteRuns(schema string, f Filter) (int64, error) {
	where, args := f.clause(1)
	if where == "" {
		return 0, fmt.Errorf("refusing to delete agent run records without a filter: specify at least one criterion")
	}
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.agent_run_records`+where, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRun deletes one record by id and returns the number removed.
func (r *PostgresRepository) DeleteRun(schema, id string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.agent_run_records WHERE id = $1`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// clause builds the WHERE clause (with its leading space) and the arguments; start is the number of the
// first placeholder.
func (f Filter) clause(start int) (string, []interface{}) {
	conds := make([]string, 0, 6)
	args := make([]interface{}, 0, 6)
	idx := start
	add := func(expr string, v interface{}) {
		conds = append(conds, fmt.Sprintf(expr, idx))
		args = append(args, v)
		idx++
	}
	if f.AgentID != "" {
		add("agent_id = $%d", f.AgentID)
	}
	if f.SessionID != "" {
		add("session_id = $%d", f.SessionID)
	}
	if f.UserID != "" {
		add("user_id = $%d", f.UserID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.RunType != "" {
		add("run_type = $%d", f.RunType)
	}
	if f.From != nil {
		add("started_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("started_at <= $%d", *f.To)
	}
	where := ""
	for _, c := range conds {
		if where == "" {
			where = " WHERE " + c
		} else {
			where += " AND " + c
		}
	}
	return where, args
}

// scanAgentRun reads one row in agentRunColumns order; both *sql.Row and *sql.Rows satisfy the argument.
func scanAgentRun(s interface {
	Scan(dest ...interface{}) error
}) (*models.AgentRunRecord, error) {
	var m models.AgentRunRecord
	if err := s.Scan(&m.ID, &m.AgentID, &m.SessionID, &m.UserID, &m.RunType, &m.Status, &m.ErrMsg,
		&m.DurationMs, &m.StartedAt, &m.FinishedAt, &m.Question, &m.QuestionSize, &m.Answer, &m.AnswerTruncated,
		&m.AnswerSize, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}
