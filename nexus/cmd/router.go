package main

import (
	"flame/nexus/admin"
	"flame/nexus/platform"
	mcpserver "flame/nexus/service/mcp/server"
	rpcserver "flame/nexus/service/rpc/server"
	"flame/nexus/service/websocket"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/redis"
	"flame/nexus/shared/svcpath"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// SetupRouter 设置API路由
func SetupRouter() *gin.Engine {
	// 创建Gin引擎
	router := gin.Default()

	// 添加请求日志中间件
	router.Use(func(c *gin.Context) {
		// /health 自己做就绪判定（返回 503 而不是通用的 500），让探针能区分「未就绪」
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}
		if issues := checkReadiness(config.Cfg); len(issues) != 0 {
			// 逐条进日志：业务调用方只拿到「暂时不可用」，排障的人拿到「哪条挂了、为什么」。
			xlog.Error("service not ready: %s", describeIssues(issues))
			// 就绪失败是暂时的：503 + Retry-After，别让客户端当成 500 那种内部 bug。
			c.Header("Retry-After", "5")
			xhttp.SendErrorResponse(c, http.StatusServiceUnavailable, "service_unavailable", "Service not ready")
			c.Abort()
			return
		}
		// 打印请求信息
		xlog.Info("[Request] %s %s %s", c.Request.Method, c.Request.URL.Path, c.ClientIP())
		// 继续处理请求
		c.Next()
	})

	// 健康检查：DB 与 Redis 都是强依赖，任一未连通即 503，判据与业务中间件同一份
	// （checkReadiness）。redis 字段只是把连接状态摊开给运维看，不单独决定状态码。
	router.GET("/health", func(c *gin.Context) {
		redisState := "disconnected"
		if redis.IsConnected() {
			redisState = "connected"
		}
		// 探针是运维面：这里如实给出「哪条强依赖没就绪」，但只给固定文案，
		// 原始错误（可能含内网地址）留在 StartRefresher 与中间件的日志里。
		issues := checkReadiness(config.Cfg)
		body := gin.H{
			"service": "nexus",
			"redis":   redisState,
		}
		if len(issues) != 0 {
			body["status"] = "unavailable"
			body["issues"] = issues
			c.JSON(http.StatusServiceUnavailable, body)
			return
		}
		body["status"] = "ok"
		c.JSON(http.StatusOK, body)
	})

	// 服务API路由组
	serviceGroup := router.Group("/service")

	// MCP 2.0 Server：租户由路径段携带（路径必填，无租户头）。
	//
	// 端点常驻，没有进程级开关：「是否对本租户开放 MCP」由租户级
	// <schema>.mcp_server_configs.is_active（按 auth_type 分 apikey / oauth）决定，
	// 未配置或未启用时认证会明确报错。原先的 config.mcp.enabled 只控制本路由是否注册，
	// 会出现「管理端配置面开着、端点却 404」的口径分裂，已删除。
	mcpHandler := mcpserver.NewMCPHandler()
	serviceGroup.POST("/mcp/:schema", func(c *gin.Context) {
		ctx := svcpath.WithSchema(c.Request.Context(), c.Param("schema"))
		mcpHandler(c.Writer, c.Request.WithContext(ctx))
	})

	// RPC Server：租户由路径段携带（路径必填，无租户头）。
	//
	// 同 MCP：端点常驻，「是否对本租户开放」由租户级 <schema>.rpc_server_configs.is_active
	// （按 auth_type 分 apikey / oauth）决定。原先的 config.rpc.enabled 只控制本路由是否注册，
	// 会造成「管理端配好了、端点却 404」的口径分裂，已删除。
	rpcHandler := rpcserver.NewRPCHandler()
	serviceGroup.POST("/rpc/:schema", func(c *gin.Context) {
		ctx := svcpath.WithSchema(c.Request.Context(), c.Param("schema"))
		rpcHandler(c.Writer, c.Request.WithContext(ctx))
	})

	// 管理路由组
	adminGroup := router.Group("/admin")
	{
		admin.SetupRoutes(adminGroup)
	}

	// 平台管理路由组（多租户管理台）：管理员管所有租户 / 用户只管自己名下租户。
	// 与 /admin 相互独立：平台 token 无 schema（进不了 /admin/*），
	// 租户 token 无 scope（进不了 /platform/*）。
	platformGroup := router.Group("/platform")
	{
		platform.SetupRoutes(platformGroup)
	}

	// ===== WebSocket 接入（与 HTTP 共用同一端口） =====
	wsHub := websocket.NewHub()
	go wsHub.Run()

	// /ws 业务组：鉴权 + echo 骨架
	wsGroup := router.Group("/ws")
	wsGroup.Use(auth.AuthMiddleware())
	{
		wsGroup.GET("", websocket.EchoHandler(wsHub))
		wsGroup.GET("/:channel", websocket.EchoHandler(wsHub))
	}

	// /debug 调试组：A+B 方案
	// A：路径带对象 id（flowId / agentId），连接即绑定
	// B：连接后首条消息发 token 鉴权（浏览器 WS 无法自定义握手头），再发单步控制指令
	// 鉴权在 websocket.debugConn 的首条消息中完成，故此处不挂 auth.AuthMiddleware
	debugGroup := router.Group("/debug")
	{
		debugGroup.GET("/flow/:flowId", websocket.DebugHandler(websocket.FlowDebugSource, "flowId"))
		debugGroup.GET("/agent/:agentId", websocket.DebugHandler(websocket.AgentDebugSource, "agentId"))
	}

	// 静态文件服务
	staticRoot := config.Cfg.Server.StaticRoot
	if staticRoot != "" {
		router.Static("/static", staticRoot)
		xlog.Info("Static file server mounted at /static -> %s", staticRoot)
	}

	return router
}
