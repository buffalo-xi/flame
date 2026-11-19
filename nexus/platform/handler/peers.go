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

// 跨服务器市场的两侧配置（仅平台管理员）：
//
//	/platform/peers*        —— 我去拉的那一端（地址 + 出站 token）
//	/platform/market-tokens* —— 允许谁来拉我（只存 sha256，明文只在创建时回一次）
//
// 拉取流程刻意分两步：先看目录（PeerCatalog），再逐条导入（ImportFromPeer）。
// 批量拉取会把「对面发了一堆东西」变成一个不可撤销的动作，而逐个导入每次都能看清 warnings。

const (
	peerHTTPTimeout = 30 * time.Second

	// peerCatalogMax 目录响应体上限（与租户侧 remoteCatalogMax 同量级）。
	// 必须配「多读一个字节再判超限」：io.ReadAll(io.LimitReader(...)) 读满上限就 EOF、
	// err 仍为 nil，于是半截 JSON 进 Unmarshal，把「目录太大」说成「响应不合法」。
	peerCatalogMax = 4 << 20
)

var peerClient = &http.Client{Timeout: peerHTTPTimeout}

var (
	tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// schemaNameRe 与 platform.tenants 的 CHECK 同口径（对端可见名单里存的是租户 schema）
	schemaNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	// originRe 来源站点名（进 varchar(64)，同时参与唯一键，格式先收一道）
	originRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// schemaErrMsg 把「表/列缺失」翻成一句能照做的话。
//
// platform 下的建表/迁移脚本是手工执行的（见 platform/README.md），表结构落后于代码时
// 报的是 pq 原文（column "visible_to" does not exist），第一次部署的人只能去翻文档。
// 42703 = undefined_column，42P01 = undefined_table。
func schemaErrMsg(prefix, script string, err error) string {
	if err == nil {
		return prefix
	}
	var pe *pq.Error
	if errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "42703") {
		return prefix + ": " + err.Error() + "（表结构落后于代码，请执行 nexus/platform/sql/" + script + "）"
	}
	return prefix + ": " + err.Error()
}

// peerView 对端视图：不回 token 明文（列表刷新一次漏一次），只回「有没有配」。
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
		// auth_mode 是推导值（token 空 = none），前端据此回填下拉；
		// 保留 has_token 是因为老前端只认它。
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
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询对端失败", "peer_market.sql", err), "")
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
	AuthMode      string   `json:"auth_mode"` // token（默认）| none
	Enabled       *bool    `json:"enabled"`
	VisibleTo     string   `json:"visible_to"`     // all | allowlist
	TenantSchemas []string `json:"tenant_schemas"` // allowlist 名单
	InstallPolicy string   `json:"install_policy"` // review | auto
}

func (r *peerRequest) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	r.Token = strings.TrimSpace(r.Token)
	if !originRe.MatchString(r.Name) {
		return errors.New("站点名只允许字母、数字、点、下划线、横线，长度 ≤64")
	}
	if r.BaseURL == "" {
		return errors.New("请填写对端地址（例如 https://test.example.com）")
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("对端地址必须是 http(s):// 开头且带主机名")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("对端地址不要带路径（只填到主机名/端口，/platform 会自动拼）")
	}

	// 认证方式：对端市场可以公开（market.open: true），这时不需要令牌。
	// 不传 auth_mode 时按「有没有令牌」推断，兼容只传 token 的旧调用方。
	r.AuthMode = strings.TrimSpace(r.AuthMode)
	switch r.AuthMode {
	case "":
		if r.Token == "" {
			r.AuthMode = peers.AuthModeNone
		} else {
			r.AuthMode = peers.AuthModeToken
		}
	case peers.AuthModeNone:
		// 公开市场：不留残令牌。留着的旧值会在对端重新开启令牌校验时变成一个
		// 「看起来配过、其实早就该删」的凭据。
		r.Token = ""
	case peers.AuthModeToken:
		if r.Token == "" {
			return errors.New("选择「市场令牌」时必须填对端给你的 flmt_… 令牌；对端市场公开时请选「公开（不带令牌）」")
		}
	default:
		return errors.New("认证方式只能是 token（市场令牌）或 none（公开市场）")
	}

	// 可见范围：all = 所有租户都能在自己的资产市场里看到这个远端；allowlist = 只给名单里的
	r.VisibleTo = strings.TrimSpace(r.VisibleTo)
	if r.VisibleTo == "" {
		r.VisibleTo = peers.VisibleAll
	}
	if r.VisibleTo != peers.VisibleAll && r.VisibleTo != peers.VisibleAllowlist {
		return errors.New("可见范围只能是 all（所有租户）或 allowlist（指定租户）")
	}
	schemas := make([]string, 0, len(r.TenantSchemas))
	for _, s := range r.TenantSchemas {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		if !schemaNameRe.MatchString(v) {
			return fmt.Errorf("租户 schema 不合法：%s", v)
		}
		schemas = append(schemas, v)
	}
	if r.VisibleTo == peers.VisibleAllowlist && len(schemas) == 0 {
		return errors.New("选择「指定租户」时至少要勾一个租户，否则谁都看不到这个市场")
	}
	r.TenantSchemas = schemas

	// 安装策略：review = 拉下来待审（默认）；auto = 直接装到发起租户
	r.InstallPolicy = strings.TrimSpace(r.InstallPolicy)
	if r.InstallPolicy == "" {
		r.InstallPolicy = peers.InstallPolicyReview
	}
	if r.InstallPolicy != peers.InstallPolicyReview && r.InstallPolicy != peers.InstallPolicyAuto {
		return errors.New("安装策略只能是 review（先审核）或 auto（直接安装）")
	}
	return nil
}

