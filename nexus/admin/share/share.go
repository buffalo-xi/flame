// Package share 租户侧的跨租户资源分发：上架（listing）/ 安装（install）/ 我的资产。
//
// 术语（见 nexus/docs/terminology.md）：动作 list / delist，状态 listed / delisted，
// 路由 POST /admin/share/listings，库表 platform.listing_*。界面与代码同词。
//
// 为什么落在 admin 而不是新进程：全仓共用同一个 db.DB 连接池与同一份 agent.root_dir，
// 租户侧接口本来就要读本租户 schema 与定义域文件，再写平台 schema（显式限定）即可。
// 平台侧只保留「审核 / 可见范围」，见 platform/handler/assets.go。
//
// 三条硬规矩（细节见 docs/asset-sharing.md）：
//  1. 复制，不引用：装完就是目标租户自己的实体，之后与快照无关；
//  2. 快照是唯一真源：安装只从 platform-assets/<id>/<ver>/ 拷，不回源租户读；
//  3. 冲突不静默：重名回 409 + 建议名，由用户确认后带 name= 重试。
package share

import (
	"encoding/json"
	"errors"
	"sync"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/repository/agentrelations"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

var (
	skillRepo = skills.NewRepository()
	agentRepo = agents.NewRepository()
	flowRepo  = flows.NewRepository()
	grantRepo = toolgrants.NewRepository()
	// relationRepo 只读「谁能调谁」：上架时把调用依赖的名字记进 manifest，
	// 导入前据此核对目标租户有没有那个智能体（授权行本身不随包走）。
	relationRepo = agentrelations.NewRepository()

	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// getStorage 懒加载存储实例（与 admin/skills、service/agent 同一份配置）。
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return store, storeErr
}

// tenantTable 拼租户表名：schema 来自 authInfo（登录时解析过的租户），不能来自请求体。
func tenantTable(schema, name string) string {
	return pq.QuoteIdentifier(schema) + "." + name
}

// assetErrMsg 把「表还没建 / 列还没加」翻成一句能照做的话。
//
// 跨租户分发的三张表不在自动迁移里（DB 脚本向来是手工执行，见 platform/README.md），
// 直接回 pq 原文，第一次部署的人只能去翻文档：
//
//	42P01 = undefined_table    表缺失 → 建表脚本还没跑
//	42703 = undefined_column   列缺失 → 迁移脚本跑了一半，表结构落后于代码
func assetErrMsg(prefix string, err error) string {
	if err == nil {
		return prefix
	}
	var pe *pq.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42P01":
			return prefix + ": " + err.Error() + "（平台表缺失，请执行 nexus/platform/sql/shared_assets.sql）"
		case "42703":
			return prefix + ": " + err.Error() + "（表结构落后于代码，请执行 nexus/platform/sql/ 下的迁移脚本，如 asset_pack_digest.sql）"
		}
	}
	return prefix + ": " + err.Error()
}

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return info, true
}

// listedByView 上架人视图：租户与人是两组 {id|schema, display_name}。
//
// JSON 用 schema（与 tenantView 同口径），SQL 列才是 schema_name；
// 人的 display_name 取上架时快照（不回查 users：用户改名/停用后历史要能读）。
func listedByView(a *sharedassets.Asset) gin.H {
	by := ""
	if a.ListedBy.Valid {
		by = a.ListedBy.String
	}
	// 导入行的 source_schema 是「对方那台机器上的租户」，本机同名 schema 只是巧合，
	// 拿它查本机 tenants 会给出误导性的名字 —— 这类行统一用来源站点名展示。
	tenantName := sharedassets.TenantDisplayName(a.SourceSchema)
	if a.Imported() {
		tenantName = a.OriginServer
	}
	return gin.H{
		"tenant": gin.H{
			"schema":       a.SourceSchema,
			"display_name": tenantName,
		},
		"user":   gin.H{"id": by, "display_name": a.ListedByName},
		"origin": importedOriginView(a),
	}
}

// importedOriginView 导入溯源（本机上架的行返回 nil）。
func importedOriginView(a *sharedassets.Asset) gin.H {
	if !a.Imported() {
		return nil
	}
	v := gin.H{"server": a.OriginServer}
	if a.OriginAssetID.Valid {
		v["asset_id"] = a.OriginAssetID.String
	}
	if a.ImportedAt.Valid {
		v["imported_at"] = a.ImportedAt.Time
	}
	return v
}

// assetView 列表/详情共用的视图。manifest 只在详情里展开（列表只给数量与体积）。
func assetView(a *sharedassets.Asset, installed bool) gin.H {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		_ = json.Unmarshal(a.Manifest, &m)
	}
	return gin.H{
		"id":           a.ID,
		"kind":         a.Kind,
		"name":         a.Name,
		"display_name": a.DisplayName,
		"description":  a.Description,
		"build":        a.Build,
		"status":       a.Status,
		"visibility":   a.Visibility,
		"listed_by":    listedByView(a),
		"entry":        m.Entry,
		"deps":         m.Deps,
		"file_count":   len(m.Files),
		"total_size":   m.TotalSize(),
		"installed":    installed,
		"created_at":   a.CreatedAt,
		"updated_at":   a.UpdatedAt,
	}
}
