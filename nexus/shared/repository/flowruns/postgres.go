package flowruns

import (
	"database/sql"
	"fmt"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
)

// PostgresRepository PostgreSQL 实现的运行记录存储
type PostgresRepository struct{}

// SaveRun 在事务内：插入 run → 批量插入 nodes → 裁剪超限（保留最近 maxRunRecords 条）。
func (r *PostgresRepository) SaveRun(schema string, run *models.FlowRunRecord, nodes []*models.FlowNodeRecord, maxRunRecords int) error {
	if run == nil {
		return nil
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 插入 run 记录
	if err := tx.QueryRow(`
		INSERT INTO `+schema+`.flow_run_records
			(id, flow_id, status, run_type, result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
			 run_args, run_limits, run_flow_vars, run_flow_vars_truncated, run_flow_vars_size, drafts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id`,
		run.ID, run.FlowID, run.Status, run.RunType, run.Result, run.Progress, run.ProgressTruncated, run.ProgressSize, run.ErrMsg, run.NodeCount, run.DurationMs, run.StartedAt, run.FinishedAt,
		run.RunArgs, run.RunLimits, run.RunFlowVars, run.RunFlowVarsTruncated, run.RunFlowVarsSize, run.Drafts,
	).Scan(&run.ID); err != nil {
		return err
	}

	// 2. 批量插入 node 记录
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
				  start_time, end_time, duration_ms, extra)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
			n.ID, run.ID, n.NodeID, n.ProcessID, n.NodeType, n.Status,
			n.InputVars, n.InputVarsTruncated, n.InputVarsSize,
			n.OutputVars, n.OutputVarsTruncated, n.OutputVarsSize,
			n.LocalVars, n.LocalVarsTruncated, n.LocalVarsSize,
			n.RunLogs, n.RunLogsTruncated, n.RunLogsLines,
			n.ErrCode, n.ErrMsg, n.ErrStack, n.RetryCount, n.ErrorAction,
			n.StartTime, n.EndTime, n.DurationMs, n.Extra,
		); err != nil {
			return err
		}
	}

	// 3. 裁剪：debug 与正式记录各自保留最近 maxRunRecords 条（分开计数）。
	if err := trimExcess(tx, schema, run.FlowID, run.RunType, maxRunRecords); err != nil {
		return err
	}

	return tx.Commit()
}

// trimExcess 按 run_type 分组裁剪超限记录：
//   - debug 与非 debug（call/task）分开计数，各自保留最近 limit 条；
//   - limit<=0 不裁剪（0 表示不存储，调用方 SaveRun 之前就已返回）。
//
// 历史执行列表读的就是这张表，所以 debug 运行同样要多条留存。
// 早期这里把 debug 硬编码成"每 flow 只留 1 条"，表现出来就是
// "明明调试跑了很多次，历史执行里只有一条"。
func trimExcess(tx *sql.Tx, schema, flowId, runType string, limit int) error {
	if limit <= 0 {
		return nil
	}
	// debug 与 preview 都是「跑草稿」的运行，合在一组裁剪；正式调用（call/task）另算一组
	return deleteExcess(tx, schema, flowId, runType == "debug" || runType == "preview", limit)
}

// deleteExcess 删除某分组下，按 started_at DESC（id 兜底）排序后超出 limit 的最旧 run 及其 node 记录。
// debugOnly=true 匹配 run_type='debug'；false 匹配 run_type<>'debug'（call+task 一起裁剪）。
func deleteExcess(tx *sql.Tx, schema, flowId string, debugOnly bool, limit int) error {
	// 「跑草稿」的一组：debug（调试器）+ preview（试跑）
	cond := "run_type IN ('debug','preview')"
	if !debugOnly {
		cond = "run_type NOT IN ('debug','preview')"
	}
	// 先删 node（run_id 在超限集合内）
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
	// 再删 run
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

// ListRuns 列出某个工作流的历史运行记录（按开始时间倒序）。
func (r *PostgresRepository) ListRuns(schema string, flowId string, runType string, status string, startFrom, startTo *time.Time, limit int, offset int) ([]*models.FlowRunRecord, int, error) {
	// flowId 为空 = 跨流程查（管理端「历史运行」用），此时不加 flow_id 条件。
	// 不能改写成 flow_id = ''：uuid 列传空串会直接报 22P02。
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

	// 总数
	var total int
	countSQL := `SELECT COUNT(*) FROM ` + schema + `.flow_run_records ` + where
	if err := db.DB.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 列表
	listSQL := `SELECT id, flow_id, status, run_type, result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
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
		if err := rows.Scan(&rec.ID, &rec.FlowID, &rec.Status, &rec.RunType, &rec.Result, &rec.Progress, &rec.ProgressTruncated, &rec.ProgressSize, &rec.ErrMsg,
			&rec.NodeCount, &rec.DurationMs, &rec.StartedAt, &rec.FinishedAt,
			&rec.RunArgs, &rec.RunLimits, &rec.RunFlowVars, &rec.RunFlowVarsTruncated, &rec.RunFlowVarsSize,
			&rec.Drafts); err != nil {
			return nil, 0, err
		}
		records = append(records, &rec)
	}
	return records, total, rows.Err()
}

// GetRunDetail 获取一次运行记录及其所有节点记录（节点按开始时间升序）。
func (r *PostgresRepository) GetRunDetail(schema string, runID string) (*models.FlowRunRecord, []*models.FlowNodeRecord, error) {
	var run models.FlowRunRecord
	err := db.DB.QueryRow(`
		SELECT id, flow_id, status, run_type, result, progress, progress_truncated, progress_size, err_msg, node_count, duration_ms, started_at, finished_at,
			run_args, run_limits, run_flow_vars, run_flow_vars_truncated, run_flow_vars_size, drafts
		FROM `+schema+`.flow_run_records WHERE id = $1`, runID).Scan(
		&run.ID, &run.FlowID, &run.Status, &run.RunType, &run.Result, &run.Progress, &run.ProgressTruncated, &run.ProgressSize, &run.ErrMsg,
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
			err_code, err_msg, err_stack, retry_count, error_action,
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
			&n.StartTime, &n.EndTime, &n.DurationMs, &n.Extra); err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, &n)
	}
	return &run, nodes, rows.Err()
}

// DeleteRun 删除某工作流下的一次运行记录及其节点记录，返回删除的 run 条数。
//
// 先删节点再删 run：node 表不依赖级联外键，显式删才不会留孤儿记录
// （run 被删、node 还在，「按 run 查节点」就会读到幽灵数据）。
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

// DeleteRuns 删除某工作流的全部运行记录（调试与正式一起），返回删除的 run 条数。
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

// DeleteRunsByFilter 按组合条件批量删除运行记录（管理端「批量清理」），返回删除的 run 条数。
//
// 与 DeleteRuns 一样先删节点记录再删运行记录，区别是条件可选可组合。
// 无条件直接拒绝：管理端的批量清理必须带筛选，否则一次误调用就是清库。
func (r *PostgresRepository) DeleteRunsByFilter(schema, flowId, status string, startFrom, startTo *time.Time) (int64, error) {
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
	if startFrom != nil {
		add("started_at >= $%d", *startFrom)
	}
	if startTo != nil {
		add("started_at <= $%d", *startTo)
	}
	if where == "" {
		return 0, fmt.Errorf("拒绝无条件删除运行记录：请至少指定一个筛选条件")
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