// CreatePeer POST /platform/peers
func CreatePeer(c *gin.Context) {
	var req peerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "参数不合法: "+err.Error(), "")
		return
	}
	if err := req.normalize(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	exist, err := peers.ExistsPeerName(req.Name, "")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询对端失败", "peer_market.sql", err), "")
		return
	}
	if exist {
		xhttp.ErrCode(c, "peer.name_taken", gin.H{"name": req.Name}, "同名对端已存在："+req.Name, "")
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
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("新建对端失败", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"peer": peerView(p)}, "")
}

// UpdatePeer PUT /platform/peers/:id
func UpdatePeer(c *gin.Context) {
	p, err := peers.GetPeer(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询对端失败", "peer_market.sql", err), "")
		return
	}
	if p == nil {
		xhttp.ErrCode(c, "peer.not_found", gin.H{"id": c.Param("id")}, "对端不存在", "")
		return
	}
	var req peerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "参数不合法: "+err.Error(), "")
		return
	}
	// 认证方式优先于「留空 = 不改」：
	//   auth_mode=none → 明确切成公开市场，把旧令牌清掉（要能主动删，否则切不回去）；
	//   其余（含不传 auth_mode）→ 令牌留空表示不改，沿用旧值。
	if strings.TrimSpace(req.AuthMode) != peers.AuthModeNone && strings.TrimSpace(req.Token) == "" {
		req.Token = p.Token
	}
	if err := req.normalize(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	exist, err := peers.ExistsPeerName(req.Name, p.ID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询对端失败", "peer_market.sql", err), "")
		return
	}
	if exist {
		xhttp.ErrCode(c, "peer.name_taken", gin.H{"name": req.Name}, "同名对端已存在："+req.Name, "")
		return
	}
	p.Name, p.BaseURL, p.Token = req.Name, req.BaseURL, req.Token
	p.VisibleTo, p.Tenants, p.InstallPolicy = req.VisibleTo, req.TenantSchemas, req.InstallPolicy
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := peers.UpdatePeer(p); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("更新对端失败", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"peer": peerView(p)}, "")
}

// DeletePeer DELETE /platform/peers/:id
func DeletePeer(c *gin.Context) {
	if err := peers.DeletePeer(c.Param("id")); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("删除对端失败", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{}, "")
}

