package globalvars

import (
	"database/sql"
	"errors"
	"fmt"

	"flame/nexus/shared/db"
	"flame/nexus/shared/secureval"
)

// PostgresRepository is the PostgreSQL implementation of global variable storage.
type PostgresRepository struct{}

// The column list lives in one place: add a column here and both List and Get follow.
// updated_by may be NULL and is read into xsql.UUID (NULL -> empty string).
const columns = `name, value, kind, description, updated_by, updated_at`

// List returns every variable of one tenant (ascending by name, the order the UI displays).
func (r *PostgresRepository) List(schema string) ([]GlobalVar, error) {
	rows, err := db.DB.Query(`SELECT ` + columns + ` FROM ` + schema + `.global_vars ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]GlobalVar, 0)
	for rows.Next() {
		var v GlobalVar
		if err := rows.Scan(&v.Name, &v.Value, &v.Kind, &v.Description, &v.UpdatedBy, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if err := openValue(&v); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// Get returns one variable; a missing one returns (nil, nil).
func (r *PostgresRepository) Get(schema, name string) (*GlobalVar, error) {
	if name == "" {
		return nil, nil
	}
	var v GlobalVar
	err := db.DB.QueryRow(`SELECT `+columns+` FROM `+schema+`.global_vars WHERE name = $1`, name).
		Scan(&v.Name, &v.Value, &v.Kind, &v.Description, &v.UpdatedBy, &v.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := openValue(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Upsert writes or overwrites by name.
//
// created_by deliberately lands only on INSERT: overwriting a value is "changing the configuration", not
// "changing the creator".
// updated_at is refreshed on every overwrite — the runtime cache uses it to decide whether to refetch (see
// shared/globalvars).
func (r *PostgresRepository) Upsert(schema string, v *GlobalVar) error {
	if v == nil || v.Name == "" {
		return fmt.Errorf("the global variable has no name")
	}
	value, err := sealValue(v)
	if err != nil {
		return err
	}
	_, err = db.DB.Exec(`
		INSERT INTO `+schema+`.global_vars (name, value, kind, description, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5, NOW(), NOW())
		ON CONFLICT (name) DO UPDATE SET
			value = EXCLUDED.value,
			kind = EXCLUDED.kind,
			description = EXCLUDED.description,
			updated_by = EXCLUDED.updated_by,
			updated_at = NOW()`,
		v.Name, value, v.Kind, v.Description, v.UpdatedBy)
	return err
}

// Delete removes one variable and reports whether it hit anything.
func (r *PostgresRepository) Delete(schema, name string) (bool, error) {
	res, err := db.DB.Exec(`DELETE FROM `+schema+`.global_vars WHERE name = $1`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// sealValue returns what should go into the value column: secret is encrypted, everything else as is.
//
// It does not touch the caller's v.Value — the handler holds the decrypted plaintext and still needs it for the
// response, and rewriting it in place would echo a string of enc:v1:... in the "saved" reply.
func sealValue(v *GlobalVar) (string, error) {
	if v.Kind != KindSecret || v.Value == "" {
		return v.Value, nil
	}
	sealed, err := secureval.Encrypt(v.Value)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt the value of variable %s: %w", v.Name, err)
	}
	return sealed, nil
}

// openValue restores the value column to plaintext after reading (in place).
//
// Anything that is not secret, an empty value, and plaintext written before encryption shipped are all returned
// as is by secureval.Decrypt.
func openValue(v *GlobalVar) error {
	if v.Kind != KindSecret || v.Value == "" {
		return nil
	}
	plain, err := secureval.Decrypt(v.Value)
	if err != nil {
		return fmt.Errorf("failed to decrypt variable %s (was the key rotated?): %w", v.Name, err)
	}
	v.Value = plain
	return nil
}
