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

// Server is the sandboxd runtime state.
type Server struct {
	cfg         *Config
	exec        sandbox.Sandbox      // simplex isolation implementation (a new process each time, never pooled)
	pools       map[string]*slotPool // duplex: per-language slot pool plus a concurrency gate
	interactive *interactiveRegistry // long interaction: session-level logical sandbox + multi-process registry
	upgrader    websocket.Upgrader

	// execInFlight is the number of in-flight /v1/exec connections (/metrics, for gateway load balancing).
	execInFlight atomic.Int64

	// env is the capability snapshot probed once at startup, only read by /v1/capabilities (never re-probed).
	// It is a read-only field, so there is no concurrent write at run time.
	env Environment
}

// NewServer assembles the runtime: it resolves the worker script (L2) or the codehost binary (L3) and creates
// the slot pool of every language.
func NewServer(cfg *Config, iso sandbox.Sandbox) (*Server, error) {
	s := &Server{
		cfg:   cfg,
		exec:  iso,
		pools: make(map[string]*slotPool),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// An internal service, authenticated by token; Origin is not restricted
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	// mode=none is "host pass-through": no namespace / container is created, so none of the limits (memory / CPU
	// / PID / per-file size) can be applied (only the timeout is real, enforced by ctx). That is not a defect but
	// the definition of this mode — and it must be said out loud: configuring memory_mb and believing the gate
	// exists is more dangerous than not configuring it.
	if cfg.Mode == isolate.NoneName {
		xlog.Info("sandboxd: mode=none: host pass-through, no memory / CPU / PID / per-file size cap applied (only the timeout takes effect); for local development only")
	}

	// The writable root must be ready before the pools are created: prewarm is asynchronous, and creating it
	// later would race.
	if err := prepareTmpRoot(cfg.TmpDir); err != nil {
		return nil, err
	}

	scriptLimits := Clamp(cfg.Code.Limits.ToSandbox(), cfg.Limits.ToSandbox())

	// Merge "top-level language definitions" with "duplex policies": the entry name is resolved against the
	// current mode right here and frozen, so downstream (pool / unit / worker) only sees one string and never
	// has to know about per-mode overrides.
	// Failing to resolve aborts startup — silently skipping would make the caller hit unsupported_lang at run
	// time, which is far harder to diagnose than a startup error.
	langs, lerr := cfg.Languages()
	if lerr != nil {
		return nil, fmt.Errorf("sandboxd: %w", lerr)
	}

	// Always use the resident codehost: the wrapper templates are embedded, so no worker script is needed.
	codehostBinary, err := cfg.ResolveCodehostBinary(cfg.Mode)
	if err != nil {
		return nil, fmt.Errorf("sandboxd: %w", err)
	}

	for lang, lc := range langs {
		s.pools[lang] = newSlotPool(lang, lc, iso, codehostBinary, cfg.TmpDir, scriptLimits)
		xlog.Info("sandboxd: lang %s ready (command=%s, idle_initial=%d, idle_max=%d, max_concurrent=%d)",
			lang, lc.Command, lc.Idle.Initial, lc.Idle.Max, lc.MaxConcurrent)
	}

	// Long-interaction registry: the session-level logical sandbox (docker reuses a container; nsjail/none start
	// an independent one).
	// The idle-reclaim ceiling comes from interactive_idle_sec in the config (seconds), 300 by default.
	s.interactive = newInteractiveRegistryWithJanitor(
		iso,
		time.Duration(cfg.InteractiveIdleSec)*time.Second,
		time.Duration(cfg.InteractiveEphemeralIdleSec)*time.Second,
		time.Duration(cfg.InteractiveJanitorSec)*time.Second,
	)
	xlog.Info("sandboxd: interactive idle timeout = %ds/%ds, janitor = %ds (interactive_idle_sec / interactive_ephemeral_idle_sec / interactive_janitor_sec)",
		cfg.InteractiveIdleSec, cfg.InteractiveEphemeralIdleSec, cfg.InteractiveJanitorSec)

	// Capability probe: run once; /v1/capabilities serves the result (nexus pulls it into the agent prompt).
	//
	// A failure does not block startup: answering "unknown" is still honest, while refusing to start would turn
	// sandbox availability into a hard startup dependency of sandboxd — a failed probe should degrade, not
	// escalate.
	s.env = s.probeEnvironment(context.Background())
	if s.env.Error != "" {
		xlog.Error("sandboxd: environment probe failed: %s", s.env.Error)
	} else {
		xlog.Info("sandboxd: environment probed (%d lines)", strings.Count(s.env.Text, "\n"))
	}

	return s, nil
}

// Close releases every slot pool and the long-interaction sandboxes.
func (s *Server) Close() {
	for _, p := range s.pools {
		p.Close()
	}
	s.interactive.Close()
}

// Router returns the HTTP routes.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if s.cfg.Token != "" {
		api.Use(tokenAuth(s.cfg.Token))
	}
	api.GET("/exec", s.handleExec)               // simplex: one-shot commands
	api.GET("/code", s.handleCode)               // hosted code snippets (input → return); the handler name stays handleCode (it drives codehost's script protocol)
	api.GET("/interactive", s.handleInteractive) // long interaction: session-level resident processes
	// Capability snapshot: nexus pulls it into the agent prompt (isolation mode / network declaration /
	// languages / sandbox environment)
	api.GET("/capabilities", s.handleCapabilities)
	// Load snapshot: the gateway pulls it periodically for load balancing (exec concurrency / script pool
	// occupancy / long-interaction occupancy)
	api.GET("/metrics", s.handleMetrics)
	return r
}

