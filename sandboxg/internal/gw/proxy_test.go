package gw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flame/tools/sandbox"

	"github.com/gorilla/websocket"
)

// newFakeBackend starts a fake sandboxd: ready is returned on open, then onMessage decides how to answer
// (nil means drop).
func newFakeBackend(t *testing.T, onMessage func(ws *websocket.Conn, f sandbox.Frame)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		for {
			_, data, rerr := ws.ReadMessage()
			if rerr != nil {
				return
			}
			var f sandbox.Frame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			if f.Type == sandbox.FrameOpen {
				b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameReady})
				if ws.WriteMessage(websocket.TextMessage, b) != nil {
					return
				}
				continue
			}
			if onMessage != nil {
				onMessage(ws, f)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestGateway builds a gateway with only this backend and swaps the sticky store for an observable fake.
func newTestGateway(t *testing.T, backendURL string) (*Gateway, *fakeSticky) {
	t.Helper()
	g, err := New(&Config{
		StickyTTLSec: 2100,
		Backends:     []BackendConfig{{URL: backendURL}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fs := &fakeSticky{bindings: map[string]string{"s1": backendURL}}
	g.sticky = fs
	return g, fs
}

// dialGateway connects to the gateway's /v1/interactive and completes open → ready.
func dialGateway(t *testing.T, g *Gateway, sessionID string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(g.Router())
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/interactive", nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	open, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameOpen, SessionID: sessionID, ToolID: "shell"})
	if err := conn.WriteMessage(websocket.TextMessage, open); err != nil {
		t.Fatalf("send open: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil { // wait for ready, confirming the proxy is up
		t.Fatalf("read ready: %v", err)
	}
	return conn
}

// When sandboxd says "this session has no long-interaction tool left", the sticky binding must be dropped at
// once — otherwise it keeps pinning the session to the original backend for up to sticky_ttl_sec.
func TestProxyUnbindOnSessionEmpty(t *testing.T) {
	backend := newFakeBackend(t, func(ws *websocket.Conn, f sandbox.Frame) {
		if f.Type != sandbox.FrameKill {
			return
		}
		b, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameSessionEmpty, SessionID: "s1"})
		_ = ws.WriteMessage(websocket.TextMessage, b)
	})
	g, fs := newTestGateway(t, backend.URL)

	conn := dialGateway(t, g, "s1")
	kill, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameKill})
	if err := conn.WriteMessage(websocket.TextMessage, kill); err != nil {
		t.Fatalf("send kill: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fs.mu.Lock()
		got := append([][2]string(nil), fs.unbinds...)
		_, stillBound := fs.bindings["s1"]
		fs.mu.Unlock()
		if len(got) > 0 {
			if got[0] != [2]string{"s1", backend.URL} {
				t.Fatalf("unbind arguments = %v, want [s1 %s]", got[0], backend.URL)
			}
			if stillBound {
				t.Errorf("the binding should be deleted after unbinding")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("receiving session_empty did not trigger an unbind")
}

// newCapsBackend starts a throwaway backend that only answers /v1/capabilities.
func newCapsBackend(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The sticky TTL must cover sandboxd's idle-reclaim upper bound: sticky_ttl > idle + 2×janitor.
// The two values live in the config files of two different processes, so the gateway can only validate
// against what capabilities reports.
func TestStickyTTLProblems(t *testing.T) {
	srv := newCapsBackend(t, `{"mode":"none","interactive":{"idle_sec":1800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	ctx := context.Background()

	// 2100 > 1800 + 240 → passes
	ok, err := New(&Config{StickyTTLSec: 2100, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p := ok.stickyTTLProblems(ctx); len(p) != 0 {
		t.Fatalf("2100s should pass, got %v", p)
	}

	// 1900 is not greater than 1920 → reported once
	bad, err := New(&Config{StickyTTLSec: 1900, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	p := bad.stickyTTLProblems(ctx)
	if len(p) != 1 {
		t.Fatalf("one problem should be reported, got %v", p)
	}
	for _, want := range []string{"sticky_ttl_sec=1900", "1800", "1920"} {
		if !strings.Contains(p[0], want) {
			t.Errorf("the diagnostic is missing %q: %s", want, p[0])
		}
	}
	// Each backend is reported once, otherwise a line would be emitted every 2s
	if again := bad.stickyTTLProblems(ctx); len(again) != 0 {
		t.Errorf("must not repeat the warning, got %v", again)
	}

	// An older sandboxd does not report the interactive section: nothing to validate against, so it passes
	legacy := newCapsBackend(t, `{"mode":"none"}`)
	old, err := New(&Config{StickyTTLSec: 1, Backends: []BackendConfig{{URL: legacy.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if p := old.stickyTTLProblems(ctx); len(p) != 0 {
		t.Errorf("must not warn when the peer reports nothing, got %v", p)
	}
}

// After the peer is restarted with a changed config the check must run again: a cache that never expires
// would let the old values mask a new failing one — and when the old values pass there is not even a log line.
func TestStickyTTLProblemsRefreshesCaps(t *testing.T) {
	var payload atomic.Value
	payload.Store(`{"mode":"none","interactive":{"idle_sec":1800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	defer srv.Close()

	old := capsRefresh
	capsRefresh = time.Millisecond
	defer func() { capsRefresh = old }()

	g, err := New(&Config{StickyTTLSec: 2100, Backends: []BackendConfig{{URL: srv.URL}}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if p := g.stickyTTLProblems(ctx); len(p) != 0 {
		t.Fatalf("2100 against idle=1800 should pass, got %v", p)
	}

	// sandboxd switches to idle=2800 and restarts (same address)
	payload.Store(`{"mode":"none","interactive":{"idle_sec":2800,"ephemeral_idle_sec":120,"janitor_sec":60}}`)
	time.Sleep(5 * time.Millisecond) // let the cache expire

	p := g.stickyTTLProblems(ctx)
	if len(p) != 1 {
		t.Fatalf("a larger idle on the peer should be reported again, got %v", p)
	}
	if !strings.Contains(p[0], "2800") || !strings.Contains(p[0], "2920") {
		t.Errorf("the diagnostic should be based on the new values: %s", p[0])
	}
}

// The negative case: without session_empty (other tools of the same session may still be alive) unbinding must
// never happen.
func TestProxyKeepsBindingWithoutEmptyFrame(t *testing.T) {
	backend := newFakeBackend(t, nil) // answers nothing to kill
	g, fs := newTestGateway(t, backend.URL)

	conn := dialGateway(t, g, "s1")
	kill, _ := json.Marshal(&sandbox.Frame{Type: sandbox.FrameKill})
	if err := conn.WriteMessage(websocket.TextMessage, kill); err != nil {
		t.Fatalf("send kill: %v", err)
	}
	_ = conn.Close() // end this proxy session

	// Leave enough time for an (unwanted) unbind to happen and be recorded.
	time.Sleep(200 * time.Millisecond)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.unbinds) != 0 {
		t.Errorf("unbound without session_empty: %v", fs.unbinds)
	}
	if _, ok := fs.bindings["s1"]; !ok {
		t.Errorf("the binding should be kept (other tools still use this backend)")
	}
}
