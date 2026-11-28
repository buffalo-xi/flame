package flowruns

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
)

// PostgresRepository is the PostgreSQL implementation of run record storage.
type PostgresRepository struct{}

// SaveRun runs in one transaction: insert the run, bulk-insert the nodes, then trim the excess (keeping the latest
// maxRunRecords).
func (r *PostgresRepository) SaveRun(schema string, run *models.FlowRunRecord, nodes []*models.FlowNodeRecord, maxRunRecords int) error {
	if run == nil {
		return nil
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Empty session / user must be sent as NULL: both columns are uuid, and '' fails with 22P02.
	var sessionID, userID interface{}
	if v := strings.TrimSpace(run.SessionID); v != "" {
		sessionID = v
	}
	if v := strings.TrimSpace(run.UserID); v != "" {
		userID = v
	}

	// 1. Insert the run record
	if err := tx.QueryRow(`
		INSERT INTO `+schema+`.flow_run_records
			(id, flow_id, status, run_type, result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
			 run_args, run_limits, run_flow_vars, run_flow_vars_truncated, run_flow_vars_size, drafts, session_id, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id`,
		run.ID, run.FlowID, run.Status, run.RunType, run.Result, run.Progress, run.ProgressTruncated, run.ProgressSize, run.ErrMsg, run.NodeCount, run.DurationMs, run.StartedAt, run.FinishedAt,
		run.RunArgs, run.RunLimits, run.RunFlowVars, run.RunFlowVarsTruncated, run.RunFlowVarsSize, run.Drafts,
		sessionID, userID,
	).Scan(&run.ID); err != nil {
		return err
	}

	// 2. Bulk-insert the node records
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO `+schema+`.flow_node_records
				(id, run_id, node_id, process_id, node_type, status,
				 input_vars, input_vars_truncated, input_vars_size,
				 output_vars, output_vars_truncated, output_vars_size,
				 local_vars, local_vars_truncated, local_vars_size,
				 run_logs, run_logs_truncated, run_logs_lines,
				 err_code, err_msg, err_stack, retry_count, error_action,
				 post_error, handler_error,
				 start_time, end_time, duration_ms, extra)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
			n.ID, run.ID, n.NodeID, n.ProcessID, n.NodeType, n.Status,
			n.InputVars, n.InputVarsTruncated, n.InputVarsSize,
			n.OutputVars, n.OutputVarsTruncated, n.OutputVarsSize,
			n.LocalVars, n.LocalVarsTruncated, n.LocalVarsSize,
			n.RunLogs, n.RunLogsTruncated, n.RunLogsLines,
			n.ErrCode, n.ErrMsg, n.ErrStack, n.RetryCount, n.ErrorAction,
			n.PostError, n.HandlerError,
			n.StartTime, n.EndTime, n.DurationMs, n.Extra,
		); err != nil {
			return err
		}
	}

	// 3. Trim: debug and real runs keep their own latest maxRunRecords (counted separately).
	if err := trimExcess(tx, schema, run.FlowID, run.RunType, maxRunRecords); err != nil {
		return err
	}

	return tx.Commit()
}

// trimExcess trims the excess per run_type group:
//   - debug and non-debug (call/task) are counted separately, each keeping its latest limit records;
//   - limit<=0 means no trimming (0 means "store nothing", and the caller's SaveRun already returned before this).
//
// The past-runs list reads this very table, so debug runs have to be kept in quantity too.
// Early on this hard-coded debug to "keep 1 per flow", which showed up as
// "I debugged many times but the past-runs list has a single entry".
func trimExcess(tx *sql.Tx, schema, flowId, runType string, limit int) error {
	if limit <= 0 {
		return nil
	}
	// debug and preview both "run the draft" and are trimmed as one group; real calls (call/task) are another
	return deleteExcess(tx, schema, flowId, runType == "debug" || runType == "preview", limit)
}

// deleteExcess deletes the oldest runs beyond limit in one group (ordered by started_at DESC, id as the tiebreak)
// together with their node records.
// draftGroup=true matches the runs-the-draft group; false matches everything else (call+task trimmed together).
func deleteExcess(tx *sql.Tx, schema, flowId string, draftGroup bool, limit int) error {
	// The "runs the draft" group: debug (the debugger) plus preview (a trial run)
	cond := "run_type IN ('debug','preview')"
	if !draftGroup {
		// run_type is nullable (the column was added after the table existed), and `NULL NOT IN (...)` evaluates to
		// NULL rather than true — without the IS NULL arm the legacy rows would never be trimmed and would grow forever.
		cond = "(run_type IS NULL OR run_type NOT IN ('debug','preview'))"
	}
	// Delete the nodes first (run_id within the over-limit set)
	if _, err := tx.Exec(`
		DELETE FROM `+schema+`.flow_node_records
		WHERE run_id IN (
			SELECT id FROM `+schema+`.flow_run_records
			WHERE flow_id = $1 AND `+cond+`
			ORDER BY started_at DESC, id DESC
			OFFSET $2
		)`, flowId, limit); err != nil {
		return err
	}
	// Then the runs
	if _, err := tx.Exec(`
		DELETE FROM `+schema+`.flow_run_records
		WHERE id IN (
			SELECT id FROM `+schema+`.flow_run_records
			WHERE flow_id = $1 AND `+cond+`
			ORDER BY started_at DESC, id DESC
			OFFSET $2
		)`, flowId, limit); err != nil {
		return err
	}
	return nil
}

