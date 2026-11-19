package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/assetimport"
	"flame/nexus/shared/repository/peers"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// The tenant-side "remote market": the platform configures peers and visibility, and a tenant sees a peer's catalog inside its own asset market,
// pulling the bundle only when the user clicks install (pull-through).
//
// Three hard constraints:
//  1. the token stays on the server: a tenant never sees `flmt_…` and the browser never talks to the peer directly (that would hand out a site-wide read-only token);
//  2. only sources the platform configured are reachable: requests carry a peer name, not a URL, so there is no SSRF surface;
//  3. what is pulled passes the same review gate: with install_policy=review the import lands as pending and the tenant can install only after approval.
//
// The catalog is cached for 60s: N tenants browsing the market must not become N peer requests; an unreachable peer yields its own error,
// leaving local assets and other peers alone (a dead peer must not drag down the local market).

const (
	remoteCatalogTTL = 60 * time.Second
	remoteCatalogMax = 4 << 20
	// remotePackageMax caps a peer bundle: the same limit as the local import / upload (a 128 MiB archive).
	remotePackageMax  = 128 << 20
	remoteHTTPTimeout = 20 * time.Second

	// remoteCatalogItemLimit caps how many items one peer returns at a time (the total is counted after filtering and before truncation).
	// The remote market is not paginated: a peer catalog is cached whole, so paging saves no transfer and only turns "cannot find it" into "page through it".
	// A cap plus search is the right way to narrow this page. 500 items is roughly 1-2 MB of JSON, still inside the 4 MiB catalog budget.
	remoteCatalogItemLimit = 500
)

var remoteClient = &http.Client{Timeout: remoteHTTPTimeout}

