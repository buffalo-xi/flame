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
// 管理员视角：全部租户 + 生效配额 + 当前用量。
//
// 分页口径：page 从 1 起；page_size 省略 / <=0 = **不分页**（返回全部，兼容旧调用），上限 100
// —— 每行要算三次用量 COUNT，放大 page_size 等于放大 DB 压力。
// total 是满足筛选条件的总数（不是本页条数），前端分页器用它。
func ListTenants(c *gin.Context) {
	q, status := c.Query("q"), c.Query("status")
	page, pageSize := parsePaging(c)

	list, err := tenantsrepo.List(status, q, pageSize, (page-1)*pageSize)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to list tenants: "+err.Error(), nil, "")
		return
	}

	total := len(list)
	if pageSize > 0 {
		n, cerr := tenantsrepo.Count(status, q)
		if cerr != nil {
			xhttp.ErrJSON(c, "Failed to count tenants: "+cerr.Error(), nil, "")
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

// parsePaging 解析 page / page_size：page 缺省 1（最小 1），page_size 缺省 0 = 不分页，上限 100。
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
	// Schema 空 = 由后端自动探索（管理台不给手填，见 GET /platform/tenant-schemas/next）
	Schema      string `json:"schema"`
	DisplayName string `json:"display_name"`
	Plan        string `json:"plan"`
	// OwnerUserID 负责人（平台「用户」id）；空 = 管理员代管
	OwnerUserID string `json:"owner_user_id"`
	// AdminName / AdminPassword 该租户首个 manage 用户（用于「租户登录」进控制台）
	// AdminName 空 = provision.DefaultAdminName（primary）
	AdminName     string `json:"admin_name"`
	AdminPassword string `json:"admin_password" binding:"required"`
}

// NextTenantSchema GET /platform/tenant-schemas/next
//
// 新建租户用：自动探索一个未被占用的 schema（t0、t1、t2… 从 0 起逐个 +1 试）。
// 这里给的是**建议值** —— 真正落库时 Provision 会再查一次（占用则回 409），
// 两次之间被抢占时前端重新探索、等操作者确认后再开通。
//
// 路径用独立静态前缀 tenant-schemas：/tenants/next-schema 会与 /tenants/:schema 同级，有冲突风险。
func NextTenantSchema(c *gin.Context) {
	schema, err := provision.NextSchema()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "探索可用 schema 失败: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema}, "")
}

// CreateTenant POST /platform/tenants
//
// 开通租户：单事务内建 schema + 模板建表 + 首个租户管理员 + 注册表行（见 nexus/platform/provision）。
// 响应里回带"首个管理员账号名"，前端据此提示操作者用「租户登录」验证。
//
// schema：body 留空 = 自动探索（管理台走这条，不给手填）；显式传入仍按显式值走（兼容脚本调用）。
// admin_name：留空 = provision.DefaultAdminName（primary）。
// **落库前会再查一次是否被占用**：两次检查之间被抢占则回 409，操作者确认新候选后重试。
func CreateTenant(c *gin.Context) {
	me, ok := auth.FromContext(c)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "平台身份缺失", "")
		return
	}

	var req createTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// schema：留空 = 自动探索（t0、t1、t2… 取第一个未被占用的）
	schema := strings.TrimSpace(req.Schema)
	if schema == "" {
		s, serr := provision.NextSchema()
		if serr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "探索可用 schema 失败: "+serr.Error(), "")
			return
		}
		schema = s
	}
	// 首个管理员登录名：留空 = primary
	adminName := strings.TrimSpace(req.AdminName)
	if adminName == "" {
		adminName = provision.DefaultAdminName
	}

	// 负责人必须是 role=user 且存在的平台账号（与 UpdateTenant 同口径）
	owner := strings.TrimSpace(req.OwnerUserID)
	if owner != "" {
		u, uerr := pmusers.GetByID(owner)
		if uerr != nil {
			xhttp.ErrJSON(c, "Failed to load owner: "+uerr.Error(), nil, "")
			return
		}
		if u == nil {
			xhttp.ErrCode(c, "tenant.owner_not_found", gin.H{"owner": owner}, "负责人不存在", "")
			return
		}
		if u.Role != auth.RoleUser {
			xhttp.ErrCode(c, "tenant.owner_invalid_role", gin.H{"owner": owner},
				"负责人必须是平台「用户」身份的账号", "")
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
		// 被抢占（探索/检查到落库之间有人建了同名租户）：回 409，操作者确认新候选后重试 —— 不静默改 schema
		if errors.Is(err, provision.ErrSchemaTaken) {
			xhttp.SendErrorResponse(c, http.StatusConflict, "conflict",
				fmt.Sprintf("schema %q 已被占用（刚被其它操作占走），请用重新探索后的值再次开通", schema))
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
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
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": strings.TrimSpace(c.Param("schema"))}, "租户不存在", "")
		return
	}
	xhttp.OkJSON(c, "Success", tenantView(t), "")
}

type updateTenantRequest struct {
	DisplayName string `json:"display_name"`
	Remark      string `json:"remark"`
	// OwnerUserID 负责人（必须是 role=user 的平台用户）；空串表示清空（管理员代管）
	OwnerUserID string `json:"owner_user_id"`
}

// UpdateTenant PUT /platform/tenants/:schema
//
// 改显示名 / 备注 / 负责人（转移负责人就是改这个字段）。
func UpdateTenant(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "租户不存在", "")
		return
	}

	var req updateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	owner := strings.TrimSpace(req.OwnerUserID)
	if owner != "" {
		// 负责人必须是"用户"身份的平台用户：管理员是平台级角色，不该被绑到某个租户上
		u, uerr := pmusers.GetByID(owner)
		if uerr != nil {
			xhttp.ErrJSON(c, "Failed to load owner: "+uerr.Error(), nil, "")
			return
		}
		if u == nil {
			xhttp.ErrCode(c, "tenant.owner_not_found", gin.H{"owner": owner}, "负责人不存在", "")
			return
		}
		if u.Role != auth.RoleUser {
			xhttp.ErrCode(c, "tenant.owner_invalid_role", gin.H{"owner": owner},
				"负责人必须是平台「用户」身份的账号", "")
			return
		}
	}

	if err := tenantsrepo.Update(schema, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Remark), owner); err != nil {
		xhttp.ErrJSON(c, "Failed to update tenant: "+err.Error(), nil, "")
		return
	}
	// 注册表可能被租户侧白名单缓存着：主动失效，避免"改了看不到 / 停用不生效"
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
// 只允许 active ⇄ suspended：pending / deleted 属内部态，不给接口直接写。
// 停用后租户侧 /login 与 MCP/RPC 解析都会立即被拒。
func SetTenantStatus(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	var req setTenantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status != sharedtenants.StatusActive && status != sharedtenants.StatusSuspended {
		xhttp.ErrCode(c, "tenant.invalid_status", gin.H{"status": status}, "status 只能是 active 或 suspended", "")
		return
	}

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "租户不存在", "")
		return
	}
	if err := tenantsrepo.SetStatus(schema, status); err != nil {
		xhttp.ErrJSON(c, "Failed to update status: "+err.Error(), nil, "")
		return
	}
	sharedtenants.Invalidate(schema)

	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "status": status}, "")
}

