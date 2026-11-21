package gw

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisSticky is the shared sticky store of a multi-instance gateway (redis).
//
// A grab uses SET NX EX (atomic: under concurrency only one gateway wins the first bind); a reverse index set
// (instance → sessions) lets RemoveBackend locate the bindings to delete quickly.
// Key naming: browserg:session:<id> holds the instance key, browserg:backend:<key> holds the session set.
//
// The prefix is separate from sandboxg's on purpose: the same session_id is used for the shell channel and the browser
// channel, and the two must land on different pools.
type RedisSticky struct {
	client redis.UniversalClient
}

// NewRedisSticky creates a redis sticky store.
func NewRedisSticky(client redis.UniversalClient) *RedisSticky {
	return &RedisSticky{client: client}
}

func (s *RedisSticky) sessionKey(sid string) string { return "browserg:session:" + sid }
func (s *RedisSticky) backendKey(key string) string { return "browserg:backend:" + key }

// unbindScript is a conditional delete: it only deletes while the current value still points at the instance this binding
// belongs to. A bare DEL would wrongly delete the new binding in the race where "nexus already re-opened with the same
// session by the time the unbind signal arrives".
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
	_ = s.client.Expire(ctx, s.sessionKey(sessionID), ttl).Err() // a failed renewal is not fatal
	return val, true, nil
}

func (s *RedisSticky) Bind(ctx context.Context, sessionID, backendKey string, ttl time.Duration) (bool, error) {
	grabbed, err := s.client.SetNX(ctx, s.sessionKey(sessionID), backendKey, ttl).Result()
	if err != nil {
		return false, err
	}
	// Reverse index: SADD whether or not the grab succeeded (it is idempotent), so RemoveBackend can locate the bindings.
	// A longer expiry than the binding is renewed at the same time: a set has no member-level TTL, so without the renewal
	// the members would stay forever once the session keys expire.
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, s.backendKey(backendKey), sessionID)
	pipe.Expire(ctx, s.backendKey(backendKey), 2*ttl)
	_, _ = pipe.Exec(ctx)
	return grabbed, nil
}

// Unbind conditionally deletes the binding and drops the session from the reverse index.
func (s *RedisSticky) Unbind(ctx context.Context, sessionID, backendKey string) error {
	if err := unbindScript.Run(ctx, s.client, []string{s.sessionKey(sessionID)}, backendKey).Err(); err != nil {
		return err
	}
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
