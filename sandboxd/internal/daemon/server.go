package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Server sandboxd 运行态。
type Server struct {
	cfg         *Config
	exec        sandbox.Sandbox      // 单工隔离实现（每次都新建进程，永不入池）
	pools       map[string]*slotPool // 双工：按语言的槽位池 + 并发闸
	interactive *interactiveRegistry // 长交互：session 级逻辑沙箱 + 多进程注册表
	upgrader    websocket.Upgrader

	// execInFlight 当前 in-flight 的 /v1/exec 连接数（/metrics 供 gateway 负载均衡）。
	execInFlight atomic.Int64

	// browser 浏览器能力快照（启动时异步探测，见 browser.go）。
	browser atomic.Pointer[BrowserInfo]

	// env 启动时探测一次的能力快照，仅供 /v1/capabilities 读取（不再重探）。
	// 只读字段，运行期无并发写。
	env Environment
}

// NewServer 组装运行态：解析 worker 脚本（L2）或 codehost 二进制（L3），创建各语言的槽位池。
func NewServer(cfg *Config, iso sandbox.Sandbox) (*Server, error) {
	s := &Server{
		cfg:   cfg,
		exec:  iso,
		pools: make(map[string]*slotPool),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// 内网服务，鉴权靠 token；不限制 Origin
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	// mode=none 是「宿主直通」：不起 namespace / 容器，所以 limits 里的内存 / CPU /
	// PID / 单文件大小全都无从施加（只有超时是真的，由 ctx 兜）。这不是缺陷，而是
	// 这一档的定义 —— 但必须说出来：配了 memory_mb 却以为有闸，比没配更危险。
	if cfg.Mode == isolate.NoneName {
		xlog.Info("sandboxd: mode=none: 宿主直通，不施加内存 / CPU / PID / 单文件大小上限（仅超时生效）；只用于本机开发")
	}

	// 可写目录必须早于池创建就绪：prewarm 是异步的，晚建会有竞态。
	if err := prepareTmpRoot(cfg.TmpDir); err != nil {
		return nil, err
	}

	scriptLimits := Clamp(cfg.Code.Limits.ToSandbox(), cfg.Limits.ToSandbox())

	// 合并「顶层语言定义」与「双工策略」：入口名在这里就按当前 mode 解析定死，
	// 下游（池 / 单元 / worker）只认一个字符串，不必知道「按 mode 覆盖」这回事。
	// 取不到就启动即失败 —— 静默跳过会让调用方在运行期收到 unsupported_lang，
	// 比启动期报错难查得多。
	langs, lerr := cfg.Languages()
	if lerr != nil {
		return nil, fmt.Errorf("sandboxd: %w", lerr)
	}

	// 统一走 codehost 常驻宿主：内嵌包装模板，不再需要 worker 脚本。
	codehostBinary, err := cfg.ResolveCodehostBinary(cfg.Mode)
	if err != nil {
		return nil, fmt.Errorf("sandboxd: %w", err)
	}

	for lang, lc := range langs {
		s.pools[lang] = newSlotPool(lang, lc, iso, codehostBinary, cfg.TmpDir, scriptLimits)
		xlog.Info("sandboxd: lang %s ready (command=%s, idle_initial=%d, idle_max=%d, max_concurrent=%d)",
			lang, lc.Command, lc.Idle.Initial, lc.Idle.Max, lc.MaxConcurrent)
	}

	// 长交互注册表：session 级逻辑沙箱（docker 复用容器、nsjail/none 独立 Start）。
	// 空闲回收上限来自 config 的 interactive_idle_sec（秒），缺省 1800。
	s.interactive = newInteractiveRegistryWithJanitor(
		iso,
		time.Duration(cfg.InteractiveIdleSec)*time.Second,
		time.Duration(cfg.InteractiveEphemeralIdleSec)*time.Second,
		time.Duration(cfg.InteractiveJanitorSec)*time.Second,
	)
	xlog.Info("sandboxd: interactive idle timeout = %ds/%ds, janitor = %ds (interactive_idle_sec / interactive_ephemeral_idle_sec / interactive_janitor_sec)",
		cfg.InteractiveIdleSec, cfg.InteractiveEphemeralIdleSec, cfg.InteractiveJanitorSec)

	// 能力探测：只跑一次，结果供 /v1/capabilities 取用（nexus 拉去注入 agent 提示词）。
	//
	// 失败不阻断启动：返回「未知」仍然是诚实的，而起不来会让沙箱可用性变成 sandboxd
	// 的启动硬依赖 —— 探测失败该降级，不该放大。
	s.env = s.probeEnvironment(context.Background())
	if s.env.Error != "" {
		xlog.Error("sandboxd: environment probe failed: %s", s.env.Error)
	} else {
		xlog.Info("sandboxd: environment probed (%d lines)", strings.Count(s.env.Text, "\n"))
	}

	// 浏览器能力探测：异步，失败不阻断启动（浏览器只是可选能力）。
	s.startBrowserProbe()
	return s, nil
}

// Close 释放所有槽位池与长交互沙箱。
func (s *Server) Close() {
	for _, p := range s.pools {
		p.Close()
	}
	s.interactive.Close()
}

// Router 返回 HTTP 路由。
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if s.cfg.Token != "" {
		api.Use(tokenAuth(s.cfg.Token))
	}
	api.GET("/exec", s.handleExec)               // 单工：一次性命令
	api.GET("/code", s.handleCode)             // 托管代码片段（输入 → 返回）；handler 名沿用 handleCode（驱动的是 codehost 的 script 协议）
	api.GET("/interactive", s.handleInteractive) // 长交互：session 级常驻进程
	// 能力快照：nexus 拉取后注入 agent 提示词（隔离方式 / 网络声明 / 语言 / 沙箱环境）
	api.GET("/capabilities", s.handleCapabilities)
	// 负载快照：gateway 定期拉取做负载均衡（exec 并发 / 脚本池占用 / 长交互占用）
	api.GET("/metrics", s.handleMetrics)
	return r
}

