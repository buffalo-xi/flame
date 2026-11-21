package browserpool

import (
	"net/http"

	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Server is the HTTP face of the pool.
type Server struct {
	cfg  *Config
	pool *Pool
	up   websocket.Upgrader
}

// NewServer creates the pool and its router state.
func NewServer(cfg *Config) *Server {
	return &Server{
		cfg:  cfg,
		pool: New(cfg),
		up: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// An internal service authenticated by token; Origin is not restricted (same rule as sandboxd).
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Pool exposes the pool (used by main for the startup log and by tests).
func (s *Server) Pool() *Pool { return s.pool }

// Close releases every browser.
func (s *Server) Close() { s.pool.Close() }

// Router builds the routes.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/v1")
	if s.cfg.Token != "" {
		api.Use(tokenAuth(s.cfg.Token))
	}
	// The browser channel: the same frame protocol as sandboxd's /v1/interactive, so a caller that already speaks it
	// needs a new URL, not a new client.
	api.GET("/browser", s.handleBrowser)
	api.GET("/capabilities", s.handleCapabilities)
	api.GET("/metrics", s.handleMetrics)
	api.GET("/sessions", s.handleSessions)
	api.DELETE("/sessions/:id", s.handleDeleteSession)
	return r
}

// handleBrowser serves one browser call.
//
// The lease belongs to the session, not to the connection: a disconnect only means "this call is over" (the browser
// stays, cookies and tabs included), exactly like sandboxd's long-interactive channel. An explicit kill frame — or the
// idle janitor — is what destroys it.
//
// The open frame's argv / mounts are ignored on purpose: this service does not take part in the session's sandbox.
// Which driver and which Chromium to launch is its own configuration. Only Spec.Dir is honoured, as a workspace path
// relative to the shared root (see browserpool.workspaceDir) — that is where the session's screenshots and downloads go.
func (s *Server) handleBrowser(c *gin.Context) {
	ws, err := s.up.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame"})
		return
	}
	if f.SessionID == "" {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "session_id is required"})
		return
	}

	// An open frame without a spec is a probe or a kill (that is how sandbox.InteractiveClient.Kill works): it must not
	// create a browser. Acquiring would spawn a Chromium — hundreds of megabytes, seconds of latency — only for the
	// kill frame that follows to destroy it. Answer "there is none" instead; the client treats it as already destroyed.
	if f.Spec == nil && !s.pool.Has(f.SessionID) {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "no browser session for " + f.SessionID})
		return
	}

	workDir := ""
	if f.Spec != nil {
		workDir = f.Spec.Dir
	}
	lease, aerr := s.pool.Acquire(c.Request.Context(), f.SessionID, c.Query("tenant"), workDir)
	if aerr != nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: aerr.Error()})
		return
	}

	// One call at a time per session: a browserhost serves one JSON op at a time, so the lock keeps two concurrent
	// calls from interleaving their requests and responses.
	lease.Begin()
	defer lease.End()

	lease.Worker.attach(&wsWriter{c: wc, ch: sandbox.ChanStdout})
	defer lease.Worker.detach()

	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady}); werr != nil {
		return
	}

	for {
		cf, data, rerr := wc.Recv()
		if rerr != nil {
			return // this call is over; the lease stays (idle reclamation owns the lifetime)
		}
		if data != nil {
			if data[0] != sandbox.ChanStdin {
				continue
			}
			if _, werr := lease.Worker.Write(data[1:]); werr != nil {
				_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: werr.Error()})
				return
			}
			continue
		}
		if cf.Type == sandbox.FrameKill {
			s.pool.Release(f.SessionID)
			_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameSessionEmpty, SessionID: f.SessionID})
			return
		}
	}
}

// handleCapabilities reports what this deployment can do, plus the load snapshot the caller needs to size its queue.
func (s *Server) handleCapabilities(c *gin.Context) {
	st := s.pool.SelfTest()
	stats := s.pool.Stats()

	body := gin.H{
		"enabled":          true,
		"workers":          stats.Workers,
		"sessions":         stats.Sessions,
		"busy":             stats.Busy,
		"queue":            stats.Queue,
		"queue_timeout":    s.cfg.QueueTimeoutSec,
		"idle_sec":         s.cfg.IdleTimeoutSec,
		"janitor_sec":      s.cfg.JanitorSec,
		"headed_available": s.cfg.HeadedAllowed(),
		"headless_default": s.cfg.HeadlessDefault(),
		"mode":             s.cfg.Mode,
	}
	if st == nil {
		body["available"] = false
		body["error"] = "probing browser version"
	} else if st.Error != "" {
		body["available"] = false
		body["error"] = st.Error
	} else {
		body["available"] = true
		body["engine"] = st.Engine
		body["version"] = st.Version
	}
	c.JSON(http.StatusOK, body)
}

// handleMetrics is the load snapshot (a caller can use it to pick an instance, like sandboxg does for sandboxd).
func (s *Server) handleMetrics(c *gin.Context) {
	stats := s.pool.Stats()
	c.JSON(http.StatusOK, gin.H{
		"workers":  stats.Workers,
		"sessions": stats.Sessions,
		"busy":     stats.Busy,
		"queue":    stats.Queue,
	})
}

// handleSessions lists the live leases (debugging: "who is holding the two workers").
func (s *Server) handleSessions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"sessions": s.pool.Leases()})
}

// handleDeleteSession releases a session explicitly (the fast path when a run ends).
func (s *Server) handleDeleteSession(c *gin.Context) {
	id := c.Param("id")
	released := s.pool.Release(id)
	xlog.Info("browserd: explicit release of session %s (released=%v)", id, released)
	c.JSON(http.StatusOK, gin.H{"session": id, "released": released})
}

// tokenAuth validates the Bearer token (same scheme as sandboxd, so one client configuration serves both).
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

// Write sends one frame. A failed write is swallowed: it only means the caller disconnected, which must not tear down
// the driver's stdout path (the same rule as sandboxd's writerSwitch).
func (w *wsWriter) Write(p []byte) (int, error) {
	_ = w.c.SendData(w.ch, p)
	return len(p), nil
}
