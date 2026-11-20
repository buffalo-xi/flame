// Package sharedassets reads and writes the cross-tenant asset distribution tables (platform.shared_assets /
// shared_asset_tenants / shared_asset_installs).
//
// It lives in shared/repository rather than writing the SQL twice on each side: the tenant side (admin/share) lists and
// installs, the platform side (platform/handler) reviews and delists, and **the same row** is changed by both in turn —
// with two separate column orders, "this side scans fine, that side scans misaligned" is only a matter of time. The
// platform package may depend on shared/*, and the tenant package already sits under shared, so this is the symmetric
// single landing spot.
//
// The platform schema name comes from configuration (config.Cfg.Database.PlatformSchema) and is never taken from a
// caller — a table name is a second-order injection surface and must not be decided by a request body.
package sharedassets

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"

	"github.com/google/uuid"

	"github.com/lib/pq"
)

// Asset states (the same set as the CHECK in platform/sql/shared_assets.sql).
const (
	StatusDraft    = "draft"
	StatusPending  = "pending"
	StatusListed   = "listed"
	StatusRejected = "rejected"
	StatusDelisted = "delisted"
)

// Visibility scopes.
const (
	VisibilityAll       = "all"
	VisibilityAllowlist = "allowlist"
)

// kind values (the same set as assetpack.Kind; that layer is not reused here so this package can be used by either side
// on its own).
const (
	KindSkill    = "skill"
	KindTool     = "custom_tool"
	KindAgent    = "agent"
	KindWorkflow = "workflow"
)

// columns are in one-to-one correspondence with the Scan order (change both together when the table changes).
const columns = `id, kind, name, display_name, description, source_schema, source_id,
	build, status, visibility, manifest, payload, listed_by, listed_by_name,
	review_note, created_at, updated_at,
	origin_server, origin_asset_id, imported_at, imported_by`

// Asset is one listed snapshot (JSON columns keep their raw bytes and are decoded on demand).
type Asset struct {
	ID           string
	Kind         string
	Name         string
	DisplayName  string
	Description  string
	SourceSchema string
	SourceID     string
	Build        int
	Status       string
	Visibility   string
	Manifest     []byte
	Payload      []byte
	ListedBy     sql.NullString
	ListedByName string
	ReviewNote   string // platform review note (a rejection reason is written here)
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// The origin dimension for a cross-server import (rows listed on this machine all carry the defaults: ''/NULL)
	OriginServer  string         // the origin site name (confirmed by the operator on import; defaults to the server name in the pack)
	OriginAssetID sql.NullString // the asset id on the other machine (provenance only, not a reference)
	ImportedAt    sql.NullTime
	ImportedBy    sql.NullString
}

// Imported reports whether the asset was imported from another server (rather than listed by a tenant on this machine).
func (a *Asset) Imported() bool { return a != nil && a.OriginServer != "" }

// Scan scans an Asset out of one row (shared by the repository internals and callers' custom queries).
func Scan(sc interface{ Scan(...any) error }, a *Asset) error {
	return sc.Scan(
		&a.ID, &a.Kind, &a.Name, &a.DisplayName, &a.Description,
		&a.SourceSchema, &a.SourceID, &a.Build, &a.Status, &a.Visibility,
		&a.Manifest, &a.Payload, &a.ListedBy, &a.ListedByName,
		&a.ReviewNote, &a.CreatedAt, &a.UpdatedAt,
		&a.OriginServer, &a.OriginAssetID, &a.ImportedAt, &a.ImportedBy,
	)
}

// Columns keeps the column order consistent for callers building their own queries.
func Columns() string { return columns }

func table(name string) string {
	return pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + "." + name
}

// Get fetches one row by id; missing returns (nil, nil), matching the semantics of the repository's existing packages.
func Get(id string) (*Asset, error) {
	var a Asset
	if err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+` WHERE id = $1`, id), &a); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// Filter holds the list filters (a zero value filters nothing).
