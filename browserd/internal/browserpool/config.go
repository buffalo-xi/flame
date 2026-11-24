// Package browserpool is the standalone browser pool: it keeps one Chromium per live browser session, caps how many
// sessions may exist at once, queues the rest and reclaims idle ones.
//
// Why a separate service rather than a tool inside the sandbox: a browser is not "a shell that draws pictures". It is
// 200-400 MB, takes seconds to start, and carries state in its user-data-dir. Decoupling it from the sandbox means the
// sandbox mode (none / nsjail / docker) stops deciding how a browser is launched — this service decides that on its own,
// and the isolation boundary becomes "the container this service runs in" (or the OS user, when it runs bare).
//
// The one thing the browser still shares with its caller is the **workspace**: screenshots and downloads are files the
// model has to be able to open afterwards. WorkspaceRoot is therefore that shared directory (nexus's storage.root_dir),
// and every session runs the driver with cwd inside it — see workspaceDir.
package browserpool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// WorkspaceRoot is the directory shared with the caller: it must be the same mount as nexus's storage.root_dir.
	//
	// It exists because a browser produces **files** (screenshots, downloads) and the model has to be able to open them
	// afterwards. Writing them into this service's own cwd would leave them on a machine the tenant never sees, so each
	// session instead runs the driver with cwd = WorkspaceRoot + the workspace path the caller sent in the open frame.
	WorkspaceRoot string `yaml:"workspace_root"`

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

	// Registry is the optional service registration (consul / nacos).
	//
	// It exists so browserg's discovery can find this instance: the gateway can only discover what registers itself, so a
	// pool managed by discovery needs this switched on. A deployment with a static `backends` list on the gateway does not.
	Registry RegistryConfig `yaml:"registry"`
}

// RegistryConfig is the service registration of one browserd instance.
type RegistryConfig struct {
	// Enabled turns registration on. Default false: an unregistered instance is reachable through a static backend list,
	// and announcing itself to a registry nobody reads is noise.
	Enabled bool `yaml:"enabled"`
	// Type is the registry kind: consul | nacos. Empty defaults to consul when Enabled.
	Type string `yaml:"type"`
	// Address is the registry address (such as localhost:8500).
	Address string `yaml:"address"`
	// ServiceName is the name every instance registers under; browserg's discovery.service_name must match it.
	ServiceName string `yaml:"service_name"`
	// ServiceID is this instance's identity inside the registry; empty means
	// "<service_name>-<advertised address>-<port>", which is unique per host automatically.
	//
	// Set it only when an external system needs a stable, human-chosen name: the ID is the key of the agent's
	// local service table, so a value that does not include the host makes two machines overwrite each other.
	ServiceID string `yaml:"service_id"`
	// ServiceAddr is the address the gateway should dial: "host" or "host:port". Empty means derive it from
	// listen (a concrete bind address is used as-is; a wildcard bind falls back to AdvertiseIface, then to
	// the hostname's first non-loopback address).
	//
	// It is not the same thing as Listen and cannot be replaced by it: a wildcard bind (0.0.0.0) says nothing
	// about which address is reachable from another machine.
	ServiceAddr string `yaml:"service_addr"`
	// AdvertiseIface names the network interface to take the advertised address from when listen binds a
	// wildcard address. Empty means the hostname's first non-loopback address.
	AdvertiseIface string `yaml:"advertise_iface"`
	// ServicePort is deprecated and ignored: the advertised port always follows listen, because a
	// registration whose port differs from the listener is a configuration error in every deployment that
	// does not also remap the port. The field stays so an old file can be told so instead of silently losing
	// the value.
	ServicePort int `yaml:"service_port"`
	// HealthCheckPath is what the registry probes; empty means /healthz.
	HealthCheckPath string `yaml:"health_check_path"`
}

