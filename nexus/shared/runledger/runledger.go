package runledger

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/redis"
	"flame/tools/xlog"
)

// ===== The run ledger (visible across instances) =====
//
// Why it is needed: the agent's runRegistry is an in-process map, and a flow's flow_run_records are only written
// once the run finishes — so neither side's "running" state is visible. With several replicas the admin console
// cannot see what is running on another instance, and /stop cannot find the right one. This writes "the runs
// this process is executing" into Redis, and the read side merges them into one tenant view.
//
// Why Redis: it is already a hard dependency of this service (the cache); a failed ledger write only makes the
// list narrower and **never affects the run itself** (every ledger call is best-effort, and errors are only
// logged).
//
// Key layout: one hash per tenant — run:active:{schema}, field = run_id, value = Snapshot JSON.
//   - no SAdd + SCAN: xredis only exposes hash / string commands today;
//   - fields have no TTL of their own (unsupported before Redis 7.4), so liveness rests on three fallbacks:
//     1) heartbeat (heartbeat_at, refreshed every runLedgerInterval);
//     2) trimming on read (no heartbeat for more than runLedgerTTL = the instance crashed / went away, so it is
//        deleted while reading);
//     3) a key-level TTL (ledgerTTL, renewed on every write / heartbeat) — so it heals even when nobody reads.
const (
	// KindAgent is an agent run.
	KindAgent = "agent"
	// KindFlow is a workflow run.
	KindFlow = "flow"
)

const (
	// runLedgerTTL is how long without a heartbeat before this run is considered gone.
	// A bit more than 4 heartbeats: one occasional Redis hiccup should not make the list flicker.
	runLedgerTTL = 90 * time.Second
	// runLedgerInterval is the heartbeat period.
	runLedgerInterval = 20 * time.Second
	// ledgerTTL is the overall expiry of the ledger key, renewed on every write / heartbeat.
	//
	// Why it is needed: field-level TTLs are unsupported before Redis 7.4, and "trim on read" has a gap — if a
	// tenant's instances crash and nobody opens the runs page afterwards, the dirty entries stay in the hash for
	// good. With a key-level TTL the key disappears on its own as long as no live instance of that tenant is
	// running anything.
	// Twice runLedgerTTL: while a live instance keeps sending heartbeats it can never expire.
	ledgerTTL = 2 * runLedgerTTL
)

// Entry is one run's registration information (shared by agent and flow).
type Entry struct {
	Kind       string // KindAgent / KindFlow
	RunID      string
	SessionID  string
	Schema     string
	TargetID   string // agent_id / flow_id
	TargetName string // agent name / flow name
	UserID     string
	RunType    string // call / rpc / mcp / flow / agent / debug / preview
	StartedAt  time.Time
}

