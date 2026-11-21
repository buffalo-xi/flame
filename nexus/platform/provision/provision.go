// Package provision creates a tenant: everything needed for "the new tenant can sign in to the console" inside one transaction.
//
// Orchestration (the order is the design):
//  1. run every check first (schema name / whether it is taken / admin account and password rules) - nothing touches the
//     database unless all of them pass
//  2. do all database-side actions inside a single transaction:
//     CREATE SCHEMA "<schema>"
//     SELECT set_config('search_path', $1, true)   - transaction-scoped, parameterised (no string concatenation)
//     run the template package's template/schema.sql   - every table definition
//     run the template package's template/seed.sql     - main + protected skills + grant bindings
//     INSERT INTO users ...                            - the tenant's first manage user
//     materialise the template package's template/files/ - write the definition-domain files under tenants/<schema>/
//     INSERT INTO platform.tenants ...                 - the registry row (platform schema, explicitly qualified)
//     PostgreSQL supports transactional DDL, so any failure rolls everything back and no half-built schema is left behind.
//  3. invalidate caches after commit (the shared/tenants allow-list + quota) -
//     otherwise the new tenant waits a whole TTL before it can sign in.
//
// Files are written before the commit: on failure the transaction is rolled back and DeletePrefix reclaims what was written, so
// it is "either a complete tenant or nothing left behind".
//
// The schema may be empty: NextSchema finds one automatically (t0, t1, t2... the first unused one);
// if it is taken by the time of the write, ErrSchemaTaken is returned and the API layer answers 409 so the operator confirms a
// new candidate and retries.
package provision

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/account"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xlog"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// templateAssets is the template package for tenant creation resources (embedded as a whole, so the runtime depends on no
// specific tenant):
//
//	template/schema.sql  every table definition (converted from a t0 dump; object names carry no schema prefix)
//	template/seed.sql    rows for the preinstalled assets (main + four protected skills + grants / bindings, fixed uuids)
//	template/files/      definition-domain files (agents/main/AGENT.md, skills/<name>/SKILL.md)
//
// It lives under this package because //go:embed can only embed this package and its subdirectories.
//
//go:embed template
var templateAssets embed.FS

// templateFilesRoot is the root of the file tree inside the package; on materialisation the relative paths are written under
// tenants/<schema>/.
const templateFilesRoot = "template/files"

// Request is the tenant creation input.
type Request struct {
	Schema        string // new tenant schema name: ^[a-z][a-z0-9_]{1,31}$
	DisplayName   string // display name; the schema name when empty
	Plan          string // plan tag, may be empty
	OwnerUserID   string // owner (a platform "user" id); empty = admin-managed
	AdminName     string // the tenant's first manage user (login name)
	AdminPassword string // at least account.MinPasswordLength characters
	CreatedBy     string // operator (platform administrator id), recorded only
}

// Result is the creation result (echoed by the API, in particular "the first admin account" which the caller must be told).
type Result struct {
	Schema      string
	DisplayName string
	AdminName   string
	AdminUserID string
}

var (
	// schemaPattern matches the CHECK constraint on platform.tenants (the database is a second line of defence)
	schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	// adminPattern matches the platform user login-name rule (namePattern in handler/users.go)
	adminPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// reservedSchemas are system schemas that may never be turned into a tenant
	reservedSchemas = map[string]bool{
		"pg_catalog":         true,
		"information_schema": true,
		"public":             true,
		"platform":           true,
	}
)

// DefaultAdminName is the login name of a new tenant's first administrator (the console always uses it and no longer asks the
// operator to type one).
//
// The value comes from shared/tenants.PrimaryUserName: the tenant side uses the same constant for the "root account cannot be
// modified / deleted" guard, and both sides must share the source (platform cannot import nexus/admin or vice versa, hence the
// constant in shared).
const DefaultAdminName = sharedtenants.PrimaryUserName

// ErrSchemaTaken means the schema is taken: the registry already has the row, or a schema of that name already exists in the
// database.
// The API layer answers 409 plus a new candidate so the operator can confirm and retry.
var ErrSchemaTaken = errors.New("schema already taken")

// schemaAutoMax bounds automatic discovery attempts (t0...t9999), preventing an endless loop on abnormal data.
const schemaAutoMax = 10000

