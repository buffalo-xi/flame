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

// 市场列表分页：一次最多 200 条，默认 50。
//
// 用「加载更多」而不是页码：顺序按 updated_at DESC，用户对「第 3 页是什么」没有预期，
// 页码只会让「找不到」变成「翻着找」。远端市场不做分页（对端目录是整包缓存，
// 分也省不了传输），那边用条数上限 + 搜索收敛。
const (
	marketPageDefault = 50
	marketPageMax     = 200
)

// queryInt 读 ?limit/?offset 这类整数参数：没传用默认值，非法也回默认，越界夹到区间。
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

// ListAssets 可安装列表：只列已通过审核、且对本租户可见的资产。
//
// 可见性收在 sharedassets.List 的 VisibleTo 里（EXISTS 子查询）：allowlist 模式下
// 「没有白名单行」必须等于「不可见」，写在 SQL 里才不会因为上层忘了判而漏出去。
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
	// Count 不拼 LIMIT：拿到的才是「筛选后的全量」，前端据此判断还有没有下一页
	total, err := sharedassets.Count(f)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("count assets", err), nil, "")
		return
	}
	f.Limit, f.Offset = limit, offset
	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query assets", err), nil, "")
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

	// 来源下拉的数据：忽略 origin 条件（其余条件照旧），筛了某个来源时其它选项不会消失
	counts, cerr := sharedassets.CountByOrigin(f)
	if cerr != nil {
		xhttp.ErrJSON(c, assetErrMsg("count origins", cerr), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":    items,
		"total":    total, // 筛选后的总数，不是本页条数
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
		"origins":  originOptions(counts),
	}, "")
}

// originFilter 把 ?origin= 翻译成过滤条件：
//
//	留空 = 全部来源；local = 只看本机上架；其它值 = 只看该对端站点导入的。
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

// originOptions 来源下拉选项：本机排第一，其余按名字排（条数来自 CountByOrigin）。
func originOptions(counts map[string]int) []gin.H {
	out := make([]gin.H, 0, len(counts))
	for server, n := range counts {
		label := server
		if server == "" {
			label = "本机上架"
		}
		out = append(out, gin.H{"server": server, "label": label, "count": n})
	}
	sort.Slice(out, func(i, j int) bool {
		si, _ := out[i]["server"].(string)
		sj, _ := out[j]["server"].(string)
		if (si == "") != (sj == "") {
			return si == "" // 本机永远在最前
		}
		return si < sj
	})
	return out
}

// errAssetNotFound 资产不存在。
//
// error 编码供前端翻译；params.id 只用于定位，不进翻译模板。
// msg 仍是中文原文 —— 日志、curl 与未升级的调用方看到的和以前一样。
func errAssetNotFound(c *gin.Context, id string) {
	xhttp.ErrCode(c, "asset.not_found", map[string]any{"id": id}, "资产不存在", "")
}

