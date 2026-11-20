package flowconfig

import (
	"database/sql"
	"encoding/json"

	"flame/nexus/shared/redis"
)

// rowCacheKey returns the cache key of one tenant's raw row (an empty tenantID means the system row).
func rowCacheKey(tenantID string) string {
	if tenantID == "" {
		return "flowcfg:row:__system__"
	}
	return "flowcfg:row:" + tenantID
}

// rowDTO is the cached value: *int32 expresses NULL (not set) and keeps the -1 semantics.
type rowDTO struct {
	MaxTimeout        *int32 `json:"max_timeout"`
	MaxRecursionDepth *int32 `json:"max_recursion_depth"`
	MaxNodeCount      *int32 `json:"max_node_count"`
	MaxExecCount      *int32 `json:"max_exec_count"`
	MaxLoopTimes      *int32 `json:"max_loop_times"`
	MaxRunRecords     *int32 `json:"max_run_records"`
	MaxSessionRecords *int32 `json:"max_session_records"`
}

// loadRowCached reads one row: a Redis hit returns directly; a miss (or Redis being unusable) goes back to the DB and
// backfills the cache.
func loadRowCached(tenantID string) *row {
	key := rowCacheKey(tenantID)

	if v, ok := cacheGet(key); ok {
		if v == "null" { // "the row does not exist" was cached
			return nil
		}
		var dto rowDTO
		if err := json.Unmarshal([]byte(v), &dto); err == nil {
			return dto.toRow()
		}
		// A decode failure counts as a miss, so it continues to the DB
	}

	r, err := loadRow(tenantID)
	if err != nil {
		return nil // a real DB error: do not cache it, merge falls back
	}
	cacheSet(key, r) // r may be nil (no such row) -> store the "null" marker, avoiding repeated empty lookups
	return r
}

func cacheGet(key string) (string, bool) {
	// Not enabled / not created, or the last Ping judged it unavailable -> go straight to the DB instead of waiting for a
	// Redis timeout
	if redis.Client == nil || !redis.IsConnected() {
		return "", false
	}
	v, err := redis.Client.Get(key)
	if err != nil || v == "" { // xredis maps key-missing to ("", nil)
		return "", false
	}
	return v, true
}

func cacheSet(key string, r *row) {
	if redis.Client == nil || !redis.IsConnected() {
		return
	}
	b, err := json.Marshal(r.toDTO()) // nil -> "null"
	if err != nil {
		return
	}
	_ = redis.Client.Set(key, b, redis.DefaultCacheTTL)
}

// Invalidate actively invalidates one tenant's cache (the platform side calls it after changing that tenant's row).
func Invalidate(tenantID string) {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(tenantID))
}

// InvalidateAll invalidates the system row's cache (the platform side calls it after changing the system row).
// The cache holds the "raw row", so when the system row changes only this one key has to go; every tenant's next Resolve
// merges afresh.
func InvalidateAll() {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(""))
}

// ---- DTO <-> row conversion ----

func (r *row) toDTO() *rowDTO {
	if r == nil {
		return nil
	}
	return &rowDTO{
		MaxTimeout:        nullToPtr(r.MaxTimeout),
		MaxRecursionDepth: nullToPtr(r.MaxRecursionDepth),
		MaxNodeCount:      nullToPtr(r.MaxNodeCount),
		MaxExecCount:      nullToPtr(r.MaxExecCount),
		MaxLoopTimes:      nullToPtr(r.MaxLoopTimes),
		MaxRunRecords:     nullToPtr(r.MaxRunRecords),
		MaxSessionRecords: nullToPtr(r.MaxSessionRecords),
	}
}

func (d *rowDTO) toRow() *row {
	if d == nil {
		return nil
	}
	return &row{
		MaxTimeout:        ptrToNull(d.MaxTimeout),
		MaxRecursionDepth: ptrToNull(d.MaxRecursionDepth),
		MaxNodeCount:      ptrToNull(d.MaxNodeCount),
		MaxExecCount:      ptrToNull(d.MaxExecCount),
		MaxLoopTimes:      ptrToNull(d.MaxLoopTimes),
		MaxRunRecords:     ptrToNull(d.MaxRunRecords),
		MaxSessionRecords: ptrToNull(d.MaxSessionRecords),
	}
}

func nullToPtr(v sql.NullInt32) *int32 {
	if !v.Valid {
		return nil
	}
	i := v.Int32
	return &i
}

func ptrToNull(p *int32) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *p, Valid: true}
}
