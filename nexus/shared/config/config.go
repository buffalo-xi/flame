package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the service configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
	Registry RegistryConfig `yaml:"registry"`
	Redis    RedisConfig    `yaml:"redis"`
	JWT      JWTConfig      `yaml:"jwt"`
	Security SecurityConfig `yaml:"security"`
	LiteLLM  LiteLLMConfig  `yaml:"litellm"`
	Agent    AgentConfig    `yaml:"agent"`
	Sandbox  SandboxConfig  `yaml:"sandbox"`
	Plugin   PluginConfig   `yaml:"plugin"`
}

// PluginConfig is the host configuration of the on-disk plugins
// ({root_dir}/plugins/<kind>/<tool>/<impl>/plugin.py).
//
// Only "host-level" parameters live here: where the plugin directory is, which interpreter to use, the timeout and the
// refresh interval.
// Which implementation each tenant uses and what its keys are is tenant-level configuration (the DB plugins table) and
// does not belong here.
type PluginConfig struct {
	Dir             string `yaml:"dir"`              // empty = {agent.root_dir}/plugins; an absolute path also works
	Python          string `yaml:"python"`           // interpreter command; empty = python (usually python on Windows)
	Timeout         int    `yaml:"timeout"`          // per-call timeout in seconds; 0 = 30
	RefreshInterval int    `yaml:"refresh_interval"` // manifest refresh interval in seconds; 0 = 60

	// The call quota is not here: it is a "tenant × plugin" platform policy stored in
	// {platform_schema}.tenant_plugins (see shared/pluginpolicy and
	// platform/sql/tenant_plugins.sql). In a config file it would become one global number,
	// neither per-tenant nor able to stop a tenant from raising its own quota.
}

// DirOrDefault returns the plugin root: when dir is empty, {root_dir}/plugins.
//
// rootDir is passed in by the caller (config.Agent.RootDir) rather than read from the global Cfg here — that avoids the
// circular-dependency-flavoured style of "the config package reads the global config".
func (c PluginConfig) DirOrDefault(rootDir string) string {
	if d := strings.TrimSpace(c.Dir); d != "" {
		return d
	}
	root := strings.TrimSpace(rootDir)
	if root == "" {
		return "plugins"
	}
	return filepath.Join(root, "plugins")
}

// TimeoutDuration is the per-call timeout: 30s when it is not configured.
func (c PluginConfig) TimeoutDuration() time.Duration {
	if c.Timeout > 0 {
		return time.Duration(c.Timeout) * time.Second
	}
	return 30 * time.Second
}

// RefreshIntervalDuration is the manifest refresh interval: 60s when it is not configured.
func (c PluginConfig) RefreshIntervalDuration() time.Duration {
	if c.RefreshInterval > 0 {
		return time.Duration(c.RefreshInterval) * time.Second
	}
	return time.Minute
}

// Sandbox endpoint paths (matching sandboxd's routes, see sandboxd/internal/daemon/server.go).
const (
	// sandboxExecPath is the simplex endpoint: one-shot command execution (the caller supplies the full argv).
	sandboxExecPath = "/v1/exec"
	// sandboxCodePath is the managed code snippet endpoint: the caller supplies "language + snippet + inputs", and the
	// server weaves them into a complete program through a wrapper template, hosts the interpreter and returns a
	// structured result.
	//
	// Renamed from /v1/script in 2026-10: the exec channel also runs script files (.py/.js/.lua/.sh), so naming the
	// channel "script" took a property both channels share and made it the name of one of them. The real difference in
	// the contract is "who decides the interpreter and the input/output protocol", which is exactly what code says.
	sandboxCodePath = "/v1/code"
	// legacySandboxScriptPath is the old endpoint, **used only for stripping** (a config may carry the full old
	// endpoint).
	// It is never a request path: both ends are in the same repo and module, so old and new routes do not coexist.
	legacySandboxScriptPath = "/v1/script"
	// sandboxInteractivePath is the long-interaction endpoint: a persistent process that outlives the connection (the
	// shell tool).
	sandboxInteractivePath = "/v1/interactive"
	// sandboxCapabilitiesPath is the capability snapshot endpoint.
	//
	// ⚠️ This is **plain HTTP** (not WebSocket): it returns sandboxd's isolation mode, network declaration and the
	// sandbox environment probed at startup, which nexus pulls and injects into the agent prompt.
	sandboxCapabilitiesPath = "/v1/capabilities"
)

