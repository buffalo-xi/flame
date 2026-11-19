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

// 对端市场（站到站只读）。挂在 /platform/market 下，由市场令牌（market_tokens）鉴权，
// 不是平台登录身份 —— 对端只是一台机器，不该拿到能改配置的凭据。
//
// 出口口径两条：
//  1. **只出 listed**：pending / rejected / delisted 一律不出现，所以「本机审核通过」
//     就是对端可见的唯一前提；allowlist 是租户维度，对端不受它约束（对端拉回去之后
//     还要在它自己的平台上再审一次、再按它自己的租户可见范围安装）。
//  2. **只出本机原创**（origin_server = ''）：导入行不再往外转发。
//     否则 A→B、B→A 会形成环路：每次导入生成一条新行（origin 变了但仍 listed），
//     又出现在出口目录里，同一份东西被反复复制；来源也会被改名冒充（B 站的转发件挂在
//     A 站名下），`origin_server` 这条溯源链直接断掉。
//     对端如果要这份资产，应当去原始站点拉。

// ListMarketCatalog GET /platform/market/catalog?kind=&since=
//
// since 传 RFC3339 时只回该时间之后更新过的，用于增量同步；拿不到就全量（资产在百级）。
func ListMarketCatalog(c *gin.Context) {
	f := sharedassets.Filter{
		Status:      sharedassets.StatusListed,
		Kind:        strings.TrimSpace(c.Query("kind")),
		OriginLocal: true, // 只出本机上架的行，见上面的出口口径 2
	}

	var since time.Time
	if s := strings.TrimSpace(c.Query("since")); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			xhttp.ErrCode(c, "common.invalid_params", gin.H{"param": "since"},
				"since 需要 RFC3339 时间（如 2026-11-02T10:00:00Z）", "")
			return
		}
		since = t
	}

	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "查询市场目录失败: "+err.Error(), "")
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
// 只能拉已通过审核的**本机原创**资产：目录里看不到的 id 这里也不给包，两条口子口径一致。
// 导入行（origin_server 非空）同样 404 —— 它出不了目录，也就不该能按 id 直取；
// 否则「只出原创」这层限制会被一个猜到的 id 绕过。
func MarketAssetPackage(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "查询资产失败: "+err.Error(), "")
		return
	}
	if a == nil || a.Status != sharedassets.StatusListed || a.OriginServer != "" {
		xhttp.SendErrorResponse(c, http.StatusNotFound, "not_found", "资产不存在或未通过审核")
		return
	}
	data, berr := buildAssetPackage(a)
	if berr != nil {
		xhttp.ErrJSON(c, berr.Error(), nil, "")
		return
	}
	servePackage(c, a, data)
}
