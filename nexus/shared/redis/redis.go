package redis

import (
	"sync/atomic"
	"time"

	"flame/tools/xredis"
)

// DefaultCacheTTL 配置类元数据缓存（quota / flowconfig 等）的兜底有效期。
// 正常依赖各模块的「写后删」（Invalidate / InvalidateAll）；TTL 只是兜底：
// 绕过接口直接改库、或多实例下漏删时，最多延迟一个 TTL 周期生效。
// 若某模块对正/负缓存有各自的时效要求（如 tenants 的 2min / 30s），应自行定义，
// 不要复用本常量。
const DefaultCacheTTL = 5 * time.Minute

// redisConnected Redis 连接状态（atomic，避免并发读写竞争）
var redisConnected atomic.Bool

// IsConnected 返回 Redis 连接状态
func IsConnected() bool { return redisConnected.Load() }

// Client 全局Redis客户端
var Client xredis.Client

func CheckRedisConnection() bool {
	if Client == nil {
		return false
	}
	err := Client.Ping()
	redisConnected.Store(err == nil)
	return err == nil
}