// ListRuns lists one workflow's past runs (newest start first).
func (r *PostgresRepository) ListRuns(schema string, flowId string, runType string, status string, userID string, sessionID string, startFrom, startTo *time.Time, limit int, offset int) ([]*models.FlowRunRecord, int, error) {
	// An empty flowId means a cross-flow query (the admin console's "past runs"), in which case the flow_id
	// condition is omitted.
	// It cannot be rewritten as flow_id = '': passing an empty string to a uuid column fails with 22P02.
	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1
	if flowId != "" {
		where += fmt.Sprintf(" AND flow_id = $%d", argIdx)
		args = append(args, flowId)
		argIdx++
	}
	if runType != "" {
		where += fmt.Sprintf(" AND run_type = $%d", argIdx)
		args = append(args, runType)
		argIdx++
	}
	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	// An empty userID filters nothing: runs recorded before the column existed, and runs with no identified caller, both
	// have NULL there and must stay visible in the unfiltered list.
	if userID != "" {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}
	// An empty sessionID filters nothing: a debug run or a protocol call without a session has NULL there and must stay
	// visible in the unfiltered list. A non-empty value must be a uuid - an empty string would fail with 22P02.
	if sessionID != "" {
		where += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, sessionID)
		argIdx++
	}
	if startFrom != nil {
		where += fmt.Sprintf(" AND started_at >= $%d", argIdx)
		args = append(args, *startFrom)
		argIdx++
	}
	if startTo != nil {
		where += fmt.Sprintf(" AND started_at <= $%d", argIdx)
		args = append(args, *startTo)
		argIdx++
	}

	// Total
	var total int
	countSQL := `SELECT COUNT(*) FROM ` + schema + `.flow_run_records ` + where
	if err := db.DB.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// The list
	// run_type / session_id / user_id are nullable: COALESCE keeps a legacy NULL from failing the scan (converting NULL to
	// string is unsupported). session_id::text is required - COALESCE on a uuid column and an empty string literal does
	// not type-check.
	listSQL := `SELECT id, flow_id, COALESCE(session_id::text, ''), COALESCE(user_id::text, ''), status, COALESCE(run_type, ''), result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
			run_args, run_limits, run_flow_vars, run_flow_vars_truncated, run_flow_vars_size, drafts
		FROM ` + schema + `.flow_run_records ` + where +
		fmt.Sprintf(" ORDER BY started_at DESC, id DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	records := make([]*models.FlowRunRecord, 0)
	for rows.Next() {
		var rec models.FlowRunRecord
		if err := rows.Scan(&rec.ID, &rec.FlowID, &rec.SessionID, &rec.UserID, &rec.Status, &rec.RunType, &rec.Result, &rec.Progress, &rec.ProgressTruncated, &rec.ProgressSize, &rec.ErrMsg,
			&rec.NodeCount, &rec.DurationMs, &rec.StartedAt, &rec.FinishedAt,
			&rec.RunArgs, &rec.RunLimits, &rec.RunFlowVars, &rec.RunFlowVarsTruncated, &rec.RunFlowVarsSize,
			&rec.Drafts); err != nil {
			return nil, 0, err
		}
		records = append(records, &rec)
	}
	return records, total, rows.Err()
}

