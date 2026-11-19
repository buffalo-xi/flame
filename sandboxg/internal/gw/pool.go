package gw

import (
	"context"
	"fmt"
	"sync"

	"flame/tools/xlog"
	"flame/tools/xregistry"
)

// Pool is the backend pool: it holds the instance list and picks a backend by load snapshot + health.
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
// backends are returned so the caller can drop their sticky bindings.
// Under the lock it only does set arithmetic and never touches backend internals (score etc. are maintained by refresh).
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
	// Append in the registry's order instead of ranging over the map: firstHealthy / the leastLoaded tie-break promise a
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

// all returns every backend reference (a copy), for sweeps that do not care about health (e.g. the TTL check).
func (p *Pool) all() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	return bs
}

// leastLoaded picks the healthy backend with the smallest score (the first one on a tie). Returns nil when none is healthy.
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

// byURL finds a healthy backend by URL (used when sticky routing hits). Returns nil when it is missing or already evicted.
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

// firstHealthy returns the first healthy backend in a deterministic order (used to pass through metadata such as
// capabilities, so the snapshot source does not jump around with leastLoaded as the load changes). Returns nil when none is
// healthy.
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

// snapshots returns the load snapshot of every backend (a read-only copy, for the gateway's /metrics).
func (p *Pool) snapshots() []backendSnapshot {
	p.mu.RLock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	p.mu.RUnlock()
	out := make([]backendSnapshot, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.snapshot())
	}
	return out
}

// refreshAll pulls the /metrics snapshot of every backend. It copies the references first so no IO happens under the lock.
func (p *Pool) refreshAll(ctx context.Context) {
	p.mu.RLock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	p.mu.RUnlock()
	for _, b := range bs {
		b.refresh(ctx)
	}
}

// checkHealth pings every backend's /healthz and updates its health; a newly evicted backend is reported through onUnhealthy.
//
// CompareAndSwap(true->false) guarantees the callback fires once per "crossing the threshold" (HealthLoop is a single
// goroutine today, but the CAS keeps the semantics stable if health checks ever run concurrently).
func (p *Pool) checkHealth(ctx context.Context, failThreshold int, onUnhealthy func(*Backend)) {
	p.mu.RLock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	p.mu.RUnlock()

	for _, b := range bs {
		if b.ping(ctx) {
			if !b.healthy.Load() {
				xlog.Info("sandboxg: backend %s recovered", b.URL)
				// A recovery usually means it was just restarted: the configuration (idle / janitor) may have changed, so
				// let the next sweep re-pull capabilities instead of keeping the stale cache.
				b.invalidateCaps()
			}
			b.healthy.Store(true)
			b.failCount.Store(0)
			continue
		}
		if n := b.failCount.Add(1); int(n) >= failThreshold && b.healthy.CompareAndSwap(true, false) {
			xlog.Error("sandboxg: backend %s marked unhealthy after %d failures", b.URL, n)
			if onUnhealthy != nil {
				onUnhealthy(b)
			}
		}
	}
}
