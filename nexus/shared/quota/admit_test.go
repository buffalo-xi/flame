package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/storage"
)

// TestResolveWithoutDatabaseFailsOpen: the quota package must never panic when the database is not initialised (unit tests,
// early startup, a broken deployment). Everything degrades to the code fallback and the checks fail open.
func TestResolveWithoutDatabaseFailsOpen(t *testing.T) {
	if db.DB != nil {
		t.Skip("db.DB is initialised; this test covers the nil case")
	}
	lim := Resolve("t0")
	if lim.MaxSessionStorageBytes != fallback.MaxSessionStorageBytes || lim.MaxUserSessions != fallback.MaxUserSessions {
		t.Fatalf("Resolve = %+v, want the code fallback", lim)
	}
	if got := ResolveUserSessionStorage("t0", ""); got != fallback.MaxSessionStorageBytes {
		t.Fatalf("ResolveUserSessionStorage = %d, want %d", got, fallback.MaxSessionStorageBytes)
	}
	if _, err := CountUserSessions("t0", "agent", "u1"); err == nil {
		t.Fatalf("CountUserSessions should report the missing database")
	}
	// Fail-open: a broken count must never block creating a session
	if err := CheckUserSessions("t0", "agent", "u1"); err != nil {
		t.Fatalf("CheckUserSessions = %v, want nil (fail-open)", err)
	}
	// No storage handle: a write is not inspected at all
	if ev, err := Admit(context.Background(), nil, "t0", testKey, "", 1024); ev != nil || err != nil {
		t.Fatalf("Admit = (%v, %v), want (nil, nil)", ev, err)
	}
	// Snapshot reads the counter through the PG backend: it must report 0 rather than panic
	if used, limit := Snapshot(context.Background(), "t0", testKey, ""); used != 0 || limit != fallback.MaxSessionStorageBytes {
		t.Fatalf("Snapshot = (%d, %d), want (0, %d)", used, limit, fallback.MaxSessionStorageBytes)
	}
}

// TestFallbackQuotaValues pins the code defaults: they are what a tenant inherits before anyone configures it, and both
// session limits used to be -1 here. A finite ceiling is what makes the per-user session check and the storage admission
// actually fire, so a silent return to -1 would quietly disable them.
func TestFallbackQuotaValues(t *testing.T) {
	fb := Fallback()
	if fb.MaxAgents != 50 || fb.MaxSkills != 100 || fb.MaxFlows != 100 {
		t.Errorf("resource fallbacks = %d / %d / %d, want 50 / 100 / 100", fb.MaxAgents, fb.MaxSkills, fb.MaxFlows)
	}
	if fb.MaxSessionStorageBytes != 1<<30 {
		t.Errorf("MaxSessionStorageBytes fallback = %d, want 1 GiB", fb.MaxSessionStorageBytes)
	}
	if fb.MaxUserSessions != 200 {
		t.Errorf("MaxUserSessions fallback = %d, want 200", fb.MaxUserSessions)
	}
}

const testKey = "tenants/t0/users/u1/sessions/s1"

type testFile struct {
	rel  string        // path relative to the session root
	size int64         // bytes
	age  time.Duration // how long ago it was last modified
}