type Filter struct {
	Kind         string
	Status       string
	SourceSchema string // the ones I listed
	OriginServer string // which peer site they were imported from (an empty string does not filter)
	// OriginLocal selects only the rows listed on this machine (an empty origin_server). It is mutually exclusive with
	// OriginServer, and OriginLocal wins when both are set (a locally listed row always has an empty origin_server).
	OriginLocal bool
	Query       string // fuzzy match on name / display_name / description
	// VisibleTo, when non-empty, returns only what that tenant may install: listed + (all, or an allowlist containing it)
	VisibleTo string
	Limit     int
	Offset    int
}

// where builds the WHERE clause. skipOrigin serves the "origin summary": even when the current filter selects one
// origin, the dropdown must still show the others, otherwise the remaining options vanish once test is picked.
func where(f Filter, skipOrigin bool) ([]string, []any) {
	where := make([]string, 0, 6)
	args := make([]any, 0, 6)

	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}

	if f.Kind != "" {
		add("kind = ?", f.Kind)
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.SourceSchema != "" {
		add("source_schema = ?", f.SourceSchema)
	}
	if !skipOrigin {
		if f.OriginLocal {
			where = append(where, "origin_server = ''")
		} else if f.OriginServer != "" {
			add("origin_server = ?", f.OriginServer)
		}
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		p := "$" + itoa(len(args))
		where = append(where, "(name ILIKE "+p+" OR display_name ILIKE "+p+" OR description ILIKE "+p+")")
	}
	if f.VisibleTo != "" {
		args = append(args, f.VisibleTo)
		p := "$" + itoa(len(args))
		where = append(where, "status = 'listed'")
		where = append(where, `(visibility <> 'allowlist' OR EXISTS (
			SELECT 1 FROM `+table("shared_asset_tenants")+` w WHERE w.asset_id = shared_assets.id AND w.schema_name = `+p+`))`)
	}
	return where, args
}

