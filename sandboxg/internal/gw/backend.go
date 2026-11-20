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

// Backend is one sandboxd instance plus its load snapshot.
//
// The load is an "estimate": snapshots may drift momentarily, but that is enough to drive load balancing.
// A failed refresh keeps the old values (lastSeen unchanged) — a stale estimate is preferred over
// evicting proactively (eviction belongs to the health check, a later stage).
type Backend struct {
	URL   string // HTTP base address (the configured value)
	Token string

	mu       sync.RWMutex
	exec     int64     // exec_inflight
	code     int64     // Σ code_busy
	active   int64     // interactive_active (processes serving a call, used for scoring)
	sessions int64     // interactive_sessions (for observation)
	procs    int64     // interactive_processes (for observation, idle ones included)
	lastSeen time.Time // the last successful pull
	err      error     // the last pull error (for observation)

	// healthy is the health state (atomic, updated by the health-check loop). It starts true and turns
	// false once consecutive failures reach the threshold (evicted), then back to true on recovery.
	healthy   atomic.Bool
	failCount atomic.Int32 // consecutive failure count

	// Interactive timeouts (the interactive section of /v1/capabilities). Only used to check whether this
	// gateway's sticky_ttl covers the peer's idle-reclaim upper bound; it is static information pulled after
	// startup.
	capsMu sync.Mutex
	caps   interactiveCaps
}

// capsRefresh is how often capabilities is re-pulled.
//
// Why not pull once: restarting sandboxd with a changed config is routine, and idle/janitor live in that
// config. A cache that never expires leads to "the peer changed, this side still judges by the old
// values" — worse, when the verdict is "fine" there is not even a log line, so people believe the check
// is in effect.
//
// A variable rather than a constant: tests need to shrink the interval to milliseconds.
var capsRefresh = time.Minute

// interactiveCaps are the long-interaction lifecycle parameters sandboxd declares.
type interactiveCaps struct {
	IdleSec    int // interactive_idle_sec: idle reclaim of a persistent session process
	EphSec     int // interactive_ephemeral_idle_sec: the short backstop for ephemeral processes
	JanitorSec int // interactive_janitor_sec: the sweep interval (upper bound of the reclaim delay)

	known     bool      // whether a pull succeeded (a failure retries on the next refresh)
	at        time.Time // when it was pulled (decides when to re-pull)
	ttlWarned bool      // whether the TTL shortfall was already reported for this set of values
}

// capsPayload only takes the fields needed to validate the TTL; the rest (mode/network/langs/environment)
// does not concern the gateway.
type capsPayload struct {
	Interactive struct {
		IdleSec          int `json:"idle_sec"`
		EphemeralIdleSec int `json:"ephemeral_idle_sec"`
		JanitorSec       int `json:"janitor_sec"`
	} `json:"interactive"`
}

// fetchInteractiveCaps pulls /v1/capabilities once and takes the interactive section from sandboxd.
//
// An older sandboxd has no such section: everything decodes as 0 and the caller treats it as "unknown"
// (no validation, no warning).
func (b *Backend) fetchInteractiveCaps(ctx context.Context) (interactiveCaps, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.capabilitiesURL(), nil)
	if err != nil {
		return interactiveCaps{}, err
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return interactiveCaps{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return interactiveCaps{}, fmt.Errorf("capabilities: http %d", resp.StatusCode)
	}
	var p capsPayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return interactiveCaps{}, err
	}
	return interactiveCaps{
		IdleSec:    p.Interactive.IdleSec,
		EphSec:     p.Interactive.EphemeralIdleSec,
		JanitorSec: p.Interactive.JanitorSec,
		known:      true,
	}, nil
}

// setCaps records the peer's capabilities.
//
// Unchanged values keep the old warning flag (so the same line is not repeated); changed values clear it —
// the peer was restarted with a different config, so the verdict must be re-evaluated even if it was
// reported last time.
func (b *Backend) setCaps(c interactiveCaps) {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	c.at = time.Now()
	if c.IdleSec == b.caps.IdleSec && c.JanitorSec == b.caps.JanitorSec {
		c.ttlWarned = b.caps.ttlWarned
	}
	b.caps = c
}

