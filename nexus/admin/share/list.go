package share

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Marketplace pagination: at most 200 items at a time, 50 by default.
//
// It uses "load more" rather than page numbers: the order is updated_at DESC, users have no expectation of
// "what page 3 is", and page numbers only turn "cannot find it" into "keeps paging". The remote market is
// not paginated (a peer catalog is cached whole, so paging saves no transfer); there a cap plus search narrows it down.
const (
	marketPageDefault = 50
	marketPageMax     = 200
)

// queryInt reads integer parameters such as ?limit / ?offset: the default when absent, the default when invalid, and clamped to the range when out of bounds.
func queryInt(c *gin.Context, key string, def, lo, hi int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// ListAssets is the installable list: only assets that passed review and are visible to this tenant.
//
// Visibility lives in VisibleTo of sharedassets.List (an EXISTS subquery): in allowlist mode
// "no allowlist row" must mean "not visible", and keeping that in SQL stops it leaking when a caller forgets to check.
func ListAssets(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	limit := queryInt(c, "limit", marketPageDefault, 1, marketPageMax)
	offset := queryInt(c, "offset", 0, 0, 1<<20)
	originServer, originLocal := originFilter(c.Query("origin"))
	f := sharedassets.Filter{
		VisibleTo:    info.Schema,
		Kind:         strings.TrimSpace(c.Query("kind")),
		Query:        strings.TrimSpace(c.Query("q")),
		OriginServer: originServer,
		OriginLocal:  originLocal,
	}
	// Count applies no LIMIT, so it returns the full filtered set and the front end can tell whether another page exists
	total, err := sharedassets.Count(f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("count assets", err), "")
		return
	}
	f.Limit, f.Offset = limit, offset
	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query assets", err), "")
		return
	}

	installed := sharedassets.InstalledBuilds(info.Schema)
	items := make([]gin.H, 0, len(list))
	for i := range list {
		_, has := installed[list[i].ID]
		v := assetView(&list[i], has)
		if ver, ok := installed[list[i].ID]; ok && list[i].Build > ver {
			v["upgradable"] = true
		}
		items = append(items, v)
	}

	// Data for the origin dropdown: the origin condition is ignored (the rest apply), so filtering one origin does not make the other options disappear
	counts, cerr := sharedassets.CountByOrigin(f)
	if cerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("count origins", cerr), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":    items,
		"total":    total, // total after filtering, not the number of items on this page
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
		"origins":  originOptions(counts),
	}, "")
}

// originFilter turns ?origin= into filter conditions:
//
//	empty = every origin; local = only what was listed here; anything else = only what that peer site imported.
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

// originOptions builds the origin dropdown: this machine first, the rest by name (counts come from CountByOrigin).
func originOptions(counts map[string]int) []gin.H {
	out := make([]gin.H, 0, len(counts))
	for server, n := range counts {
		label := server
		if server == "" {
			label = "listed here"
		}
		out = append(out, gin.H{"server": server, "label": label, "count": n})
	}
	sort.Slice(out, func(i, j int) bool {
		si, _ := out[i]["server"].(string)
		sj, _ := out[j]["server"].(string)
		if (si == "") != (sj == "") {
			return si == "" // this machine always comes first
		}
		return si < sj
	})
	return out
}

// errAssetNotFound reports a missing asset.
//
// The error code is for the front end to translate; params.id only locates the asset and never enters a template.
// msg is the plain fallback text - logs, curl and callers that have not upgraded still read it.
func errAssetNotFound(c *gin.Context, id string) {
	xhttp.ErrCode(c, "asset.not_found", map[string]any{"id": id}, "asset not found", "")
}

// GetAsset returns asset detail (within the installable scope), with the file list and dependencies for a pre-install check.
func GetAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query asset", err), "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	visible, verr := sharedassets.VisibleTo(a, info.Schema)
	if verr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "check visibility: "+verr.Error(), "")
		return
	}
	// Your own asset (even a draft) is visible; someone else's must be listed and visible.
	// "Own" = created here plus this tenant's schema: an imported row's source_schema can also match this machine's name (the other site is t0 too),
	// so the schema alone does not make it yours - otherwise a draft imported row would be visible to this tenant.
	mine := a.SourceSchema == info.Schema && a.OriginServer == ""
	if !mine && (!visible || a.Status != sharedassets.StatusListed) {
		xhttp.ErrCode(c, "asset.not_available", map[string]any{"id": a.ID},
			"this asset is not open to your tenant", "")
		return
	}

	_, has := sharedassets.InstalledBuilds(info.Schema)[a.ID]
	view := assetView(a, has)

	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		_ = json.Unmarshal(a.Manifest, &m)
	}
	view["manifest"] = m
	view["review_note"] = a.ReviewNote
	xhttp.OkJSON(c, "Success", gin.H{"asset": view}, "")
}

