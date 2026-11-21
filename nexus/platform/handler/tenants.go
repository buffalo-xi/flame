package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"flame/nexus/platform/auth"
	"flame/nexus/platform/provision"
	"flame/nexus/platform/repository/pmusers"
	tenantsrepo "flame/nexus/platform/repository/tenants"
	"flame/nexus/shared/agentconfig"
	"flame/nexus/shared/config"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/storage"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// ListTenants GET /platform/tenants?q=&status=&page=&page_size=
//
// Admin view: every tenant plus its effective quota and current usage.
//
// Paging: page starts at 1; an omitted / <=0 page_size means **no paging** (return everything, for
// older callers), capped at 100
// - each row runs three usage COUNTs, so a larger page_size means more database load.
// total is the number of rows matching the filter (not the size of this page); the frontend pager uses it.
func ListTenants(c *gin.Context) {
	q, status := c.Query("q"), c.Query("status")
	page, pageSize := parsePaging(c)

	list, err := tenantsrepo.List(status, q, pageSize, (page-1)*pageSize)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to list tenants: "+err.Error(), "")
		return
	}

	total := len(list)
	if pageSize > 0 {
		n, cerr := tenantsrepo.Count(status, q)
		if cerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to count tenants: "+cerr.Error(), "")
			return
		}
		total = n
	}

	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, tenantView(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}, "")
}

// parsePaging parses page / page_size: page defaults to 1 (minimum 1), page_size defaults to 0, which
// means no paging, capped at 100.
func parsePaging(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.Query("page_size"))
	if pageSize < 0 {
		pageSize = 0
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

type createTenantRequest struct {
	// An empty Schema means the backend picks one (the console does not allow typing it, see GET /platform/tenant-schemas/next)
	Schema      string `json:"schema"`
	DisplayName string `json:"display_name"`
	Plan        string `json:"plan"`
	// OwnerUserID is the owner (a platform "user" id); empty means administrated by an admin
	OwnerUserID string `json:"owner_user_id"`
	// AdminName / AdminPassword are the tenant's first manage user (used to sign in to the console)
	// An empty AdminName means provision.DefaultAdminName (primary)
	AdminName     string `json:"admin_name"`
	AdminPassword string `json:"admin_password" binding:"required"`
}

// NextTenantSchema GET /platform/tenant-schemas/next
//
// For creating a tenant: pick an unused schema (try t0, t1, t2... from 0 upwards, one at a time).
// What it returns is a **suggestion** - Provision checks again when it actually writes (409 if taken);
// if someone else takes it in between, the frontend picks another and awaits confirmation before
// provisioning.
//
// The path uses a separate static prefix tenant-schemas: /tenants/next-schema would sit at the same
// level as /tenants/:schema and risks a conflict.
func NextTenantSchema(c *gin.Context) {
	schema, err := provision.NextSchema()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to find an available schema: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema}, "")
}

