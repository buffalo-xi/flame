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

// 租户侧的「远端市场」：平台配好对端 + 可见范围，租户在自己的资产市场里就能看到对方的目录，
// 点安装那一刻才真正拉包（pull-through）。
//
// 三条硬约束：
//  1. 令牌只在服务端：租户永远拿不到 `flmt_…`，浏览器也绝不直连对端（否则等于把整站只读令牌发出去）；
//  2. 只能点平台配好的源：请求里带的是 peer 名，不是 URL —— 没有 SSRF 面；
//  3. 拉下来的东西一样过审核闸：install_policy=review 时导入为 pending，平台通过后租户才能装。
//
// 目录带 60s 缓存：N 个租户刷市场不该变成 N 次对端请求；对端不可达时返回该 peer 的 error，
// 不影响本机资产与其它 peer（远端挂了不能连累本机市场）。

const (
	remoteCatalogTTL = 60 * time.Second
	remoteCatalogMax = 4 << 20
	// remotePackageMax 对端资产包上限：与本地导入 / 上传同一口径（128 MiB 压缩包）。
	remotePackageMax  = 128 << 20
	remoteHTTPTimeout = 20 * time.Second

	// remoteCatalogItemLimit 单个对端一次最多回多少条（过滤之后、截断之前先数总数）。
	// 远端市场不做分页：对端目录整包缓存，分页省不了传输，只会把「找不到」变成「翻页找」。
	// 上限 + 搜索才是这个页面的正确收敛方式。500 条约 1~2 MB JSON，仍在目录 4 MiB 的预算内。
	remoteCatalogItemLimit = 500
)

var remoteClient = &http.Client{Timeout: remoteHTTPTimeout}

// catalogItem 对端目录的一条（字段与 /platform/market/catalog 对齐）。
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
	mu     sync.Mutex // 每个 peer 一把：慢对端只堵它自己
	server string
	items  []catalogItem
	err    string
	at     time.Time
	loaded bool
}

var remoteCatalogCache = struct {
	mu   sync.Mutex
	data map[string]*catalogCacheEntry // key = peer id（一次拉全量，kind 由上层筛）
}{data: make(map[string]*catalogCacheEntry, 4)}

// peerCatalog 取对端目录（带缓存）。失败也缓存：对端不可达时，N 个租户刷新只打一次连接。
//
// 锁的粒度按 peer 分：**不**持全局锁去发 HTTP —— 否则一个 20s 超时的死对端会把所有
// 租户的远端市场一起卡住。同 peer 并发进来只有第一个真的回源，其余等它拿到结果。
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
		e.err = "对端地址不合法：" + err.Error()
		return e
	}
	// 对端市场公开（peer.auth_mode=none）时不带 Authorization：
	// 带着一笔过期的令牌去敲一个已开 market.open 的对端，只会换回 401。
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		e.err = "连接对端失败：" + err.Error()
		return e
	}
	defer func() { _ = resp.Body.Close() }()

	// 多读一个字节：读满上限说明响应被截断，直接报「目录太大」，
	// 否则半截 JSON 会在下面被解成「格式不对」，把排查方向带偏
	body, _ := io.ReadAll(io.LimitReader(resp.Body, remoteCatalogMax+1))
	if resp.StatusCode != http.StatusOK {
		e.err = fmt.Sprintf("对端返回 %d：%s", resp.StatusCode, truncate(string(body), 200))
		return e
	}
	if len(body) > remoteCatalogMax {
		e.err = fmt.Sprintf(
			"对端目录超过 %d MiB 上限，响应已被截断（不是格式问题）。请在对端减少已上架资产，或拆分为多个站点",
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
		e.err = "对端响应不是合法 JSON（快照过旧或地址填错？）"
		return e
	}
	// 对端自报名（站点名即身份）：只做展示与排查用，不拿它拦配置。
	// 「对端指向本机」目前**允许**（单机自测最省事）：不会自增殖，因为出口只出本机原创
	// （见 platform/handler/market.go 的出口口径 2）—— 从自己拉回来的那条 origin 非空，
	// 不会再出现在出口目录里，一轮就收敛。若哪天放开「转发导入行」，这里必须重新加拦截。
	e.server = payload.Data.Server
	e.items = payload.Data.Items
	return e
}

