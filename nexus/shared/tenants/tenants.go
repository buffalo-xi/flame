// Package tenants is the read-only allowlist over the tenant registry (platform.tenants).
//
// Purpose (hot path): /login and resolveTenant on the MCP / RPC side confirm that a schema is "a registered and
// active tenant" before splicing it into SQL —
//
//  1. it narrows the injection surface: the schema is concatenated into an identifier position (SELECT ... FROM
//     <schema>.users), and a character-set check only blocks part of it, while the allowlist restricts the value
//     to "a registered tenant name";
//  2. it makes suspending / deleting a tenant take effect immediately: previously, as long as the schema still
//     existed in the database, login and calls always worked;
//  3. one rule for both: console login and service-side resolution now share the same verdict.
//
// Caching: the in-process sync.Map is gone in favour of a shared Redis cache (see cache.go); when Redis is
// disabled or unavailable it queries the database directly. The platform side calls Invalidate after writing.
package tenants

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// The status vocabulary (matching the CHECK constraint on platform.tenants.status).
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDeleted   = "deleted"
	StatusPending   = "pending"
)

// PrimaryUserName is the login name of a tenant's root account: the first manage user created when the platform
// provisions a tenant (platform/provision.DefaultAdminName takes its value from here).
//
// The tenant side treats it as a **protected account**: it may not be modified (profile / role / status) and may
// not be deleted — which guarantees that "the account handed over at provisioning" is always a valid, loggable
// manage user. The guard lives in updateUser under nexus/admin/users.
//
// The constant lives in shared because neither side may import the other (platform cannot import nexus/admin, and
// admin cannot import nexus/platform), so this is the one shared copy — do not write the literal again on either
// side.
const PrimaryUserName = "primary"

const (
	// ttl is the cache lifetime of a hit registry row
	ttl = 2 * time.Minute
	// negativeTTL is the lifetime of the "not registered" negative cache (so a bad request cannot hammer the
	// database), deliberately shorter than the positive one
	negativeTTL = 30 * time.Second
)

// Tenant is the read-only view of one registry row.
type Tenant struct {
	Schema      string
	DisplayName string
	Status      string
	Plan        string
	OwnerUserID string
}

func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenants"
}

// Get returns the registry row; an unregistered schema returns (nil, nil).
//
// A failed query returns an error and **writes no cache** — the caller must treat the error as "cannot decide"
// and refuse the request (fail-closed), never silently as "not registered" or "let it through".
func Get(schema string) (*Tenant, error) {
	key := strings.TrimSpace(schema)
	if key == "" {
		return nil, nil
	}
	if db.DB == nil {
		return nil, errors.New("database not initialized")
	}
	if t, ok := loadCache(key); ok {
		return t, nil // a nil t means "not registered" (negative cache hit)
	}

	var (
		t     Tenant
		disp  sql.NullString
		plan  sql.NullString
		owner sql.NullString
	)
	err := db.DB.QueryRow(
		`SELECT schema_name, COALESCE(display_name,''), COALESCE(status,''), COALESCE(plan,''),
		        COALESCE(owner_user_id::text,'')
		   FROM `+tableName()+` WHERE schema_name = $1`, key).
		Scan(&t.Schema, &disp, &t.Status, &plan, &owner)
	switch {
	case err == sql.ErrNoRows:
		storeCache(key, nil, negativeTTL)
		return nil, nil
	case err != nil:
		// Errors are not cached: the next request retries, so one hiccup does not get pinned
		return nil, err
	}

	t.DisplayName = disp.String
	t.Plan = plan.String
	t.OwnerUserID = owner.String
	storeCache(key, &t, ttl)
	return &t, nil
}

// IsActive reports whether a schema is a "registered and active" tenant (hot path, cache-backed).
//
// A failed query returns an error: the caller should refuse the request (fail-closed), not treat it as
// true/false.
func IsActive(schema string) (bool, error) {
	t, err := Get(schema)
	if err != nil {
		return false, err
	}
	return t != nil && t.Status == StatusActive, nil
}

// ListSchemas returns every tenant schema (sorted by name).
//
// For one-off startup jobs (a directory migration, say); it does not use the cache. status is not filtered:
// suspended and deleted tenants' directories have to be migrated as well, so no old layout is left behind.
func ListSchemas() ([]string, error) {
	if db.DB == nil {
		return nil, errors.New("database not initialized")
	}
	rows, err := db.DB.Query(`SELECT schema_name FROM ` + tableName() + ` ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