// CreateTenant POST /platform/tenants
//
// Provision a tenant: create the schema, build the tables from the template, create the first tenant
// admin and write the registry row in a single transaction (see nexus/platform/provision).
// The response carries the "first admin account name" so the frontend can tell the operator to verify
// it through the tenant login.
//
// schema: empty in the body means auto-select (the console goes this way and does not allow typing);
// an explicit value is still honoured (for scripted callers).
// admin_name: empty means provision.DefaultAdminName (primary).
// **Availability is checked once more right before writing**: if it was taken in between, a 409 is
// returned and the operator confirms a new candidate and retries.
func CreateTenant(c *gin.Context) {
	me, ok := auth.FromContext(c)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "missing platform identity", "")
		return
	}

	var req createTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// schema: empty means auto-select (take the first free one among t0, t1, t2...)
	schema := strings.TrimSpace(req.Schema)
	if schema == "" {
		s, serr := provision.NextSchema()
		if serr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to find an available schema: "+serr.Error(), "")
			return
		}
		schema = s
	}
	// First admin login name: empty means primary
	adminName := strings.TrimSpace(req.AdminName)
	if adminName == "" {
		adminName = provision.DefaultAdminName
	}

	// The owner must be an existing platform account with role=user (same rule as UpdateTenant)
	owner := strings.TrimSpace(req.OwnerUserID)
	if owner != "" {
		u, uerr := pmusers.GetByID(owner)
		if uerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to load owner: "+uerr.Error(), "")
			return
		}
		if u == nil {
			xhttp.ErrCode(c, "tenant.owner_not_found", gin.H{"owner": owner}, "owner not found", "")
			return
		}
		if u.Role != auth.RoleUser {
			xhttp.ErrCode(c, "tenant.owner_invalid_role", gin.H{"owner": owner},
				"the owner must be an account with the platform \"user\" role", "")
			return
		}
	}

	res, err := provision.Provision(provision.Request{
		Schema:        schema,
		DisplayName:   req.DisplayName,
		Plan:          req.Plan,
		OwnerUserID:   owner,
		AdminName:     adminName,
		AdminPassword: req.AdminPassword,
		CreatedBy:     me.ID,
	})
	if err != nil {
		// Taken in the meantime (someone created the same schema between the check and the write): return
		// 409 so the operator confirms a new candidate and retries - never silently change the schema
		if errors.Is(err, provision.ErrSchemaTaken) {
			xhttp.SendErrorResponse(c, http.StatusConflict, "conflict",
				fmt.Sprintf("schema %q has just been taken by another operation; retry with a freshly selected value", schema))
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	t, terr := tenantsrepo.Get(res.Schema)
	if terr != nil || t == nil {
		xhttp.OkJSON(c, "Success", gin.H{
			"schema":        res.Schema,
			"created_admin": res.AdminName,
		}, "")
		return
	}
	v := tenantView(t)
	v["created_admin"] = res.AdminName
	xhttp.OkJSON(c, "Success", v, "")
}

// GetTenant GET /platform/tenants/:schema
func GetTenant(c *gin.Context) {
	t, err := tenantsrepo.Get(c.Param("schema"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": strings.TrimSpace(c.Param("schema"))}, "tenant not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", tenantView(t), "")
}

type updateTenantRequest struct {
	DisplayName string `json:"display_name"`
	Remark      string `json:"remark"`
	// OwnerUserID is the owner (must be a platform user with role=user); an empty string clears it (admin-managed)
	OwnerUserID string `json:"owner_user_id"`
}

// UpdateTenant PUT /platform/tenants/:schema
//
// Changes the display name / remark / owner (transferring ownership means changing this field).
func UpdateTenant(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found", "")
		return
	}

	var req updateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	owner := strings.TrimSpace(req.OwnerUserID)
	if owner != "" {
		// The owner must be a platform user with the "user" role: an admin is a platform-level role and
		// should not be bound to a single tenant
		u, uerr := pmusers.GetByID(owner)
		if uerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "Failed to load owner: "+uerr.Error(), "")
			return
		}
		if u == nil {
			xhttp.ErrCode(c, "tenant.owner_not_found", gin.H{"owner": owner}, "owner not found", "")
			return
		}
		if u.Role != auth.RoleUser {
			xhttp.ErrCode(c, "tenant.owner_invalid_role", gin.H{"owner": owner},
				"the owner must be an account with the platform \"user\" role", "")
			return
		}
	}

	if err := tenantsrepo.Update(schema, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Remark), owner); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update tenant: "+err.Error(), "")
		return
	}
	// The registry may be cached for the tenant-side allow-list: invalidate it so "renamed but not shown / suspended but still working" cannot happen
	sharedtenants.Invalidate(schema)

	t2, err := tenantsrepo.Get(schema)
	if err != nil || t2 == nil {
		xhttp.OkJSON(c, "Success", gin.H{"schema": schema}, "")
		return
	}
	xhttp.OkJSON(c, "Success", tenantView(t2), "")
}

type setTenantStatusRequest struct {
	Status string `json:"status" binding:"required"` // active | suspended
}

// SetTenantStatus PUT /platform/tenants/:schema/status
//
// Only active <-> suspended is allowed: pending / deleted are internal states with no direct write
// endpoint.
// Once suspended, tenant-side /login and MCP/RPC resolution are rejected immediately.
func SetTenantStatus(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	var req setTenantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status != sharedtenants.StatusActive && status != sharedtenants.StatusSuspended {
		xhttp.ErrCode(c, "tenant.invalid_status", gin.H{"status": status}, "status must be active or suspended", "")
		return
	}

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found", "")
		return
	}
	if err := tenantsrepo.SetStatus(schema, status); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update status: "+err.Error(), "")
		return
	}
	sharedtenants.Invalidate(schema)

	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "status": status}, "")
}