// List lists assets by filter.
func List(f Filter) ([]Asset, error) {
	where, args := where(f, false)

	q := `SELECT ` + columns + ` FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY updated_at DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + itoa(f.Limit) + ` OFFSET ` + itoa(f.Offset)
	}

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Asset, 0, 16)
	for rows.Next() {
		var a Asset
		if err := Scan(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Count counts with the same filters as List (Limit/Offset ignored), so "load more" can tell the frontend how many are
// left.
func Count(f Filter) (int, error) {
	where, args := where(f, false)
	q := `SELECT count(*) FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountByOrigin counts rows per origin site (an empty key means listed on this machine), for the "origin" dropdown in the
// UI. It ignores the origin criteria of the Filter while the other criteria (kind / status / visibility / search) still
// apply, so "the result set changed but the origin options do not disappear".
func CountByOrigin(f Filter) (map[string]int, error) {
	where, args := where(f, true)
	q := `SELECT origin_server, count(*) FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` GROUP BY origin_server`

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int, 4)
	for rows.Next() {
		var origin string
		var n int
		if err := rows.Scan(&origin, &n); err != nil {
			return nil, err
		}
		out[origin] = n
	}
	return out, rows.Err()
}

// Insert creates one row (listing / import).
func Insert(a *Asset) error {
	_, err := db.DB.Exec(`INSERT INTO `+table("shared_assets")+`
		(id, kind, name, display_name, description, source_schema, source_id, build,
		 status, visibility, manifest, payload, listed_by, listed_by_name,
		 origin_server, origin_asset_id, imported_at, imported_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::uuid,$8,$9,$10,$11::jsonb,$12::jsonb,$13::uuid,$14,
		        $15,$16::uuid,$17,$18::uuid)`,
		a.ID, a.Kind, a.Name, a.DisplayName, a.Description, a.SourceSchema, a.SourceID,
		a.Build, a.Status, a.Visibility, string(a.Manifest), string(a.Payload),
		a.ListedBy, a.ListedByName,
		a.OriginServer, a.OriginAssetID, a.ImportedAt, a.ImportedBy)
	return err
}

// FindByOriginName looks an imported row up by "origin site + kind + source schema + name" (import de-duplication; the
// name is case insensitive).
//
// The same rule as the unique index shared_assets_line_idx: a row listed on this machine always has an empty
// origin_server and is therefore never found.
func FindByOriginName(originServer, kind, sourceSchema, name string) (*Asset, error) {
	var a Asset
	err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+
		` WHERE origin_server = $1 AND kind = $2 AND source_schema = $3 AND lower(name) = lower($4)`,
		originServer, kind, sourceSchema, name), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// FindByOriginAsset finds the local row for "origin site + the other side's asset id" (the fast de-duplication path for
// on-demand pulls): one remote asset installed by several tenants is downloaded once and shares the same local row.
func FindByOriginAsset(originServer, originAssetID string) (*Asset, error) {
	originServer = strings.TrimSpace(originServer)
	originAssetID = strings.TrimSpace(originAssetID)
	if originServer == "" || originAssetID == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(originAssetID); err != nil {
		return nil, nil // the other side gave a non-uuid id; treat it as never imported
	}
	var a Asset
	err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+
		` WHERE origin_server = $1 AND origin_asset_id = $2::uuid ORDER BY build DESC LIMIT 1`,
		originServer, originAssetID), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ErrStaleBuild means the build computed this time is already stale: another concurrent listing / import pushed the row
// to a higher number.
//
// Why turn this into an error instead of writing anyway: build is monotonic (snapshots only grow, and old numbers are
// reclaimed by PruneOldBuilds). If the slower write went through, the row would fall back to an old number that may
// already have been deleted, ending up as "the row points at a nonexistent snapshot" — export, install and peer pulls all
// come up empty with no visible cause. A caller that gets it should discard the snapshot it just laid down and ask the
// user to refresh and retry.
var ErrStaleBuild = errors.New("this asset was overwritten by another listing/import (the snapshot number has moved on)")

// staleOrErr turns "0 rows affected" into ErrStaleBuild (that is what makes `WHERE build < $n` effective).
func staleOrErr(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStaleBuild
	}
	return nil
}

// UpdateImported overwrites an import: replace the content and snapshot, go back to pending review, and refresh the
// provenance (the id is unchanged, so install records still point at the same asset).
//
// The WHERE carries `build < $4`: when two concurrent imports each compute the same number, the one that lands first wins
// and the later one gets ErrStaleBuild (instead of writing the row back to an old number).
func UpdateImported(a *Asset) error {
	res, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET display_name = $2, description = $3, build = $4, status = $5,
		    manifest = $6::jsonb, payload = $7::jsonb,
		    origin_asset_id = $8::uuid, imported_at = $9, imported_by = $10::uuid,
		    reviewed_by = NULL, reviewed_at = NULL, review_note = '', updated_at = now()
		WHERE id = $1 AND build < $4`,
		a.ID, a.DisplayName, a.Description, a.Build, a.Status,
		string(a.Manifest), string(a.Payload),
		a.OriginAssetID, a.ImportedAt, a.ImportedBy)
	return staleOrErr(res, err)
}

// UpdateListing re-lists (overwrite): change the content, go back to pending review, and clear the previous review
// verdict.
//
// The WHERE carries `build < $5` as well (the reason is in UpdateImported).
func UpdateListing(a *Asset) error {
	res, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET display_name = $2, description = $3, source_id = $4::uuid, build = $5,
		    status = $6, manifest = $7::jsonb, payload = $8::jsonb,
		    listed_by = $9::uuid, listed_by_name = $10,
		    reviewed_by = NULL, reviewed_at = NULL, review_note = '', updated_at = now()
		WHERE id = $1 AND build < $5`,
		a.ID, a.DisplayName, a.Description, a.SourceID, a.Build,
		a.Status, string(a.Manifest), string(a.Payload),
		a.ListedBy, a.ListedByName)
	return staleOrErr(res, err)
}

// Review is the platform review: change the state and record the reviewer and the note.
func Review(id, status, note, reviewerID string) error {
	_, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET status = $2, review_note = $3, reviewed_by = NULLIF($4, '')::uuid,
		    reviewed_at = now(), updated_at = now()
		WHERE id = $1`, id, status, note, reviewerID)
	return err
}