// newTestStorage lays out a session workspace on a temp directory.
func newTestStorage(t *testing.T, files []testFile) *storage.FilesystemStorage {
	t.Helper()
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)
	for _, f := range files {
		abs := filepath.Join(root, filepath.FromSlash(testKey+"/"+f.rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, make([]byte, f.size), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		mod := time.Now().Add(-f.age)
		if err := os.Chtimes(abs, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", abs, err)
		}
	}
	return st
}

func TestScanFiles(t *testing.T) {
	st := newTestStorage(t, []testFile{
		{rel: "a.txt", size: 10, age: time.Hour},
		{rel: "sub/b.txt", size: 32, age: 2 * time.Hour},
	})
	total, files, err := scanFiles(context.Background(), st, testKey)
	if err != nil {
		t.Fatalf("scanFiles: %v", err)
	}
	if total != 42 {
		t.Fatalf("total = %d, want 42", total)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
}

// TestEvictOldestEnough covers the normal case: oldest first, skills/ and freshly modified files spared, and the returned paths
// are relative to the session root (what the frontend shows).
func TestEvictOldestEnough(t *testing.T) {
	st := newTestStorage(t, []testFile{
		{rel: "skills/keep.md", size: 100, age: 3 * time.Hour}, // oldest, but a skill package
		{rel: "old.txt", size: 100, age: 2 * time.Hour},
		{rel: "mid.txt", size: 100, age: time.Hour},
		{rel: "new.txt", size: 100, age: time.Minute}, // inside evictSkipRecent
	})
	ctx := context.Background()
	total, files, err := scanFiles(ctx, st, testKey)
	if err != nil {
		t.Fatalf("scanFiles: %v", err)
	}
	if total != 400 {
		t.Fatalf("total = %d, want 400", total)
	}

	evicted, enough := evictOldest(ctx, st, testKey, files, &total, 350, 100)
	if !enough {
		t.Fatalf("enough = false, want true (total=%d)", total)
	}
	want := []string{"old.txt", "mid.txt"}
	if len(evicted) != len(want) || evicted[0] != want[0] || evicted[1] != want[1] {
		t.Fatalf("evicted = %v, want %v", evicted, want)
	}
	if total != 200 {
		t.Fatalf("total = %d after eviction, want 200", total)
	}

	// The protected files must still exist
	for _, rel := range []string{"skills/keep.md", "new.txt"} {
		if _, _, err := st.Stat(ctx, testKey+"/"+rel); err != nil {
			t.Fatalf("%s should have been spared: %v", rel, err)
		}
	}
}

// TestEvictOldestNotEnough: when even deleting everything evictable is not enough, it reports failure and leaves the protected files.
func TestEvictOldestNotEnough(t *testing.T) {
	st := newTestStorage(t, []testFile{
		{rel: "skills/keep.md", size: 100, age: 3 * time.Hour},
		{rel: "old.txt", size: 100, age: 2 * time.Hour},
		{rel: "new.txt", size: 100, age: time.Minute},
	})
	ctx := context.Background()
	total, files, err := scanFiles(ctx, st, testKey)
	if err != nil {
		t.Fatalf("scanFiles: %v", err)
	}

	evicted, enough := evictOldest(ctx, st, testKey, files, &total, 50, 100)
	if enough {
		t.Fatalf("enough = true, want false")
	}
	if len(evicted) != 1 || evicted[0] != "old.txt" {
		t.Fatalf("evicted = %v, want [old.txt]", evicted)
	}
	if total != 200 {
		t.Fatalf("total = %d, want 200", total)
	}
	for _, rel := range []string{"skills/keep.md", "new.txt"} {
		if _, _, err := st.Stat(ctx, testKey+"/"+rel); err != nil {
			t.Fatalf("%s should have been spared: %v", rel, err)
		}
	}
}

// TestEvictOldestCap: one request deletes at most evictMaxPerCall files, the rest is left to later writes.
func TestEvictOldestCap(t *testing.T) {
	files := make([]testFile, 0, evictMaxPerCall+10)
	for i := 0; i < evictMaxPerCall+10; i++ {
		files = append(files, testFile{rel: "f" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".txt", size: 10, age: 2 * time.Hour})
	}
	st := newTestStorage(t, files)
	ctx := context.Background()
	total, scanned, err := scanFiles(ctx, st, testKey)
	if err != nil {
		t.Fatalf("scanFiles: %v", err)
	}

	evicted, enough := evictOldest(ctx, st, testKey, scanned, &total, 100, 100)
	if enough {
		t.Fatalf("enough = true, want false (the cap prevents freeing that much)")
	}
	if len(evicted) != evictMaxPerCall {
		t.Fatalf("len(evicted) = %d, want %d", len(evicted), evictMaxPerCall)
	}
}

func TestExpired(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		u    Usage
		lim  int64
		want bool
	}{
		{"never verified", Usage{Bytes: 10}, 100, true},
		{"fresh", Usage{Bytes: 10, VerifiedAt: now}, 100, false},
		{"stale", Usage{Bytes: 10, VerifiedAt: now.Add(-CalibrateAfter - time.Minute)}, 100, true},
		{"near limit, past the short interval", Usage{Bytes: 95, VerifiedAt: now.Add(-2 * NearLimitInterval)}, 100, true},
		{"near limit, within the short interval", Usage{Bytes: 95, VerifiedAt: now.Add(-NearLimitInterval / 2)}, 100, false},
		{"far from limit", Usage{Bytes: 50, VerifiedAt: now.Add(-2 * NearLimitInterval)}, 100, false},
	}
	for _, c := range cases {
		if got := expired(c.u, c.lim); got != c.want {
			t.Errorf("%s: expired = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestGetStoreIsStableUnderConcurrency pins the contract that the backend is chosen once: were the lazy initialisation not
// serialised, concurrent first callers could each build their own store and the counter would split into two.
func TestGetStoreIsStableUnderConcurrency(t *testing.T) {
	storeMu.Lock()
	prev := store
	store = nil
	storeMu.Unlock()
	t.Cleanup(func() {
		storeMu.Lock()
		store = prev
		storeMu.Unlock()
	})

	const n = 32
	got := make([]Store, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = GetStore()
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("GetStore returned two different backends (%T vs %T)", got[0], got[i])
		}
	}
}

// fakeStore is an in-memory counter backend: the admission logic is under test here, not Redis or PostgreSQL.
type fakeStore struct {
	usage map[string]Usage
	sets  int
}

func (f *fakeStore) Get(_ context.Context, _, key string) (Usage, bool, error) {
	u, ok := f.usage[key]
	return u, ok, nil
}

func (f *fakeStore) Add(_ context.Context, _, key string, delta int64) error {
	u := f.usage[key]
	u.Bytes += delta
	f.usage[key] = u
	return nil
}

func (f *fakeStore) Set(_ context.Context, _, key string, u Usage) error {
	f.sets++
	f.usage[key] = u
	return nil
}

func (f *fakeStore) Reset(_ context.Context, _, key string) error {
	delete(f.usage, key)
	return nil
}

// useFakeStore swaps the counter backend and the storage-limit resolver for one test (both are package-level seams).
func useFakeStore(t *testing.T, f *fakeStore, limit int64) {
	t.Helper()
	prevStore, prevLimit := store, resolveSessionStorage
	store = f
	resolveSessionStorage = func(string, string) int64 { return limit }
	t.Cleanup(func() {
		store = prevStore
		resolveSessionStorage = prevLimit
	})
}

// TestAdmitFastPath: a fresh counter that still fits is trusted - no scan, no deletion, no rewrite.
func TestAdmitFastPath(t *testing.T) {
	st := newTestStorage(t, []testFile{{rel: "old.txt", size: 100, age: 2 * time.Hour}})
	f := &fakeStore{usage: map[string]Usage{testKey: {Bytes: 100, VerifiedAt: time.Now()}}}
	useFakeStore(t, f, 1000)

	evicted, err := Admit(context.Background(), st, "t0", testKey, "", 100)
	if err != nil || evicted != nil {
		t.Fatalf("Admit = (%v, %v), want (nil, nil)", evicted, err)
	}
	if f.sets != 0 {
		t.Fatalf("Set called %d times, want 0 (a trusted counter must not be rewritten)", f.sets)
	}
	if _, _, serr := st.Stat(context.Background(), testKey+"/old.txt"); serr != nil {
		t.Fatalf("old.txt should have been left alone: %v", serr)
	}
}

// TestAdmitEvictsAndRewrites: a missing counter forces a scan, the oldest file is deleted, and the real usage is written back
// as the new baseline.
func TestAdmitEvictsAndRewrites(t *testing.T) {
	st := newTestStorage(t, []testFile{
		{rel: "old.txt", size: 200, age: 2 * time.Hour},
		{rel: "new.txt", size: 100, age: time.Minute},
	})
	f := &fakeStore{usage: map[string]Usage{}}
	useFakeStore(t, f, 300)

	evicted, err := Admit(context.Background(), st, "t0", testKey, "", 100)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != "old.txt" {
		t.Fatalf("evicted = %v, want [old.txt]", evicted)
	}
	if f.sets != 1 {
		t.Fatalf("Set called %d times, want 1", f.sets)
	}
	if got := f.usage[testKey].Bytes; got != 100 {
		t.Fatalf("counter = %d after eviction, want 100 (300 scanned - 200 deleted)", got)
	}
}

// TestAdmitExceededCarriesEvicted: when deleting everything evictable is still not enough, the error reports both what was
// already deleted and the remaining usage, so the caller can explain the aftermath.
func TestAdmitExceededCarriesEvicted(t *testing.T) {
	st := newTestStorage(t, []testFile{
		{rel: "old.txt", size: 100, age: 2 * time.Hour},
		{rel: "new.txt", size: 100, age: time.Minute},
	})
	f := &fakeStore{usage: map[string]Usage{}}
	useFakeStore(t, f, 150)

	evicted, err := Admit(context.Background(), st, "t0", testKey, "", 100)
	var qe *ExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("Admit error = %v, want *ExceededError", err)
	}
	if qe.Metric != "bytes" || qe.Scope != "session" || qe.Limit != 150 || qe.Used != 100 {
		t.Fatalf("error = %+v, want session/bytes used=100 limit=150", qe)
	}
	if len(qe.Evicted) != 1 || qe.Evicted[0] != "old.txt" {
		t.Fatalf("Evicted = %v, want [old.txt]", qe.Evicted)
	}
	if len(evicted) != 1 || evicted[0] != "old.txt" {
		t.Fatalf("returned evicted = %v, want [old.txt]", evicted)
	}
}

// TestCommitAndReset: usage is only booked after a successful write, and cleared when the session goes away.
func TestCommitAndReset(t *testing.T) {
	f := &fakeStore{usage: map[string]Usage{}}
	useFakeStore(t, f, 1000)
	ctx := context.Background()

	Commit(ctx, "t0", testKey, "", 120)
	Commit(ctx, "t0", testKey, "", 30)
	if got := f.usage[testKey].Bytes; got != 150 {
		t.Fatalf("counter = %d, want 150", got)
	}

	// A delete may give usage back, but never below zero: a negative counter reads as "far from the limit" and would let
	// the fast path admit writes that really do overflow.
	Commit(ctx, "t0", testKey, "", -200)
	if got := f.usage[testKey].Bytes; got != 150 {
		t.Fatalf("counter = %d after an oversized delete, want 150 (it must not go negative)", got)
	}
	Commit(ctx, "t0", testKey, "", -50)
	if got := f.usage[testKey].Bytes; got != 100 {
		t.Fatalf("counter = %d after a normal delete, want 100", got)
	}

	// A counter left negative (an older build) must read as 0 rather than a negative usage
	f.usage[testKey] = Usage{Bytes: -5, VerifiedAt: time.Now()}
	if used, _ := Snapshot(ctx, "t0", testKey, ""); used != 0 {
		t.Fatalf("Snapshot = %d, want 0 for a negative counter", used)
	}

	Reset(ctx, "t0", testKey)
	if _, ok := f.usage[testKey]; ok {
		t.Fatalf("counter should be gone after Reset")
	}
}