// SetMyTenantStatus PUT /platform/me/tenants/:schema/status
//
// A "user" enables or disables a tenant **under their own name** (the "my tenants" page of the console).
//
// The only difference from the admin PUT /platform/tenants/:schema/status is the permission rule: this
// goes through mustOwnTenant (owner_user_id == yourself, otherwise 403; admins use the admin one).
//   - only active <-> suspended is allowed: pending / deleted are internal states, not writable from either side
//   - a suspension takes effect immediately - the shared/tenants allow-list cache must be invalidated
//     after the write, otherwise it waits a full TTL
func SetMyTenantStatus(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req setTenantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status != sharedtenants.StatusActive && status != sharedtenants.StatusSuspended {
		xhttp.ErrCode(c, "tenant.invalid_status", gin.H{"status": status}, "status must be active or suspended", "")
		return
	}

	if err := tenantsrepo.SetStatus(schema, status); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to update status: "+err.Error(), "")
		return
	}
	sharedtenants.Invalidate(schema)

	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "status": status}, "")
}

type setTenantQuotaRequest struct {
	// nil = unset (NULL, falls back to the system default); -1 = unlimited; a positive number = ceiling
	MaxAgents *int `json:"max_agents"`
	MaxSkills *int `json:"max_skills"`
	MaxFlows  *int `json:"max_flows"`

	// MaxSessionStorageBytes is the per-session storage ceiling in bytes; -1 means unlimited.
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	// MaxUserSessions is the per-user session ceiling.
	MaxUserSessions *int `json:"max_user_sessions"`
}

// SetTenantQuota PUT /platform/tenants/:schema/quota
func SetTenantQuota(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	var req setTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	if field, qerr := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); qerr != nil {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": field, "min": -1}, qerr.Error(), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes must be -1 (unlimited) or a non-negative number", "")
		return
	}

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found", "")
		return
	}

	if err := tenantsrepo.SetQuota(schema, req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to set quota: "+err.Error(), "")
		return
	}
	quota.Invalidate(schema)

	limits := quota.Resolve(schema)
	xhttp.OkJSON(c, "Success", gin.H{
		"schema": schema,
		"quota": gin.H{
			"max_agents":                limits.MaxAgents,
			"max_skills":                limits.MaxSkills,
			"max_flows":                 limits.MaxFlows,
			"max_session_storage_bytes": limits.MaxSessionStorageBytes,
			"max_user_sessions":         limits.MaxUserSessions,
		},
	}, "")
}

// setMyTenantQuotaRequest: "my tenants" may edit all five quotas itself.
// Absent / null clears the value (falls back to the system row); -1 means unlimited; a positive number is the ceiling.
type setMyTenantQuotaRequest struct {
	MaxAgents       *int `json:"max_agents"`
	MaxSkills       *int `json:"max_skills"`
	MaxFlows        *int `json:"max_flows"`
	MaxUserSessions *int `json:"max_user_sessions"`

	// MaxSessionStorageBytes is the per-session storage ceiling in bytes; -1 means unlimited.
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
}