// SandboxConfig is the sandbox service connection configuration.
//
// The nexus side does no isolation and holds no pool: every execution is delegated to the separate sandboxd service.
// The isolation mode (none / nsjail / docker), the resource limits and the worker pool policy are all decided by
// sandboxd's own configuration — the business side only connects to it and submits requests.
//
// Only a "base address" is given here, not two full URLs: simplex / duplex are **two routes on the same sandboxd**, and
// configuring a host:port for each would allow two different instances, while mode / limits / allowed_mount_roots / token
// are all instance-level — a mismatch shows up as "exec works, but the workflow code node reports a mount violation or
// busy", which is extremely hard to pin down. This type assembles the paths by purpose.
type SandboxConfig struct {
	// URL is the sandbox service base address (without a path), such as ws://sandboxd:8080 or wss://sandboxd:443.
	URL string `yaml:"url"`
	// Token is optional: sent as a Bearer token with the handshake, and must match the sandboxd side.
	Token string `yaml:"token"`
}

// ExecEndpoint is the simplex endpoint (the agent's exec / run_skill_script tools).
// An unconfigured URL returns an empty string, and the caller decides whether it is usable.
func (c SandboxConfig) ExecEndpoint() string { return c.endpoint(sandboxExecPath) }

// CodeEndpoint is the code snippet endpoint (workflow code / branch / loop / binding).
// An unconfigured URL returns an empty string, and the caller decides whether it is usable.
func (c SandboxConfig) CodeEndpoint() string { return c.endpoint(sandboxCodePath) }

// InteractiveEndpoint is the long-interaction endpoint (the agent's shell tool): a persistent process that outlives the
// connection.
// An unconfigured URL returns an empty string, and the caller decides whether it is usable.
func (c SandboxConfig) InteractiveEndpoint() string { return c.endpoint(sandboxInteractivePath) }

// CapabilitiesEndpoint is the capability snapshot endpoint (HTTP): whether the sandbox can reach the network and what it
// contains.
//
// ⚠️ Unlike the two above, this is plain HTTP rather than WebSocket — sandbox.url holds a ws:// base address, so the
// scheme must be converted to http here (wss -> https), otherwise net/http rejects the URL outright.
// An unconfigured URL returns an empty string, and the caller decides whether it is usable.
func (c SandboxConfig) CapabilitiesEndpoint() string {
	switch u := c.endpoint(sandboxCapabilitiesPath); {
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	default:
		return u // already http(s), or unconfigured (an empty string)
	}
}

// endpoint assembles "base address + purpose path".
//
// It also tolerates "the full endpoint written into url" (such as ws://h/v1/exec): a trailing known endpoint path is
// stripped first and the path appended afterwards, so /v1/exec/v1/exec never appears. An empty value returns an empty
// string as is, never a half address such as "/v1/exec".
//
// The strip list gained legacySandboxScriptPath: the endpoint path changed from /v1/script to /v1/code in 2026-10, and
// **the endpoint can be written into config.yaml** (outside compile time) — deployments that put the full old endpoint
// into url really exist, and without the strip they would assemble /v1/script/v1/code. It is used only for stripping and
// provides no route compatibility: requests always go to /v1/code. The route itself stays a compile-time matter; both
// ends are in the same repo and module, so one build keeps them in sync.
func (c SandboxConfig) endpoint(p string) string {
	base := strings.TrimRight(strings.TrimSpace(c.URL), "/")
	for _, ep := range []string{sandboxExecPath, sandboxCodePath, sandboxInteractivePath, legacySandboxScriptPath} {
		if strings.HasSuffix(base, ep) {
			base = strings.TrimRight(strings.TrimSuffix(base, ep), "/")
			break
		}
	}
	if base == "" {
		return ""
	}
	return base + p
}

// AgentConfig is the agent storage configuration.
type AgentConfig struct {
	RootDir string          `yaml:"root_dir"`
	Storage StorageConfig   `yaml:"storage"`
	HTTP    AgentHTTPConfig `yaml:"http"`
}