// capsSnapshot returns the currently known long-interaction parameters; known=false means it has not been
// pulled yet or the peer does not report them.
func (b *Backend) capsSnapshot() interactiveCaps {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	return b.caps
}

// invalidateCaps marks the cache as stale so the next sweep pulls it again.
// Called when a backend recovers from "unhealthy": it most likely just restarted with a new config.
func (b *Backend) invalidateCaps() {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	b.caps.known = false
}

// refreshCapsIfStale pulls again when the cache is stale; a failure keeps the old values (retried next
// time) without spamming the log.
func (b *Backend) refreshCapsIfStale(ctx context.Context) {
	b.capsMu.Lock()
	fresh := b.caps.known && time.Since(b.caps.at) < capsRefresh
	b.capsMu.Unlock()
	if fresh {
		return
	}
	got, err := b.fetchInteractiveCaps(ctx)
	if err != nil {
		return
	}
	b.setCaps(got)
}

// markTTLWarned marks "the TTL shortfall was already reported" and returns whether this is the first time.
func (b *Backend) markTTLWarned() bool {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	if b.caps.ttlWarned {
		return false
	}
	b.caps.ttlWarned = true
	return true
}

// metricsPayload matches the JSON of sandboxd /v1/metrics.
type metricsPayload struct {
	ExecInFlight        int64            `json:"exec_inflight"`
	CodeBusy            map[string]int64 `json:"code_busy"`
	InteractiveSessions int64            `json:"interactive_sessions"`
	InteractiveProcess  int64            `json:"interactive_processes"`
	InteractiveActive   int64            `json:"interactive_active"`
}

// score is the load score: the three kinds of occupancy are summed because they share the same source.
// Only active interactive processes count (idle ones burn no CPU). A lower score means idler.
func (b *Backend) score() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.exec + b.code + b.active
}

// refresh pulls /v1/metrics once and updates the snapshot.
func (b *Backend) refresh(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.metricsURL(), nil)
	if err != nil {
		b.setErr(err)
		return
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.setErr(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.setErr(fmt.Errorf("http %d", resp.StatusCode))
		return
	}
	var m metricsPayload
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		b.setErr(err)
		return
	}
	var code int64
	for _, v := range m.CodeBusy {
		code += v
	}
	b.mu.Lock()
	b.exec = m.ExecInFlight
	b.code = code
	b.active = m.InteractiveActive
	b.sessions = m.InteractiveSessions
	b.procs = m.InteractiveProcess
	b.lastSeen = time.Now()
	b.err = nil
	b.mu.Unlock()
}

func (b *Backend) setErr(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// backendSnapshot is the read-only load snapshot of one backend (for the gateway /metrics output).
type backendSnapshot struct {
	URL       string    `json:"url"`
	Healthy   bool      `json:"healthy"`
	Score     int64     `json:"score"`
	Exec      int64     `json:"exec_inflight"`
	Code      int64     `json:"code_busy"`
	Active    int64     `json:"interactive_active"`
	Sessions  int64     `json:"interactive_sessions"`
	Processes int64     `json:"interactive_processes"`
	LastSeen  time.Time `json:"last_seen"`
	Error     string    `json:"error,omitempty"`
}

// snapshot returns the backend's current load snapshot (a read-only copy).
func (b *Backend) snapshot() backendSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s := backendSnapshot{
		URL:       b.URL,
		Healthy:   b.healthy.Load(),
		Score:     b.exec + b.code + b.active,
		Exec:      b.exec,
		Code:      b.code,
		Active:    b.active,
		Sessions:  b.sessions,
		Processes: b.procs,
		LastSeen:  b.lastSeen,
	}
	if b.err != nil {
		s.Error = b.err.Error()
	}
	return s
}

// wsURL turns the HTTP base address into ws and appends the path (such as /v1/exec).
func (b *Backend) wsURL(path string) string {
	return strings.NewReplacer("http://", "ws://", "https://", "wss://").Replace(b.URL) + path
}

func (b *Backend) metricsURL() string {
	return b.URL + "/v1/metrics"
}

func (b *Backend) capabilitiesURL() string {
	return b.URL + "/v1/capabilities"
}

func (b *Backend) healthURL() string {
	return b.URL + "/healthz"
}

// ping probes /healthz once and reports whether the backend is healthy.
func (b *Backend) ping(ctx context.Context) bool {
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
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