// SchemaAvailable reports whether a schema can be used for a tenant: the registry does not have it and the database has no
// schema of that name.
//
// Both must be checked: a soft-deleted registry row may already have had its schema dropped, while conversely an orphan schema
// left by a half-finished creation (or created by hand) may have no registry row - checking only one would hit the wall at
// CREATE SCHEMA.
func SchemaAvailable(schema string) (bool, error) {
	exists, err := tenantsrepo.Exists(schema)
	if err != nil {
		return false, fmt.Errorf("failed to check the tenant registry: %w", err)
	}
	if exists {
		return false, nil
	}
	var n int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = $1`, schema).Scan(&n); err != nil {
		return false, fmt.Errorf("failed to check database schemas: %w", err)
	}
	return n == 0, nil
}

// NextSchema finds the first available tenant schema: t0, t1, t2... (counting up from 0), skipping reserved system names.
//
// It fetches all taken names at once and then tries candidates in memory, avoiding one database round-trip per candidate.
func NextSchema() (string, error) {
	taken, err := takenSchemas()
	if err != nil {
		return "", err
	}
	for i := 0; i <= schemaAutoMax; i++ {
		cand := "t" + strconv.Itoa(i)
		if reservedSchemas[cand] || taken[cand] {
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("tried %d schemas in a row and all are taken; clean up unused tenants first", schemaAutoMax+1)
}

// takenSchemas is every taken schema name = the database's schemas UNION the registry (both sets of facts count).
func takenSchemas() (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT schema_name FROM information_schema.schemata
		 UNION
		 SELECT schema_name FROM ` + pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + `.tenants`)
	if err != nil {
		return nil, fmt.Errorf("failed to read taken schemas: %w", err)
	}
	defer rows.Close()

	taken := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		taken[s] = true
	}
	return taken, rows.Err()
}

// isDuplicateSchema reports whether this is "a schema of that name already exists" (PG 42P06 duplicate_schema).
func isDuplicateSchema(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "42P06"
}

// Provision creates a tenant (idempotency: an already registered schema errors out directly, never created twice).
func Provision(req Request) (*Result, error) {
	schema := strings.TrimSpace(req.Schema)
	displayName := strings.TrimSpace(req.DisplayName)
	adminName := strings.TrimSpace(req.AdminName)

	// ---- 1) Checks (no database access) ----
	if !schemaPattern.MatchString(schema) {
		return nil, fmt.Errorf("invalid schema name: it must start with a lower-case letter, contain only letters/digits/underscores and be 2-32 characters")
	}
	if reservedSchemas[schema] {
		return nil, fmt.Errorf("schema name %q is a reserved system name", schema)
	}
	if !adminPattern.MatchString(adminName) {
		return nil, fmt.Errorf("invalid administrator login name: only letters/digits/underscores/hyphens, 1-64 characters")
	}
	switch {
	case len(req.AdminPassword) < account.MinPasswordLength:
		return nil, fmt.Errorf("the administrator password must be at least %d characters", account.MinPasswordLength)
	case len(req.AdminPassword) > account.MaxPasswordBytes:
		return nil, account.ErrPasswordTooLong
	}

	// "Taken" has two forms: already in the registry / a schema of that name already in the database (including half-built
	// orphans)
	available, err := SchemaAvailable(schema)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, fmt.Errorf("%w: %q is already in the registry or a schema of that name already exists in the database (handle the old tenant first if a rebuild is intended)", ErrSchemaTaken, schema)
	}
	if displayName == "" {
		displayName = schema
	}

	adminHash, err := account.HashPassword(req.AdminPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to hash the password: %w", err)
	}
	adminID := uuid.NewString()

	// ---- 2) A single transaction: create the schema + tables from the template + first admin + registry row ----
	// DDL always goes through the main pool: platform.ddl_dsn could once point at a separate DDL account, and that setting is
	// gone.
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin the transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // rolling back after a commit is a harmless no-op

	// The schema name already passed the regex; QuoteIdentifier escaping is still applied (defence in depth)
	if _, err := tx.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		// Taken between the check above and this point (concurrent creation / someone created a schema by hand)
		if isDuplicateSchema(err) {
			return nil, fmt.Errorf("%w: %q already exists in the database", ErrSchemaTaken, schema)
		}
		return nil, fmt.Errorf("failed to create the schema: %w", err)
	}
	// Transaction-scoped search_path: every object name in the template is unqualified and lands in the new schema through it
	if _, err := tx.Exec(`SELECT set_config('search_path', $1, true)`, schema); err != nil {
		return nil, fmt.Errorf("failed to set search_path: %w", err)
	}
	// Template package: structure first, then the preinstalled assets. Both scripts assume search_path already points at the
	// new schema.
	schemaSQL, err := templateAssets.ReadFile("template/schema.sql")
	if err != nil {
		return nil, fmt.Errorf("failed to read the template table definitions: %w", err)
	}
	seedSQL, err := templateAssets.ReadFile("template/seed.sql")
	if err != nil {
		return nil, fmt.Errorf("failed to read the template's preinstalled assets: %w", err)
	}
	if _, err := tx.Exec(string(schemaSQL)); err != nil {
		return nil, fmt.Errorf("failed to run the template table definitions: %w", err)
	}
	// The seed uses fixed uuids, so a second run would hit the primary key - creation is a one-shot action, not idempotent
	if _, err := tx.Exec(string(seedSQL)); err != nil {
		return nil, fmt.Errorf("failed to write the preinstalled assets: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO users (id, name, display_name, email, phone, password, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, '', '', $4, 'manage', 'active', NOW(), NOW())`,
		adminID, adminName, adminName, adminHash); err != nil {
		return nil, fmt.Errorf("failed to create the tenant administrator: %w", err)
	}

	// Definition-domain files are materialised before the commit: the package's files/ -> tenants/<schema>/
	// On failure the transaction is rolled back and the directory cleaned, so no half-built tenant is left ("database built, but
	// main has no AGENT.md")
	if err := materializeTemplateFiles(context.Background(), schema); err != nil {
		cleanupTenantDir(schema)
		return nil, err
	}

	// At this point search_path points at the new tenant schema, so platform tables must be explicitly qualified
	// (the column list matches repository/tenants.Create; change both when changing it)
	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	if _, err := tx.Exec(
		`INSERT INTO `+platformSchema+`.tenants
		   (schema_name, display_name, status, plan, owner_user_id, remark, created_at, updated_at, created_by)
		 VALUES ($1, $2, 'active', $3, NULLIF($4, '')::uuid, '', NOW(), NOW(), NULLIF($5, '')::uuid)`,
		schema, displayName, strings.TrimSpace(req.Plan),
		strings.TrimSpace(req.OwnerUserID), strings.TrimSpace(req.CreatedBy)); err != nil {
		return nil, fmt.Errorf("failed to write the tenant registry row: %w", err)
	}

	// Default capability grants: a new tenant can use every registered capability out of the box (exec / shell / code / browser
	// / egress).
	// The platform can still turn them off one by one and the global gate can still emergency-stop them - what is written here
	// is a tenant row and bypasses no layer.
	if err := grantDefaultCapabilities(tx, schema); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// ---- 4) Cache invalidation (after the commit, so a rollback does not invalidate for nothing) ----
	sharedtenants.Invalidate(schema)
	quota.Invalidate(schema)

	xlog.Info("[platform-provision] created tenant %s (first administrator %s)", schema, adminName)
	return &Result{Schema: schema, DisplayName: displayName, AdminName: adminName, AdminUserID: adminID}, nil
}

