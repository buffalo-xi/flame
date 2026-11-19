package quota

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/storage"
	"flame/tools/xlog"
)

const (
	// evictMaxPerCall caps how many files one request deletes: deleting too many would drag an upload / write into the seconds, and the rest is
	// left to the next write (the quota is gradual, there is no need to empty it in one go).
	evictMaxPerCall = 50
	// evictSkipRecent spares files modified within this window - they are very likely the artefact the agent is writing right now.
	evictSkipRecent = 5 * time.Minute
)

// ExceededError means the quota is exhausted. The caller (an HTTP handler) turns it into a 507 carrying scope / metric / used / limit
// (plus evicted, when the pre-check deleted files), so the frontend can guide the user with something like "go clean up".
type ExceededError struct {
	Scope  string // session | user
	Metric string // bytes | sessions
	Used   int64
	Limit  int64
	// Evicted lists the files the pre-check deleted before giving up (paths relative to the session root). The caller
	// reports them: "space is full" alone leaves the user wondering where those files went.
	Evicted []string
}

func (e *ExceededError) Error() string {
	if e.Metric == "sessions" {
		return fmt.Sprintf("session count limit reached (%d): delete sessions you no longer need and try again", e.Limit)
	}
	return fmt.Sprintf("session workspace is full (%s / %s): clean up files you no longer need and try again",
		humanBytes(e.Used), humanBytes(e.Limit))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	for _, u := range units {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", f/unit)
}

type fileInfo struct {
	rel  string
	size int64
	mod  time.Time
}

// resolveSessionStorage and resolveSessions are seams: production reads them from the database (user override -> tenant
// value, clamped), while tests swap in a constant to exercise the admission logic without a database.
var (
	resolveSessionStorage = ResolveUserSessionStorage
	resolveSessions       = ResolveUserSessions
)

// Admit is the quota pre-check before a write: when necessary it scans the session directory and evicts oldest-first until this write fits.
//
// scopeKey is the session workspace's storage-relative path (the result of paths.UserSessionKey);
// delta is how many bytes this write adds (for an overwrite pass new-old, which may be negative).
// It returns the files it deleted (paths relative to the session workspace root), which the caller can hand back to the frontend
// or to the model as a hint.
//
// Call convention (the order matters):
//
//	evicted, err := quota.Admit(...)   // pre-check + eviction
//	if err != nil { return err }       // 507: not a single byte is written
//	write the file
//	quota.Commit(...)                  // only account after a successful write; a failure is not counted, so the usage never inflates
//
// userID is used to pick "this user's" session storage ceiling (user_quotas overriding the tenant value);
// an empty string degrades to the tenant value.
func Admit(ctx context.Context, st storage.Storage, schema, scopeKey, userID string, delta int64) ([]string, error) {
	lim := resolveSessionStorage(schema, userID)
	if lim == -1 || st == nil || scopeKey == "" {
		return nil, nil // unlimited / not a session workspace: never touch the quota
	}
	if delta <= 0 {
		return nil, nil // an overwrite with a smaller size, or a delete: it cannot increase usage
	}

	s := GetStore()
	u, ok, _ := s.Get(ctx, schema, scopeKey)

	var evicted []string
	if !ok || u.Bytes+delta > lim || expired(u, lim) {
		total, files, err := scanFiles(ctx, st, scopeKey)
		if err != nil {
			// A failed scan does not block the write: the quota is a protective feature, and when the accounting breaks it is more reasonable
			// to let the user write than to refuse service because the accounting failed.
			xlog.Error("[QUOTA] failed to scan the session directory schema=%s key=%s: %v", schema, scopeKey, err)
			return nil, nil
		}
		var enough bool
		evicted, enough = evictOldest(ctx, st, scopeKey, files, &total, lim, delta)
		// The scanned value is the current truth: write it back as the baseline so later writes keep taking the O(1) fast path
		if serr := s.Set(ctx, schema, scopeKey, Usage{Bytes: total, VerifiedAt: time.Now()}); serr != nil {
			xlog.Error("[QUOTA] failed to write the usage back schema=%s key=%s: %v", schema, scopeKey, serr)
		}
		if !enough {
			xlog.Error("[QUOTA_ALERT] session storage exhausted schema=%s key=%s used=%d limit=%d delta=%d",
				schema, scopeKey, total, lim, delta)
			return evicted, &ExceededError{Scope: "session", Metric: "bytes", Used: total, Limit: lim, Evicted: evicted}
		}
		if len(evicted) > 0 {
			xlog.Error("[QUOTA_ALERT] evicted old files schema=%s key=%s count=%d", schema, scopeKey, len(evicted))
		}
	}
	return evicted, nil
}

// Commit accounts after a successful write (delta is the bytes actually added).
// Do **not** call it after a failed write, so the accounting never shows a false overrun of "nothing landed but the quota was charged".
func Commit(ctx context.Context, schema, scopeKey, userID string, delta int64) {
	if delta == 0 || scopeKey == "" {
		return
	}
	if resolveSessionStorage(schema, userID) == -1 {
		return // no accounting while unlimited
	}
	if delta < 0 {
		// Never let a delete push the counter below zero. A negative counter reads as "far from the limit", so the fast
		// path would happily admit writes that really do overflow until the next calibration. A counter already that low
		// means it never saw the bytes in the first place (exec writes straight to disk); calibration fixes that, guessing
		// here does not.
		u, ok, _ := GetStore().Get(ctx, schema, scopeKey)
		if !ok || u.Bytes+delta < 0 {
			return
		}
	}
	if err := GetStore().Add(ctx, schema, scopeKey, delta); err != nil {
		xlog.Error("[QUOTA] failed to account schema=%s key=%s delta=%d: %v", schema, scopeKey, delta, err)
	}
}

// Reset clears the accounting when a session is deleted.
func Reset(ctx context.Context, schema, scopeKey string) {
	if scopeKey == "" {
		return
	}
	if err := GetStore().Reset(ctx, schema, scopeKey); err != nil {
		xlog.Error("[QUOTA] failed to clear usage schema=%s key=%s: %v", schema, scopeKey, err)
	}
}

// Snapshot returns a session's current usage and ceiling (for the workspace toolbar's usage bar).
// It reads the counter only and never scans; a missing counter returns 0.
func Snapshot(ctx context.Context, schema, scopeKey, userID string) (used, limit int64) {
	limit = resolveSessionStorage(schema, userID)
	if scopeKey == "" {
		return 0, limit
	}
	if u, ok, _ := GetStore().Get(ctx, schema, scopeKey); ok {
		// A counter left negative by an older build must not surface as "minus five bytes": usage is never negative.
		if u.Bytes < 0 {
			return 0, limit
		}
		return u.Bytes, limit
	}
	return 0, limit
}

// CheckUserSessions checks whether a user has reached their session count ceiling. Over the limit it returns *ExceededError.
// A failed count lets the request through (fail-open): the quota should not block business because of one database hiccup.
func CheckUserSessions(schema, ownerType, userID string) error {
	lim := resolveSessions(schema, userID) // user override -> tenant value (clamped)
	if lim == -1 || userID == "" {
		return nil
	}
	n, err := CountUserSessions(schema, ownerType, userID)
	if err != nil {
		xlog.Error("[QUOTA] failed to count sessions schema=%s uid=%s: %v", schema, userID, err)
		return nil
	}
	if n >= lim {
		xlog.Error("[QUOTA_ALERT] session count over the limit schema=%s uid=%s used=%d limit=%d", schema, userID, n, lim)
		return &ExceededError{Scope: "user", Metric: "sessions", Used: int64(n), Limit: int64(lim)}
	}
	return nil
}

// expired decides whether the counter needs recalibrating:
//   - past CalibrateAfter: exec may have written directly without being accounted
//   - close to the ceiling and past NearLimitInterval since the last calibration: avoid "thought it was 92% when it was already 99%"
func expired(u Usage, lim int64) bool {
	if u.VerifiedAt.IsZero() {
		return true
	}
	age := time.Since(u.VerifiedAt)
	if age > CalibrateAfter {
		return true
	}
	if lim > 0 && float64(u.Bytes) >= NearLimitRatio*float64(lim) && age > NearLimitInterval {
		return true
	}
	return false
}

// scanFiles walks a session workspace recursively and returns the total bytes plus the file list (in no particular order).
func scanFiles(ctx context.Context, st storage.Storage, key string) (int64, []fileInfo, error) {
	w, ok := st.(storage.Walker)
	if !ok {
		return 0, nil, fmt.Errorf("storage driver does not support WalkPrefix")
	}
	var total int64
	files := make([]fileInfo, 0, 64)
	err := w.WalkPrefix(ctx, key, func(rel string, size int64, mod time.Time) error {
		total += size
		files = append(files, fileInfo{rel: rel, size: size, mod: mod})
		return nil
	})
	return total, files, err
}

// evictOldest deletes in ascending mtime order until total+delta <= lim or nothing is left to delete.
//
// Protection rules: never delete files under the session root's skills/, never delete files modified within evictSkipRecent, and delete at most
// evictMaxPerCall per call. It returns the deleted list and whether enough space has been freed.
func evictOldest(ctx context.Context, st storage.Storage, key string, files []fileInfo,
	total *int64, lim, delta int64) ([]string, bool) {
	if *total+delta <= lim {
		return nil, true
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	now := time.Now()
	evicted := make([]string, 0, 8)
	for _, f := range files {
		if *total+delta <= lim || len(evicted) >= evictMaxPerCall {
			break
		}
		sub := strings.TrimPrefix(f.rel, key+"/")
		if sub == "skills" || strings.HasPrefix(sub, "skills/") {
			continue // a skill package is part of the agent definition and is not treated as an artefact
		}
		if !f.mod.IsZero() && now.Sub(f.mod) < evictSkipRecent {
			continue // possibly a file the agent is writing right now
		}
		if err := st.Delete(ctx, f.rel); err != nil {
			xlog.Error("[QUOTA] failed to evict file key=%s: %v", f.rel, err)
			continue
		}
		*total -= f.size
		// Expose the path relative to the session root (for the frontend hint); deleting still used the full key
		evicted = append(evicted, sub)
	}
	return evicted, *total+delta <= lim
}
