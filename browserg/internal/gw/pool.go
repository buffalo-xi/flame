package gw

import (
	"context"
	"fmt"
	"sync"

	"flame/tools/xlog"
	"flame/tools/xregistry"
)

// Pool is the instance pool: it holds the backend list and picks one by load snapshot + health.
type Pool struct {
	mu       sync.RWMutex
	backends []*Backend
}

// NewPool builds a pool from configuration.
func NewPool(cfgs []BackendConfig) *Pool {
	p := &Pool{}
	for _, c := range cfgs {
		b := &Backend{URL: c.URL, Token: c.Token}
		b.healthy.Store(true)
		p.backends = append(p.backends, b)
	}
	return p
}

// Reconcile brings the pool in line with the target instance list: new ones join, vanished ones leave, and the removed
// instances are returned so the caller can drop their sticky bindings.
func (p *Pool) Reconcile(instances []xregistry.Instance, token string) (removed []*Backend) {
	want := make(map[string]struct{}, len(instances))
	for _, in := range instances {
		want[fmt.Sprintf("http://%s:%d", in.Address, in.Port)] = struct{}{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	have := make(map[string]*Backend, len(p.backends))
	for _, b := range p.backends {
		have[b.URL] = b
	}
	// Append in the registry's order instead of ranging over the map: the leastLoaded tie-break promises a
	// deterministic order, and map iteration would reshuffle the pool on every reconcile.
	for _, in := range instances {
		url := fmt.Sprintf("http://%s:%d", in.Address, in.Port)
		if _, ok := have[url]; ok {
			continue
		}
		b := &Backend{URL: url, Token: token}
		b.healthy.Store(true)
		have[url] = b // also guards against the same instance showing up twice in one list
		p.backends = append(p.backends, b)
	}
	kept := p.backends[:0]
	for _, b := range p.backends {
		if _, ok := want[b.URL]; ok {
			kept = append(kept, b)
		} else {
			removed = append(removed, b)
		}
	}
	p.backends = kept
	return removed
}

// all returns every backend reference (a copy), for sweeps that do not care about health.
func (p *Pool) all() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	return bs
}

// leastLoaded picks the healthy instance with the smallest score (the first one on a tie). Returns nil when none is
// healthy.
func (p *Pool) leastLoaded() *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *Backend
	for _, b := range p.backends {
		if !b.healthy.Load() {
			continue
		}
		if best == nil || b.score() < best.score() {
			best = b
		}
	}
	return best
}

// byURL finds a healthy instance by URL (used when sticky routing hits). Returns nil when it is missing or already
// evicted.
func (p *Pool) byURL(url string) *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, b := range p.backends {
		if b.URL == url && b.healthy.Load() {
			return b
		}
	}
	return nil
}

// firstHealthy returns the first healthy instance in a deterministic order.
func (p *Pool) firstHealthy() *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, b := range p.backends {
		if b.healthy.Load() {
			return b
		}
	}
	return nil
}

// tenantSessions is the number of live sessions one tenant holds across every healthy instance.
//
// This is the input of the per-tenant cap. It is a snapshot (refreshed every metrics_interval), so a burst can overshoot
// by a little; Quota keeps a short-lived pending count to close most of that window.
func (p *Pool) tenantSessions(tenant string) int {
	if tenant == "" {
		return 0
	}
	var sum int
	for _, b := range p.all() {
		if b.healthy.Load() {
			sum += b.tenantCount(tenant)
		}
	}
	return sum
}

// snapshots returns the load snapshot of every instance (a read-only copy, for the gateway's /metrics and
// /v1/capabilities).
func (p *Pool) snapshots() []backendSnapshot {
	bs := p.all()
	out := make([]backendSnapshot, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.snapshot())
	}
	return out
}

// refreshAll pulls both snapshots of every instance. It copies the references first so no IO happens under the lock.
func (p *Pool) refreshAll(ctx context.Context) {
	for _, b := range p.all() {
		b.refresh(ctx)
	}
}

// checkHealth pings every instance's /healthz and updates its health; a newly evicted instance is reported through
// onUnhealthy so the caller can drop its sticky bindings.
//
// CompareAndSwap(true->false) guarantees the callback fires once per "crossing the threshold".
func (p *Pool) checkHealth(ctx context.Context, failThreshold int, onUnhealthy func(*Backend)) {
	for _, b := range p.all() {
		if b.ping(ctx) {
			if !b.healthy.Load() {
				xlog.Info("browserg: instance %s recovered", b.URL)
			}
			b.healthy.Store(true)
			b.failCount.Store(0)
			continue
		}
		if n := b.failCount.Add(1); int(n) >= failThreshold && b.healthy.CompareAndSwap(true, false) {
			xlog.Error("browserg: instance %s marked unhealthy after %d failures", b.URL, n)
			if onUnhealthy != nil {
				onUnhealthy(b)
			}
		}
	}
}