// handleCapabilities 返回本服务的对外能力快照：
//
//	mode            隔离方式
//	network         按通道的网络策略 —— **部署上限**，不是某次调用的事实
//	                  { exec: off|on-request|on, code: off }
//	langs           顶层语言定义（沙箱能跑起来的语言）
//	code_langs      可用于托管代码片段（工作流 code 节点）的语言
//	environment     启动时探测到的沙箱环境（OS / 解释器版本 / 已装模块）
//
// network 为什么按通道拆：exec 走单工、每请求新建进程，网络能按请求切；code 是
// 全局池化单元，网络在建单元时写死，而且恒为 off（见 EffectiveCodeNetwork）。
// 合并成一个值的话，消费方必然在其中一条通道上得到错误的结论。
//
// network 为什么是上限而不是事实：它报的是部署允许什么；某次调用实际能不能联网，
// 还要叠加租户许可与请求标志（nexus 侧算完再注入提示词，不要把这层判断丢给 agent）。
// 配置与启动方式不一致时启动已经失败了（见 validateNetwork），运行期无需求值。
// environment 为什么是探测：那是真·运行期事实，只有沙箱自己知道（见 probe.go）。
//
// langs 与 code_langs 必须分成两项：前者是顶层语言定义（单工也用，例如只跑技能
// 脚本的语言不必开 code 池），后者才是「工作流 code 节点能选哪些语言」的答案。
// 合成一项的后果是消费方（编辑器 / 创建流程的 agent）会选到一个跑不起来的语言。
func (s *Server) handleCapabilities(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"mode": s.cfg.Mode,
		"network": gin.H{
			"exec": s.cfg.EffectiveNetwork(),
			"code": s.cfg.EffectiveCodeNetwork(),
		},
		"langs":       sortedNames(s.cfg.Langs),
		"code_langs":  s.codeLangs(),
		"environment": s.env,
		// 长交互的生命周期参数：gateway 用它们校验自己的粘性 TTL 是否覆盖得住
		// （sticky_ttl > idle_sec + 2×janitor_sec）。这两个值分别在两个进程的配置
		// 文件里，不报出来网关就只能靠人肉对齐。
		"interactive": gin.H{
			"idle_sec":           s.cfg.InteractiveIdleSec,
			"ephemeral_idle_sec": s.cfg.InteractiveEphemeralIdleSec,
			"janitor_sec":        s.cfg.InteractiveJanitorSec,
		},
		// 浏览器能力：镜像里有没有 Chromium、能不能有头。
		// 与 environment 一样是**探测结果**，不是配置声明 —— 启用与否在配置里，
		// 但「装没装、版本多少」只有沙箱自己知道。
		"browser": s.BrowserSnapshot(),
	})
}

// handleMetrics 返回负载快照：供 gateway 定期拉取做负载均衡。
//
// 三类占用同源（争的是同一台 sandboxd 的 CPU/内存/fd），gateway 侧加权成负载分数：
//
//	score = exec_inflight + Σ(code_busy) + interactive_active
//
// interactive_active 是「正服务调用」的进程数（空闲长交互进程不烧 CPU，不计重），
// interactive_processes 只作观测。这是「估计值」而非精确账本：快照之间可能存在
// 瞬时漂移，但足以支撑负载均衡。
func (s *Server) handleMetrics(c *gin.Context) {
	busy := make(map[string]int, len(s.pools))
	for lang, p := range s.pools {
		busy[lang] = len(p.sem) // 已占用的并发额度 = 借出中的 worker 数
	}
	sessions, processes, active := s.interactive.stats()
	c.JSON(http.StatusOK, gin.H{
		"exec_inflight":         s.execInFlight.Load(),
		"code_busy":           busy,
		"interactive_sessions":  sessions,
		"interactive_processes": processes,
		"interactive_active":    active,
	})
}

// codeLangs 可用于**双工交互**（工作流脚本）的语言。
//
// 判据就是「本服务当下有没有为它建池」：s.pools 的键来自 code.pools
// （解释器入口已按当前 mode 解析成功才建得起来），而 handleCode 拒掉一个语言的
// 唯一闸门也正是这次查找（s.pools[lang] 不存在 → unsupported_lang）。两者同源，
// 才不会出现「列表里有、跑起来却是 unsupported_lang」。
//
// ⚠️ 名字里的 script 指 codehost 的协议（路由 /v1/code），与已废弃的配置键
// code.langs（那个是 code.pools 的旧名）无关。
func (s *Server) codeLangs() []string {
	return sortedNames(s.pools)
}

// sortedNames 取 map 的键并排序。
// 顺序必须稳定且可复现：这份结果既进 LLM 上下文，也进工作流编辑器的语言下拉。
func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// tokenAuth Bearer 令牌校验。
func tokenAuth(token string) gin.HandlerFunc {
	want := "Bearer " + token
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != want {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// wsWriter 把 Write 转成指定通道的二进制帧。
type wsWriter struct {
	c  *sandbox.Conn
	ch byte
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if err := w.c.SendData(w.ch, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// exitCode 把进程结束错误转成退出码。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *sandbox.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}
