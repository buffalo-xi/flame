package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"

	"flame/nexus/platform/auth"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// 跨租户资源分发的平台侧：审核 / 可见范围。
//
// 只做「治理」不做「搬运」：快照由租户侧接口写（admin/share），安装也在租户侧完成；
// 这里改的是状态机与白名单，因此平台侧不需要读任何租户 schema。
//
// 状态机（见 docs/asset-sharing.md §6）：
//
//	draft → pending → listed | rejected
//	listed → delisted
//
// P0 不分页：资产数量在百级，先把「审核得动」跑通；要分页时在
// sharedassets.Filter 上加 Limit/Offset（已支持）并补一个 Count。

// ListAssets GET /platform/assets?status=&kind=&q=&source=&origin=
//
// 默认按「待审优先、再按更新时间倒序」给资产列表用：没有 status 时把 pending 排在最前，
// 免得管理员在几十条里翻。
//
// origin=local 只看本机上架的行，其它值按对端站点名过滤；接入多个远端市场后，
// 资产列表里「这条是从哪台机器收进来的」必须能一眼分开。
func ListAssets(c *gin.Context) {
	originServer, originLocal := originFilter(c.Query("origin"))
	f := sharedassets.Filter{
		Status:       strings.TrimSpace(c.Query("status")),
		Kind:         strings.TrimSpace(c.Query("kind")),
		SourceSchema: strings.TrimSpace(c.Query("source")),
		Query:        strings.TrimSpace(c.Query("q")),
		OriginServer: originServer,
		OriginLocal:  originLocal,
	}
	list, err := sharedassets.List(f)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load assets: "+err.Error(), nil, "")
		return
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, platformAssetView(&list[i], false))
	}
	// 待审的排前面（稳定排序：同组内保持 updated_at 倒序）
	if strings.TrimSpace(c.Query("status")) == "" {
		pending := make([]gin.H, 0, len(items))
		rest := make([]gin.H, 0, len(items))
		for _, it := range items {
			if it["status"] == sharedassets.StatusPending {
				pending = append(pending, it)
				continue
			}
			rest = append(rest, it)
		}
		items = append(pending, rest...)
	}
	// 来源下拉的数据：忽略 origin 条件，其余条件照旧
	counts, cerr := sharedassets.CountByOrigin(f)
	if cerr != nil {
		xhttp.ErrJSON(c, "Failed to count origins: "+cerr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":   items,
		"total":   len(items),
		"origins": originOptions(counts),
	}, "")
}

// originFilter 把 ?origin= 翻译成过滤条件（留空 = 全部；local = 本机；其它 = 该对端站点）。
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

// originOptions 来源下拉选项：本机排第一，其余按名字排。
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

// GetAsset GET /platform/assets/:id
//
// 详情给审核用：依赖声明、文件清单、上架人、被几个租户装过、上一轮的审核备注。
func GetAsset(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}

	v := platformAssetView(a, true)
	installs, cerr := sharedassets.CountInstalls(a.ID)
	if cerr != nil {
		installs = 0 // 统计失败不该让详情页打不开
	}
	v["install_count"] = installs

	if a.Visibility == sharedassets.VisibilityAllowlist {
		if schemas, serr := sharedassets.VisibleSchemas(a.ID); serr == nil {
			v["allowlist"] = schemas
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"asset": v}, "")
}

type reviewRequest struct {
	Action string `json:"action"` // approve | reject | delist
	Note   string `json:"note"`
}

// ReviewAsset POST /platform/assets/:id/review
//
// 状态迁移在这里收口：approve/reject 只对 pending 生效，delist 只对 listed 生效。
// 不做「任意状态跳任意状态」—— 那会让 rejected 的资产被误上架、delisted 的又被 approve 回来，
// 审核记录就失去意义了。
func ReviewAsset(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	var req reviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}

	var next string
	switch strings.TrimSpace(req.Action) {
	case "approve":
		if a.Status != sharedassets.StatusPending {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"只有待审的资产可以通过（当前："+a.Status+"）", "")
			return
		}
		next = sharedassets.StatusListed
	case "reject":
		if a.Status != sharedassets.StatusPending {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"只有待审的资产可以驳回（当前："+a.Status+"）", "")
			return
		}
		next = sharedassets.StatusRejected
	case "delist":
		if a.Status != sharedassets.StatusListed {
			xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
				"只有已通过的资产可以下架（当前："+a.Status+"）", "")
			return
		}
		next = sharedassets.StatusDelisted
	default:
		xhttp.ErrCode(c, "asset.invalid_action", gin.H{"action": strings.TrimSpace(req.Action)},
			"未知动作（只支持 approve / reject / delist）", "")
		return
	}

	if err := sharedassets.Review(a.ID, next, strings.TrimSpace(req.Note), info.ID); err != nil {
		xhttp.ErrJSON(c, "review: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": a.ID, "status": next}, "")
}

type visibilityRequest struct {
	Visibility string   `json:"visibility"` // all | allowlist
	Schemas    []string `json:"schemas"`
}

