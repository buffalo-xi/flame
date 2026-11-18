package gw

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisSticky 多实例 gateway 的共享粘性存储（redis）。
//
// 抢占用 SET NX EX（原子：并发下只有一个 gateway 能首次绑定成功）；
// 反向索引 set（backend → sessions）供 RemoveBackend 快速定位要删的绑定。
// 键命名：sandboxg:session:<id> 存后端 key，sandboxg:backend:<key> 存 session 集合。
type RedisSticky struct {
	client redis.UniversalClient
}

// NewRedisSticky 构造 redis 粘性存储。
func NewRedisSticky(client redis.UniversalClient) *RedisSticky {
	return &RedisSticky{client: client}
}

func (s *RedisSticky) sessionKey(sid string) string { return "sandboxg:session:" + sid }
func (s *RedisSticky) backendKey(key string) string { return "sandboxg:backend:" + key }

// unbindScript 条件删除：只有当前值仍指向这条绑定所属的后端才删。
// 裸 DEL 会在「解绑信号到达时 nexus 已经用同一 session 重新 open」的竞态下误删新绑定。
var unbindScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

func (s *RedisSticky) Lookup(ctx context.Context, sessionID string, ttl time.Duration) (string, bool, error) {
	val, err := s.client.Get(ctx, s.sessionKey(sessionID)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_ = s.client.Expire(ctx, s.sessionKey(sessionID), ttl).Err() // 续期失败不致命
	return val, true, nil
}

func (s *RedisSticky) Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (bool, error) {
	grabbed, err := s.client.SetNX(ctx, s.sessionKey(sessionID), backendKey, ttl).Result()
	if err != nil {
		return false, err
	}
	// 反向索引：抢到与否都 SADD（幂等），供 RemoveBackend 定位。
	//
	// 同时续一个比绑定更长的过期时间：集合没有成员级 TTL，不续的话 session 键过期后
	// 成员会永远留着（只增不减），直到整个后端被摘除才清。
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, s.backendKey(backendKey), sessionID)
	pipe.Expire(ctx, s.backendKey(backendKey), 2*ttl)
	_, _ = pipe.Exec(ctx)
	return grabbed, nil
}

// Unbind 条件删除绑定，并从反向索引里摘掉这个 session。
func (s *RedisSticky) Unbind(ctx context.Context, sessionID, backendKey string) error {
	if err := unbindScript.Run(ctx, s.client, []string{s.sessionKey(sessionID)}, backendKey).Err(); err != nil {
		return err
	}
	// 反向索引同步摘掉，免得 RemoveBackend 再为它做一次无意义的 DEL。
	_ = s.client.SRem(ctx, s.backendKey(backendKey), sessionID).Err()
	return nil
}

func (s *RedisSticky) RemoveBackend(ctx context.Context, backendKey string) error {
	ids, err := s.client.SMembers(ctx, s.backendKey(backendKey)).Result()
	if err != nil {
		return err
	}
	for _, id := range ids {
		_ = s.client.Del(ctx, s.sessionKey(id)).Err()
	}
	_ = s.client.Del(ctx, s.backendKey(backendKey)).Err()
	return nil
}
