package handler

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"flame/nexus/platform/auth"
	"flame/nexus/shared/listingpolicy"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// 上架审核策略的平台侧读写。
//
// 只影响「租户上架资产」是否要过审核；从远端市场拉取的审核看
// peer_servers.install_policy（platform/handler/peers.go），两码事。

// GetListingPolicy GET /platform/listing-policy
//
// 回全局默认 + 所有租户覆盖。前端按「跟随默认 / 强制审核 / 免审」三态渲染：
// overrides 里没有的租户就是「跟随默认」（不存 inherit 值，避免自相矛盾的行）。
func GetListingPolicy(c *gin.Context) {
	def, err := listingpolicy.Default()
	if err != nil {
		xhttp.ErrJSON(c, schemaErrMsg("查询上架策略失败", "listing_policy.sql", err), nil, "")
		return
	}
	overrides, err := listingpolicy.ListOverrides()
	if err != nil {
		xhttp.ErrJSON(c, schemaErrMsg("查询上架策略失败", "listing_policy.sql", err), nil, "")
		return
	}
	items := make([]gin.H, 0, len(overrides))
	for schema, require := range overrides {
		items = append(items, gin.H{"schema_name": schema, "require_review": require})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["schema_name"].(string) < items[j]["schema_name"].(string)
	})
	xhttp.OkJSON(c, "Success", gin.H{"require_review": def, "overrides": items}, "")
}

// SetListingDefault PUT /platform/listing-policy  body: {require_review: bool}
//
// false = 所有租户上架后直接可见（不推荐），true = 一律进待审。
// 改这个不会追溯已经上架的内容，只影响之后的上架。
func SetListingDefault(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	var req struct {
		RequireReview *bool `json:"require_review"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RequireReview == nil {
		xhttp.ErrJSON(c, "参数不合法: body 需要 {\"require_review\": true|false}", nil, "")
		return
	}
	if err := listingpolicy.SetDefault(*req.RequireReview, info.ID); err != nil {
		xhttp.ErrJSON(c, schemaErrMsg("保存上架策略失败", "listing_policy.sql", err), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"require_review": *req.RequireReview}, "")
}

// SetListingOverride PUT /platform/listing-policy/:schema  body: {require_review: bool|null}
//
// null（或字段缺失）= 删除该租户的覆盖，回到全局默认。
func SetListingOverride(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}
	schema := strings.TrimSpace(c.Param("schema"))
	if schema == "" {
		xhttp.ErrJSON(c, "缺少租户 schema", nil, "")
		return
	}
	var req struct {
		RequireReview *bool `json:"require_review"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "参数不合法: body 需要 {\"require_review\": true|false|null}", nil, "")
		return
	}
	if err := listingpolicy.SetOverride(schema, req.RequireReview, info.ID); err != nil {
		// 外键指向 platform.tenants：租户不存在时给出可读的话，别把约束名甩给用户
		var pe *pq.Error
		if errors.As(err, &pe) && pe.Code == "23503" {
			xhttp.ErrJSON(c, "租户不存在："+schema, nil, "")
			return
		}
		xhttp.ErrJSON(c, schemaErrMsg("保存上架策略失败", "listing_policy.sql", err), nil, "")
		return
	}
	var out any
	if req.RequireReview != nil {
		out = *req.RequireReview
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema_name": schema, "require_review": out}, "")
}