// Snapshot is one ledger entry (the shape the list returns).
type Snapshot struct {
	Kind       string `json:"kind"`
	RunID      string `json:"run_id"`
	SessionID  string `json:"session_id"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	UserID     string `json:"user_id"`
	RunType    string `json:"run_type"`
	Instance   string `json:"instance"`
	StartedAt  int64  `json:"started_at"`   // unix ms
	Heartbeat  int64  `json:"heartbeat_at"` // unix ms; stale once runLedgerTTL passes without an update
	// Local means this one is on the current instance (filled in by the read side, never stored in Redis):
	// only such a run can be hit directly by this process's /stop.
	Local bool `json:"local"`
	// UserName is the user's display name (also filled in by the read side, never stored): the ledger keeps only
	// user_id and the name lives in the users table — a renamed user should change here immediately rather than
	// leaving an old snapshot in the ledger.
	UserName string `json:"user_name,omitempty"`
}

// Store is the Redis capability the ledger uses (extracted so unit tests can swap in an in-memory
// implementation).
type Store interface {
	HSetMultiple(key string, values map[string]interface{}) error
	HGetAll(key string) (map[string]string, error)
	HDel(key string, fields ...string) error
	HLen(key string) (int64, error)
	Del(keys ...string) error
	// Expire renews the whole ledger key (see ledgerTTL): so dirty entries are cleaned up even when nobody reads.
	Expire(key string, expiration time.Duration) error
}

// The entries running in this process: both the heartbeat and the "Redis unavailable" fallback view read it.
var local = struct {
	mu      sync.Mutex
	entries map[string]Entry
	once    sync.Once
}{entries: make(map[string]Entry)}

var (
	instanceOnce sync.Once
	instanceName string
)

// LocalInstanceID is this instance's identifier: host:port (degrading to host when the config is not loaded).
// It is only used for display in the ledger and for the Local flag, never for routing.
func LocalInstanceID() string {
	instanceOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		if config.Cfg != nil && config.Cfg.Server.Port > 0 {
			instanceName = fmt.Sprintf("%s:%d", host, config.Cfg.Server.Port)
			return
		}
		instanceName = host
	})
	return instanceName
}

// Register records one run (writes the ledger and starts heartbeating) and returns an idempotent unregister
// function.
//
// An empty schema (a call without tenant semantics) or a disabled Redis degrades to "memory only": the run
// itself is unaffected, it is just invisible across instances.
func Register(e Entry) func() {
	if e.RunID == "" || e.Schema == "" {
		return func() {}
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}

	local.mu.Lock()
	local.entries[e.RunID] = e
	local.mu.Unlock()

	if s := currentStore(); s != nil {
		if err := record(s, e, time.Now()); err != nil {
			logErr("failed to write the run ledger schema=%s kind=%s run=%s: %v", e.Schema, e.Kind, e.RunID, err)
		}
		startHeartbeat()
		// Subscribing to stop signals depends on Redis, which may not be ready at startup, so every registration
		// retries "make sure it is subscribed" (a no-op once subscribed).
		ensureStopSubscription()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			local.mu.Lock()
			delete(local.entries, e.RunID)
			local.mu.Unlock()
			if s := currentStore(); s != nil {
				if err := drop(s, e.Schema, e.RunID); err != nil {
					logErr("failed to clean the run ledger schema=%s run=%s: %v", e.Schema, e.RunID, err)
				}
			}
		})
	}
}

// List lists a tenant's currently running runs: the Redis ledger (across instances) plus the local fallback.
//
// An empty kind means all. A non-nil err means the ledger could not be read in full (the list then holds only
// this process's runs).
func List(schema, kind string) ([]Snapshot, error) {
	var (
		remote []Snapshot
		rerr   error
	)
	if s := currentStore(); s != nil {
		remote, rerr = list(s, schema, time.Now())
	}

	byID := make(map[string]Snapshot, len(remote))
	for _, snap := range remote {
		byID[snap.RunID] = snap
	}
	// Local fallback: when Redis is unavailable, or in the instant before a heartbeat landed, at least this
	// process's runs are visible.
	for _, snap := range localSnapshots(schema, time.Now()) {
		if prev, ok := byID[snap.RunID]; ok {
			if prev.Instance == "" {
				prev.Instance = snap.Instance
			}
			if prev.StartedAt == 0 {
				prev.StartedAt = snap.StartedAt
			}
			prev.Local = true
			byID[snap.RunID] = prev
			continue
		}
		byID[snap.RunID] = snap
	}

	out := make([]Snapshot, 0, len(byID))
	for _, snap := range byID {
		if kind != "" && snap.Kind != kind {
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, rerr
}

// ===== Stop signals (across instances) =====
//
// Why they are needed: a run lives only in the memory of the machine running it (the agent's runRegistry, a
// flow's ExecContext), while /stop is an HTTP request the load balancer may route to any instance. The ledger
// answers "which machine"; this answers "tell that machine": the stop intent goes to a Redis channel, every
// instance receives it, only the one holding the run matches, and the rest simply ignore it (cancellation is
// idempotent anyway).
const stopChannelPattern = "run:stop:*"

// StopSignal is one stop intent. An empty RunID means "stop the whole session" (only the agent supports
// session-level stops).
type StopSignal struct {
	Schema    string `json:"schema"`
	Kind      string `json:"kind"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
}

