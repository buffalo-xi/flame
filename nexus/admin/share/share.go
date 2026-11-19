// Package share implements tenant-side cross-tenant distribution: listing / install / my assets.
//
// Terminology (see nexus/docs/terminology.md): the actions are list / delist, the states listed / delisted,
// the route POST /admin/share/listings and the tables platform.listing_*. UI copy and code use the same word.
//
// Why it lives in admin rather than a new process: the whole repository shares one db.DB pool and one agent.root_dir,
// and a tenant-side API already reads the tenant schema and definition-domain files, so writing the platform schema (explicitly qualified) is enough.
// The platform side keeps only review and visibility scope; see platform/handler/assets.go.
//
// Three hard rules (details in docs/asset-sharing.md):
//  1. copy, never reference: an install becomes the target tenant's own entity and has nothing to do with the snapshot afterwards;
//  2. the snapshot is the only source of truth: an install copies from platform-assets/<id>/<ver>/ and never reads back from the source tenant;
//  3. a clash is never silent: a duplicate name returns 409 plus a suggested name, and the user retries with name= after confirming.
package share

import (
	"encoding/json"
	"errors"
	"sync"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/repository/agentrelations"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

var (
	skillRepo = skills.NewRepository()
	agentRepo = agents.NewRepository()
	flowRepo  = flows.NewRepository()
	grantRepo = toolgrants.NewRepository()
	// relationRepo is read-only "who may call whom": listing records the names of call dependencies in the manifest,
	// so the pre-import check can ask whether the target tenant has that agent (the grant rows themselves do not travel).
	relationRepo = agentrelations.NewRepository()

	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// getStorage lazily builds the storage instance (the same configuration as admin/skills and service/agent).
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return store, storeErr
}

// tenantTable builds a tenant table name: schema comes from authInfo (resolved at login), never from the request body.
func tenantTable(schema, name string) string {
	return pq.QuoteIdentifier(schema) + "." + name
}

// assetErrMsg turns "the table is missing / the column is missing" into an actionable sentence.
//
// The cross-tenant distribution tables are not part of automatic migration (DB scripts are always run by hand; see platform/README.md),
// so returning the raw pq text leaves a first-time deployer digging through the docs:
//
//	42P01 = undefined_table    table missing -> the create script has not run
//	42703 = undefined_column   column missing -> the migration ran halfway and the schema lags the code
func assetErrMsg(prefix string, err error) string {
	if err == nil {
		return prefix
	}
	var pe *pq.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42P01":
			return prefix + ": " + err.Error() + " (platform tables missing; run nexus/platform/sql/shared_assets.sql)"
		case "42703":
			return prefix + ": " + err.Error() + " (the schema lags the code; run the migration scripts under nexus/platform/sql/, such as asset_pack_digest.sql)"
		}
	}
	return prefix + ": " + err.Error()
}

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrCode(c, "common.internal", nil, "Invalid authentication information", "")
		return nil, false
	}
	return info, true
}

// listedByView is the lister view: tenant and person are two {id|schema, display_name} pairs.
//
// JSON uses schema (the same as tenantView) while the SQL column is schema_name;
// the person's display_name is the snapshot taken at listing time (users is not re-queried, so history stays readable after a rename or disable).
func listedByView(a *sharedassets.Asset) gin.H {
	by := ""
	if a.ListedBy.Valid {
		by = a.ListedBy.String
	}
	// An imported row's source_schema is "the tenant on the other machine"; a same-named schema here is a coincidence,
	// and looking it up in the local tenants table would show a misleading name - such rows show the origin site instead.
	tenantName := sharedassets.TenantDisplayName(a.SourceSchema)
	if a.Imported() {
		tenantName = a.OriginServer
	}
	return gin.H{
		"tenant": gin.H{
			"schema":       a.SourceSchema,
			"display_name": tenantName,
		},
		"user":   gin.H{"id": by, "display_name": a.ListedByName},
		"origin": importedOriginView(a),
	}
}

// importedOriginView shows import provenance (nil for rows listed on this machine).
func importedOriginView(a *sharedassets.Asset) gin.H {
	if !a.Imported() {
		return nil
	}
	v := gin.H{"server": a.OriginServer}
	if a.OriginAssetID.Valid {
		v["asset_id"] = a.OriginAssetID.String
	}
	if a.ImportedAt.Valid {
		v["imported_at"] = a.ImportedAt.Time
	}
	return v
}

// assetView is the shared list / detail view. The manifest is expanded in the detail only (the list shows counts and sizes).
func assetView(a *sharedassets.Asset, installed bool) gin.H {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		_ = json.Unmarshal(a.Manifest, &m)
	}
	return gin.H{
		"id":           a.ID,
		"kind":         a.Kind,
		"name":         a.Name,
		"display_name": a.DisplayName,
		"description":  a.Description,
		"build":        a.Build,
		"status":       a.Status,
		"visibility":   a.Visibility,
		"listed_by":    listedByView(a),
		"entry":        m.Entry,
		"deps":         m.Deps,
		"file_count":   len(m.Files),
		"total_size":   m.TotalSize(),
		"installed":    installed,
		"created_at":   a.CreatedAt,
		"updated_at":   a.UpdatedAt,
	}
}
