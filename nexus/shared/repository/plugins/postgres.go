package plugins

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL plugin configuration store.
type PostgresRepository struct{}

// NewRepository builds the repository.
func NewRepository() Repository { return &PostgresRepository{} }

var _ Repository = (*PostgresRepository)(nil)

// selectColumns: the column order must match scanPlugin. kind follows id, so it lines up with the unique key
// (kind, name).
const selectColumns = `id,kind,name,impl,fallbacks,configs,is_active,created_at,updated_at,last_used_at,last_error,last_error_at`

// Get returns one row by (kind, name).
func (r *PostgresRepository) Get(authInfo *auth.AuthInfo, kind, name string) (*models.Plugin, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s.%s WHERE kind=$1 AND name=$2`, selectColumns, authInfo.Schema, tableName)
	p, err := scanPlugin(db.DB.QueryRow(q, kind, name))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, wrapPgErr(err)
	}
	return p, nil
}

// List lists this tenant's plugin configurations (every kind). Not filtering by kind is deliberate: the callers
// all want everything at once.
func (r *PostgresRepository) List(authInfo *auth.AuthInfo) ([]models.Plugin, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s.%s ORDER BY kind, name`, selectColumns, authInfo.Schema, tableName)
	rows, err := db.DB.Query(q)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()

	out := make([]models.Plugin, 0, 8)
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, wrapPgErr(err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Upsert inserts or updates by (kind, name). Before writing, nil JSON columns are normalised to [] / {}, so an
// explicit NULL cannot overwrite the table default.
//
// An empty kind is an outright error rather than falling back to "tools": the database's DEFAULT only applies
// when the column is not passed at all, and once an empty string is written that row can never be read by any
// kind-filtered query — the plugin "saves successfully but vanishes", the hardest class of fault to diagnose.
func (r *PostgresRepository) Upsert(authInfo *auth.AuthInfo, p *models.Plugin) error {
	if p == nil {
		return fmt.Errorf("plugins: nil plugin")
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" {
		return fmt.Errorf("plugins: kind is empty (it must be set explicitly, see models.Plugin.Kind)")
	}
	id := p.ID
	if id == "" {
		id = uuid.NewString()
	}
	q := fmt.Sprintf(`INSERT INTO %s.%s (id,kind,name,impl,fallbacks,configs,is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (kind, name) DO UPDATE SET
			impl=EXCLUDED.impl,
			fallbacks=EXCLUDED.fallbacks,
			configs=EXCLUDED.configs,
			is_active=EXCLUDED.is_active,
			updated_at=CURRENT_TIMESTAMP`, authInfo.Schema, tableName)
	_, err := db.DB.Exec(q, id, kind, p.Name, p.Impl, jsonOr(p.Fallbacks, "[]"), jsonOr(p.Configs, "{}"), p.IsActive)
	return wrapPgErr(err)
}

// SetActive only changes the enabled state.
func (r *PostgresRepository) SetActive(authInfo *auth.AuthInfo, kind, name string, active bool) error {
	q := fmt.Sprintf(`UPDATE %s.%s SET is_active=$3, updated_at=CURRENT_TIMESTAMP WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	res, err := db.DB.Exec(q, kind, name, active)
	if err != nil {
		return wrapPgErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes this tenant's plugin configuration.
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, kind, name string) error {
	q := fmt.Sprintf(`DELETE FROM %s.%s WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	res, err := db.DB.Exec(q, kind, name)
	if err != nil {
		return wrapPgErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// maxLastErrorRunes is the storage ceiling for last_error (truncated by rune, so no multi-byte character is cut
// in half). Enough for troubleshooting; the whole stack does not belong in one column.
const maxLastErrorRunes = 200

// TouchUsed records one successful call and clears the previous error.
//
// It deliberately does not touch updated_at: that column means "when was the configuration changed", and once
// polluted by call frequency it can no longer tell whether the configuration is stale.
func (r *PostgresRepository) TouchUsed(authInfo *auth.AuthInfo, kind, name string) error {
	q := fmt.Sprintf(`UPDATE %s.%s
		SET last_used_at=CURRENT_TIMESTAMP, last_error='', last_error_at=NULL
		WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	// The row may already be deleted: failing to write observability data is not a failed call
	_, err := db.DB.Exec(q, kind, name)
	return ignoreNoRows(err)
}

// SetError records one failure (the message is truncated before writing).
func (r *PostgresRepository) SetError(authInfo *auth.AuthInfo, kind, name, message string) error {
	q := fmt.Sprintf(`UPDATE %s.%s
		SET last_error=$3, last_error_at=CURRENT_TIMESTAMP
		WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	_, err := db.DB.Exec(q, kind, name, truncateRunes(message, maxLastErrorRunes))
	return ignoreNoRows(err)
}

func ignoreNoRows(err error) error {
	if err == nil {
		return nil
	}
	return wrapPgErr(err)
}

func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

func scanPlugin(scanner interface{ Scan(...any) error }) (*models.Plugin, error) {
	var p models.Plugin
	var isActive sql.NullBool
	var lastUsed, lastErrAt sql.NullTime
	var lastErr sql.NullString
	err := scanner.Scan(&p.ID, &p.Kind, &p.Name, &p.Impl, &p.Fallbacks, &p.Configs, &isActive,
		&p.CreatedAt, &p.UpdatedAt, &lastUsed, &lastErr, &lastErrAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive.Bool
	if lastUsed.Valid {
		t := lastUsed.Time
		p.LastUsedAt = &t
	}
	if lastErrAt.Valid {
		t := lastErrAt.Time
		p.LastErrorAt = &t
	}
	p.LastError = lastErr.String
	return &p, nil
}

// jsonOr keeps the existing JSON; an empty value falls back to the literal (such as "[]" / "{}").
func jsonOr(j xsql.JSONB, fallback string) any {
	v, err := j.Value()
	if err != nil || v == nil {
		return json.RawMessage(fallback)
	}
	return v
}

func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return fmt.Errorf("plugins: %w", err)
}