// SetStatus changes the state only (used by the tenant side to withdraw: the reviewer and the review time are untouched).
func SetStatus(id, status string) error {
	_, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

// Delete deletes one row (withdrawing a draft).
func Delete(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+table("shared_assets")+` WHERE id = $1`, id)
	return err
}

// SetVisibility sets the visibility scope; in allowlist mode the whitelist is replaced wholesale (in a transaction).
func SetVisibility(id, visibility string, schemas []string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE `+table("shared_assets")+`
		SET visibility = $2, updated_at = now() WHERE id = $1`, id, visibility); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+table("shared_asset_tenants")+` WHERE asset_id = $1`, id); err != nil {
		return err
	}
	if visibility == VisibilityAllowlist {
		for _, s := range schemas {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO `+table("shared_asset_tenants")+` (asset_id, schema_name)
				VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, s); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// VisibleSchemas lists the whitelisted tenants of an asset (allowlist mode).
func VisibleSchemas(id string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT schema_name FROM `+table("shared_asset_tenants")+
		` WHERE asset_id = $1 ORDER BY schema_name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// VisibleTo reports whether an asset is visible to a tenant (all is always visible; allowlist checks the whitelist).
func VisibleTo(a *Asset, schema string) (bool, error) {
	if a.Visibility != VisibilityAllowlist {
		return true, nil
	}
	var one int
	err := db.DB.QueryRow(`SELECT 1 FROM `+table("shared_asset_tenants")+
		` WHERE asset_id = $1 AND schema_name = $2`, a.ID, schema).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// TenantDisplayName returns a tenant's display name (falling back to the schema, so an empty string never leaks into the
// UI).
func TenantDisplayName(schema string) string {
	var name string
	if err := db.DB.QueryRow(`SELECT display_name FROM `+table("tenants")+
		` WHERE schema_name = $1`, schema).Scan(&name); err != nil || strings.TrimSpace(name) == "" {
		return schema
	}
	return name
}

// Install is one install record (asset-side information included, so a list can use it directly).
type Install struct {
	AssetID         string
	Kind            string
	AssetName       string
	SourceSchema    string
	Build           int
	LatestBuild     int
	InstalledName   string
	InstalledID     string
	InstalledBy     string
	InstalledByName string
	InstalledAt     time.Time
	// Digest fingerprints the content that was installed (empty on old records); the frontend uses it to say "this is the
	// same content as last time"
	Digest string
}

// RecordInstall writes one install record.
//
// digest fingerprints "the content that was installed" (see assetpack.StampManifest): the next time an entity of the same
// name is installed, it is what tells "is this the same content". Pass an empty string when an uploaded pack has no
// asset_id.
func RecordInstall(assetID, schema string, build int, installedName, installedID, by, byName, digest string) error {
	_, err := db.DB.Exec(`INSERT INTO `+table("shared_asset_installs")+`
		(asset_id, schema_name, build, installed_name, installed_id, installed_by, installed_by_name, digest)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8)`,
		assetID, schema, build, installedName, installedID, by, byName, digest)
	return err
}

// LatestInstallDigest returns the content fingerprint in an entity's most recent install record (empty when there is no
// record or it is an old one).
//
// It is only used to "inform", never to "block": the entity may have been changed locally after the install, so the
// fingerprint only says "the pack installed this time is the same as last time", not that the entity's current content
// matches the pack.
func LatestInstallDigest(schema, installedID string) (string, error) {
	var d string
	err := db.DB.QueryRow(`SELECT COALESCE(digest, '') FROM `+table("shared_asset_installs")+`
		 WHERE schema_name = $1 AND installed_id = $2
		 ORDER BY installed_at DESC LIMIT 1`, schema, installedID).Scan(&d)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return d, nil
}

// InstallsBy lists a tenant's install records (with the asset name, kind and latest snapshot, so "a new snapshot is
// available" can be reported).
func InstallsBy(schema string) ([]Install, error) {
	rows, err := db.DB.Query(`
		SELECT i.asset_id, a.kind, a.name, a.source_schema, i.build, a.build,
		       i.installed_name, i.installed_id, COALESCE(i.installed_by_name, ''), i.installed_at,
		       COALESCE(i.digest, '')
		  FROM `+table("shared_asset_installs")+` i
		  JOIN `+table("shared_assets")+` a ON a.id = i.asset_id
		 WHERE i.schema_name = $1
		 ORDER BY i.installed_at DESC`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Install, 0, 8)
	for rows.Next() {
		var i Install
		if err := rows.Scan(&i.AssetID, &i.Kind, &i.AssetName, &i.SourceSchema,
			&i.Build, &i.LatestBuild, &i.InstalledName, &i.InstalledID,
			&i.InstalledByName, &i.InstalledAt, &i.Digest); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteMineUnlisted deletes every locally listed row of a tenant that has not passed review yet, returning the deleted
// ids.
//
// Only "locally original (origin_server empty) and not approved" rows are deleted:
//   - an approved asset may be in use, and deleting it would leave install records dangling; only the platform may
//     delist or delete it;
//   - an imported row is not "my listing", and deleting it is the remote marketplace's business.
//
// One DELETE ... RETURNING rather than "query then delete one by one": between the list and the delete somebody may have
// submitted another one, and a single statement guarantees "exactly this batch was deleted".
func DeleteMineUnlisted(schema string) ([]string, error) {
	rows, err := db.DB.Query(`DELETE FROM `+table("shared_assets")+`
		WHERE source_schema = $1 AND origin_server = '' AND status <> 'listed'
		RETURNING id`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteInstall deletes one install record, reporting whether a row was actually deleted.
//
// It deletes the record only and **not** the installed entity: the record is the ledger of "what I installed", while
// uninstalling is a different matter. schema comes from the caller's identity (never from the request body), otherwise
// another tenant's records could be deleted.
func DeleteInstall(assetID, schema, installedID string) (bool, error) {
	res, err := db.DB.Exec(`DELETE FROM `+table("shared_asset_installs")+`
		WHERE asset_id = $1 AND schema_name = $2 AND installed_id = $3`,
		assetID, schema, installedID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClearInstalls deletes every install record of a tenant, returning how many were deleted.
//
// The same rule as DeleteInstall: only the ledger is erased, the entities are not uninstalled. One DELETE instead of a
// loop, so "clear" either fully happens or does not happen at all — failing halfway leaves records that are even harder
// to explain.
func ClearInstalls(schema string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+table("shared_asset_installs")+` WHERE schema_name = $1`, schema)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountInstalls counts how many tenants have installed an asset (the review page asks "is anyone really using this").
func CountInstalls(assetID string) (int, error) {
	var n int
	err := db.DB.QueryRow(`SELECT count(DISTINCT schema_name) FROM `+table("shared_asset_installs")+
		` WHERE asset_id = $1`, assetID).Scan(&n)
	return n, err
}

// InstalledBuilds maps assetID -> highest snapshot installed by this tenant (the list marks "installed / a new snapshot
// is available").
func InstalledBuilds(schema string) map[string]int {
	out := map[string]int{}
	rows, err := db.DB.Query(`SELECT asset_id, max(build) FROM `+table("shared_asset_installs")+
		` WHERE schema_name = $1 GROUP BY asset_id`, schema)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  string
			ver int
		)
		if err := rows.Scan(&id, &ver); err == nil {
			out[id] = ver
		}
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }
