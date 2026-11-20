package admin

import (
	"flame/nexus/admin/agentconfigs"
	"flame/nexus/admin/agents"
	"flame/nexus/admin/customtools"
	"flame/nexus/admin/flow"
	"flame/nexus/admin/flowconfigs"
	"flame/nexus/admin/flows"
	"flame/nexus/admin/gatewaytest"
	"flame/nexus/admin/globalvars"
	"flame/nexus/admin/llmmodels"
	"flame/nexus/admin/llmproviderpresets"
	"flame/nexus/admin/llmproviders"
	"flame/nexus/admin/mcpconnections"
	"flame/nexus/admin/mcpserverconfig"
	"flame/nexus/admin/plugins"
	"flame/nexus/admin/rpcconnections"
	"flame/nexus/admin/rpcserverconfig"
	"flame/nexus/admin/runs"
	"flame/nexus/admin/share"
	"flame/nexus/admin/skills"
	"flame/nexus/admin/svcauthbindings"
	"flame/nexus/admin/svcendpoints"
	"flame/nexus/admin/toolgrants"
	"flame/nexus/admin/users"
	"flame/nexus/shared/auth"

	"github.com/gin-gonic/gin"
)

// SetupRoutes wires the admin routes.
func SetupRoutes(router *gin.RouterGroup) {
	// Login route (no auth middleware).
	router.POST("/login", auth.Login)

	// LiteLLM gateway test routes (no auth; development and debugging only).
	gatewayTestGroup := router.Group("/gatewaytest")
	{
		gatewayTestGroup.POST("/run", gatewaytest.RunGatewayTest)
		gatewayTestGroup.POST("/provision", gatewaytest.ProvisionTest)
		gatewayTestGroup.POST("/deprovision", gatewaytest.DeprovisionTest)
		gatewayTestGroup.POST("/generate", gatewaytest.GenerateTest)
	}

	// Workspace file "navigation download": the browser's window.location cannot carry an Authorization header,
	// so it authenticates with a short-lived HMAC ticket in the URL (see admin/agents/download_ticket.go).
	router.GET("/filedl/:id", agents.DownloadByTicket)

	// Auth middleware from here on.
	router.Use(auth.AuthMiddleware())

	// Custom tools ({schema}.custom_tools): tenant-defined custom__* tools.
	// Separate from the plugins page: a plugin enables an implementation on disk, while this creates your own tool.
	customToolsGroup := router.Group("/customtools")
	{
		customToolsGroup.GET("/", customtools.ListCustomTools)
		customToolsGroup.POST("/create", customtools.CreateCustomTool)
		customToolsGroup.GET("/:name", customtools.GetCustomTool)
		customToolsGroup.PUT("/:name", customtools.UpdateCustomTool)
		customToolsGroup.DELETE("/:name", customtools.DeleteCustomTool)
		// Trial run: the same execution core as a live call, returning stdout / stderr / exit code to the admin.
		// gin allows a static segment (/create) and a parameter segment (/:name/run) as siblings; if startup reports a
		// route conflict, change it to POST /customtools/run (name in the body) - only one front-end call site moves.
		customToolsGroup.POST("/:name/run", customtools.RunCustomTool)
		// Content directory file management (mirrors the skill content directory; see the files.go header for the two differences):
		//   TOOL.json is readable but not writable and not deletable (the contract has one write path), and the entry file cannot be deleted.
		customToolsGroup.GET("/:name/files", customtools.ListToolFiles)
		customToolsGroup.GET("/:name/file", customtools.ReadToolFile)
		customToolsGroup.PUT("/:name/file", customtools.WriteToolFile)
		customToolsGroup.POST("/:name/file", customtools.CreateToolEntry)
		customToolsGroup.DELETE("/:name/file", customtools.DeleteToolEntry)
		// Upload into the content directory (multipart: file + dir; no overwrite, a clash saves as name-2.ext)
		customToolsGroup.POST("/:name/upload", customtools.UploadToolFile)
		// Streaming file download (read-only; drag-to-desktop fetches the bytes, requires ?path=)
		customToolsGroup.GET("/:name/download", customtools.DownloadToolFile)
		// Versions: a production run reads the is_current copy (versions/tools/<name>/v<n>),
		// while the draft only applies to debugging in the editor. See shared/versions and nexus/docs/terminology.md.
		customToolsGroup.GET("/:name/versions", customtools.ListToolVersions)
		// Release = freeze the current draft directory into a new version and make it current
		customToolsGroup.POST("/:name/versions", customtools.ReleaseToolVersion)
		// Rollback = switch production to that version (the draft is untouched); apply = write that version back into the draft (production is untouched)
		customToolsGroup.POST("/:name/versions/:version/rollback", customtools.RollbackToolVersion)
		// Apply = write that version back into the draft (production untouched); delete = history only (the current version is refused)
		customToolsGroup.POST("/:name/versions/:version/apply", customtools.ApplyToolVersion)
		customToolsGroup.DELETE("/:name/versions/:version", customtools.DeleteToolVersion)
	}

	// Cross-tenant distribution (platform review + copy-style install):
	//   - list: submit an entity of this tenant as a platform asset (lands in pending, waiting for platform review)
	//   - install: copy a platform asset into an entity of this tenant (files first, DB second; a clash returns 409 plus a suggested name)
	//   - my listings / installed: status and an "update available" hint
	// The platform schema comes from config.database.platform_schema; table names never come from the request body.
	// See docs/asset-sharing.md for the design.
	shareGroup := router.Group("/share")
	{
		shareGroup.GET("/assets", share.ListAssets)
		shareGroup.GET("/assets/:id", share.GetAsset)
		shareGroup.POST("/listings", share.CreateListing)
		shareGroup.GET("/mine", share.ListMine)
		shareGroup.POST("/mine/:id/withdraw", share.Withdraw)
		// My listings: delete directly (skipping the withdraw flow) and clear every unapproved one at once
		shareGroup.DELETE("/mine", share.ClearMine)
		shareGroup.DELETE("/mine/:id", share.DeleteMine)
		shareGroup.POST("/install", share.Install)
		// Bundle export / import: "export" on a card downloads a zip directly (no platform snapshot, no review),
		// and "import" uploads a zip that goes through the same install logic into an entity of this tenant. See docs/asset-sharing.md.
		shareGroup.GET("/export", share.Export)
		shareGroup.POST("/import", share.Import)
		// Pre-import check: parse the bundle read-only and list whether each dependency (skill / tool / called agent / capability) exists in this tenant,
		// so what is missing is visible before anything is touched instead of read from warnings afterwards
		shareGroup.POST("/import/inspect", share.Inspect)
		// Pre-install check for the marketplace (read-only): same-named entity / dependencies / capabilities at once, from the same source and rules as the upload preflight
		shareGroup.POST("/install/inspect", share.InspectAsset)
		shareGroup.GET("/installs", share.ListInstalls)
		// Deletes install records only, never the entities (uninstalling uses each kind's delete API)
		shareGroup.DELETE("/installs", share.ClearInstalls)
		shareGroup.DELETE("/installs/:asset_id/:installed_id", share.DeleteInstall)

		// Remote market (pull-through): the catalog shows only peers the platform configured and made visible to this tenant;
		// pulling is done server-side with the token - the token stays on the server and the front end never sees it.
		// The peer address is sent as a read-only field (so the tenant can see where the site is), but it never feeds
		// the construction of any outbound request: pulls still go by peer name, so there is no SSRF surface.
		// Pull and install are separate: pulling deduplicates by asset, installing is per tenant.
		shareGroup.GET("/remote-assets", share.RemoteAssets)
		shareGroup.POST("/pull-asset", share.PullAsset)
	}

	// LLM provider routes
	llmProvidersGroup := router.Group("/llmproviders")
	{
		llmProvidersGroup.POST("/", llmproviders.ListLLMProviders)
		llmProvidersGroup.GET("/:id", llmproviders.GetLLMProvider)
		llmProvidersGroup.POST("/create", llmproviders.CreateLLMProvider)
		llmProvidersGroup.PUT("/:id", llmproviders.UpdateLLMProvider)
		llmProvidersGroup.DELETE("/:id", llmproviders.DeleteLLMProvider)
	}

	// LLM provider preset routes (system level, shared globally)
	llmProviderPresetsGroup := router.Group("/llmproviderpresets")
	{
		llmProviderPresetsGroup.GET("/", llmproviderpresets.ListLLMProviderPresets)
	}

	// LLM model routes
	llmModelsGroup := router.Group("/llmmodels")
	{
		llmModelsGroup.POST("/", llmmodels.ListLLMModels)
		llmModelsGroup.GET("/:id", llmmodels.GetLLMModel)
		llmModelsGroup.POST("/create", llmmodels.CreateLLMModel)
		llmModelsGroup.PUT("/:id", llmmodels.UpdateLLMModel)
		llmModelsGroup.DELETE("/:id", llmmodels.DeleteLLMModel)
		llmModelsGroup.POST("/provider/:provider_id", llmmodels.ListLLMModelsByProviderID)
		llmModelsGroup.GET("/default", llmmodels.GetDefaultLLMModel)
		llmModelsGroup.POST("/:id/provision", llmmodels.ProvisionLLMModel)
		llmModelsGroup.POST("/:id/deprovision", llmmodels.DeprovisionLLMModel)
		llmModelsGroup.POST("/deprovision-all", llmmodels.DeprovisionAllLLMModels)
		llmModelsGroup.POST("/selectable", llmmodels.ListSelectableLLMModels)
		// Align every local LLM config with the LiteLLM gateway and clean up stale gateway records
		llmModelsGroup.POST("/sync", llmmodels.SyncLLMModels)
	}

	// Run ledger: this tenant's agent + flow runs in progress (other instances included, kept in Redis).
	// Stopping is broadcast: a run lives only in the memory of the machine running it, so sending the intent to every instance is the reliable path.
	runningGroup := router.Group("/running")
	{
		runningGroup.GET("", runs.ListRunning)
		runningGroup.POST("/stop", runs.StopRun)
		// Historical runs: query across flows / clean up by filter / CSV export.
		// Two different data sets from the two APIs above: the ledger lives in Redis for the duration of a run,
		// while this reads flow_run_records in PostgreSQL, written when a run finishes and kept long term.
		runningGroup.GET("/history", runs.ListHistory)
		runningGroup.DELETE("/history", runs.CleanupHistory)
		runningGroup.GET("/history/export", runs.ExportHistory)
	}

	// Agent routes (the agents table)
	agentsGroup := router.Group("/agents")
	{
		agentsGroup.POST("/", agents.ListAgents)
		agentsGroup.GET("/:id", agents.GetAgent)
		agentsGroup.POST("/create", agents.CreateAgent)
		agentsGroup.PUT("/:id", agents.UpdateAgent)
		agentsGroup.DELETE("/:id", agents.DeleteAgent)
		agentsGroup.POST("/:id/chat", agents.ChatAgent)
		// Stop a running agent (by session or by run); idempotent, returning stopped:0 when nothing runs
		agentsGroup.POST("/:id/stop", agents.StopAgentRun)
		agentsGroup.POST("/:id/sessions", agents.CreateSession)
		agentsGroup.GET("/:id/sessions", agents.ListSessions)
		agentsGroup.PUT("/:id/status", agents.SetAgentStatus)
		// Call grants (agent_relations): out-edges (who it may call) + in-edges (who may call it)
		agentsGroup.GET("/:id/relations", agents.GetAgentRelations)
		agentsGroup.PUT("/:id/relations", agents.SetAgentRelations)
		// Skill bindings (the agent side of agent_skill_bindings): the skills this agent may use
		agentsGroup.GET("/:id/skillbindings", agents.GetAgentSkillBindings)
		agentsGroup.PUT("/:id/skillbindings", agents.SetAgentSkillBindings)
		// Skills exposed as user commands ("/<name>") for the chat window: the same visibility as the agent's own skills
		agentsGroup.GET("/:id/commands", agents.ListAgentCommands)
		// Workspace file management (an agent is essentially a folder; skills/ is read-only)
		agentsGroup.GET("/:id/files", agents.ListAgentFiles)
		agentsGroup.GET("/:id/file", agents.ReadAgentFile)
		agentsGroup.PUT("/:id/file", agents.WriteAgentFile)
		agentsGroup.POST("/:id/file", agents.CreateAgentEntry)
		agentsGroup.DELETE("/:id/file", agents.DeleteAgentEntry)
		// Upload into the definition domain (multipart: file + dir; skills/ is refused, a clash saves as name-2.ext)
		agentsGroup.POST("/:id/upload", agents.UploadAgentFile)
		// Streaming download from the definition domain (read-only; drag-to-desktop takes the bytes from here)
		agentsGroup.GET("/:id/download", agents.DownloadAgentFile)
		// Session "workspace" browsing (run products; session_id locates the user and session directory)
		agentsGroup.GET("/:id/session/files", agents.ListSessionFiles)
		agentsGroup.GET("/:id/session/file", agents.ReadSessionFile)
		// Create a file / directory (subdirectories use the same call: put / in path and the storage layer creates the levels)
		agentsGroup.POST("/:id/session/file", agents.CreateSessionEntry)
		// Delete products (directories deleted recursively, unrecoverable): the UI must confirm
		agentsGroup.DELETE("/:id/session/file", agents.DeleteSessionEntry)
		// Rename / move (POST {path, new_path}): renaming cannot rely on "download and create again" -
		// a create takes content as a string, and binary files have no such path
		agentsGroup.POST("/:id/session/rename", agents.RenameSessionEntry)
		// Streaming download (binary / large files): same source and auth as session/file, Content-Disposition: attachment
		agentsGroup.GET("/:id/session/download", agents.DownloadSessionFile)
		// Download ticket: the front end gets schema/path/exp/sig and navigates to /filedl/:id (native browser progress / resume)
		agentsGroup.POST("/:id/session/download-ticket", agents.CreateDownloadTicket)
		// Recently changed / generated files (the workbench "newly generated" tab): root plus one level, newest mtime first
		agentsGroup.GET("/:id/session/recent", agents.RecentSessionFiles)
		// Chat "+": upload a file into uploads/ of the session workspace (multipart: file + optional session_id)
		agentsGroup.POST("/:id/session/upload", agents.UploadSessionFile)
		// Tool grants: read / write grants plus a preview of the tools actually in effect
		agentsGroup.GET("/:id/toolgrants", toolgrants.GetAgentToolGrants)
		agentsGroup.PUT("/:id/toolgrants", toolgrants.SetAgentToolGrants)
		agentsGroup.GET("/:id/effective-tools", toolgrants.GetAgentEffectiveTools)
		// Versions: private skills have no version of their own; they are frozen with this agent's version
		// (their content lives in versions/agents/<name>/v<n>/skills/** and their metadata in the skills column of the version row)
		agentsGroup.GET("/:id/versions", agents.ListAgentVersions)
		agentsGroup.POST("/:id/versions", agents.ReleaseAgentVersion)
		// Rollback = switch production to that version (the draft is untouched); apply = write that version back into the draft (production is untouched)
		agentsGroup.POST("/:id/versions/:version/rollback", agents.RollbackAgentVersion)
		agentsGroup.POST("/:id/versions/:version/apply", agents.ApplyAgentVersion)
		agentsGroup.DELETE("/:id/versions/:version", agents.DeleteAgentVersion)
	}

	// Built-in tool list (agent settings -> tool configuration): a separate path to avoid clashing with /agents/:id
	agentToolsGroup := router.Group("/agenttools")
	{
		agentToolsGroup.GET("/", agents.ListBuiltinTools)
	}

	// Tool grants: the referenceable catalog + custom tool groups
	toolCatalogGroup := router.Group("/toolcatalog")
	{
		toolCatalogGroup.GET("/", toolgrants.ListToolCatalog)
	}
	toolGroupsGroup := router.Group("/toolgroups")
	{
		toolGroupsGroup.GET("/", toolgrants.ListToolGroups)
		toolGroupsGroup.POST("/", toolgrants.UpsertToolGroup)
		toolGroupsGroup.DELETE("/:name", toolgrants.DeleteToolGroup)
	}
	// Tool grants: reference detail (the impact check before deleting / delisting)
	toolGrantsGroup := router.Group("/toolgrants")
	{
		toolGrantsGroup.GET("/usages", toolgrants.GetRefUsages)
	}

	// Skill routes (the skills table)
	skillsGroup := router.Group("/skills")
	{
		skillsGroup.POST("/", skills.ListSkills)
		skillsGroup.GET("/:id", skills.GetSkill)
		skillsGroup.POST("/create", skills.CreateSkill)
		skillsGroup.PUT("/:id", skills.UpdateSkill)
		skillsGroup.DELETE("/:id", skills.DeleteSkill)
		skillsGroup.PUT("/:id/status", skills.SetSkillStatus)
		// Visibility (the restricted switch + the allowlist of granted agents)
		skillsGroup.GET("/:id/visibility", skills.GetSkillVisibility)
		skillsGroup.PUT("/:id/visibility", skills.SetSkillVisibility)
		// Content directory file management (mirrors the agent workspace file API; the SKILL.md entry cannot be deleted)
		skillsGroup.GET("/:id/files", skills.ListSkillFiles)
		skillsGroup.GET("/:id/file", skills.ReadSkillFile)
		skillsGroup.PUT("/:id/file", skills.WriteSkillFile)
		skillsGroup.POST("/:id/file", skills.CreateSkillEntry)
		skillsGroup.DELETE("/:id/file", skills.DeleteSkillEntry)
		// Upload into the content directory (multipart: file + dir; no overwrite, a clash saves as name-2.ext)
		skillsGroup.POST("/:id/upload", skills.UploadSkillFile)
		// Streaming download of skill content (read-only; drag-to-desktop takes the bytes from here)
		skillsGroup.GET("/:id/download", skills.DownloadSkillFile)
		// Tool dependencies (a skill declares which tools it needs, so the agent side can inject them on demand)
		skillsGroup.GET("/:id/toolgrants", toolgrants.GetSkillToolGrants)
		skillsGroup.PUT("/:id/toolgrants", toolgrants.SetSkillToolGrants)
		// Versions: tenant-level skills only. A private skill has no version of its own - its version is that of the
		// agent it belongs to (see /agents/:id/versions and nexus/docs/terminology.md)
		skillsGroup.GET("/:id/versions", skills.ListSkillVersions)
		skillsGroup.POST("/:id/versions", skills.ReleaseSkillVersion)
		// Rollback = switch production to that version (the draft is untouched); apply = write that version back into the draft (production is untouched)
		skillsGroup.POST("/:id/versions/:version/rollback", skills.RollbackSkillVersion)
		skillsGroup.POST("/:id/versions/:version/apply", skills.ApplySkillVersion)
		skillsGroup.DELETE("/:id/versions/:version", skills.DeleteSkillVersion)
	}

	// Session routes (generic: agent / flow sessions)
	sessionsGroup := router.Group("/sessions")
	{
		sessionsGroup.GET("/latest", agents.GetLatestSession)
		sessionsGroup.GET("/:id/messages", agents.ListSessionMessages)
		sessionsGroup.GET("/:id/usage", agents.GetSessionUsage)
		sessionsGroup.DELETE("/:id", agents.DeleteSession)
	}

	// Service endpoint routes
	svcendpointsGroup := router.Group("/svcendpoints")
	{
		svcendpointsGroup.POST("/", svcendpoints.ListSvcEndpoints)
		svcendpointsGroup.GET("/:id", svcendpoints.GetSvcEndpoint)
		svcendpointsGroup.POST("/create", svcendpoints.CreateSvcEndpoint)
		svcendpointsGroup.PUT("/:id", svcendpoints.UpdateSvcEndpoint)
		svcendpointsGroup.DELETE("/:id", svcendpoints.DeleteSvcEndpoint)
		svcendpointsGroup.POST("/retract", svcendpoints.RetractSvcEndpoints)
		svcendpointsGroup.POST("/delete-by-target", svcendpoints.DeleteSvcEndpointsByTarget)
	}

	// Workflow routes
	flowsGroup := router.Group("/flows")
	{
		flowsGroup.POST("/", flows.ListFlows)
		flowsGroup.GET("/:id", flows.GetFlow)
		flowsGroup.POST("/create", flows.CreateFlow)
		flowsGroup.PUT("/:id", flows.UpdateFlow)
		flowsGroup.DELETE("/:id", flows.DeleteFlow)
		flowsGroup.GET("/:id/breakpoints", flows.GetBreakpoints)
		flowsGroup.POST("/:id/breakpoints", flows.SetBreakpoint)
		flowsGroup.GET("/:id/runs", flows.ListRuns)
		flowsGroup.GET("/:id/runs/:runId", flows.GetRunDetail)
		flowsGroup.DELETE("/:id/runs", flows.DeleteRuns)
		flowsGroup.DELETE("/:id/runs/:runId", flows.DeleteRun)
		flowsGroup.PUT("/:id/status", flows.SetFlowStatus)
		// Versions: a production run reads is_current (see repository.GetCurrentFlowVersion),
		// while the draft applies only to editor debugging and run_flow trial runs
		flowsGroup.GET("/:id/versions", flows.ListFlowVersions)
		flowsGroup.POST("/:id/versions", flows.ReleaseFlowVersion)
		// Rollback = switch production to that version (the canvas is untouched); apply = load that version into the canvas (production is untouched)
		flowsGroup.POST("/:id/versions/:version/rollback", flows.RollbackFlowVersion)
		flowsGroup.POST("/:id/versions/:version/apply", flows.ApplyFlowVersion)
		// Delete = history rows only. The current version (is_current) is the definition production calls use, so it is refused
		flowsGroup.DELETE("/:id/versions/:version", flows.DeleteFlowVersion)
	}

	// MCP connection routes
	mcpConnectionsGroup := router.Group("/mcpconnections")
	{
		mcpConnectionsGroup.POST("/", mcpconnections.ListMcpConnections)
		mcpConnectionsGroup.GET("/:id", mcpconnections.GetMcpConnection)
		mcpConnectionsGroup.POST("/create", mcpconnections.CreateMcpConnection)
		mcpConnectionsGroup.PUT("/:id", mcpconnections.UpdateMcpConnection)
		mcpConnectionsGroup.DELETE("/:id", mcpconnections.DeleteMcpConnection)
		mcpConnectionsGroup.GET("/:id/tools", mcpconnections.ListMcpConnectionTools)
		mcpConnectionsGroup.POST("/:id/refresh-tools", mcpconnections.RefreshMcpConnectionTools)
		mcpConnectionsGroup.PUT("/:id/enabled", mcpconnections.SetMcpConnectionEnabled)
	}

	// Plugins (on-disk Python plugins: discovery + tenant-level enable / configure / test)
	// Note: there is no "create plugin" API - a plugin registers itself by putting its code in {root_dir}/plugins/<kind>/<tool>/.
	pluginsGroup := router.Group("/plugins")
	{
		pluginsGroup.GET("/", plugins.ListPlugins)
		pluginsGroup.POST("/refresh", plugins.RefreshPlugins)
		pluginsGroup.PUT("/:name", plugins.UpdatePlugin)
		pluginsGroup.POST("/:name/test", plugins.TestPlugin)
	}

	// RPC connection routes
	rpcConnectionsGroup := router.Group("/rpcconnections")
	{
		rpcConnectionsGroup.POST("/", rpcconnections.ListRpcConnections)
		rpcConnectionsGroup.GET("/:id", rpcconnections.GetRpcConnection)
		rpcConnectionsGroup.POST("/create", rpcconnections.CreateRpcConnection)
		rpcConnectionsGroup.PUT("/:id", rpcconnections.UpdateRpcConnection)
		rpcConnectionsGroup.DELETE("/:id", rpcconnections.DeleteRpcConnection)
		rpcConnectionsGroup.GET("/:id/tools", rpcconnections.ListRpcConnectionTools)
		rpcConnectionsGroup.POST("/:id/refresh-tools", rpcconnections.RefreshRpcConnectionTools)
		rpcConnectionsGroup.PUT("/:id/enabled", rpcconnections.SetRpcConnectionEnabled)
	}

	// MCP server auth configuration routes (the mcp_server_configs table, one row per auth_type)
	mcpServerConfigGroup := router.Group("/mcpserverconfig")
	{
		mcpserverconfig.RegisterRoutes(mcpServerConfigGroup)
	}

	// RPC server auth configuration routes (the rpc_server_configs table, one row per auth_type)
	rpcServerConfigGroup := router.Group("/rpcserverconfig")
	{
		rpcserverconfig.RegisterRoutes(rpcServerConfigGroup)
	}

	// Server-side auth identity binding routes (the svc_auth_bindings table, shared by MCP and RPC)
	svcAuthBindingsGroup := router.Group("/svcauthbindings")
	{
		svcauthbindings.RegisterRoutes(svcAuthBindingsGroup)
	}

	// Tenant user list routes (for picking a user when creating a service credential)
	usersGroup := router.Group("/users")
	{
		users.RegisterRoutes(usersGroup)
	}

	// Tenant-level self-service quota (the tenant row of platform.quota_configs; the system ceiling is read-only and only validated).
	// It is /tenantquota rather than /users/quota because the latter would make a static segment and /users/:id
	// siblings in the route tree, and this file has split APIs apart for that clash more than once (see /me/password).
	tenantQuotaGroup := router.Group("/tenantquota")
	{
		tenantQuotaGroup.GET("", users.GetTenantQuota)
		tenantQuotaGroup.PUT("", users.UpdateTenantQuota)
	}

	// Self-service password change for the logged-in user
	// Note: /me/password stands alone rather than living under /users - that avoids the static-segment /
	// parameter-segment sibling clash with /users/:id/password (gin's route tree is sensitive to it).
	meGroup := router.Group("/me")
	{
		meGroup.POST("/password", users.ChangeMyPassword)
	}

	// Workflow run parameter routes (platform.flow_configs, system defaults + tenant configuration)
	flowconfigsGroup := router.Group("/flowconfigs")
	{
		flowconfigsGroup.GET("/", flowconfigs.Getflowconfigs)
		flowconfigsGroup.PUT("/", flowconfigs.UpdateTenantflowconfigs)
	}

	// Agent run parameters (platform.agent_configs, system defaults + tenant configuration)
	// The same semantics as flowconfigs, except this table does not accept -1 (all four fields are safety valves)
	agentconfigsGroup := router.Group("/agentconfigs")
	{
		agentconfigsGroup.GET("/", agentconfigs.GetAgentConfigs)
		agentconfigsGroup.PUT("/", agentconfigs.UpdateTenantAgentConfigs)
	}

	// Global variables ({schema}.global_vars)
	//
	// Global within a tenant: a workflow node input declares source_type: "global", and
	// agent and skill documents write ${global_vars.name}. A bundle carries only the reference names, never the values,
	// so installing into another tenant means filling them in once here - that is where "export and import never hardcode values" lands.
	// Secret values are always masked in responses and encrypted at rest; plaintext is read only while resolving them at run time.
	globalVarsGroup := router.Group("/globalvars")
	{
		globalVarsGroup.GET("/", globalvars.ListGlobalVars)
		globalVarsGroup.POST("/", globalvars.UpsertGlobalVar)
		// Reference query: the delete confirmation uses it to list what would break
		globalVarsGroup.GET("/:name/usage", globalvars.GlobalVarUsage)
		// References block deletion by default (only force=1 really deletes)
		globalVarsGroup.DELETE("/:name", globalvars.DeleteGlobalVar)
	}

	// Workflow metadata API (node capabilities, script languages, system limits)
	flowMetaGroup := router.Group("/flow")
	{
		flow.RegisterRoutes(flowMetaGroup)
	}
}
