package sessionrecords

import (
	"fmt"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL 实现的会话记录存储
type PostgresRepository struct{}

// sessionRecordColumns 读取时的列清单，顺序必须与下方 rows.Scan 一一对应。
//
// output / run_type / error / owner_type 在库里都允许 NULL（见 provision 模板的
// session_records 建表语句：只有 id / owner_id / session_id / run_id / created_at /
// duration_ms / status 带 NOT NULL）。而 models.SessionRecord 里这几个字段是 string，
// 直接 Scan 一条 NULL 就报 `converting NULL to string is unsupported` ——
// 整个历史接口 500，前端只看到「暂无对话」，像是记录被删了。
// 在 SQL 侧折成空串：比给每个字段配 *string 或 sql.NullString 再逐条转更好维护，
// 也让「NULL 与空串同义」这个既有语义（见 models.SessionRecord 注释）在读取处显式化。
const sessionRecordColumns = `id, COALESCE(owner_type, ''), owner_id, session_id, run_id, input,
	COALESCE(output, ''), COALESCE(run_type, ''), duration_ms, status, COALESCE(error, ''), created_at`

// Append 在事务内：插入一条记录 → 裁剪该 session 超出 maxN 的旧记录。
func (r *PostgresRepository) Append(schema string, rec *models.SessionRecord, maxN int) error {
	if rec == nil {
		return nil
	}
	if rec.Input == nil {
		rec.Input = []byte("{}")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}

	// run_id 是 uuid NOT NULL（见 provision 模板）。原先这里是「为空就写 NULL」，
	// 但 NULL 同样违反约束，只是把错误推迟到 INSERT 变成一句
	// `null value in column "run_id"`，调用方看不出是哪个字段没传。这里提前拦下。
	if rec.RunID == "" {
		return fmt.Errorf("会话记录缺少 run_id（owner=%s/%s session=%s）", rec.OwnerType, rec.OwnerID, rec.SessionID)
	}
	runID := rec.RunID

	if _, err := tx.Exec(`
		INSERT INTO `+schema+`.session_records
			(id, owner_type, owner_id, session_id, run_id, input, output, run_type, duration_ms, status, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		rec.ID, rec.OwnerType, rec.OwnerID, rec.SessionID, runID, rec.Input, rec.Output, rec.RunType, rec.DurationMs,
		normalizeStatus(rec.Status), rec.Error,
	); err != nil {
		return err
	}

	if maxN > 0 {
		if _, err := tx.Exec(`
			DELETE FROM `+schema+`.session_records
			WHERE owner_type = $1 AND owner_id = $2 AND session_id = $3
			  AND id NOT IN (
				SELECT id FROM `+schema+`.session_records
				WHERE owner_type = $1 AND owner_id = $2 AND session_id = $3
				ORDER BY created_at DESC, id DESC
				LIMIT $4
			  )`, rec.OwnerType, rec.OwnerID, rec.SessionID, maxN); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// normalizeStatus 兜底：调用方没给 status 时按 ok 写。
// 注意不能依赖列默认值 —— 默认值只在「不写这一列」时生效，显式传空串会存成 ”。
func normalizeStatus(s string) string {
	if s == "" {
		return models.SessionStatusOK
	}
	return s
}

// ListBySession 读取某 owner + session 的历史记录（旧→新）。
func (r *PostgresRepository) ListBySession(schema, ownerType, ownerID, sessionID string, limit int, since *time.Time) ([]models.SessionRecord, error) {
	where := "WHERE owner_type = $1 AND owner_id = $2 AND session_id = $3"
	args := []interface{}{ownerType, ownerID, sessionID}
	argIdx := 4
	if since != nil {
		where += fmt.Sprintf(" AND created_at >= $%d", argIdx)
		args = append(args, *since)
		argIdx++
	}

	listSQL := `SELECT ` + sessionRecordColumns + `
		FROM ` + schema + `.session_records ` + where + ` ORDER BY created_at ASC, id ASC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]models.SessionRecord, 0)
	for rows.Next() {
		var m models.SessionRecord
		if err := rows.Scan(&m.ID, &m.OwnerType, &m.OwnerID, &m.SessionID, &m.RunID, &m.Input, &m.Output, &m.RunType, &m.DurationMs, &m.Status, &m.Error, &m.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, m)
	}
	return records, rows.Err()
}

