package gw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// errSessionLost is "the session exists, but the instance that held it is gone".
//
// A browser cannot be migrated: the cookies, the open tabs and a half-filled form live in one Chromium on one instance. So
// the gateway refuses to silently route the session to another instance — that would build a second, empty browser and the
// model would keep talking to a page that no longer shows what it remembers. Failing loudly is the only honest answer;
// nexus surfaces this frame to the model, which then calls open again.
var errSessionLost = errors.New("browser session lost")

// errNoBackend is "every instance is unhealthy".
var errNoBackend = errors.New("no healthy backend")

// Gateway is the browser gateway: WS proxy + instance pool + load snapshots + sticky routing + tenant quota.
type Gateway struct {
	cfg       *Config
	pool      *Pool
	sticky    StickyStore
	quota     *Quota
	discovery xregistry.Discovery
	upgrader  websocket.Upgrader
	// discoveryEmptyLogged rate-limits the "discovery answered nothing" warning to one line per streak.
	discoveryEmptyLogged bool
}

// New builds the gateway. The sticky store follows the config: redis.addr present uses redis, otherwise per-process
// memory.
func New(cfg *Config) (*Gateway, error) {
	sticky, err := newSticky(cfg)
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		cfg:    cfg,
		pool:   NewPool(cfg.Backends),
		sticky: sticky,
		quota:  NewQuota(cfg.MaxPerTenant),
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
			return nil, fmt.Errorf("browserg: discovery: %w", err)
		}
		g.discovery = disc
	}
	return g, nil
}

// newSticky builds the sticky store from the config. An unreachable redis is a hard error (with several gateway
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
		return nil, fmt.Errorf("browserg: redis %s: %w", cfg.Redis.Addr, err)
	}
	return NewRedisSticky(client), nil
}

// Router exposes the /v1/* endpoints nexus expects from a pool.
//
// Only the browser channel exists here — no exec/code: this gateway fronts browserd, and those two routes belong to
// sandboxg.
func (g *Gateway) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if g.cfg.Token != "" {
		api.Use(tokenAuth(g.cfg.Token))
	}
	api.GET("/browser", g.handleProxy)
	// Capability snapshot: aggregated across instances (plain HTTP, not WS).
	api.GET("/capabilities", g.handleCapabilities)
	// The gateway's own view: per-instance load, health and the aggregate score.
	api.GET("/metrics", g.handleMetrics)
	return r
}

// RefreshLoop periodically pulls the instance snapshots.
func (g *Gateway) RefreshLoop(ctx context.Context) {
	interval := time.Duration(g.cfg.MetricsIntervalSec()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Pull once at startup so the snapshots become usable immediately; otherwise the first batch of requests all lands on
	// the first instance.
	g.pool.refreshAll(ctx)
	g.checkStickyTTL()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.pool.refreshAll(ctx)
			g.checkStickyTTL()
		}
	}
}

// MetricsIntervalSec is the snapshot refresh cadence.
func (c *Config) MetricsIntervalSec() int {
	if c.SnapshotIntervalSec > 0 {
		return c.SnapshotIntervalSec
	}
	return 2
}

// checkStickyTTL verifies that the sticky TTL covers each instance's idle-reclaim upper bound:
//
//	sticky_ttl > idle_sec + 2×janitor_sec
//
// Why it must be checked at run time: the two values live in the configuration files of two different processes (browserd
// and browserg) and nothing keeps them in sync.
//
// When it does not hold, warn only and do not block service: the consequence is "the binding may expire while the lease is
// alive", which is how one session ends up with two Chromiums (the model then sees whichever it hits last) — a correctness
// risk, but a misconfiguration should not make the gateway refuse service.
func (g *Gateway) checkStickyTTL() {
	for _, problem := range g.stickyTTLProblems() {
		xlog.Error("browserg: %s", problem)
	}
}

// stickyTTLProblems returns the TTL shortfall diagnostics (empty means fine). Split out as a pure query so a unit test can
// assert on it directly instead of scraping the log.
func (g *Gateway) stickyTTLProblems() []string {
	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second
	var out []string
	for _, b := range g.pool.all() {
		c := b.caps()
		if c.IdleSec <= 0 {
			continue // the instance does not report the section: nothing to check
		}
		need := time.Duration(c.IdleSec+2*c.Janitor) * time.Second
		if ttl > need {
			continue
		}
		if !b.markTTLWarned(c.IdleSec, c.Janitor) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"sticky_ttl_sec=%ds does not cover the idle-reclaim upper bound of %s "+
				"idle_sec(%ds) + 2×janitor_sec(%ds) = %ds: "+
				"the binding may expire while the browser is still alive, so the same session risks landing "+
				"on two instances and building one Chromium on each; "+
				"set browserg's sticky_ttl_sec to > %d",
			g.cfg.StickyTTLSec, b.URL, c.IdleSec, c.Janitor, int(need.Seconds()), int(need.Seconds())))
	}
	return out
}

