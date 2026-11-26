// Package customtools stores tenant-defined custom tools (table {schema}.custom_tools).
//
// Division of labour with the plugins repository:
//   - plugins: implementations discovered on disk plus one configuration per tenant (the code is not in the database)
//   - customtools: the tenant's own tool definitions (metadata in the database, code on disk under tools/<name>/)
//
// Every method locates the table by schema: this table lives in the **tenant database** (alongside skills / plugins), not
// the platform one — a custom tool is the tenant's own business logic, not a platform capability.
package customtools

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/versions"
	"flame/tools/xsql"
)

const tableName = "custom_tools"

// normalizeRequiredJSON fills the NOT NULL jsonb columns with a valid empty value.
//
// A nil is written as SQL NULL by xsql.JSONB and trips the NOT NULL constraint directly; the error is the database-level
// "null value in column ...", from which a reader cannot tell that "a key was missing in TOOL.json". Normalise once
// before writing: none of the three columns (argument schema / config declaration / config values) may be NULL.
func normalizeRequiredJSON(t *models.CustomTool) {
	if t.Parameters.Data == nil {
		t.Parameters = xsql.JSONB{Data: []byte(`{"type":"object","properties":{}}`)}
	}
	if t.ConfigFields.Data == nil {
		t.ConfigFields = xsql.JSONB{Data: []byte("[]")}
	}
	if t.Configs.Data == nil {
		t.Configs = xsql.JSONB{Data: []byte("{}")}
	}
}

// columns is the column list in one place: its order maps one-to-one onto scan, and a table change only touches here.
const columns = `id, name, display_name, description, parameters, output, lang, entry,
	timeout_ms, max_output_bytes, egress, config_fields, configs, status, created_by, created_at, updated_at`

// Repository is the custom tool repository.
type Repository interface {
	// List lists every custom tool of the tenant (abnormal states included), by name ascending.
	List(schema string) ([]models.CustomTool, error)
	// ListEnabled lists only status=1 (for the catalogue / execution).
	ListEnabled(schema string) ([]models.CustomTool, error)
	// Get fetches by name; missing returns sql.ErrNoRows.
	Get(schema, name string) (*models.CustomTool, error)
	// Create inserts one row; an empty ID is generated.
	Create(schema string, t *models.CustomTool) error
	// Update overwrites the editable columns by name (created_* is untouched).
	Update(schema string, t *models.CustomTool) error
	// Delete deletes the metadata by name (whether the code on disk goes with it is the caller's decision).
	Delete(schema, name string) error
}

// NewRepository creates the repository.
func NewRepository() Repository { return &postgresRepository{} }

type postgresRepository struct{}

func table(schema string) string { return schema + "." + tableName }

func (r *postgresRepository) List(schema string) ([]models.CustomTool, error) {
	return r.query(schema, `SELECT `+columns+` FROM `+table(schema)+` ORDER BY name`)
}

func (r *postgresRepository) ListEnabled(schema string) ([]models.CustomTool, error) {
	return r.query(schema, `SELECT `+columns+` FROM `+table(schema)+` WHERE status = 1 ORDER BY name`)
}

func (r *postgresRepository) query(schema, q string, args ...any) ([]models.CustomTool, error) {
	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.CustomTool, 0, 8)
	for rows.Next() {
		var t models.CustomTool
		if err := rows.Scan(
			&t.ID, &t.Name, &t.DisplayName, &t.Description, &t.Parameters, &t.Output,
			&t.Lang, &t.Entry, &t.TimeoutMS, &t.MaxOutputBytes, &t.Egress,
			&t.ConfigFields, &t.Configs, &t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *postgresRepository) Get(schema, name string) (*models.CustomTool, error) {
	var t models.CustomTool
	err := db.DB.QueryRow(
		`SELECT `+columns+` FROM `+table(schema)+` WHERE name = $1`, name,
	).Scan(
		&t.ID, &t.Name, &t.DisplayName, &t.Description, &t.Parameters, &t.Output,
		&t.Lang, &t.Entry, &t.TimeoutMS, &t.MaxOutputBytes, &t.Egress,
		&t.ConfigFields, &t.Configs, &t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *postgresRepository) Create(schema string, t *models.CustomTool) error {
	normalizeRequiredJSON(t)
	if t.ID == "" {
		t.ID = newID()
	}
	if t.Entry == "" {
		t.Entry = "main.py"
	}
	if t.TimeoutMS <= 0 {
		t.TimeoutMS = 60000
	}
	if t.MaxOutputBytes <= 0 {
		t.MaxOutputBytes = 65536
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+table(schema)+`
		 (id, name, display_name, description, parameters, output, lang, entry,
		  timeout_ms, max_output_bytes, egress, config_fields, configs, status, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		t.ID, t.Name, t.DisplayName, t.Description, t.Parameters, t.Output, t.Lang, t.Entry,
		t.TimeoutMS, t.MaxOutputBytes, t.Egress, t.ConfigFields, t.Configs, t.Status, t.CreatedBy,
	)
	return err
}

func (r *postgresRepository) Update(schema string, t *models.CustomTool) error {
	normalizeRequiredJSON(t)
	res, err := db.DB.Exec(
		`UPDATE `+table(schema)+` SET
		     display_name=$2, description=$3, parameters=$4, output=$5, lang=$6, entry=$7,
		     timeout_ms=$8, max_output_bytes=$9, egress=$10, config_fields=$11, configs=$12,
		     status=$13, updated_at=CURRENT_TIMESTAMP
		 WHERE name=$1`,
		t.Name, t.DisplayName, t.Description, t.Parameters, t.Output, t.Lang, t.Entry,
		t.TimeoutMS, t.MaxOutputBytes, t.Egress, t.ConfigFields, t.Configs, t.Status,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *postgresRepository) Delete(schema, name string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// A missing name keeps the previous semantics (a DELETE that matches nothing is not an error).
	var id string
	switch qerr := tx.QueryRow(`SELECT id FROM `+table(schema)+` WHERE name = $1 FOR UPDATE`, name).Scan(&id); {
	case errors.Is(qerr, sql.ErrNoRows):
		return nil
	case qerr != nil:
		return qerr
	}

	// A released version is what production runs: deleting the row would take the tool out of the catalog, which is an
	// explicit unrelease, not a side effect of delete. Same rule as the other three domains.
	var released int
	switch qerr := tx.QueryRow(`SELECT version FROM `+schema+`.tool_versions
		WHERE tool_id = $1 AND is_current`, id).Scan(&released); {
	case qerr == nil:
		return &versions.ErrHasReleasedVersion{Version: released}
	case !errors.Is(qerr, sql.ErrNoRows):
		return qerr
	}

	if _, err := tx.Exec(`DELETE FROM `+table(schema)+` WHERE name = $1`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// newID generates a UUIDv4.
//
// Hand-rolled rather than taking a dependency: this is the only place in the project that needs a random id, and pulling
// in a uuid package is not worth it.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Falling back to a time-unrelated constant when the random source fails would collide on the primary key;
		// panicking here is more honest: a process whose cryptographic random source is unusable should not be serving
		// writes anyway.
		panic(fmt.Sprintf("customtools: generate id: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
