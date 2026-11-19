// Package quota reads tenant resource quotas and counts resources (platform.quota_configs).
//
// Semantics (following the flow_configs convention):
//   - an empty tenant_id is the system row (the global default); 't0' is a tenant row (overriding the system row)
//   - NULL = not set (fallback: tenant row -> system row -> code fallback)
//   - -1 = unlimited (a real value, it does not fall back)
//
// The system row is also a **hard ceiling** (the same rule as flowconfig): at run time min(tenant value, system value) is used, and a system
// row of -1 means no limit - so configuring a tenant row above the system row is useless, it is clamped to the system value.
// When the system row is not set, the code fallback (100 / 300 / 300 / -1 / -1) is the ceiling: the two session-related limits are unlimited
// by default (per-session storage, sessions per user), while the other three stay finite.
//
// Caching: an in-process sync.Map is no longer used; a shared Redis cache took its place (see cache.go); when Redis is disabled or unavailable
// it goes straight to the database.
package quota

import (
	"database/sql"
	"errors"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// Limits is the finally effective quota ceiling (-1 means unlimited).
type Limits struct {
	MaxAgents int
	MaxSkills int
	MaxFlows  int

	// MaxSessionStorageBytes is the byte ceiling of one session workspace (over the limit, the session's oldest files are deleted).
	MaxSessionStorageBytes int64
	// MaxUserSessions is the number of sessions one user may own (over the limit, new ones are refused; old ones are not deleted).
	MaxUserSessions int
}

// fallback holds the code defaults (used when the table does not exist or has nothing configured).
//
// The two session-related limits are -1 (unlimited): a session is a "working venue" rather than a countable asset, and a default limit means
// that once it is reached not even one round can run (storage 507 / create session 507) - while both can still be tightened explicitly in the
// system row, the tenant row or user_quotas.
var fallback = Limits{
	MaxAgents: 100, MaxSkills: 300, MaxFlows: 300,
	MaxSessionStorageBytes: -1, // per-session workspace unlimited
	MaxUserSessions:        -1, // sessions per user unlimited
}

// row corresponds to one quota_configs row; NULL is expressed with the Null types.
type row struct {
	MaxAgents              sql.NullInt32
	MaxSkills              sql.NullInt32
	MaxFlows               sql.NullInt32
	MaxSessionStorageBytes sql.NullInt64
	MaxUserSessions        sql.NullInt32
}

func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".quota_configs"
}

// Resolve returns the finally effective quota for a tenant (tenant row -> system row -> fallback).
// The data comes from the shared Redis cache (see cache.go); when Redis is disabled or unavailable it goes straight to the database.
// An empty tenantID means the system level.
func Resolve(tenantID string) Limits {
	t := loadRowCached(tenantID)
	s := loadRowCached("")
	return Limits{
		MaxAgents: clampMax(
			pick(field(t, "MaxAgents"), field(s, "MaxAgents"), fallback.MaxAgents),
			pick(sql.NullInt32{}, field(s, "MaxAgents"), fallback.MaxAgents),
		),
		MaxSkills: clampMax(
			pick(field(t, "MaxSkills"), field(s, "MaxSkills"), fallback.MaxSkills),
			pick(sql.NullInt32{}, field(s, "MaxSkills"), fallback.MaxSkills),
		),
		MaxFlows: clampMax(
			pick(field(t, "MaxFlows"), field(s, "MaxFlows"), fallback.MaxFlows),
			pick(sql.NullInt32{}, field(s, "MaxFlows"), fallback.MaxFlows),
		),
		MaxSessionStorageBytes: clampMax64(
			pick64(field64(t, "MaxSessionStorageBytes"), field64(s, "MaxSessionStorageBytes"), fallback.MaxSessionStorageBytes),
			pick64(sql.NullInt64{}, field64(s, "MaxSessionStorageBytes"), fallback.MaxSessionStorageBytes),
		),
		MaxUserSessions: clampMax(
			pick(field(t, "MaxUserSessions"), field(s, "MaxUserSessions"), fallback.MaxUserSessions),
			pick(sql.NullInt32{}, field(s, "MaxUserSessions"), fallback.MaxUserSessions),
		),
	}
}

// Fallback returns the built-in code defaults (used when the system row is unset), so the admin UI can show "what it falls back to when left
// empty".
func Fallback() Limits {
	return fallback
}