// DiscoveryLoop periodically pulls the instance list from service discovery and reconciles it.
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

// discoverOnce pulls the instance list once and reconciles it, clearing the sticky bindings of the instances that were
// evicted.
//
// Two answers deliberately do not empty the pool: an error (the list stays as it is), and a successful but empty answer (a
// rolling restart looks exactly like that). A non-empty answer **is** authoritative: a static seed the registry does not
// list leaves the pool.
func (g *Gateway) discoverOnce(ctx context.Context) {
	insts, err := g.discovery.ListInstances(ctx, g.cfg.Discovery.ServiceName)
	if err != nil {
		xlog.Error("browserg: discovery: %v", err)
		return
	}
	if len(insts) == 0 {
		if !g.discoveryEmptyLogged {
			g.discoveryEmptyLogged = true
			xlog.Error("browserg: discovery returned no instance of %q - keeping the current pool of %d instance(s); "+
				"an instance that is really gone is still evicted by the health check",
				g.cfg.Discovery.ServiceName, len(g.pool.all()))
		}
		return
	}
	g.discoveryEmptyLogged = false

	removed := g.pool.Reconcile(insts, g.cfg.BrowserToken)
	if len(removed) > 0 {
		urls := make([]string, 0, len(removed))
		for _, b := range removed {
			urls = append(urls, b.URL)
		}
		xlog.Info("browserg: discovery dropped %d instance(s) the registry does not list: %s",
			len(removed), strings.Join(urls, ", "))
	}
	for _, b := range removed {
		if err := g.sticky.RemoveBackend(ctx, b.URL); err != nil {
			xlog.Error("browserg: remove bindings for %s: %v", b.URL, err)
		}
	}
}

// route decides which instance a session goes to, and reports whether the decision came from an existing binding.
//
// Every open frame with a session key is sticky — including a session-less run, where nexus puts the run id in the same
// field. sandboxg can afford "sticky only for /v1/interactive" because its other routes are stateless; a browser is never
// stateless: routing two opens of one run to two instances would hand the model two different pages.
func (g *Gateway) route(ctx context.Context, sessionID string) (b *Backend, hit bool, err error) {
	ttl := time.Duration(g.cfg.StickyTTLSec) * time.Second

	if sessionID != "" {
		key, ok, lerr := g.sticky.Lookup(ctx, sessionID, ttl)
		if lerr != nil {
			return nil, false, lerr
		}
		if ok {
			if bb := g.pool.byURL(key); bb != nil {
				return bb, true, nil
			}
			// The binding outlived its instance: the browser state is gone with it. Drop the stale binding so the next
			// open binds afresh, and tell the caller instead of quietly building an empty browser here.
			go g.unbind(sessionID, key)
			return nil, false, fmt.Errorf("%w: the instance holding it is no longer healthy; open the page again", errSessionLost)
		}
	}

	backend := g.pool.leastLoaded()
	if backend == nil {
		return nil, false, errNoBackend
	}
	if sessionID == "" {
		return backend, false, nil
	}
	grabbed, berr := g.sticky.Bind(ctx, sessionID, backend.URL, ttl)
	if berr != nil {
		return nil, false, berr
	}
	if grabbed {
		return backend, false, nil
	}
	// Lost the grab: read back the instance someone else just bound and use it instead, so one session never lands on
	// several instances.
	if key, ok, lerr := g.sticky.Lookup(ctx, sessionID, ttl); lerr == nil && ok {
		if bb := g.pool.byURL(key); bb != nil {
			return bb, true, nil
		}
	}
	return backend, false, nil
}

// HealthLoop periodically probes the instances' /healthz, evicts unhealthy ones and clears their sticky bindings.
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
					xlog.Error("browserg: remove bindings for %s: %v", b.URL, err)
				}
			})
		}
	}
}

