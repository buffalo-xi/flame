package runledger

import (
	"sync"
	"testing"
	"time"
)

// fakeStore 内存版台账，只实现 runledger.go 用到的那几个命令。
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

// ttlOf 读某个键最近一次的续期时长（未续期过返回 0）。
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

// 写台账 → 读得到；注销后为空，且空 hash 的键被删除。
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
		t.Fatal("空 hash 的键应被删除")
	}
}

// 写台账要给键续期：租户彻底没在跑之后，脏条目不必等下一次读才清掉。
func TestRecordRefreshesKeyTTL(t *testing.T) {
	store := newFakeStore()
	if err := record(store, testEntry("run-ttl", KindAgent), time.Now()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := store.ttlOf(ledgerKey("t1")); got != ledgerTTL {
		t.Fatalf("record 后键 TTL = %v, want %v", got, ledgerTTL)
	}
	// TTL 必须明显长于失效判定，否则活着的租户会被误清
	if ledgerTTL <= runLedgerTTL {
		t.Fatalf("ledgerTTL(%v) 必须大于 runLedgerTTL(%v)", ledgerTTL, runLedgerTTL)
	}
}

// 心跳过期（实例崩溃）的条目在读取时被裁掉，不会一直挂在列表里。
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
		t.Fatal("失效条目应被读时裁剪掉")
	}
}

// 本进程兜底 + kind 过滤：Redis 未初始化时（测试里 redis.Client 为 nil）只看本地。
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

	// 别的租户看不到
	if other, _ := List("t2", ""); len(other) != 0 {
		t.Fatalf("List(t2) = %+v, want empty", other)
	}

	unregAgent()
	unregFlow()
	if left, _ := List("t1", ""); len(left) != 0 {
		t.Fatalf("List after unregister = %+v, want empty", left)
	}
}

// 没租户语义的调用（schema 空）直接跳过，不能污染任何租户的台账。
func TestRegisterRequiresSchema(t *testing.T) {
	e := testEntry("run-no-schema", KindAgent)
	e.Schema = ""
	unreg := Register(e)
	defer unreg()

	if runs, _ := List("", ""); len(runs) != 0 {
		t.Fatalf("List(empty schema) = %+v, want empty", runs)
	}
}