// PeerCatalog GET /platform/peers/:id/catalog?kind=
//
// 代理对端的只读目录，并标注「本机是否已经从该站点导入过 / 本地快照」——
// 前端据此决定按钮是「导入」还是「覆盖导入」。
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
		xhttp.ErrCode(c, "peer.unreachable", gin.H{"peer": p.Name}, "连接对端失败: "+err.Error(), "")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// 多读一个字节：读满上限说明响应被截断，直接报「目录太大」而不是让 JSON 解码去背锅
	body, _ := io.ReadAll(io.LimitReader(resp.Body, peerCatalogMax+1))
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("对端返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}
	if len(body) > peerCatalogMax {
		msg := fmt.Sprintf(
			"对端目录超过 %d MiB 上限，响应已被截断（不是格式问题）。请在对端减少已上架资产，或按类型分批拉取（?kind=skill / custom_tool / agent / workflow）",
			peerCatalogMax>>20,
		)
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}

	var payload struct {
		Data struct {
			Server string           `json:"server"`
			Items  []map[string]any `json:"items"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(body, &payload); jerr != nil {
		_ = peers.TouchPeer(p.ID, "对端响应不是合法 JSON")
		xhttp.ErrCodeData(c, "peer.invalid_response", gin.H{"peer": p.Name},
			"对端响应不是合法 JSON（对端快照过旧或地址填错？）", body, "")
		return
	}

	// 本机已从该站点导入过的资产：origin_asset_id → 本地快照
	imported, err := sharedassets.List(sharedassets.Filter{OriginServer: p.Name})
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "查询本机导入失败: "+err.Error(), "")
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
// 拉包 → 走与人工上传完全相同的导入管线（校验/脱敏/落盘/写库）。
// auto_approve 的对端才直接 listed，否则一律 pending。
func ImportFromPeer(c *gin.Context) {
	p, ok := mustPeer(c)
	if !ok {
		return
	}
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	var req struct {
		AssetID string `json:"asset_id"`
		Force   bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AssetID) == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "请提供要导入的资产 id", "")
		return
	}

	resp, err := peerGet(p, "/market/assets/"+url.PathEscape(req.AssetID)+"/package")
	if err != nil {
		_ = peers.TouchPeer(p.ID, err.Error())
		xhttp.ErrCode(c, "peer.unreachable", gin.H{"peer": p.Name}, "连接对端失败: "+err.Error(), "")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("对端返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))
		_ = peers.TouchPeer(p.ID, msg)
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}

	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPackageUpload+1))
	if rerr != nil {
		xhttp.ErrCode(c, "peer.invalid_response", gin.H{"peer": p.Name}, "读取对端资产包失败: "+rerr.Error(), "")
		return
	}
	if len(data) > maxPackageUpload {
		xhttp.ErrCode(c, "asset.package_too_large", gin.H{"limit_mb": maxPackageUpload >> 20},
			fmt.Sprintf("对端的包超过上限（%d MiB）", maxPackageUpload>>20), "")
		return
	}

	st, serr := packageStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
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
			xhttp.ErrJSON(c, ce.Error(), gin.H{"existing": gin.H{
				"id": ce.ExistingID, "build": ce.ExistingBuild, "status": ce.ExistingStatus,
			}}, "")
			return
		}
		xhttp.ErrJSON(c, ierr.Error(), nil, "")
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
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询对端失败", "peer_market.sql", err), "")
		return nil, false
	}
	if p == nil {
		xhttp.ErrCode(c, "peer.not_found", gin.H{"id": c.Param("id")}, "对端不存在", "")
		return nil, false
	}
	if !p.Enabled {
		xhttp.ErrCode(c, "peer.disabled", gin.H{"name": p.Name}, "对端已停用："+p.Name, "")
		return nil, false
	}
	return p, true
}

// peerGet 向对端发一个只读请求（地址带 /platform 前缀，令牌走 Authorization）。
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

// ===== 市场出口策略（要不要校验令牌） =====

// GetMarketSettings GET /platform/market-settings
//
// 没有行 = 要求令牌（open=false）：这是 fail-closed 的默认值，不靠种子数据物化。
func GetMarketSettings(c *gin.Context) {
	s, err := peers.GetMarketSetting()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询市场设置失败", "peer_market.sql", err), "")
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
// 打开 = 任何人拿到本机地址就能读已通过审核的目录与资产包。这是治理决定，
// 由平台管理员在管理端做，不走配置文件（写文件意味着要登机器改 + 重启）。
// 带令牌但无效的请求仍然 401 —— 开放不等于不校验。
func SetMarketSettings(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	var req struct {
		Open *bool `json:"open"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Open == nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "参数不合法: body 需要 {\"open\": true|false}", "")
		return
	}
	if err := peers.SetMarketOpen(*req.Open, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("保存市场设置失败", "peer_market.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"open": *req.Open}, "")
}

// ===== 市场出口令牌 =====

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
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("查询市场令牌失败", "peer_market.sql", err), "")
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
// 明文令牌**只在这里返回一次**，库里只有 sha256；丢了就删掉重建。
func CreateMarketToken(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "参数不合法: "+err.Error(), "")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !tokenNameRe.MatchString(req.Name) {
		xhttp.ErrCode(c, "peer.invalid_token_name", nil, "令牌名只允许字母、数字、点、下划线、横线，长度 ≤64", "")
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "生成令牌失败: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "新建市场令牌失败: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "参数不合法: "+err.Error(), "")
		return
	}
	if err := peers.SetMarketTokenEnabled(c.Param("id"), req.Enabled); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "更新市场令牌失败: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"enabled": req.Enabled}, "")
}

// DeleteMarketToken DELETE /platform/market-tokens/:id
func DeleteMarketToken(c *gin.Context) {
	if err := peers.DeleteMarketToken(c.Param("id")); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "删除市场令牌失败: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{}, "")
}