// handleProxy proxies one WS connection: read the open frame → pick an instance → connect → forward the open frame → pipe
// both directions.
//
// The protocol is transparent: frames are not rewritten. The open frame is peeked only for the routing and quota
// decisions.
func (g *Gateway) handleProxy(c *gin.Context) {
	client, err := g.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	// 1) Read the first open frame (a peek; it is forwarded to the instance as is).
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

	// 2) Route by session key.
	backend, hit, rerr := g.route(c.Request.Context(), open.SessionID)
	if rerr != nil {
		xlog.Error("browserg: route: %v", rerr)
		_ = client.WriteMessage(websocket.TextMessage, errFrame(rerr.Error()))
		return
	}

	// 3) Quota: only for a new binding. A hit means this session already has (or had) a browser there, and refusing a
	// reconnect would strand a live page that the tenant is still paying for in the live count.
	tenant := c.Query("tenant")
	reserved := false
	if !hit {
		if qerr := g.quota.Acquire(tenant, g.pool.tenantSessions); qerr != nil {
			xlog.Info("browserg: %v", qerr)
			_ = client.WriteMessage(websocket.TextMessage, errFrame(qerr.Error()))
			return
		}
		reserved = true
	}

	// 4) Connect to the instance. The query is preserved: ?tenant= is the key browserd counts its own quota with, and a
	// path-only forward would silently drop it.
	dialCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	h := http.Header{}
	if backend.Token != "" {
		h.Set("Authorization", "Bearer "+backend.Token)
	}
	bws, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).
		DialContext(dialCtx, backend.wsURL(c.Request.URL.Path, c.Request.URL.RawQuery), h)
	if err != nil {
		xlog.Error("browserg: dial instance %s%s: %v", backend.URL, c.Request.URL.Path, err)
		if reserved {
			g.quota.Release(tenant)
		}
		_ = client.WriteMessage(websocket.TextMessage, errFrame("backend unreachable"))
		return
	}
	bws.SetReadLimit(maxFrameBytes)
	defer func() { _ = bws.Close() }()

	// 5) Forward the open frame (as is), then hand the reserved slot back: from here on the live count from /v1/sessions
	// takes over.
	if err := bws.WriteMessage(typ, payload); err != nil {
		if reserved {
			g.quota.Release(tenant)
		}
		return
	}
	if reserved {
		g.quota.Release(tenant)
	}

	// 6) Pipe both directions: whichever side drops ends this proxy.
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
			// The instance says this session has no lease left — the sticky binding ends here. The opposite rule
			// ("unbind on any kill") would be wrong: a kill for a session that was re-opened in the meantime must not
			// destroy the new binding, which is why Unbind is conditional on the instance key.
			if open.SessionID != "" && isEmptyFrame(t, d) {
				go g.unbind(open.SessionID, backend.URL)
			}
		}
	}()
	<-errc
}

// handleCapabilities aggregates /v1/capabilities across instances.
//
// Unlike sandboxg (which passes one instance through), the aggregate is the only honest answer for a pool: nexus uses
// `available` to decide whether the browser tool works at all, `headed_available` to gate a headed open, and the counters
// to explain load.
//
//	available        any instance can run a browser
//	headed_available **all** healthy instances allow it (conservative: a headed request may land on any of them)
//	workers/sessions/busy/queue   summed over healthy instances
//	idle_sec         minimum (the worst case for the sticky TTL check)
//	janitor_sec      maximum
func (g *Gateway) handleCapabilities(c *gin.Context) {
	snaps := g.pool.snapshots()

	var (
		healthy, available, headed, headless int
		workers, sessions, busy, queue       int
		idleMin, janitorMax                  int
		engine, version, firstErr            string
	)
	for _, s := range snaps {
		if !s.Healthy {
			continue
		}
		healthy++
		workers += s.Workers
		sessions += s.Sessions
		busy += s.Busy
		queue += s.Queue
		if s.IdleSec > 0 && (idleMin == 0 || s.IdleSec < idleMin) {
			idleMin = s.IdleSec
		}
		if s.Janitor > janitorMax {
			janitorMax = s.Janitor
		}
		if s.Available {
			available++
			if version == "" {
				engine, version = s.Engine, s.Version
			}
		} else if firstErr == "" && s.Error != "" {
			firstErr = s.Error
		}
		if s.Headed {
			headed++
		}
		if s.Headless {
			headless++
		}
	}

	body := gin.H{
		"enabled":          true,
		"available":        available > 0,
		"workers":          workers,
		"sessions":         sessions,
		"busy":             busy,
		"queue":            queue,
		"idle_sec":         idleMin,
		"janitor_sec":      janitorMax,
		"headed_available": healthy > 0 && headed == healthy,
		"headless_default": healthy > 0 && headless == healthy,
		"backends_total":   len(snaps),
		"backends_healthy": healthy,
	}
	if version != "" {
		body["engine"] = engine
		body["version"] = version
	}
	if available == 0 {
		switch {
		case healthy == 0:
			body["error"] = "no healthy browser instance"
		case firstErr != "":
			body["error"] = firstErr
		default:
			body["error"] = "no instance reported a usable browser"
		}
	}
	c.JSON(http.StatusOK, body)
}

// handleMetrics returns the gateway's own view: per-instance snapshots plus the aggregate score.
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

// isEmptyFrame recognizes the "no lease left for this session" signal sent by browserd.
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
// It uses a standalone context: the client usually disconnects right after the signal, so the request context is already
// cancelled and using it would cancel the very work that must happen.
func (g *Gateway) unbind(sessionID, backendURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.sticky.Unbind(ctx, sessionID, backendURL); err != nil {
		xlog.Error("browserg: unbind %s from %s: %v", sessionID, backendURL, err)
	}
}

// errFrame builds an error control frame.
func errFrame(msg string) []byte {
	b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameError, Message: msg})
	return b
}

// tokenAuth is the same Bearer authentication as browserd.
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