// GetAssetVisibility GET /platform/assets/:id/visibility
func GetAssetVisibility(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}
	schemas, serr := sharedassets.VisibleSchemas(a.ID)
	if serr != nil {
		xhttp.ErrJSON(c, "Failed to load allowlist: "+serr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"visibility": a.Visibility,
		"schemas":    schemas,
	}, "")
}

// SetAssetVisibility PUT /platform/assets/:id/visibility
//
// allowlist 每次全量替换（前端就是「勾选框 → 保存」），不提供单条增删：
// 半套增量接口最容易出现「以为删了其实没删」的白名单。
func SetAssetVisibility(c *gin.Context) {
	var req visibilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	vis := strings.TrimSpace(req.Visibility)
	if vis != sharedassets.VisibilityAll && vis != sharedassets.VisibilityAllowlist {
		xhttp.ErrCode(c, "asset.invalid_visibility", gin.H{"visibility": vis},
			"visibility 只能是 all 或 allowlist", "")
		return
	}

	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}

	// 白名单里的租户必须真实存在：写进一个拼错的 schema 等于「看起来开放了，其实没人能装」
	for _, s := range req.Schemas {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, terr := tenantsrepo.Get(s)
		if terr != nil {
			xhttp.ErrJSON(c, "check tenant: "+terr.Error(), nil, "")
			return
		}
		if t == nil {
			xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": s}, "租户不存在："+s, "")
			return
		}
	}

	if err := sharedassets.SetVisibility(a.ID, vis, req.Schemas); err != nil {
		xhttp.ErrJSON(c, "set visibility: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"visibility": vis, "schemas": req.Schemas}, "")
}

// platformAssetView 平台侧资产视图（与租户侧同源，多带审核信息）。
func platformAssetView(a *sharedassets.Asset, withManifest bool) gin.H {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		_ = json.Unmarshal(a.Manifest, &m)
	}
	by := ""
	if a.ListedBy.Valid {
		by = a.ListedBy.String
	}
	// 导入行的 source_schema 是「对方那台机器上的租户」，本机有同名 schema 纯属巧合，
	// 拿它去查本机 tenants 会给出误导性的名字 —— 这类行统一用来源站点名展示。
	tenantName := sharedassets.TenantDisplayName(a.SourceSchema)
	if a.Imported() {
		tenantName = a.OriginServer
	}

	v := gin.H{
		"id":           a.ID,
		"kind":         a.Kind,
		"name":         a.Name,
		"display_name": a.DisplayName,
		"description":  a.Description,
		"build":        a.Build,
		"status":       a.Status,
		"visibility":   a.Visibility,
		"origin":       originView(a),
		"listed_by": gin.H{
			"tenant": gin.H{
				"schema":       a.SourceSchema,
				"display_name": tenantName,
			},
			"user": gin.H{"id": by, "display_name": a.ListedByName},
		},
		"entry":       m.Entry,
		"deps":        m.Deps,
		"file_count":  len(m.Files),
		"total_size":  m.TotalSize(),
		"review_note": a.ReviewNote,
		"created_at":  a.CreatedAt,
		"updated_at":  a.UpdatedAt,
	}
	if withManifest {
		v["manifest"] = m
	}
	return v
}

// DeleteAsset DELETE /platform/assets/:id
//
// 删除不要求先下架：下架是「撤出市场但留档」，删除是「彻底清掉」，两者是并列手段，
// 不是前后步骤 —— 在架的直接删同样不碰租户已装出去的副本（复制式语义）。
//
// 仍拦住租户侧还没走完的三种状态：
//   - pending / rejected 属于租户侧没走完的流程，替租户拍板不合适（要处置就是驳回）；
//   - draft 本来就由租户侧「清空未上架」自己收拾。
//
// 先删库再清快照目录：库是入口，目录清不掉只留垃圾（记日志，不回滚）。
// 白名单与安装记录随外键级联删除；已经装到租户里的实体不动 —— 那是租户的东西。
func DeleteAsset(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}
	switch a.Status {
	case sharedassets.StatusListed, sharedassets.StatusDelisted:
	default:
		xhttp.ErrCode(c, "asset.invalid_status", gin.H{"id": a.ID, "status": a.Status},
			"只有已上架或已下架的资产可以删除（当前状态："+a.Status+"）", "")
		return
	}

	if derr := sharedassets.Delete(a.ID); derr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "删除失败: "+derr.Error(), "")
		return
	}

	if store, serr := tenantStorage(); serr != nil {
		xlog.Error("[platform] 资产 %s 已删除，但存储不可用、快照未清理: %v", a.ID, serr)
	} else if perr := store.DeletePrefix(context.Background(), path.Join(paths.PlatformAssetsSubdir, a.ID)); perr != nil {
		xlog.Error("[platform] 资产 %s 已删除，但清理快照目录失败: %v", a.ID, perr)
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": a.ID}, "")
}