// handleCapabilities returns this service's outward capability snapshot:
//
//	mode            isolation mode
//	network         the network policy per channel — a **deployment ceiling**, not the fact of one call
//	                { exec: off|on-request|on, code: off }
//	langs           top-level language definitions (the languages the sandbox can run)
//	code_langs      languages usable for hosted code snippets (workflow code nodes)
//	environment     the sandbox environment probed at startup (OS / interpreter versions / installed modules)
//
// Why network is split per channel: exec goes through the simplex path and starts a new process per request, so
// its network can be switched per request; code is a globally pooled unit whose network is fixed when the unit
// is built, and is always off (see EffectiveCodeNetwork). Collapsed into one value, a consumer would necessarily
// draw the wrong conclusion on one of the two channels.
//
// Why network is a ceiling rather than a fact: it reports what the deployment allows. Whether one particular
// call can actually reach the network also depends on the tenant grant and the request flag (nexus combines them
// and injects the prompt — that judgement must not be delegated to the agent). A mismatch between the config and
// how the service was started already fails at startup (see validateNetwork), so nothing has to be evaluated at
// run time.
// Why environment is a probe: that is a genuine runtime fact, and only the sandbox knows it (see probe.go).
//
// langs and code_langs must stay two items: the former is the top-level language definitions (also used by the
// simplex path — a language that only runs skill scripts need not open a code pool), and only the latter answers
// "which languages a workflow code node may choose". Merged into one, a consumer (the editor / the agent creating
// a flow) would be able to pick a language that cannot run.
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
		// Long-interaction lifecycle parameters: the gateway uses them to check whether its own sticky TTL covers
		// them (sticky_ttl > idle_sec + 2×janitor_sec). The two values live in the config files of two different
		// processes, so unless they are reported the gateway can only align them by hand.
		"interactive": gin.H{
			"idle_sec":           s.cfg.InteractiveIdleSec,
			"ephemeral_idle_sec": s.cfg.InteractiveEphemeralIdleSec,
			"janitor_sec":        s.cfg.InteractiveJanitorSec,
		},
	})
}

// handleMetrics returns the load snapshot: pulled periodically by the gateway for load balancing.
//
// The three kinds of occupancy share one source (they compete for the same sandboxd's CPU/memory/fds), and the
// gateway weights them into a load score:
//
//	score = exec_inflight + Σ(code_busy) + interactive_active
//
// interactive_active is the number of processes actually serving a call (idle long-interaction processes burn no
// CPU and are not weighted); interactive_processes is for observation only. This is an "estimate", not an exact
// ledger: snapshots may drift momentarily, but it is enough to drive load balancing.
func (s *Server) handleMetrics(c *gin.Context) {
	busy := make(map[string]int, len(s.pools))
	for lang, p := range s.pools {
		busy[lang] = len(p.sem) // occupied concurrency quota = workers currently lent out
	}
	sessions, processes, active := s.interactive.stats()
	c.JSON(http.StatusOK, gin.H{
		"exec_inflight":         s.execInFlight.Load(),
		"code_busy":             busy,
		"interactive_sessions":  sessions,
		"interactive_processes": processes,
		"interactive_active":    active,
	})
}

// codeLangs returns the languages usable for **duplex interaction** (workflow scripts).
//
// The criterion is simply "does this service build a pool for it right now": the keys of s.pools come from
// code.pools (a pool only exists once the interpreter entry resolved for the current mode), and the only gate
// through which handleCode rejects a language is precisely that lookup (no s.pools[lang] → unsupported_lang).
// Same source, so "it is in the list but running it yields unsupported_lang" cannot happen.
//
// ⚠️ The "script" in the name refers to codehost's protocol (route /v1/code) and is unrelated to the retired
// config key code.langs (the old name of code.pools).
func (s *Server) codeLangs() []string {
	return sortedNames(s.pools)
}

// sortedNames takes the keys of a map and sorts them.
// The order must be stable and reproducible: the result goes into the LLM context and into the language dropdown
// of the workflow editor.
func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// tokenAuth validates the Bearer token.
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

// wsWriter turns Write into a binary frame on the given channel.
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

// exitCode turns a process exit error into an exit code.
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
