package quota

import (
	"database/sql"
	"encoding/json"

	"flame/nexus/shared/redis"
)

// rowCacheKey returns the cache key of one tenant's raw row (an empty tenantID means the system row).
func rowCacheKey(tenantID string) string {
	if tenantID == "" {
		return "quota:row:__system__"
	}
	return "quota:row:" + tenantID
}

// rowDTO is the cached value: *int32 / *int64 express NULL (not set), preserving the -1 semantics.
type rowDTO struct {
	MaxAgents              *int32 `json:"max_agents"`
	MaxSkills              *int32 `json:"max_skills"`
	MaxFlows               *int32 `json:"max_flows"`
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	MaxUserSessions        *int32 `json:"max_user_sessions"`
}

// loadRowCached reads one row: a Redis hit returns directly; a miss or an unavailable Redis goes back to the database and refills the cache.
func loadRowCached(tenantID string) *row {
	key := rowCacheKey(tenantID)

	if v, ok := cacheGet(key); ok {
		if v == "null" { // "the row does not exist" is cached
			return nil
		}
		var dto rowDTO
		if err := json.Unmarshal([]byte(v), &dto); err == nil {
			return dto.toRow()
		}
	}

	r, err := loadRow(tenantID)
	if err != nil {
		return nil // a real DB error: do not cache it, let Resolve fall back
	}
	cacheSet(key, r) // r may be nil (no such row) -> store the "null" marker
	return r
}

func cacheGet(key string) (string, bool) {
	// Disabled / never created, or the last Ping judged it unavailable -> go straight to the DB instead of waiting out a Redis timeout
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

// Invalidate actively invalidates one tenant's quota cache (called after the platform side changes that tenant's row).
func Invalidate(tenantID string) {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(tenantID))
}

// InvalidateAll invalidates the system row's cache (called after the platform side changes the system row).
// What the cache holds is the **raw row**, so changing the system row only needs this one key deleted; each tenant's next Resolve merges again
// automatically.
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
		MaxAgents:              nullToPtr(r.MaxAgents),
		MaxSkills:              nullToPtr(r.MaxSkills),
		MaxFlows:               nullToPtr(r.MaxFlows),
		MaxSessionStorageBytes: nullToPtr64(r.MaxSessionStorageBytes),
		MaxUserSessions:        nullToPtr(r.MaxUserSessions),
	}
}

func (d *rowDTO) toRow() *row {
	if d == nil {
		return nil
	}
	return &row{
		MaxAgents:              ptrToNull(d.MaxAgents),
		MaxSkills:              ptrToNull(d.MaxSkills),
		MaxFlows:               ptrToNull(d.MaxFlows),
		MaxSessionStorageBytes: ptrToNull64(d.MaxSessionStorageBytes),
		MaxUserSessions:        ptrToNull(d.MaxUserSessions),
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

func nullToPtr64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	i := v.Int64
	return &i
}

func ptrToNull64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}