// SetMyTenantStatus PUT /platform/me/tenants/:schema/status
//
// 「用户」身份启停**自己名下**的租户（管理台「我的租户」页）。
//
// 与管理员那条 PUT /platform/tenants/:schema/status 的区别只在权限口径：
// 这里复用 mustOwnTenant（owner_user_id == 自己，否则 403；管理员走管理员那条）。
//   - 只允许 active ⇄ suspended：pending / deleted 属内部态，两边都不给写
//   - 停用立即生效 —— 写完必须失效 shared/tenants 白名单缓存，否则要等一个 TTL 周期
func SetMyTenantStatus(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req setTenantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status != sharedtenants.StatusActive && status != sharedtenants.StatusSuspended {
		xhttp.ErrCode(c, "tenant.invalid_status", gin.H{"status": status}, "status 只能是 active 或 suspended", "")
		return
	}

	if err := tenantsrepo.SetStatus(schema, status); err != nil {
		xhttp.ErrJSON(c, "Failed to update status: "+err.Error(), nil, "")
		return
	}
	sharedtenants.Invalidate(schema)

	xhttp.OkJSON(c, "Success", gin.H{"schema": schema, "status": status}, "")
}

type setTenantQuotaRequest struct {
	// nil = 未设置（NULL，回退系统默认）；-1 = 不限制；正数 = 上限
	MaxAgents *int `json:"max_agents"`
	MaxSkills *int `json:"max_skills"`
	MaxFlows  *int `json:"max_flows"`

	// MaxSessionStorageBytes 单会话空间上限（字节）；-1 = 不限制。
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	// MaxUserSessions 单用户会话数上限。
	MaxUserSessions *int `json:"max_user_sessions"`
}