// AgentHTTPConfig is how workspace file "downloads" are delivered (it affects downloads only, not previews or uploads).
type AgentHTTPConfig struct {
	// XAccelPrefix is the nginx internal path prefix (such as /__files). Once configured, downloads are sent by nginx
	// straight from disk: nexus returns only an X-Accel-Redirect header, spending no application bandwidth and no
	// goroutine, and Range comes for free.
	// Empty (the default) falls back to http.ServeContent, served by this process.
	XAccelPrefix string `yaml:"x_accel_prefix"`

	// PresignDownload makes S3-stored downloads 302 straight to a presigned URL, so the browser fetches the bytes from
	// the object store (Range / resumable downloads for free, and nexus spends no bandwidth).
	//
	// The default is false, deliberately: it is an assertion about **this deployment**, not a performance option —
	//   - an internal MinIO often is "reachable from nexus but not from the browser";
	//   - some S3-compatible implementations ignore response-content-disposition (the download name stays garbled);
	//   - when the bucket policy only allows access through a CDN, a presigned direct hit on the origin returns 403.
	// The price of a wrong assertion is a 400 on download, not a slower one. Even with the s3 driver it can be turned off
	// to go through nexus (exactly the behaviour from before this feature existed).
	PresignDownload bool `yaml:"presign_download"`

	// PresignProbeKey lets the assertion above be evaluated automatically: configure a small object that really exists in
	// the bucket (0 bytes is best), and before the first download the URL is signed once and really GET, checking whether
	// the response headers were overridden. It is used only if that passes; otherwise it degrades automatically (retrying
	// after a 10-minute cooldown). Empty = no probe, PresignDownload is trusted directly.
	// Note: it must be an object that **exists** — a missing key only yields an error XML, which cannot prove the headers
	// were overridden.
	PresignProbeKey string `yaml:"presign_probe_key"`

	// PresignTTL is the presigned link lifetime in seconds, 300 by default: enough for the browser to catch the 302,
	// without leaking the link for long.
	PresignTTL int `yaml:"presign_ttl"`
	// MaxDownloadSize is the per-file download limit in MiB, 100 by default.
	MaxDownloadSize int64 `yaml:"max_download_size"`
}

// PresignTTLDuration is the presigned lifetime: 5 minutes when it is not configured.
func (c AgentHTTPConfig) PresignTTLDuration() time.Duration {
	if c.PresignTTL > 0 {
		return time.Duration(c.PresignTTL) * time.Second
	}
	return 5 * time.Minute
}

// MaxDownloadBytes is the per-file download limit in bytes: 100 MiB when it is not configured.
func (c AgentHTTPConfig) MaxDownloadBytes() int64 {
	if c.MaxDownloadSize > 0 {
		return c.MaxDownloadSize << 20
	}
	return 100 << 20
}

// StorageConfig is the storage driver configuration.
type StorageConfig struct {
	Driver string   `yaml:"driver"` // local / nfs / s3
	S3     S3Config `yaml:"s3"`
}

// S3Config is the S3 connection configuration.
type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"` // empty = the official AWS endpoint; non-empty = self-hosted / minio compatible
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

// LiteLLMConfig is the LiteLLM Proxy gateway configuration.
type LiteLLMConfig struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	MasterKey string `yaml:"master_key"`
	Database  string `yaml:"database"` // LiteLLM's own Postgres database name (such as litellm), used to query its registry directly
}

// JWTConfig is the JWT configuration.
type JWTConfig struct {
	SecretKey   string `yaml:"secret_key"`
	TokenExpiry int    `yaml:"token_expiry"`
	Issuer      string `yaml:"issuer"`
}

// SecurityConfig is the static encryption setting for sensitive configuration values (see shared/secureval).
//
// It only covers "configuration values stored in the database": currently the value of kind=secret rows in
// {schema}.global_vars.
// It takes no part in login authentication; rotating it only makes already-encrypted values undecryptable and never
// invalidates a token — which is why it is kept separate from jwt.secret_key.
type SecurityConfig struct {
	// ValueKey is the encryption key. Empty = derived from jwt.secret_key (works out of the box,
	// but changing jwt.secret_key makes every already-encrypted value undecryptable).
	ValueKey string `yaml:"value_key"`
}

// ServerConfig is the server configuration.
type ServerConfig struct {
	Name         string `yaml:"name"`
	Port         int    `yaml:"port"`
	Host         string `yaml:"host"`
	ReadTimeout  int    `yaml:"read_timeout"`
	WriteTimeout int    `yaml:"write_timeout"`
	StaticRoot   string `yaml:"static_root"`
}