type stopPublisher interface {
	Publish(channel string, payload []byte) error
}

type stopSubscriber interface {
	SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error)
}

// BroadcastStop broadcasts a stop intent to every instance (over Redis PUBLISH; this process included — handling
// it locally already is harmless, cancellation is idempotent). A false return means Redis is unavailable: the
// broadcast did not go out and only a local hit can be hoped for.
func BroadcastStop(sig StopSignal) (bool, error) {
	if sig.Schema == "" || (sig.RunID == "" && sig.SessionID == "") {
		return false, nil
	}
	pub, ok := currentStore().(stopPublisher)
	if !ok {
		return false, nil
	}
	b, err := json.Marshal(sig)
	if err != nil {
		return false, err
	}
	if err := pub.Publish("run:stop:"+sig.Schema, b); err != nil {
		return false, err
	}
	return true, nil
}

var stopHub = struct {
	mu       sync.Mutex
	handlers []func(StopSignal)
	started  bool
}{}

// SubscribeStop registers a local stop-signal handler; only one Redis subscription is kept per process.
//
// The subscription is established the first time a Redis client is available (Register retries), so the startup
// order does not matter: registering before Redis is ready does not lose the subscription for good.
func SubscribeStop(handler func(StopSignal)) {
	if handler == nil {
		return
	}
	stopHub.mu.Lock()
	stopHub.handlers = append(stopHub.handlers, handler)
	stopHub.mu.Unlock()
	ensureStopSubscription()
}

func ensureStopSubscription() {
	stopHub.mu.Lock()
	started := stopHub.started
	stopHub.mu.Unlock()
	if started {
		return
	}
	sub, ok := currentStore().(stopSubscriber)
	if !ok {
		return
	}

	stopHub.mu.Lock()
	if stopHub.started {
		stopHub.mu.Unlock()
		return
	}
	stopHub.started = true
	stopHub.mu.Unlock()

	if _, err := sub.SubscribePattern(dispatchStop, stopChannelPattern); err != nil {
		// The subscription was not established: put the flag back and retry on the next Register
		stopHub.mu.Lock()
		stopHub.started = false
		stopHub.mu.Unlock()
		logErr("failed to subscribe to the run stop channel: %v", err)
	}
}

func dispatchStop(channel string, payload []byte) {
	var sig StopSignal
	if err := json.Unmarshal(payload, &sig); err != nil {
		return
	}
	if sig.Schema == "" {
		sig.Schema = strings.TrimPrefix(channel, "run:stop:")
	}
	stopHub.mu.Lock()
	handlers := append([]func(StopSignal){}, stopHub.handlers...)
	stopHub.mu.Unlock()
	for _, h := range handlers {
		h(sig)
	}
}

// ---- internals ----

func currentStore() Store {
	if redis.Client == nil {
		return nil
	}
	return redis.Client // xredis.Client satisfies Store (only hash / string commands are used)
}

// ledgerKey is this tenant's run ledger (the xredis prefix is added uniformly by the client).
func ledgerKey(schema string) string { return "run:active:" + schema }

func snapshot(e Entry, now time.Time) Snapshot {
	return Snapshot{
		Kind:       e.Kind,
		RunID:      e.RunID,
		SessionID:  e.SessionID,
		TargetID:   e.TargetID,
		TargetName: e.TargetName,
		UserID:     e.UserID,
		RunType:    e.RunType,
		Instance:   LocalInstanceID(),
		StartedAt:  e.StartedAt.UnixMilli(),
		Heartbeat:  now.UnixMilli(),
	}
}

