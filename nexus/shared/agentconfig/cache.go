package agentconfig

import (
	"database/sql"
	"encoding/json"

	"flame/nexus/shared/redis"
)

// rowCacheKey 返回某租户原始行的缓存键（tenantID 为空表示系统行）。
func rowCacheKey(tenantID string) string {
	if tenantID == "" {
		return "agentcfg:row:__system__"
	}
	return "agentcfg:row:" + tenantID
}

// rowDTO 缓存值：*int32 表达 NULL（未设置）。
type rowDTO struct {
	MaxIterations  *int32 `json:"max_iterations"`
	MaxCallDepth   *int32 `json:"max_call_depth"`
	MaxTools       *int32 `json:"max_tools"`
	MaxOutputBytes *int32 `json:"max_output_bytes"`
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
		// 反序列化失败：当未命中，继续回源
	}

	r, err := loadRow(tenantID)
	if err != nil {
		return nil // 真实 DB 错误：不缓存，交给 resolveField 走 fallback
	}
	cacheSet(key, r) // r 可能为 nil（无该行）→ 存 "null" 标记，避免反复打空库
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

// Invalidate 主动失效某租户的缓存（改完该租户行后调用）。
func Invalidate(tenantID string) {
	if redis.Client == nil {
		return
	}
	_ = redis.Client.Del(rowCacheKey(tenantID))
}

// InvalidateAll 失效系统行缓存（改完系统行后调用）。
// 缓存存的是「原始行」，系统行一变只需删这一个键，各租户下次 Resolve 会自动重新合并。
func InvalidateAll() {
	Invalidate("")
}

// ---- DTO <-> row 转换 ----

func (r *row) toDTO() *rowDTO {
	if r == nil {
		return nil
	}
	return &rowDTO{
		MaxIterations:  nullToPtr(r.MaxIterations),
		MaxCallDepth:   nullToPtr(r.MaxCallDepth),
		MaxTools:       nullToPtr(r.MaxTools),
		MaxOutputBytes: nullToPtr(r.MaxOutputBytes),
	}
}

func (d *rowDTO) toRow() *row {
	if d == nil {
		return nil
	}
	return &row{
		MaxIterations:  ptrToNull(d.MaxIterations),
		MaxCallDepth:   ptrToNull(d.MaxCallDepth),
		MaxTools:       ptrToNull(d.MaxTools),
		MaxOutputBytes: ptrToNull(d.MaxOutputBytes),
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
