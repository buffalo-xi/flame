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

// SetupRouter sets up the API routes.
func SetupRouter() *gin.Engine {
	// Create the Gin engine.
	router := gin.Default()

	// Request logging middleware.
	router.Use(func(c *gin.Context) {
		// /health does its own readiness decision (503 instead of the generic 500), so a probe can tell
		// "not ready" apart.
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}
		if issues := checkReadiness(config.Cfg); len(issues) != 0 {
			// One log line per issue: business callers only get "temporarily unavailable", while whoever
			// debugs gets "which dependency failed and why".
			xlog.Error("service not ready: %s", describeIssues(issues))
			// A readiness failure is temporary: 503 plus Retry-After, so clients do not read it as an
			// internal bug like a 500.
			c.Header("Retry-After", "5")
			xhttp.SendErrorResponse(c, http.StatusServiceUnavailable, "service_unavailable", "Service not ready")
			c.Abort()
			return
		}
		// Log the request.
		xlog.Info("[Request] %s %s %s", c.Request.Method, c.Request.URL.Path, c.ClientIP())
		// Continue handling the request.
		c.Next()
	})

	// Health check: both the DB and Redis are hard dependencies, so either being unreachable means 503,
	// using the same predicate as the business middleware (checkReadiness). The redis field only exposes
	// the connection state to operators; it does not decide the status code on its own.
	router.GET("/health", func(c *gin.Context) {
		redisState := "disconnected"
		if redis.IsConnected() {
			redisState = "connected"
		}
		// The probe is an operator-facing surface: it reports faithfully "which hard dependency is not
		// ready", but only with fixed copy — the raw error (which may contain internal addresses) stays in
		// the logs of StartRefresher and the middleware.
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

	// Service API route group.
	serviceGroup := router.Group("/service")

	// MCP 2.0 Server: the tenant is carried by a path segment (the path is mandatory, there is no tenant header).
	//
	// The endpoint is always mounted and has no process-level switch: "is MCP open for this tenant" is
	// decided by the tenant-level <schema>.mcp_server_configs.is_active (split by auth_type into apikey /
	// oauth), and authentication reports an explicit error when it is unconfigured or disabled. The old
	// config.mcp.enabled only controlled whether this route was registered, which produced the split of
	// "the admin config page says open, the endpoint returns 404", so it was removed.
	mcpHandler := mcpserver.NewMCPHandler()
	serviceGroup.POST("/mcp/:schema", func(c *gin.Context) {
		ctx := svcpath.WithSchema(c.Request.Context(), c.Param("schema"))
		mcpHandler(c.Writer, c.Request.WithContext(ctx))
	})

	// RPC Server: the tenant is carried by a path segment (the path is mandatory, there is no tenant header).
	//
	// Same as MCP: the endpoint is always mounted and "is it open for this tenant" is decided by the
	// tenant-level <schema>.rpc_server_configs.is_active (split by auth_type into apikey / oauth). The old
	// config.rpc.enabled only controlled whether this route was registered, which produced the split of
	// "the admin side is configured, the endpoint returns 404", so it was removed.
	rpcHandler := rpcserver.NewRPCHandler()
	serviceGroup.POST("/rpc/:schema", func(c *gin.Context) {
		ctx := svcpath.WithSchema(c.Request.Context(), c.Param("schema"))
		rpcHandler(c.Writer, c.Request.WithContext(ctx))
	})

	// Admin route group.
	adminGroup := router.Group("/admin")
	{
		admin.SetupRoutes(adminGroup)
	}

	// Platform route group (the multi-tenant console): an administrator manages every tenant, a user only
	// the tenants under their own name.
	// It is independent of /admin: a platform token has no schema (it cannot enter /admin/*), and a tenant
	// token has no scope (it cannot enter /platform/*).
	platformGroup := router.Group("/platform")
	{
		platform.SetupRoutes(platformGroup)
	}

	// ===== WebSocket access (shares the same port as HTTP) =====
	wsHub := websocket.NewHub()
	go wsHub.Run()

	// The /ws business group: authentication plus the echo skeleton.
	wsGroup := router.Group("/ws")
	wsGroup.Use(auth.AuthMiddleware())
	{
		wsGroup.GET("", websocket.EchoHandler(wsHub))
		wsGroup.GET("/:channel", websocket.EchoHandler(wsHub))
	}

	// The /debug group: plan A+B.
	// A: the path carries the object id (flowId / agentId), so connecting binds the object.
	// B: after connecting, the first message carries the token (a browser WS cannot set handshake
	// headers), and control commands follow.
	// Authentication happens inside the first message of websocket.debugConn, so auth.AuthMiddleware is
	// deliberately not mounted here.
	debugGroup := router.Group("/debug")
	{
		debugGroup.GET("/flow/:flowId", websocket.DebugHandler(websocket.FlowDebugSource, "flowId"))
		debugGroup.GET("/agent/:agentId", websocket.DebugHandler(websocket.AgentDebugSource, "agentId"))
	}

	// Static file service.
	staticRoot := config.Cfg.Server.StaticRoot
	if staticRoot != "" {
		router.Static("/static", staticRoot)
		xlog.Info("Static file server mounted at /static -> %s", staticRoot)
	}

	return router
}
