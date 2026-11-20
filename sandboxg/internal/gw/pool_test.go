package gw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"flame/tools/xlog"
	"flame/tools/xregistry"

	"github.com/gin-gonic/gin"
)

// TestMain initializes xlog (checkHealth logs an Error when evicting, which panics without Init).
func TestMain(m *testing.M) {
	if err := xlog.Init(os.TempDir(), "sandboxg-test"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestBackendRefresh: a successful /metrics pull → the score adds up correctly and lastSeen/err are set.
func TestBackendRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"exec_inflight":         3,
			"code_busy":             map[string]int{"lua": 2, "python": 3},
			"interactive_sessions":  1,
			"interactive_processes": 4,
			"interactive_active":    1,
		})
	}))
	defer srv.Close()

	b := &Backend{URL: srv.URL}
	b.refresh(context.Background())

	// exec 3 + code(2+3) 5 + active 1 = 9 (idle processes do not count towards the score)
	if got := b.score(); got != 9 {
		t.Errorf("score = %d, want 9", got)
	}
	if b.lastSeen.IsZero() {
		t.Errorf("a successful refresh should record lastSeen")
	}
	if b.err != nil {
		t.Errorf("a successful refresh should leave err nil, got %v", b.err)
	}
}

// TestBackendRefreshError: the backend errors → err is recorded, lastSeen is not updated, the old score is kept.
func TestBackendRefreshError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := &Backend{URL: srv.URL, exec: 7}
	b.refresh(context.Background())

	if b.err == nil {
		t.Errorf("an HTTP 500 should record err")
	}
	if !b.lastSeen.IsZero() {
		t.Errorf("a failure must not update lastSeen")
	}
	if b.score() != 7 {
		t.Errorf("a failure should keep the old score, got %d, want 7", b.score())
	}
}

// TestBackendURL: http→ws conversion plus path joining.
func TestBackendURL(t *testing.T) {
	b := &Backend{URL: "http://127.0.0.1:27081"}
	if got := b.wsURL("/v1/exec"); got != "ws://127.0.0.1:27081/v1/exec" {
		t.Errorf("wsURL = %q", got)
	}
	if got := b.metricsURL(); got != "http://127.0.0.1:27081/v1/metrics" {
		t.Errorf("metricsURL = %q", got)
	}
}

// TestPoolLeastLoaded: picks the backend with the smallest score (deterministic: the first one on a tie).
func TestPoolLeastLoaded(t *testing.T) {
	p := NewPool([]BackendConfig{
		{URL: "http://a"},
		{URL: "http://b"},
		{URL: "http://c"},
	})
	p.backends[0].exec = 10
	p.backends[1].code = 1
	p.backends[2].active = 5

	if got := p.leastLoaded(); got != p.backends[1] {
		t.Errorf("should pick backend[1] (score 1), got %v", got.URL)
	}
}

// TestPoolLeastLoadedSkipsUnhealthy: unhealthy backends do not take part in selection.
func TestPoolLeastLoadedSkipsUnhealthy(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	// a has score 0 but is evicted; b has score 100 but is healthy → pick b
	p.backends[0].healthy.Store(false)
	p.backends[1].exec = 100

	if got := p.leastLoaded(); got != p.backends[1] {
		t.Errorf("should skip the unhealthy a and pick b, got %v", got.URL)
	}
}

// TestPoolCheckHealth: consecutive failures reaching the threshold → evict and call back; recovery restores health.
func TestPoolCheckHealth(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPool([]BackendConfig{{URL: srv.URL}})
	var removed []string
	onUnhealthy := func(b *Backend) { removed = append(removed, b.URL) }

	// Healthy: one success keeps it healthy
	p.checkHealth(context.Background(), 3, onUnhealthy)
	if !p.backends[0].healthy.Load() {
		t.Errorf("a healthy backend must not be evicted")
	}

	// Turning unhealthy: the first two failures stay under the threshold, the third evicts and calls back once
	up.Store(false)
	p.checkHealth(context.Background(), 3, onUnhealthy) // 1
	p.checkHealth(context.Background(), 3, onUnhealthy) // 2
	if !p.backends[0].healthy.Load() {
		t.Errorf("two failures must not evict")
	}
	p.checkHealth(context.Background(), 3, onUnhealthy) // 3
	if p.backends[0].healthy.Load() {
		t.Errorf("the third failure should evict")
	}
	if len(removed) != 1 {
		t.Errorf("eviction should call back once, got %d", len(removed))
	}

	// Recovery: healthy goes back to true, failCount resets
	up.Store(true)
	p.checkHealth(context.Background(), 3, onUnhealthy)
	if !p.backends[0].healthy.Load() {
		t.Errorf("should be healthy again after recovery")
	}
	if p.backends[0].failCount.Load() != 0 {
		t.Errorf("failCount should reset after recovery, got %d", p.backends[0].failCount.Load())
	}
}

