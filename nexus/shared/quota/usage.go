package quota

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/redis"
)

// Usage is the bytes one workspace currently occupies plus the time of the last exact scan.
//
// When VerifiedAt is older than CalibrateAfter the number may have been dirtied by a bypass write (exec / shell inside the
// sandbox), so the next write rescans.
type Usage struct {
	Bytes      int64
	VerifiedAt time.Time
}

const (
	// CalibrateAfter is how long a counter stays fresh: past it the next write triggers an exact scan. It is the only way to
	// cover "exec writes straight to disk, nexus cannot see it" - triggered only by a write, with no background job.
	CalibrateAfter = 10 * time.Minute
	// NearLimitRatio is the share of the limit above which the scan interval tightens to NearLimitInterval.
	NearLimitRatio = 0.9
	// NearLimitInterval is the minimum scan interval close to the limit, so back-to-back writes do not scan every time.
	NearLimitInterval = 60 * time.Second

	usageTTL = 7 * 24 * time.Hour
)

// Store is the counter backend: Redis when available, otherwise PostgreSQL (the session_usage table inside the tenant schema).
//
// scopeKey is the workspace's storage-relative path (e.g. tenants/t0/users/u1/sessions/s1): using it instead of (uid, sid) lets a
// session and a future run workspace share one counter, and the caller does not have to look up the session's owner to account.
type Store interface {
	Get(ctx context.Context, schema, scopeKey string) (Usage, bool, error)
	Add(ctx context.Context, schema, scopeKey string, delta int64) error
	Set(ctx context.Context, schema, scopeKey string, u Usage) error
	Reset(ctx context.Context, schema, scopeKey string) error
}

var (
	// storeMu guards the lazy initialisation below. Without it the check-then-set on a plain package variable is a data
	// race the moment two of the first concurrent requests hit GetStore, and - worse than the race - both could build a
	// backend and end up counting into different stores.
	storeMu sync.Mutex
	store   Store
)

// GetStore returns the counter backend. It is decided on the first call and never switches at run time - flipping backends while
// Redis flaps would make the two sources count separately and manufacture drift.
func GetStore() Store {
	storeMu.Lock()
	defer storeMu.Unlock()
	if store != nil {
		return store
	}
	if redis.Client != nil && redis.IsConnected() {
		store = &redisStore{}
	} else {
		store = &pgStore{}
	}
	return store
}

// ---------------------------------------------------------------------------
// Redis backend
// ---------------------------------------------------------------------------

type redisStore struct{}

func usageKey(scopeKey string) string {
	return "quota:usage:" + scopeKey
}

func (redisStore) Get(_ context.Context, _ string, scopeKey string) (Usage, bool, error) {
	if redis.Client == nil || !redis.IsConnected() {
		return Usage{}, false, nil
	}
	m, err := redis.Client.HGetAll(usageKey(scopeKey))
	if err != nil {
		return Usage{}, false, nil // a Redis hiccup counts as "no counter", left to the scan to refill
	}
	if len(m) == 0 {
		return Usage{}, false, nil
	}
	u := Usage{}
	u.Bytes, _ = strconv.ParseInt(m["bytes"], 10, 64)
	if sec, err := strconv.ParseInt(m["verified_at"], 10, 64); err == nil && sec > 0 {
		u.VerifiedAt = time.Unix(sec, 0)
	}
	return u, true, nil
}

func (redisStore) Add(_ context.Context, _ string, scopeKey string, delta int64) error {
	if redis.Client == nil || !redis.IsConnected() {
		return fmt.Errorf("redis unavailable")
	}
	key := usageKey(scopeKey)
	if _, err := redis.Client.HIncrBy(key, "bytes", delta); err != nil {
		return err
	}
	return redis.Client.Expire(key, usageTTL)
}

func (redisStore) Set(_ context.Context, _ string, scopeKey string, u Usage) error {
	if redis.Client == nil || !redis.IsConnected() {
		return fmt.Errorf("redis unavailable")
	}
	key := usageKey(scopeKey)
	err := redis.Client.HSetMultiple(key, map[string]interface{}{
		"bytes":       u.Bytes,
		"verified_at": u.VerifiedAt.Unix(),
	})
	if err != nil {
		return err
	}
	return redis.Client.Expire(key, usageTTL)
}

func (redisStore) Reset(_ context.Context, _ string, scopeKey string) error {
	if redis.Client == nil {
		return nil
	}
	return redis.Client.Del(usageKey(scopeKey))
}

// ---------------------------------------------------------------------------
// PostgreSQL backend (deployments without Redis)
//
// The table lives in the tenant schema:
//   {schema}.session_usage(scope_key text PRIMARY KEY, used_bytes bigint, verified_at timestamptz)
// The DDL is in platform/sql/quota_storage.sql.
// A session usually has a single writer (the agent runs tools serially), so row-lock contention is not a concern.
// ---------------------------------------------------------------------------

type pgStore struct{}

func (pgStore) Get(_ context.Context, schema, scopeKey string) (Usage, bool, error) {
	if db.DB == nil {
		return Usage{}, false, fmt.Errorf("database is not initialised")
	}
	var u Usage
	var verified time.Time
	err := db.DB.QueryRow(
		`SELECT used_bytes, verified_at FROM `+schema+`.session_usage WHERE scope_key = $1`, scopeKey,
	).Scan(&u.Bytes, &verified)
	if err == sql.ErrNoRows {
		return Usage{}, false, nil
	}
	if err != nil {
		return Usage{}, false, err
	}
	u.VerifiedAt = verified
	return u, true, nil
}

func (pgStore) Add(_ context.Context, schema, scopeKey string, delta int64) error {
	if db.DB == nil {
		return fmt.Errorf("database is not initialised")
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.session_usage (scope_key, used_bytes, verified_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (scope_key) DO UPDATE
		    SET used_bytes = session_usage.used_bytes + EXCLUDED.used_bytes,
		        verified_at = now()`,
		scopeKey, delta)
	return err
}

func (pgStore) Set(_ context.Context, schema, scopeKey string, u Usage) error {
	if db.DB == nil {
		return fmt.Errorf("database is not initialised")
	}
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.session_usage (scope_key, used_bytes, verified_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (scope_key) DO UPDATE
		    SET used_bytes = EXCLUDED.used_bytes, verified_at = EXCLUDED.verified_at`,
		scopeKey, u.Bytes, u.VerifiedAt)
	return err
}

func (pgStore) Reset(_ context.Context, schema, scopeKey string) error {
	if db.DB == nil {
		return fmt.Errorf("database is not initialised")
	}
	_, err := db.DB.Exec(`DELETE FROM `+schema+`.session_usage WHERE scope_key = $1`, scopeKey)
	return err
}
