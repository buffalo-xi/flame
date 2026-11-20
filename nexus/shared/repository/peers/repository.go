// Package peers holds both ends of the cross-server marketplace configuration:
//
//	platform.peer_servers  — the end I pull from (address + outbound token + whether review is skipped)
//	platform.market_tokens — who may pull from me (only the sha256 is stored; the plaintext is returned once, at creation)
//
// Both tables sit on the platform side and have nothing to do with tenant data; persisting an import reuses
// shared/repository/sharedassets.
package peers

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

func peerTable() string  { return config.Cfg.Database.PlatformSchema + ".peer_servers" }
func tokenTable() string { return config.Cfg.Database.PlatformSchema + ".market_tokens" }

// Install policies (what happens to a pulled asset when a tenant clicks "install").
const (
	// InstallPolicyReview puts pulled assets into pending review first; the tenant may install only after the platform
	// approves (the default: environments do not wave each other through).
	InstallPolicyReview = "review"
	// InstallPolicyAuto lists a pulled asset (listed) and installs it into the initiating tenant right away (recommended
	// only for a test source you fully control).
	InstallPolicyAuto = "auto"
)

// Visibility scopes (which tenants can see this peer in their own asset marketplace).
const (
	VisibleAll       = "all"
	VisibleAllowlist = "allowlist"
)

// Outbound auth mode (whether a token is sent when I pull from a peer).
//
// Not stored: an empty token string already means none — "is there a token" is the only fact, and one more column would
// allow a self-contradictory row of "the mode is token but the token is empty".
const (
	// AuthModeToken sends a Bearer token (the peer marketplace requires one; the default).
	AuthModeToken = "token"
	// AuthModeNone sends no token (the peer enabled market.open, an open marketplace).
	AuthModeNone = "none"
)

// Peer is one peer site (I pull from it).
type Peer struct {
	ID        string
	Name      string
	BaseURL   string
	Token     string // outbound token (plaintext; only returned by the admin endpoint)
	Enabled   bool
	VisibleTo string   // all | allowlist
	Tenants   []string // the allowlist (takes effect when visible_to=allowlist)
	// InstallPolicy decides what happens after a tenant clicks "install": review = import into pending review first;
	// auto = install directly.
	InstallPolicy string
	LastSyncAt    sql.NullTime
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AuthMode reports whether this peer needs a token (derived from Token, see AuthModeToken / AuthModeNone).
func (p *Peer) AuthMode() string {
	if strings.TrimSpace(p.Token) != "" {
		return AuthModeToken
	}
	return AuthModeNone
}

const peerColumns = `id, name, base_url, token, enabled, visible_to,
	COALESCE(tenant_schemas, '[]'::jsonb), install_policy,
	last_sync_at, last_error, created_at, updated_at`

func scanPeer(sc interface{ Scan(...any) error }, p *Peer) error {
	var tenants []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.BaseURL, &p.Token, &p.Enabled, &p.VisibleTo,
		&tenants, &p.InstallPolicy, &p.LastSyncAt, &p.LastError, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return err
	}
	p.Tenants = nil
	if len(tenants) > 0 {
		_ = json.Unmarshal(tenants, &p.Tenants)
	}
	return nil
}

// VisibleToTenant reports whether a peer is open to a tenant (a disabled peer is never visible; the caller checks enabled
// first).
//
// This is the single criterion for "may a tenant see this remote in the marketplace": the platform configures the
// visibility scope, and the tenant-side endpoints must filter by it, otherwise the allowlist may as well not exist.
func VisibleToTenant(p *Peer, schema string) bool {
	if p == nil || !p.Enabled || schema == "" {
		return false
	}
	if p.VisibleTo != VisibleAllowlist {
		return true
	}
	for _, s := range p.Tenants {
		if s == schema {
			return true
		}
	}
	return false
}

// TenantsJSON serialises the allowlist into jsonb text (for writing to the database).
func (p *Peer) TenantsJSON() string {
	if len(p.Tenants) == 0 {
		return "[]"
	}
	b, err := json.Marshal(p.Tenants)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ListPeers lists every peer (sorted by name).
func ListPeers() ([]Peer, error) {
	rows, err := db.DB.Query(`SELECT ` + peerColumns + ` FROM ` + peerTable() + ` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		if err := scanPeer(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPeer fetches by id; missing returns (nil, nil).
func GetPeer(id string) (*Peer, error) {
	var p Peer
	err := scanPeer(db.DB.QueryRow(`SELECT `+peerColumns+` FROM `+peerTable()+` WHERE id = $1::uuid`, id), &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ExistsPeerName reports whether a name is taken (excludeID excludes the row itself on update).
func ExistsPeerName(name, excludeID string) (bool, error) {
	var n int
	err := db.DB.QueryRow(`SELECT count(*) FROM `+peerTable()+
		` WHERE lower(name) = lower($1) AND ($2 = '' OR id <> $2::uuid)`, strings.TrimSpace(name), excludeID).Scan(&n)
	return n > 0, err
}

// InsertPeer creates a peer.
func InsertPeer(p *Peer) error {
	_, err := db.DB.Exec(`INSERT INTO `+peerTable()+`
		(id, name, base_url, token, enabled, visible_to, tenant_schemas, install_policy)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		p.ID, p.Name, p.BaseURL, p.Token, p.Enabled,
		p.VisibleTo, p.TenantsJSON(), p.InstallPolicy)
	return err
}

// UpdatePeer changes the configuration.
func UpdatePeer(p *Peer) error {
	_, err := db.DB.Exec(`UPDATE `+peerTable()+`
		SET name = $2, base_url = $3, token = $4, enabled = $5,
		    visible_to = $6, tenant_schemas = $7::jsonb, install_policy = $8, updated_at = now()
		WHERE id = $1::uuid`,
		p.ID, p.Name, p.BaseURL, p.Token, p.Enabled,
		p.VisibleTo, p.TenantsJSON(), p.InstallPolicy)
	return err
}

// DeletePeer deletes a peer (assets already imported are unaffected: they only keep the origin_server string).
func DeletePeer(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+peerTable()+` WHERE id = $1::uuid`, id)
	return err
}

