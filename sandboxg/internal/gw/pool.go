package gw

import (
	"context"
	"fmt"
	"sync"

	"flame/tools/xlog"
	"flame/tools/xregistry"
)

// Pool 后端池：持有实例列表，按负载快照 + 健康状态选后端。
type Pool struct {
	mu       sync.RWMutex
	backends []*Backend
}

// NewPool 从配置构造后端池。
func NewPool(cfgs []BackendConfig) *Pool {
	p := &Pool{}
	for _, c := range cfgs {
		b := &Backend{URL: c.URL, Token: c.Token}
		b.healthy.Store(true)
		p.backends = append(p.backends, b)
	}
	return p
}

// Reconcile 按目标实例列表对账：新增进池、消失移出，返回被移出的后端供清粘性。
// 锁内只做集合运算，不碰后端内部状态（score 等由 refresh 异步维护）。
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
	for url := range want {
		if _, ok := have[url]; !ok {
			b := &Backend{URL: url, Token: token}
			b.healthy.Store(true)
			p.backends = append(p.backends, b)
		}
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

// all 返回所有后端引用（拷贝）。供不关心健康状态的巡检使用（如 TTL 校验）。
func (p *Pool) all() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	return bs
}

// leastLoaded 从健康后端里选分数最小者（相等取第一个）。全部不健康返回 nil。
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

// byURL 按 URL 找健康后端（粘性路由命中时用）。找不到或已摘除返回 nil。
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

// firstHealthy 返回第一个健康后端（确定性顺序，供 capabilities 等元数据透传，
// 避免 leastLoaded 随负载跳变导致快照来源不稳定）。全部不健康返回 nil。
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

// snapshots 返回所有后端负载快照（只读拷贝，供 gateway /metrics）。
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

// refreshAll 拉取所有后端的 /metrics 快照。先拷贝引用再逐个刷新，避免持锁做 IO。
func (p *Pool) refreshAll(ctx context.Context) {
	p.mu.RLock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	p.mu.RUnlock()
	for _, b := range bs {
		b.refresh(ctx)
	}
}

// checkHealth 探所有后端 /healthz，更新健康状态；发现新摘除时回调 onUnhealthy。
//
// CompareAndSwap(true→false) 保证同一次「跨阈值」只回调一次（虽然 HealthLoop
// 是单 goroutine，CAS 让语义更稳，避免未来并发健康检查时重复删绑定）。
func (p *Pool) checkHealth(ctx context.Context, failThreshold int, onUnhealthy func(*Backend)) {
	p.mu.RLock()
	bs := make([]*Backend, len(p.backends))
	copy(bs, p.backends)
	p.mu.RUnlock()

	for _, b := range bs {
		if b.ping(ctx) {
			if !b.healthy.Load() {
				xlog.Info("sandboxg: backend %s recovered", b.URL)
				// 恢复多半意味着它刚重启过：配置（idle / janitor）可能变了，
				// 让下一轮巡检重新拉 capabilities，而不是继续用旧缓存。
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