func record(s Store, e Entry, now time.Time) error {
	b, err := json.Marshal(snapshot(e, now))
	if err != nil {
		return err
	}
	key := ledgerKey(e.Schema)
	if err := s.HSetMultiple(key, map[string]interface{}{e.RunID: string(b)}); err != nil {
		return err
	}
	// A failed renewal does not make this entry unusable (the field is already written); the next heartbeat tries
	// again.
	_ = s.Expire(key, ledgerTTL)
	return nil
}

func drop(s Store, schema, runID string) error {
	key := ledgerKey(schema)
	if err := s.HDel(key, runID); err != nil {
		return err
	}
	// Delete the key once the last entry is gone, so no empty hash is left in the key tree.
	if n, err := s.HLen(key); err == nil && n == 0 {
		return s.Del(key)
	}
	return nil
}

func list(s Store, schema string, now time.Time) ([]Snapshot, error) {
	key := ledgerKey(schema)
	raw, err := s.HGetAll(key)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(raw))
	stale := make([]string, 0)
	for runID, val := range raw {
		var snap Snapshot
		if err := json.Unmarshal([]byte(val), &snap); err != nil {
			stale = append(stale, runID) // bad data: clean it up while here
			continue
		}
		if snap.Heartbeat == 0 || now.Sub(time.UnixMilli(snap.Heartbeat)) > runLedgerTTL {
			stale = append(stale, runID) // an entry left behind by a crashed / disconnected instance
			continue
		}
		if snap.RunID == "" {
			snap.RunID = runID
		}
		out = append(out, snap)
	}
	if len(stale) > 0 {
		if herr := s.HDel(key, stale...); herr == nil {
			if n, nerr := s.HLen(key); nerr == nil && n == 0 {
				_ = s.Del(key)
			}
		}
	}
	return out, nil
}

func localSnapshots(schema string, now time.Time) []Snapshot {
	local.mu.Lock()
	out := make([]Snapshot, 0, len(local.entries))
	for _, e := range local.entries {
		if e.Schema != schema {
			continue
		}
		snap := snapshot(e, now)
		snap.Local = true
		out = append(out, snap)
	}
	local.mu.Unlock()
	return out
}

func startHeartbeat() {
	local.once.Do(func() {
		go func() {
			t := time.NewTicker(runLedgerInterval)
			defer t.Stop()
			for range t.C {
				heartbeat()
			}
		}()
	})
}

// heartbeat refreshes the heartbeat of every run in this process (one round trip per tenant).
func heartbeat() {
	s := currentStore()
	if s == nil {
		return
	}
	local.mu.Lock()
	entries := make([]Entry, 0, len(local.entries))
	for _, e := range local.entries {
		entries = append(entries, e)
	}
	local.mu.Unlock()
	if len(entries) == 0 {
		return
	}

	now := time.Now()
	bySchema := make(map[string]map[string]interface{}, 1)
	for _, e := range entries {
		b, err := json.Marshal(snapshot(e, now))
		if err != nil {
			continue // one bad entry must not block the whole heartbeat batch
		}
		m := bySchema[e.Schema]
		if m == nil {
			m = make(map[string]interface{})
			bySchema[e.Schema] = m
		}
		m[e.RunID] = string(b)
	}
	for schema, values := range bySchema {
		key := ledgerKey(schema)
		if err := s.HSetMultiple(key, values); err != nil {
			logErr("failed to refresh the run ledger heartbeat schema=%s: %v", schema, err)
			return
		}
		// The heartbeat doubles as the key renewal: as long as live instances keep beating, the key never expires.
		_ = s.Expire(key, ledgerTTL)
	}
}

// errLogAt rate-limits ledger errors: when Redis is broken the heartbeat runs every 20s, which must not flood
// the log.
var errLogAt atomic.Int64

func logErr(format string, args ...interface{}) {
	now := time.Now().Unix()
	last := errLogAt.Load()
	if now-last < 60 || !errLogAt.CompareAndSwap(last, now) {
		return
	}
	xlog.Error(format, args...)
}
