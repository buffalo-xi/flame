// Package pmusers is the data access layer for the platform user table (platform.users).
//
// Platform users (administrator admin / user user) are completely unrelated to each tenant's <schema>.users
// (same name, different thing: this is the platform login identity, that is an account inside a tenant):
// password, status and role all live in this table, and platform login only queries it.
//
// Note: platform tables are always referenced with an explicit schema prefix; never write a bare users -
// tenant schemas have a table of the same name, and a missing prefix silently hits the wrong table.
package pmusers

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// User is one platform.users row (Password is the bcrypt hash).
type User struct {
	ID          string
	Name        string // login name, unique and immutable
	DisplayName string
	Password    string
	Role        string // admin | user
	Status      string // active | disabled | locked
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func tableName() string { return config.Cfg.Database.PlatformSchema + ".users" }

const columns = `id, name, COALESCE(display_name,''), COALESCE(password,''), COALESCE(role,''), COALESCE(status,''),
                 created_at, updated_at`

// scanner works with both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner, u *User) error {
	return s.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Password, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
}

// GetByName fetches a user by login name; a missing one returns (nil, nil).
func GetByName(name string) (*User, error) {
	var u User
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE name = $1`, strings.TrimSpace(name)), &u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID fetches a user by id; a missing one returns (nil, nil).
func GetByID(id string) (*User, error) {
	var u User
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE id = $1`, strings.TrimSpace(id)), &u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// filter builds the WHERE clause and its arguments.
// List and Count must use the same conditions, so they are extracted here to keep the two from drifting.
func filter(role, status, q string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if r := strings.TrimSpace(role); r != "" {
		args = append(args, r)
		where = append(where, fmt.Sprintf("role = $%d", len(args)))
	}
	if s := strings.TrimSpace(status); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if kw := strings.TrimSpace(q); kw != "" {
		args = append(args, "%"+strings.ToLower(kw)+"%")
		where = append(where, fmt.Sprintf("(LOWER(name) LIKE $%d OR LOWER(display_name) LIKE $%d)", len(args), len(args)))
	}
	return strings.Join(where, " AND "), args
}

// List lists platform users. An empty role / status means no filter; q does a case-insensitive fuzzy match on name and
// display_name.
// limit <= 0 means no paging (return everything); otherwise one page is taken with LIMIT/OFFSET, offset starting at 0.
func List(role, status, q string, limit, offset int) ([]User, error) {
	cond, args := filter(role, status, q)
	sqlStr := `SELECT ` + columns + ` FROM ` + tableName() + ` WHERE ` + cond + ` ORDER BY role, name`
	if limit > 0 {
		args = append(args, limit, offset)
		sqlStr += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := db.DB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]User, 0)
	for rows.Next() {
		var u User
		if err := scan(rows, &u); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}

// Count counts the platform users matching the conditions (the same ones as List) - the paged total uses it rather than
// len(items).
func Count(role, status, q string) (int, error) {
	cond, args := filter(role, status, q)
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+tableName()+` WHERE `+cond, args...).Scan(&n)
	return n, err
}

// Create inserts a platform user (the caller generates the id; Password must already be hashed).
func Create(u *User) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (id, name, display_name, password, role, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW())`,
		u.ID, u.Name, u.DisplayName, u.Password, u.Role, u.Status)
	return err
}

// UpdateProfile updates the display name / role / status (name is immutable: it anchors login and the audit trail).
func UpdateProfile(id, displayName, role, status string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET display_name=$2, role=$3, status=$4, updated_at=NOW() WHERE id=$1`,
		strings.TrimSpace(id), displayName, role, status)
	return err
}

// UpdatePassword resets the password (hash must be a bcrypt value).
func UpdatePassword(id, hash string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET password=$2, updated_at=NOW() WHERE id=$1`, strings.TrimSpace(id), hash)
	return err
}

// CountActiveAdmins counts the "signable administrators".
// It backs the guard "the last administrator cannot be disabled or demoted" (otherwise the platform locks itself out and only
// editing the database can save it).
//
// The only call site is that guard in handler/users.go.
//
// The pool is still checked explicitly: database/sql panics on a nil *DB (this has actually happened).
func CountActiveAdmins() (int, error) {
	if db.DB == nil {
		return 0, errors.New("database not initialized")
	}
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM ` + tableName() + ` WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, err
}
