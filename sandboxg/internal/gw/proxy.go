package gw

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
	"flame/tools/xregistry"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// maxFrameBytes 单条消息上限，与 sandbox 包同口径。
const maxFrameBytes = 32 << 20 // 32 MiB

// Gateway 网关：WS 代理 + 后端池 + 负载快照 + 粘性路由。
type Gateway struct {
	cfg       *Config
	pool      *Pool
	sticky    StickyStore
	discovery xregistry.Discovery
	upgrader  websocket.Upgrader
}

// New 构造网关。粘性存储按配置选：配了 redis.addr 用 redis，否则单机内存。
func New(cfg *Config) (*Gateway, error) {
	sticky, err := newSticky(cfg)
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		cfg:    cfg,
		pool:   NewPool(cfg.Backends),
		sticky: sticky,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	if cfg.Discovery.Type != "" {
		disc, err := xregistry.NewDiscovery(&xregistry.DiscoveryConfig{
			Type:    xregistry.Type(cfg.Discovery.Type),
			Address: cfg.Discovery.Address,
		})
		if err != nil {
			return nil, fmt.Errorf("sandboxg: discovery: %w", err)
		}
		g.discovery = disc
	}
	return g, nil
}

// newSticky 按配置构造粘性存储。redis 连不上直接报错（多实例下粘性错乱比失败更糟）。
func newSticky(cfg *Config) (StickyStore, error) {
	if cfg.Redis.Addr == "" {
		return NewMemorySticky(), nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sandboxg: redis %s: %w", cfg.Redis.Addr, err)
	}
	return NewRedisSticky(client), nil
}

// Router 对 nexus 暴露与 sandboxd 相同的 /v1/* 端点。
func (g *Gateway) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if g.cfg.Token != "" {
		api.Use(tokenAuth(g.cfg.Token))
	}
	api.GET("/exec", g.handleProxy)
	api.GET("/code", g.handleProxy) // 2026-10 由 /v1/code 更名，与 sandboxd 路由同步
	api.GET("/interactive", g.handleProxy)
	// 能力快照：透传某个健康后端（普通 HTTP，非 WS）。
	api.GET("/capabilities", g.handleCapabilities)
	// gateway 自身负载视图：供监控 / 上层负载均衡观测各后端分数与健康状态。
	api.GET("/metrics", g.handleMetrics)
	return r
}

// RefreshLoop 定期拉取后端负载快照。
func (g *Gateway) RefreshLoop(ctx context.Context) {
	interval := time.Duration(g.cfg.MetricsIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 启动先拉一次，让快照尽快可用（否则第一批请求全打到第一个后端）。
	g.pool.refreshAll(ctx)
	g.checkStickyTTL(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.pool.refreshAll(ctx)
			// 顺带校验 TTL 与对方 idle 的关系（拉到一次后就近乎零成本）。
			g.checkStickyTTL(ctx)
		}
	}
}

// checkStickyTTL 校验粘性 TTL 是否覆盖得住对方的空闲回收上界：
//
//	sticky_ttl > interactive_idle_sec + 2×interactive_janitor_sec
//
// 为什么必须运行时校验：这两个值分别在 sandboxd 与 sandboxg 两个进程的配置文件里，
// 没有任何机制保证同步。sandboxd 通过 /v1/capabilities 的 interactive 段把它自己
// 的配置报出来，网关据此比对。
//
// 不满足时只告警、不阻断服务：后果是「绑定可能在进程仍存活时过期」，可能把同一
// session 路由到另一台后端、各建一个进程（状态分裂）—— 是正确性风险，但配置写错
// 也不该让整个网关拒绝服务。
func (g *Gateway) checkStickyTTL(ctx context.Context) {
	for _, problem := range g.stickyTTLProblems(ctx) {
		xlog.Error("sandboxg: %s", problem)
	}
}

// stickyTTLProblems 返回 TTL 不足的诊断（空 = 合格）。抽成纯查询是为了能在单测里
// 直接断言，不必去抓日志。
func (g *Gateway) stickyTTLProblems(ctx context.Context) []string {
	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second
	var out []string
	for _, b := range g.pool.all() {
		// 按 capsRefresh 周期重新拉：sandboxd 改 idle 重启后必须能被告知，
		// 否则这里会一直拿旧值判断，而且"旧值合格"时连一条日志都不会有。
		b.refreshCapsIfStale(ctx)
		c := b.capsSnapshot()
		// 对方没报这一段（老版本 sandboxd）：无从校验，当合格处理。
		if c.IdleSec <= 0 {
			continue
		}
		need := time.Duration(c.IdleSec+2*c.JanitorSec) * time.Second
		if ttl > need {
			continue
		}
		if !b.markTTLWarned() { // 每个后端只报一次，这个值不会自己变
			continue
		}
		out = append(out, fmt.Sprintf(
			"sticky_ttl_sec=%ds 覆盖不住 %s 的长交互空闲回收上界 "+
				"interactive_idle_sec(%ds) + 2×interactive_janitor_sec(%ds) = %ds："+
				"绑定可能在进程仍存活时过期，同一 session 有落到两个后端各建一个进程的风险；"+
				"请把 sandboxg 的 sticky_ttl_sec 调到 > %d",
			g.cfg.StickyTTLSec, b.URL, c.IdleSec, c.JanitorSec,
			int(need.Seconds()), int(need.Seconds())))
	}
	return out
}