// ErrTenantNotFound means the registry does not have this tenant: the delete endpoint uses it to tell "never existed" from
// "failed halfway".
var ErrTenantNotFound = errors.New("tenant not found")

// Remove deletes a tenant outright: the registry row + the platform-side config rows owned by that tenant_id + the whole tenant
// schema.
//
// The order is the design (strictly the reverse of Provision):
//  1. confirm the registry has the row - only "tenants the platform knows" are deleted, so a slip cannot DROP some other schema
//     (hand-made objects, someone else's database) as a tenant
//  2. inside one transaction: clear the platform-side tenant rows -> DROP SCHEMA ... CASCADE -> delete the registry row
//     The registry row goes last: only once it is gone does SchemaAvailable consider the name free, and a mid-way failure rolls
//     everything back, so no orphan of "registry gone but schema still there" is left
//  3. the storage directory is not deleted here (this package only touches the database): the API layer calls
//     storage.DeletePrefix
//
// Irreversible: that tenant's agents, skills, flows, users and session records all disappear with the schema.
func Remove(schema string) error {
	schema = strings.TrimSpace(schema)
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("invalid schema name: it must start with a lower-case letter, contain only letters/digits/underscores and be 2-32 characters")
	}
	if reservedSchemas[schema] {
		return fmt.Errorf("schema name %q is a reserved system name", schema)
	}

	exists, err := tenantsrepo.Exists(schema)
	if err != nil {
		return fmt.Errorf("failed to check the tenant registry: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: %q", ErrTenantNotFound, schema)
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin the transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Platform-side tenant rows: plugin grants, capability grants, quotas, flow run limits, agent run limits
	for _, table := range []string{
		"tenant_plugins", "tenant_capabilities", "quota_configs", "flow_configs", "agent_configs",
	} {
		if err := deleteTenantRows(tx, table, schema); err != nil {
			return fmt.Errorf("failed to clean %s: %w", table, err)
		}
	}

	// The schema name already passed the regex; QuoteIdentifier escaping is still applied (defence in depth)
	if _, err := tx.Exec(`DROP SCHEMA IF EXISTS ` + pq.QuoteIdentifier(schema) + ` CASCADE`); err != nil {
		return fmt.Errorf("failed to drop the schema: %w", err)
	}

	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	if _, err := tx.Exec(`DELETE FROM `+platformSchema+`.tenants WHERE schema_name = $1`, schema); err != nil {
		return fmt.Errorf("failed to delete the tenant registry row: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	return nil
}

// deleteTenantRows clears the rows belonging to one tenant in a platform table.
//
// to_regclass probes first: deployments may be at different script progress points (for example agent_configs did not exist
// early on), and one optional table missing must not bring the whole delete down.
func deleteTenantRows(tx *sql.Tx, table string, tenantID string) error {
	ps := config.Cfg.Database.PlatformSchema
	var exists sql.NullString
	if err := tx.QueryRow(`SELECT to_regclass($1)::text`, ps+"."+table).Scan(&exists); err != nil {
		return err
	}
	if !exists.Valid {
		return nil
	}
	_, err := tx.Exec(
		`DELETE FROM `+pq.QuoteIdentifier(ps)+`.`+pq.QuoteIdentifier(table)+` WHERE tenant_id = $1`,
		tenantID)
	return err
}

// grantDefaultCapabilities writes an allowed row for every capability to a new tenant (allowed by default).
//
// Why materialise rather than "allow by default": capabilitypolicy means **deny by default**, and no row = not enabled.
// To make a new tenant usable out of the box the rows must be written; changing it to "no row means allowed" would make this
// platform-side gate fail open for every existing tenant (back to "the tenant decides").
//
// The global gate (platform.capability_gates) still overrides this: an emergency-stopped capability stays unusable.
func grantDefaultCapabilities(tx *sql.Tx, tenantID string) error {
	keys := capability.All()
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	for _, k := range keys {
		normalized = append(normalized, capability.Normalize(k))
	}

	platformSchema := pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema)
	_, err := tx.Exec(
		`INSERT INTO `+platformSchema+`.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
		 SELECT $1, k, true, CURRENT_TIMESTAMP FROM unnest($2::text[]) AS k
		 ON CONFLICT (tenant_id, capability) DO UPDATE
		    SET is_allowed = true, updated_at = CURRENT_TIMESTAMP`,
		tenantID, pq.Array(normalized))
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return fmt.Errorf("failed to write capability grants (run nexus/platform/sql/tenant_capabilities.sql first): %w", err)
		}
		return fmt.Errorf("failed to write capability grants: %w", err)
	}
	return nil
}

