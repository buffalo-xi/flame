package gw

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the sandboxg gateway configuration.
type Config struct {
	// Listen is the address exposed to nexus.
	Listen string `yaml:"listen"`
	// LogDir is the log directory.
	LogDir string `yaml:"log_dir"`
	// Token authenticates nexus (optional). Empty means no authentication.
	Token string `yaml:"token"`
	// MetricsIntervalSec is how often the backend /v1/metrics is scraped (seconds).
	MetricsIntervalSec int `yaml:"metrics_interval_sec"`
	// StickyTTLSec is the lifetime of an interactive sticky binding (seconds).
	//
	// It must be **greater** than sandboxd's interactive_idle_sec + 2× the janitor sweep interval (60s):
	//   - binding expiring before the process → the same session may land on two backends and build one
	//     process on each (split state);
	//   - binding expiring after the process → the next open lands on the original backend and lazily
	//     builds a new process, leaving just one more record that the TTL will clear.
	// Too long beats too short. The FrameSessionEmpty returned by kill is the immediate invalidation path
	// and does not depend on this value.
	StickyTTLSec int `yaml:"sticky_ttl_sec"`
	// HealthCheckIntervalSec is how often /healthz is probed (seconds).
	HealthCheckIntervalSec int `yaml:"health_check_interval_sec"`
	// HealthCheckFailThreshold evicts a backend after N consecutive failures.
	HealthCheckFailThreshold int `yaml:"health_check_fail_threshold"`
	// Redis is the shared store for sticky bindings (mandatory for a multi-instance gateway; empty falls
	// back to per-process memory).
	Redis RedisConfig `yaml:"redis"`
	// Backends is the static sandboxd instance pool (used when discovery is empty; a seed / fallback once
	// discovery is configured).
	Backends []BackendConfig `yaml:"backends"`
	// SandboxToken is the token shared by every backend (service discovery only yields addresses, the
	// token is attached uniformly).
	SandboxToken string `yaml:"sandbox_token"`
	// Discovery is service discovery (the source of the backend list). An empty type falls back to the
	// static backends.
	Discovery DiscoveryConfig `yaml:"discovery"`
}

// RedisConfig is the redis configuration of the sticky store.
type RedisConfig struct {
	// Addr is the address (such as localhost:6379). Empty means no redis and per-process memory instead.
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// BackendConfig is a single sandboxd instance.
type BackendConfig struct {
	// URL is the HTTP base address (such as http://127.0.0.1:27081). The gateway derives ws and /metrics
	// from it.
	URL string `yaml:"url"`
	// Token authenticates against the backend (optional).
	Token string `yaml:"token"`
}

// DiscoveryConfig is the service discovery configuration (the source of the backend list).
type DiscoveryConfig struct {
	// Type is the registry kind: consul | nacos. Empty means no discovery and a fallback to the static
	// backends.
	Type string `yaml:"type"`
	// Address is the registry address (such as localhost:8500).
	Address string `yaml:"address"`
	// ServiceName is the service to discover (sandboxd by default).
	ServiceName string `yaml:"service_name"`
	// IntervalSec is how often the instance list is fetched and reconciled (seconds).
	IntervalSec int `yaml:"interval_sec"`
}

// LoadConfig reads and validates the configuration.
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
		// The hard requirement is sticky_ttl > sandboxd's interactive_idle_sec + 2×janitor_sec, checked at run
		// time against /v1/capabilities. sandboxd now defaults to 300, making the lower bound 420, but 2100 stays
		// the default: it still covers a deployment that left interactive_idle_sec at the old 1800, and the two
		// failure modes are not symmetric - a binding that outlives the process costs one stale record, while one
		// that expires early can split a session across two backends.
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
