package gw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Backend is one browserd instance plus its load snapshot.
//
// The snapshot comes from two endpoints and they answer different questions:
//   - /v1/capabilities: can it run a browser at all, how big is it (workers), how busy (sessions/busy/queue), and what
//     are its idle/janitor values (needed to validate the sticky TTL);
//   - /v1/sessions: which tenant holds which lease — the only way to enforce a per-tenant cap across instances.
//
// A failed refresh keeps the previous values: a stale estimate is better than evicting, and eviction belongs to the
// health check.
type Backend struct {
	URL   string // HTTP base address (the configured value)
	Token string

	mu       sync.RWMutex
	workers  int
	sessions int
	busy     int
	queue    int
	// perTenant is the lease count per tenant, as reported by /v1/sessions.
	perTenant map[string]int
	available bool
	engine    string
	version   string
	headed    bool
	headless  bool
	idleSec   int
	janitor   int
	lastSeen  time.Time
	err       error

	// healthy is the health state (atomic, updated by the health-check loop). It starts true and turns false once
	// consecutive failures reach the threshold, then back to true on recovery.
	healthy   atomic.Bool
	failCount atomic.Int32

	capsMu    sync.Mutex
	capsAt    time.Time
	ttlWarned bool
}

// capsPayload is the part of browserd /v1/capabilities this gateway cares about.
type capsPayload struct {
	Available       bool   `json:"available"`
	Engine          string `json:"engine"`
	Version         string `json:"version"`
	HeadedAvailable bool   `json:"headed_available"`
	HeadlessDefault bool   `json:"headless_default"`
	Workers         int    `json:"workers"`
	Sessions        int    `json:"sessions"`
	Busy            int    `json:"busy"`
	Queue           int    `json:"queue"`
	IdleSec         int    `json:"idle_sec"`
	JanitorSec      int    `json:"janitor_sec"`
	Error           string `json:"error"`
}

// sessionsPayload is the body of browserd /v1/sessions.
type sessionsPayload struct {
	Sessions []struct {
		Session string `json:"session"`
		Tenant  string `json:"tenant"`
		Busy    bool   `json:"busy"`
	} `json:"sessions"`
}

// refresh pulls both snapshots. Partial failure keeps whichever half succeeded.
func (b *Backend) refresh(ctx context.Context) {
	b.refreshCaps(ctx)
	b.refreshSessions(ctx)
}

func (b *Backend) refreshCaps(ctx context.Context) {
	var p capsPayload
	if err := b.getJSON(ctx, b.capabilitiesURL(), &p); err != nil {
		b.setErr(err)
		return
	}
	b.mu.Lock()
	b.workers = p.Workers
	b.sessions = p.Sessions
	b.busy = p.Busy
	b.queue = p.Queue
	b.available = p.Available
	b.engine = p.Engine
	b.version = p.Version
	b.headed = p.HeadedAvailable
	b.headless = p.HeadlessDefault
	b.idleSec = p.IdleSec
	b.janitor = p.JanitorSec
	b.lastSeen = time.Now()
	b.err = nil
	b.mu.Unlock()

	b.capsMu.Lock()
	b.capsAt = time.Now()
	b.capsMu.Unlock()
}

func (b *Backend) refreshSessions(ctx context.Context) {
	var p sessionsPayload
	if err := b.getJSON(ctx, b.sessionsURL(), &p); err != nil {
		b.setErr(err)
		return
	}
	counts := make(map[string]int, len(p.Sessions))
	for _, s := range p.Sessions {
		if s.Tenant != "" {
			counts[s.Tenant]++
		}
	}
	b.mu.Lock()
	b.perTenant = counts
	b.lastSeen = time.Now()
	b.err = nil
	b.mu.Unlock()
}

