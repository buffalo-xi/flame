package runledger

import (
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory ledger implementing only the commands runledger.go uses.
type fakeStore struct {
	mu   sync.Mutex
	data map[string]map[string]string
	ttl  map[string]time.Duration
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		data: map[string]map[string]string{},
		ttl:  map[string]time.Duration{},
	}
}

// ttlOf reads the last renewal duration of a key (0 when it was never renewed).
func (f *fakeStore) ttlOf(key string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ttl[key]
}

func (f *fakeStore) Expire(key string, expiration time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ttl[key] = expiration
	return nil
}

func (f *fakeStore) HSetMultiple(key string, values map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.data[key]
	if m == nil {
		m = map[string]string{}
		f.data[key] = m
	}
	for k, v := range values {
		m[k] = v.(string)
	}
	return nil
}

func (f *fakeStore) HGetAll(key string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.data[key]))
	for k, v := range f.data[key] {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) HDel(key string, fields ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, field := range fields {
		delete(f.data[key], field)
	}
	return nil
}

func (f *fakeStore) HLen(key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.data[key])), nil
}

func (f *fakeStore) Del(keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range keys {
		delete(f.data, key)
	}
	return nil
}

func testEntry(runID, kind string) Entry {
	return Entry{
		Kind:       kind,
		RunID:      runID,
		SessionID:  "sess-1",
		Schema:     "t1",
		TargetID:   "target-1",
		TargetName: "poet",
		UserID:     "u1",
		RunType:    "call",
		StartedAt:  time.Now(),
	}
}

// Write to the ledger -> readable back; after dropping it the list is empty and the now-empty hash key is deleted.
func TestRecordAndDrop(t *testing.T) {
	store := newFakeStore()
	now := time.Now()

	if err := record(store, testEntry("run-1", KindAgent), now); err != nil {
		t.Fatalf("record: %v", err)
	}
	runs, err := list(store, "t1", now)
	if err != nil || len(runs) != 1 {
		t.Fatalf("list = %+v, err = %v, want 1 run", runs, err)
	}
	got := runs[0]
	if got.RunID != "run-1" || got.Kind != KindAgent || got.TargetName != "poet" || got.Instance == "" {
		t.Fatalf("snapshot = %+v", got)
	}

	if err := drop(store, "t1", "run-1"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if runs, _ = list(store, "t1", now); len(runs) != 0 {
		t.Fatalf("list after drop = %+v, want empty", runs)
	}
	if _, ok := store.data[ledgerKey("t1")]; ok {
		t.Fatal("the now-empty hash key should be deleted")
	}
}

// Writing to the ledger must renew the key: once a tenant has stopped running entirely, dirty entries should not have to
// wait for the next read to be cleaned up.
func TestRecordRefreshesKeyTTL(t *testing.T) {
	store := newFakeStore()
	if err := record(store, testEntry("run-ttl", KindAgent), time.Now()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := store.ttlOf(ledgerKey("t1")); got != ledgerTTL {
		t.Fatalf("key TTL after record = %v, want %v", got, ledgerTTL)
	}
	// The TTL must be noticeably longer than the staleness threshold, or a live tenant would be swept away by mistake
	if ledgerTTL <= runLedgerTTL {
		t.Fatalf("ledgerTTL(%v) must be greater than runLedgerTTL(%v)", ledgerTTL, runLedgerTTL)
	}
}

// An entry whose heartbeat expired (the instance crashed) is pruned on read, so it never sits in the list forever.
func TestListPrunesStale(t *testing.T) {
	store := newFakeStore()
	now := time.Now()

	if err := record(store, testEntry("run-stale", KindAgent), now.Add(-2*runLedgerTTL)); err != nil {
		t.Fatalf("record: %v", err)
	}
	runs, err := list(store, "t1", now)
	if err != nil || len(runs) != 0 {
		t.Fatalf("list = %+v, err = %v, want empty", runs, err)
	}
	if _, ok := store.data[ledgerKey("t1")]["run-stale"]; ok {
		t.Fatal("a stale entry should be pruned on read")
	}
}

// Local-process fallback + kind filtering: with Redis uninitialised (redis.Client is nil in a test) only local state is
// visible.
func TestListFallsBackToLocal(t *testing.T) {
	unregAgent := Register(testEntry("run-a", KindAgent))
	defer unregAgent()
	unregFlow := Register(testEntry("run-f", KindFlow))
	defer unregFlow()

	all, err := List("t1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List(all) = %d runs, want 2", len(all))
	}
	for _, snap := range all {
		if !snap.Local {
			t.Fatalf("run %s should be marked local: %+v", snap.RunID, snap)
		}
	}

	flows, err := List("t1", KindFlow)
	if err != nil {
		t.Fatalf("List(flow): %v", err)
	}
	if len(flows) != 1 || flows[0].RunID != "run-f" {
		t.Fatalf("List(flow) = %+v, want run-f only", flows)
	}

	// Another tenant cannot see them
	if other, _ := List("t2", ""); len(other) != 0 {
		t.Fatalf("List(t2) = %+v, want empty", other)
	}

	unregAgent()
	unregFlow()
	if left, _ := List("t1", ""); len(left) != 0 {
		t.Fatalf("List after unregister = %+v, want empty", left)
	}
}

// A call with no tenant semantics (empty schema) is skipped outright and must not pollute any tenant's ledger.
func TestRegisterRequiresSchema(t *testing.T) {
	e := testEntry("run-no-schema", KindAgent)
	e.Schema = ""
	unreg := Register(e)
	defer unreg()

	if runs, _ := List("", ""); len(runs) != 0 {
		t.Fatalf("List(empty schema) = %+v, want empty", runs)
	}
}
