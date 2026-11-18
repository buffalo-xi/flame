// Package platform 平台管理台的服务端（多租户管理）。
//
// 边界规则（详见 README.md）：
//   - 只依赖 nexus/shared/* 与 tools/*，**禁止** import nexus/admin/**、nexus/service/**
//   - 平台身份与租户身份完全独立，靠 claims 形态互斥（见 auth 包注释）
//
// 挂载方式（nexus/cmd/router.go，两行）：
//
//	platformGroup := router.Group("/platform")
//	platform.SetupRoutes(platformGroup)
//
// 将来若要把平台拆成独立进程，本包加一个 cmd/main.go 起自己的 gin 引擎即可，
// 对 nexus/** 没有任何反向依赖。
package platform

import (
	pauth "flame/nexus/platform/auth"
	"flame/nexus/platform/handler"

	"github.com/gin-gonic/gin"
)

// SetupRoutes 挂载平台路由（外层已带 /platform 前缀）。
func SetupRoutes(r *gin.RouterGroup) {
	// 登录：公开（与租户侧 /admin/login 同形态）
	r.POST("/login", handler.Login)

	// 平台身份即可（管理员与用户都能访问自己的信息面）
	self := r.Group("")
	self.Use(pauth.PlatformAuthMiddleware())
	{
		self.GET("/me", handler.Me)
		self.GET("/me/tenants", handler.MyTenants)
		// 「用户」启停自己名下的租户（owner 校验在 handler 里；管理员走 /tenants/:schema/status）
		self.PUT("/me/tenants/:schema/status", handler.SetMyTenantStatus)
		// 「用户」读写自己名下租户的工作流运行参数（系统级只读对照；校验口径同管理员那条）
		self.GET("/me/tenants/:schema/flowconfigs", handler.GetMyTenantFlowConfigs)
		self.PUT("/me/tenants/:schema/flowconfigs", handler.UpdateMyTenantFlowConfigs)
		// 「用户」读写自己名下租户的智能体运行参数（同 flowconfigs：系统级只读对照）
		self.GET("/me/tenants/:schema/agentconfigs", handler.GetMyTenantAgentConfigs)
		self.PUT("/me/tenants/:schema/agentconfigs", handler.UpdateMyTenantAgentConfigs)
		// 「用户」读写自己名下租户的会话配额（只开放两个会话字段；超系统顶直接拒）
		self.GET("/me/tenants/:schema/quota", handler.GetMyTenantQuota)
		self.PUT("/me/tenants/:schema/quota", handler.UpdateMyTenantQuota)
		// 「用户」为名下租户的根账号 primary 重置密码（忘记初始密码时的自助恢复口）
		self.POST("/me/tenants/:schema/primary/password", handler.ResetMyTenantPrimaryPassword)
		// 自助改密：本人凭旧密码更换（放 /me 下，避开 /users/:id/password 的同级静态段冲突）
		self.POST("/me/password", handler.ChangeMyPassword)
	}

	// 对端市场（站到站只读）：用「市场令牌」鉴权，与平台登录身份无关。
	// 出口只有 listed 资产 —— 本机审核通过是唯一前提；allowlist 是租户维度，对端不受约束。
	market := r.Group("/market")
	market.Use(pauth.MarketTokenMiddleware())
	{
		market.GET("/catalog", handler.ListMarketCatalog)
		market.GET("/assets/:id/package", handler.MarketAssetPackage)
	}

	// 仅平台管理员：租户管理 + 平台用户管理
	admin := r.Group("")
	admin.Use(pauth.PlatformAuthMiddleware(), pauth.RequirePlatformAdmin())
	{
		// 跨租户资源分发（platform.shared_assets）：审核 / 下架 / 可见范围。
		// 只治理不搬运 —— 打包与安装都在租户侧（admin/share），这里不读任何租户 schema。
		admin.GET("/assets", handler.ListAssets)
		admin.GET("/assets/:id", handler.GetAsset)
		admin.POST("/assets/:id/review", handler.ReviewAsset)
		// 彻底删除：在架（listed）与已下架（delisted）都能直接删，不要求先下架；
		// 下架是「留档撤出」，删除是「清库」，两种处置并列。租户没走完的档位（pending /
		// rejected / draft）仍由 handler 拒绝。
		admin.DELETE("/assets/:id", handler.DeleteAsset)
		admin.GET("/assets/:id/visibility", handler.GetAssetVisibility)
		admin.PUT("/assets/:id/visibility", handler.SetAssetVisibility)
		// 跨服务器搬运（手工包）：导出成 zip / 上传 zip 导入。
		// 导入一律落 pending：对面审过不等于本机放行，见 docs/asset-sharing.md §12。
		// 用独立静态前缀 asset-imports，避免与 /assets/:id 同级静态段冲突（同 tenant-schemas 的做法）。
		admin.GET("/assets/:id/package", handler.ExportAssetPackage)
		admin.POST("/asset-imports", handler.ImportAssetPackage)

		// 跨服务器市场：我去拉的对端（peers）与允许谁拉我（market-tokens）。
		// 拉取先看目录、再逐条导入 —— 批量导入会把「对面发来一堆东西」变成不可撤销的动作。
		admin.GET("/peers", handler.ListPeers)
		admin.POST("/peers", handler.CreatePeer)
		admin.PUT("/peers/:id", handler.UpdatePeer)
		admin.DELETE("/peers/:id", handler.DeletePeer)
		admin.GET("/peers/:id/catalog", handler.PeerCatalog)
		admin.POST("/peers/:id/import", handler.ImportFromPeer)

		admin.GET("/market-tokens", handler.ListMarketTokens)
		admin.POST("/market-tokens", handler.CreateMarketToken)
		admin.PUT("/market-tokens/:id", handler.SetMarketToken)
		admin.DELETE("/market-tokens/:id", handler.DeleteMarketToken)

		// 市场出口策略：要不要校验令牌（匿名访问开关）。
		// 必须挂在 admin 组、且不能用 /market/settings 这个路径 —— /platform/market/*
		// 整段由市场令牌鉴权，平台管理员进去反而会被 401。
		admin.GET("/market-settings", handler.GetMarketSettings)
		admin.PUT("/market-settings", handler.SetMarketSettings)

		// 上架审核策略：租户上架资产要不要过审核（全局默认 + 按租户覆盖）。
		// 与「从远端拉取」的 install_policy 无关，那条在 peer_servers 上。
		admin.GET("/listing-policy", handler.GetListingPolicy)
		admin.PUT("/listing-policy", handler.SetListingDefault)
		admin.PUT("/listing-policy/:schema", handler.SetListingOverride)

		admin.GET("/tenants", handler.ListTenants)
		admin.POST("/tenants", handler.CreateTenant)
		// 新建租户用：自动探索一个未被占用的 schema（tN）。
		// 用独立静态前缀 tenant-schemas，避免与 /tenants/:schema 同级静态段冲突。
		admin.GET("/tenant-schemas/next", handler.NextTenantSchema)
		admin.GET("/tenants/:schema", handler.GetTenant)
		admin.PUT("/tenants/:schema", handler.UpdateTenant)
		admin.PUT("/tenants/:schema/status", handler.SetTenantStatus)
		// 彻底删除（不可逆）：body 必须回填 confirm_schema，见 handler.DeleteTenant
		admin.DELETE("/tenants/:schema", handler.DeleteTenant)
		admin.GET("/tenants/:schema/quota", handler.GetTenantQuota)
		admin.PUT("/tenants/:schema/quota", handler.SetTenantQuota)

		// 插件停用（platform.tenant_plugins）：缺省对所有租户开放（2026-11-02 起），
		// 本表只存「停用」行；租户侧负责启用与配实现（{schema}.plugins）。
		admin.GET("/plugins", handler.ListPluginOverview) // 插件管理页：清单 × 各租户原始行 × 启用状态
		admin.GET("/tenants/:schema/plugins", handler.ListTenantPlugins)
		admin.PUT("/tenants/:schema/plugins/:name", handler.SetTenantPluginPolicy)
		admin.DELETE("/tenants/:schema/plugins/:name", handler.DeleteTenantPluginPolicy)
		// 全局停用（platform.plugin_gates）：急停口，缺省放行、压过租户的显式设置。
		// 没有列表接口：/plugins 已在每个插件上带 blocked。
		admin.PUT("/system/plugin-gates/:name", handler.SetPluginGate)
		admin.DELETE("/system/plugin-gates/:name", handler.DeletePluginGate)

		// 工具能力的平台授权（platform.tenant_capabilities）：exec / browser 等五档。
		// 租户侧只有「给哪个 agent 授权」，能不能用这类工具由平台决定 ——
		// 浏览器是常驻 Chromium、shell 会留下常驻进程，成本与风险在平台侧。
		// 键名仍是 sandbox.*（不改库、不改接口），界面上叫「能力」。
		admin.GET("/tenants/:schema/capabilities", handler.ListTenantCapabilities)
		admin.PUT("/tenants/:schema/capabilities/:key", handler.SetTenantCapabilityPolicy)
		admin.DELETE("/tenants/:schema/capabilities/:key", handler.DeleteTenantCapabilityPolicy)
		// 全局硬闸（platform.capability_gates）：急停口，缺省放行、压过租户的显式开启。
		// 与租户授权分开两个资源，避免「改缺省」被误当成「总闸」。
		admin.GET("/system/capability-gates", handler.ListCapabilityGates)
		admin.PUT("/system/capability-gates/:key", handler.SetCapabilityGate)
		admin.DELETE("/system/capability-gates/:key", handler.DeleteCapabilityGate)

		// 工作流运行参数（platform.flow_configs 的租户行；系统行只读）
		admin.GET("/tenants/:schema/flowconfigs", handler.GetTenantFlowConfigs)
		admin.PUT("/tenants/:schema/flowconfigs", handler.UpdateTenantFlowConfigs)

		// 智能体运行参数（platform.agent_configs 的租户行；系统行只读）
		admin.GET("/tenants/:schema/agentconfigs", handler.GetTenantAgentConfigs)
		admin.PUT("/tenants/:schema/agentconfigs", handler.UpdateTenantAgentConfigs)

		// 系统设置（platform.flow_configs / agent_configs / quota_configs 的**系统行** tenant_id = ''）
		admin.GET("/system/flowconfigs", handler.GetSystemFlowConfigs)
		admin.PUT("/system/flowconfigs", handler.UpdateSystemFlowConfigs)
		admin.GET("/system/agentconfigs", handler.GetSystemAgentConfigs)
		admin.PUT("/system/agentconfigs", handler.UpdateSystemAgentConfigs)
		admin.GET("/system/quota", handler.GetSystemQuota)
		admin.PUT("/system/quota", handler.UpdateSystemQuota)

		// LLM 服务商预设（platform.llm_provider_presets，全局共享，租户侧只读）
		admin.GET("/system/llm-provider-presets", handler.ListLLMProviderPresets)
		admin.POST("/system/llm-provider-presets", handler.CreateLLMProviderPreset)
		admin.PUT("/system/llm-provider-presets/:id", handler.UpdateLLMProviderPreset)
		admin.DELETE("/system/llm-provider-presets/:id", handler.DeleteLLMProviderPreset)

		admin.GET("/users", handler.ListUsers)
		// 某用户名下的租户（详情卡「名下租户」块；静态段在 :id 下有 password 先例，无冲突）
		admin.GET("/users/:id/tenants", handler.UserTenants)
		admin.POST("/users", handler.CreateUser)
		admin.PUT("/users/:id", handler.UpdateUser)
		admin.POST("/users/:id/password", handler.ResetUserPassword)
	}
}