// TestGatewayMetrics: the gateway's own /metrics sums the per-backend scores (weighted by active, idle
// processes excluded) and counts healthy backends correctly.
func TestGatewayMetrics(t *testing.T) {
	p := NewPool([]BackendConfig{
		{URL: "http://a"},
		{URL: "http://b"},
	})
	// a: exec 3 + code 1 + active 1 = 5 (procs 4 are idle processes, not scored)
	p.backends[0].exec = 3
	p.backends[0].code = 1
	p.backends[0].active = 1
	p.backends[0].procs = 4
	// b: evicted, not scored but counted in the total
	p.backends[1].healthy.Store(false)

	g := &Gateway{pool: p}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	g.handleMetrics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		TotalScore      int64 `json:"total_score"`
		BackendsTotal   int   `json:"backends_total"`
		BackendsHealthy int   `json:"backends_healthy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /metrics: %v", err)
	}
	if body.TotalScore != 5 {
		t.Errorf("total_score = %d, want 5 (active is weighted, idle procs are not)", body.TotalScore)
	}
	if body.BackendsTotal != 2 || body.BackendsHealthy != 1 {
		t.Errorf("total/healthy = %d/%d, want 2/1", body.BackendsTotal, body.BackendsHealthy)
	}
}

// TestGatewayCapabilitiesProxy: /v1/capabilities passes through to the first healthy backend and the response
// comes back unchanged.
func TestGatewayCapabilitiesProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"nsjail","network":"off"}`))
	}))
	defer backend.Close()

	p := NewPool([]BackendConfig{{URL: backend.URL}})
	g := &Gateway{pool: p}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	g.handleCapabilities(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != `{"mode":"nsjail","network":"off"}` {
		t.Errorf("body = %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestPoolFirstHealthy: deterministically picks the first healthy backend; nil when none is healthy.
func TestPoolFirstHealthy(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	if got := p.firstHealthy(); got != p.backends[0] {
		t.Errorf("should pick the first healthy one, a")
	}
	p.backends[0].healthy.Store(false)
	if got := p.firstHealthy(); got != p.backends[1] {
		t.Errorf("should pick b once a is evicted")
	}
	p.backends[1].healthy.Store(false)
	if got := p.firstHealthy(); got != nil {
		t.Errorf("should return nil when none is healthy")
	}
}

// TestPoolReconcile: reconciles against the instance list — new ones join, vanished ones leave; URL/token/healthy
// are correct.
func TestPoolReconcile(t *testing.T) {
	p := NewPool(nil)

	// First reconcile: add a and b.
	if removed := p.Reconcile([]xregistry.Instance{
		{ID: "a", Address: "127.0.0.1", Port: 27081},
		{ID: "b", Address: "127.0.0.1", Port: 27082},
	}, "tok"); removed != nil {
		t.Fatalf("the first reconcile should not remove anything, got %d", len(removed))
	}
	if len(p.backends) != 2 {
		t.Fatalf("pool size = %d, want 2", len(p.backends))
	}
	if p.backends[0].URL != "http://127.0.0.1:27081" || p.backends[0].Token != "tok" {
		t.Errorf("backends[0] = %q token=%q", p.backends[0].URL, p.backends[0].Token)
	}
	if !p.backends[0].healthy.Load() {
		t.Errorf("a new backend should be healthy by default")
	}

	// Second reconcile: keep a, drop b.
	removed := p.Reconcile([]xregistry.Instance{
		{ID: "a", Address: "127.0.0.1", Port: 27081},
	}, "tok")
	if len(removed) != 1 || removed[0].URL != "http://127.0.0.1:27082" {
		t.Fatalf("should remove b, got %v", removed)
	}
	if len(p.backends) != 1 || p.backends[0].URL != "http://127.0.0.1:27081" {
		t.Fatalf("only a should be left after the reconcile, got %v", p.backends)
	}
}