// DiscoveryLoop 周期从服务发现拉实例列表并对账（新增进池、消失移出并清粘性）。
// 失败保留旧列表不摘除（避免 discovery 抖动清空整个池）。
func (g *Gateway) DiscoveryLoop(ctx context.Context) {
	if g.discovery == nil {
		return
	}
	interval := time.Duration(g.cfg.Discovery.IntervalSec) * time.Second
	g.discoverOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.discoverOnce(ctx)
		}
	}
}

// discoverOnce 拉一次实例列表并对账，摘除的后端清粘性绑定。
func (g *Gateway) discoverOnce(ctx context.Context) {
	insts, err := g.discovery.ListInstances(ctx, g.cfg.Discovery.ServiceName)
	if err != nil {
		xlog.Error("sandboxg: discovery: %v", err)
		return
	}
	for _, b := range g.pool.Reconcile(insts, g.cfg.SandboxToken) {
		if err := g.sticky.RemoveBackend(ctx, b.URL); err != nil {
			xlog.Error("sandboxg: remove bindings for %s: %v", b.URL, err)
		}
	}
}

// route 决定一条请求发往哪个后端。
//
// interactive（带 session_id）走粘性：命中绑定固定转发；未命中按负载 + 原子抢占
// 绑定。抢占失败（并发下另一个 gateway 先绑了）时读回它绑的后端并重定向过去，
// 避免同一 session 打到多个实例。其余（exec/code、无 session_id）纯负载。
func (g *Gateway) route(ctx context.Context, path, sessionID string) (*Backend, error) {
	if path != "/v1/interactive" || sessionID == "" {
		if b := g.pool.leastLoaded(); b != nil {
			return b, nil
		}
		return nil, fmt.Errorf("no healthy backend")
	}

	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second

	// 先查已有绑定
	if key, ok, err := g.sticky.Lookup(ctx, sessionID, ttl); err != nil {
		return nil, err
	} else if ok {
		if b := g.pool.byURL(key); b != nil {
			return b, nil
		}
		// 绑定指向已摘除的后端：落到负载 + 重新抢占
	}

	backend := g.pool.leastLoaded()
	if backend == nil {
		return nil, fmt.Errorf("no healthy backend")
	}
	grabbed, err := g.sticky.Bind(ctx, sessionID, backend.URL, ttl)
	if err != nil {
		return nil, err
	}
	if grabbed {
		return backend, nil
	}
	// 没抢到：读回别人刚绑的后端，重定向过去
	if key, ok, err := g.sticky.Lookup(ctx, sessionID, ttl); err == nil && ok {
		if b := g.pool.byURL(key); b != nil {
			return b, nil
		}
	}
	return backend, nil
}

// HealthLoop 定期探后端 /healthz，摘除不健康实例并清理其粘性绑定。
func (g *Gateway) HealthLoop(ctx context.Context) {
	interval := time.Duration(g.cfg.HealthCheckIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.pool.checkHealth(ctx, g.cfg.HealthCheckFailThreshold, func(b *Backend) {
				if err := g.sticky.RemoveBackend(ctx, b.URL); err != nil {
					xlog.Error("sandboxg: remove bindings for %s: %v", b.URL, err)
				}
			})
		}
	}
}

