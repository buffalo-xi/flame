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

// Platform-side read/write of the listing review policy.
//
// It only governs whether a tenant listing needs review; review of pulls from a remote market
// follows peer_servers.install_policy (platform/handler/peers.go) - a different thing.

// GetListingPolicy GET /platform/listing-policy
//
// Returns the global default plus every tenant override. The frontend renders three states
// "follow default / force review / no review": a tenant absent from overrides follows the default
// (no inherit value is stored, so no self-contradicting rows).
func GetListingPolicy(c *gin.Context) {
	def, err := listingpolicy.Default()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query the listing policy", "listing_policy.sql", err), "")
		return
	}
	overrides, err := listingpolicy.ListOverrides()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to query the listing policy", "listing_policy.sql", err), "")
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
// false = a listing from any tenant is visible right after listing (not recommended), true = every
// listing waits for review. Changing it does not apply retroactively, only to later listings.
func SetListingDefault(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	var req struct {
		RequireReview *bool `json:"require_review"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RequireReview == nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: the body must be {\"require_review\": true|false}", "")
		return
	}
	if err := listingpolicy.SetDefault(*req.RequireReview, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to save the listing policy", "listing_policy.sql", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"require_review": *req.RequireReview}, "")
}

// SetListingOverride PUT /platform/listing-policy/:schema  body: {require_review: bool|null}
//
// null (or a missing field) deletes that tenant's override and returns to the global default.
func SetListingOverride(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}
	schema := strings.TrimSpace(c.Param("schema"))
	if schema == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "missing tenant schema", "")
		return
	}
	var req struct {
		RequireReview *bool `json:"require_review"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid parameters: the body must be {\"require_review\": true|false|null}", "")
		return
	}
	if err := listingpolicy.SetOverride(schema, req.RequireReview, info.ID); err != nil {
		// The foreign key points at platform.tenants: give a readable message when the tenant is
		// missing instead of throwing the constraint name at the user
		var pe *pq.Error
		if errors.As(err, &pe) && pe.Code == "23503" {
			xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found: "+schema, "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, schemaErrMsg("failed to save the listing policy", "listing_policy.sql", err), "")
		return
	}
	var out any
	if req.RequireReview != nil {
		out = *req.RequireReview
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema_name": schema, "require_review": out}, "")
}
