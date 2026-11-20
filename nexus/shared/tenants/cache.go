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

// loadCache returns (tenant, true) on a hit; a nil tenant means a negative cache entry ("not registered").
// A miss, or Redis being unusable, returns (nil, false) and the caller goes back to the DB.
func loadCache(schema string) (*Tenant, bool) {
	// Not enabled / not created, or the last Ping judged it unavailable -> go straight to the DB instead of waiting for a
	// Redis timeout
	if redis.Client == nil || !redis.IsConnected() {
		return nil, false
	}
	v, err := redis.Client.Get(tenantCacheKey(schema))
	if err != nil || v == "" { // miss or read failure -> go back to the DB
		return nil, false
	}
	if v == "null" { // negative cache: not registered
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
	b, err := json.Marshal(t) // nil -> "null" (negative cache)
	if err != nil {
		return
	}
	_ = redis.Client.Set(tenantCacheKey(schema), b, d)
}

// Invalidate actively invalidates the cache: the platform side calls it after provisioning / disabling / deleting a
// tenant or changing its state.
// An empty schema is a no-op (a full flush is left to the TTL; every current caller passes a concrete schema).
func Invalidate(schema string) {
	key := strings.TrimSpace(schema)
	if key == "" || redis.Client == nil {
		return
	}
	_ = redis.Client.Del(tenantCacheKey(key))
}
