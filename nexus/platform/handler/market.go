package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"flame/nexus/platform/auth"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// The peer market (server-to-server, read-only). Mounted under /platform/market and authenticated by a market token
// (market_tokens), not by a platform login: a peer is just a machine and should not hold a credential that can change
// configuration.
//
// Two egress rules:
//  1. **Only listed assets go out**: pending / rejected / delisted never appear, so "reviewed here" is the only
//     precondition for peer visibility; the allow-list is a tenant-level dimension the peer is not subject to (after
//     pulling it back, the peer reviews it on its own platform again and installs it within its own tenants' visible
//     scope).
//  2. **Only local originals go out** (origin_server = ''): imported rows are not forwarded again.
//     Otherwise A->B and B->A form a loop: each import creates a new row (the origin changed but it is still listed),
//     which shows up in the egress catalog again, and the same thing is copied over and over; provenance could also be
//     faked by renaming (a forwarded item from site B listed under site A's name), which breaks the origin_server
//     provenance chain outright.
//     A peer that wants such an asset should pull it from the original site.

// ListMarketCatalog GET /platform/market/catalog?kind=&since=
//
// With an RFC3339 since it returns only rows updated after that time, for incremental sync; without it the list is
// complete (assets number in the hundreds).
func ListMarketCatalog(c *gin.Context) {
	f := sharedassets.Filter{
		Status:      sharedassets.StatusListed,
		Kind:        strings.TrimSpace(c.Query("kind")),
		OriginLocal: true, // only rows listed on this server, see egress rule 2 above
	}

	var since time.Time
	if s := strings.TrimSpace(c.Query("since")); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			xhttp.ErrCode(c, "common.invalid_params", gin.H{"param": "since"},
				"since requires an RFC3339 timestamp (for example 2026-11-02T10:00:00Z)", "")
			return
		}
		since = t
	}

	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to query the market catalog: "+err.Error(), "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		a := &list[i]
		if !since.IsZero() && !a.UpdatedAt.After(since) {
			continue
		}
		var m assetpack.Manifest
		if len(a.Manifest) > 0 {
			_ = json.Unmarshal(a.Manifest, &m)
		}
		items = append(items, gin.H{
			"id":           a.ID,
			"kind":         a.Kind,
			"name":         a.Name,
			"display_name": a.DisplayName,
			"description":  a.Description,
			"build":        a.Build,
			"entry":        m.Entry,
			"deps":         m.Deps,
			"file_count":   len(m.Files),
			"total_size":   m.TotalSize(),
			"updated_at":   a.UpdatedAt,
		})
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"server": strings.TrimSpace(config.Cfg.Server.Name),
		"token":  auth.MarketTokenName(c),
		"items":  items,
		"total":  len(items),
	}, "")
}

// MarketAssetPackage GET /platform/market/assets/:id/package
//
// Only a reviewed **local-original** asset can be pulled: an id that is not in the catalog gets no package here either,
// so the two doors agree.
// An imported row (origin_server set) also gets 404 - it cannot appear in the catalog and therefore must not be
// reachable by id, or the "local originals only" restriction could be bypassed with a guessed id.
func MarketAssetPackage(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to query the asset: "+err.Error(), "")
		return
	}
	if a == nil || a.Status != sharedassets.StatusListed || a.OriginServer != "" {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "the asset does not exist or has not been reviewed")
		return
	}
	data, berr := buildAssetPackage(a)
	if berr != nil {
		xhttp.ErrCode(c, "common.internal", nil, berr.Error(), "")
		return
	}
	servePackage(c, a, data)
}