// DatabaseConfig is the database configuration.
type DatabaseConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	// PlatformSchema is the platform / admin schema name. System-level shared data (LLM provider presets, the tenant
	// registry, platform admin accounts and so on) is stored there, apart from the tenant schemas (t0/t1/...).
	PlatformSchema string `yaml:"platform_schema"`
}

// LoggingConfig is the logging configuration.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// RegistryConfig is the service registry configuration.
type RegistryConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Type        string   `yaml:"type"`
	Address     string   `yaml:"address"`
	ServiceName string   `yaml:"service_name"`
	ServiceAddr string   `yaml:"service_addr"`
	ServicePort int      `yaml:"service_port"`
	Tags        []string `yaml:"tags"`
}

// RedisConfig is the Redis configuration.
//
// There is no enabled switch: Redis is a hard dependency — the process connects at startup and retries every 5s while it
// cannot, and during that time checkReadiness reports not ready and the outside gets a 503 throughout.
// The old redis.enabled made one configuration map onto two feature sets — with it off the cache still fell back to the
// DB and quotas still counted in PG, so everything looked fine, while the run ledger held only this process and a
// cross-instance stop was never broadcast (runledger.BroadcastStop simply returned false). Such a misconfiguration only
// shows up with several replicas, which is not worth keeping.
type RedisConfig struct {
	Mode     string   `yaml:"mode"`
	Addrs    []string `yaml:"addrs"`
	Password string   `yaml:"password"`
	DB       int      `yaml:"db"`
	PoolSize int      `yaml:"pool_size"`
	Timeout  int      `yaml:"timeout"`
	Prefix   string   `yaml:"prefix"`
}

// Cfg is the global configuration instance.
var Cfg *Config

// AgentRootDir returns the agent storage root; an unloaded configuration returns an empty string.
//
// Why a nil-safe entry point is needed: dereferencing config.Cfg directly would panic at construction time in
// "construct only, never execute" unit tests (such as a Catalog test that only checks a directory tree). The empty string
// is not a default value — the consumer (checkRoot in shared/toolfile) reports "storage root is not initialised" outright,
// rather than quietly reading and writing under the working directory and conjuring a fake directory tree.
func AgentRootDir() string {
	if Cfg == nil {
		return ""
	}
	return Cfg.Agent.RootDir
}

// envPattern matches ${VAR} style environment variable placeholders.
var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv expands the ${VAR} form only and leaves every other $ character alone.
func expandEnv(data []byte) []byte {
	return envPattern.ReplaceAllFunc(data, func(m []byte) []byte {
		key := string(m[2 : len(m)-1]) // strip ${ and }
		return []byte(os.Getenv(key))
	})
}

