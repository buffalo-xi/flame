package gw

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestQuotaEnforcesAcrossInstances(t *testing.T) {
	// limit 1, live already 0: the first Acquire is fine, the second is refused by the pending counter even though the
	// live snapshot has not caught up yet.
	q := NewQuota(1)
	live := func(string) int { return 0 }

	if err := q.Acquire("t0", live); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := q.Acquire("t0", live); err == nil {
		t.Fatal("second acquire must be refused while the first slot is pending")
	}
	q.Release("t0")
	if err := q.Acquire("t0", live); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	// The live count is the other half of the sum.
	other := NewQuota(1)
	if err := other.Acquire("t0", func(string) int { return 1 }); err == nil {
		t.Fatal("a tenant already holding a live session must be refused")
	}
}

func TestQuotaPendingExpires(t *testing.T) {
	q := NewQuota(1)
	q.ttl = 20 * time.Millisecond
	if err := q.Acquire("t0", nil); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if q.Pending("t0") != 1 {
		t.Fatalf("pending = %d, want 1", q.Pending("t0"))
	}
	time.Sleep(40 * time.Millisecond)
	if got := q.Pending("t0"); got != 0 {
		t.Errorf("pending = %d after the ttl, want 0 (a crashed open must not leak quota)", got)
	}
}

func TestQuotaUnlimitedAndTenantless(t *testing.T) {
	unlimited := NewQuota(0)
	for i := 0; i < 10; i++ {
		if err := unlimited.Acquire("t0", func(string) int { return 99 }); err != nil {
			t.Fatalf("an unlimited quota refused: %v", err)
		}
	}
	limited := NewQuota(1)
	if err := limited.Acquire("", func(string) int { return 99 }); err != nil {
		t.Errorf("a tenant-less open must pass (nothing to count): %v", err)
	}
}

// Queueing is the only real scarcity signal, so it must dominate the occupancy rate.
func TestScoreQueueFirst(t *testing.T) {
	queued := &Backend{queue: 1, sessions: 0, workers: 10}
	packed := &Backend{queue: 0, sessions: 10, workers: 10}
	if queued.score() <= packed.score() {
		t.Errorf("score(queued=%d) must exceed score(packed=%d)", queued.score(), packed.score())
	}

	// With the same queue, the instance with the most free workers wins.
	half := &Backend{queue: 0, sessions: 5, workers: 10}
	empty := &Backend{queue: 0, sessions: 2, workers: 10}
	if empty.score() >= half.score() {
		t.Errorf("score(empty=%d) must be lower than score(half=%d)", empty.score(), half.score())
	}
}

func TestLeastLoadedPicksSmallestScore(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	bs := p.all()
	bs[0].queue = 3
	bs[1].queue = 1
	if got := p.leastLoaded(); got != bs[1] {
		t.Errorf("leastLoaded = %s, want the instance with the shorter queue", got.URL)
	}
	bs[1].healthy.Store(false)
	if got := p.leastLoaded(); got != bs[0] {
		t.Errorf("leastLoaded must skip unhealthy instances, got %v", got)
	}
	bs[0].healthy.Store(false)
	if got := p.leastLoaded(); got != nil {
		t.Errorf("leastLoaded = %v, want nil when none is healthy", got)
	}
}

func TestTenantSessionsAggregatesHealthyOnly(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}, {URL: "http://b"}})
	bs := p.all()
	bs[0].perTenant = map[string]int{"t0": 2}
	bs[1].perTenant = map[string]int{"t0": 1}
	if got := p.tenantSessions("t0"); got != 3 {
		t.Errorf("tenantSessions = %d, want 3", got)
	}
	bs[1].healthy.Store(false)
	if got := p.tenantSessions("t0"); got != 2 {
		t.Errorf("tenantSessions = %d after eviction, want 2", got)
	}
}

// The sticky TTL must cover idle + 2×janitor, otherwise one session can end up with two Chromiums.
func TestStickyTTLProblems(t *testing.T) {
	short := &Backend{URL: "http://b", idleSec: 300, janitor: 60}
	g := &Gateway{cfg: &Config{StickyTTLSec: 400}, pool: &Pool{backends: []*Backend{short}}}
	problems := g.stickyTTLProblems()
	if len(problems) != 1 || !strings.Contains(problems[0], "sticky_ttl_sec=400") {
		t.Fatalf("problems = %v, want one shortfall for 400s", problems)
	}
	if got := g.stickyTTLProblems(); len(got) != 0 {
		t.Errorf("the same shortfall must be reported once, got %v", got)
	}

	ok := &Backend{URL: "http://b", idleSec: 300, janitor: 60}
	g2 := &Gateway{cfg: &Config{StickyTTLSec: 2100}, pool: &Pool{backends: []*Backend{ok}}}
	if got := g2.stickyTTLProblems(); len(got) != 0 {
		t.Errorf("2100s covers 300 + 2x60, got %v", got)
	}

	// An instance that does not report the section is not judged at all.
	silent := &Backend{URL: "http://b"}
	g3 := &Gateway{cfg: &Config{StickyTTLSec: 1}, pool: &Pool{backends: []*Backend{silent}}}
	if got := g3.stickyTTLProblems(); len(got) != 0 {
		t.Errorf("an instance without idle_sec must be skipped, got %v", got)
	}
}

func TestRouteWithoutSessionFallsBackToLoad(t *testing.T) {
	p := NewPool([]BackendConfig{{URL: "http://a"}})
	p.all()[0].healthy.Store(true)
	g := &Gateway{cfg: &Config{StickyTTLSec: 60}, pool: p, sticky: NewMemorySticky()}

	b, hit, err := g.route(context.Background(), "")
	if err != nil || hit || b == nil {
		t.Fatalf("route(empty) = %v, %v, %v", b, hit, err)
	}
	if _, ok, _ := g.sticky.Lookup(context.Background(), "", time.Minute); ok {
		t.Error("an empty session key must not create a binding")
	}
}