// GetRunDetail fetches one run record plus all of its node records (nodes ascending by start time).
func (r *PostgresRepository) GetRunDetail(schema string, runID string) (*models.FlowRunRecord, []*models.FlowNodeRecord, error) {
	var run models.FlowRunRecord
	err := db.DB.QueryRow(`
		SELECT id, flow_id, COALESCE(session_id::text, ''), COALESCE(user_id::text, ''), status, COALESCE(run_type, ''), result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
			run_args, run_limits, run_flow_vars, run_flow_vars_truncated, run_flow_vars_size, drafts
		FROM `+schema+`.flow_run_records WHERE id = $1`, runID).Scan(
		&run.ID, &run.FlowID, &run.SessionID, &run.UserID, &run.Status, &run.RunType, &run.Result, &run.Progress, &run.ProgressTruncated, &run.ProgressSize, &run.ErrMsg,
		&run.NodeCount, &run.DurationMs, &run.StartedAt, &run.FinishedAt,
		&run.RunArgs, &run.RunLimits, &run.RunFlowVars, &run.RunFlowVarsTruncated, &run.RunFlowVarsSize,
		&run.Drafts)
	if err != nil {
		return nil, nil, err
	}

	rows, err := db.DB.Query(`
		SELECT id, run_id, node_id, process_id, node_type, status,
			input_vars, input_vars_truncated, input_vars_size,
			output_vars, output_vars_truncated, output_vars_size,
			local_vars, local_vars_truncated, local_vars_size,
			run_logs, run_logs_truncated, run_logs_lines,
			err_code, err_msg, err_stack, retry_count, error_action, post_error, handler_error,
			start_time, end_time, duration_ms, extra
		FROM `+schema+`.flow_node_records WHERE run_id = $1 ORDER BY start_time ASC, id ASC`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	nodes := make([]*models.FlowNodeRecord, 0)
	for rows.Next() {
		var n models.FlowNodeRecord
		if err := rows.Scan(&n.ID, &n.RunID, &n.NodeID, &n.ProcessID, &n.NodeType, &n.Status,
			&n.InputVars, &n.InputVarsTruncated, &n.InputVarsSize,
			&n.OutputVars, &n.OutputVarsTruncated, &n.OutputVarsSize,
			&n.LocalVars, &n.LocalVarsTruncated, &n.LocalVarsSize,
			&n.RunLogs, &n.RunLogsTruncated, &n.RunLogsLines,
			&n.ErrCode, &n.ErrMsg, &n.ErrStack, &n.RetryCount, &n.ErrorAction,
			&n.PostError, &n.HandlerError,
			&n.StartTime, &n.EndTime, &n.DurationMs, &n.Extra); err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, &n)
	}
	return &run, nodes, rows.Err()
}

// DeleteRun deletes one run record of a workflow together with its node records, returning the number of runs
// removed.
//
// Nodes are deleted before runs: the node table does not rely on a cascading foreign key, so deleting explicitly is
// what keeps orphan records from piling up (the run gone, the nodes still there, and "nodes by run" then returns
// ghost data).
func (r *PostgresRepository) DeleteRun(schema, flowId, runID string) (int64, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DELETE FROM `+schema+`.flow_node_records
		WHERE run_id IN (
			SELECT id FROM `+schema+`.flow_run_records WHERE id = $1 AND flow_id = $2
		)`, runID, flowId); err != nil {
		return 0, err
	}

	res, err := tx.Exec(`DELETE FROM `+schema+`.flow_run_records WHERE id = $1 AND flow_id = $2`, runID, flowId)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// DeleteRuns deletes every run record of a workflow (debug and real alike), returning the number of runs removed.
func (r *PostgresRepository) DeleteRuns(schema, flowId string) (int64, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DELETE FROM `+schema+`.flow_node_records
		WHERE run_id IN (
			SELECT id FROM `+schema+`.flow_run_records WHERE flow_id = $1
		)`, flowId); err != nil {
		return 0, err
	}

	res, err := tx.Exec(`DELETE FROM `+schema+`.flow_run_records WHERE flow_id = $1`, flowId)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// DeleteRunsByFilter deletes run records in bulk by a combination of criteria (the admin console's "bulk clean-up"),
// returning the number of runs removed.
//
// Like DeleteRuns it deletes the node records first and the runs second; the difference is that the criteria are
// optional and combinable.
// Unconditional deletion is refused outright: the admin console's bulk clean-up must carry a filter, otherwise one
// mistaken call wipes the table.
func (r *PostgresRepository) DeleteRunsByFilter(schema, flowId, status string, userID string, sessionID string, startFrom, startTo *time.Time) (int64, error) {
	where := ""
	args := []interface{}{}
	argIdx := 1
	add := func(expr string, v interface{}) {
		if where == "" {
			where = " WHERE " + fmt.Sprintf(expr, argIdx)
		} else {
			where += " AND " + fmt.Sprintf(expr, argIdx)
		}
		args = append(args, v)
		argIdx++
	}
	if flowId != "" {
		add("flow_id = $%d", flowId)
	}
	if status != "" {
		add("status = $%d", status)
	}
	if userID != "" {
		add("user_id = $%d", userID)
	}
	if sessionID != "" {
		add("session_id = $%d", sessionID)
	}
	if startFrom != nil {
		add("started_at >= $%d", *startFrom)
	}
	if startTo != nil {
		add("started_at <= $%d", *startTo)
	}
	if where == "" {
		return 0, fmt.Errorf("refusing to delete run records without a filter: specify at least one criterion")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM `+schema+`.flow_node_records WHERE run_id IN (
		SELECT id FROM `+schema+`.flow_run_records`+where+`)`, args...); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM `+schema+`.flow_run_records`+where, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
