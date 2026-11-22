package agentconfig

import (
	"database/sql"
	"encoding/json"

	"flame/nexus/shared/redis"
	"flame/tools/xlog"
)

// rowCacheKey returns the cache key of one tenant's raw row (an empty tenantID means the system row).
func rowCacheKey(tenantID string) string {
	if tenantID == "" {
		return "agentcfg:row:__system__"
	}
	return "agentcfg:row:" + tenantID
}

// rowDTO is the cached value: *int32 expresses NULL (not set).
type rowDTO struct {
	MaxIterations  *int32 `json:"max_iterations"`
	MaxCallDepth   *int32 `json:"max_call_depth"`
	MaxTools       *int32 `json:"max_tools"`
	MaxOutputBytes *int32 `json:"max_output_bytes"`
	// Every column added to agent_configs must be mirrored here: the cache stores this DTO, so a field missing
	// from it silently reads as NULL ("not set") and the configured value never reaches the runtime.
	MaxRunRecords     *int32 `json:"max_run_records"`
	MaxSessionRecords *int32 `json:"max_session_records"`
}

// loadRowCached reads one row: a Redis hit returns directly; a miss (or Redis being unusable) goes back to the DB and
// backfills the cache.
func loadRowCached(tenantID string) *Row {
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
		// A real DB error: do not cache it, resolveField falls back. Logged because the caller degrades silently -
		// without this line "the row failed to load" and "the row is not configured" look identical in production.
		xlog.Error("agentconfig: failed to load row tenant=%q (falling back to the code default): %v", tenantID, err)
		return nil
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

func cacheSet(key string, r *Row) {
	if redis.Client == nil || !redis.IsConnected() {
		return
	}
	b, err := json.Marshal(r.toDTO()) // nil -> "null"
	if err != nil {
		return
	}
	_ = redis.Client.Set(key, b, redis.DefaultCacheTTL)
}

// Invalidate actively invalidates one tenant's cache (call it after changing that tenant's row).
func Invalidate(tenantID string) {
	// Same guard as cacheGet / cacheSet: with Redis down a delete would only fail, and IsConnected already knows that.
	if redis.Client == nil || !redis.IsConnected() {
		return
	}
	_ = redis.Client.Del(rowCacheKey(tenantID))
}

// InvalidateAll invalidates the system row's cache (call it after changing the system row).
// The cache holds the "raw row", so when the system row changes only this one key has to go; every tenant's next Resolve
// merges afresh.
func InvalidateAll() {
	Invalidate("")
}

// ---- DTO <-> row conversion ----

func (r *Row) toDTO() *rowDTO {
	if r == nil {
		return nil
	}
	return &rowDTO{
		MaxIterations:     nullToPtr(r.MaxIterations),
		MaxCallDepth:      nullToPtr(r.MaxCallDepth),
		MaxTools:          nullToPtr(r.MaxTools),
		MaxOutputBytes:    nullToPtr(r.MaxOutputBytes),
		MaxRunRecords:     nullToPtr(r.MaxRunRecords),
		MaxSessionRecords: nullToPtr(r.MaxSessionRecords),
	}
}

func (d *rowDTO) toRow() *Row {
	if d == nil {
		return nil
	}
	return &Row{
		MaxIterations:     ptrToNull(d.MaxIterations),
		MaxCallDepth:      ptrToNull(d.MaxCallDepth),
		MaxTools:          ptrToNull(d.MaxTools),
		MaxOutputBytes:    ptrToNull(d.MaxOutputBytes),
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
