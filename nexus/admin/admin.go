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

// SetupRoutes 设置管理路由
func SetupRoutes(router *gin.RouterGroup) {
	// 登录路由（不需要认证中间件）
	router.POST("/login", auth.Login)

	// LiteLLM 网关测试路由（不需要认证，仅开发/调试用）
	gatewayTestGroup := router.Group("/gatewaytest")
	{
		gatewayTestGroup.POST("/run", gatewaytest.RunGatewayTest)
		gatewayTestGroup.POST("/provision", gatewaytest.ProvisionTest)
		gatewayTestGroup.POST("/deprovision", gatewaytest.DeprovisionTest)
		gatewayTestGroup.POST("/generate", gatewaytest.GenerateTest)
	}

	// 工作区文件「导航式下载」：浏览器的 window.location 带不了 Authorization 头，
	// 靠 URL 里的短时 HMAC 票（见 admin/agents/download_ticket.go）鉴权。
	router.GET("/filedl/:id", agents.DownloadByTicket)

	// 添加认证中间件
	router.Use(auth.AuthMiddleware())

	// 自定义工具（{schema}.custom_tools）：租户自定义的 custom__* 工具。
	// 与插件页分开：插件是"启用磁盘上的实现"，这里是"新建自己的工具"。
	customToolsGroup := router.Group("/customtools")
	{
		customToolsGroup.GET("/", customtools.ListCustomTools)
		customToolsGroup.POST("/create", customtools.CreateCustomTool)
		customToolsGroup.GET("/:name", customtools.GetCustomTool)
		customToolsGroup.PUT("/:name", customtools.UpdateCustomTool)
		customToolsGroup.DELETE("/:name", customtools.DeleteCustomTool)
		// 试跑：与线上调用同一条执行内核，把 stdout / stderr / 退出码回给管理员。
		// gin 支持静态段（/create）与参数段（/:name/run）做兄弟节点；若启动报路由冲突，
		// 改成 POST /customtools/run（name 放进 body）即可，前端只动一处。
		customToolsGroup.POST("/:name/run", customtools.RunCustomTool)
		// 内容目录文件管理（与技能内容目录同构，两处差别见 files.go 顶部注释）：
		//   TOOL.json 可读不可写不可删（契约只有一个写口），入口文件不可删。
		customToolsGroup.GET("/:name/files", customtools.ListToolFiles)
		customToolsGroup.GET("/:name/file", customtools.ReadToolFile)
		customToolsGroup.PUT("/:name/file", customtools.WriteToolFile)
		customToolsGroup.POST("/:name/file", customtools.CreateToolEntry)
		customToolsGroup.DELETE("/:name/file", customtools.DeleteToolEntry)
		// 上传到内容目录（multipart：file + dir；同名不覆盖，另存 name-2.ext）
		customToolsGroup.POST("/:name/upload", customtools.UploadToolFile)
		// 文件流式下载（只读；拖出到桌面取字节，需带 ?path=）
		customToolsGroup.GET("/:name/download", customtools.DownloadToolFile)
		// 版本：正式运行读 is_current 那一份（versions/tools/<name>/v<n>），
		// 草稿只在编辑页调试时生效。改动口径见 shared/versions 与 nexus/docs/terminology.md
		customToolsGroup.GET("/:name/versions", customtools.ListToolVersions)
		// 发布 = 把当前草稿目录冻结成新版本并设为当前
		customToolsGroup.POST("/:name/versions", customtools.ReleaseToolVersion)
		// 回滚 = 线上切到该版本（不动草稿）；载入 = 该版本写回草稿（不动线上）
		customToolsGroup.POST("/:name/versions/:version/rollback", customtools.RollbackToolVersion)
		// 载入 = 该版本内容写回草稿（不动线上）；删除 = 只清历史（当前版拒绝）
		customToolsGroup.POST("/:name/versions/:version/apply", customtools.ApplyToolVersion)
		customToolsGroup.DELETE("/:name/versions/:version", customtools.DeleteToolVersion)
	}

	// 跨租户资源分发（平台审核 + 复制式安装）：
	//   - 上架：把本租户的技能提交成平台资产（进 pending，等平台审核）
	//   - 安装：从平台资产复制成本租户实体（先文件后 DB，冲突回 409 + 建议名）
	//   - 我的上架 / 已安装：状态与「有新版本」提示
	// 平台 schema 由 config.database.platform_schema 决定，表名不接受请求体输入。
	// 方案见 docs/asset-sharing.md。
	shareGroup := router.Group("/share")
	{
		shareGroup.GET("/assets", share.ListAssets)
		shareGroup.GET("/assets/:id", share.GetAsset)
		shareGroup.POST("/listings", share.CreateListing)
		shareGroup.GET("/mine", share.ListMine)
		shareGroup.POST("/mine/:id/withdraw", share.Withdraw)
		// 我的上架：直接删除（不经过撤回流转）与一键清空未过审的
		shareGroup.DELETE("/mine", share.ClearMine)
		shareGroup.DELETE("/mine/:id", share.DeleteMine)
		shareGroup.POST("/install", share.Install)
		// 资产包导出 / 导入：卡片上的「导出」直接下载 zip（不落平台快照、不进审核），
		// 「导入」上传 zip 后走同一套安装逻辑装成本租户实体。见 docs/asset-sharing.md。
		shareGroup.GET("/export", share.Export)
		shareGroup.POST("/import", share.Import)
		// 导入前预检：只读解析包，列出依赖（技能 / 工具 / 被调用智能体 / 能力）在本租户是否存在，
		// 缺什么在动手前就能看见，而不是装完读 warnings
		shareGroup.POST("/import/inspect", share.Inspect)
		// 市场安装前预检（只读）：同名实体 / 依赖 / 能力一次看全，与上传导入的预检同源同判据
		shareGroup.POST("/install/inspect", share.InspectAsset)
		shareGroup.GET("/installs", share.ListInstalls)
		// 只删安装流水，不卸载实体（卸载走各自的删除接口）
		shareGroup.DELETE("/installs", share.ClearInstalls)
		shareGroup.DELETE("/installs/:asset_id/:installed_id", share.DeleteInstall)

		// 远端市场（pull-through）：目录只看平台配好且对本租户可见的对端；
		// 拉取由服务端持令牌完成 —— 令牌始终只在服务端，前端拿不到。
		// 对端地址会以只读字段下发（租户端标注「这个站在哪」用），但它不参与
		// 任何出站请求的构造：拉取仍然按 peer 名走，没有 SSRF 面。
		// 拉取与安装拆开：拉取按「资产」去重，安装才按「租户」。
		shareGroup.GET("/remote-assets", share.RemoteAssets)
		shareGroup.POST("/pull-asset", share.PullAsset)
	}

	// LLM服务商管理路由
	llmProvidersGroup := router.Group("/llmproviders")
	{
		llmProvidersGroup.POST("/", llmproviders.ListLLMProviders)
		llmProvidersGroup.GET("/:id", llmproviders.GetLLMProvider)
		llmProvidersGroup.POST("/create", llmproviders.CreateLLMProvider)
		llmProvidersGroup.PUT("/:id", llmproviders.UpdateLLMProvider)
		llmProvidersGroup.DELETE("/:id", llmproviders.DeleteLLMProvider)
	}

	// LLM服务商预设路由（系统级，全局共享）
	llmProviderPresetsGroup := router.Group("/llmproviderpresets")
	{
		llmProviderPresetsGroup.GET("/", llmproviderpresets.ListLLMProviderPresets)
	}

	// LLM模型管理路由
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
		// 把本地 LLM 配置逐个对齐到 LiteLLM 网关，并清理网关上的多余记录
		llmModelsGroup.POST("/sync", llmmodels.SyncLLMModels)
	}

	// 运行台账：本租户当前在跑的 agent + flow（含其它实例，Redis 台账）。
	// 终止走广播：运行只存在于跑它的那台机器的内存里，向所有实例发停止意图最稳。
	runningGroup := router.Group("/running")
	{
		runningGroup.GET("", runs.ListRunning)
		runningGroup.POST("/stop", runs.StopRun)
		// 历史运行：跨流程查询 / 按条件清理 / CSV 导出。
		// 与上面两个接口是两套数据：台账在 Redis、只活在运行期间；
		// 这里读的是 PG 的 flow_run_records，跑完才落库、长期保留。
		runningGroup.GET("/history", runs.ListHistory)
		runningGroup.DELETE("/history", runs.CleanupHistory)
		runningGroup.GET("/history/export", runs.ExportHistory)
	}

	// 智能体管理路由（agents 表）
	agentsGroup := router.Group("/agents")
	{
		agentsGroup.POST("/", agents.ListAgents)
		agentsGroup.GET("/:id", agents.GetAgent)
		agentsGroup.POST("/create", agents.CreateAgent)
		agentsGroup.PUT("/:id", agents.UpdateAgent)
		agentsGroup.DELETE("/:id", agents.DeleteAgent)
		agentsGroup.POST("/:id/chat", agents.ChatAgent)
		// 终止正在运行的 agent（按会话或按 run）；幂等，没在跑时返回 stopped:0
		agentsGroup.POST("/:id/stop", agents.StopAgentRun)
		agentsGroup.POST("/:id/sessions", agents.CreateSession)
		agentsGroup.GET("/:id/sessions", agents.ListSessions)
		agentsGroup.PUT("/:id/status", agents.SetAgentStatus)
		// 调用授权（agent_relations）：出边（可调用谁）+ 入边（谁能调用它）
		agentsGroup.GET("/:id/relations", agents.GetAgentRelations)
		agentsGroup.PUT("/:id/relations", agents.SetAgentRelations)
		// 技能绑定（agent_skill_bindings 的 agent 侧）：该 agent 可用的技能
		agentsGroup.GET("/:id/skillbindings", agents.GetAgentSkillBindings)
		agentsGroup.PUT("/:id/skillbindings", agents.SetAgentSkillBindings)
		// 工作区文件管理（agent 的本质是一个文件夹；skills/ 只读）
		agentsGroup.GET("/:id/files", agents.ListAgentFiles)
		agentsGroup.GET("/:id/file", agents.ReadAgentFile)
		agentsGroup.PUT("/:id/file", agents.WriteAgentFile)
		agentsGroup.POST("/:id/file", agents.CreateAgentEntry)
		agentsGroup.DELETE("/:id/file", agents.DeleteAgentEntry)
		// 上传到定义域（multipart：file + dir；skills/ 拒绝，同名另存 name-2.ext）
		agentsGroup.POST("/:id/upload", agents.UploadAgentFile)
		// 定义域文件流式下载（只读；拖出到桌面用它取字节）
		agentsGroup.GET("/:id/download", agents.DownloadAgentFile)
		// 会话「工作区」文件浏览（运行产物；用 session_id 定位 user + 会话目录）
		agentsGroup.GET("/:id/session/files", agents.ListSessionFiles)
		agentsGroup.GET("/:id/session/file", agents.ReadSessionFile)
		// 新建文件 / 目录（子目录同样走这条：path 里带 / 即可，中间层由存储层建出）
		agentsGroup.POST("/:id/session/file", agents.CreateSessionEntry)
		// 删除产物（目录递归删除、不可恢复）：界面侧必须二次确认
		agentsGroup.DELETE("/:id/session/file", agents.DeleteSessionEntry)
		// 重命名 / 移动（POST {path, new_path}）：改名不能靠「下载再新建」——
		// 新建的 content 是字符串，二进制文件根本没这条路
		agentsGroup.POST("/:id/session/rename", agents.RenameSessionEntry)
		// 流式下载（二进制 / 大文件）：与 session/file 同源同鉴权，Content-Disposition: attachment
		agentsGroup.GET("/:id/session/download", agents.DownloadSessionFile)
		// 下载票：前端拿到 schema/path/exp/sig 后拼 /filedl/:id 导航下载（浏览器原生进度/续传）
		agentsGroup.POST("/:id/session/download-ticket", agents.CreateDownloadTicket)
		// 最近改动/生成的文件（工作台「新生成的文件」标签）：根目录 + 一层子目录，按 mtime 倒序
		agentsGroup.GET("/:id/session/recent", agents.RecentSessionFiles)
		// chat 输入框「+」：把文件传到会话工作区 uploads/（multipart：file + 可选 session_id）
		agentsGroup.POST("/:id/session/upload", agents.UploadSessionFile)
		// 工具授权（工具授权体系）：授权项读写 + 实际生效工具预览
		agentsGroup.GET("/:id/toolgrants", toolgrants.GetAgentToolGrants)
		agentsGroup.PUT("/:id/toolgrants", toolgrants.SetAgentToolGrants)
		agentsGroup.GET("/:id/effective-tools", toolgrants.GetAgentEffectiveTools)
		// 版本：私有技能没有独立版本号，随这份 agent 版本一起冻结
		// （内容在 versions/agents/<name>/v<n>/skills/**，元数据在版本行 skills 列）
		agentsGroup.GET("/:id/versions", agents.ListAgentVersions)
		agentsGroup.POST("/:id/versions", agents.ReleaseAgentVersion)
		// 回滚 = 线上切到该版本（不动草稿）；载入 = 该版本写回草稿（不动线上）
		agentsGroup.POST("/:id/versions/:version/rollback", agents.RollbackAgentVersion)
		agentsGroup.POST("/:id/versions/:version/apply", agents.ApplyAgentVersion)
		agentsGroup.DELETE("/:id/versions/:version", agents.DeleteAgentVersion)
	}

	// 内置工具清单（智能体设置 → 工具配置）：独立路径，避免与 /agents/:id 冲突
	agentToolsGroup := router.Group("/agenttools")
	{
		agentToolsGroup.GET("/", agents.ListBuiltinTools)
	}

	// 工具授权：可引用目录 + 自定义工具组
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
	// 工具授权：引用明细（删除 / 下线前的影响面查询）
	toolGrantsGroup := router.Group("/toolgrants")
	{
		toolGrantsGroup.GET("/usages", toolgrants.GetRefUsages)
	}

	// 技能管理路由（skills 表）
	skillsGroup := router.Group("/skills")
	{
		skillsGroup.POST("/", skills.ListSkills)
		skillsGroup.GET("/:id", skills.GetSkill)
		skillsGroup.POST("/create", skills.CreateSkill)
		skillsGroup.PUT("/:id", skills.UpdateSkill)
		skillsGroup.DELETE("/:id", skills.DeleteSkill)
		skillsGroup.PUT("/:id/status", skills.SetSkillStatus)
		// 可见性（restricted 开关 + 授权 agent 白名单）
		skillsGroup.GET("/:id/visibility", skills.GetSkillVisibility)
		skillsGroup.PUT("/:id/visibility", skills.SetSkillVisibility)
		// 内容目录文件管理（与 agent 工作区文件接口同构；入口 SKILL.md 不可删除）
		skillsGroup.GET("/:id/files", skills.ListSkillFiles)
		skillsGroup.GET("/:id/file", skills.ReadSkillFile)
		skillsGroup.PUT("/:id/file", skills.WriteSkillFile)
		skillsGroup.POST("/:id/file", skills.CreateSkillEntry)
		skillsGroup.DELETE("/:id/file", skills.DeleteSkillEntry)
		// 上传到内容目录（multipart：file + dir；同名不覆盖，另存 name-2.ext）
		skillsGroup.POST("/:id/upload", skills.UploadSkillFile)
		// 技能内容文件流式下载（只读；拖出到桌面用它取字节）
		skillsGroup.GET("/:id/download", skills.DownloadSkillFile)
		// 工具依赖（skill 声明「需要哪些工具」，供 agent 侧「按需注入」使用）
		skillsGroup.GET("/:id/toolgrants", toolgrants.GetSkillToolGrants)
		skillsGroup.PUT("/:id/toolgrants", toolgrants.SetSkillToolGrants)
		// 版本：只覆盖租户级技能。私有技能没有独立版本号 —— 它的版本就是所属
		// agent 的版本（见 /agents/:id/versions 与 nexus/docs/terminology.md）
		skillsGroup.GET("/:id/versions", skills.ListSkillVersions)
		skillsGroup.POST("/:id/versions", skills.ReleaseSkillVersion)
		// 回滚 = 线上切到该版本（不动草稿）；载入 = 该版本写回草稿（不动线上）
		skillsGroup.POST("/:id/versions/:version/rollback", skills.RollbackSkillVersion)
		skillsGroup.POST("/:id/versions/:version/apply", skills.ApplySkillVersion)
		skillsGroup.DELETE("/:id/versions/:version", skills.DeleteSkillVersion)
	}

	// 会话管理路由（泛化：agent/flow 会话）
	sessionsGroup := router.Group("/sessions")
	{
		sessionsGroup.GET("/latest", agents.GetLatestSession)
		sessionsGroup.GET("/:id/messages", agents.ListSessionMessages)
		sessionsGroup.GET("/:id/usage", agents.GetSessionUsage)
		sessionsGroup.DELETE("/:id", agents.DeleteSession)
	}

	// 服务端点管理路由
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

	// 工作流实例管理路由
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
		// 版本：正式运行读 is_current（见 repository.GetCurrentFlowVersion），
		// 草稿只在编辑页调试/run_flow 试跑时生效
		flowsGroup.GET("/:id/versions", flows.ListFlowVersions)
		flowsGroup.POST("/:id/versions", flows.ReleaseFlowVersion)
		// 回滚 = 线上切到该版本（不动画布）；apply = 该版本载入画布（不动线上）
		flowsGroup.POST("/:id/versions/:version/rollback", flows.RollbackFlowVersion)
		flowsGroup.POST("/:id/versions/:version/apply", flows.ApplyFlowVersion)
		// 删除 = 只清历史行。当前版本（is_current）是正式调用的定义来源，拒绝删除
		flowsGroup.DELETE("/:id/versions/:version", flows.DeleteFlowVersion)
	}

	// MCP 连接管理路由
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

	// 插件（磁盘 Python 插件：发现 + 租户级启用/配置/试跑）
	// 注意：没有「创建插件」接口 —— 插件靠把代码放进 {root_dir}/plugins/<kind>/<tool>/ 自注册。
	pluginsGroup := router.Group("/plugins")
	{
		pluginsGroup.GET("/", plugins.ListPlugins)
		pluginsGroup.POST("/refresh", plugins.RefreshPlugins)
		pluginsGroup.PUT("/:name", plugins.UpdatePlugin)
		pluginsGroup.POST("/:name/test", plugins.TestPlugin)
	}

	// RPC 连接管理路由
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

	// MCP 服务鉴权配置路由（mcp_server_configs 表，按 auth_type 分行）
	mcpServerConfigGroup := router.Group("/mcpserverconfig")
	{
		mcpserverconfig.RegisterRoutes(mcpServerConfigGroup)
	}

	// RPC 服务鉴权配置路由（rpc_server_configs 表，按 auth_type 分行）
	rpcServerConfigGroup := router.Group("/rpcserverconfig")
	{
		rpcserverconfig.RegisterRoutes(rpcServerConfigGroup)
	}

	// 服务端认证身份绑定路由（svc_auth_bindings 表，MCP/RPC 共用）
	svcAuthBindingsGroup := router.Group("/svcauthbindings")
	{
		svcauthbindings.RegisterRoutes(svcAuthBindingsGroup)
	}

	// 租户用户列表路由（供服务凭证选择用户）
	usersGroup := router.Group("/users")
	{
		users.RegisterRoutes(usersGroup)
	}

	// 租户级配额自助（platform.quota_configs 的租户行；系统顶只读、只做校验）。
	// 独立成 /tenantquota 而不是 /users/quota：后者会与 /users/:id 形成
	// "静态段 / 参数段同级"，本文件已多次因这类冲突把接口拆开（见 /me/password）。
	tenantQuotaGroup := router.Group("/tenantquota")
	{
		tenantQuotaGroup.GET("", users.GetTenantQuota)
		tenantQuotaGroup.PUT("", users.UpdateTenantQuota)
	}

	// 当前登录用户自助修改密码
	// 注意：独立成 /me/password，不放进 /users —— 避免与 /users/:id/password 这种
	// 静态段与参数段同级的路由冲突（gin 的路由树对此敏感）。
	meGroup := router.Group("/me")
	{
		meGroup.POST("/password", users.ChangeMyPassword)
	}

	// 工作流运行参数管理路由（platform.flow_configs，系统缺省 + 租户配置）
	flowconfigsGroup := router.Group("/flowconfigs")
	{
		flowconfigsGroup.GET("/", flowconfigs.Getflowconfigs)
		flowconfigsGroup.PUT("/", flowconfigs.UpdateTenantflowconfigs)
	}

	// 智能体运行参数（platform.agent_configs，系统缺省 + 租户配置）
	// 与 flowconfigs 同一套语义，区别是本表不接受 -1（四个字段都是安全阀）
	agentconfigsGroup := router.Group("/agentconfigs")
	{
		agentconfigsGroup.GET("/", agentconfigs.GetAgentConfigs)
		agentconfigsGroup.PUT("/", agentconfigs.UpdateTenantAgentConfigs)
	}

	// 全局变量（{schema}.global_vars）
	//
	// 本租户内全局：工作流的节点输入变量里声明 source_type: "global"、
	// 智能体/技能文档写 ${global_vars.名字}。资产包只带引用名不带值，
	// 装到别的租户只需在这里填一次 —— 这是「导出导入不把值写死」的落点。
	// secret 类型的值一律掩码出参、加密落库；明文只在运行期解析时取。
	globalVarsGroup := router.Group("/globalvars")
	{
		globalVarsGroup.GET("/", globalvars.ListGlobalVars)
		globalVarsGroup.POST("/", globalvars.UpsertGlobalVar)
		// 引用面查询：删除确认框用它列出「谁会坏」
		globalVarsGroup.GET("/:name/usage", globalvars.GlobalVarUsage)
		// 默认拦引用（force=1 才真删）
		globalVarsGroup.DELETE("/:name", globalvars.DeleteGlobalVar)
	}

	// 工作流元数据接口（节点能力、脚本语言、系统限制）
	flowMetaGroup := router.Group("/flow")
	{
		flow.RegisterRoutes(flowMetaGroup)
	}
}
