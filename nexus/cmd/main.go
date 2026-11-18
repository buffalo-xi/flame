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

// litellmGW 是 LiteLLM 网关实例（可能为 nil，表示未启用或未初始化）。
// 由 main 在启动时创建，供 checkReadiness 等只读读取。其连接状态由后台 goroutine 维护。
var litellmGW *litellm.ProxyGateway

func main() {

	// 加载配置
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		return
	}
	config.Cfg = cfg

	// 初始化 agent 存储
	if err := agent.InitStorage(); err != nil {
		xlog.Error("Failed to initialize agent storage: %v", err)
		return
	}

	// 初始化日志系统
	if err := xlog.Init("logs", "nexus"); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}
	defer xlog.Close()

	// 初始化Redis客户端
	myredisConfig := &xredis.Config{
		Mode:     xredis.RedisMode(cfg.Redis.Mode),
		Addrs:    cfg.Redis.Addrs,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
		Timeout:  time.Duration(cfg.Redis.Timeout) * time.Second,
		Prefix:   cfg.Redis.Prefix,
	}

	// 服务注册：创建注册器。注册/注销由下方 goroutine 按 DB 就绪状态驱动。
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

	// LiteLLM 自身 Postgres 库的 DSN（dbname 即 yaml 的 litellm.database）。
	// 启动时仅预计算，连接推迟到下方重连 goroutine 中建立（litellmGW 以 db=nil 初始化）。
	litellmDBDSN := ""
	if cfg.LiteLLM.Enabled && cfg.LiteLLM.URL != "" && cfg.LiteLLM.MasterKey != "" {
		litellmDBDSN = cfg.Database.GetDSN()
		if cfg.LiteLLM.Database != "" {
			litellmDBDSN = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai application_name=nexus_service",
				cfg.Database.Host, cfg.Database.Port, cfg.Database.Username, cfg.Database.Password, cfg.LiteLLM.Database)
		}
		// db=nil：连接不在启动时建立，交由重连 goroutine 注入
		litellmGW = litellm.New(cfg.LiteLLM.URL, cfg.LiteLLM.MasterKey, nil)
		gateway.SetCurrent(litellmGW)
		xlog.Info("LiteLLM gateway initialized: %s", cfg.LiteLLM.URL)
	}

	// 启动兜底清理，等 DB 就绪后执行一次（两者都要查库）：
	//   - runs/：无 session 调用的一次性场地。只收「超过 24 小时没动」的目录，
	//     不整棵删 —— 多实例共享同一存储时，整棵删会带走别的实例正在跑的场地
	//     （见 service/agent/janitor.go 的 runsTTL）；
	//   - platform-assets/：不在表里的孤儿快照 + 掉出保留窗口的旧版本。
	go func() {
		for i := 0; i < 60; i++ {
			if db.IsConnected() {
				agent.CleanupOrphanRuns()
				share.CleanupSnapshots()
				return
			}
			time.Sleep(2 * time.Second)
		}
		xlog.Info("启动兜底清理跳过：数据库未就绪")
	}()

	// 数据库连接检查和重连
	go func() {
		for {
			if !db.CheckDatabaseConnection() {
				xlog.Info("Attempting to reconnect to database...")
				db.ReconnectDatabase()
			}
			// LiteLLM 库连接检查与重连：Ping 失败则重新建连并注入
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

	// Redis 连接检查和重连（必须依赖，没有开关）。本 goroutine 同时承担首次建连。
	//
	// 连不上时不拒绝启动，但在建连成功前 checkReadiness 会一直报 redis 未就绪，
	// 中间件据此对所有业务请求回 503 —— 「没连上就当没这个服务」，而不是先接流量再降级。
	//
	// 日志限流（60s 一条）：不可用时每 5s 就会进一次这个分支，不限流会把控制台刷满，
	// 真正的错误反而被淹掉。
	go func() {
		var lastLogAt time.Time
		for {
			if !redis.CheckRedisConnection() {
				// NewClient 内部已 Ping，失败返回 (nil, err)；此处必须判错后再赋值，
				// 否则失败会把 redis.Client 清成 nil，连瞬时抖动都会丢掉已有连接对象。
				client, cerr := xredis.NewClient(myredisConfig)
				if cerr != nil {
					// 「正在重连」与失败原因压成一条：分成两条会互相压制，
					// 结果要么刷屏，要么只剩一句「在重连」而永远看不到为什么连不上。
					if time.Since(lastLogAt) >= time.Minute {
						lastLogAt = time.Now()
						xlog.Info("reconnect to redis failed: %v (retrying every 5s)", cerr)
					}
				} else {
					if old := redis.Client; old != nil {
						_ = old.Close() // 回收旧客户端的连接池，避免每次重连都留一份
					}
					redis.Client = client
					redis.CheckRedisConnection() // 立即刷新连接状态，避免探针在首个 Ping 周期内一直判为不可用
					xlog.Info("create Redis client successfully")
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()

	// 服务注册检查和重注册：DB 与 Redis 都就绪才注册；任一断开主动注销
	// （让 gateway 不再路由过来），恢复后重新注册。两者都是强依赖 ——
	// Redis 断了虽然缓存能回源，但跨实例台账与停止广播会失效，那种「半残」状态
	// 不该继续接流量。
	// 新 Registrar 没有 IsServiceRegistered，用本地状态记录当前是否已注册。
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
					// 文案说清是谁断了，否则排障只能靠猜
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

	// 检查是否超过截止日期（2027年6月1号）
	cutoffDate := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if time.Now().UTC().After(cutoffDate) {
		xlog.Error("Program aborted")
		return
	}

	// 初始化沙箱客户端：nexus 侧不做隔离、不持有池，
	// 所有执行都委托给独立的 sandboxd 服务（同一台服务上的单工 / 双工两条路由，
	// 由 sandbox.url 这个基地址按用途拼出，见 shared/config 的 ExecEndpoint / CodeEndpoint）。
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

	// 长交互客户端（shell 工具）：/v1/interactive，持久进程按 session_id 复用。
	// 无 session 的调用由工具侧标记 ephemeral，run 结束显式销毁 + 短超时兜底。
	agentTools.SetInteractive(sandbox.NewInteractive(
		cfg.Sandbox.InteractiveEndpoint(), cfg.Sandbox.Token, sandbox.Limits{}))

	// 能力快照：把 sandboxd 的「沙箱能否联网 + 沙箱里有什么」拉过来，供 agent 提示词使用。
	//
	// 每 60s 刷新一次 —— sandboxd 改配置重启是常态（网络声明、镜像都可能变），只在 nexus
	// 启动时拉一次，提示词会立刻开始撒谎。拉取失败不阻断启动，也不清空上一次的结果：
	// agent 拿到「可能过期的真实环境」比拿到「未知」有用，文案里会注明。
	sandboxinfo.StartRefresher(context.Background(), cfg.Sandbox.CapabilitiesEndpoint(), cfg.Sandbox.Token, time.Minute)

	// 磁盘插件：工具面与实现来自 {root_dir}/plugins 下的 Python 插件（自注册），
	// nexus 只负责发现 / 租户配置 / 调度。
	//
	// 启动扫一次（失败只记日志，不阻断启动 —— 插件是可选能力），之后按 refresh_interval
	// 后台刷新；目录没变时不会起 python 进程。
	pluginDir := cfg.Plugin.DirOrDefault(cfg.Agent.RootDir)
	if err := plugin.EnsureDir(pluginDir); err != nil {
		xlog.Error("插件目录不可用 dir=%s: %v（跳过插件加载）", pluginDir, err)
	} else {
		pluginHost := plugin.NewHost(pluginDir, cfg.Plugin.Python, cfg.Plugin.TimeoutDuration())
		registry := plugin.NewRegistry(pluginHost, agentTools.Names())
		_ = registry.Refresh(context.Background(), true)
		// SetDefault 同时登记「接口视图」与「具体注册表」：后者给管理端「重扫磁盘」用
		plugin.SetDefault(registry)
		// 执行通道：ExecuteTool 在内置工具表里查不到时转给插件执行器。
		// 注入后插件工具才真正可调用（在此之前它只是目录里的一个名字）。
		agentTools.SetPluginRunner(plugin.NewDefaultRunner(pluginHost, registry))
		registry.Start(context.Background(), cfg.Plugin.RefreshIntervalDuration())
	}

	codeClient := code.NewClient(cfg.Sandbox.CodeEndpoint(), cfg.Sandbox.Token, sandbox.Limits{})
	// 把 sandboxd 回传的 script ID 记下来，作为与 sandboxd 日志对账的键：
	// sandboxd 侧记「script → container（容器 ID）」，这里记 script 出现的时刻，
	// 排查「某次调用走了哪个容器」时两边按 script 对齐。
	// 用 Debug 级别：调用明细量大（lua 是高频调用），不该混进常规 Info。
	codeClient.SetOnReady(func(scriptID string, reused bool) {
		xlog.Debug("sandbox script: %s reused=%v", scriptID, reused)
	})
	executors.SetCodeClient(codeClient)
	defer codeClient.Close()

	xlog.Info("Sandbox client initialized: base=%s exec=%s code=%s",
		cfg.Sandbox.URL, cfg.Sandbox.ExecEndpoint(), cfg.Sandbox.CodeEndpoint())

	// 关闭时回收 stdio MCP 会话进程池（避免残留子进程）
	defer client.GetSessionPool().Close()

	// 初始化路由
	router := SetupRouter()
	// 启动服务器
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	xlog.Info("Server starting on %s", addr)
	if err := router.Run(addr); err != nil {
		xlog.Error("Failed to start server: %v", err)
		return
	}
}

// readinessIssue 一条"为什么不能提供服务"。
//
// 分两层：Detail 是给探针看的固定文案（不含内网地址等细节），Cause 是原始错误，
// 只进日志。业务调用方两者都看不到 —— 它唯一能做的是重试，知道内网拓扑只增加风险。
type readinessIssue struct {
	Key    string `json:"dependency"`
	Detail string `json:"detail"`
	Cause  string `json:"-"`
}

// checkReadiness 聚合所有强依赖的连接状态，纯函数、只读。空结果 = 就绪。
// 各连接状态由对应 goroutine 维护，此处仅按需计算，避免并发赋值竞争。
//
// 与旧的 isServiceReady（返回 bool）相比，原因不再被吞掉：同样是"不就绪"，
// 「沙箱快照还没拉到」（秒级自愈）和「DB 断了」（要人介入）对调用方与排障者
// 的含义完全不同，不该都退化成一个 false。
func checkReadiness(cfg *config.Config) []readinessIssue {
	var issues []readinessIssue
	if !db.IsConnected() {
		issues = append(issues, readinessIssue{Key: "db", Detail: "database not connected"})
	}
	// Redis 是必须依赖：跨实例运行台账（runledger）、停止信号广播都靠它，配额计数也以它为主路径。
	// 没连通就不对外服务 —— 「服务看着能用、但停不掉别的实例上的运行」比直接 503 更难排查。
	if !redis.IsConnected() {
		issues = append(issues, readinessIssue{Key: "redis", Detail: "redis not connected"})
	}
	if cfg.LiteLLM.Enabled && litellmGW != nil && !litellmGW.IsConnected() {
		issues = append(issues, readinessIssue{Key: "litellm", Detail: "llm gateway not connected"})
	}
	// 配了沙箱就必须先拿到能力快照：Mode 为空意味着 nexus 不知道沙箱环境
	// （隔离方式 / OS / 可执行语言 / 网络），此时不应对外提供服务 —— 宁可 503，
	// 也不要让 agent 在未知环境里跑命令。StartRefresher 是异步的，首次拉取成功
	// 后这里才会放行（未就绪时它按 firstRetry 加急重试，不会干等一个刷新周期）。
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

// describeIssues 把就绪问题压成一行日志：探针拿 Key+Detail，日志拿 Key+Detail+Cause。
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