// catalogItem is one entry of a peer catalog (fields aligned with /platform/market/catalog).
type catalogItem struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	Build       int             `json:"build"`
	Entry       string          `json:"entry"`
	Deps        json.RawMessage `json:"deps"`
	FileCount   int             `json:"file_count"`
	TotalSize   int64           `json:"total_size"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type catalogCacheEntry struct {
	mu     sync.Mutex // one lock per peer: a slow peer only blocks itself
	server string
	items  []catalogItem
	err    string
	at     time.Time
	loaded bool
}

var remoteCatalogCache = struct {
	mu   sync.Mutex
	data map[string]*catalogCacheEntry // key = peer id (the whole catalog is fetched once; the caller filters by kind)
}{data: make(map[string]*catalogCacheEntry, 4)}

// peerCatalog returns a peer catalog (cached). Failures are cached too: an unreachable peer costs one connection for N tenant refreshes.
//
// Locking is per peer: HTTP is **never** issued under the global lock - a dead peer with a 20s timeout would otherwise
// stall every tenant's remote market. For the same peer only the first concurrent caller fetches; the rest wait for its result.
func peerCatalog(p *peers.Peer) *catalogCacheEntry {
	remoteCatalogCache.mu.Lock()
	e := remoteCatalogCache.data[p.ID]
	if e == nil {
		e = &catalogCacheEntry{}
		remoteCatalogCache.data[p.ID] = e
	}
	remoteCatalogCache.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loaded && time.Since(e.at) < remoteCatalogTTL {
		return &catalogCacheEntry{server: e.server, items: e.items, err: e.err, at: e.at, loaded: true}
	}
	fresh := fetchPeerCatalog(p)
	e.server, e.items, e.err, e.at, e.loaded = fresh.server, fresh.items, fresh.err, time.Now(), true
	return &catalogCacheEntry{server: e.server, items: e.items, err: e.err, at: e.at, loaded: true}
}

func fetchPeerCatalog(p *peers.Peer) *catalogCacheEntry {
	e := &catalogCacheEntry{}
	req, err := http.NewRequest(http.MethodGet,
		strings.TrimRight(p.BaseURL, "/")+"/platform/market/catalog", nil)
	if err != nil {
		e.err = "invalid peer address: " + err.Error()
		return e
	}
	// When the peer market is public (peer.auth_mode=none) no Authorization is sent:
	// knocking on a peer that opened market.open with an expired token only earns a 401.
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		e.err = "failed to reach the peer: " + err.Error()
		return e
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one extra byte: hitting the limit means the response was truncated, so report "catalog too large"
	// instead of letting a half JSON body decode as "bad format" below and misdirect the investigation
	body, _ := io.ReadAll(io.LimitReader(resp.Body, remoteCatalogMax+1))
	if resp.StatusCode != http.StatusOK {
		e.err = fmt.Sprintf("peer returned %d: %s", resp.StatusCode, truncate(string(body), 200))
		return e
	}
	if len(body) > remoteCatalogMax {
		e.err = fmt.Sprintf(
			"the peer catalog exceeds the %d MiB limit and the response was truncated (this is not a format problem); list fewer assets on the peer or split it into several sites",
			remoteCatalogMax>>20,
		)
		return e
	}
	var payload struct {
		Data struct {
			Server string        `json:"server"`
			Items  []catalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		e.err = "the peer response is not valid JSON (an outdated snapshot, or a wrong address?)"
		return e
	}
	// The name the peer reports (the site name is its identity) is used for display and diagnosis only, never to gate configuration.
	// "A peer pointing at this machine" is currently **allowed** (the easiest single-box self test): it does not self-multiply, because the
	// outbound catalog carries only local originals (see rule 2 in platform/handler/market.go) - the row pulled from itself has a non-empty
	// origin, never appears in the outbound catalog, and one round settles it. If forwarding imported rows is ever allowed, a guard must come back here.
	e.server = payload.Data.Server
	e.items = payload.Data.Items
	return e
}

// invalidatePeerCatalog drops a peer's cache (after a pull, or when configuration changed and must apply at once rather than after 60s).
func invalidatePeerCatalog(peerID string) {
	remoteCatalogCache.mu.Lock()
	delete(remoteCatalogCache.data, peerID)
	remoteCatalogCache.mu.Unlock()
}

// displayPeerURL is the display form of a peer address: scheme://host[:port][/path] kept,
// userinfo / query / fragment stripped and the trailing slash removed.
//
// Why strip: base_url may embed credentials (https://u:p@host) or carry a query string,
// and neither belongs to "where the market is" - showing them only adds another leak surface.
// It returns the input unchanged when parsing fails: this is a display field, and dirty data must not turn the whole page into an error.
func displayPeerURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// RemoteAssets GET /admin/share/remote-assets?peer=&kind=&q=
//
// It returns only peers **open to this tenant** (platform.peer_servers.visible_to + enabled).
// Each peer reports its own error, so "the test site shows N items, the staging one is temporarily unreachable" can coexist.
//
// No pagination: a peer catalog is fetched whole and cached (see remoteCatalogTTL), so paging saves rendering only,
// not transfer, and turns "finding something" into "paging through it". So a cap plus search narrows it down,
// and it **filters before truncating** - otherwise a match for "essay" sitting at item 300 could never be found.
func RemoteAssets(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	onlyPeer := strings.TrimSpace(c.Query("peer"))
	kind := strings.TrimSpace(c.Query("kind"))
	keyword := strings.TrimSpace(c.Query("q"))

	list, err := peers.ListPeers()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list remote markets: "+err.Error(), "")
		return
	}

	installed := sharedassets.InstalledBuilds(info.Schema)
	// Fetch every locally imported row once and index it by site -> origin asset id:
	// this decides whether the button says install / already pulled / waiting for review, and supplies the local snapshot (to detect stale).
	// It does not query per peer: with many peers that becomes N full table scans.
	localAll, lerr := sharedassets.List(sharedassets.Filter{})
	if lerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query imported assets", lerr), "")
		return
	}
	importedBy := make(map[string]map[string]*sharedassets.Asset, len(list))
	for i := range localAll {
		a := &localAll[i]
		if a.OriginServer == "" || !a.OriginAssetID.Valid {
			continue
		}
		m := importedBy[a.OriginServer]
		if m == nil {
			m = make(map[string]*sharedassets.Asset, 4)
			importedBy[a.OriginServer] = m
		}
		m[a.OriginAssetID.String] = a
	}

	type peerTarget struct {
		p     *peers.Peer
		entry *catalogCacheEntry
	}
	targets := make([]peerTarget, 0, len(list))
	for i := range list {
		p := &list[i]
		if !peers.VisibleToTenant(p, info.Schema) {
			continue
		}
		if onlyPeer != "" && !strings.EqualFold(onlyPeer, p.Name) {
			continue
		}
		targets = append(targets, peerTarget{p: p})
	}

	// Fetch every peer catalog concurrently: serially the first paint waits for the sum of all sites, and one slow site (20s timeout)
	// delays the whole page even though the others were ready long ago. peerCatalog locks per peer
	// (the global lock only guards the map lookup), so this is concurrency-safe and concurrent callers for one site still fetch once.
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(t *peerTarget) {
			defer wg.Done()
			t.entry = peerCatalog(t.p)
		}(&targets[i])
	}
	wg.Wait()

	out := make([]gin.H, 0, len(targets))
	for i := range targets {
		p, entry := targets[i].p, targets[i].entry
		node := gin.H{
			"peer": p.Name,
			// url lets the tenant see where this site is: it helps with same-named sites and with connection troubleshooting.
			// Two boundaries still hold:
			//   - it never carries a token (constraint 1: the token stays on the server);
			//   - requests still carry a peer name rather than a URL, so there is no SSRF surface (constraint 2) -
			//     what is sent here is a read-only display value and never feeds the construction of an outbound request.
			"url":            displayPeerURL(p.BaseURL),
			"install_policy": p.InstallPolicy,
			"items":          []gin.H{},
			"count":          0, // match total (after filtering, before truncation)
			"truncated":      false,
		}
		if entry.err != "" {
			node["error"] = entry.err
			out = append(out, node)
			continue
		}
		node["server"] = entry.server

		imported := importedBy[p.Name]
		matched := 0
		items := make([]gin.H, 0, 64)
		for _, it := range entry.items {
			if kind != "" && it.Kind != kind {
				continue
			}
			if keyword != "" && !remoteMatch(it, keyword) {
				continue
			}
			matched++ // keep counting: count is the match total and does not depend on truncation
			if len(items) >= remoteCatalogItemLimit {
				continue
			}
			// Catalog fields keep the same names as the local assetView so the front end can reuse one renderer
			h := gin.H{
				"id":           it.ID,
				"kind":         it.Kind,
				"name":         it.Name,
				"display_name": it.DisplayName,
				"description":  it.Description,
				"build":        it.Build,
				"entry":        it.Entry,
				"deps":         it.Deps,
				"file_count":   it.FileCount,
				"total_size":   it.TotalSize,
				"updated_at":   it.UpdatedAt,
				"remote":       p.Name,
			}
			if a, hit := imported[it.ID]; hit {
				h["local"] = gin.H{"asset_id": a.ID, "build": a.Build, "status": a.Status}
				// installed means "this tenant installed it"; the build number lets the front end spot an outdated install
				if v, has := installed[a.ID]; has {
					h["installed"] = true
					h["installed_build"] = v
				}
				if it.Build > a.Build {
					h["stale"] = true // the peer has a newer snapshot than the local copy
				}
			}
			items = append(items, h)
		}
		sort.SliceStable(items, func(a, b int) bool {
			return fmt.Sprint(items[a]["name"]) < fmt.Sprint(items[b]["name"])
		})
		node["items"] = items
		node["count"] = matched
		node["truncated"] = matched > len(items)
		out = append(out, node)
	}

	xhttp.OkJSON(c, "Success", gin.H{"peers": out, "total": len(out)}, "")
}

// remoteMatch matches a catalog item by keyword (name / display name / description, case-insensitive).
//
// It is called before truncation: an item at position 300 must still be findable, or "found but not shown" is the hardest thing to explain.
func remoteMatch(it catalogItem, kw string) bool {
	kw = strings.ToLower(kw)
	return strings.Contains(strings.ToLower(it.Name), kw) ||
		strings.Contains(strings.ToLower(it.DisplayName), kw) ||
		strings.Contains(strings.ToLower(it.Description), kw)
}

type pullRequest struct {
	Peer    string `json:"peer"`
	AssetID string `json:"asset_id"`
	Force   bool   `json:"force"`
}

// PullAsset POST /admin/share/pull-asset
//
// Pull on demand: fetch one remote asset into a local platform asset row (a shared snapshot), and the front end then calls /share/install to install it into the tenant.
// The two steps are deliberate: pulling deduplicates by asset (several tenants installing the same one download it once), while installing is per tenant.
//
// The returned status decides what the front end does next:
//
//	listed  -> install right away
//	pending -> submitted for platform review, waiting for approval (the default path under install_policy=review)
func PullAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var req pullRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	peerName := strings.TrimSpace(req.Peer)
	assetID := strings.TrimSpace(req.AssetID)
	if peerName == "" || assetID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "peer and asset id are required", "")
		return
	}

	// Only platform-configured sources are reachable: name -> configuration, and the URL never comes from the request body (no SSRF surface)
	list, err := peers.ListPeers()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list remote markets: "+err.Error(), "")
		return
	}
	var peer *peers.Peer
	for i := range list {
		if strings.EqualFold(list[i].Name, peerName) {
			peer = &list[i]
			break
		}
	}
	if peer == nil || !peers.VisibleToTenant(peer, info.Schema) {
		xhttp.ErrCode(c, "asset.peer_not_available", map[string]any{"peer": peerName},
			"this remote market is not open to your tenant", "")
		return
	}

	// Fast path: this machine already has the row (another tenant or the platform pulled it earlier) -> no download.
	// Force=true skips the fast path: the caller explicitly wants a fresh snapshot, and whether to overwrite is left to the Import logic below
	// (which finds an existing row by name, reuses its id and bumps the snapshot monotonically).
	if existing, ferr := sharedassets.FindByOriginAsset(peer.Name, assetID); ferr != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("query imported asset", ferr), "")
		return
	} else if existing != nil && !req.Force {
		xhttp.OkJSON(c, "Success", pullResultView(existing, false), "")
		return
	}

	data, derr := fetchPeerPackage(peer, assetID)
	if derr != nil {
		xhttp.ErrCode(c, "common.internal", nil, derr.Error(), "")
		return
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	res, ierr := assetimport.Import(bytes.NewReader(data), int64(len(data)), st, assetimport.Options{
		Origin:      peer.Name,
		Force:       req.Force,
		AutoApprove: peer.InstallPolicy == peers.InstallPolicyAuto,
		ImportedBy:  info.ID,
	})
	if ierr != nil {
		var ce *assetimport.ConflictError
		if errors.As(ierr, &ce) {
			// The peer's asset id matches a local record: a concurrent or historical leftover, so return the existing row
			if e, ferr := sharedassets.FindByOriginAsset(peer.Name, assetID); ferr == nil && e != nil {
				xhttp.OkJSON(c, "Success", pullResultView(e, false), "")
				return
			}
			// Same name but a different origin id: most likely the peer withdrew and listed again, deriving a new id.
			// ConflictError only states the facts (origin / name / build) and the platform import side pops its own confirmation;
			// the tenant market's buttons mean something else, so this builds its own "overwrite required to continue",
			// returns it together with the structured conflict, and the front end asks once and retries with force.
			xhttp.ErrCodeData(c, "asset.name_conflict",
				map[string]any{"name": ce.Name, "build": ce.ExistingBuild},
				fmt.Sprintf("this machine already has an asset named \"%s\" (v%d); continue only by overwriting", ce.Name, ce.ExistingBuild),
				gin.H{"conflict": gin.H{
					"name":     ce.Name,
					"build":    ce.ExistingBuild,
					"status":   ce.ExistingStatus,
					"asset_id": ce.ExistingID,
					"peer":     peer.Name,
				}}, "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "pull failed: "+ierr.Error(), "")
		return
	}
	invalidatePeerCatalog(peer.ID)

	asset, gerr := sharedassets.Get(res.AssetID)
	if gerr != nil || asset == nil {
		xhttp.OkJSON(c, "Success", gin.H{
			"asset_id": res.AssetID, "kind": res.Kind, "name": res.Name,
			"build": res.Build, "status": res.Status, "warnings": res.Warnings,
		}, "")
		return
	}
	view := pullResultView(asset, true)
	view["warnings"] = res.Warnings
	xhttp.OkJSON(c, "Success", view, "")
}

// pullResultView is the pull result: the front end decides between "continue installing" and "waiting for review".
func pullResultView(a *sharedassets.Asset, pulled bool) gin.H {
	return gin.H{
		"asset_id":      a.ID,
		"kind":          a.Kind,
		"name":          a.Name,
		"display_name":  a.DisplayName,
		"description":   a.Description,
		"build":         a.Build,
		"status":        a.Status,
		"source_schema": a.SourceSchema,
		"origin_server": a.OriginServer,
		"pulled":        pulled,
		"need_review":   a.Status != sharedassets.StatusListed,
		"listed":        a.Status == sharedassets.StatusListed,
		"warnings":      []string{},
	}
}

// fetchPeerPackage downloads the zip from the peer (server side; the token never leaves the platform).
func fetchPeerPackage(p *peers.Peer, assetID string) ([]byte, error) {
	url := strings.TrimRight(p.BaseURL, "/") + "/platform/market/assets/" + assetID + "/package"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid peer address: %w", err)
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach the peer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("peer returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, remotePackageMax+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the peer bundle: %w", err)
	}
	if len(data) > remotePackageMax {
		return nil, fmt.Errorf("the peer bundle exceeds the limit (%d MiB)", remotePackageMax>>20)
	}
	return data, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