// GetAsset 资产详情（可安装范围）；含文件清单与依赖，供安装前确认。
func GetAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query asset", err), nil, "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	visible, verr := sharedassets.VisibleTo(a, info.Schema)
	if verr != nil {
		xhttp.ErrJSON(c, "check visibility: "+verr.Error(), nil, "")
		return
	}
	// 自己的（哪怕是草稿）能看，别人的必须已审核且可见。
	// 「自己」= 本机原创 + 本租户 schema：导入行的 source_schema 也可能与本机同名（对方也叫 t0），
	// 不能只凭 schema 就当成自己的 —— 否则草稿态的导入行能被本租户看到。
	mine := a.SourceSchema == info.Schema && a.OriginServer == ""
	if !mine && (!visible || a.Status != sharedassets.StatusListed) {
		xhttp.ErrCode(c, "asset.not_available", map[string]any{"id": a.ID},
			"该资产未对你所在的租户开放", "")
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

// ListMine 我上架的（含草稿 / 待审 / 被驳回），带驳回原因。
//
// OriginLocal 必须带上：导入行的 source_schema 记的是**对方那台机器上的租户**，
// 与本机同名 schema（都是 t0）撞上时，从别的站导入的资产会被当成「我上架的」列出来，
// 界面上显示成「我上架的东西，来源却是 aaa 站」。
func ListMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, err := sharedassets.List(sharedassets.Filter{SourceSchema: info.Schema, OriginLocal: true})
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query assets", err), nil, "")
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

// Withdraw 撤开上架：pending → draft（等改完再交）；draft → 直接删除（含快照目录）。
//
// 已通过的资产不允许自己撤下 —— 装出去的那些实体已经与快照无关，撤下只会让
// 「谁装过」的记录悬空，真要下架走平台侧 delisted。
func Withdraw(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query asset", err), nil, "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	// 导入行不是「我上架的」：source_schema 只是对方那台机器上的租户名，与本机撞名是巧合
	if a.SourceSchema != info.Schema || a.OriginServer != "" {
		xhttp.ErrCode(c, "asset.not_owner", map[string]any{"id": a.ID}, "只能撤回本租户上架的资产", "")
		return
	}

	switch a.Status {
	case sharedassets.StatusPending:
		if err := sharedassets.SetStatus(a.ID, sharedassets.StatusDraft); err != nil {
			xhttp.ErrJSON(c, assetErrMsg("withdraw", err), nil, "")
			return
		}
		xhttp.OkJSON(c, "Success", gin.H{"status": sharedassets.StatusDraft}, "")
	case sharedassets.StatusDraft:
		if err := sharedassets.Delete(a.ID); err != nil {
			xhttp.ErrJSON(c, assetErrMsg("delete asset", err), nil, "")
			return
		}
		// 行删掉了再删文件：反过来的话行还在、内容却没了，审核页会展示一个空资产
		if st, serr := getStorage(); serr == nil {
			_ = st.DeletePrefix(context.Background(), paths.PlatformAssetsSubdir+"/"+a.ID)
		}
		xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
	default:
		xhttp.ErrCode(c, "asset.invalid_status", map[string]any{"id": a.ID, "status": a.Status},
			"当前状态（"+a.Status+"）不能撤回：已通过审核的资产由平台下架", "")
	}
}

// DeleteMine DELETE /admin/share/mine/:id —— 删掉自己的一条上架（行 + 快照目录）。
//
// 与 Withdraw 的区别：Withdraw 是「撤回流转」（pending → draft，draft → 删除），
// 这里是「直接清掉」，草稿/待审/被驳回/已下架的都能删，省掉两步。
// listed 一律拒绝：它已经装到别人那里去过，删除会让安装记录悬空，先让平台下架。
func DeleteMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query asset", err), nil, "")
		return
	}
	if a == nil {
		errAssetNotFound(c, c.Param("id"))
		return
	}
	if a.SourceSchema != info.Schema || a.OriginServer != "" {
		xhttp.ErrCode(c, "asset.not_owner", map[string]any{"id": a.ID}, "只能删除本租户上架的资产", "")
		return
	}
	if a.Status == sharedassets.StatusListed {
		xhttp.ErrCode(c, "asset.invalid_status", map[string]any{"id": a.ID, "status": a.Status},
			"已通过审核的资产不能直接删除：它可能已经被别的租户装走，"+
				"请先让平台下架（delisted），再回来删除", "")
		return
	}
	if err := sharedassets.Delete(a.ID); err != nil {
		xhttp.ErrJSON(c, assetErrMsg("delete asset", err), nil, "")
		return
	}
	// 行删掉了再删文件：反过来的话行还在、内容却没了，审核页会展示一个空资产
	if st, serr := getStorage(); serr == nil {
		_ = st.DeletePrefix(context.Background(), paths.PlatformAssetsSubdir+"/"+a.ID)
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
}

// ClearMine DELETE /admin/share/mine —— 清空本租户所有「未通过审核」的上架。
//
// 保留 listed：那些资产可能已经被别的租户安装，删掉会让他们的安装记录悬空。
// 返回删了几条，界面据此提示「已清空 N 条（已通过的 N 条保留）」。
func ClearMine(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	ids, err := sharedassets.DeleteMineUnlisted(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("clear mine", err), nil, "")
		return
	}
	// 行删掉之后再清快照目录（顺序与 DeleteMine 一致）
	if st, serr := getStorage(); serr == nil {
		ctx := context.Background()
		for _, id := range ids {
			_ = st.DeletePrefix(ctx, paths.PlatformAssetsSubdir+"/"+id)
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": len(ids)}, "")
}

// ListInstalls 本租户的安装记录（谁、从哪个快照、装成了什么名字）。
func ListInstalls(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, err := sharedassets.InstallsBy(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("query installs", err), nil, "")
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
