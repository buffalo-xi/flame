package quota

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"flame/nexus/shared/db"
	"flame/nexus/shared/redis"
)

// Usage 一个工作场地当前占用的字节数与"上次精确扫描"的时间。
//
// VerifiedAt 超过 CalibrateAfter 就说明这个数字可能被旁路写入（沙箱里的 exec / shell）
// 弄脏了，下次写入时要重新扫一次。
type Usage struct {
	Bytes      int64
	VerifiedAt time.Time
}

const (
	// CalibrateAfter 计数「保鲜期」：超过这个时间，下一次写入会触发一次精确扫描。
	// 这是唯一覆盖「exec 直写 nexus 看不到」的手段 —— 只在有写入时触发，不设后台任务。
	CalibrateAfter = 10 * time.Minute
	// NearLimitRatio 用量超过上限的这个比例时，把扫描间隔收紧到 NearLimitInterval。
	NearLimitRatio = 0.9
	// NearLimitInterval 接近上限时的最小扫描间隔，避免连续写入每次都扫。
	NearLimitInterval = 60 * time.Second

	usageTTL = 7 * 24 * time.Hour
)

// Store 计数后端：有 Redis 时用 Redis，否则落 PG（租户 schema 内的 session_usage 表）。
//
// scopeKey 是工作场地的存储相对路径（如 tenants/t0/users/u1/sessions/s1）：
// 用它而不是 (uid, sid) 是为了让 session 与将来的 run 场地共用同一套计数，
// 调用方也不必为了记账再去查一次会话归属。
type Store interface {
	Get(ctx context.Context, schema, scopeKey string) (Usage, bool, error)
	Add(ctx context.Context, schema, scopeKey string, delta int64) error
	Set(ctx context.Context, schema, scopeKey string, u Usage) error
	Reset(ctx context.Context, schema, scopeKey string) error
}

var store Store

// GetStore 返回计数后端。首次调用时决定，运行期不再切换 ——
// Redis 抖动时来回换后端会让两个源各自计数，反而制造漂移。
func GetStore() Store {
	if store != nil {
		return store
	}
	if redis.Client != nil && redis.IsConnected() {
		store = &redisStore{}
	} else {
		store = &pgStore{}
	}
	return store
}

// ---------------------------------------------------------------------------
// Redis 后端
// ---------------------------------------------------------------------------

type redisStore struct{}

func usageKey(scopeKey string) string {
	return "quota:usage:" + scopeKey
}

func (redisStore) Get(_ context.Context, _ string, scopeKey string) (Usage, bool, error) {
	if redis.Client == nil || !redis.IsConnected() {
		return Usage{}, false, nil
	}
	m, err := redis.Client.HGetAll(usageKey(scopeKey))
	if err != nil {
		return Usage{}, false, nil // Redis 抖动按「没有计数」处理，交给扫描回填
	}
	if len(m) == 0 {
		return Usage{}, false, nil
	}
	u := Usage{}
	u.Bytes, _ = strconv.ParseInt(m["bytes"], 10, 64)
	if sec, err := strconv.ParseInt(m["verified_at"], 10, 64); err == nil && sec > 0 {
		u.VerifiedAt = time.Unix(sec, 0)
	}
	return u, true, nil
}

func (redisStore) Add(_ context.Context, _ string, scopeKey string, delta int64) error {
	if redis.Client == nil || !redis.IsConnected() {
		return fmt.Errorf("redis unavailable")
	}
	key := usageKey(scopeKey)
	if _, err := redis.Client.HIncrBy(key, "bytes", delta); err != nil {
		return err
	}
	return redis.Client.Expire(key, usageTTL)
}

func (redisStore) Set(_ context.Context, _ string, scopeKey string, u Usage) error {
	if redis.Client == nil || !redis.IsConnected() {
		return fmt.Errorf("redis unavailable")
	}
	key := usageKey(scopeKey)
	err := redis.Client.HSetMultiple(key, map[string]interface{}{
		"bytes":       u.Bytes,
		"verified_at": u.VerifiedAt.Unix(),
	})
	if err != nil {
		return err
	}
	return redis.Client.Expire(key, usageTTL)
}

func (redisStore) Reset(_ context.Context, _ string, scopeKey string) error {
	if redis.Client == nil {
		return nil
	}
	return redis.Client.Del(usageKey(scopeKey))
}

// ---------------------------------------------------------------------------
// PostgreSQL 后端（没有 Redis 的部署）
//
// 表在租户 schema 内：
//   {schema}.session_usage(scope_key text PRIMARY KEY, used_bytes bigint, verified_at timestamptz)
// 建表语句见 platform/sql/quota_storage.sql。
// 单会话并发通常是 1（agent 串行跑工具），热点行锁不是问题。
// ---------------------------------------------------------------------------

type pgStore struct{}

func (pgStore) Get(_ context.Context, schema, scopeKey string) (Usage, bool, error) {
	var u Usage
	var verified time.Time
	err := db.DB.QueryRow(
		`SELECT used_bytes, verified_at FROM `+schema+`.session_usage WHERE scope_key = $1`, scopeKey,
	).Scan(&u.Bytes, &verified)
	if err == sql.ErrNoRows {
		return Usage{}, false, nil
	}
	if err != nil {
		return Usage{}, false, err
	}
	u.VerifiedAt = verified
	return u, true, nil
}

func (pgStore) Add(_ context.Context, schema, scopeKey string, delta int64) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.session_usage (scope_key, used_bytes, verified_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (scope_key) DO UPDATE
		    SET used_bytes = session_usage.used_bytes + EXCLUDED.used_bytes,
		        verified_at = now()`,
		scopeKey, delta)
	return err
}

func (pgStore) Set(_ context.Context, schema, scopeKey string, u Usage) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+schema+`.session_usage (scope_key, used_bytes, verified_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (scope_key) DO UPDATE
		    SET used_bytes = EXCLUDED.used_bytes, verified_at = EXCLUDED.verified_at`,
		scopeKey, u.Bytes, u.VerifiedAt)
	return err
}

func (pgStore) Reset(_ context.Context, schema, scopeKey string) error {
	_, err := db.DB.Exec(`DELETE FROM `+schema+`.session_usage WHERE scope_key = $1`, scopeKey)
	return err
}
