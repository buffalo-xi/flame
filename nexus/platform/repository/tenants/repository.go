// Package tenants is the data access layer for the tenant registry (platform.tenants) - platform-side CRUD.
//
// Note: the allow-list read on the tenant-side hot path lives in shared/tenants (with a TTL cache); both use the same table.
// Any platform-side write must call shared/tenants.Invalidate(schema) afterwards, otherwise "suspend a tenant" can take a
// whole TTL to take effect.
package tenants

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Tenant is one platform.tenants row.
type Tenant struct {
	Schema      string
	DisplayName string
	Status      string
	Plan        string
	OwnerUserID string // owner (platform.users.id); empty means none (admin-managed)
	Remark      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func tableName() string { return config.Cfg.Database.PlatformSchema + ".tenants" }

const columns = `schema_name, COALESCE(display_name,''), COALESCE(status,''), COALESCE(plan,''),
                 COALESCE(owner_user_id::text,''), COALESCE(remark,''), created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner, t *Tenant) error {
	return s.Scan(&t.Schema, &t.DisplayName, &t.Status, &t.Plan, &t.OwnerUserID, &t.Remark, &t.CreatedAt, &t.UpdatedAt)
}

// Get fetches a tenant by schema; a missing one returns (nil, nil).
func Get(schema string) (*Tenant, error) {
	var t Tenant
	err := scan(db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE schema_name = $1`, strings.TrimSpace(schema)), &t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Exists reports whether the schema is registered (including suspended / soft-deleted ones).
func Exists(schema string) (bool, error) {
	t, err := Get(schema)
	return t != nil, err
}

// filter builds the WHERE clause and its arguments.
// List and Count must use the same conditions, so they are extracted here to keep the two from drifting.
func filter(status, q string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if s := strings.TrimSpace(status); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if kw := strings.TrimSpace(q); kw != "" {
		args = append(args, "%"+strings.ToLower(kw)+"%")
		where = append(where, fmt.Sprintf("(LOWER(schema_name) LIKE $%d OR LOWER(display_name) LIKE $%d)", len(args), len(args)))
	}
	return strings.Join(where, " AND "), args
}

// List lists tenants. An empty status means no filter; q matches schema_name / display_name (case-insensitive).
// limit <= 0 means no paging (return everything); otherwise one page is taken with LIMIT/OFFSET, offset starting at 0.
func List(status, q string, limit, offset int) ([]Tenant, error) {
	cond, args := filter(status, q)
	sqlStr := `SELECT ` + columns + ` FROM ` + tableName() + ` WHERE ` + cond + ` ORDER BY schema_name`
	if limit > 0 {
		args = append(args, limit, offset)
		sqlStr += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	return query(sqlStr, args...)
}

// Count counts the tenants matching the conditions (the same ones as List) - the paged total uses it rather than len(items).
func Count(status, q string) (int, error) {
	cond, args := filter(status, q)
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM `+tableName()+` WHERE `+cond, args...).Scan(&n)
	return n, err
}

// ListByOwner lists the tenants under one user's name (used when a user identity enters the console).
func ListByOwner(ownerUserID string) ([]Tenant, error) {
	return query(
		`SELECT `+columns+` FROM `+tableName()+
			` WHERE owner_user_id = $1 AND status <> 'deleted' ORDER BY schema_name`,
		strings.TrimSpace(ownerUserID))
}

// CountByOwner counts the tenants under one user's name (used to warn before disabling that user).
func CountByOwner(ownerUserID string) (int, error) {
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM `+tableName()+` WHERE owner_user_id = $1 AND status <> 'deleted'`,
		strings.TrimSpace(ownerUserID)).Scan(&n)
	return n, err
}

// Create writes the registry row (the schema itself is created by provision, see nexus/platform/provision).
func Create(t *Tenant, createdBy string) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+`
		   (schema_name, display_name, status, plan, owner_user_id, remark, created_at, updated_at, created_by)
		 VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,NOW(),NOW(),NULLIF($7,'')::uuid)`,
		t.Schema, t.DisplayName, t.Status, t.Plan, t.OwnerUserID, t.Remark, createdBy)
	return err
}

// Update changes the display name / remark / owner (an empty ownerUserID clears the owner).
func Update(schema, displayName, remark, ownerUserID string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+`
		    SET display_name=$2, remark=$3, owner_user_id=NULLIF($4,'')::uuid, updated_at=NOW()
		  WHERE schema_name=$1`,
		strings.TrimSpace(schema), displayName, remark, strings.TrimSpace(ownerUserID))
	return err
}