// LoadConfig loads the configuration from a file.
func LoadConfig(filePath string) (*Config, error) {
	// Read the config file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand the ${VAR} environment placeholders
	data = expandEnv(data)

	// Decode the YAML
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	setDefaultConfig(&config)
	if err := validateJWT(&config); err != nil {
		return nil, err
	}
	if err := validateSecurity(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// setDefaultConfig applies the defaults.
func setDefaultConfig(config *Config) {
	if config.Server.Name == "" {
		config.Server.Name = "nexus"
	}
	if config.Server.Port == 0 {
		config.Server.Port = 22205
	}
	if config.Server.Host == "" {
		config.Server.Host = "0.0.0.0"
	}
	if config.Server.ReadTimeout == 0 {
		config.Server.ReadTimeout = 30
	}
	if config.Server.WriteTimeout == 0 {
		config.Server.WriteTimeout = 30
	}
	if config.Server.StaticRoot == "" {
		config.Server.StaticRoot = "../../static"
	}

	if config.Database.Type == "" {
		config.Database.Type = "postgres"
	}
	if config.Database.Host == "" {
		config.Database.Host = "localhost"
	}
	if config.Database.Port == 0 {
		config.Database.Port = 5432
	}
	if config.Database.Username == "" {
		config.Database.Username = "postgres"
	}
	if config.Database.Password == "" {
		config.Database.Password = "pg"
	}
	if config.Database.Database == "" {
		config.Database.Database = ""
	}
	if config.Database.PlatformSchema == "" {
		config.Database.PlatformSchema = "platform"
	}

	if config.Logging.Level == "" {
		config.Logging.Level = "info"
	}
	if config.Logging.Format == "" {
		config.Logging.Format = "text"
	}

	if config.Registry.Type == "" {
		config.Registry.Type = "consul"
	}
	if config.Registry.Address == "" {
		config.Registry.Address = "localhost:8500"
	}
	if config.Registry.ServiceName == "" {
		config.Registry.ServiceName = config.Server.Name
	}
	if config.Registry.ServiceAddr == "" {
		config.Registry.ServiceAddr = "http://localhost"
	}
	if config.Registry.ServicePort == 0 {
		config.Registry.ServicePort = config.Server.Port
	}

	// Redis defaults
	// Redis is enabled by default
	if config.Redis.Mode == "" {
		config.Redis.Mode = "single"
	}
	if len(config.Redis.Addrs) == 0 {
		config.Redis.Addrs = []string{"localhost:6379"}
	}
	if config.Redis.PoolSize == 0 {
		config.Redis.PoolSize = 10
	}
	if config.Redis.Timeout == 0 {
		config.Redis.Timeout = 1
	}

	// Note: jwt.secret_key is no longer given a fallback — empty / default / placeholder / too short all refuse to start
	// (see validateJWT). Otherwise it would silently fall back to the public default key, meaning anyone could forge a
	// token and name any tenant.
	if config.JWT.TokenExpiry == 0 {
		config.JWT.TokenExpiry = 24 * 60 * 60 // minutes
	}
	if config.JWT.Issuer == "" {
		config.JWT.Issuer = "nexus"
	}

	// The default timeout of Script moved out of the local configuration (see the CodeLangConfig comment): when a node has
	// no timeout control, the generic fallback chain applies (Instance.Timeout -> flow_configs.max_timeout -> the code
	// fallback 300).

	// Agent storage defaults: an empty driver means local
	if config.Agent.Storage.Driver == "" {
		config.Agent.Storage.Driver = "local"
	}
}

// validateJWT forces the JWT key to be configured explicitly: empty / placeholder / built-in default / too short all
// refuse to start.
//
// Reason: HS256 is a symmetric signature, so once the key is guessable a token can be forged, and the claims carry the
// schema — meaning one could skip login and operate any tenant's admin surface directly.
//
// Two forms are supported, both expanded before YAML parsing (see expandEnv):
//   - a literal random string:  secret_key: "xxxxx"
//   - an injected environment variable: secret_key: "${JWT_SECRET}" (expanded to an empty string when unset -> refused
//     here)
func validateJWT(c *Config) error {
	key := c.JWT.SecretKey
	switch {
	case key == "":
		return fmt.Errorf("jwt.secret_key is empty: set a random key, or make sure an environment variable such as ${JWT_SECRET} is injected")
	case strings.Contains(strings.ToUpper(key), "CHANGE_ME"):
		return fmt.Errorf("jwt.secret_key is still a placeholder; replace it with a random key (openssl rand -base64 48)")
	case key == "your-secret-key" || key == "defaultSecretKey":
		return fmt.Errorf("jwt.secret_key is still the built-in default and must be replaced with a random key")
	case len(key) < 32:
		return fmt.Errorf("jwt.secret_key is too short (%d characters); use a random value of at least 32 characters", len(key))
	}
	return nil
}

// validateSecurity only checks the case where security.value_key is **configured explicitly**.
//
// Empty is a valid configuration: it is then derived from jwt.secret_key, the behaviour of this file before the setting
// existed, so existing deployments do not fail to start over a new optional setting.
// Once it is filled in, however, it must not be weak — it directly decides the encryption strength of secret-class
// configuration values.
func validateSecurity(c *Config) error {
	key := strings.TrimSpace(c.Security.ValueKey)
	if key == "" {
		return nil
	}
	switch {
	case strings.Contains(strings.ToUpper(key), "CHANGE_ME"):
		return fmt.Errorf("security.value_key is still a placeholder; replace it with a random key (openssl rand -base64 48)")
	case len(key) < 32:
		return fmt.Errorf("security.value_key is too short (%d characters); use a random value of at least 32 characters", len(key))
	}
	return nil
}

// GetDSN returns the database connection string.
func (c *DatabaseConfig) GetDSN() string {
	switch c.Type {
	case "postgres":
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai application_name=nexus_service",
			c.Host, c.Port, c.Username, c.Password, c.Database)
	case "mysql":
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			c.Username, c.Password, c.Host, c.Port, c.Database)
	default:
		return ""
	}
}