// invalidatePeerCatalog 清掉该 peer 的缓存（拉取后 / 配置改了要立刻生效，不能等 60s）。
func invalidatePeerCatalog(peerID string) {
	remoteCatalogCache.mu.Lock()
	delete(remoteCatalogCache.data, peerID)
	remoteCatalogCache.mu.Unlock()
}

// displayPeerURL 对端地址的展示形态：保留 scheme://host[:port][/path]，
// 剥掉 userinfo / query / fragment，去掉末尾斜杠。
//
// 为什么剥：base_url 里可能内嵌凭据（https://u:p@host）或带查询串，
// 那些不属于「市场在哪」这个信息，展示出去只是多一份泄露面。
// 解析失败时原样返回 —— 这是展示字段，不该因为脏数据把整页打成错误。
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
// 只返回**对本租户开放**的对端（platform.peer_servers.visible_to + enabled）。
// 每个 peer 独立给 error，前端可以「test 站显示 N 条，staging 站暂时不可达」并存。
//
// 不做分页：对端目录是整包拿回来缓存的（见 remoteCatalogTTL），分页只能省渲染、
// 省不了传输，反而让「找东西」变成「翻页找」。所以这里用条数上限 + 搜索收敛，
// 并且**先过滤再截断** —— 否则搜「essay」时命中项如果排在第 300 条就永远搜不到。
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
		xhttp.ErrCode(c, "common.internal", nil, "查询远端市场失败: "+err.Error(), "")
		return
	}

	installed := sharedassets.InstalledBuilds(info.Schema)
	// 一次查出本机所有「从对端导入」的行，按 站点 → 对方 asset id 建索引：
	// 决定按钮是「安装」还是「已拉取 / 等审核」，同时给出本地快照（判断 stale）。
	// 不按 peer 循环查：peers 多了会变成 N 次全表扫描。
	localAll, lerr := sharedassets.List(sharedassets.Filter{})
	if lerr != nil {
		xhttp.ErrJSON(c, assetErrMsg("query imported assets", lerr), nil, "")
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

	// 并发拉各站目录：串行时首屏要等「所有站之和」，其中任一慢站（20s 超时）
	// 都会拖住整页，明明其它站早就好了。peerCatalog 的锁是 per-peer 的
	// （全局锁只护 map 取指针），所以并发安全，且同一站并发进来仍只回源一次。
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
			// url 只用于租户端标注「这个站在哪」：同名站点、连不通排查时需要它。
			// 两条边界不变：
			//   - 绝不带令牌（硬约束 1，令牌只在服务端）；
			//   - 请求仍然只传 peer 名、不传 URL，所以没有 SSRF 面（硬约束 2）——
			//     这里下发的是只读展示值，不参与任何出站请求的构造。
			"url":            displayPeerURL(p.BaseURL),
			"install_policy": p.InstallPolicy,
			"items":          []gin.H{},
			"count":          0, // 匹配总数（过滤后、截断前）
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
			matched++ // 照数：count 是「匹配总数」，与截断无关
			if len(items) >= remoteCatalogItemLimit {
				continue
			}
			// 目录字段与本地 assetView 保持同名，前端可以用同一套渲染
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
				// installed 看的是「本租户装过没有」，快照号给前端判断「装了旧版」
				if v, has := installed[a.ID]; has {
					h["installed"] = true
					h["installed_build"] = v
				}
				if it.Build > a.Build {
					h["stale"] = true // 远端有更新快照，本地还是旧的
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

// remoteMatch 目录项的关键字匹配（名字 / 展示名 / 描述，大小写不敏感）。
//
// 在截断之前调用：命中项排在第 300 条时也要能搜出来，否则「搜到了却没显示」最难解释。
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
// 按需拉取：把远端的一条资产拉成本机的平台资产行（共享快照），然后前端再调 /share/install 装到租户。
// 拆成两步是刻意的：拉取是按「资产」去重的（多个租户装同一份只下载一次），安装才是按「租户」的。
//
// 返回里的 status 决定前端怎么走：
//
//	listed → 可以立刻 install
//	pending   → 已提交平台审核，等通过（install_policy=review 的默认路径）
func PullAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var req pullRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	peerName := strings.TrimSpace(req.Peer)
	assetID := strings.TrimSpace(req.AssetID)
	if peerName == "" || assetID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "请提供来源站点与资产 id", "")
		return
	}

	// 只能点平台配好的源：名字 → 配置，URL 不来自请求体（没有 SSRF 面）
	list, err := peers.ListPeers()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "查询远端市场失败: "+err.Error(), "")
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
			"该远端市场未对你所在的租户开放", "")
		return
	}

	// 快路径：本机已经有这一条（别的租户或平台预先拉过）→ 不再下载。
	// Force=true 时跳过快路径：调用方明确要「拉新快照」，覆盖与否交给下面的 Import 判定
	// （它会按同名规则找既有行并复用 id，快照单调 +1）。
	if existing, ferr := sharedassets.FindByOriginAsset(peer.Name, assetID); ferr != nil {
		xhttp.ErrJSON(c, assetErrMsg("query imported asset", ferr), nil, "")
		return
	} else if existing != nil && !req.Force {
		xhttp.OkJSON(c, "Success", pullResultView(existing, false), "")
		return
	}

	data, derr := fetchPeerPackage(peer, assetID)
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
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
			// 对方 asset id 与本机记录一致：并发或历史残留，直接回既有行
			if e, ferr := sharedassets.FindByOriginAsset(peer.Name, assetID); ferr == nil && e != nil {
				xhttp.OkJSON(c, "Success", pullResultView(e, false), "")
				return
			}
			// 同名但来源 id 不同：多半是对方「撤回后重新上架」过，派生出新 id。
			// ConflictError 只陈述事实（来源 / 名字 / 快照号），平台导入侧是自己弹确认框；
			// 租户市场的按钮语义不一样，所以这里自己拼一句「需要覆盖才能继续」，
			// 连同结构化 conflict 一起回，前端问一次再带 force 重试。
			xhttp.ErrCodeData(c, "asset.name_conflict",
				map[string]any{"name": ce.Name, "build": ce.ExistingBuild},
				fmt.Sprintf("本机已有同名资产「%s」（v%d），需要覆盖才能继续", ce.Name, ce.ExistingBuild),
				gin.H{"conflict": gin.H{
					"name":     ce.Name,
					"build":    ce.ExistingBuild,
					"status":   ce.ExistingStatus,
					"asset_id": ce.ExistingID,
					"peer":     peer.Name,
				}}, "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "拉取失败: "+ierr.Error(), "")
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

// pullResultView 拉取结果：前端据此决定「继续安装」还是「提示等审核」。
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

// fetchPeerPackage 从对端下载 zip（走服务端，令牌不出平台）。
func fetchPeerPackage(p *peers.Peer, assetID string) ([]byte, error) {
	url := strings.TrimRight(p.BaseURL, "/") + "/platform/market/assets/" + assetID + "/package"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("对端地址不合法: %w", err)
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接对端失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("对端返回 %d：%s", resp.StatusCode, truncate(string(body), 200))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, remotePackageMax+1))
	if err != nil {
		return nil, fmt.Errorf("读取对端资产包失败: %w", err)
	}
	if len(data) > remotePackageMax {
		return nil, fmt.Errorf("对端的包超过上限（%d MiB）", remotePackageMax>>20)
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
