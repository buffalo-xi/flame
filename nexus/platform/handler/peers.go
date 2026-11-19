package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"flame/nexus/platform/auth"
	"flame/nexus/shared/assetimport"
	"flame/nexus/shared/repository/peers"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Both sides of the cross-server market configuration (platform administrators only):
//
//	/platform/peers*         - the end I pull from (address plus outbound token)
//	/platform/market-tokens* - who may pull from me (only the sha256 is stored; the plain token is returned once at creation)
//
// Pulling is deliberately two steps: see the catalog first (PeerCatalog), then import one by one
// (ImportFromPeer).
// A bulk pull turns "the other side sent a pile of things" into one irreversible action, while
// importing one by one always shows the warnings of each item.

const (
	peerHTTPTimeout = 30 * time.Second

	// peerCatalogMax caps the catalog response body (same order of magnitude as remoteCatalogMax on the tenant side).
	// It must be paired with "read one byte past the limit": io.ReadAll(io.LimitReader(...)) returns EOF with a nil
	// error once the limit is reached, so a truncated JSON would reach Unmarshal and report "catalog too large" as
	// "invalid response".
	peerCatalogMax = 4 << 20
)

var peerClient = &http.Client{Timeout: peerHTTPTimeout}

var (
	tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// schemaNameRe matches the CHECK on platform.tenants (the peer's visible list holds tenant schemas)
	schemaNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	// originRe is the origin site name (goes into varchar(64) and takes part in a unique key, so the format is enforced first)
	originRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// schemaErrMsg turns "missing table/column" into a sentence the reader can act on.
//
// The DDL and migration scripts under platform are run by hand (see platform/README.md); when the table
// schema is behind the code, the raw pq text (column "visible_to" does not exist) is what comes back, and a
// first-time deployer has nothing to go on but the docs.
// 42703 = undefined_column, 42P01 = undefined_table.
func schemaErrMsg(prefix, script string, err error) string {
	if err == nil {
		return prefix
	}
	var pe *pq.Error
	if errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "42703") {
		return prefix + ": " + err.Error() + " (the schema is behind the code; run nexus/platform/sql/" + script + ")"
	}
	return prefix + ": " + err.Error()
}

// peerView is the peer view: the plain token is never returned (a single list refresh would leak it each time),
// only whether one is configured.
func peerView(p *peers.Peer) gin.H {
	v := gin.H{
		"id":             p.ID,
		"name":           p.Name,
		"base_url":       p.BaseURL,
		"enabled":        p.Enabled,
		"visible_to":     p.VisibleTo,
		"tenant_schemas": p.Tenants,
		"install_policy": p.InstallPolicy,
		"last_error":     p.LastError,
		"has_token":      strings.TrimSpace(p.Token) != "",
		// auth_mode is a derived value (an empty token means none), which the frontend uses to preselect the dropdown;
		// has_token is kept because an older frontend only knows that field.
		"auth_mode":  p.AuthMode(),
		"created_at": p.CreatedAt,
		"updated_at": p.UpdatedAt,
	}
	if p.LastSyncAt.Valid {
		v["last_sync_at"] = p.LastSyncAt.Time
	}
	return v
}

