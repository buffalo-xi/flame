package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"

	"flame/nexus/platform/auth"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// Platform side of cross-tenant asset distribution: review / visibility.
//
// It only governs, never transfers: snapshots are written by the tenant-side endpoint (admin/share)
// and installation also happens on the tenant side; what changes here is the state machine and the
// allow-list, so the platform side never needs to read a tenant schema.
//
// State machine (see docs/asset-sharing.md section 6):
//
//	draft -> pending -> listed | rejected
//	listed -> delisted
//
// P0 does not paginate: assets number in the hundreds, so getting "review actually works" done comes
// first; when paging is needed, add Limit/Offset to sharedassets.Filter (already supported) and a
// Count.

// ListAssets GET /platform/assets?status=&kind=&q=&source=&origin=
//
// By default the list is ordered "pending first, then newest update": without a status filter,
// pending rows come first so an administrator does not have to page through dozens of them.
//
// origin=local shows only rows listed on this server; any other value filters by peer site name. With
// several remote markets attached, "which machine did this row come from" must be visible at a
// glance.
func ListAssets(c *gin.Context) {
	originServer, originLocal := originFilter(c.Query("origin"))
	f := sharedassets.Filter{
		Status:       strings.TrimSpace(c.Query("status")),
		Kind:         strings.TrimSpace(c.Query("kind")),
		SourceSchema: strings.TrimSpace(c.Query("source")),
		Query:        strings.TrimSpace(c.Query("q")),
		OriginServer: originServer,
		OriginLocal:  originLocal,
	}
	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load assets: "+err.Error(), "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, platformAssetView(&list[i], false))
	}
	// Pending first (stable sort: updated_at descending is preserved inside each group)
	if strings.TrimSpace(c.Query("status")) == "" {
		pending := make([]gin.H, 0, len(items))
		rest := make([]gin.H, 0, len(items))
		for _, it := range items {
			if it["status"] == sharedassets.StatusPending {
				pending = append(pending, it)
				continue
			}
			rest = append(rest, it)
		}
		items = append(pending, rest...)
	}
	// Data for the origin dropdown: the origin filter is ignored, every other filter still applies
	counts, cerr := sharedassets.CountByOrigin(f)
	if cerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to count origins: "+cerr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":   items,
		"total":   len(items),
		"origins": originOptions(counts),
	}, "")
}

// originFilter translates ?origin= into filter conditions (empty = all; local = this server; anything
// else = that peer site).
func originFilter(raw string) (originServer string, originLocal bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	if strings.EqualFold(v, "local") {
		return "", true
	}
	return v, false
}

// originOptions builds the origin dropdown: this server first, the rest sorted by name.
//
// label is language-neutral on purpose: it is the site name, and an empty string means "listed on this
// server", which the frontend renders from its locale bundle (the backend never emits UI copy).
func originOptions(counts map[string]int) []gin.H {
	out := make([]gin.H, 0, len(counts))
	for server, n := range counts {
		out = append(out, gin.H{"server": server, "label": server, "count": n})
	}
	sort.Slice(out, func(i, j int) bool {
		si, _ := out[i]["server"].(string)
		sj, _ := out[j]["server"].(string)
		if (si == "") != (sj == "") {
			return si == "" // this server always comes first
		}
		return si < sj
	})
	return out
}

// GetAsset GET /platform/assets/:id
//
// The detail view for review: dependency declaration, file manifest, lister, install count across
// tenants, previous review note.
func GetAsset(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}

	v := platformAssetView(a, true)
	installs, cerr := sharedassets.CountInstalls(a.ID)
	if cerr != nil {
		installs = 0 // a failed count must not make the detail page unopenable
	}
	v["install_count"] = installs

	if a.Visibility == sharedassets.VisibilityAllowlist {
		if schemas, serr := sharedassets.VisibleSchemas(a.ID); serr == nil {
			v["allowlist"] = schemas
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"asset": v}, "")
}

type reviewRequest struct {
	Action string `json:"action"` // approve | reject | delist
	Note   string `json:"note"`
}

// ReviewAsset POST /platform/assets/:id/review
//
// State transitions are settled here: approve/reject only apply to pending, delist only to listed.
// There is no "any state to any state" - that would let a rejected asset slip into the market or an
// approved one be re-approved after delisting, and the review record would lose its meaning.
func ReviewAsset(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	var req reviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}

	var next string
	switch strings.TrimSpace(req.Action) {
	case "approve":
		if a.Status != sharedassets.StatusPending {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"only a pending asset can be approved (current: "+a.Status+")", "")
			return
		}
		next = sharedassets.StatusListed
	case "reject":
		if a.Status != sharedassets.StatusPending {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"only a pending asset can be rejected (current: "+a.Status+")", "")
			return
		}
		next = sharedassets.StatusRejected
	case "delist":
		if a.Status != sharedassets.StatusListed {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"only a listed asset can be delisted (current: "+a.Status+")", "")
			return
		}
		next = sharedassets.StatusDelisted
	default:
		xhttp.ErrCode(c, "asset.invalid_action", gin.H{"action": strings.TrimSpace(req.Action)},
			"unknown action (only approve / reject / delist are supported)", "")
		return
	}

	if err := sharedassets.Review(a.ID, next, strings.TrimSpace(req.Note), info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "review: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": a.ID, "status": next}, "")
}