// BrowserConfig is how the browser itself is launched.
type BrowserConfig struct {
	// Binary is the browserhost executable (the driver). Empty means "browserhost" from PATH.
	Binary string `yaml:"binary"`
	// ExecPath is the Chromium executable; empty = browserhost autodetects chromium / chrome / msedge.
	ExecPath string `yaml:"exec_path"`
	// ProfileRoot is the parent of the per-session user-data-dir. Every session gets its own directory: sharing one
	// would leak cookies and local storage from one session into the next.
	//
	// It deliberately does **not** live under WorkspaceRoot: the profile is this service's private state, and it must be
	// deleted when the session ends (see Pool.cleanupProfile), while the workspace is the tenant's and is never touched.
	ProfileRoot string `yaml:"profile_root"`
	// Headless defaults to true; a pointer so that an explicit `headless: false` is distinguishable from "not set".
	Headless *bool `yaml:"headless"`
	// AllowHeaded defaults to false: a headed browser needs a display, which a pool container does not have. It is
	// useful when the pool runs bare on a developer's Windows desktop.
	AllowHeaded *bool `yaml:"allow_headed"`
}

// Defaults, kept in one place so the config file can stay minimal.
const (
	defaultListen          = ":27091"
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
	if c.WorkspaceRoot == "" {
		c.WorkspaceRoot = filepath.Join("run", "browserd", "workspace")
	}
}

// normalizePaths resolves the directory roots against the startup cwd.
//
// A relative profile root would otherwise mean "wherever this process happened to be started from" — the same class of
// bug as a relative tmp_dir, and here it decides where a few hundred megabytes of Chromium profile land. The same
// applies to the workspace root, which decides where the session's files show up.
func (c *Config) normalizePaths() error {
	for _, f := range []struct {
		key string
		p   *string
	}{
		{"workspace_root", &c.WorkspaceRoot},
		{"browser.profile_root", &c.Browser.ProfileRoot},
	} {
		abs, err := filepath.Abs(*f.p)
		if err != nil {
			return fmt.Errorf("%s: %q is not a usable path: %w", f.key, *f.p, err)
		}
		*f.p = abs
	}

	// The driver is started with cwd = the session's workspace (see startProc), so a relative driver path is resolved
	// against the tenant's directory, not against this service: "bin/browserhost.exe" then becomes
	// "<workspace>/bin/browserhost.exe" and every spawn fails with "the system cannot find the path specified" while the
	// self-test — which runs from this process's own cwd — still passes. Anything that names a host path is therefore
	// made absolute here; a bare command name keeps PATH semantics (defaultBinary is deliberately one).
	for _, f := range []struct {
		key string
		p   *string
	}{
		{"browser.binary", &c.Browser.Binary},
		{"browser.exec_path", &c.Browser.ExecPath},
	} {
		abs, err := absolutizeCommandPath(*f.p)
		if err != nil {
			return fmt.Errorf("%s: %q is not a usable path: %w", f.key, *f.p, err)
		}
		*f.p = abs
	}
	return nil
}

// absolutizeCommandPath turns a host path into an absolute one, leaving everything else alone.
//
// The three cases mirror sandboxd's resolveCodehostBinary, for the same reason:
//   - an empty value stays empty ("not configured": the default, or autodetect for exec_path);
//   - a leading "/" is a **container** path whose meaning belongs to the image's rootfs, so it is passed through
//     verbatim — filepath.Abs is platform dependent and on Windows would turn /usr/bin/chromium into d:\usr\..., which
//     could not be exec'd;
//   - any other value containing a separator (either kind: "bin/browserhost.exe" is a path on Windows too) is a host
//     path and is resolved against this service's startup cwd;
//   - a bare name is a PATH lookup and is left alone.
func absolutizeCommandPath(v string) (string, error) {
	switch {
	case v == "":
		return "", nil
	case strings.HasPrefix(v, "/"):
		return v, nil
	case strings.ContainsAny(v, `/\`) || filepath.VolumeName(v) != "":
		return filepath.Abs(v)
	}
	return v, nil
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
	if c.Registry.Enabled {
		if c.Registry.Type == "" {
			c.Registry.Type = "consul"
		}
		switch c.Registry.Type {
		case "consul", "nacos":
		default:
			return fmt.Errorf("registry.type = %q (want consul or nacos)", c.Registry.Type)
		}
		if c.Registry.Address == "" {
			return fmt.Errorf("registry.enabled requires registry.address")
		}
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
