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

// maxFrameBytes is the per-message limit, matching the sandbox package.
const maxFrameBytes = 32 << 20 // 32 MiB

// Gateway is the gateway: WS proxy + backend pool + load snapshots + sticky routing.
type Gateway struct {
	cfg       *Config
	pool      *Pool
	sticky    StickyStore
	discovery xregistry.Discovery
	upgrader  websocket.Upgrader
	// discoveryEmptyLogged rate-limits the "discovery answered nothing" warning to one line per streak instead of one
	// per interval. Only DiscoveryLoop writes it, and that loop is a single goroutine.
	discoveryEmptyLogged bool
}

// New builds the gateway. The sticky store follows the config: redis.addr present uses redis, otherwise
// per-process memory.
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

// newSticky builds the sticky store from the config. An unreachable redis is a hard error (with several
// instances, scrambled stickiness is worse than failing).
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

// Router exposes the same /v1/* endpoints to nexus as sandboxd does.
func (g *Gateway) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if g.cfg.Token != "" {
		api.Use(tokenAuth(g.cfg.Token))
	}
	api.GET("/exec", g.handleProxy)
	api.GET("/code", g.handleProxy) // renamed from /v1/code in 2026-10, in step with the sandboxd routes
	api.GET("/interactive", g.handleProxy)
	// Capability snapshot: proxied from a healthy backend (plain HTTP, not WS).
	api.GET("/capabilities", g.handleCapabilities)
	// The gateway's own load view: monitoring / an upstream load balancer observes each backend's score and
	// health here.
	api.GET("/metrics", g.handleMetrics)
	return r
}

