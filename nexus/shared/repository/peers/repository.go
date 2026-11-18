// Package peers 跨服务器市场的两端配置：
//
//	platform.peer_servers  —— 我去拉的那一端（地址 + 出站 token + 是否自动通过）
//	platform.market_tokens —— 允许谁来拉我（只存 sha256，明文只在创建时回一次）
//
// 两张表都在平台侧，与租户数据无关；导入落库复用 shared/repository/sharedassets。
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

// 安装策略（租户点「安装」时，拉下来的资产怎么处理）。
const (
	// InstallPolicyReview 拉下来先进待审，平台通过后租户才能装（默认：跨环境不互相放行）。
	InstallPolicyReview = "review"
	// InstallPolicyAuto 拉下来直接上架（listed）并立刻装到发起租户（只建议给自己完全掌控的测试源）。
	InstallPolicyAuto = "auto"
)

// 可见范围（哪些租户能在自己的资产市场里看见这个对端）。
const (
	VisibleAll       = "all"
	VisibleAllowlist = "allowlist"
)

// 出站认证方式（我拉对端时带不带令牌）。
//
// 不落库：token 为空串就是 none —— 「有没有令牌」本身就是唯一事实，再加一列
// 就可能出现「方式是 token 但令牌是空的」这种自相矛盾的行。
const (
	// AuthModeToken 带 Bearer 令牌（对端市场要求令牌，默认）。
	AuthModeToken = "token"
	// AuthModeNone 不带令牌（对端开了 market.open，公开市场）。
	AuthModeNone = "none"
)

// Peer 一个对端站点（我去拉它）。
type Peer struct {
	ID        string
	Name      string
	BaseURL   string
	Token     string // 出站令牌（明文；只在管理员接口返回）
	Enabled   bool
	VisibleTo string   // all | allowlist
	Tenants   []string // allowlist 名单（visible_to=allowlist 时生效）
	// InstallPolicy 决定租户点了「安装」之后：review = 先导入待审；auto = 直接装。
	InstallPolicy string
	LastSyncAt    sql.NullTime
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AuthMode 这个对端要不要带令牌（推导自 Token，见 AuthModeToken / AuthModeNone）。
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

// VisibleToTenant 该对端是否对某租户开放（停用的对端一律不可见，由调用方先判 enabled）。
//
// 这是「租户能不能在市场里看见这个远端」的唯一判据：平台配了可见范围，
// 租户侧接口就必须按它过滤，否则 allowlist 等于没写。
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

// TenantsJSON 把名单序列化成 jsonb 文本（写库用）。
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

// ListPeers 全部对端（按名字排）。
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

// GetPeer 按 id 取；不存在返回 (nil, nil)。
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

// ExistsPeerName 名字是否已被占用（excludeID 用于更新时排除自己）。
func ExistsPeerName(name, excludeID string) (bool, error) {
	var n int
	err := db.DB.QueryRow(`SELECT count(*) FROM `+peerTable()+
		` WHERE lower(name) = lower($1) AND ($2 = '' OR id <> $2::uuid)`, strings.TrimSpace(name), excludeID).Scan(&n)
	return n > 0, err
}

// InsertPeer 新建对端。
func InsertPeer(p *Peer) error {
	_, err := db.DB.Exec(`INSERT INTO `+peerTable()+`
		(id, name, base_url, token, enabled, visible_to, tenant_schemas, install_policy)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		p.ID, p.Name, p.BaseURL, p.Token, p.Enabled,
		p.VisibleTo, p.TenantsJSON(), p.InstallPolicy)
	return err
}

// UpdatePeer 改配置。
func UpdatePeer(p *Peer) error {
	_, err := db.DB.Exec(`UPDATE `+peerTable()+`
		SET name = $2, base_url = $3, token = $4, enabled = $5,
		    visible_to = $6, tenant_schemas = $7::jsonb, install_policy = $8, updated_at = now()
		WHERE id = $1::uuid`,
		p.ID, p.Name, p.BaseURL, p.Token, p.Enabled,
		p.VisibleTo, p.TenantsJSON(), p.InstallPolicy)
	return err
}

// DeletePeer 删除对端（已导入的资产不受影响：它们只保留 origin_server 字符串）。
func DeletePeer(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+peerTable()+` WHERE id = $1::uuid`, id)
	return err
}

// TouchPeer 记录一次同步结果（ok 时清空 last_error）。
func TouchPeer(id string, errMsg string) error {
	_, err := db.DB.Exec(`UPDATE `+peerTable()+`
		SET last_sync_at = now(), last_error = $2, updated_at = now() WHERE id = $1::uuid`, id, errMsg)
	return err
}

// ============================================================================
// 市场出口令牌
// ============================================================================

// MarketToken 一个只读市场令牌（库里只有 sha256）。
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

// ListMarketTokens 全部令牌（不回明文，明文只在创建时出现过一次）。
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

// InsertMarketToken 新建令牌。
func InsertMarketToken(t *MarketToken) error {
	_, err := db.DB.Exec(`INSERT INTO `+tokenTable()+`
		(id, name, token_hash, enabled, created_by) VALUES ($1::uuid,$2,$3,$4,$5::uuid)`,
		t.ID, t.Name, t.TokenHash, t.Enabled, t.CreatedBy)
	return err
}

// DeleteMarketToken 吊销令牌。
func DeleteMarketToken(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+tokenTable()+` WHERE id = $1::uuid`, id)
	return err
}

// SetMarketTokenEnabled 启用/停用（吊销的软方式：保留审计）。
func SetMarketTokenEnabled(id string, enabled bool) error {
	_, err := db.DB.Exec(`UPDATE `+tokenTable()+` SET enabled = $2 WHERE id = $1::uuid`, id, enabled)
	return err
}

// FindMarketTokenByHash 入站鉴权：按 sha256 找启用的令牌；命中后顺手刷 last_used_at。
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
	// 尽力而为：刷不到不影响本次请求
	_, _ = db.DB.Exec(`UPDATE `+tokenTable()+` SET last_used_at = now() WHERE id = $1::uuid`, t.ID)
	return &t, nil
}

// ============================================================================
// 市场出口策略（单行表：要不要校验令牌）
// ============================================================================

func settingsTable() string { return config.Cfg.Database.PlatformSchema + ".market_settings" }

// MarketSetting 出口策略现状（没有行时 Open=false）。
type MarketSetting struct {
	Open      bool
	UpdatedAt sql.NullTime
	UpdatedBy sql.NullString
}

// GetMarketSetting 读出口策略（含「谁在什么时候改的」）。
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

// MarketOpen 市场是否允许匿名访问。没有行 = 不允许（fail closed：忘配置绝不等于全开）。
//
// 不做进程内缓存：与 capabilitypolicy / pluginpolicy 的闸同口径 —— 多实例部署下
// 缓存会让「刚关掉的开关」在一段时间里仍生效，而这个开关是安全相关的一票。
// /platform/market/* 是低频接口（拉目录、导包），一次点查换掉这份不一致是划算的。
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

// SetMarketOpen 写出口策略（单行 upsert），by 为操作者平台用户 id（可为空）。
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
