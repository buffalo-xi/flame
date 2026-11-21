// Package browserpool is the standalone browser pool: it keeps one Chromium per live browser session, caps how many
// sessions may exist at once, queues the rest and reclaims idle ones.
//
// Why a separate service rather than a tool inside the sandbox: a browser is not "a shell that draws pictures". It is
// 200-400 MB, takes seconds to start, and carries state in its user-data-dir. Decoupling it from the sandbox means the
// sandbox mode (none / nsjail / docker) stops deciding how a browser is launched — this service decides that on its own,
// and the isolation boundary becomes "the container this service runs in" (or the OS user, when it runs bare).
package browserpool

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode values.
const (
	// ModeProcess runs Chromium as a plain child process of this service. In a container the container is the
	// boundary; bare on Windows the browser's own sandbox stays on.
	ModeProcess = "process"
	// ModeDocker (one container per worker) is deliberately not implemented yet: it is only useful for debugging, and
	// it would make this service depend on the docker CLI for no operational gain.
	ModeDocker = "docker"
)

// Config is the browserd configuration.
//
// Everything that used to be derived from sandboxd's isolation mode lives here as a plain value: which binary drives
// the browser, which Chromium to launch, where profiles and downloads go. There is no per-mode map any more, because
// there is only one way this service runs the browser.
type Config struct {
	Listen string `yaml:"listen"`
	// Token authenticates the caller (nexus). Empty means no authentication — internal network only.
	Token  string `yaml:"token"`
	LogDir string `yaml:"log_dir"`
	// Mode is how Chromium is started: process (this container / this host is the boundary) or docker (not implemented).
	Mode string `yaml:"mode"`

	// Workers is the maximum number of live browser sessions. It is also the memory ceiling of this service: one
	// Chromium per live session, so the expected footprint is roughly Workers x 300 MB at full house.
	Workers int `yaml:"workers"`
	// MaxPerTenant caps how many sessions one tenant may hold at once (0 = no per-tenant cap). Without it a single
	// tenant can occupy every worker and make everyone else queue.
	MaxPerTenant int `yaml:"max_per_tenant"`
	// QueueTimeoutSec is how long a call waits for a free worker before it is rejected with busy. Queueing instead of
	// failing fast keeps short bursts working; a timeout instead of waiting forever keeps a stuck run from piling up.
	QueueTimeoutSec int `yaml:"queue_timeout_sec"`
	// IdleTimeoutSec is how long a session survives without a call. A live session keeps a Chromium resident, which is
	// the whole cost of this design, so the reclaim window is deliberately short.
	IdleTimeoutSec int `yaml:"idle_timeout_sec"`
	// JanitorSec is the sweep interval: the real reclaim time falls in (idle, idle+janitor].
	JanitorSec int `yaml:"janitor_sec"`

	Browser BrowserConfig `yaml:"browser"`
}

// BrowserConfig is how the browser itself is launched.
type BrowserConfig struct {
	// Binary is the browserhost executable (the driver). Empty means "browserhost" from PATH.
	Binary string `yaml:"binary"`
	// ExecPath is the Chromium executable; empty = browserhost autodetects chromium / chrome / msedge.
	ExecPath string `yaml:"exec_path"`
	// ProfileRoot is the parent of the per-session user-data-dir. Every session gets its own directory: sharing one
	// would leak cookies and local storage from one session into the next.
	ProfileRoot string `yaml:"profile_root"`
	// DownloadsRoot is the parent of the per-session download directory.
	DownloadsRoot string `yaml:"downloads_root"`
	// Headless defaults to true; a pointer so that an explicit `headless: false` is distinguishable from "not set".
	Headless *bool `yaml:"headless"`
	// AllowHeaded defaults to false: a headed browser needs a display, which a pool container does not have. It is
	// useful when the pool runs bare on a developer's Windows desktop.
	AllowHeaded *bool `yaml:"allow_headed"`
}

