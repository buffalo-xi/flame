package sessionrecords

import (
	"fmt"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of session record storage.
type PostgresRepository struct{}

// sessionRecordColumns is the read column list; the order must match the rows.Scan below one to one.
//
// output / run_type / error / owner_type are all nullable in the database (see the session_records table in the
// provision template: only id / owner_id / session_id / run_id / created_at / duration_ms / status are NOT
// NULL). The corresponding fields in models.SessionRecord are plain strings, and scanning a NULL straight into
// one fails with `converting NULL to string is unsupported` — which turns the whole history endpoint into a 500
// and the frontend just shows "no conversation yet", as if the records had been deleted.
// Folding it to the empty string in SQL is easier to maintain than a *string or sql.NullString per field with a
// per-row conversion, and it makes the existing "NULL and empty string are synonymous" semantics (see the
// models.SessionRecord comment) explicit at the read site.
const sessionRecordColumns = `id, COALESCE(owner_type, ''), owner_id, session_id, run_id, input,
	COALESCE(output, ''), COALESCE(run_type, ''), duration_ms, status, COALESCE(error, ''), created_at`

// Append runs in one transaction: insert a record, then trim this session's records beyond maxN.
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

	// run_id is uuid NOT NULL (see the provision template). This used to write NULL when it was empty, but NULL
	// violates the same constraint and merely defers the failure to an INSERT error reading
	// `null value in column "run_id"`, leaving the caller unable to tell which field was missing. So it is caught
	// here.
	if rec.RunID == "" {
		return fmt.Errorf("session record is missing run_id (owner=%s/%s session=%s)", rec.OwnerType, rec.OwnerID, rec.SessionID)
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

// normalizeStatus is the fallback: when the caller gives no status, write ok.
// Note that the column default cannot be relied on — a default only applies when the column is not written, and
// passing an empty string stores "".
func normalizeStatus(s string) string {
	if s == "" {
		return models.SessionStatusOK
	}
	return s
}

// ListBySession reads one owner + session's history (oldest first).
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

// ListBySessionPage pages through the history: the latest limit records with created_at < before, returned
// oldest first.
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

	// Fetched newest first, so reverse into oldest first
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}

// RecordFilter is the admin console's criteria for querying past runs: every field is optional and a zero value
// means "no filter".
//
// Why ListBySession is not reused: that set hard-codes owner / session in the where clause, serving "one
// session reading its own memory"; the admin console asks "what has this whole tenant run", where both criteria
// must be optional.
type RecordFilter struct {
	OwnerType string
	OwnerID   string
	SessionID string
	Status    string
	From      *time.Time
	To        *time.Time
}

// clause builds the WHERE clause (with its leading space) and the arguments; start is the number of the first
// placeholder.
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

// ListRecords queries past runs across agents and sessions (admin console), paged newest first, with the total.
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

// DeleteRecords deletes past runs by criteria and returns the number removed.
//
// Unconditional deletion is refused outright: the admin console's "bulk clean-up" must carry a filter, otherwise
// one mistaken call wipes the table. The caller should first call ListRecords to show the user the total to
// confirm.
func (r *PostgresRepository) DeleteRecords(schema string, f RecordFilter) (int64, error) {
	where, args := f.clause(1)
	if where == "" {
		return 0, fmt.Errorf("refusing to delete session run records without a filter: specify at least one criterion")
	}
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.session_records`+where, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
