package gw

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrQuotaExceeded is "this tenant already holds as many sessions as it may".
var ErrQuotaExceeded = errors.New("browser quota exceeded")

// quotaPendingTTL bounds how long an admitted-but-not-yet-visible open keeps occupying a slot.
//
// It must cover the worst case between "this gateway says yes" and "the instance reports the lease": dial + queue
// (browserd's queue_timeout, 30s by default) + launch. After that the slot is reclaimed even if the open never showed up,
// so a crashed instance cannot leak quota forever.
const quotaPendingTTL = 60 * time.Second

// Quota enforces MaxPerTenant across every pool instance.
//
// A per-instance cap multiplies by the number of instances (two instances with max_per_tenant=1 let one tenant hold two
// browsers), so the cap has to live where the whole pool is visible: here.
//
// Two inputs:
//   - live: the count reported by /v1/sessions across healthy instances (accurate, but up to one refresh interval stale);
//   - pending: opens this gateway admitted and has not seen in the snapshot yet (closes most of that window).
//
// Without the pending counter, N concurrent opens in the same gap all pass the same check and the cap is exceeded by N-1.
type Quota struct {
	mu      sync.Mutex
	limit   int
	pending map[string]pendingQuota
	ttl     time.Duration
	now     func() time.Time
}

type pendingQuota struct {
	n       int
	expires time.Time
}

// NewQuota creates the limiter. limit <= 0 means "no cap" and every call succeeds.
func NewQuota(limit int) *Quota {
	return &Quota{
		limit:   limit,
		pending: make(map[string]pendingQuota),
		ttl:     quotaPendingTTL,
		now:     time.Now,
	}
}

// Acquire reserves one session slot for tenant; live reports the current live count across the pool.
//
// A tenant-less call (no ?tenant= on the channel) is always admitted: without a key there is nothing to count, and
// refusing would break deployments that do not send one.
func (q *Quota) Acquire(tenant string, live func(string) int) error {
	if q == nil || q.limit <= 0 || tenant == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweepLocked()

	held := q.pending[tenant].n
	used := held
	if live != nil {
		used += live(tenant)
	}
	if used >= q.limit {
		return fmt.Errorf("%w: tenant %s holds %d/%d browser session(s)", ErrQuotaExceeded, tenant, used, q.limit)
	}
	q.pending[tenant] = pendingQuota{n: held + 1, expires: q.now().Add(q.ttl)}
	return nil
}

// Release gives a reserved slot back (after the open was forwarded, whatever its outcome).
//
// The live count takes over once the instance reports the lease, so holding the pending slot any longer would double-count
// it against the tenant.
func (q *Quota) Release(tenant string) {
	if q == nil || q.limit <= 0 || tenant == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	p := q.pending[tenant]
	if p.n <= 1 {
		delete(q.pending, tenant)
		return
	}
	q.pending[tenant] = pendingQuota{n: p.n - 1, expires: p.expires}
}

// Pending reports the reserved slot count of a tenant (observability).
func (q *Quota) Pending(tenant string) int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweepLocked()
	return q.pending[tenant].n
}

func (q *Quota) sweepLocked() {
	now := q.now()
	for tenant, p := range q.pending {
		if now.After(p.expires) {
			delete(q.pending, tenant)
		}
	}
}