// ListMine lists what this tenant has listed (drafts / pending / rejected included), with rejection notes.
//
// OriginLocal is required: an imported row's source_schema records **the tenant on the other machine**,
// so when it matches a local schema name (both are t0) assets imported from another site would be listed as "mine",
// showing up as "something I listed, yet the origin says site aaa".
func ListMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, err := sharedassets.List(sharedassets.Filter{SourceSchema: info.Schema, OriginLocal: true})
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query assets", err), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		v := assetView(&list[i], false)
		v["review_note"] = list[i].ReviewNote
		items = append(items, v)
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

// Withdraw takes a listing back: pending -> draft (resubmit once edited); draft -> deleted outright (snapshot directory included).
//
// A listed asset cannot be withdrawn by its author - installed entities no longer depend on the snapshot, so withdrawing
// would only orphan the "who installed it" records; to take it off the market use delist on the platform side.
func Withdraw(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query asset", err), "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	// An imported row is not "mine": its source_schema is just the tenant name on the other machine, and a local match is a coincidence
	if a.SourceSchema != info.Schema || a.OriginServer != "" {
		xhttp.ErrCode(c, "asset.not_owner", map[string]any{"id": a.ID}, "only assets listed by this tenant can be withdrawn", "")
		return
	}

	switch a.Status {
	case sharedassets.StatusPending:
		if err := sharedassets.SetStatus(a.ID, sharedassets.StatusDraft); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("withdraw", err), "")
			return
		}
		xhttp.OkJSON(c, "Success", gin.H{"status": sharedassets.StatusDraft}, "")
	case sharedassets.StatusDraft:
		if err := sharedassets.Delete(a.ID); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("delete asset", err), "")
			return
		}
		// Delete the row first, then the files: the other way round leaves a row with no content, and the review page shows an empty asset
		if st, serr := getStorage(); serr == nil {
			_ = st.DeletePrefix(context.Background(), paths.PlatformAssetsSubdir+"/"+a.ID)
		}
		xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
	default:
		xhttp.ErrCode(c, "asset.invalid_status", map[string]any{"id": a.ID, "status": a.Status},
			"the current status ("+a.Status+") cannot be withdrawn: a listed asset is delisted by the platform", "")
	}
}

// DeleteMine DELETE /admin/share/mine/:id - deletes one of your own listings (row + snapshot directory).
//
// The difference from Withdraw: Withdraw moves the state (pending -> draft, draft -> deleted),
// while this clears it directly, so drafts / pending / rejected / delisted can all go in one step.
// listed is always refused: it may already be installed elsewhere, and deleting it would orphan those install records; delist it on the platform first.
func DeleteMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query asset", err), "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	if a.SourceSchema != info.Schema || a.OriginServer != "" {
		xhttp.ErrCode(c, "asset.not_owner", map[string]any{"id": a.ID}, "only assets listed by this tenant can be deleted", "")
		return
	}
	if a.Status == sharedassets.StatusListed {
		xhttp.ErrCode(c, "asset.invalid_status", map[string]any{"id": a.ID, "status": a.Status},
			"a listed asset cannot be deleted directly: another tenant may already have installed it; "+
				"delist it on the platform first, then come back and delete", "")
		return
	}
	if err := sharedassets.Delete(a.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("delete asset", err), "")
		return
	}
	// Delete the row first, then the files: the other way round leaves a row with no content, and the review page shows an empty asset
	if st, serr := getStorage(); serr == nil {
		_ = st.DeletePrefix(context.Background(), paths.PlatformAssetsSubdir+"/"+a.ID)
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
}

// ClearMine DELETE /admin/share/mine - clears every unlisted listing of this tenant.
//
// listed is kept: other tenants may have installed those assets, and deleting them would orphan their records.
// It returns how many were deleted, so the UI can say "cleared N (N listed ones kept)".
func ClearMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	ids, err := sharedassets.DeleteMineUnlisted(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("clear mine", err), "")
		return
	}
	// Clear the snapshot directories after the rows (same order as DeleteMine)
	if st, serr := getStorage(); serr == nil {
		ctx := context.Background()
		for _, id := range ids {
			_ = st.DeletePrefix(ctx, paths.PlatformAssetsSubdir+"/"+id)
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": len(ids)}, "")
}

// ListInstalls lists this tenant's install records (who, from which snapshot, under what name).
func ListInstalls(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, err := sharedassets.InstallsBy(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query installs", err), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, i := range list {
		items = append(items, gin.H{
			"asset_id":          i.AssetID,
			"kind":              i.Kind,
			"asset_name":        i.AssetName,
			"installed_name":    i.InstalledName,
			"installed_id":      i.InstalledID,
			"build":             i.Build,
			"latest_build":      i.LatestBuild,
			"upgradable":        i.LatestBuild > i.Build,
			"source_schema":     i.SourceSchema,
			"installed_by":      i.InstalledBy,
			"installed_by_name": i.InstalledByName,
			"installed_at":      i.InstalledAt,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}
