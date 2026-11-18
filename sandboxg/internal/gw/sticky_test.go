package gw

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMemoryStickyBindLookup：绑定后能查到，过期后查不到。
func TestMemoryStickyBindLookup(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	// 未绑定 → miss
	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("未绑定 session 不应命中")
	}

	// 绑定 → 命中
	if _, err := s.Bind(ctx, "s1", "http://a", time.Hour); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if key, ok, _ := s.Lookup(ctx, "s1", time.Hour); !ok || key != "http://a" {
		t.Errorf("绑定后应命中 http://a，got %q ok=%v", key, ok)
	}
}

// TestMemoryStickyExpire：绑定过期后 Lookup 失效，且被清掉。
func TestMemoryStickyExpire(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	if _, err := s.Bind(ctx, "s1", "http://a", time.Millisecond); err != nil {
		t.Fatalf("bind: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("过期绑定不应命中")
	}
}

// TestMemoryStickyBindKeepsOriginal：已存在未过期绑定，重复 Bind 不覆盖（保持原后端）。
func TestMemoryStickyBindKeepsOriginal(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	if _, err := s.Bind(ctx, "s1", "http://a", time.Hour); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := s.Bind(ctx, "s1", "http://b", time.Hour); err != nil {
		t.Fatalf("re-bind: %v", err)
	}
	if key, ok, _ := s.Lookup(ctx, "s1", time.Hour); !ok || key != "http://a" {
		t.Errorf("重复 Bind 不应覆盖，got %q ok=%v", key, ok)
	}
}

// TestRouteSticky：interactive 带 session 粘性；exec/code 与无 session 纯负载。
func TestRouteSticky(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends: []BackendConfig{
			{URL: "http://a", Token: ""},
			{URL: "http://b", Token: ""},
		},
	})
	ctx := context.Background()

	// exec 纯负载：两个后端分数都是 0，取第一个（确定性）→ a
	if b, _ := g.route(ctx, "/v1/exec", ""); b != g.pool.byURL("http://a") {
		t.Errorf("exec 应纯负载（选 a）")
	}

	// interactive 未命中 → 负载选 a + 绑定
	b1, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b1 != g.pool.byURL("http://a") {
		t.Errorf("interactive 未命中应选 a")
	}

	// 手动把 a 的分数抬高，再查同一 session → 仍命中 a（粘性固定，不再按负载）
	g.pool.byURL("http://a").mu.Lock()
	g.pool.byURL("http://a").exec = 100
	g.pool.byURL("http://a").mu.Unlock()

	b2, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route 2: %v", err)
	}
	if b2 != g.pool.byURL("http://a") {
		t.Errorf("粘性命中应固定转发 a，实际选了 %v", b2.URL)
	}

	// 另一个 session 未命中 → 负载选 b（a 分数 100 了）
	b3, err := g.route(ctx, "/v1/interactive", "s2")
	if err != nil {
		t.Fatalf("route 3: %v", err)
	}
	if b3 != g.pool.byURL("http://b") {
		t.Errorf("新 session 应按负载选 b，实际选了 %v", b3.URL)
	}
}

// TestRouteStickyStaleBinding：绑定指向已不存在的后端 → 落到负载。
func TestRouteStickyStaleBinding(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends:    []BackendConfig{{URL: "http://a"}},
	})
	ctx := context.Background()

	// 直接塞一条指向「不存在的后端」的绑定
	_, _ = g.sticky.Bind(ctx, "s1", "http://gone", time.Hour)

	b, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b != g.pool.byURL("http://a") {
		t.Errorf("过期绑定应落到负载选 a，实际选了 %v", b.URL)
	}
}

// TestMemoryStickyRemoveBackend：RemoveBackend 删掉指向该后端的所有绑定，不影响其它。
func TestMemoryStickyRemoveBackend(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()
	_, _ = s.Bind(ctx, "s1", "http://a", time.Hour)
	_, _ = s.Bind(ctx, "s2", "http://a", time.Hour)
	_, _ = s.Bind(ctx, "s3", "http://b", time.Hour)

	_ = s.RemoveBackend(ctx, "http://a")

	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("RemoveBackend(a) 后 s1 应失效")
	}
	if _, ok, _ := s.Lookup(ctx, "s2", time.Hour); ok {
		t.Errorf("RemoveBackend(a) 后 s2 应失效")
	}
	if key, ok, _ := s.Lookup(ctx, "s3", time.Hour); !ok || key != "http://b" {
		t.Errorf("RemoveBackend(a) 不应影响 s3，got %q ok=%v", key, ok)
	}
}

// Unbind 只在绑定仍指向该后端时删除：解绑信号晚到不该误伤刚重建的绑定。
func TestMemoryStickyUnbind(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	_, _ = s.Bind(ctx, "s1", "http://a", time.Hour)

	// 指向别的后端 → 不动
	_ = s.Unbind(ctx, "s1", "http://b")
	if key, ok, _ := s.Lookup(ctx, "s1", time.Hour); !ok || key != "http://a" {
		t.Fatalf("Unbind 不该删掉指向别处的绑定，got %q ok=%v", key, ok)
	}

	// 指向本后端 → 删掉
	_ = s.Unbind(ctx, "s1", "http://a")
	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("Unbind 命中后端时绑定应删除")
	}

	// 未绑定 → 幂等
	if err := s.Unbind(ctx, "nope", "http://a"); err != nil {
		t.Errorf("对不存在的 session Unbind 应幂等，got %v", err)
	}
}

// fakeSticky 可控粘性存储：模拟并发抢占 —— Bind 时若 preemptWith 非空，
// 视为「别人已抢先绑定到 preemptWith」，返回没抢到。
type fakeSticky struct {
	mu          sync.Mutex
	bindings    map[string]string
	preemptWith string
	unbinds     [][2]string // 记录解绑调用（session, backend）
}

func (f *fakeSticky) Lookup(ctx context.Context, sid string, ttl time.Duration) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bindings[sid]
	return b, ok, nil
}

func (f *fakeSticky) Bind(ctx context.Context, sid, key string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.preemptWith != "" {
		f.bindings[sid] = f.preemptWith
		return false, nil
	}
	if _, ok := f.bindings[sid]; ok {
		return false, nil
	}
	f.bindings[sid] = key
	return true, nil
}

func (f *fakeSticky) RemoveBackend(ctx context.Context, backendKey string) error { return nil }

func (f *fakeSticky) Unbind(ctx context.Context, sid, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.bindings[sid]; ok && b == key {
		delete(f.bindings, sid)
	}
	f.unbinds = append(f.unbinds, [2]string{sid, key})
	return nil
}

// TestRoutePreemptRedirect：抢占失败（并发下别人先绑）→ 重定向到已绑定后端。
func TestRoutePreemptRedirect(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends:     []BackendConfig{{URL: "http://a"}, {URL: "http://b"}},
	})
	fs := &fakeSticky{bindings: map[string]string{}, preemptWith: "http://b"}
	g.sticky = fs

	// Lookup miss → leastLoaded 选 a → Bind 失败（被抢占到 b）→ 重定向到 b
	b, err := g.route(context.Background(), "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b != g.pool.byURL("http://b") {
		t.Errorf("抢占失败应重定向到 b，实际选了 %v", b.URL)
	}
}