// RefreshLoop periodically pulls the backend load snapshots.
func (g *Gateway) RefreshLoop(ctx context.Context) {
	interval := time.Duration(g.cfg.MetricsIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Pull once at startup so the snapshots become usable immediately (otherwise the first batch of
	// requests all lands on the first backend).
	g.pool.refreshAll(ctx)
	g.checkStickyTTL(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.pool.refreshAll(ctx)
			// Check the TTL against the peer's idle values along the way (nearly free once pulled).
			g.checkStickyTTL(ctx)
		}
	}
}

// checkStickyTTL verifies that the sticky TTL covers the peer's idle-reclaim upper bound:
//
//	sticky_ttl > interactive_idle_sec + 2×interactive_janitor_sec
//
// Why it must be checked at run time: the two values live in the configuration files of two different
// processes (sandboxd and sandboxg) and nothing keeps them in sync. sandboxd reports its own config through
// the interactive section of /v1/capabilities, and the gateway compares against it.
//
// When it does not hold, warn only and do not block service: the consequence is "the binding may expire
// while the process is still alive", which may route the same session to another backend and build one
// process on each (split state) — a correctness risk, but a misconfiguration should not make the whole
// gateway refuse service.
func (g *Gateway) checkStickyTTL(ctx context.Context) {
	for _, problem := range g.stickyTTLProblems(ctx) {
		xlog.Error("sandboxg: %s", problem)
	}
}

// stickyTTLProblems returns the TTL shortfall diagnostics (empty means fine). It is split out as a pure query
// so a unit test can assert on it directly instead of scraping the log.
func (g *Gateway) stickyTTLProblems(ctx context.Context) []string {
	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second
	var out []string
	for _, b := range g.pool.all() {
		// Re-pull every capsRefresh: after sandboxd is restarted with a different idle value it must be able
		// to say so, otherwise this keeps judging by the old values — and when the old values "pass" there is
		// not even a log line.
		b.refreshCapsIfStale(ctx)
		c := b.capsSnapshot()
		// The peer does not report the section (older sandboxd): nothing to check, treat it as passing.
		if c.IdleSec <= 0 {
			continue
		}
		need := time.Duration(c.IdleSec+2*c.JanitorSec) * time.Second
		if ttl > need {
			continue
		}
		if !b.markTTLWarned() { // report once per backend, the value does not change by itself
			continue
		}
		out = append(out, fmt.Sprintf(
			"sticky_ttl_sec=%ds does not cover the long-interaction idle-reclaim upper bound of %s "+
				"interactive_idle_sec(%ds) + 2×interactive_janitor_sec(%ds) = %ds: "+
				"the binding may expire while the process is still alive, so the same session risks landing "+
				"on two backends and building one process on each; "+
				"set sandboxg's sticky_ttl_sec to > %d",
			g.cfg.StickyTTLSec, b.URL, c.IdleSec, c.JanitorSec,
			int(need.Seconds()), int(need.Seconds())))
	}
	return out
}

// DiscoveryLoop periodically pulls the instance list from service discovery and reconciles it (new ones
// join the pool, vanished ones leave and their sticky bindings are cleared).
// A failed pull keeps the old list instead of evicting (so a discovery blip cannot empty the whole pool).
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

// discoverOnce pulls the instance list once and reconciles it, clearing the sticky bindings of the backends
// that were evicted.
//
// Two answers deliberately do not empty the pool:
//   - an error: the list stays as it is, so a discovery blip cannot take the gateway down;
//   - a **successful but empty** list: it is ambiguous - "the last instance deregistered" and "the registry answered
//     before the service re-registered" (a rolling restart is exactly the second) look identical. Giving it authority
//     would drop every backend and fail every request until the next registration, so the pool keeps its last known
//     list; a backend that is really gone is then removed by the health check (health_check_fail_threshold).
//
// A non-empty answer *is* authoritative: a static seed from `backends` that the registry does not list leaves the pool,
// which is what keeps a registry-managed deployment from serving instances the operator already deregistered.
func (g *Gateway) discoverOnce(ctx context.Context) {
	insts, err := g.discovery.ListInstances(ctx, g.cfg.Discovery.ServiceName)
	if err != nil {
		xlog.Error("sandboxg: discovery: %v", err)
		return
	}
	if len(insts) == 0 {
		if !g.discoveryEmptyLogged {
			g.discoveryEmptyLogged = true
			xlog.Error("sandboxg: discovery returned no instance of %q - keeping the current pool of %d backend(s); "+
				"a backend that is really gone is still evicted by the health check",
				g.cfg.Discovery.ServiceName, len(g.pool.all()))
		}
		return
	}
	g.discoveryEmptyLogged = false

	removed := g.pool.Reconcile(insts, g.cfg.SandboxToken)
	if len(removed) > 0 {
		urls := make([]string, 0, len(removed))
		for _, b := range removed {
			urls = append(urls, b.URL)
		}
		xlog.Info("sandboxg: discovery dropped %d backend(s) the registry does not list: %s",
			len(removed), strings.Join(urls, ", "))
	}
	for _, b := range removed {
		if err := g.sticky.RemoveBackend(ctx, b.URL); err != nil {
			xlog.Error("sandboxg: remove bindings for %s: %v", b.URL, err)
		}
	}
}

// route decides which backend a request goes to.
//
// interactive (with a session_id) is sticky: a binding hit is forwarded there unconditionally; a miss picks
// by load and grabs the binding atomically. When the grab loses (another gateway bound first under
// concurrency) the backend it bound is read back and used, so the same session does not hit several
// instances. Everything else (exec/code, no session_id) is pure load balancing.
func (g *Gateway) route(ctx context.Context, path, sessionID string) (*Backend, error) {
	if path != "/v1/interactive" || sessionID == "" {
		if b := g.pool.leastLoaded(); b != nil {
			return b, nil
		}
		return nil, fmt.Errorf("no healthy backend")
	}

	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second

	// Look for an existing binding first.
	if key, ok, err := g.sticky.Lookup(ctx, sessionID, ttl); err != nil {
		return nil, err
	} else if ok {
		if b := g.pool.byURL(key); b != nil {
			return b, nil
		}
		// The binding points at an evicted backend: fall back to load balancing and grab again.
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
	// Lost the grab: read back the backend someone else just bound and use it instead.
	if key, ok, err := g.sticky.Lookup(ctx, sessionID, ttl); err == nil && ok {
		if b := g.pool.byURL(key); b != nil {
			return b, nil
		}
	}
	return backend, nil
}

// HealthLoop periodically probes the backend /healthz, evicts unhealthy instances and clears their sticky
// bindings.
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

// handleProxy proxying one WS connection: read the open frame → pick a backend → connect → forward the open
// frame → pipe both directions.
//
// The protocol is transparent: frames are not rewritten. The open frame is peeked only for the routing
// decision (sticky routing needs the session_id) and is forwarded as is afterwards. Piping uses bare gorilla
// connections so the frame bytes go back and forth unchanged.
func (g *Gateway) handleProxy(c *gin.Context) {
	client, err := g.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	// 1) Read the first open frame (a peek, its meaning is not consumed — it is forwarded to the backend as is).
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

	// 2) Route: interactive is sticky by session, everything else goes by load.
	backend, rerr := g.route(c.Request.Context(), c.Request.URL.Path, open.SessionID)
	if rerr != nil {
		xlog.Error("sandboxg: route: %v", rerr)
		_ = client.WriteMessage(websocket.TextMessage, errFrame("route failed"))
		return
	}

	// 3) Connect to the backend (ws://base + the current request path).
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

	// 4) Forward the open frame (as is).
	if err := bws.WriteMessage(typ, payload); err != nil {
		return
	}

	// 5) Pipe both directions: whichever side drops ends this proxy.
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
			// sandboxd says this session has no long-interaction tool left — the sticky binding ends here.
			//
			// The opposite rule ("unbind on any kill") does not work: a session may hold several
			// long-interaction tools, and killing one of them leaves the session valid; only sandboxd's
			// registry knows whether it is empty.
			if open.SessionID != "" && isEmptyFrame(t, d) {
				go g.unbind(open.SessionID, backend.URL)
			}
		}
	}()
	<-errc
}

