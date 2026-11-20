package redis

import (
	"sync/atomic"
	"time"

	"flame/tools/xredis"
)

// DefaultCacheTTL is the fallback lifetime of configuration-style metadata caches (quota / flowconfig and so on).
// The normal mechanism is each module's "delete on write" (Invalidate / InvalidateAll); the TTL is only a backstop — when
// the database is edited around the API, or a delete is missed with several instances, the change takes effect at most
// one TTL later.
// A module with its own expiry needs (such as tenants' 2min / 30s for positive and negative results) must define its own
// constant and not reuse this one.
const DefaultCacheTTL = 5 * time.Minute

// redisConnected is the Redis connection state (atomic, so concurrent readers and writers do not race).
var redisConnected atomic.Bool

// IsConnected returns the Redis connection state.
func IsConnected() bool { return redisConnected.Load() }

// Client is the global Redis client.
var Client xredis.Client

// CheckRedisConnection pings Redis and records the result.
func CheckRedisConnection() bool {
	if Client == nil {
		return false
	}
	err := Client.Ping()
	redisConnected.Store(err == nil)
	return err == nil
}
