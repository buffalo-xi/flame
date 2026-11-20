package gw

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMemoryStickyBindLookup: after binding it is found; once expired it is not.
func TestMemoryStickyBindLookup(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	// Not bound → miss
	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("an unbound session must not hit")
	}

	// Bound → hit
	if _, err := s.Bind(ctx, "s1", "http://a", time.Hour); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if key, ok, _ := s.Lookup(ctx, "s1", time.Hour); !ok || key != "http://a" {
		t.Errorf("should hit http://a after binding, got %q ok=%v", key, ok)
	}
}

// TestMemoryStickyExpire: once the binding expires Lookup misses and the entry is dropped.
func TestMemoryStickyExpire(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	if _, err := s.Bind(ctx, "s1", "http://a", time.Millisecond); err != nil {
		t.Fatalf("bind: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("an expired binding must not hit")
	}
}

// TestMemoryStickyBindKeepsOriginal: with an existing unexpired binding, a repeated Bind does not overwrite
// it (the original backend is kept).
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
		t.Errorf("a repeated Bind must not overwrite, got %q ok=%v", key, ok)
	}
}

// TestRouteSticky: interactive with a session is sticky; exec/code and no-session requests are pure load
// balancing.
func TestRouteSticky(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends: []BackendConfig{
			{URL: "http://a", Token: ""},
			{URL: "http://b", Token: ""},
		},
	})
	ctx := context.Background()

	// exec is pure load balancing: both backends score 0, so the first is taken (deterministic) → a
	if b, _ := g.route(ctx, "/v1/exec", ""); b != g.pool.byURL("http://a") {
		t.Errorf("exec should be pure load balancing (pick a)")
	}

	// interactive misses → load balancing picks a, and the binding is grabbed
	b1, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b1 != g.pool.byURL("http://a") {
		t.Errorf("an interactive miss should pick a")
	}

	// Manually raise a's score, then look the same session up again → still a (sticky, no longer by load)
	g.pool.byURL("http://a").mu.Lock()
	g.pool.byURL("http://a").exec = 100
	g.pool.byURL("http://a").mu.Unlock()

	b2, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route 2: %v", err)
	}
	if b2 != g.pool.byURL("http://a") {
		t.Errorf("a sticky hit must keep forwarding to a, got %v", b2.URL)
	}

	// Another session misses → load balancing picks b (a now scores 100)
	b3, err := g.route(ctx, "/v1/interactive", "s2")
	if err != nil {
		t.Fatalf("route 3: %v", err)
	}
	if b3 != g.pool.byURL("http://b") {
		t.Errorf("a new session should pick b by load, got %v", b3.URL)
	}
}

// TestRouteStickyStaleBinding: a binding pointing at a backend that no longer exists → falls back to load
// balancing.
func TestRouteStickyStaleBinding(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends:     []BackendConfig{{URL: "http://a"}},
	})
	ctx := context.Background()

	// Drop in a binding that points at a backend that does not exist
	_, _ = g.sticky.Bind(ctx, "s1", "http://gone", time.Hour)

	b, err := g.route(ctx, "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b != g.pool.byURL("http://a") {
		t.Errorf("a stale binding should fall back to load balancing and pick a, got %v", b.URL)
	}
}

// TestMemoryStickyRemoveBackend: RemoveBackend deletes every binding pointing at that backend and leaves the
// others alone.
func TestMemoryStickyRemoveBackend(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()
	_, _ = s.Bind(ctx, "s1", "http://a", time.Hour)
	_, _ = s.Bind(ctx, "s2", "http://a", time.Hour)
	_, _ = s.Bind(ctx, "s3", "http://b", time.Hour)

	_ = s.RemoveBackend(ctx, "http://a")

	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("s1 should be gone after RemoveBackend(a)")
	}
	if _, ok, _ := s.Lookup(ctx, "s2", time.Hour); ok {
		t.Errorf("s2 should be gone after RemoveBackend(a)")
	}
	if key, ok, _ := s.Lookup(ctx, "s3", time.Hour); !ok || key != "http://b" {
		t.Errorf("RemoveBackend(a) must not affect s3, got %q ok=%v", key, ok)
	}
}

// Unbind only deletes while the binding still points at that backend: a late unbind signal must not damage a
// binding that was just re-created.
func TestMemoryStickyUnbind(t *testing.T) {
	s := NewMemorySticky()
	ctx := context.Background()

	_, _ = s.Bind(ctx, "s1", "http://a", time.Hour)

	// Pointing elsewhere → untouched
	_ = s.Unbind(ctx, "s1", "http://b")
	if key, ok, _ := s.Lookup(ctx, "s1", time.Hour); !ok || key != "http://a" {
		t.Fatalf("Unbind must not delete a binding that points elsewhere, got %q ok=%v", key, ok)
	}

	// Pointing at this backend → deleted
	_ = s.Unbind(ctx, "s1", "http://a")
	if _, ok, _ := s.Lookup(ctx, "s1", time.Hour); ok {
		t.Errorf("Unbind should delete the binding when it hits the backend")
	}

	// Not bound → idempotent
	if err := s.Unbind(ctx, "nope", "http://a"); err != nil {
		t.Errorf("Unbind on a missing session should be idempotent, got %v", err)
	}
}

// fakeSticky is a controllable sticky store simulating a concurrent grab — when preemptWith is non-empty,
// Bind pretends "someone else grabbed it first for preemptWith" and reports a lost grab.
type fakeSticky struct {
	mu          sync.Mutex
	bindings    map[string]string
	preemptWith string
	unbinds     [][2]string // recorded unbind calls (session, backend)
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

// TestRoutePreemptRedirect: a lost grab (someone bound first under concurrency) → redirect to the backend that
// already holds the binding.
func TestRoutePreemptRedirect(t *testing.T) {
	g, _ := New(&Config{
		StickyTTLSec: 1800,
		Backends:     []BackendConfig{{URL: "http://a"}, {URL: "http://b"}},
	})
	fs := &fakeSticky{bindings: map[string]string{}, preemptWith: "http://b"}
	g.sticky = fs

	// Lookup miss → leastLoaded picks a → Bind fails (preempted to b) → redirect to b
	b, err := g.route(context.Background(), "/v1/interactive", "s1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if b != g.pool.byURL("http://b") {
		t.Errorf("a lost grab should redirect to b, got %v", b.URL)
	}
}