// UpdateMyTenantQuota PUT /platform/me/tenants/:schema/quota
//
// "User" self-service: edit the five quotas of a tenant under their own name. It differs from the admin
// SetTenantQuota in exactly two ways:
//  1. permissions are scoped by owner (mustOwnTenant), not any tenant;
//  2. exceeding the system ceiling is **rejected outright** rather than truncated - a self-service page
//     has to say so on the spot, otherwise "saved successfully but the effective value did not change"
//     is a lie.
//
// All five can be loosened or tightened, but never beyond the system ceiling; when the ceiling itself is
// -1 (unlimited) no upper bound applies.
func UpdateMyTenantQuota(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req setMyTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	if field, qerr := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); qerr != nil {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": field, "min": -1}, qerr.Error(), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes must be -1 (unlimited) or a non-negative number", "")
		return
	}

	// The system ceiling is the system row (falling back to the code default when unset / -1); -1 means
	// unlimited, so no upper bound applies
	sys := quota.Resolve("")
	if req.MaxAgents != nil && sys.MaxAgents >= 0 && *req.MaxAgents > sys.MaxAgents {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_agents", "limit": sys.MaxAgents},
			fmt.Sprintf("the agent ceiling cannot exceed the system limit (%d)", sys.MaxAgents), "")
		return
	}
	if req.MaxSkills != nil && sys.MaxSkills >= 0 && *req.MaxSkills > sys.MaxSkills {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_skills", "limit": sys.MaxSkills},
			fmt.Sprintf("the skill ceiling cannot exceed the system limit (%d)", sys.MaxSkills), "")
		return
	}
	if req.MaxFlows != nil && sys.MaxFlows >= 0 && *req.MaxFlows > sys.MaxFlows {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_flows", "limit": sys.MaxFlows},
			fmt.Sprintf("the flow ceiling cannot exceed the system limit (%d)", sys.MaxFlows), "")
		return
	}
	if req.MaxUserSessions != nil && sys.MaxUserSessions >= 0 && *req.MaxUserSessions > sys.MaxUserSessions {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_user_sessions", "limit": sys.MaxUserSessions},
			fmt.Sprintf("the per-user session ceiling cannot exceed the system limit (%d)", sys.MaxUserSessions), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && sys.MaxSessionStorageBytes >= 0 &&
		*req.MaxSessionStorageBytes > sys.MaxSessionStorageBytes {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_session_storage_bytes", "limit": sys.MaxSessionStorageBytes},
			fmt.Sprintf("the per-session storage ceiling cannot exceed the system limit (%d bytes)", sys.MaxSessionStorageBytes), "")
		return
	}

	if err := tenantsrepo.SetQuota(schema, req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to set quota: "+err.Error(), "")
		return
	}
	quota.Invalidate(schema)

	limits := quota.Resolve(schema)
	xhttp.OkJSON(c, "Success", gin.H{
		"schema": schema,
		"quota": gin.H{
			"max_agents":                limits.MaxAgents,
			"max_skills":                limits.MaxSkills,
			"max_flows":                 limits.MaxFlows,
			"max_session_storage_bytes": limits.MaxSessionStorageBytes,
			"max_user_sessions":         limits.MaxUserSessions,
		},
	}, "")
}

// DeleteTenant DELETE /platform/tenants/:schema
//
// Delete a tenant completely: the registry row + platform-side config rows + the tenant schema (DROP ...
// CASCADE) + the storage directory.
//
// Two preconditions:
//  1. an active tenant must be suspended first - that step already made the operator confirm "this
//     affects callers", so deletion does not ask again, but it may not be skipped either;
//  2. only a tenant present in the registry is deleted (no orphan schemas, see provision.Remove).
//
// Order: database first (the source of truth), then the directory. The reverse ("directory first, delete
// fails") leaves a half-broken state where the tenant exists but all its files are gone; a failed
// directory removal is only garbage, worth a log line for manual cleanup.
func DeleteTenant(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant: "+err.Error(), "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "tenant not found", "")
		return
	}
	if t.Status == sharedtenants.StatusActive {
		xhttp.ErrCode(c, "tenant.must_suspend_first", gin.H{"name": schema},
			"the tenant is currently active; suspend it before deleting", "")
		return
	}

	if err := provision.Remove(schema); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the tenant: "+err.Error(), "")
		return
	}

	// Four caches are keyed by schema: the registry allow-list (whether login is allowed), quotas, flow run
	// limits and agent run limits
	sharedtenants.Invalidate(schema)
	quota.Invalidate(schema)
	flowconfig.Invalidate(schema)
	agentconfig.Invalidate(schema)

	// Directory cleanup comes after the database delete succeeded: a failure does not change the outcome, it only leaves garbage
	if store, serr := tenantStorage(); serr != nil {
		xlog.Error("[platform] tenant %s deleted, but storage is unavailable so the directory was not cleaned: %v", schema, serr)
	} else if derr := store.DeletePrefix(context.Background(), path.Join(paths.TenantsSubdir, schema)); derr != nil {
		xlog.Error("[platform] tenant %s deleted, but cleaning the storage directory failed: %v", schema, derr)
	}

	xlog.Info("[platform] deleted tenant %s (status before deletion %s)", schema, t.Status)
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema}, "")
}

// tenantStorage lazily creates the storage instance (same config as service/agent.InitStorage and the admin side).
var (
	tenantStoreOnce sync.Once
	tenantStore     storage.Storage
	tenantStoreErr  error
)

func tenantStorage() (storage.Storage, error) {
	tenantStoreOnce.Do(func() {
		if config.Cfg == nil {
			tenantStoreErr = errors.New("config not loaded")
			return
		}
		tenantStore, tenantStoreErr = storage.New(config.Cfg.Storage)
	})
	return tenantStore, tenantStoreErr
}