// handleMetrics returns the gateway's own load view: the per-backend snapshots plus the aggregate score.
//
// Unlike sandboxd /v1/metrics this is "the world as the gateway sees it" — backend health, each score, the
// total. Upstream monitoring can use it to decide whether the gateway is saturated (a high total_score).
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

// handleCapabilities proxies /v1/capabilities from a healthy backend (plain HTTP, not WS).
//
// Unlike the load-balanced exec/code routes, the capability snapshot is metadata and unrelated to load, so
// the first healthy backend in a deterministic order is used here, keeping the snapshot source from jumping
// with load. The response is proxied as is, preserving protocol transparency.
func (g *Gateway) handleCapabilities(c *gin.Context) {
	b := g.pool.firstHealthy()
	if b == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no healthy backend"})
		return
	}
	g.proxyHTTP(c, b.capabilitiesURL(), b.Token)
}

// proxyHTTP forwards one GET to a backend and returns it as is (status code + Content-Type + body).
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

	// Only Content-Type is forwarded; the other hop-by-hop headers (Content-Length/Connection ...) are
	// handled by the http layer itself.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

// isEmptyFrame recognizes the "session has no long-interaction tool left" signal sent by the backend.
// Non-text frames and non-JSON payloads always pass through — the proxy path must not change behaviour
// because parsing failed.
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

// unbind drops one sticky binding (best effort: a failure is only logged and the TTL remains the backstop).
//
// It uses a standalone ctx: the client usually disconnects right after the signal, so the request ctx is
// already cancelled and using it would cancel the very work that must happen.
func (g *Gateway) unbind(sessionID, backendURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.sticky.Unbind(ctx, sessionID, backendURL); err != nil {
		xlog.Error("sandboxg: unbind %s from %s: %v", sessionID, backendURL, err)
	}
}

// errFrame builds an error control frame.
func errFrame(msg string) []byte {
	b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameError, Message: msg})
	return b
}

// tokenAuth is the same Bearer authentication as sandboxd.
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