// handleProxy 一次 WS 代理：读 open 帧 → 选后端 → 连后端 → 转 open → 双向透传。
//
// 协议透明：不重写帧。open 帧 peek 出来只为路由决策（后续阶段粘性路由要读
// session_id），决策后原样转发。透传用裸 gorilla 连接，保证帧字节原样往返。
func (g *Gateway) handleProxy(c *gin.Context) {
	client, err := g.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	// 1) 读第一条 open 帧（peek，不消费语义 —— 原样转发给后端）
	typ, payload, err := client.ReadMessage()
	if err != nil {
		return
	}
	if typ != websocket.TextMessage {
		_ = client.WriteMessage(websocket.TextMessage, errFrame("expected open frame"))
		return
	}
	var open sandbox.Frame
	if err := json.Unmarshal(payload, &open); err != nil || open.Type != sandbox.FrameOpen {
		_ = client.WriteMessage(websocket.TextMessage, errFrame("bad open frame"))
		return
	}

	// 2) 路由：interactive 按 session 粘性，其余按负载。
	backend, rerr := g.route(c.Request.Context(), c.Request.URL.Path, open.SessionID)
	if rerr != nil {
		xlog.Error("sandboxg: route: %v", rerr)
		_ = client.WriteMessage(websocket.TextMessage, errFrame("route failed"))
		return
	}

	// 3) 连后端（ws://base + 当前请求路径）
	dialCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	h := http.Header{}
	if backend.Token != "" {
		h.Set("Authorization", "Bearer "+backend.Token)
	}
	bws, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).
		DialContext(dialCtx, backend.wsURL(c.Request.URL.Path), h)
	if err != nil {
		xlog.Error("sandboxg: dial backend %s%s: %v", backend.URL, c.Request.URL.Path, err)
		_ = client.WriteMessage(websocket.TextMessage, errFrame("backend unreachable"))
		return
	}
	bws.SetReadLimit(maxFrameBytes)
	defer func() { _ = bws.Close() }()

	// 4) 转发 open 帧（原样）
	if err := bws.WriteMessage(typ, payload); err != nil {
		return
	}

	// 5) 双向透传：任一侧断开即结束本次代理。
	errc := make(chan error, 2)
	go func() { // client → backend
		for {
			t, d, rerr := client.ReadMessage()
			if rerr != nil {
				errc <- rerr
				return
			}
			if werr := bws.WriteMessage(t, d); werr != nil {
				errc <- werr
				return
			}
		}
	}()
	go func() { // backend → client
		for {
			t, d, rerr := bws.ReadMessage()
			if rerr != nil {
				errc <- rerr
				return
			}
			if werr := client.WriteMessage(t, d); werr != nil {
				errc <- werr
				return
			}
			// sandboxd 说这个 session 已经没有任何长交互工具了 —— 粘性绑定到此为止。
			//
			// 不能反过来「收到 kill 就解绑」：一个 session 可以有多个长交互工具，
			// 杀掉其中一个时会话仍然有效，是否为空只有 sandboxd 的注册表知道。
			if open.SessionID != "" && isEmptyFrame(t, d) {
				go g.unbind(open.SessionID, backend.URL)
			}
		}
	}()
	<-errc
}

// handleMetrics 返回 gateway 自身的负载视图：各后端快照 + 汇总分数。
//
// 与 sandboxd /v1/metrics 不同：这里是「gateway 看到的世界」——后端健康状态、
// 各自分数、汇总分。上层监控可据此判断 gateway 是否已经打满（total_score 高）。
func (g *Gateway) handleMetrics(c *gin.Context) {
	snaps := g.pool.snapshots()
	var total int64
	healthy := 0
	for _, s := range snaps {
		total += s.Score
		if s.Healthy {
			healthy++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"backends":         snaps,
		"total_score":      total,
		"backends_total":   len(snaps),
		"backends_healthy": healthy,
	})
}

// handleCapabilities 透传某个健康后端的 /v1/capabilities（普通 HTTP，非 WS）。
//
// 与 exec/code 的负载路由不同，能力快照是元数据、与负载无关，这里按确定性顺序
// 挑第一个健康后端，避免快照来源随负载跳变。响应原样透传，保持协议透明。
func (g *Gateway) handleCapabilities(c *gin.Context) {
	b := g.pool.firstHealthy()
	if b == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no healthy backend"})
		return
	}
	g.proxyHTTP(c, b.capabilitiesURL(), b.Token)
}

// proxyHTTP 把一次 GET 请求透传到一个后端并原样回传（状态码 + Content-Type + 体）。
func (g *Gateway) proxyHTTP(c *gin.Context, url, token string) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad gateway"})
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		xlog.Error("sandboxg: proxy %s: %v", url, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "backend unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// 只透传 Content-Type；其余 hop-by-hop 头（Content-Length/Connection 等）由 http 层自行处理。
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

// isEmptyFrame 判断后端发来的「session 已无长交互工具」信号。
// 非文本帧、非 JSON 一律放过 —— 透传路径不该因为解析失败改变行为。
func isEmptyFrame(typ int, payload []byte) bool {
	if typ != websocket.TextMessage {
		return false
	}
	var f sandbox.Frame
	if err := json.Unmarshal(payload, &f); err != nil {
		return false
	}
	return f.Type == sandbox.FrameSessionEmpty
}

// unbind 解掉一条粘性绑定（尽力而为：失败只记日志，TTL 仍是兜底）。
//
// 用独立 ctx：信号之后客户端通常立刻断开，请求 ctx 已被取消，
// 拿它来解绑等于把该做的事直接取消掉。
func (g *Gateway) unbind(sessionID, backendURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.sticky.Unbind(ctx, sessionID, backendURL); err != nil {
		xlog.Error("sandboxg: unbind %s from %s: %v", sessionID, backendURL, err)
	}
}

// errFrame 构造一条 error 控制帧。
func errFrame(msg string) []byte {
	b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameError, Message: msg})
	return b
}

// tokenAuth 与 sandboxd 同口径的 Bearer 鉴权。
func tokenAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || strings.TrimPrefix(auth, prefix) != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