// TouchPeer records one sync result (clears last_error when errMsg is empty).
func TouchPeer(id string, errMsg string) error {
	_, err := db.DB.Exec(`UPDATE `+peerTable()+`
		SET last_sync_at = now(), last_error = $2, updated_at = now() WHERE id = $1::uuid`, id, errMsg)
	return err
}

// ============================================================================
// Marketplace export tokens
// ============================================================================

// MarketToken is one read-only market token (the database holds the sha256 only).
type MarketToken struct {
	ID         string
	Name       string
	TokenHash  string
	Enabled    bool
	CreatedBy  sql.NullString
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
}

const tokenColumns = `id, name, token_hash, enabled, created_by, created_at, last_used_at`

func scanToken(sc interface{ Scan(...any) error }, t *MarketToken) error {
	return sc.Scan(&t.ID, &t.Name, &t.TokenHash, &t.Enabled, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt)
}

// ListMarketTokens lists every token (never the plaintext, which only existed once at creation).
func ListMarketTokens() ([]MarketToken, error) {
	rows, err := db.DB.Query(`SELECT ` + tokenColumns + ` FROM ` + tokenTable() + ` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketToken
	for rows.Next() {
		var t MarketToken
		if err := scanToken(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertMarketToken creates a token.
func InsertMarketToken(t *MarketToken) error {
	_, err := db.DB.Exec(`INSERT INTO `+tokenTable()+`
		(id, name, token_hash, enabled, created_by) VALUES ($1::uuid,$2,$3,$4,$5::uuid)`,
		t.ID, t.Name, t.TokenHash, t.Enabled, t.CreatedBy)
	return err
}

// DeleteMarketToken revokes a token.
func DeleteMarketToken(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+tokenTable()+` WHERE id = $1::uuid`, id)
	return err
}

// SetMarketTokenEnabled enables / disables a token (the soft way to revoke: the audit trail is kept).
func SetMarketTokenEnabled(id string, enabled bool) error {
	_, err := db.DB.Exec(`UPDATE `+tokenTable()+` SET enabled = $2 WHERE id = $1::uuid`, id, enabled)
	return err
}

// FindMarketTokenByHash does inbound authentication: find the enabled token by sha256, and refresh last_used_at on a hit.
func FindMarketTokenByHash(hash string) (*MarketToken, error) {
	var t MarketToken
	err := scanToken(db.DB.QueryRow(`SELECT `+tokenColumns+` FROM `+tokenTable()+
		` WHERE token_hash = $1 AND enabled = true`, hash), &t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Best effort: a failed refresh does not affect this request
	_, _ = db.DB.Exec(`UPDATE `+tokenTable()+` SET last_used_at = now() WHERE id = $1::uuid`, t.ID)
	return &t, nil
}

// ============================================================================
// Marketplace export policy (single-row table: whether tokens are checked)
// ============================================================================

func settingsTable() string { return config.Cfg.Database.PlatformSchema + ".market_settings" }

// MarketSetting is the current export policy (Open=false when there is no row).
type MarketSetting struct {
	Open      bool
	UpdatedAt sql.NullTime
	UpdatedBy sql.NullString
}

// GetMarketSetting reads the export policy (including who changed it and when).
func GetMarketSetting() (*MarketSetting, error) {
	var s MarketSetting
	err := db.DB.QueryRow(`SELECT open, updated_at, updated_by FROM `+settingsTable()+` WHERE id = 1`).
		Scan(&s.Open, &s.UpdatedAt, &s.UpdatedBy)
	if err == sql.ErrNoRows {
		return &MarketSetting{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// MarketOpen reports whether the marketplace allows anonymous access. No row means not allowed (fail closed: forgetting
// to configure never means wide open).
//
// No in-process cache: the same rule as the capabilitypolicy / pluginpolicy gates — with several instances deployed a
// cache would keep "the switch just turned off" in effect for a while, and this switch is a security-relevant vote.
// /platform/market/* are low-frequency endpoints (pull the catalogue, import a pack), so one point query is a fair price
// for dropping that inconsistency.
func MarketOpen() (bool, error) {
	var open bool
	err := db.DB.QueryRow(`SELECT open FROM ` + settingsTable() + ` WHERE id = 1`).Scan(&open)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return open, nil
}

// SetMarketOpen writes the export policy (single-row upsert); by is the operator's platform user id (may be empty).
func SetMarketOpen(open bool, by string) error {
	var byVal any
	if by = strings.TrimSpace(by); by != "" {
		byVal = by
	}
	_, err := db.DB.Exec(`INSERT INTO `+settingsTable()+` (id, open, updated_at, updated_by)
		VALUES (1, $1, now(), $2::uuid)
		ON CONFLICT (id) DO UPDATE SET
		    open = EXCLUDED.open, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		open, byVal)
	return err
}