// getJSON is a small GET helper: it applies the instance token and decodes the body into dst.
func (b *Backend) getJSON(ctx context.Context, url string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (b *Backend) setErr(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// score is the load score: queueing is the only real scarcity signal, so it dominates.
//
//	queue x 1_000_000 + sessions x 1_000 / workers
//
// The second term is the worker occupancy rate: with the same queue, the instance with the most free workers wins. It is
// deliberately not "number of processes" (what sandboxg uses): one Chromium is 300 MB and one shell call is not, so the
// two loads are not comparable and must not share a formula.
func (b *Backend) score() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	workers := int64(b.workers)
	if workers <= 0 {
		workers = 1 // not pulled yet: fall back to the raw session count
	}
	return int64(b.queue)*1_000_000 + int64(b.sessions)*1_000/workers
}

// tenantCount is the number of live leases this instance holds for one tenant.
func (b *Backend) tenantCount(tenant string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.perTenant[tenant]
}

// backendSnapshot is the read-only load snapshot of one instance (for the gateway /metrics and /v1/capabilities).
type backendSnapshot struct {
	URL       string    `json:"url"`
	Healthy   bool      `json:"healthy"`
	Score     int64     `json:"score"`
	Available bool      `json:"available"`
	Engine    string    `json:"engine,omitempty"`
	Version   string    `json:"version,omitempty"`
	Headed    bool      `json:"headed_available"`
	Headless  bool      `json:"headless_default"`
	Workers   int       `json:"workers"`
	Sessions  int       `json:"sessions"`
	Busy      int       `json:"busy"`
	Queue     int       `json:"queue"`
	IdleSec   int       `json:"idle_sec"`
	Janitor   int       `json:"janitor_sec"`
	LastSeen  time.Time `json:"last_seen"`
	Error     string    `json:"error,omitempty"`
}

func (b *Backend) snapshot() backendSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s := backendSnapshot{
		URL:       b.URL,
		Healthy:   b.healthy.Load(),
		Score:     b.scoreLocked(),
		Available: b.available,
		Engine:    b.engine,
		Version:   b.version,
		Headed:    b.headed,
		Headless:  b.headless,
		Workers:   b.workers,
		Sessions:  b.sessions,
		Busy:      b.busy,
		Queue:     b.queue,
		IdleSec:   b.idleSec,
		Janitor:   b.janitor,
		LastSeen:  b.lastSeen,
	}
	if b.err != nil {
		s.Error = b.err.Error()
	}
	return s
}

// scoreLocked is score() for callers that already hold the read lock.
func (b *Backend) scoreLocked() int64 {
	workers := int64(b.workers)
	if workers <= 0 {
		workers = 1
	}
	return int64(b.queue)*1_000_000 + int64(b.sessions)*1_000/workers
}

// capsSnapshot returns what the sticky-TTL check needs (a copy).
type capsSnapshot struct {
	Available bool
	Engine    string
	Version   string
	Headed    bool
	Headless  bool
	Workers   int
	Sessions  int
	Busy      int
	Queue     int
	IdleSec   int
	Janitor   int
}

func (b *Backend) caps() capsSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return capsSnapshot{
		Available: b.available,
		Engine:    b.engine,
		Version:   b.version,
		Headed:    b.headed,
		Headless:  b.headless,
		Workers:   b.workers,
		Sessions:  b.sessions,
		Busy:      b.busy,
		Queue:     b.queue,
		IdleSec:   b.idleSec,
		Janitor:   b.janitor,
	}
}

// markTTLWarned reports whether this is the first time the shortfall is seen for the current idle/janitor values.
//
// Unchanged values keep the flag (so the same line is not repeated every sweep); a restart with different values clears
// it, because the verdict must be re-evaluated even if it was already reported.
func (b *Backend) markTTLWarned(idle, janitor int) bool {
	b.mu.RLock()
	unchanged := b.idleSec == idle && b.janitor == janitor
	b.mu.RUnlock()

	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	if !unchanged {
		b.ttlWarned = false
	}
	if b.ttlWarned {
		return false
	}
	b.ttlWarned = true
	return true
}

// wsURL turns the HTTP base address into ws and appends path + query.
//
// The query matters: nexus puts ?tenant= on the channel, and that is the key the pool's per-tenant quota is counted with.
// Dropping it (as a naive path-only forward does) would silently disable quota accounting on the instance.
func (b *Backend) wsURL(path, rawQuery string) string {
	u := strings.NewReplacer("http://", "ws://", "https://", "wss://").Replace(b.URL) + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

func (b *Backend) capabilitiesURL() string { return b.URL + "/v1/capabilities" }
func (b *Backend) sessionsURL() string     { return b.URL + "/v1/sessions" }
func (b *Backend) healthURL() string       { return b.URL + "/healthz" }

// ping probes /healthz once and reports whether the instance is healthy.
func (b *Backend) ping(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.healthURL(), nil)
	if err != nil {
		return false
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