// Defaults, kept in one place so the config file can stay minimal.
const (
	defaultListen          = ":27090"
	defaultLogDir          = "logs"
	defaultWorkers         = 2
	defaultQueueTimeoutSec = 20
	defaultIdleTimeoutSec  = 300
	defaultJanitorSec      = 30

	// defaultBinary is the driver's file name; resolved through PATH, like every other "bare command name" in this
	// project (see resolveCodehostBinary in sandboxd).
	defaultBinary = "browserhost"
)

// LoadConfig reads and normalises the configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("browserd: read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("browserd: parse config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.normalizePaths(); err != nil {
		return nil, fmt.Errorf("browserd: config %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("browserd: config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	if c.LogDir == "" {
		c.LogDir = defaultLogDir
	}
	if c.Mode == "" {
		c.Mode = ModeProcess
	}
	if c.Workers <= 0 {
		c.Workers = defaultWorkers
	}
	if c.QueueTimeoutSec <= 0 {
		c.QueueTimeoutSec = defaultQueueTimeoutSec
	}
	if c.IdleTimeoutSec <= 0 {
		c.IdleTimeoutSec = defaultIdleTimeoutSec
	}
	if c.JanitorSec <= 0 {
		c.JanitorSec = defaultJanitorSec
	}
	if c.Browser.Binary == "" {
		c.Browser.Binary = defaultBinary
	}
	if c.Browser.ProfileRoot == "" {
		c.Browser.ProfileRoot = filepath.Join("run", "browserd", "profiles")
	}
	if c.Browser.DownloadsRoot == "" {
		c.Browser.DownloadsRoot = filepath.Join("run", "browserd", "downloads")
	}
}

// normalizePaths resolves the two directory roots against the startup cwd.
//
// A relative profile root would otherwise mean "wherever this process happened to be started from" — the same class of
// bug as a relative tmp_dir, and here it decides where a few hundred megabytes of Chromium profile land.
func (c *Config) normalizePaths() error {
	for _, f := range []struct {
		key string
		p   *string
	}{
		{"browser.profile_root", &c.Browser.ProfileRoot},
		{"browser.downloads_root", &c.Browser.DownloadsRoot},
	} {
		abs, err := filepath.Abs(*f.p)
		if err != nil {
			return fmt.Errorf("%s: %q is not a usable path: %w", f.key, *f.p, err)
		}
		*f.p = abs
	}
	return nil
}

func (c *Config) validate() error {
	switch c.Mode {
	case ModeProcess:
	case ModeDocker:
		return fmt.Errorf("mode docker is not implemented yet — use mode: process (the container this service runs in is the boundary)")
	default:
		return fmt.Errorf("mode %q is not supported (want %s)", c.Mode, ModeProcess)
	}
	if c.Workers < 1 {
		return fmt.Errorf("workers must be at least 1")
	}
	if c.MaxPerTenant < 0 {
		return fmt.Errorf("max_per_tenant must not be negative")
	}
	if c.IdleTimeoutSec < 1 {
		return fmt.Errorf("idle_timeout_sec must be at least 1 second")
	}
	if c.JanitorSec < 1 {
		return fmt.Errorf("janitor_sec must be at least 1 second")
	}
	return nil
}

// QueueTimeout is how long a call waits for a free worker.
func (c *Config) QueueTimeout() time.Duration {
	return time.Duration(c.QueueTimeoutSec) * time.Second
}

// IdleTimeout is the idle reclaim window of a session.
func (c *Config) IdleTimeout() time.Duration {
	return time.Duration(c.IdleTimeoutSec) * time.Second
}

// JanitorInterval is the sweep interval of idle reclamation.
func (c *Config) JanitorInterval() time.Duration {
	return time.Duration(c.JanitorSec) * time.Second
}

// HeadlessDefault reports whether a request that does not say otherwise gets a headless browser.
func (c *Config) HeadlessDefault() bool {
	if c.Browser.Headless == nil {
		return true
	}
	return *c.Browser.Headless
}

// HeadedAllowed reports whether headed browsing is permitted at all.
func (c *Config) HeadedAllowed() bool {
	return c.Browser.AllowHeaded != nil && *c.Browser.AllowHeaded
}