// SetStatus changes the tenant status (active / suspended / deleted / pending).
func SetStatus(schema, status string) error {
	_, err := db.DB.Exec(
		`UPDATE `+tableName()+` SET status=$2, updated_at=NOW() WHERE schema_name=$1`,
		strings.TrimSpace(schema), strings.TrimSpace(status))
	return err
}

// SetQuota overwrites a tenant's quota row (platform.quota_configs).
// The caller must call quota.Invalidate(schema) afterwards (this package does not import shared/quota, to avoid coupling to the
// quota semantics).
func SetQuota(schema string, maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	return setQuotaRow(schema, maxAgents, maxSkills, maxFlows, maxSessionStorageBytes, maxUserSessions)
}

// SetSystemQuota overwrites the **system row** (tenant_id = ''): the default ceiling when a tenant has no config of its own.
// The caller must call quota.InvalidateAll() afterwards (once the system row changes, every tenant's cached cell is stale).
func SetSystemQuota(maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	return setQuotaRow("", maxAgents, maxSkills, maxFlows, maxSessionStorageBytes, maxUserSessions)
}

// QuotaRow is the raw quota_configs row: nil = NULL = unset (**no** system-row fallback is applied here).
type QuotaRow struct {
	MaxAgents *int
	MaxSkills *int
	MaxFlows  *int

	// MaxSessionStorageBytes is the per-session storage ceiling in bytes; nil = fall back to the system row.
	MaxSessionStorageBytes *int64
	// MaxUserSessions is the per-user session ceiling; nil = fall back to the system row.
	MaxUserSessions *int
}

// GetQuotaRow reads the raw quota row; when the row is absent it returns a zero value (all nil).
//
// The console must see the raw values to tell "unset" from "explicitly set to some number";
// for the "final effective value" go through shared/quota.Resolve (it falls back tenant -> sys -> fallback).
func GetQuotaRow(tenantID string) (*QuotaRow, error) {
	var agents, skills, flows sql.NullInt32
	var sessBytes sql.NullInt64
	var userSessions sql.NullInt32
	err := db.DB.QueryRow(
		`SELECT max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions
		   FROM `+quotaTable()+` WHERE tenant_id = $1`,
		strings.TrimSpace(tenantID)).Scan(&agents, &skills, &flows, &sessBytes, &userSessions)
	if err == sql.ErrNoRows {
		return &QuotaRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &QuotaRow{
		MaxAgents:              nullInt(agents),
		MaxSkills:              nullInt(skills),
		MaxFlows:               nullInt(flows),
		MaxSessionStorageBytes: nullInt64(sessBytes),
		MaxUserSessions:        nullInt(userSessions),
	}, nil
}

// setQuotaRow overwrites one quota_configs row (an empty tenantID means the system row).
//
// Semantics follow the existing convention: NULL = unset (fall back to the system row), -1 = unlimited, a positive number = ceiling.
// It uses "delete then insert inside a transaction" rather than UPSERT, so it does not depend on the unique constraint's name or
// shape.
func setQuotaRow(tenantID string, maxAgents, maxSkills, maxFlows *int,
	maxSessionStorageBytes *int64, maxUserSessions *int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	tbl := quotaTable()
	id := strings.TrimSpace(tenantID)
	if _, err := tx.Exec(`DELETE FROM `+tbl+` WHERE tenant_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO `+tbl+`
		   (tenant_id, max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		id, quotaArg(maxAgents), quotaArg(maxSkills), quotaArg(maxFlows),
		quotaArg64(maxSessionStorageBytes), quotaArg(maxUserSessions)); err != nil {
		return err
	}
	return tx.Commit()
}

func quotaTable() string {
	return config.Cfg.Database.PlatformSchema + ".quota_configs"
}

// nullInt converts sql.NullInt32 -> *int (Invalid -> nil).
func nullInt(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

// quotaArg maps nil -> NULL (unset), otherwise writes the value as is (-1 means unlimited).
func quotaArg(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullInt64 converts sql.NullInt64 -> *int64 (Invalid -> nil).
func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// quotaArg64 is quotaArg for bigint columns (the session storage ceiling).
func quotaArg64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func query(sqlStr string, args ...any) ([]Tenant, error) {
	rows, err := db.DB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Tenant, 0)
	for rows.Next() {
		var t Tenant
		if err := scan(rows, &t); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}