// ListPeers GET /platform/peers
func ListPeers(c *gin.Context) {
	list, err := peers.ListPeers()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query peers", "peer_market.sql", err), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, peerView(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

type peerRequest struct {
	Name          string   `json:"name"`
	BaseURL       string   `json:"base_url"`
	Token         string   `json:"token"`
	AuthMode      string   `json:"auth_mode"` // token (default) | none
	Enabled       *bool    `json:"enabled"`
	VisibleTo     string   `json:"visible_to"`     // all | allowlist
	TenantSchemas []string `json:"tenant_schemas"` // allowlist entries
	InstallPolicy string   `json:"install_policy"` // review | auto
}

func (r *peerRequest) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	r.Token = strings.TrimSpace(r.Token)
	if !originRe.MatchString(r.Name) {
		return errors.New("a site name may contain letters, digits, dots, underscores and hyphens, up to 64 characters")
	}
	if r.BaseURL == "" {
		return errors.New("please fill in the peer address (for example https://test.example.com)")
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the peer address must start with http(s):// and include a host name")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("the peer address must not include a path (host name and port only; /platform is appended automatically)")
	}

	// Auth mode: a peer market can be public (market.open: true), in which case no token is needed.
	// When auth_mode is absent it is inferred from whether a token is present, for older callers that only send token.
	r.AuthMode = strings.TrimSpace(r.AuthMode)
	switch r.AuthMode {
	case "":
		if r.Token == "" {
			r.AuthMode = peers.AuthModeNone
		} else {
			r.AuthMode = peers.AuthModeToken
		}
	case peers.AuthModeNone:
		// Public market: leave no leftover token. A stale value would become a credential that "looks configured
		// but should have been deleted long ago" once the peer re-enables token checking.
		r.Token = ""
	case peers.AuthModeToken:
		if r.Token == "" {
			return errors.New("choosing \"market token\" requires the flmt_... token the peer gave you; for a public peer market choose \"public (no token)\"")
		}
	default:
		return errors.New("the auth mode must be token (market token) or none (public market)")
	}

	// Visibility: all = every tenant sees this remote in its own asset market; allowlist = only the listed tenants
	r.VisibleTo = strings.TrimSpace(r.VisibleTo)
	if r.VisibleTo == "" {
		r.VisibleTo = peers.VisibleAll
	}
	if r.VisibleTo != peers.VisibleAll && r.VisibleTo != peers.VisibleAllowlist {
		return errors.New("visibility must be all (every tenant) or allowlist (selected tenants)")
	}
	schemas := make([]string, 0, len(r.TenantSchemas))
	for _, s := range r.TenantSchemas {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		if !schemaNameRe.MatchString(v) {
			return fmt.Errorf("invalid tenant schema: %s", v)
		}
		schemas = append(schemas, v)
	}
	if r.VisibleTo == peers.VisibleAllowlist && len(schemas) == 0 {
		return errors.New("choosing \"selected tenants\" requires at least one tenant, otherwise nobody can see this market")
	}
	r.TenantSchemas = schemas

	// Install policy: review = pull it into review (default); auto = install straight into the requesting tenant
	r.InstallPolicy = strings.TrimSpace(r.InstallPolicy)
	if r.InstallPolicy == "" {
		r.InstallPolicy = peers.InstallPolicyReview
	}
	if r.InstallPolicy != peers.InstallPolicyReview && r.InstallPolicy != peers.InstallPolicyAuto {
		return errors.New("the install policy must be review (review first) or auto (install directly)")
	}
	return nil
}

// CreatePeer POST /platform/peers
func CreatePeer(c *gin.Context) {
	var req peerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: "+err.Error(), "")
		return
	}
	if err := req.normalize(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	exist, err := peers.ExistsPeerName(req.Name, "")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query peers", "peer_market.sql", err), "")
		return
	}
	if exist {
		xhttp.ErrCode(c, "peer.name_taken", gin.H{"name": req.Name}, "a peer with this name already exists: "+req.Name, "")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p := &peers.Peer{
		ID: uuid.NewString(), Name: req.Name, BaseURL: req.BaseURL, Token: req.Token,
		Enabled: enabled, VisibleTo: req.VisibleTo, Tenants: req.TenantSchemas,
		InstallPolicy: req.InstallPolicy,
	}
	if err := peers.InsertPeer(p); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to create the peer", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"peer": peerView(p)}, "")
}

// UpdatePeer PUT /platform/peers/:id
func UpdatePeer(c *gin.Context) {
	p, err := peers.GetPeer(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query peers", "peer_market.sql", err), "")
		return
	}
	if p == nil {
		xhttp.ErrCode(c, "peer.not_found", gin.H{"id": c.Param("id")}, "peer not found", "")
		return
	}
	var req peerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: "+err.Error(), "")
		return
	}
	// The auth mode takes priority over "empty means unchanged":
	//   auth_mode=none -> explicitly switch to a public market and clear the old token (it must be removable, or the switch cannot be undone);
	//   anything else (including an absent auth_mode) -> an empty token means unchanged, so keep the old value.
	if strings.TrimSpace(req.AuthMode) != peers.AuthModeNone && strings.TrimSpace(req.Token) == "" {
		req.Token = p.Token
	}
	if err := req.normalize(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	exist, err := peers.ExistsPeerName(req.Name, p.ID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query peers", "peer_market.sql", err), "")
		return
	}
	if exist {
		xhttp.ErrCode(c, "peer.name_taken", gin.H{"name": req.Name}, "a peer with this name already exists: "+req.Name, "")
		return
	}
	p.Name, p.BaseURL, p.Token = req.Name, req.BaseURL, req.Token
	p.VisibleTo, p.Tenants, p.InstallPolicy = req.VisibleTo, req.TenantSchemas, req.InstallPolicy
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := peers.UpdatePeer(p); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to update the peer", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"peer": peerView(p)}, "")
}

// DeletePeer DELETE /platform/peers/:id
func DeletePeer(c *gin.Context) {
	if err := peers.DeletePeer(c.Param("id")); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to delete the peer", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{}, "")
}