// ListBySessionPage 分页读取历史记录：取 created_at < before 的最近 limit 条，返回旧→新。
func (r *PostgresRepository) ListBySessionPage(schema, ownerType, ownerID, sessionID string, limit int, before *time.Time) ([]models.SessionRecord, error) {
	where := "WHERE owner_type = $1 AND owner_id = $2 AND session_id = $3"
	args := []interface{}{ownerType, ownerID, sessionID}
	argIdx := 4
	if before != nil {
		where += fmt.Sprintf(" AND created_at < $%d", argIdx)
		args = append(args, *before)
		argIdx++
	}

	listSQL := `SELECT ` + sessionRecordColumns + `
		FROM ` + schema + `.session_records ` + where + ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		listSQL += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, limit)
	}

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]models.SessionRecord, 0)
	for rows.Next() {
		var m models.SessionRecord
		if err := rows.Scan(&m.ID, &m.OwnerType, &m.OwnerID, &m.SessionID, &m.RunID, &m.Input, &m.Output, &m.RunType, &m.DurationMs, &m.Status, &m.Error, &m.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 倒序取回后反转为旧→新
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}

// RecordFilter 管理端查询历史运行的条件：全部字段可选，零值 = 不过滤。
//
// 为什么不复用 ListBySession：那组方法把 owner / session 写死在 where 里，服务的是
// 「某个会话读自己的记忆」；管理端要回答的是「整个租户跑过什么」，两个条件都必须可选。
type RecordFilter struct {
	OwnerType string
	OwnerID   string
	SessionID string
	Status    string
	From      *time.Time
	To        *time.Time
}

// clause 生成 WHERE 子句（含前导空格）与参数；start 是第一个占位符的序号。
func (f RecordFilter) clause(start int) (string, []interface{}) {
	conds := make([]string, 0, 6)
	args := make([]interface{}, 0, 6)
	idx := start
	add := func(expr string, v interface{}) {
		conds = append(conds, fmt.Sprintf(expr, idx))
		args = append(args, v)
		idx++
	}
	if f.OwnerType != "" {
		add("owner_type = $%d", f.OwnerType)
	}
	if f.OwnerID != "" {
		add("owner_id = $%d", f.OwnerID)
	}
	if f.SessionID != "" {
		add("session_id = $%d", f.SessionID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.From != nil {
		add("created_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("created_at <= $%d", *f.To)
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

// ListRecords 跨 agent / 会话查询历史运行（管理端），按时间倒序分页，返回总数。
func (r *PostgresRepository) ListRecords(schema string, f RecordFilter, limit, offset int) ([]models.SessionRecord, int, error) {
	where, args := f.clause(1)

	var total int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+schema+`.session_records`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT ` + sessionRecordColumns + `
		FROM ` + schema + `.session_records` + where +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.DB.Query(listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	records := make([]models.SessionRecord, 0)
	for rows.Next() {
		var m models.SessionRecord
		if err := rows.Scan(&m.ID, &m.OwnerType, &m.OwnerID, &m.SessionID, &m.RunID, &m.Input, &m.Output, &m.RunType, &m.DurationMs, &m.Status, &m.Error, &m.CreatedAt); err != nil {
			return nil, 0, err
		}
		records = append(records, m)
	}
	return records, total, rows.Err()
}

// DeleteRecords 按条件删除历史运行，返回删除条数。
//
// 无条件直接拒绝：管理端的「批量清理」必须带筛选，否则一次误调用就是清库。
// 调用方在真正删除前应当先 ListRecords 拿到 total 让用户确认。
func (r *PostgresRepository) DeleteRecords(schema string, f RecordFilter) (int64, error) {
	where, args := f.clause(1)
	if where == "" {
		return 0, fmt.Errorf("拒绝无条件删除会话运行记录：请至少指定一个筛选条件")
	}
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.session_records`+where, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