// SetTenantQuota PUT /platform/tenants/:schema/quota
func SetTenantQuota(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))
	var req setTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	if err := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes 只能是 -1（不限制）或非负数", "")
		return
	}

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "租户不存在", "")
		return
	}

	if err := tenantsrepo.SetQuota(schema, req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrJSON(c, "Failed to set quota: "+err.Error(), nil, "")
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

// setMyTenantQuotaRequest 「我的租户」可自助修改全部五项配额。
// 缺省 / null = 清除（回退系统行）；-1 = 不限；正数 = 上限。
type setMyTenantQuotaRequest struct {
	MaxAgents       *int `json:"max_agents"`
	MaxSkills       *int `json:"max_skills"`
	MaxFlows        *int `json:"max_flows"`
	MaxUserSessions *int `json:"max_user_sessions"`

	// MaxSessionStorageBytes 单会话空间上限（字节）；-1 = 不限制。
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
}

// UpdateMyTenantQuota PUT /platform/me/tenants/:schema/quota
//
// 「用户」自助：改自己名下租户的五项配额。与管理员那条 SetTenantQuota 的差别只有两点：
//  1. 权限按 owner 收口（mustOwnTenant），不是任意租户；
//  2. 超系统顶**直接拒**而不是截断生效 —— 自助页面必须当场说清，
//     否则「保存成功但生效值没变」等于骗人。
//
// 五项都能放宽也能收紧，但绝不能越过系统顶；系统顶本身为 -1（不限）时不做上限约束。
func UpdateMyTenantQuota(c *gin.Context) {
	schema, ok := mustOwnTenant(c)
	if !ok {
		return
	}

	var req setMyTenantQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	if err := checkQuotaSystemValues([]intField{
		{"max_agents", req.MaxAgents},
		{"max_skills", req.MaxSkills},
		{"max_flows", req.MaxFlows},
		{"max_user_sessions", req.MaxUserSessions},
	}); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if req.MaxSessionStorageBytes != nil && *req.MaxSessionStorageBytes < -1 {
		xhttp.ErrCode(c, "quota.invalid_value", gin.H{"field": "max_session_storage_bytes", "min": -1},
			"max_session_storage_bytes 只能是 -1（不限制）或非负数", "")
		return
	}

	// 系统顶 = 系统行（未设置 / -1 时退到代码兜底）；-1 = 不限，不做上限约束
	sys := quota.Resolve("")
	if req.MaxAgents != nil && sys.MaxAgents >= 0 && *req.MaxAgents > sys.MaxAgents {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_agents", "limit": sys.MaxAgents},
			fmt.Sprintf("智能体数量上限不能超过系统限额（%d）", sys.MaxAgents), "")
		return
	}
	if req.MaxSkills != nil && sys.MaxSkills >= 0 && *req.MaxSkills > sys.MaxSkills {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_skills", "limit": sys.MaxSkills},
			fmt.Sprintf("技能数量上限不能超过系统限额（%d）", sys.MaxSkills), "")
		return
	}
	if req.MaxFlows != nil && sys.MaxFlows >= 0 && *req.MaxFlows > sys.MaxFlows {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_flows", "limit": sys.MaxFlows},
			fmt.Sprintf("工作流数量上限不能超过系统限额（%d）", sys.MaxFlows), "")
		return
	}
	if req.MaxUserSessions != nil && sys.MaxUserSessions >= 0 && *req.MaxUserSessions > sys.MaxUserSessions {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_user_sessions", "limit": sys.MaxUserSessions},
			fmt.Sprintf("单用户会话数上限不能超过系统限额（%d）", sys.MaxUserSessions), "")
		return
	}
	if req.MaxSessionStorageBytes != nil && sys.MaxSessionStorageBytes >= 0 &&
		*req.MaxSessionStorageBytes > sys.MaxSessionStorageBytes {
		xhttp.ErrCode(c, "quota.exceeds_system_limit", gin.H{"field": "max_session_storage_bytes", "limit": sys.MaxSessionStorageBytes},
			fmt.Sprintf("单会话空间上限不能超过系统限额（%d 字节）", sys.MaxSessionStorageBytes), "")
		return
	}

	if err := tenantsrepo.SetQuota(schema, req.MaxAgents, req.MaxSkills, req.MaxFlows,
		req.MaxSessionStorageBytes, req.MaxUserSessions); err != nil {
		xhttp.ErrJSON(c, "Failed to set quota: "+err.Error(), nil, "")
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
// 彻底删除一个租户：注册表行 + 平台侧配置行 + 租户 schema（DROP ... CASCADE）+ 存储目录。
//
// 两道前置：
//  1. 正常在用（active）的租户必须先停用 —— 停用那一步已经让操作者确认过
//     「会影响调用方」，删除不重复问一遍，但也不允许跳过；
//  2. 只删注册表里存在的租户（不认孤儿 schema，见 provision.Remove）。
//
// 顺序：先删库（事实来源），成功后再清目录。反过来「先删目录、库删失败」会留下
// 租户还在但文件全没了的半残状态；目录删失败只是垃圾，记日志等人工清理。
func DeleteTenant(c *gin.Context) {
	schema := strings.TrimSpace(c.Param("schema"))

	t, err := tenantsrepo.Get(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant: "+err.Error(), nil, "")
		return
	}
	if t == nil {
		xhttp.ErrCode(c, "tenant.not_found", gin.H{"name": schema}, "租户不存在", "")
		return
	}
	if t.Status == sharedtenants.StatusActive {
		xhttp.ErrCode(c, "tenant.must_suspend_first", gin.H{"name": schema},
			"租户当前是「正常」状态，请先停用再删除", "")
		return
	}

	if err := provision.Remove(schema); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "删除租户失败: "+err.Error(), "")
		return
	}

	// 四份缓存都按 schema 存着：注册表白名单（能否登录）、配额、工作流运行参数、智能体运行参数
	sharedtenants.Invalidate(schema)
	quota.Invalidate(schema)
	flowconfig.Invalidate(schema)
	agentconfig.Invalidate(schema)

	// 目录清理放在库删成功之后：失败不影响删除结果，只留垃圾
	if store, serr := tenantStorage(); serr != nil {
		xlog.Error("[platform] 租户 %s 已删除，但存储不可用、目录未清理: %v", schema, serr)
	} else if derr := store.DeletePrefix(context.Background(), path.Join(paths.TenantsSubdir, schema)); derr != nil {
		xlog.Error("[platform] 租户 %s 已删除，但清理存储目录失败: %v", schema, derr)
	}

	xlog.Info("[platform] 已删除租户 %s（删除前状态 %s）", schema, t.Status)
	xhttp.OkJSON(c, "Success", gin.H{"schema": schema}, "")
}

// tenantStorage 懒加载存储实例（与 service/agent.InitStorage、admin 侧同配置）。
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
		tenantStore, tenantStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return tenantStore, tenantStoreErr
}