type visibilityRequest struct {
	Visibility string   `json:"visibility"` // all | allowlist
	Schemas    []string `json:"schemas"`
}

// GetAssetVisibility GET /platform/assets/:id/visibility
func GetAssetVisibility(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}
	schemas, serr := sharedassets.VisibleSchemas(a.ID)
	if serr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load allowlist: "+serr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"visibility": a.Visibility,
		"schemas":    schemas,
	}, "")
}

// SetAssetVisibility PUT /platform/assets/:id/visibility
//
// The allow-list is replaced wholesale every time (the frontend is "tick boxes -> save"), with no
// per-entry add/remove: a half-baked incremental endpoint is the easiest way to end up with an
// allow-list that "looks deleted but is not".
func SetAssetVisibility(c *gin.Context) {
	var req visibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	vis := strings.TrimSpace(req.Visibility)
	if vis != sharedassets.VisibilityAll && vis != sharedassets.VisibilityAllowlist {
		xhttp.ErrCode(c, "asset.invalid_visibility", gin.H{"visibility": vis},
			"visibility must be all or allowlist", "")
		return
	}

	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}

	// Every tenant in the allow-list must really exist: writing a misspelled schema in means "it looks
	// open, but nobody can install it"
	for _, s := range req.Schemas {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, terr := tenantsrepo.Get(s)
		if terr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "check tenant: "+terr.Error(), "")
			return
		}
		if t == nil {
			xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": s}, "tenant not found: "+s, "")
			return
		}
	}

	if err := sharedassets.SetVisibility(a.ID, vis, req.Schemas); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "set visibility: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"visibility": vis, "schemas": req.Schemas}, "")
}

// platformAssetView is the platform-side asset view (same shape as the tenant side plus review data).
func platformAssetView(a *sharedassets.Asset, withManifest bool) gin.H {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		_ = json.Unmarshal(a.Manifest, &m)
	}
	by := ""
	if a.ListedBy.Valid {
		by = a.ListedBy.String
	}
	// An imported row's source_schema is "the tenant on the other machine", and a same-named schema
	// here is a coincidence; looking it up in the local tenants table would produce a misleading name,
	// so such rows are labelled with the origin site instead.
	tenantName := sharedassets.TenantDisplayName(a.SourceSchema)
	if a.Imported() {
		tenantName = a.OriginServer
	}

	v := gin.H{
		"id":           a.ID,
		"kind":         a.Kind,
		"name":         a.Name,
		"display_name": a.DisplayName,
		"description":  a.Description,
		"build":        a.Build,
		"status":       a.Status,
		"visibility":   a.Visibility,
		"origin":       originView(a),
		"listed_by": gin.H{
			"tenant": gin.H{
				"schema":       a.SourceSchema,
				"display_name": tenantName,
			},
			"user": gin.H{"id": by, "display_name": a.ListedByName},
		},
		"entry":       m.Entry,
		"deps":        m.Deps,
		"file_count":  len(m.Files),
		"total_size":  m.TotalSize(),
		"review_note": a.ReviewNote,
		"created_at":  a.CreatedAt,
		"updated_at":  a.UpdatedAt,
	}
	if withManifest {
		v["manifest"] = m
	}
	return v
}

// DeleteAsset DELETE /platform/assets/:id
//
// Deleting does not require delisting first: delisting is "withdraw from the market but keep the
// record", deleting is "remove entirely"; they are parallel tools, not consecutive steps - deleting a
// listed asset also leaves copies already installed in tenants alone (copy semantics).
//
// Three states the tenant side has not finished are still blocked:
//   - pending / rejected are unfinished tenant-side flows, and deciding for the tenant is not
//     appropriate (the way to handle them is to reject);
//   - draft is cleaned up by the tenant side's own "clear unlisted" action.
//
// The row goes first, then the snapshot directory: the row is the entry point, and a directory that
// cannot be removed only leaves garbage (logged, no rollback).
// The allow-list and install records cascade with the foreign keys; entities already installed in
// tenants are untouched - they belong to the tenant.
func DeleteAsset(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}
	switch a.Status {
	case sharedassets.StatusListed, sharedassets.StatusDelisted:
	default:
		xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
			"only a listed or delisted asset can be deleted (current status: "+a.Status+")", "")
		return
	}

	if derr := sharedassets.Delete(a.ID); derr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "delete failed: "+derr.Error(), "")
		return
	}

	if store, serr := tenantStorage(); serr != nil {
		xlog.Error("[platform] asset %s deleted, but storage is unavailable so the snapshot was not cleaned: %v", a.ID, serr)
	} else if perr := store.DeletePrefix(context.Background(), path.Join(paths.PlatformAssetsSubdir, a.ID)); perr != nil {
		xlog.Error("[platform] asset %s deleted, but cleaning the snapshot directory failed: %v", a.ID, perr)
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": a.ID}, "")
}
