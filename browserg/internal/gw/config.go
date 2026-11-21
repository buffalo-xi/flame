package gw

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the browserg gateway configuration.
//
// It mirrors sandboxg on purpose (one gateway in front of a pool of instances, transparent frames), but the numbers it
// carries describe a browser pool: the capacity unit is a live Chromium, not a per-call process.
type Config struct {
	// Listen is the address exposed to nexus.
	Listen string `yaml:"listen"`
	// LogDir is the log directory.
	LogDir string `yaml:"log_dir"`
	// Token authenticates nexus (optional). Empty means no authentication.
	Token string `yaml:"token"`
	// BrowserToken authenticates towards the instances that came from discovery. Instances listed under Backends use
	// their own token field; a registry only yields addresses, so this is the uniform token for those.
	BrowserToken string `yaml:"browser_token"`
	// StickyTTLSec is the lifetime of a session binding (seconds).
	//
	// It must be **greater** than browserd's idle_sec + 2× janitor_sec (checked at run time against each instance):
	//   - binding expiring before the lease → the same session lands on another instance and builds a second Chromium
	//     (two sets of cookies, two pages, and the model sees whichever it hits last);
	//   - binding expiring after the lease → the next open lands on the original instance and lazily builds a new
	//     browser, costing one stale record that the TTL clears.
	// Too long beats too short. The FrameSessionEmpty returned by a kill is the immediate invalidation path and does not
	// depend on this value.
	StickyTTLSec int `yaml:"sticky_ttl_sec"`
	// SnapshotIntervalSec is how often each instance's /v1/capabilities and /v1/sessions are pulled (seconds).
	// It bounds how stale the load score and the per-tenant live count are.
	SnapshotIntervalSec int `yaml:"metrics_interval_sec"`
	// HealthCheckIntervalSec is how often /healthz is probed (seconds).
	HealthCheckIntervalSec int `yaml:"health_check_interval_sec"`
	// HealthCheckFailThreshold evicts an instance after N consecutive failures.
	HealthCheckFailThreshold int `yaml:"health_check_fail_threshold"`
	// MaxPerTenant caps how many live sessions one tenant may hold **across the whole pool** (0 = no cap).
	//
	// The gateway is the only enforcement point: a per-instance cap would multiply by the number of instances, so a
	// browserd deployed behind this gateway should leave its own max_per_tenant at 0 and let workers be the hard ceiling.
	MaxPerTenant int `yaml:"max_per_tenant"`
	// Redis is the shared store for sticky bindings (mandatory for a multi-instance gateway; empty falls back to
	// per-process memory, which only makes sense for a single gateway).
	Redis RedisConfig `yaml:"redis"`
	// Backends is the static pool: the only source when discovery.type is empty, and the initial seed when it is set.
	Backends []BackendConfig `yaml:"backends"`
	// Discovery is service discovery (the source of the instance list). A failing pull, and an empty answer, keep the
	// current list.
	Discovery DiscoveryConfig `yaml:"discovery"`
}

// RedisConfig is the redis configuration of the sticky store.
type RedisConfig struct {
	// Addr is the address (such as localhost:6379). Empty means no redis and per-process memory instead.
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// BackendConfig is a single browserd instance.
type BackendConfig struct {
	// URL is the HTTP base address (such as http://127.0.0.1:27091). The gateway derives ws and the metadata paths
	// from it.
	URL string `yaml:"url"`
	// Token authenticates against the instance (optional).
	Token string `yaml:"token"`
}

// DiscoveryConfig is the service discovery configuration.
type DiscoveryConfig struct {
	// Type is the registry kind: consul | nacos. Empty means no discovery and a fallback to the static backends.
	Type string `yaml:"type"`
	// Address is the registry address (such as localhost:8500).
	Address string `yaml:"address"`
	// ServiceName is the service to discover (browserd by default).
	ServiceName string `yaml:"service_name"`
	// IntervalSec is how often the instance list is fetched and reconciled (seconds).
	IntervalSec int `yaml:"interval_sec"`
}

// LoadConfig reads and validates the configuration.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("browserg: read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("browserg: parse config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":27090"
	}
	if c.LogDir == "" {
		c.LogDir = "logs"
	}
	if c.StickyTTLSec <= 0 {
		// The hard lower bound is idle_sec + 2×janitor_sec of every instance, checked continuously against
		// /v1/capabilities. 2100 also covers a deployment that left the pool's idle at the old sandbox defaults, and the
		// two failure modes are not symmetric: one stale record versus one session split across two instances.
		c.StickyTTLSec = 2100
	}
	if c.SnapshotIntervalSec <= 0 {
		c.SnapshotIntervalSec = 2
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
		c.Discovery.ServiceName = "browserd"
	}
}

func (c *Config) validate() error {
	if c.Discovery.Type == "" && len(c.Backends) == 0 {
		return fmt.Errorf("browserg: no backends and no discovery configured")
	}
	for i, b := range c.Backends {
		if b.URL == "" {
			return fmt.Errorf("browserg: backends[%d].url is empty", i)
		}
	}
	if c.MaxPerTenant < 0 {
		return fmt.Errorf("browserg: max_per_tenant = %d (want 0 for unlimited, or a positive cap)", c.MaxPerTenant)
	}
	switch c.Discovery.Type {
	case "", "consul", "nacos":
	default:
		return fmt.Errorf("browserg: discovery.type = %q (want \"\", consul or nacos)", c.Discovery.Type)
	}
	return nil
}
