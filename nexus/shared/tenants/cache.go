package tenants

import (
	"encoding/json"
	"strings"
	"time"

	"flame/nexus/shared/redis"
)

func tenantCacheKey(schema string) string {
	return "tenant:row:" + schema
}

// loadCache 命中返回 (tenant, true)：tenant 为 nil 表示负缓存（"未注册"）。
// 未命中或 Redis 不可用返回 (nil, false)，由调用方回源 DB。
func loadCache(schema string) (*Tenant, bool) {
	// 未启用/未创建，或最近一次 Ping 判定不可用 → 直接回源 DB，避免等 Redis 超时
	if redis.Client == nil || !redis.IsConnected() {
		return nil, false
	}
	v, err := redis.Client.Get(tenantCacheKey(schema))
	if err != nil || v == "" { // 未命中或读取失败 → 回源
		return nil, false
	}
	if v == "null" { // 负缓存：未注册
		return nil, true
	}
	var t Tenant
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, false
	}
	return &t, true
}

func storeCache(schema string, t *Tenant, d time.Duration) {
	if redis.Client == nil || !redis.IsConnected() {
		return
	}
	b, err := json.Marshal(t) // nil → "null"（负缓存）
	if err != nil {
		return
	}
	_ = redis.Client.Set(tenantCacheKey(schema), b, d)
}

// Invalidate 主动失效缓存：平台侧开通/停用/删除租户、改状态后调用。
// 空 schema 按无操作处理（全量刷新交由 TTL 兜底；现有调用方均传具体 schema）。
func Invalidate(schema string) {
	key := strings.TrimSpace(schema)
	if key == "" || redis.Client == nil {
		return
	}
	_ = redis.Client.Del(tenantCacheKey(key))
}
