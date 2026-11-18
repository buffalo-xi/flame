package xredis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisMode Redis模式
type RedisMode string

const (
	// ModeSingle 单机模式
	ModeSingle RedisMode = "single"
	// ModeCluster 集群模式
	ModeCluster RedisMode = "cluster"
)

// Config Redis配置
type Config struct {
	Mode     RedisMode     `json:"mode" yaml:"mode"`           // 模式：single 或 cluster
	Addrs    []string      `json:"addrs" yaml:"addrs"`         // 地址列表，单机模式只需要第一个
	Password string        `json:"password" yaml:"password"`   // 密码
	DB       int           `json:"db" yaml:"db"`               // 数据库索引，只在单机模式有效
	PoolSize int           `json:"pool_size" yaml:"pool_size"` // 连接池大小
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`     // 超时时间
	Prefix   string        `json:"prefix" yaml:"prefix"`       // 键前缀
}

// Client Redis客户端接口
type Client interface {
	// Get 获取键值（使用默认context）
	Get(key string) (string, error)
	// GetContext 获取键值（使用指定context）
	GetContext(ctx context.Context, key string) (string, error)
	// Set 设置键值，带过期时间（使用默认context）
	Set(key string, value interface{}, expiration time.Duration) error
	// SetContext 设置键值，带过期时间（使用指定context）
	SetContext(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	// Del 删除键（使用默认context）
	Del(keys ...string) error
	// DelContext 删除键（使用指定context）
	DelContext(ctx context.Context, keys ...string) error
	// Exists 检查键是否存在（使用默认context）
	Exists(keys ...string) (bool, error)
	// ExistsContext 检查键是否存在（使用指定context）
	ExistsContext(ctx context.Context, keys ...string) (bool, error)
	// Expire 设置键的过期时间（使用默认context）
	Expire(key string, expiration time.Duration) error
	// ExpireContext 设置键的过期时间（使用指定context）
	ExpireContext(ctx context.Context, key string, expiration time.Duration) error
	// TTL 获取键的剩余过期时间（使用默认context）
	TTL(key string) (time.Duration, error)
	// TTLContext 获取键的剩余过期时间（使用指定context）
	TTLContext(ctx context.Context, key string) (time.Duration, error)
	// HSet 设置hash字段值（使用默认context）
	HSet(key, field string, value interface{}) error
	// HSetContext 设置hash字段值（使用指定context）
	HSetContext(ctx context.Context, key, field string, value interface{}) error
	// HGet 获取hash字段值（使用默认context）
	HGet(key, field string) (string, error)
	// HGetContext 获取hash字段值（使用指定context）
	HGetContext(ctx context.Context, key, field string) (string, error)
	// HGetAll 获取hash所有字段和值（使用默认context）
	HGetAll(key string) (map[string]string, error)
	// HGetAllContext 获取hash所有字段和值（使用指定context）
	HGetAllContext(ctx context.Context, key string) (map[string]string, error)
	// HDel 删除hash字段（使用默认context）
	HDel(key string, fields ...string) error
	// HDelContext 删除hash字段（使用指定context）
	HDelContext(ctx context.Context, key string, fields ...string) error
	// HExists 检查hash字段是否存在（使用默认context）
	HExists(key, field string) (bool, error)
	// HExistsContext 检查hash字段是否存在（使用指定context）
	HExistsContext(ctx context.Context, key, field string) (bool, error)
	// HIncrBy 增加hash字段的整数值（使用默认context）
	HIncrBy(key, field string, value int64) (int64, error)
	// HIncrByContext 增加hash字段的整数值（使用指定context）
	HIncrByContext(ctx context.Context, key, field string, value int64) (int64, error)
	// HKeys 获取hash所有字段名（使用默认context）
	HKeys(key string) ([]string, error)
	// HKeysContext 获取hash所有字段名（使用指定context）
	HKeysContext(ctx context.Context, key string) ([]string, error)
	// HVals 获取hash所有字段值（使用默认context）
	HVals(key string) ([]string, error)
	// HValsContext 获取hash所有字段值（使用指定context）
	HValsContext(ctx context.Context, key string) ([]string, error)
	// HLen 获取hash字段数量（使用默认context）
	HLen(key string) (int64, error)
	// HLenContext 获取hash字段数量（使用指定context）
	HLenContext(ctx context.Context, key string) (int64, error)
	// HSetMultiple 一次性设置多个hash字段值（使用默认context）
	HSetMultiple(key string, values map[string]interface{}) error
	// HSetMultipleContext 一次性设置多个hash字段值（使用指定context）
	HSetMultipleContext(ctx context.Context, key string, values map[string]interface{}) error
	// Ping 检查Redis是否正常服务（使用默认context）
	Ping() error
	// PingContext 检查Redis是否正常服务（使用指定context）
	PingContext(ctx context.Context) error
	// Publish 发布消息到频道（使用默认context）
	Publish(channel string, payload []byte) error
	// PublishContext 发布消息到频道（使用指定context）
	PublishContext(ctx context.Context, channel string, payload []byte) error
	// SubscribePattern 按模式订阅频道（如 "run:stop:*"），每条消息回调 handler；
	// 返回的函数用于停止订阅。连接断开由 go-redis 自动重连。
	SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error)
	// Close 关闭客户端
	Close() error
}

// redisClient Redis客户端实现
type redisClient struct {
	client redis.UniversalClient
	prefix string
}

// NewClient 创建Redis客户端
func NewClient(config *Config) (Client, error) {
	if config == nil {
		return nil, fmt.Errorf("redis config is nil")
	}

	if len(config.Addrs) == 0 {
		return nil, fmt.Errorf("redis addrs is empty")
	}

	// 设置默认值
	if config.PoolSize <= 0 {
		config.PoolSize = 10
	}

	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}

	var client redis.UniversalClient

	// 根据模式创建不同的客户端
	switch config.Mode {
	case ModeSingle:
		// 单机模式
		client = redis.NewClient(&redis.Options{
			Addr:         config.Addrs[0],
			Password:     config.Password,
			DB:           config.DB,
			PoolSize:     config.PoolSize,
			DialTimeout:  config.Timeout,
			ReadTimeout:  config.Timeout,
			WriteTimeout: config.Timeout,
			// -1 关闭自动重试：让 Timeout 严格封顶，避免「超时 × 重试次数」放大单次等待。
			MaxRetries: -1,
		})
	case ModeCluster:
		// 集群模式
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        config.Addrs,
			Password:     config.Password,
			PoolSize:     config.PoolSize,
			DialTimeout:  config.Timeout,
			ReadTimeout:  config.Timeout,
			WriteTimeout: config.Timeout,
			// -1 关闭自动重试：让 Timeout 严格封顶，避免「超时 × 重试次数」放大单次等待。
			MaxRetries: -1,
		})
	default:
		return nil, fmt.Errorf("unsupported redis mode: %s", config.Mode)
	}

	// 测试连接
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

// addPrefix 添加键前缀。归一化：去掉配置前缀末尾的冒号，避免 prefix 已经
// 带 ":" 时拼出双冒号（如 "nexus:" + ":" + key = "nexus::key"），进而在键树里
// 出现无意义的空命名空间层级。
func (c *redisClient) addPrefix(key string) string {
	if c.prefix == "" {
		return key
	}
	return strings.TrimRight(c.prefix, ":") + ":" + key
}

// addPrefixes 为多个键添加前缀
func (c *redisClient) addPrefixes(keys ...string) []string {
	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = c.addPrefix(key)
	}
	return prefixedKeys
}

// Get 获取键值（使用默认context）
func (c *redisClient) Get(key string) (string, error) {
	return c.GetContext(context.Background(), key)
}

// GetContext 获取键值（使用指定context）
func (c *redisClient) GetContext(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, c.addPrefix(key)).Result()
	if err == redis.Nil {
		return "", nil // 键不存在，返回空字符串
	} else if err != nil {
		return "", err
	}
	return val, nil
}

// Set 设置键值，带过期时间（使用默认context）
func (c *redisClient) Set(key string, value interface{}, expiration time.Duration) error {
	return c.SetContext(context.Background(), key, value, expiration)
}

// SetContext 设置键值，带过期时间（使用指定context）
func (c *redisClient) SetContext(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return c.client.Set(ctx, c.addPrefix(key), value, expiration).Err()
}

// Del 删除键（使用默认context）
func (c *redisClient) Del(keys ...string) error {
	return c.DelContext(context.Background(), keys...)
}

// DelContext 删除键（使用指定context）
func (c *redisClient) DelContext(ctx context.Context, keys ...string) error {
	return c.client.Del(ctx, c.addPrefixes(keys...)...).Err()
}

// Exists 检查键是否存在（使用默认context）
func (c *redisClient) Exists(keys ...string) (bool, error) {
	return c.ExistsContext(context.Background(), keys...)
}

// ExistsContext 检查键是否存在（使用指定context）
func (c *redisClient) ExistsContext(ctx context.Context, keys ...string) (bool, error) {
	result, err := c.client.Exists(ctx, c.addPrefixes(keys...)...).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

// Expire 设置键的过期时间（使用默认context）
func (c *redisClient) Expire(key string, expiration time.Duration) error {
	return c.ExpireContext(context.Background(), key, expiration)
}

// ExpireContext 设置键的过期时间（使用指定context）
func (c *redisClient) ExpireContext(ctx context.Context, key string, expiration time.Duration) error {
	return c.client.Expire(ctx, c.addPrefix(key), expiration).Err()
}

// TTL 获取键的剩余过期时间（使用默认context）
func (c *redisClient) TTL(key string) (time.Duration, error) {
	return c.TTLContext(context.Background(), key)
}

// TTLContext 获取键的剩余过期时间（使用指定context）
func (c *redisClient) TTLContext(ctx context.Context, key string) (time.Duration, error) {
	return c.client.TTL(ctx, c.addPrefix(key)).Result()
}

// Ping 检查Redis是否正常服务（使用默认context）
func (c *redisClient) Ping() error {
	return c.PingContext(context.Background())
}

// PingContext 检查Redis是否正常服务（使用指定context）
func (c *redisClient) PingContext(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// HSet 设置hash字段值（使用默认context）
func (c *redisClient) HSet(key, field string, value interface{}) error {
	return c.HSetContext(context.Background(), key, field, value)
}

// HSetContext 设置hash字段值（使用指定context）
func (c *redisClient) HSetContext(ctx context.Context, key, field string, value interface{}) error {
	return c.client.HSet(ctx, c.addPrefix(key), field, value).Err()
}

// HGet 获取hash字段值（使用默认context）
func (c *redisClient) HGet(key, field string) (string, error) {
	return c.HGetContext(context.Background(), key, field)
}

// HGetContext 获取hash字段值（使用指定context）
func (c *redisClient) HGetContext(ctx context.Context, key, field string) (string, error) {
	val, err := c.client.HGet(ctx, c.addPrefix(key), field).Result()
	if err == redis.Nil {
		return "", nil // 字段不存在，返回空字符串
	} else if err != nil {
		return "", err
	}
	return val, nil
}

// HGetAll 获取hash所有字段和值（使用默认context）
func (c *redisClient) HGetAll(key string) (map[string]string, error) {
	return c.HGetAllContext(context.Background(), key)
}

// HGetAllContext 获取hash所有字段和值（使用指定context）
func (c *redisClient) HGetAllContext(ctx context.Context, key string) (map[string]string, error) {
	return c.client.HGetAll(ctx, c.addPrefix(key)).Result()
}

// HDel 删除hash字段（使用默认context）
func (c *redisClient) HDel(key string, fields ...string) error {
	return c.HDelContext(context.Background(), key, fields...)
}

// HDelContext 删除hash字段（使用指定context）
func (c *redisClient) HDelContext(ctx context.Context, key string, fields ...string) error {
	return c.client.HDel(ctx, c.addPrefix(key), fields...).Err()
}

// HExists 检查hash字段是否存在（使用默认context）
func (c *redisClient) HExists(key, field string) (bool, error) {
	return c.HExistsContext(context.Background(), key, field)
}

// HExistsContext 检查hash字段是否存在（使用指定context）
func (c *redisClient) HExistsContext(ctx context.Context, key, field string) (bool, error) {
	return c.client.HExists(ctx, c.addPrefix(key), field).Result()
}

// HIncrBy 增加hash字段的整数值（使用默认context）
func (c *redisClient) HIncrBy(key, field string, value int64) (int64, error) {
	return c.HIncrByContext(context.Background(), key, field, value)
}

// HIncrByContext 增加hash字段的整数值（使用指定context）
func (c *redisClient) HIncrByContext(ctx context.Context, key, field string, value int64) (int64, error) {
	return c.client.HIncrBy(ctx, c.addPrefix(key), field, value).Result()
}

// HKeys 获取hash所有字段名（使用默认context）
func (c *redisClient) HKeys(key string) ([]string, error) {
	return c.HKeysContext(context.Background(), key)
}

// HKeysContext 获取hash所有字段名（使用指定context）
func (c *redisClient) HKeysContext(ctx context.Context, key string) ([]string, error) {
	return c.client.HKeys(ctx, c.addPrefix(key)).Result()
}

// HVals 获取hash所有字段值（使用默认context）
func (c *redisClient) HVals(key string) ([]string, error) {
	return c.HValsContext(context.Background(), key)
}

// HValsContext 获取hash所有字段值（使用指定context）
func (c *redisClient) HValsContext(ctx context.Context, key string) ([]string, error) {
	return c.client.HVals(ctx, c.addPrefix(key)).Result()
}

// HLen 获取hash字段数量（使用默认context）
func (c *redisClient) HLen(key string) (int64, error) {
	return c.HLenContext(context.Background(), key)
}

// HLenContext 获取hash字段数量（使用指定context）
func (c *redisClient) HLenContext(ctx context.Context, key string) (int64, error) {
	return c.client.HLen(ctx, c.addPrefix(key)).Result()
}

// HSetMultiple 一次性设置多个hash字段值（使用默认context）
func (c *redisClient) HSetMultiple(key string, values map[string]interface{}) error {
	return c.HSetMultipleContext(context.Background(), key, values)
}

// HSetMultipleContext 一次性设置多个hash字段值（使用指定context）
func (c *redisClient) HSetMultipleContext(ctx context.Context, key string, values map[string]interface{}) error {
	return c.client.HSet(ctx, c.addPrefix(key), values).Err()
}

// Publish 发布消息到频道（使用默认context）
func (c *redisClient) Publish(channel string, payload []byte) error {
	return c.PublishContext(context.Background(), channel, payload)
}

// PublishContext 发布消息到频道（使用指定context）
func (c *redisClient) PublishContext(ctx context.Context, channel string, payload []byte) error {
	return c.client.Publish(ctx, c.addPrefix(channel), payload).Err()
}

// SubscribePattern 按模式订阅频道（PSUBSCRIBE），每条消息回调 handler；返回停止订阅的函数。
//
// 用模式订阅而不是枚举频道：调用方往往事先不知道有哪些名字（比如按租户分的频道）。
// 回调在独立 goroutine 里串行执行，频道名会先去掉配置前缀再交给 handler；
// 连接断开由 go-redis 自动重连，调用方不需要自己重试。
func (c *redisClient) SubscribePattern(handler func(channel string, payload []byte), patterns ...string) (func() error, error) {
	if len(patterns) == 0 {
		return func() error { return nil }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	pubsub := c.client.PSubscribe(ctx, c.addPrefixes(patterns...)...)

	// 等订阅确认：拿不到确认就报错，别让调用方以为已经在听了
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

// Close 关闭客户端
func (c *redisClient) Close() error {
	return c.client.Close()
}

// stripPrefix 去掉键前缀（addPrefix 的逆操作），只用于把频道名还原成业务名。
func (c *redisClient) stripPrefix(key string) string {
	p := strings.TrimSuffix(c.prefix, ":")
	if p == "" {
		return key
	}
	return strings.TrimPrefix(key, p+":")
}
