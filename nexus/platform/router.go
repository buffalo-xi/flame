// Package platform is the server side of the platform console (multi-tenant administration).
//
// Boundary rules (see README.md for details):
//   - it depends only on nexus/shared/* and tools/*, and **must not** import nexus/admin/** or nexus/service/**
//   - the platform identity and the tenant identity are fully independent and mutually exclusive by claim shape (see the
//     auth package comment)
//
// Mounting (nexus/cmd/router.go, two lines):
//
//	platformGroup := router.Group("/platform")
//	platform.SetupRoutes(platformGroup)
//
// If the platform is ever split into its own process, this package only needs a cmd/main.go with its own gin engine; there is
// no reverse dependency into nexus/**.
package platform

import (
	pauth "flame/nexus/platform/auth"
	"flame/nexus/platform/handler"

	"github.com/gin-gonic/gin"
)

// SetupRoutes mounts the platform routes (the caller already adds the /platform prefix).
func SetupRoutes(r *gin.RouterGroup) {
	// Login: public (same shape as the tenant-side /admin/login)
	r.POST("/login", handler.Login)

	// Platform identity is enough (an administrator and a user can both reach their own information surface)
	self := r.Group("")
	self.Use(pauth.PlatformAuthMiddleware())
	{
		self.GET("/me", handler.Me)
		self.GET("/me/tenants", handler.MyTenants)
		// A "user" enables / disables a tenant under their own name (owner check lives in the handler; administrators use
		// /tenants/:schema/status)
		self.PUT("/me/tenants/:schema/status", handler.SetMyTenantStatus)
		// A "user" reads and writes the flow run limits of a tenant under their own name (the system level is read-only for
		// comparison; the validation rules match the administrator route)
		self.GET("/me/tenants/:schema/flowconfigs", handler.GetMyTenantFlowConfigs)
		self.PUT("/me/tenants/:schema/flowconfigs", handler.UpdateMyTenantFlowConfigs)
		// A "user" reads and writes the agent run limits of a tenant under their own name (same as flowconfigs: the system
		// level is read-only for comparison)
		self.GET("/me/tenants/:schema/agentconfigs", handler.GetMyTenantAgentConfigs)
		self.PUT("/me/tenants/:schema/agentconfigs", handler.UpdateMyTenantAgentConfigs)
		// A "user" reads and writes the session quota of a tenant under their own name (all five fields; exceeding the system
		// ceiling is rejected outright)
		self.GET("/me/tenants/:schema/quota", handler.GetMyTenantQuota)
		self.PUT("/me/tenants/:schema/quota", handler.UpdateMyTenantQuota)
		// A "user" resets the primary root account password of a tenant under their own name (the self-service recovery entry
		// when the initial password is forgotten)
		self.POST("/me/tenants/:schema/primary/password", handler.ResetMyTenantPrimaryPassword)
		// Self-service password change: the owner proves it with the old password (under /me to avoid the sibling static segment
		// conflict with /users/:id/password)
		self.POST("/me/password", handler.ChangeMyPassword)
	}

	// Peer market (server-to-server, read-only): authenticated by a "market token", unrelated to the platform login identity.
	// Egress is listed assets only - review here is the only precondition; the allow-list is a tenant-level dimension the
	// peer is not subject to.
	market := r.Group("/market")
	market.Use(pauth.MarketTokenMiddleware())
	{
		market.GET("/catalog", handler.ListMarketCatalog)
		market.GET("/assets/:id/package", handler.MarketAssetPackage)
	}

	// Platform administrators only: tenant management + platform user management
	admin := r.Group("")
	admin.Use(pauth.PlatformAuthMiddleware(), pauth.RequirePlatformAdmin())
	{
		// Cross-tenant asset distribution (platform.shared_assets): review / delist / visibility.
		// It governs but never transfers - packing and installing both happen on the tenant side (admin/share); nothing here
		// reads a tenant schema.
		admin.GET("/assets", handler.ListAssets)
		admin.GET("/assets/:id", handler.GetAsset)
		admin.POST("/assets/:id/review", handler.ReviewAsset)
		// Hard delete: both listed and delisted rows can be deleted directly, with no delist first;
		// delisting is "withdraw but keep the record", deleting is "clear it out" - two parallel dispositions. States the
		// tenant has not finished (pending / rejected / draft) are still refused by the handler.
		admin.DELETE("/assets/:id", handler.DeleteAsset)
		admin.GET("/assets/:id/visibility", handler.GetAssetVisibility)
		admin.PUT("/assets/:id/visibility", handler.SetAssetVisibility)
		// Cross-server transfer (manual package): export to zip / upload a zip to import.
		// An import always lands in pending: review elsewhere does not mean it is allowed here, see docs/asset-sharing.md §12.
		// A static prefix of its own (asset-imports) avoids the sibling static segment conflict with /assets/:id (the same
		// approach as tenant-schemas).
		admin.GET("/assets/:id/package", handler.ExportAssetPackage)
		admin.POST("/asset-imports", handler.ImportAssetPackage)

		// Cross-server market: the peers I pull from, and who may pull from me (market-tokens).
		// Pulling looks at the catalog first and imports one by one - a bulk import turns "the other side sent a pile of things"
		// into one irreversible action.
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

		// Market egress policy: whether tokens are required (the anonymous access switch).
		// It must live in the admin group and must not use a /market/settings path - the whole /platform/market/* subtree is
		// authenticated by market tokens, and a platform administrator would get a 401 there.
		admin.GET("/market-settings", handler.GetMarketSettings)
		admin.PUT("/market-settings", handler.SetMarketSettings)

		// Listing review policy: whether a tenant's listing needs review (global default + per-tenant override).
		// Unrelated to the install_policy of "pulling from a peer", which lives on peer_servers.
		admin.GET("/listing-policy", handler.GetListingPolicy)
		admin.PUT("/listing-policy", handler.SetListingDefault)
		admin.PUT("/listing-policy/:schema", handler.SetListingOverride)

		admin.GET("/tenants", handler.ListTenants)
		admin.POST("/tenants", handler.CreateTenant)
		// For tenant creation: automatically find an unused schema (tN).
		// A static prefix of its own (tenant-schemas) avoids the sibling static segment conflict with /tenants/:schema.
		admin.GET("/tenant-schemas/next", handler.NextTenantSchema)
		admin.GET("/tenants/:schema", handler.GetTenant)
		admin.PUT("/tenants/:schema", handler.UpdateTenant)
		admin.PUT("/tenants/:schema/status", handler.SetTenantStatus)
		// Hard delete (irreversible): the tenant must be suspended first, see handler.DeleteTenant
		admin.DELETE("/tenants/:schema", handler.DeleteTenant)
		admin.GET("/tenants/:schema/quota", handler.GetTenantQuota)
		admin.PUT("/tenants/:schema/quota", handler.SetTenantQuota)

		// Plugin grants (platform.tenant_plugins): the table stores only "not allowed" rows; a tenant row is the grant.
		// The tenant side is responsible for enabling and picking an implementation ({schema}.plugins).
		admin.GET("/plugins", handler.ListPluginOverview) // plugin management page: manifest x each tenant's raw row x enabled state
		admin.GET("/tenants/:schema/plugins", handler.ListTenantPlugins)
		admin.PUT("/tenants/:schema/plugins/:name", handler.SetTenantPluginPolicy)
		admin.DELETE("/tenants/:schema/plugins/:name", handler.DeleteTenantPluginPolicy)
		// Global switch-off (platform.plugin_gates): the emergency stop, allowed by default and overriding a tenant's explicit
		// setting.
		// There is no list endpoint: /plugins already carries blocked on every plugin.
		admin.PUT("/system/plugin-gates/:name", handler.SetPluginGate)
		admin.DELETE("/system/plugin-gates/:name", handler.DeletePluginGate)

		// Platform grants for tool capabilities (platform.tenant_capabilities): five levels such as exec / browser.
		// The tenant side only decides "which agent is granted what"; whether this kind of tool may be used at all is the
		// platform's call - the browser is a resident Chromium and shell leaves resident processes, so the cost and risk sit on
		// the platform side.
		// The key names are still sandbox.* (no database or API change); the UI calls them "capabilities".
		admin.GET("/tenants/:schema/capabilities", handler.ListTenantCapabilities)
		admin.PUT("/tenants/:schema/capabilities/:key", handler.SetTenantCapabilityPolicy)
		admin.DELETE("/tenants/:schema/capabilities/:key", handler.DeleteTenantCapabilityPolicy)
		// Global hard gate (platform.capability_gates): the emergency stop, allowed by default and overriding a tenant's
		// explicit enable.
		// It is a separate resource from tenant grants, so "change the default" cannot be mistaken for "the master switch".
		admin.GET("/system/capability-gates", handler.ListCapabilityGates)
		admin.PUT("/system/capability-gates/:key", handler.SetCapabilityGate)
		admin.DELETE("/system/capability-gates/:key", handler.DeleteCapabilityGate)

		// Flow run limits (the tenant row of platform.flow_configs; the system row is read-only)
		admin.GET("/tenants/:schema/flowconfigs", handler.GetTenantFlowConfigs)
		admin.PUT("/tenants/:schema/flowconfigs", handler.UpdateTenantFlowConfigs)

		// Agent run limits (the tenant row of platform.agent_configs; the system row is read-only)
		admin.GET("/tenants/:schema/agentconfigs", handler.GetTenantAgentConfigs)
		admin.PUT("/tenants/:schema/agentconfigs", handler.UpdateTenantAgentConfigs)

		// System settings (the **system row** tenant_id = '' of platform.flow_configs / agent_configs / quota_configs)
		admin.GET("/system/flowconfigs", handler.GetSystemFlowConfigs)
		admin.PUT("/system/flowconfigs", handler.UpdateSystemFlowConfigs)
		admin.GET("/system/agentconfigs", handler.GetSystemAgentConfigs)
		admin.PUT("/system/agentconfigs", handler.UpdateSystemAgentConfigs)
		admin.GET("/system/quota", handler.GetSystemQuota)
		admin.PUT("/system/quota", handler.UpdateSystemQuota)

		// LLM provider presets (platform.llm_provider_presets, globally shared, read-only on the tenant side)
		admin.GET("/system/llm-provider-presets", handler.ListLLMProviderPresets)
		admin.POST("/system/llm-provider-presets", handler.CreateLLMProviderPreset)
		admin.PUT("/system/llm-provider-presets/:id", handler.UpdateLLMProviderPreset)
		admin.DELETE("/system/llm-provider-presets/:id", handler.DeleteLLMProviderPreset)

		admin.GET("/users", handler.ListUsers)
		// The tenants under one user (the "tenants under this name" block of the detail card; the static segment has a password
		// precedent under :id, so there is no conflict)
		admin.GET("/users/:id/tenants", handler.UserTenants)
		admin.POST("/users", handler.CreateUser)
		admin.PUT("/users/:id", handler.UpdateUser)
		admin.POST("/users/:id/password", handler.ResetUserPassword)
	}
}
