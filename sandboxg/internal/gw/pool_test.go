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

// TestMain 初始化 xlog（checkHealth 摘除时打 Error 日志，未 Init 会 panic）。
func TestMain(m *testing.M) {
	if err := xlog.Init(os.TempDir(), "sandboxg-test"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestBackendRefresh：拉 /metrics 成功 → score 正确累加 + lastSeen/err 正确。
func TestBackendRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"exec_inflight":         3,
			"code_busy":           map[string]int{"lua": 2, "python": 3},
			"interactive_sessions":  1,
			"interactive_processes": 4,
			"interactive_active":    1,
		})
	}))
	defer srv.Close()

	b := &Backend{URL: srv.URL}
	b.refresh(context.Background())

	// exec 3 + code(2+3) 5 + active 1 = 9（空闲进程不计重）
	if got := b.score(); got != 9 {
		t.Errorf("score = %d, want 9", got)
	}
	if b.lastSeen.IsZero() {
		t.Errorf("refresh 成功应记录 lastSeen")
	}
	if b.err != nil {
		t.Errorf("refresh 成功 err 应为 nil，got %v", b.err)
	}
}

// TestBackendRefreshError：后端报错 → 记 err、不更新 lastSeen、保留旧 score。
func TestBackendRefreshError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := &Backend{URL: srv.URL, exec: 7}
	b.refresh(context.Background())

	if b.err == nil {
		t.Errorf("HTTP 500 应记 err")
	}
	if !b.lastSeen.IsZero() {
		t.Errorf("失败不应更新 lastSeen")
	}
	if b.score() != 7 {
		t.Errorf("失败应保留旧 score，got %d, want 7", b.score())
	}
}

// TestBackendURL：http→ws 换算 + 路径拼接。
func TestBackendURL(t *testing.T) {
	b := &Backend{URL: "http://127.0.0.1:27081"}
	if got := b.wsURL("/v1/exec"); got != "ws://127.0.0.1:27081/v1/exec" {
		t.Errorf("wsURL = %q", got)
	}
	if got := b.metricsURL(); got != "http://127.0.0.1:27081/v1/metrics" {
		t.Errorf("metricsURL = %q", got)
	}
}

// TestPoolLeastLoaded：选分数最小的后端（确定性：相等取第一个）。
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
		t.Errorf("应选分数最小的 backend[1]（score 1），实际选了 %v", got.URL)
	}
}

// TestPoolLeastLoadedSkipsUnhealthy：不健康后端不参与选择。
func TestPoolLeastLoadedSkipsUnhealthy(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	// a 分数 0 但被摘除，b 分数 100 但健康 → 选 b
	p.backends[0].healthy.Store(false)
	p.backends[1].exec = 100

	if got := p.leastLoaded(); got != p.backends[1] {
		t.Errorf("应跳过不健康的 a，选 b，实际选了 %v", got.URL)
	}
}

// TestPoolCheckHealth：连续失败达阈值 → 摘除并回调；恢复后回健康。
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

	// 健康：一次成功即保持健康
	p.checkHealth(context.Background(), 3, onUnhealthy)
	if !p.backends[0].healthy.Load() {
		t.Errorf("健康后端不该摘除")
	}

	// 转不健康：前两次失败不到阈值，第三次摘除 + 回调一次
	up.Store(false)
	p.checkHealth(context.Background(), 3, onUnhealthy) // 1
	p.checkHealth(context.Background(), 3, onUnhealthy) // 2
	if !p.backends[0].healthy.Load() {
		t.Errorf("两次失败不该摘除")
	}
	p.checkHealth(context.Background(), 3, onUnhealthy) // 3
	if p.backends[0].healthy.Load() {
		t.Errorf("第三次失败应摘除")
	}
	if len(removed) != 1 {
		t.Errorf("摘除应回调一次，got %d", len(removed))
	}

	// 恢复：healthy 回 true、failCount 清零
	up.Store(true)
	p.checkHealth(context.Background(), 3, onUnhealthy)
	if !p.backends[0].healthy.Load() {
		t.Errorf("恢复后应回健康")
	}
	if p.backends[0].failCount.Load() != 0 {
		t.Errorf("恢复后 failCount 应清零，got %d", p.backends[0].failCount.Load())
	}
}

// TestGatewayMetrics：gateway 自身 /metrics 汇总各后端分数（分数用 active 计重、
// 不含空闲进程），并正确统计健康后端数。
func TestGatewayMetrics(t *testing.T) {
	p := NewPool([]BackendConfig{
		{URL: "http://a"},
		{URL: "http://b"},
	})
	// a：exec 3 + code 1 + active 1 = 5（procs 4 是空闲进程，不计入分数）
	p.backends[0].exec = 3
	p.backends[0].code = 1
	p.backends[0].active = 1
	p.backends[0].procs = 4
	// b：摘除，不计分但计入 total
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
		t.Errorf("total_score = %d, want 5（active 计重、空闲 procs 不计）", body.TotalScore)
	}
	if body.BackendsTotal != 2 || body.BackendsHealthy != 1 {
		t.Errorf("total/healthy = %d/%d, want 2/1", body.BackendsTotal, body.BackendsHealthy)
	}
}

// TestGatewayCapabilitiesProxy：/v1/capabilities 透传第一个健康后端，响应原样回传。
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

// TestPoolFirstHealthy：确定性挑第一个健康后端；全部不健康返回 nil。
func TestPoolFirstHealthy(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	if got := p.firstHealthy(); got != p.backends[0] {
		t.Errorf("应选第一个健康的 a")
	}
	p.backends[0].healthy.Store(false)
	if got := p.firstHealthy(); got != p.backends[1] {
		t.Errorf("a 摘除后应选 b")
	}
	p.backends[1].healthy.Store(false)
	if got := p.firstHealthy(); got != nil {
		t.Errorf("全部不健康应返回 nil")
	}
}

// TestPoolReconcile：按实例列表对账——新增进池、消失移出，URL/token/healthy 正确。
func TestPoolReconcile(t *testing.T) {
	p := NewPool(nil)

	// 首次对账：新增 a、b。
	if removed := p.Reconcile([]xregistry.Instance{
		{ID: "a", Address: "127.0.0.1", Port: 27081},
		{ID: "b", Address: "127.0.0.1", Port: 27082},
	}, "tok"); removed != nil {
		t.Fatalf("首次对账不应移除，got %d", len(removed))
	}
	if len(p.backends) != 2 {
		t.Fatalf("池大小 = %d, want 2", len(p.backends))
	}
	if p.backends[0].URL != "http://127.0.0.1:27081" || p.backends[0].Token != "tok" {
		t.Errorf("backends[0] = %q token=%q", p.backends[0].URL, p.backends[0].Token)
	}
	if !p.backends[0].healthy.Load() {
		t.Errorf("新后端应默认健康")
	}

	// 第二次对账：保留 a，去掉 b。
	removed := p.Reconcile([]xregistry.Instance{
		{ID: "a", Address: "127.0.0.1", Port: 27081},
	}, "tok")
	if len(removed) != 1 || removed[0].URL != "http://127.0.0.1:27082" {
		t.Fatalf("应移除 b，got %v", removed)
	}
	if len(p.backends) != 1 || p.backends[0].URL != "http://127.0.0.1:27081" {
		t.Fatalf("对账后池应只剩 a，got %v", p.backends)
	}
}