// loadRow reads one tenant_id's configuration row; (nil, nil) when the row does not exist, (nil, err) when the query fails.
func loadRow(tenantID string) (*row, error) {
	if db.DB == nil {
		return nil, errors.New("database is not initialised")
	}
	var r row
	err := db.DB.QueryRow(
		`SELECT max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions
		   FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(&r.MaxAgents, &r.MaxSkills, &r.MaxFlows, &r.MaxSessionStorageBytes, &r.MaxUserSessions)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Row is one raw quota row (nil = NULL = not set, fall back to the system row).
//
// The admin UI needs the raw values to tell "not set" from "explicitly set to a number"; for the effective value use Resolve.
type Row struct {
	MaxAgents              *int
	MaxSkills              *int
	MaxFlows               *int
	MaxUserSessions        *int
	MaxSessionStorageBytes *int64
}

// GetRow reads a tenant's raw quota row (an empty string = the system row); with no such row it returns an all-nil structure.
func GetRow(tenantID string) (*Row, error) {
	r, err := loadRow(strings.TrimSpace(tenantID))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return &Row{}, nil
	}
	return &Row{
		MaxAgents:              nullIntPtr(r.MaxAgents),
		MaxSkills:              nullIntPtr(r.MaxSkills),
		MaxFlows:               nullIntPtr(r.MaxFlows),
		MaxUserSessions:        nullIntPtr(r.MaxUserSessions),
		MaxSessionStorageBytes: nullInt64Ptr(r.MaxSessionStorageBytes),
	}, nil
}

// SetRow overwrites a tenant's quota row (an empty string = the system row) and invalidates the cache.
// nil = write NULL (fall back to the system row); -1 = unlimited; a positive number = the ceiling.
//
// It uses "delete then insert inside one transaction" rather than UPSERT, so it does not depend on the unique constraint's shape.
func SetRow(tenantID string, v *Row) error {
	id := strings.TrimSpace(tenantID)
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+tableName()+` WHERE tenant_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO `+tableName()+`
		   (tenant_id, max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		id, nilIfNil(v.MaxAgents), nilIfNil(v.MaxSkills), nilIfNil(v.MaxFlows),
		nilIfNil64(v.MaxSessionStorageBytes), nilIfNil(v.MaxUserSessions)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if id == "" {
		InvalidateAll()
	} else {
		Invalidate(id)
	}
	return nil
}

func nullIntPtr(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func nilIfNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nilIfNil64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// field safely reads the named int field, returning an empty NullInt32 when r is nil (no such row).
func field(r *row, name string) sql.NullInt32 {
	if r == nil {
		return sql.NullInt32{}
	}
	switch name {
	case "MaxAgents":
		return r.MaxAgents
	case "MaxSkills":
		return r.MaxSkills
	case "MaxFlows":
		return r.MaxFlows
	case "MaxUserSessions":
		return r.MaxUserSessions
	}
	return sql.NullInt32{}
}

// field64 safely reads the named int64 field (used for byte counts, avoiding the int32 ceiling).
func field64(r *row, name string) sql.NullInt64 {
	if r == nil {
		return sql.NullInt64{}
	}
	switch name {
	case "MaxSessionStorageBytes":
		return r.MaxSessionStorageBytes
	}
	return sql.NullInt64{}
}

// pick is the two-level fallback: tenant -> sys -> fallback. Only NULL (not set) falls through to the next level.
func pick(tenant, sys sql.NullInt32, fb int) int {
	if tenant.Valid {
		return int(tenant.Int32)
	}
	if sys.Valid {
		return int(sys.Int32)
	}
	return fb
}

// pick64 is pick for int64 (byte counts).
func pick64(tenant, sys sql.NullInt64, fb int64) int64 {
	if tenant.Valid {
		return tenant.Int64
	}
	if sys.Valid {
		return sys.Int64
	}
	return fb
}

// clampMax clamps to a ceiling: v is min'd against ceiling, and -1 counts as infinity (the same rule as flowconfig).
// Example: a system ceiling of 100 with a tenant value of 500 -> 100; a system ceiling of -1 (unlimited) -> the tenant value as is.
func clampMax(v, ceiling int) int {
	if ceiling == -1 {
		return v
	}
	if v == -1 {
		return ceiling
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

// clampMax64 is clampMax for int64 (byte counts).
func clampMax64(v, ceiling int64) int64 {
	if ceiling == -1 {
		return v
	}
	if v == -1 {
		return ceiling
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

// ---------------------------------------------------------------------------
// Per-user overrides ({schema}.user_quotas)
//
// Semantics: **no row**, or a NULL column = follow the tenant; a value = override, but never above the tenant value (a tenant administrator can
// only allocate within the ceiling the platform gives).
//
// A separate table rather than columns on users: the authentication table does not carry resource governance, and adding further quota
// dimensions later (total storage / file count / concurrency) touches only this table.
// ---------------------------------------------------------------------------

// UserQuota is one user's quota overrides, each with Valid=false meaning "follow the tenant".
type UserQuota struct {
	MaxSessions            sql.NullInt32
	MaxSessionStorageBytes sql.NullInt64
}

// UserQuotaInput is the override input: nil = follow the tenant; when both are nil the override row is deleted.
type UserQuotaInput struct {
	MaxSessions            *int
	MaxSessionStorageBytes *int64
}

// ResolveUserSessions returns a user's finally effective session count ceiling.
// The tenant value (already clamped to the system row) is the hard ceiling; a user can only adjust within it.
func ResolveUserSessions(schema, userID string) int {
	base := Resolve(schema).MaxUserSessions
	if userID == "" {
		return base
	}
	q, ok, err := loadUserQuota(schema, userID)
	if err != nil || !ok || !q.MaxSessions.Valid {
		return base
	}
	return clampMax(int(q.MaxSessions.Int32), base)
}

// ResolveUserSessionStorage returns a user's "per session" storage ceiling: the user override clamped to the tenant
// max_session_storage_bytes.
func ResolveUserSessionStorage(schema, userID string) int64 {
	base := Resolve(schema).MaxSessionStorageBytes
	if userID == "" {
		return base
	}
	q, ok, err := loadUserQuota(schema, userID)
	if err != nil || !ok || !q.MaxSessionStorageBytes.Valid {
		return base
	}
	return clampMax64(q.MaxSessionStorageBytes.Int64, base)
}

// UserSessionsCeiling returns the hard ceiling for what this user may configure (= the tenant value), for UI hints and backend validation.
func UserSessionsCeiling(schema string) int {
	return Resolve(schema).MaxUserSessions
}

// UserSessionStorageCeiling returns the hard ceiling for the session storage this user may configure (= the tenant value).
func UserSessionStorageCeiling(schema string) int64 {
	return Resolve(schema).MaxSessionStorageBytes
}

// SetUserQuota writes / clears a user's overrides (both nil = delete the row, back to following the tenant).
func SetUserQuota(schema, userID string, in UserQuotaInput, updatedBy string) error {
	if in.MaxSessions == nil && in.MaxSessionStorageBytes == nil {
		_, err := db.DB.Exec(`DELETE FROM `+schema+`.user_quotas WHERE user_id = $1`, userID)
		return err
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.user_quotas
		     (user_id, max_sessions, max_session_storage_bytes, updated_by, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (user_id) DO UPDATE
		    SET max_sessions              = EXCLUDED.max_sessions,
		        max_session_storage_bytes = EXCLUDED.max_session_storage_bytes,
		        updated_by                = EXCLUDED.updated_by,
		        updated_at                = now()`,
		userID, in.MaxSessions, in.MaxSessionStorageBytes, nullUUID(updatedBy))
	return err
}

// loadUserQuota reads one user_quotas row; (nil,false,nil) when there is no row.
// A missing table (an older tenant that has not been migrated) is treated the same way - quotas keep working from the tenant values, rather
// than refusing to create sessions because the table cannot be queried.
func loadUserQuota(schema, userID string) (*UserQuota, bool, error) {
	if db.DB == nil {
		return nil, false, errors.New("database is not initialised")
	}
	var q UserQuota
	err := db.DB.QueryRow(
		`SELECT max_sessions, max_session_storage_bytes
		   FROM `+schema+`.user_quotas WHERE user_id = $1`, userID,
	).Scan(&q.MaxSessions, &q.MaxSessionStorageBytes)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &q, true, nil
}

func nullUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// Resource counting (for quota checks)
//
// Like flowconfig, this package queries the database directly instead of going through repository interfaces, so that "counting" does not add a
// method to three repository interfaces.
// ---------------------------------------------------------------------------

// CountAgents counts the agents in a tenant.
func CountAgents(schema string) (int, error) {
	return countRows(schema, "agents")
}

// CountSkills counts the skills in a tenant.
func CountSkills(schema string) (int, error) {
	return countRows(schema, "skills")
}

// CountFlows counts the workflows in a tenant.
func CountFlows(schema string) (int, error) {
	return countRows(schema, "flows")
}

// CountUserSessions counts a user's sessions under the given owner type.
// It uses the sessions table's existing idx_sessions_user_latest (an index scan), so the cost is negligible.
func CountUserSessions(schema, ownerType, userID string) (int, error) {
	if db.DB == nil {
		return 0, errors.New("database is not initialised")
	}
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM `+schema+`.sessions WHERE owner_type = $1 AND user_id = $2`,
		ownerType, userID,
	).Scan(&n)
	return n, err
}

// countRows counts the rows of a table. table is only ever passed from constants inside this package; it never accepts external input.
func countRows(schema, table string) (int, error) {
	if db.DB == nil {
		return 0, errors.New("database is not initialised")
	}
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + schema + `.` + table).Scan(&n)
	return n, err
}