// PeerCatalog GET /platform/peers/:id/catalog?kind=
//
// Proxies the peer's read-only catalog and marks "already imported from that site / local snapshot", which the
// frontend uses to label the button "import" or "import and overwrite".
func PeerCatalog(c *gin.Context) {
	p, ok := mustPeer(c)
	if !ok {
		return
	}
	pathQS := "/market/catalog"
	if k := strings.TrimSpace(c.Query("kind")); k != "" {
		pathQS += "?kind=" + url.QueryEscape(k)
	}
	resp, err := peerGet(p, pathQS)
	if err != nil {
		_ = peers.TouchPeer(p.ID, err.Error())
		xhttp.ErrCode(c, "peer.unreachable", gin.H{"peer": p.Name}, "failed to reach the peer: "+err.Error(), "")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Read one byte past the limit: hitting it means the response was truncated, so report "catalog too large"
	// instead of letting the JSON decoder take the blame
	body, _ := io.ReadAll(io.LimitReader(resp.Body, peerCatalogMax+1))
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("the peer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrCode(c, "common.internal", nil, msg, "")
		return
	}
	if len(body) > peerCatalogMax {
		msg := fmt.Sprintf(
			"the peer catalog exceeds the %d MiB limit and the response was truncated (not a format problem). Ask the peer to list fewer assets, or pull by kind in batches (?kind=skill / custom_tool / agent / workflow)",
			peerCatalogMax>>20,
		)
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrCode(c, "common.internal", nil, msg, "")
		return
	}

	var payload struct {
		Data struct {
			Server string           `json:"server"`
			Items  []map[string]any `json:"items"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(body, &payload); jerr != nil {
		_ = peers.TouchPeer(p.ID, "the peer response is not valid JSON")
		xhttp.ErrCodeData(c, "peer.invalid_response", gin.H{"peer": p.Name},
			"the peer response is not valid JSON (is the peer's snapshot too old, or the address wrong?)", body, "")
		return
	}

	// Assets already imported from that site: origin_asset_id -> local snapshot
	imported, err := sharedassets.List(sharedassets.Filter{OriginServer: p.Name})
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to query local imports: "+err.Error(), "")
		return
	}
	local := make(map[string]gin.H, len(imported))
	for i := range imported {
		a := &imported[i]
		if !a.OriginAssetID.Valid {
			continue
		}
		local[a.OriginAssetID.String] = gin.H{"build": a.Build, "status": a.Status, "id": a.ID}
	}

	items := make([]gin.H, 0, len(payload.Data.Items))
	for _, it := range payload.Data.Items {
		if id, ok := it["id"].(string); ok {
			if l, hit := local[id]; hit {
				it["imported"] = true
				it["local"] = l
			}
		}
		items = append(items, it)
	}

	_ = peers.TouchPeer(p.ID, "")
	xhttp.OkJSON(c, "Success", gin.H{
		"server": payload.Data.Server,
		"peer":   p.Name,
		"items":  items,
		"total":  len(items),
	}, "")
}

// ImportFromPeer POST /platform/peers/:id/import  body: {asset_id, force}
//
// Pull the package, then run the exact same import pipeline as a manual upload (validate / scrub / write files /
// write rows).
// Only an auto_approve peer lands listed directly; everything else goes to pending.
func ImportFromPeer(c *gin.Context) {
	p, ok := mustPeer(c)
	if !ok {
		return
	}
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	var req struct {
		AssetID string `json:"asset_id"`
		Force   bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AssetID) == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "provide the asset id to import", "")
		return
	}

	resp, err := peerGet(p, "/market/assets/"+url.PathEscape(req.AssetID)+"/package")
	if err != nil {
		_ = peers.TouchPeer(p.ID, err.Error())
		xhttp.ErrCode(c, "peer.unreachable", gin.H{"peer": p.Name}, "failed to reach the peer: "+err.Error(), "")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("the peer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrCode(c, "common.internal", nil, msg, "")
		return
	}

	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPackageUpload+1))
	if rerr != nil {
		xhttp.ErrCode(c, "peer.invalid_response", gin.H{"peer": p.Name}, "failed to read the peer asset package: "+rerr.Error(), "")
		return
	}
	if len(data) > maxPackageUpload {
		xhttp.ErrCode(c, "asset.package_too_large", gin.H{"limit_mb": maxPackageUpload >> 20},
			fmt.Sprintf("the peer package exceeds the limit (%d MiB)", maxPackageUpload>>20), "")
		return
	}

	st, serr := packageStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	res, ierr := assetimport.Import(bytes.NewReader(data), int64(len(data)), st, assetimport.Options{
		Origin:      p.Name,
		Force:       req.Force,
		AutoApprove: p.InstallPolicy == peers.InstallPolicyAuto,
		ImportedBy:  info.ID,
	})
	if ierr != nil {
		_ = peers.TouchPeer(p.ID, ierr.Error())
		var ce *assetimport.ConflictError
		if errors.As(ierr, &ce) {
			xhttp.ErrCodeData(c, "asset.build_conflict", gin.H{"build": ce.ExistingBuild}, ce.Error(), gin.H{"existing": gin.H{
				"id": ce.ExistingID, "build": ce.ExistingBuild, "status": ce.ExistingStatus,
			}}, "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, ierr.Error(), "")
		return
	}
	_ = peers.TouchPeer(p.ID, "")
	xhttp.OkJSON(c, "Success", gin.H{
		"asset": gin.H{
			"id": res.AssetID, "kind": res.Kind, "name": res.Name,
			"build": res.Build, "status": res.Status,
			"origin_server": res.Origin, "source_schema": res.SourceSchema,
		},
		"overwritten": res.Overwritten,
		"warnings":    res.Warnings,
	}, "")
}

func mustPeer(c *gin.Context) (*peers.Peer, bool) {
	p, err := peers.GetPeer(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query peers", "peer_market.sql", err), "")
		return nil, false
	}
	if p == nil {
		xhttp.ErrCode(c, "peer.not_found", gin.H{"id": c.Param("id")}, "peer not found", "")
		return nil, false
	}
	if !p.Enabled {
		xhttp.ErrCode(c, "peer.disabled", gin.H{"name": p.Name}, "the peer is disabled: "+p.Name, "")
		return nil, false
	}
	return p, true
}

// peerGet sends a read-only request to the peer (the address carries the /platform prefix, the token goes in Authorization).
func peerGet(p *peers.Peer, path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/platform"+path, nil)
	if err != nil {
		return nil, err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	return peerClient.Do(req)
}

// ===== Market egress policy (whether to check tokens) =====

// GetMarketSettings GET /platform/market-settings
//
// No row means a token is required (open=false): that is the fail-closed default and is not materialised by seed data.
func GetMarketSettings(c *gin.Context) {
	s, err := peers.GetMarketSetting()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query the market settings", "peer_market.sql", err), "")
		return
	}
	v := gin.H{"open": s.Open}
	if s.UpdatedAt.Valid {
		v["updated_at"] = s.UpdatedAt.Time
	}
	if s.UpdatedBy.Valid {
		v["updated_by"] = s.UpdatedBy.String
	}
	xhttp.OkJSON(c, "Success", v, "")
}

// SetMarketSettings PUT /platform/market-settings  body: {open}
//
// Open = anyone with this server's address can read the reviewed catalog and asset packages. That is a governance
// decision made by a platform administrator in the console, not through a config file (editing a file would mean
// logging into the machine and restarting).
// A request carrying an invalid token still gets 401 - open does not mean unvalidated.
func SetMarketSettings(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	var req struct {
		Open *bool `json:"open"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Open == nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: the body must be {\"open\": true|false}", "")
		return
	}
	if err := peers.SetMarketOpen(*req.Open, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to save the market settings", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"open": *req.Open}, "")
}

// ===== Market egress tokens =====

func marketTokenView(t *peers.MarketToken) gin.H {
	v := gin.H{
		"id":         t.ID,
		"name":       t.Name,
		"enabled":    t.Enabled,
		"created_at": t.CreatedAt,
	}
	if t.LastUsedAt.Valid {
		v["last_used_at"] = t.LastUsedAt.Time
	}
	return v
}

// ListMarketTokens GET /platform/market-tokens
func ListMarketTokens(c *gin.Context) {
	list, err := peers.ListMarketTokens()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query market tokens", "peer_market.sql", err), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, marketTokenView(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": len(items)}, "")
}

// CreateMarketToken POST /platform/market-tokens  body: {name}
//
// The plain token is returned **only once, here**; the database keeps the sha256 only. Lose it and delete and
// recreate the token.
func CreateMarketToken(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: "+err.Error(), "")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !tokenNameRe.MatchString(req.Name) {
		xhttp.ErrCode(c, "peer.invalid_token_name", nil, "a token name may contain letters, digits, dots, underscores and hyphens, up to 64 characters", "")
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to generate the token: "+err.Error(), "")
		return
	}
	plain := "flmt_" + hex.EncodeToString(raw)

	t := &peers.MarketToken{
		ID:        uuid.NewString(),
		Name:      req.Name,
		TokenHash: auth.HashMarketToken(plain),
		Enabled:   true,
	}
	if info.ID != "" {
		t.CreatedBy.String = info.ID
		t.CreatedBy.Valid = true
	}
	if err := peers.InsertMarketToken(t); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the market token: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"token": marketTokenView(t), "plain": plain}, "")
}

// SetMarketToken PUT /platform/market-tokens/:id  body: {enabled}
func SetMarketToken(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: "+err.Error(), "")
		return
	}
	if err := peers.SetMarketTokenEnabled(c.Param("id"), req.Enabled); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to update the market token: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"enabled": req.Enabled}, "")
}

// DeleteMarketToken DELETE /platform/market-tokens/:id
func DeleteMarketToken(c *gin.Context) {
	if err := peers.DeleteMarketToken(c.Param("id")); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the market token: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{}, "")
}
