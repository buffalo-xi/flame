package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"flame/nexus/admin/share"
	"flame/nexus/service/agent"
	agentTools "flame/nexus/service/agent/tools"
	"flame/nexus/service/flow/executors"
	"flame/nexus/service/llm/gateway"
	"flame/nexus/service/llm/litellm"
	"flame/nexus/service/mcp/client"
	"flame/nexus/service/plugin"
	"flame/nexus/shared/browserinfo"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/redis"
	"flame/nexus/shared/sandboxinfo"
	"flame/tools/code"
	"flame/tools/sandbox"
	"flame/tools/xlog"
	"flame/tools/xredis"
	"flame/tools/xregistry"
)

// litellmGW is the LiteLLM gateway instance (may be nil, meaning disabled or not initialized).
// It is created by main at startup and read by checkReadiness and friends; its connection state is
// maintained by a background goroutine.
var litellmGW *litellm.ProxyGateway

func main() {

	// Load the configuration.
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		return
	}
	config.Cfg = cfg

	// Initialize agent storage.
	if err := agent.InitStorage(); err != nil {
		xlog.Error("Failed to initialize agent storage: %v", err)
		return
	}

	// Initialize the logging system.
	if err := xlog.Init("logs", "nexus"); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}
	defer xlog.Close()

	// The data root may be relative (storage.root_dir is ./data in config.example.yaml) and is therefore resolved against
	// the startup cwd; printing the value that is actually in effect turns "the tenant data is gone" into an obvious
	// "this is not the directory I meant".
	xlog.Info("nexus: storage root=%s driver=%s", cfg.Storage.RootDir, cfg.Storage.Driver)

	// Initialize the Redis client.
	myredisConfig := &xredis.Config{
		Mode:     xredis.RedisMode(cfg.Redis.Mode),
		Addrs:    cfg.Redis.Addrs,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
		Timeout:  time.Duration(cfg.Redis.Timeout) * time.Second,
		Prefix:   cfg.Redis.Prefix,
	}

	// Service registration: create the registrar. Registration / deregistration is driven by the
	// goroutine below according to database readiness.
	var registrar xregistry.Registrar
	if cfg.Registry.Enabled {
		rc := &xregistry.RegistrarConfig{
			Type:        xregistry.Type(cfg.Registry.Type),
			Address:     cfg.Registry.Address,
			ServiceName: cfg.Registry.ServiceName,
			ServiceAddr: cfg.Registry.ServiceAddr,
			ServicePort: cfg.Registry.ServicePort,
			Tags:        cfg.Registry.Tags,
		}
		reg, err := xregistry.NewRegistrar(rc)
		if err != nil {
			xlog.Error("Warning: Failed to create registrar: %v", err)
		} else {
			registrar = reg
		}
	}

	// DSN of LiteLLM's own Postgres database (dbname is litellm.database in the yaml).
	// It is only precomputed at startup; the connection is opened later by the reconnect goroutine below
	// (litellmGW is initialized with db=nil).
	litellmDBDSN := ""
	if cfg.LiteLLM.Enabled && cfg.LiteLLM.URL != "" && cfg.LiteLLM.MasterKey != "" {
		litellmDBDSN = cfg.Database.GetDSN()
		if cfg.LiteLLM.Database != "" {
			litellmDBDSN = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai application_name=nexus_service",
				cfg.Database.Host, cfg.Database.Port, cfg.Database.Username, cfg.Database.Password, cfg.LiteLLM.Database)
		}
		// db=nil: the connection is not opened at startup, the reconnect goroutine injects it.
		litellmGW = litellm.New(cfg.LiteLLM.URL, cfg.LiteLLM.MasterKey, nil)
		gateway.SetCurrent(litellmGW)
		xlog.Info("LiteLLM gateway initialized: %s", cfg.LiteLLM.URL)
	}

	// Startup cleanup, run once the database is ready (both steps query it):
	//   - runs/: the one-shot scratch area of session-less calls. Only directories untouched for more than
	//     24 hours are collected, never the whole tree — with several instances sharing the same storage,
	//     deleting the whole tree would take away the scratch areas other instances are still using
	//     (see runsTTL in service/agent/janitor.go);
	//   - platform-assets/: orphan snapshots that are not in the table, plus old versions that fell out of
	//     the retention window.
	go func() {
		for i := 0; i < 60; i++ {
			if db.IsConnected() {
				agent.CleanupOrphanRuns()
				share.CleanupSnapshots()
				return
			}
			time.Sleep(2 * time.Second)
		}
		xlog.Info("startup cleanup skipped: database not ready")
	}()

	// Database connection check and reconnect.
	go func() {
		for {
			if !db.CheckDatabaseConnection() {
				xlog.Info("Attempting to reconnect to database...")
				db.ReconnectDatabase()
			}
			// LiteLLM database connection check and reconnect: on a failed Ping, reconnect and inject.
			if litellmGW != nil && litellmDBDSN != "" {
				if err := litellmGW.Ping(); err != nil {
					newDB, derr := db.OpenDatabase(litellmDBDSN)
					if derr != nil {
						xlog.Error("Failed to connect litellm database: %v", derr)
					} else {
						litellmGW.SetDB(newDB)
						xlog.Info("litellm database connected successfully")
					}
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()

	// Redis connection check and reconnect (a hard dependency, with no switch). This goroutine also
	// performs the first connection.
	//
	// A failure to connect does not block startup, but until it succeeds checkReadiness keeps reporting
	// redis as not ready and the middleware answers 503 to every business request — "no connection means
	// no service", rather than taking traffic first and degrading later.
	//
	// Log throttling (one line per 60s): while unavailable this branch is taken every 5s, and without
	// throttling the console would flood and drown the actual error.
	go func() {
		var lastLogAt time.Time
		for {
			if !redis.CheckRedisConnection() {
				// NewClient already Pings internally and returns (nil, err) on failure; the error must be
				// checked before assigning, otherwise a failure would clear redis.Client to nil and even a
				// transient blip would drop the existing connection object.
				client, cerr := xredis.NewClient(myredisConfig)
				if cerr != nil {
					// "reconnecting" and the failure reason are merged into one line: split in two they
					// would suppress each other, leaving either a flood or only "reconnecting" and never
					// why the connection fails.
					if time.Since(lastLogAt) >= time.Minute {
						lastLogAt = time.Now()
						xlog.Info("reconnect to redis failed: %v (retrying every 5s)", cerr)
					}
				} else {
					if old := redis.Client; old != nil {
						_ = old.Close() // release the old client's connection pool, so reconnects do not pile up
					}
					redis.Client = client
					redis.CheckRedisConnection() // refresh the state immediately, so the probe does not read "unavailable" until the first Ping cycle
					xlog.Info("create Redis client successfully")
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()

	// Service registration check and re-registration: register only when both the database and Redis are
	// ready; deregister proactively when either drops (so the gateway stops routing here) and register
	// again once it recovers. Both are hard dependencies — with Redis down the cache could fall back to the
	// database, but the cross-instance ledger and the stop broadcast would break, and that half-broken
	// state should not keep taking traffic.
	// The new Registrar has no IsServiceRegistered, so local state tracks whether it is currently registered.
	go func() {
		time.Sleep(5 * time.Second)
		registered := false
		for {
			ready := db.IsConnected() && redis.IsConnected()
			if cfg.Registry.Enabled && registrar != nil {
				if ready {
					if !registered {
						if err := registrar.Register(); err != nil {
							xlog.Info("Warning: Failed to register service: %v", err)
						} else {
							registered = true
							xlog.Info("Service registered successfully")
						}
					}
				} else if registered {
					// The message says which dependency dropped, otherwise debugging is guesswork.
					reason := "database disconnected"
					if db.IsConnected() {
						reason = "redis disconnected"
					}
					xlog.Info("%s, deregistering service...", reason)
					if err := registrar.Deregister(); err != nil {
						xlog.Info("Warning: Failed to deregister service: %v", err)
					} else {
						registered = false
						xlog.Info("Service deregistered successfully")
					}
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()

	// Check whether the cutoff date has passed.
	cutoffDate := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if time.Now().UTC().After(cutoffDate) {
		xlog.Error("Program aborted")
		return
	}

	// Initialize the sandbox client: nexus does not isolate and holds no pool itself — every execution is
	// delegated to the separate sandboxd service (the simplex / duplex routes on the same host, derived
	// from the base address in sandbox.url for each purpose; see ExecEndpoint / CodeEndpoint in
	// shared/config).
	if cfg.Sandbox.URL == "" {
		xlog.Error("sandbox not configured: sandbox.url is required")
		println("Failed to init sandbox: sandbox.url must be configured")
		return
	}

	agentSandbox, err := sandbox.NewRemote(sandbox.RemoteOptions{
		Endpoint: cfg.Sandbox.ExecEndpoint(),
		Token:    cfg.Sandbox.Token,
	})
	if err != nil {
		xlog.Error("Failed to init sandbox for agent exec: %v", err)
		println("Failed to init sandbox: " + err.Error())
		return
	}
	agentTools.SetSandbox(agentSandbox)

	// Long-interaction client (the shell tool): /v1/interactive, a persistent process reused per session_id.
	// Calls without a session are marked ephemeral by the tool, destroyed explicitly when the run ends, with
	// a short timeout as a backstop.
	agentTools.SetInteractive(sandbox.NewInteractive(
		cfg.Sandbox.InteractiveEndpoint(), cfg.Sandbox.Token, sandbox.Limits{}))

	// Browser pool (browserd): the browser is not part of the sandbox any more — it is a separate service with its own
	// capacity (workers / per-tenant quota / idle reclamation) and its own isolation boundary, so the sandbox mode no
	// longer decides whether a browser can run.
	//
	// An empty browser.url means "this deployment has no browser": the tool reports that, and there is deliberately no
	// fallback to the old in-sandbox browser, which no longer exists.
	if cfg.Browser.URL != "" {
		agentTools.SetBrowserPool(cfg.Browser.BrowserEndpoint(), cfg.Browser.Token)
		browserinfo.StartRefresher(context.Background(),
			cfg.Browser.CapabilitiesEndpoint(), cfg.Browser.Token, time.Minute)
	} else {
		xlog.Info("browser pool not configured: the browser tool will report itself disabled")
	}

	// Capability snapshot: pull "can the sandbox reach the network + what is inside it" from sandboxd, for
	// the agent prompt.
	//
	// Refreshed every 60s — sandboxd is routinely restarted with a changed config (network declaration and
	// images may both change), and pulling only once at nexus startup would make the prompt start lying
	// immediately. A failed pull neither blocks startup nor clears the previous result: an agent holding a
	// "possibly stale but real environment" is more useful than holding "unknown", and the copy says so.
	sandboxinfo.StartRefresher(context.Background(), cfg.Sandbox.CapabilitiesEndpoint(), cfg.Sandbox.Token, time.Minute)

	// Disk plugins: the tool surface and implementation come from the Python plugins under
	// {root_dir}/plugins (self-registering); nexus only discovers them, holds the tenant config and
	// dispatches.
	//
	// The directory is scanned once at startup (a failure is only logged and does not block startup —
	// plugins are an optional capability), then refreshed in the background every refresh_interval; when
	// the directory did not change, no python process is started.
	pluginDir := cfg.Plugin.DirOrDefault(cfg.Storage.RootDir)
	if err := plugin.EnsureDir(pluginDir); err != nil {
		xlog.Error("plugin directory not usable dir=%s: %v (skipping plugin load)", pluginDir, err)
	} else {
		pluginHost := plugin.NewHost(pluginDir, cfg.Plugin.Python, cfg.Plugin.TimeoutDuration())
		registry := plugin.NewRegistry(pluginHost, agentTools.Names())
		_ = registry.Refresh(context.Background(), true)
		// SetDefault registers both the interface view and the concrete registry: the latter serves the
		// admin "rescan the disk" action.
		plugin.SetDefault(registry)
		// Execution channel: ExecuteTool hands over to the plugin runner when the built-in tool table has no
		// match. Only after this injection is a plugin tool actually callable (before that it is just a
		// name in a directory).
		agentTools.SetPluginRunner(plugin.NewDefaultRunner(pluginHost, registry))
		registry.Start(context.Background(), cfg.Plugin.RefreshIntervalDuration())
	}

	codeClient := code.NewClient(cfg.Sandbox.CodeEndpoint(), cfg.Sandbox.Token, sandbox.Limits{})
	// Record the script ID sandboxd returns, as the key for reconciling against sandboxd's logs: sandboxd
	// records "script -> container (container ID)" while this side records when the script appeared, so
	// "which container a call went to" can be matched on script.
	// Debug level: the call detail is voluminous (lua is a high-frequency call) and does not belong in
	// regular Info.
	codeClient.SetOnReady(func(scriptID string, reused bool) {
		xlog.Debug("sandbox script: %s reused=%v", scriptID, reused)
	})
	executors.SetCodeClient(codeClient)
	defer codeClient.Close()

	xlog.Info("Sandbox client initialized: base=%s exec=%s code=%s",
		cfg.Sandbox.URL, cfg.Sandbox.ExecEndpoint(), cfg.Sandbox.CodeEndpoint())

	// Reclaim the stdio MCP session process pool on shutdown (avoiding leftover child processes).
	defer client.GetSessionPool().Close()

	// Initialize the router.
	router := SetupRouter()
	// Start the server.
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	xlog.Info("Server starting on %s", addr)
	if err := router.Run(addr); err != nil {
		xlog.Error("Failed to start server: %v", err)
		return
	}
}

// readinessIssue is one "why the service cannot serve".
//
// It has two layers: Detail is the fixed copy for the probe (no internal addresses or other details),
// Cause is the raw error and goes to the log only. A business caller sees neither — all it can do is
// retry, and knowing the internal topology only adds risk.
type readinessIssue struct {
	Key    string `json:"dependency"`
	Detail string `json:"detail"`
	Cause  string `json:"-"`
}

// checkReadiness aggregates the connection state of every hard dependency; it is a pure, read-only
// function. An empty result means ready.
// Each connection state is maintained by its own goroutine and only read here, avoiding concurrent
// assignment races.
//
// Compared with the old isServiceReady (returning a bool), the reason is no longer swallowed: "not ready"
// caused by "the sandbox snapshot has not been fetched yet" (self-heals within seconds) and by "the
// database dropped" (needs a human) mean completely different things to callers and to whoever debugs,
// and should not both degrade to a single false.
func checkReadiness(cfg *config.Config) []readinessIssue {
	var issues []readinessIssue
	if !db.IsConnected() {
		issues = append(issues, readinessIssue{Key: "db", Detail: "database not connected"})
	}
	// Redis is a mandatory dependency: the cross-instance run ledger (runledger), the stop-signal broadcast
	// and the primary quota counter all rely on it.
	// No connection means no service — "the service looks fine but cannot stop a run on another instance"
	// is far harder to diagnose than a plain 503.
	if !redis.IsConnected() {
		issues = append(issues, readinessIssue{Key: "redis", Detail: "redis not connected"})
	}
	if cfg.LiteLLM.Enabled && litellmGW != nil && !litellmGW.IsConnected() {
		issues = append(issues, readinessIssue{Key: "litellm", Detail: "llm gateway not connected"})
	}
	// With a sandbox configured, the capability snapshot must be there first: an empty Mode means nexus does
	// not know the sandbox environment (isolation kind / OS / available languages / network), and it should
	// not serve in that state — better a 503 than letting an agent run commands in an unknown environment.
	// StartRefresher is asynchronous and this only lets requests through once the first pull succeeded
	// (while not ready it retries urgently at firstRetry instead of waiting a full refresh interval).
	if cfg.Sandbox.URL != "" {
		if info := sandboxinfo.Get(); info.Mode == "" {
			issues = append(issues, readinessIssue{
				Key:    "sandbox",
				Detail: "sandbox capability snapshot not fetched yet",
				Cause:  info.Err,
			})
		}
	}
	return issues
}

// describeIssues flattens the readiness issues into one log line: the probe gets Key+Detail, the log gets
// Key+Detail+Cause.
func describeIssues(issues []readinessIssue) string {
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		if is.Cause != "" {
			parts = append(parts, is.Key+": "+is.Detail+" ("+is.Cause+")")
			continue
		}
		parts = append(parts, is.Key+": "+is.Detail)
	}
	return strings.Join(parts, "; ")
}