// materializeTemplateFiles writes the package's definition-domain files into the new tenant directory:
//
//	template/files/agents/main/AGENT.md -> tenants/<schema>/agents/main/AGENT.md
//	template/files/skills/<name>/**     -> tenants/<schema>/skills/<name>/**
//
// It only writes files and creates no directories: the local implementation's WriteFile does MkdirAll itself, and on S3 writing
// an object creates the prefix (see shared/storage).
func materializeTemplateFiles(ctx context.Context, schema string) error {
	st, err := templateStorage()
	if err != nil {
		return fmt.Errorf("failed to initialise storage: %w", err)
	}

	return fs.WalkDir(templateAssets, templateFilesRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// io/fs paths always use forward slashes, so they can be joined straight into the tenant root
		rel := strings.TrimPrefix(p, templateFilesRoot+"/")
		data, err := templateAssets.ReadFile(p)
		if err != nil {
			return err
		}
		if err := st.WriteFile(ctx, path.Join(paths.TenantsSubdir, schema, rel), data); err != nil {
			return fmt.Errorf("failed to write %s: %w", rel, err)
		}
		return nil
	})
}

// cleanupTenantDir clears all files of one tenant in storage (reclaiming a half-built tenant when creation fails).
func cleanupTenantDir(schema string) {
	st, err := templateStorage()
	if err != nil {
		return
	}
	if derr := st.DeletePrefix(context.Background(), path.Join(paths.TenantsSubdir, schema)); derr != nil {
		xlog.Error("[platform-provision] failed to reclaim the storage directory of tenant %s: %v", schema, derr)
	}
}

// templateStorage lazily creates the storage instance (same config as service/agent.InitStorage).
var (
	provStoreOnce sync.Once
	provStore     storage.Storage
	provStoreErr  error
)

func templateStorage() (storage.Storage, error) {
	provStoreOnce.Do(func() {
		if config.Cfg == nil {
			provStoreErr = errors.New("config not loaded")
			return
		}
		provStore, provStoreErr = storage.New(config.Cfg.Storage)
	})
	return provStore, provStoreErr
}
