package gw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"flame/tools/sandbox"

	"github.com/gorilla/websocket"
)

// fakeInstance is a browserd stand-in: it answers /healthz, /v1/capabilities, /v1/sessions and speaks the browser
// channel, recording what the gateway forwarded.
type fakeInstance struct {
	srv *httptest.Server

	mu         sync.Mutex
	opens      []string // the raw query of every forwarded open frame
	emptyAfter bool     // send session_empty right after ready (unbind path)
	caps       string
	sessions   string
}

func newFakeInstance(t *testing.T) *fakeInstance {
	t.Helper()
	f := &fakeInstance{
		caps:     `{"enabled":true,"available":true,"workers":2,"sessions":0,"busy":0,"queue":0,"idle_sec":300,"janitor_sec":60}`,
		sessions: `{"sessions":[]}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.caps
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.sessions
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/v1/browser", func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, rerr := conn.ReadMessage(); rerr != nil {
			return
		}
		f.mu.Lock()
		f.opens = append(f.opens, r.URL.RawQuery)
		empty := f.emptyAfter
		f.mu.Unlock()

		ready, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameReady})
		if err := conn.WriteMessage(websocket.TextMessage, ready); err != nil {
			return
		}
		if empty {
			emptyFrame, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameSessionEmpty, SessionID: "s1"})
			_ = conn.WriteMessage(websocket.TextMessage, emptyFrame)
		}
		for { // drain until the client goes away
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeInstance) setCaps(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = body
}

func (f *fakeInstance) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opens)
}

func (f *fakeInstance) lastQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opens) == 0 {
		return ""
	}
	return f.opens[len(f.opens)-1]
}

// newTestGateway builds a gateway over the given fakes and starts its router.
func newTestGateway(t *testing.T, cfg *Config, fakes ...*fakeInstance) (*Gateway, *httptest.Server) {
	t.Helper()
	if cfg.Backends == nil {
		for _, f := range fakes {
			cfg.Backends = append(cfg.Backends, BackendConfig{URL: f.srv.URL})
		}
	}
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	// Pull the snapshots once so the scores and the per-tenant counts exist before the first request.
	g.pool.refreshAll(context.Background())
	srv := httptest.NewServer(g.Router())
	t.Cleanup(srv.Close)
	return g, srv
}

// openVia dials the gateway and sends an open frame, returning the first frame the gateway sent back.
func openVia(t *testing.T, srv *httptest.Server, session, tenant string) sandbox.Frame {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/browser"
	if tenant != "" {
		url += "?tenant=" + tenant
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	open, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameOpen, SessionID: session, ToolID: "browser"})
	if err := conn.WriteMessage(websocket.TextMessage, open); err != nil {
		t.Fatalf("send open: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame sandbox.Frame
	if uerr := json.Unmarshal(payload, &frame); uerr != nil {
		t.Fatalf("decode frame %q: %v", payload, uerr)
	}
	return frame
}

// The query must reach the instance: ?tenant= is the key browserd counts its own quota with, and a path-only forward
// would silently drop it.
func TestProxyForwardsQuery(t *testing.T) {
	f := newFakeInstance(t)
	_, srv := newTestGateway(t, &Config{StickyTTLSec: 60}, f)

	if got := openVia(t, srv, "s1", "t0"); got.Type != sandbox.FrameReady {
		t.Fatalf("first frame = %q, want ready", got.Type)
	}
	if got := f.lastQuery(); got != "tenant=t0" {
		t.Errorf("forwarded query = %q, want tenant=t0", got)
	}
}

// One session must always land on the same instance, even when another instance becomes idler in between.
func TestProxyIsSticky(t *testing.T) {
	busy, idle := newFakeInstance(t), newFakeInstance(t)
	busy.setCaps(`{"available":true,"workers":2,"sessions":2,"busy":1,"queue":5,"idle_sec":300,"janitor_sec":60}`)
	idle.setCaps(`{"available":true,"workers":2,"sessions":0,"busy":0,"queue":0,"idle_sec":300,"janitor_sec":60}`)

	g, srv := newTestGateway(t, &Config{StickyTTLSec: 60}, busy, idle)

	if got := openVia(t, srv, "s1", "t0"); got.Type != sandbox.FrameReady {
		t.Fatalf("first open: %q", got.Type)
	}
	if idle.openCount() != 1 || busy.openCount() != 0 {
		t.Fatalf("the idle instance should serve the first open: idle=%d busy=%d", idle.openCount(), busy.openCount())
	}
	// The load flips: the previously idle instance is now fuller than the busy one.
	idle.setCaps(`{"available":true,"workers":2,"sessions":2,"busy":1,"queue":9,"idle_sec":300,"janitor_sec":60}`)
	busy.setCaps(`{"available":true,"workers":2,"sessions":0,"busy":0,"queue":0,"idle_sec":300,"janitor_sec":60}`)
	g.pool.refreshAll(context.Background())

	if got := openVia(t, srv, "s1", "t0"); got.Type != sandbox.FrameReady {
		t.Fatalf("second open: %q", got.Type)
	}
	if idle.openCount() != 2 || busy.openCount() != 0 {
		t.Errorf("the binding must outrank load: idle=%d busy=%d", idle.openCount(), busy.openCount())
	}
}

// session_empty means "no lease left here": the binding must be dropped, and the next open re-binds.
func TestProxyUnbindsOnSessionEmpty(t *testing.T) {
	f := newFakeInstance(t)
	f.emptyAfter = true
	g, srv := newTestGateway(t, &Config{StickyTTLSec: 60}, f)

	openVia(t, srv, "s1", "t0")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := g.sticky.Lookup(context.Background(), "s1", time.Minute); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the binding survived session_empty")
}

// A binding that outlived its instance must fail loudly: a browser cannot be migrated, and silently building an empty one
// on another instance would keep the model talking to a page it no longer has.
func TestProxySessionLost(t *testing.T) {
	f := newFakeInstance(t)
	g, srv := newTestGateway(t, &Config{StickyTTLSec: 60}, f)

	if got := openVia(t, srv, "s1", "t0"); got.Type != sandbox.FrameReady {
		t.Fatalf("first open: %q", got.Type)
	}
	g.pool.all()[0].healthy.Store(false)

	got := openVia(t, srv, "s1", "t0")
	if got.Type != sandbox.FrameError || !strings.Contains(got.Message, "session lost") {
		t.Fatalf("frame = %+v, want an error mentioning session lost", got)
	}
}

// The per-tenant cap is enforced before the open is forwarded.
func TestProxyQuota(t *testing.T) {
	f := newFakeInstance(t)
	f.sessions = `{"sessions":[{"session":"other","tenant":"t0"}]}`
	g, srv := newTestGateway(t, &Config{StickyTTLSec: 60, MaxPerTenant: 1}, f)

	if got := openVia(t, srv, "s2", "t0"); got.Type != sandbox.FrameError ||
		!strings.Contains(got.Message, "quota exceeded") {
		t.Fatalf("frame = %+v, want a quota error", got)
	}
	if f.openCount() != 0 {
		t.Errorf("a rejected open must not reach the instance (opens=%d)", f.openCount())
	}
	// Another tenant is unaffected.
	if got := openVia(t, srv, "s3", "t1"); got.Type != sandbox.FrameReady {
		t.Errorf("a different tenant must pass: %+v", got)
	}
	_ = g
}
