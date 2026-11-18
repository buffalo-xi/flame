package gw

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config sandboxg 网关配置。
type Config struct {
	// Listen 对 nexus 暴露的监听地址。
	Listen string `yaml:"listen"`
	// LogDir 日志目录。
	LogDir string `yaml:"log_dir"`
	// Token 对 nexus 的鉴权（可选）。空表示不鉴权。
	Token string `yaml:"token"`
	// MetricsIntervalSec 拉取后端 /v1/metrics 的间隔（秒）。
	MetricsIntervalSec int `yaml:"metrics_interval_sec"`
	// StickyTTLSec interactive 粘性绑定的存活时长（秒）。
	//
	// 必须**大于** sandboxd 的 interactive_idle_sec + 2×janitor 巡检间隔（60s）：
	//   - 绑定比进程先过期 → 同一 session 可能落到两个后端、各建一个进程（状态分裂）；
	//   - 绑定比进程晚过期 → 下次 open 落到原后端懒建新进程，只多一条会被 TTL 清掉的记录。
	// 宁长勿短。kill 给出的 FrameSessionEmpty 是即时失效口，不依赖这个值。
	StickyTTLSec int `yaml:"sticky_ttl_sec"`
	// HealthCheckIntervalSec 探 /healthz 的间隔（秒）。
	HealthCheckIntervalSec int `yaml:"health_check_interval_sec"`
	// HealthCheckFailThreshold 连续失败 N 次判为不健康并摘除。
	HealthCheckFailThreshold int `yaml:"health_check_fail_threshold"`
	// Redis 粘性绑定的共享存储（多实例 gateway 必配；留空则退化为单机内存）。
	Redis RedisConfig `yaml:"redis"`
	// Backends 静态 sandboxd 实例池（discovery 留空时使用；discovery 配置后作为种子/兜底）。
	Backends []BackendConfig `yaml:"backends"`
	// SandboxToken 所有后端共用的 token（服务发现只给地址，token 统一附加）。
	SandboxToken string `yaml:"sandbox_token"`
	// Discovery 服务发现（后端列表来源）。type 留空则退回静态 backends。
	Discovery DiscoveryConfig `yaml:"discovery"`
}

// RedisConfig 粘性存储的 redis 配置。
type RedisConfig struct {
	// Addr 地址（如 localhost:6379）。空表示不用 redis、退化为单机内存。
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// BackendConfig 单个 sandboxd 实例。
type BackendConfig struct {
	// URL HTTP 基地址（如 http://127.0.0.1:27081）。gateway 内部自动换算 ws 与 /metrics。
	URL string `yaml:"url"`
	// Token 连后端的鉴权（可选）。
	Token string `yaml:"token"`
}

// DiscoveryConfig 服务发现配置（后端列表来源）。
type DiscoveryConfig struct {
	// Type 注册中心类型：consul | nacos。留空表示不用发现、退回静态 backends。
	Type string `yaml:"type"`
	// Address 注册中心地址（如 localhost:8500）。
	Address string `yaml:"address"`
	// ServiceName 要发现的服务名（默认 sandboxd）。
	ServiceName string `yaml:"service_name"`
	// IntervalSec 拉取实例列表并对账的间隔（秒）。
	IntervalSec int `yaml:"interval_sec"`
}

// LoadConfig 读取并校验配置。
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sandboxg: read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("sandboxg: parse config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":27080"
	}
	if c.LogDir == "" {
		c.LogDir = "logs"
	}
	if c.MetricsIntervalSec <= 0 {
		c.MetricsIntervalSec = 2
	}
	if c.StickyTTLSec <= 0 {
		// sandboxd 缺省 interactive_idle_sec=1800 + 最多 60s 的巡检延迟；
		// 留 240s 余量，宁可多留垃圾也不能让绑定先于进程消失。
		c.StickyTTLSec = 2100
	}
	if c.HealthCheckIntervalSec <= 0 {
		c.HealthCheckIntervalSec = 5
	}
	if c.HealthCheckFailThreshold <= 0 {
		c.HealthCheckFailThreshold = 3
	}
	if c.Discovery.IntervalSec <= 0 {
		c.Discovery.IntervalSec = 5
	}
	if c.Discovery.ServiceName == "" {
		c.Discovery.ServiceName = "sandboxd"
	}
}

func (c *Config) validate() error {
	if c.Discovery.Type == "" && len(c.Backends) == 0 {
		return fmt.Errorf("sandboxg: no backends and no discovery configured")
	}
	for i, b := range c.Backends {
		if b.URL == "" {
			return fmt.Errorf("sandboxg: backends[%d].url is empty", i)
		}
	}
	return nil
}
