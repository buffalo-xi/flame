package quota

import (
	"database/sql"
	"encoding/json"

	"flame/nexus/shared/redis"
)

// rowCacheKey 返回某租户原始行的缓存键（tenantID 为空表示系统行）。
func rowCacheKey(tenantID string) string {
	if tenantID == "" {
		return "quota:row:__system__"
	}
	return "quota:row:" + tenantID
}

// rowDTO 缓存值：*int32 / *int64 表达 NULL（未设置），保留 -1 语义。
type rowDTO struct {
	MaxAgents              *int32 `json:"max_agents"`
	MaxSkills              *int32 `json:"max_skills"`
	MaxFlows               *int32 `json:"max_flows"`
	MaxSessionStorageBytes *int64 `json:"max_session_storage_bytes"`
	MaxUserSessions        *int32 `json:"max_user_sessions"`
}

// loadRowCached 读一行：Redis 命中直接返回；未命中或 Redis 不可用则回源 DB 并回填。
func loadRowCached(tenantID string) *row {
	key := rowCacheKey(tenantID)

	if v, ok := cacheGet(key); ok {
		if v == "null" { // 已缓存「该行不存在」
			return nil
		}
		var dto rowDTO
		if err := json.Unmarshal([]byte(v), &dto); err == nil {
			return dto.toRow()
		}
	}

	r, err := loadRow(tenantID)
	if err != nil {
		return nil // 真实 DB 错误：不缓存，交给 Resolve 走 fallback
	}
	cacheSet(key, r) // r 可能为 nil（无该行）→ 存 "null" 标记
	return r
}

func cacheGet(key string) (string, bool) {
	// 未启用/未创建，或最近一次 Ping 判定不可用 → 直接走 DB，避免等 Redis 超时
	if redis.Client == nil || !redis.IsConnected() {
		return "", false
	}
	v, err := redis.Client.Get(key)
	if err != nil || v == "" { // xredis 把 key-missing 映射为 ("", nil)
		return "", false
	}
	return v, true
}

func cacheSet(key string, r *row) {
	if redis.Client == nil || !redis.IsConnected() {
		return
	}
	b, err := json.Marshal(r.toDTO()) // nil → "null"
	if err != nil {
		return
	}
	_ = redis.Client.Set(key, b, redis.DefaultCacheTTL)
}

// Invalidate 主动失效某租户的配额缓存（平台侧改完该租户行后调用）。
func Invalidate(tenantID string) {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(tenantID))
}

// InvalidateAll 失效系统行缓存（平台侧改完系统行后调用）。
// 缓存存的是「原始行」，系统行一变只需删这一个键，各租户下次 Resolve 会自动重新合并。
func InvalidateAll() {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(""))
}

// ---- DTO <-> row 转换 ----

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
