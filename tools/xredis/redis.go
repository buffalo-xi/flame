package xredis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisMode is the Redis deployment mode.
type RedisMode string

const (
	// ModeSingle is the single-node mode.
	ModeSingle RedisMode = "single"
	// ModeCluster is the cluster mode.
	ModeCluster RedisMode = "cluster"
)

// Config is the Redis configuration.
type Config struct {
	Mode     RedisMode     `json:"mode" yaml:"mode"`           // mode: single or cluster
	Addrs    []string      `json:"addrs" yaml:"addrs"`         // address list; single mode only uses the first
	Password string        `json:"password" yaml:"password"`   // password
	DB       int           `json:"db" yaml:"db"`               // database index; single mode only
	PoolSize int           `json:"pool_size" yaml:"pool_size"` // connection pool size
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`     // timeout
	Prefix   string        `json:"prefix" yaml:"prefix"`       // key prefix
}

// Client is the Redis client interface.
type Client interface {
	// Get returns the value of key (default context).
	Get(key string) (string, error)
	// GetContext returns the value of key (given context).
	GetContext(ctx context.Context, key string) (string, error)
	// Set sets the value of key with an expiration (default context).
	Set(key string, value interface{}, expiration time.Duration) error
	// SetContext sets the value of key with an expiration (given context).
	SetContext(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	// Del deletes keys (default context).
	Del(keys ...string) error
	// DelContext deletes keys (given context).
	DelContext(ctx context.Context, keys ...string) error
	// Exists reports whether keys exist (default context).
	Exists(keys ...string) (bool, error)
	// ExistsContext reports whether keys exist (given context).
	ExistsContext(ctx context.Context, keys ...string) (bool, error)
	// Expire sets the expiration of key (default context).
	Expire(key string, expiration time.Duration) error
	// ExpireContext sets the expiration of key (given context).
	ExpireContext(ctx context.Context, key string, expiration time.Duration) error
	// TTL returns the remaining TTL of key (default context).
	TTL(key string) (time.Duration, error)
	// TTLContext returns the remaining TTL of key (given context).
	TTLContext(ctx context.Context, key string) (time.Duration, error)
	// HSet sets a hash field value (default context).
	HSet(key, field string, value interface{}) error
	// HSetContext sets a hash field value (given context).
	HSetContext(ctx context.Context, key, field string, value interface{}) error
	// HGet returns a hash field value (default context).
	HGet(key, field string) (string, error)
	// HGetContext returns a hash field value (given context).
	HGetContext(ctx context.Context, key, field string) (string, error)
	// HGetAll returns every hash field and value (default context).
	HGetAll(key string) (map[string]string, error)
	// HGetAllContext returns every hash field and value (given context).
	HGetAllContext(ctx context.Context, key string) (map[string]string, error)
	// HDel deletes hash fields (default context).
	HDel(key string, fields ...string) error
	// HDelContext deletes hash fields (given context).
	HDelContext(ctx context.Context, key string, fields ...string) error
	// HExists reports whether a hash field exists (default context).
	HExists(key, field string) (bool, error)
	// HExistsContext reports whether a hash field exists (given context).
	HExistsContext(ctx context.Context, key, field string) (bool, error)
	// HIncrBy increments the integer value of a hash field (default context).
	HIncrBy(key, field string, value int64) (int64, error)
	// HIncrByContext increments the integer value of a hash field (given context).
	HIncrByContext(ctx context.Context, key, field string, value int64) (int64, error)
	// HKeys returns every hash field name (default context).
	HKeys(key string) ([]string, error)
	// HKeysContext returns every hash field name (given context).
	HKeysContext(ctx context.Context, key string) ([]string, error)
	// HVals returns every hash field value (default context).
	HVals(key string) ([]string, error)
	// HValsContext returns every hash field value (given context).
	HValsContext(ctx context.Context, key string) ([]string, error)
	// HLen returns the number of hash fields (default context).
	HLen(key string) (int64, error)
	// HLenContext returns the number of hash fields (given context).
	HLenContext(ctx context.Context, key string) (int64, error)
	// HSetMultiple sets several hash fields in one call (default context).
	HSetMultiple(key string, values map[string]interface{}) error
	// HSetMultipleContext sets several hash fields in one call (given context).
	HSetMultipleContext(ctx context.Context, key string, values map[string]interface{}) error
	// Ping reports whether Redis is serving (default context).
	Ping() error
	// PingContext reports whether Redis is serving (given context).
	PingContext(ctx context.Context) error
	// Publish publishes a payload to a channel (default context).
	Publish(channel string, payload []byte) error
	// PublishContext publishes a payload to a channel (given context).
	PublishContext(ctx context.Context, channel string, payload []byte) error
	// SubscribePattern subscribes to channels by pattern (e.g. "run:stop:*") and calls
	// handler for each message; the returned function stops the subscription. go-redis
	// reconnects on its own.
	SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error)
	// Close closes the client.
	Close() error
}

// redisClient is the Redis client implementation.
type redisClient struct {
	client redis.UniversalClient
	prefix string
}

// NewClient creates a Redis client.
func NewClient(config *Config) (Client, error) {
	if config == nil {
		return nil, fmt.Errorf("redis config is nil")
	}

	if len(config.Addrs) == 0 {
		return nil, fmt.Errorf("redis addrs is empty")
	}

	// Apply the defaults.
	if config.PoolSize <= 0 {
		config.PoolSize = 10
	}

	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}

	var client redis.UniversalClient

	// Build the client that matches the mode.
	switch config.Mode {
	case ModeSingle:
		// Single-node mode.
		client = redis.NewClient(&redis.Options{
			Addr:         config.Addrs[0],
			Password:     config.Password,
			DB:           config.DB,
			PoolSize:     config.PoolSize,
			DialTimeout:  config.Timeout,
			ReadTimeout:  config.Timeout,
			WriteTimeout: config.Timeout,
			// -1 disables automatic retries, so Timeout is a hard ceiling instead of being
			// multiplied by the retry count.
			MaxRetries: -1,
		})
	case ModeCluster:
		// Cluster mode.
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        config.Addrs,
			Password:     config.Password,
			PoolSize:     config.PoolSize,
			DialTimeout:  config.Timeout,
			ReadTimeout:  config.Timeout,
			WriteTimeout: config.Timeout,
			// -1 disables automatic retries, so Timeout is a hard ceiling instead of being
			// multiplied by the retry count.
			MaxRetries: -1,
		})
	default:
		return nil, fmt.Errorf("unsupported redis mode: %s", config.Mode)
	}

	// Check the connection.
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &redisClient{
		client: client,
		prefix: config.Prefix,
	}, nil
}

// addPrefix adds the key prefix. It normalises a trailing colon away from the
// configured prefix, so a prefix that already ends with ":" does not produce a
// double colon ("nexus:" + ":" + key = "nexus::key") and an empty namespace
// level in the key tree.
func (c *redisClient) addPrefix(key string) string {
	if c.prefix == "" {
		return key
	}
	return strings.TrimRight(c.prefix, ":") + ":" + key
}

// addPrefixes adds the prefix to several keys.
func (c *redisClient) addPrefixes(keys ...string) []string {
	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = c.addPrefix(key)
	}
	return prefixedKeys
}

// Get returns the value of key (default context).
func (c *redisClient) Get(key string) (string, error) {
	return c.GetContext(context.Background(), key)
}

// GetContext returns the value of key (given context).
func (c *redisClient) GetContext(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, c.addPrefix(key)).Result()
	if err == redis.Nil {
		return "", nil // a missing key means an empty string
	} else if err != nil {
		return "", err
	}
	return val, nil
}

// Set sets the value of key with an expiration (default context).
func (c *redisClient) Set(key string, value interface{}, expiration time.Duration) error {
	return c.SetContext(context.Background(), key, value, expiration)
}

// SetContext sets the value of key with an expiration (given context).
func (c *redisClient) SetContext(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return c.client.Set(ctx, c.addPrefix(key), value, expiration).Err()
}

// Del deletes keys (default context).
func (c *redisClient) Del(keys ...string) error {
	return c.DelContext(context.Background(), keys...)
}

// DelContext deletes keys (given context).
func (c *redisClient) DelContext(ctx context.Context, keys ...string) error {
	return c.client.Del(ctx, c.addPrefixes(keys...)...).Err()
}

// Exists reports whether keys exist (default context).
func (c *redisClient) Exists(keys ...string) (bool, error) {
	return c.ExistsContext(context.Background(), keys...)
}

// ExistsContext reports whether keys exist (given context).
func (c *redisClient) ExistsContext(ctx context.Context, keys ...string) (bool, error) {
	result, err := c.client.Exists(ctx, c.addPrefixes(keys...)...).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

// Expire sets the expiration of key (default context).
func (c *redisClient) Expire(key string, expiration time.Duration) error {
	return c.ExpireContext(context.Background(), key, expiration)
}

// ExpireContext sets the expiration of key (given context).
func (c *redisClient) ExpireContext(ctx context.Context, key string, expiration time.Duration) error {
	return c.client.Expire(ctx, c.addPrefix(key), expiration).Err()
}

// TTL returns the remaining TTL of key (default context).
func (c *redisClient) TTL(key string) (time.Duration, error) {
	return c.TTLContext(context.Background(), key)
}

// TTLContext returns the remaining TTL of key (given context).
func (c *redisClient) TTLContext(ctx context.Context, key string) (time.Duration, error) {
	return c.client.TTL(ctx, c.addPrefix(key)).Result()
}

// Ping reports whether Redis is serving (default context).
func (c *redisClient) Ping() error {
	return c.PingContext(context.Background())
}

// PingContext reports whether Redis is serving (given context).
func (c *redisClient) PingContext(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// HSet sets a hash field value (default context).
func (c *redisClient) HSet(key, field string, value interface{}) error {
	return c.HSetContext(context.Background(), key, field, value)
}

// HSetContext sets a hash field value (given context).
func (c *redisClient) HSetContext(ctx context.Context, key, field string, value interface{}) error {
	return c.client.HSet(ctx, c.addPrefix(key), field, value).Err()
}

// HGet returns a hash field value (default context).
func (c *redisClient) HGet(key, field string) (string, error) {
	return c.HGetContext(context.Background(), key, field)
}

// HGetContext returns a hash field value (given context).
func (c *redisClient) HGetContext(ctx context.Context, key, field string) (string, error) {
	val, err := c.client.HGet(ctx, c.addPrefix(key), field).Result()
	if err == redis.Nil {
		return "", nil // a missing field means an empty string
	} else if err != nil {
		return "", err
	}
	return val, nil
}

// HGetAll returns every hash field and value (default context).
func (c *redisClient) HGetAll(key string) (map[string]string, error) {
	return c.HGetAllContext(context.Background(), key)
}

// HGetAllContext returns every hash field and value (given context).
func (c *redisClient) HGetAllContext(ctx context.Context, key string) (map[string]string, error) {
	return c.client.HGetAll(ctx, c.addPrefix(key)).Result()
}

// HDel deletes hash fields (default context).
func (c *redisClient) HDel(key string, fields ...string) error {
	return c.HDelContext(context.Background(), key, fields...)
}

// HDelContext deletes hash fields (given context).
func (c *redisClient) HDelContext(ctx context.Context, key string, fields ...string) error {
	return c.client.HDel(ctx, c.addPrefix(key), fields...).Err()
}

// HExists reports whether a hash field exists (default context).
func (c *redisClient) HExists(key, field string) (bool, error) {
	return c.HExistsContext(context.Background(), key, field)
}

// HExistsContext reports whether a hash field exists (given context).
func (c *redisClient) HExistsContext(ctx context.Context, key, field string) (bool, error) {
	return c.client.HExists(ctx, c.addPrefix(key), field).Result()
}

// HIncrBy increments the integer value of a hash field (default context).
func (c *redisClient) HIncrBy(key, field string, value int64) (int64, error) {
	return c.HIncrByContext(context.Background(), key, field, value)
}

// HIncrByContext increments the integer value of a hash field (given context).
func (c *redisClient) HIncrByContext(ctx context.Context, key, field string, value int64) (int64, error) {
	return c.client.HIncrBy(ctx, c.addPrefix(key), field, value).Result()
}

// HKeys returns every hash field name (default context).
func (c *redisClient) HKeys(key string) ([]string, error) {
	return c.HKeysContext(context.Background(), key)
}

// HKeysContext returns every hash field name (given context).
func (c *redisClient) HKeysContext(ctx context.Context, key string) ([]string, error) {
	return c.client.HKeys(ctx, c.addPrefix(key)).Result()
}

// HVals returns every hash field value (default context).
func (c *redisClient) HVals(key string) ([]string, error) {
	return c.HValsContext(context.Background(), key)
}

// HValsContext returns every hash field value (given context).
func (c *redisClient) HValsContext(ctx context.Context, key string) ([]string, error) {
	return c.client.HVals(ctx, c.addPrefix(key)).Result()
}

// HLen returns the number of hash fields (default context).
func (c *redisClient) HLen(key string) (int64, error) {
	return c.HLenContext(context.Background(), key)
}

// HLenContext returns the number of hash fields (given context).
func (c *redisClient) HLenContext(ctx context.Context, key string) (int64, error) {
	return c.client.HLen(ctx, c.addPrefix(key)).Result()
}

// HSetMultiple sets several hash fields in one call (default context).
func (c *redisClient) HSetMultiple(key string, values map[string]interface{}) error {
	return c.HSetMultipleContext(context.Background(), key, values)
}

// HSetMultipleContext sets several hash fields in one call (given context).
func (c *redisClient) HSetMultipleContext(ctx context.Context, key string, values map[string]interface{}) error {
	return c.client.HSet(ctx, c.addPrefix(key), values).Err()
}

// Publish publishes a payload to a channel (default context).
func (c *redisClient) Publish(channel string, payload []byte) error {
	return c.PublishContext(context.Background(), channel, payload)
}

// PublishContext publishes a payload to a channel (given context).
func (c *redisClient) PublishContext(ctx context.Context, channel string, payload []byte) error {
	return c.client.Publish(ctx, c.addPrefix(channel), payload).Err()
}

// SubscribePattern subscribes to channels by pattern (PSUBSCRIBE) and calls
// handler for each message; it returns a function that stops the subscription.
//
// Pattern subscription instead of enumerating channels: the caller usually does
// not know the names in advance (channels split by tenant, for example). The
// callback runs serially on its own goroutine, and the channel name is stripped
// of the configured prefix before it reaches the handler. go-redis reconnects on
// its own, so the caller does not have to retry.
func (c *redisClient) SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error) {
	if len(patterns) == 0 {
		return func() error { return nil }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	pubsub := c.client.PSubscribe(ctx, c.addPrefixes(patterns...)...)

	// Wait for the subscription to be confirmed: without it the caller would believe
	// it is already listening.
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, err
	}

	go func() {
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				handler(c.stripPrefix(msg.Channel), []byte(msg.Payload))
			}
		}
	}()

	var once sync.Once
	return func() error {
		var err error
		once.Do(func() {
			cancel()
			err = pubsub.Close()
		})
		return err
	}, nil
}

// Close closes the client.
func (c *redisClient) Close() error {
	return c.client.Close()
}

// stripPrefix removes the key prefix (the inverse of addPrefix). It only
// restores business names from channel names.
func (c *redisClient) stripPrefix(key string) string {
	p := strings.TrimSuffix(c.prefix, ":")
	if p == "" {
		return key
	}
	return strings.TrimPrefix(key, p+":")
}
