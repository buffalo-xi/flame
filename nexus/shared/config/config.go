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

// Config 表示服务配置
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

// PluginConfig 磁盘插件（{root_dir}/plugins/<kind>/<tool>/<impl>/plugin.py）的宿主配置。
//
// 这里只有「宿主级」参数：插件目录在哪、用哪个解释器、超时与刷新间隔。
// 每个租户用哪个实现、key 是多少，属于租户级配置（DB plugins 表），不在这里。
type PluginConfig struct {
	Dir             string `yaml:"dir"`              // 空 = {agent.root_dir}/plugins；也可写绝对路径
	Python          string `yaml:"python"`           // 解释器命令；空 = python（Windows 上通常是 python）
	Timeout         int    `yaml:"timeout"`          // 单次调用超时（秒）；0 = 30
	RefreshInterval int    `yaml:"refresh_interval"` // manifest 刷新间隔（秒）；0 = 60

	// 调用额度不在这里：它是「租户 × 插件」的平台策略，存在
	// {platform_schema}.tenant_plugins（见 shared/pluginpolicy 与
	// platform/sql/tenant_plugins.sql）。放在配置文件里会变成全局一个数，
	// 既没法按租户给，也挡不住租户给自己加额度。
}

// DirOrDefault 返回插件根目录：dir 为空时取 {root_dir}/plugins。
//
// rootDir 由调用方传入（config.Agent.RootDir），不在这里读全局 Cfg —— 免得
// 「config 包读全局配置」这种循环依赖式的写法。
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

// TimeoutDuration 单次调用超时；未配置时 30s。
func (c PluginConfig) TimeoutDuration() time.Duration {
	if c.Timeout > 0 {
		return time.Duration(c.Timeout) * time.Second
	}
	return 30 * time.Second
}

// RefreshIntervalDuration manifest 刷新间隔；未配置时 60s。
func (c PluginConfig) RefreshIntervalDuration() time.Duration {
	if c.RefreshInterval > 0 {
		return time.Duration(c.RefreshInterval) * time.Second
	}
	return time.Minute
}

// 沙箱端点路径（与 sandboxd 的路由一致，见 sandboxd/internal/daemon/server.go）。
const (
	// sandboxExecPath 单工端点：一次性命令执行（argv 由调用方给全）。
	sandboxExecPath = "/v1/exec"
	// sandboxCodePath 托管式代码片段端点：调用方给「语言 + 代码片段 + inputs」，
	// 服务端按包装模板织成完整程序、托管解释器、返回结构化结果。
	//
	// 2026-10 由 /v1/script 更名：exec 通道同样在执行脚本文件（.py/.js/.lua/.sh），
	// 用 script 命名等于把两条通道共有的性质当成其中一条的名字。契约上的真正差别是
	// 「解释器与输入输出协议由谁定」，那正是 code 表达的意思。
	sandboxCodePath = "/v1/code"
	// legacySandboxScriptPath 旧端点，**只用于剥壳**（配置里可能写了完整的旧端点）。
	// 不作请求路径：两端同仓同模块，路由不存在新旧并存。
	legacySandboxScriptPath = "/v1/script"
	// sandboxInteractivePath 长交互端点：持久进程，跨连接存活（shell 工具）。
	sandboxInteractivePath = "/v1/interactive"
	// sandboxCapabilitiesPath 能力快照端点。
	//
	// ⚠️ 这是**普通 HTTP**（不是 WebSocket）：返回 sandboxd 的隔离方式、网络声明
	// 与启动时探测到的沙箱环境，由 nexus 拉取后注入 agent 提示词。
	sandboxCapabilitiesPath = "/v1/capabilities"
)

// SandboxConfig 沙箱服务连接配置。
//
// nexus 侧不做隔离、不持有池：所有执行都委托给独立的 sandboxd 服务。
// 隔离方式（none / nsjail / docker）、资源上限、worker 池策略，
// 全部由 sandboxd 自己的配置决定 —— 业务侧只负责连上它并提交请求。
//
// 这里只给「基地址」而不是两个完整 URL：单工 / 双工是**同一台 sandboxd 上的
// 两条路由**，各自配一份 host:port 会允许配成两个不同实例，而 mode / limits /
// allowed_mount_roots / token 都是单实例级的 —— 错配后表现为「exec 能跑、
// 工作流 code 节点报挂载越权或繁忙」，极难定位。路径由本类型按用途拼。
type SandboxConfig struct {
	// URL 沙箱服务基地址（不含路径），形如 ws://sandboxd:8080、wss://sandboxd:443。
	URL string `yaml:"url"`
	// Token 可选：作为 Bearer 令牌随握手发送，需与 sandboxd 侧一致。
	Token string `yaml:"token"`
}

// ExecEndpoint 单工端点（agent 的 exec / run_skill_script 工具）。
// URL 未配置时返回空串，由调用方判定是否可用。
func (c SandboxConfig) ExecEndpoint() string { return c.endpoint(sandboxExecPath) }

// CodeEndpoint 代码片段端点（工作流 code / 分支 / 循环 / binding）。
// URL 未配置时返回空串，由调用方判定是否可用。
func (c SandboxConfig) CodeEndpoint() string { return c.endpoint(sandboxCodePath) }

// InteractiveEndpoint 长交互端点（agent 的 shell 工具）：持久进程，跨连接存活。
// URL 未配置时返回空串，由调用方判定是否可用。
func (c SandboxConfig) InteractiveEndpoint() string { return c.endpoint(sandboxInteractivePath) }

// CapabilitiesEndpoint 能力快照端点（HTTP）：沙箱能否联网 + 沙箱里有什么。
//
// ⚠️ 与上面两条不同，这是普通 HTTP 而不是 WebSocket —— sandbox.url 写的是 ws:// 基地址，
// 这里必须把 scheme 换算成 http（wss → https），否则 net/http 会直接拒绝这个 URL。
// URL 未配置时返回空串，由调用方判定是否可用。
func (c SandboxConfig) CapabilitiesEndpoint() string {
	switch u := c.endpoint(sandboxCapabilitiesPath); {
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	default:
		return u // 已经是 http(s)，或未配置（空串）
	}
}

// endpoint 拼出「基地址 + 用途路径」。
//
// 顺带兼容「把完整端点写进 url」的写法（如 ws://h/v1/exec）：先剥掉结尾的已知
// 端点路径再拼，免得出 /v1/exec/v1/exec。空值原样返回空串，不产生 "/v1/exec" 这种半截地址。
//
// ★ 剥壳列表本次多一项 legacySandboxScriptPath：端点路径在 2026-10 由 /v1/script
// 改为 /v1/code，而**端点可以写进 config.yaml**（落在编译期外）—— 把完整旧端点写进
// url 的部署确实存在，不剥就会拼出 /v1/script/v1/code。它只用于剥壳，不产生路由兼容：
// 请求永远打到 /v1/code。路由本身仍是编译期内的东西，两端同仓同模块，build 一次就同步。
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

// AgentConfig agent 存储相关配置
type AgentConfig struct {
	RootDir string          `yaml:"root_dir"`
	Storage StorageConfig   `yaml:"storage"`
	HTTP    AgentHTTPConfig `yaml:"http"`
}

// AgentHTTPConfig 工作区文件「下载」的交付方式（只影响下载，不影响预览与上传）。
type AgentHTTPConfig struct {
	// XAccelPrefix nginx 内部路径前缀（如 /__files）。配置后下载改由 nginx 从磁盘直接发送：
	// nexus 只回一个 X-Accel-Redirect 头，不占应用带宽、不占 goroutine，且天然支持 Range。
	// 为空（默认）则退回 http.ServeContent，由本进程发送。
	XAccelPrefix string `yaml:"x_accel_prefix"`

	// PresignDownload 开启后，S3 存储的下载直接 302 到预签名 URL，浏览器连对象存储取字节
	// （天然支持 Range / 断点续传，nexus 不占带宽）。
	//
	// 默认 false，且这是刻意的：它是对「本部署」的断言，不是性能优化选项 ——
	//   - 内网 MinIO 常常「nexus 通、浏览器不通」；
	//   - 某些 S3 兼容实现会忽略 response-content-disposition（下载名照旧乱码）；
	//   - 桶策略只允许经 CDN 访问时，预签名直连源站会 403。
	// 断言错了的代价是下载 400，不是降速。即使 driver 是 s3，也能关掉它走 nexus 转发
	// （行为与加这个功能之前完全一致）。
	PresignDownload bool `yaml:"presign_download"`

	// PresignProbeKey 让上面的断言可被自动求值：配一个 bucket 里真实存在的小对象（0 字节最好），
	// 首次下载前先签一次 URL 并真的 GET 一下，检查响应头是否被覆盖。通过才用预签名，
	// 不通过自动降级（冷却 10 分钟后重试）。留空 = 不做探针，直接信任 PresignDownload。
	// 注意：必须配「存在」的对象 —— 对不存在的 key 只会拿到错误 XML，验不出覆盖头。
	PresignProbeKey string `yaml:"presign_probe_key"`

	// PresignTTL 预签名链接有效期（秒），默认 300：够浏览器接住 302，又不至于链接长期外泄。
	PresignTTL int `yaml:"presign_ttl"`
	// MaxDownloadSize 单文件下载上限（MiB），默认 100。
	MaxDownloadSize int64 `yaml:"max_download_size"`
}

// PresignTTLDuration 预签名有效期；未配置时 5 分钟。
func (c AgentHTTPConfig) PresignTTLDuration() time.Duration {
	if c.PresignTTL > 0 {
		return time.Duration(c.PresignTTL) * time.Second
	}
	return 5 * time.Minute
}

// MaxDownloadBytes 单文件下载上限（字节）；未配置时 100 MiB。
func (c AgentHTTPConfig) MaxDownloadBytes() int64 {
	if c.MaxDownloadSize > 0 {
		return c.MaxDownloadSize << 20
	}
	return 100 << 20
}

// StorageConfig 存储驱动配置
type StorageConfig struct {
	Driver string   `yaml:"driver"` // local / nfs / s3
	S3     S3Config `yaml:"s3"`
}

// S3Config S3 连接配置
type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"` // 空=走 AWS 官方；非空=自建/minio 兼容
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

// LiteLLMConfig 表示 LiteLLM Proxy 网关配置
type LiteLLMConfig struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	MasterKey string `yaml:"master_key"`
	Database  string `yaml:"database"` // LiteLLM 自身的 Postgres 库名（如 litellm），用于直接查注册表
}

// JWTConfig 表示JWT配置
type JWTConfig struct {
	SecretKey   string `yaml:"secret_key"`
	TokenExpiry int    `yaml:"token_expiry"`
	Issuer      string `yaml:"issuer"`
}

// SecurityConfig 敏感配置值的静态加密设置（见 shared/secureval）。
//
// 只管「落库的配置值」：目前是 {schema}.global_vars 里 kind=secret 的 value。
// 它不参与登录鉴权，轮换它只会让已加密的值解不开，不会让 token 失效 ——
// 这是与 jwt.secret_key 拆开的原因。
type SecurityConfig struct {
	// ValueKey 加密密钥。留空 = 从 jwt.secret_key 派生（开箱可用，
	// 但换 jwt.secret_key 会让已加密的值全部解不开）。
	ValueKey string `yaml:"value_key"`
}

// ServerConfig 表示服务器配置
type ServerConfig struct {
	Name         string `yaml:"name"`
	Port         int    `yaml:"port"`
	Host         string `yaml:"host"`
	ReadTimeout  int    `yaml:"read_timeout"`
	WriteTimeout int    `yaml:"write_timeout"`
	StaticRoot   string `yaml:"static_root"`
}

// DatabaseConfig 表示数据库配置
type DatabaseConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	// PlatformSchema 平台/管理 schema 名。系统级共享数据（如 LLM 服务商预设、
	// 租户注册表、平台管理员账号等）存放于此 schema，与租户 schema（t0/t1/...）区分。
	PlatformSchema string `yaml:"platform_schema"`
}

// LoggingConfig 表示日志配置
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// RegistryConfig 表示服务注册配置
type RegistryConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Type        string   `yaml:"type"`
	Address     string   `yaml:"address"`
	ServiceName string   `yaml:"service_name"`
	ServiceAddr string   `yaml:"service_addr"`
	ServicePort int      `yaml:"service_port"`
	Tags        []string `yaml:"tags"`
}

// RedisConfig 表示Redis配置。
//
// 没有 enabled 开关：Redis 是必须依赖，进程起来就建连、连不上就每 5s 重试，
// 且未连通期间 checkReadiness 报不就绪、对外一律 503。
// 原先的 redis.enabled 会让同一种配置对应两套功能集 —— 关掉后缓存仍能回源 DB、
// 配额仍能走 PG 计数，看起来一切正常，但运行台账只剩本进程、跨实例停止广播不出去
// （runledger.BroadcastStop 直接返回 false）。这种配错只在多副本时才暴露，不值得留。
type RedisConfig struct {
	Mode     string   `yaml:"mode"`
	Addrs    []string `yaml:"addrs"`
	Password string   `yaml:"password"`
	DB       int      `yaml:"db"`
	PoolSize int      `yaml:"pool_size"`
	Timeout  int      `yaml:"timeout"`
	Prefix   string   `yaml:"prefix"`
}

// Cfg 全局配置实例
var Cfg *Config

// AgentRootDir 返回 agent 存储根；配置未加载时返回空串。
//
// 之所以要这个 nil-safe 的入口：直接解引用 config.Cfg 会让「只构造、不执行」的
// 单测（比如只验目录树的 Catalog 测试）在构造期就 panic。空串不是默认值 ——
// 使用方（shared/toolfile 的 checkRoot）会明确报「存储根未初始化」，
// 而不是悄悄在工作目录下读写，造出一棵假目录树。
func AgentRootDir() string {
	if Cfg == nil {
		return ""
	}
	return Cfg.Agent.RootDir
}

// envPattern 匹配 ${VAR} 形式的环境变量占位符
var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv 仅展开 ${VAR} 形式，不触碰其它 $ 字符
func expandEnv(data []byte) []byte {
	return envPattern.ReplaceAllFunc(data, func(m []byte) []byte {
		key := string(m[2 : len(m)-1]) // 去掉 ${ 和 }
		return []byte(os.Getenv(key))
	})
}

// LoadConfig 从文件加载配置
func LoadConfig(filePath string) (*Config, error) {
	// 读取配置文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 展开 ${VAR} 环境变量占位符
	data = expandEnv(data)

	// 解析YAML
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

// setDefaultConfig 设置默认配置
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

	// Redis 默认配置
	// 默认启用 Redis
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

	// 注意：不再为 jwt.secret_key 兜底 —— 空值/默认值/占位符/过短一律拒绝启动（见 validateJWT）。
	// 否则会静默回退到公开的默认密钥，等于任何人可伪造 token 并指定任意租户。
	if config.JWT.TokenExpiry == 0 {
		config.JWT.TokenExpiry = 24 * 60 * 60 // 分钟
	}
	if config.JWT.Issuer == "" {
		config.JWT.Issuer = "nexus"
	}

	// Script 的缺省超时已移出本地配置（见 CodeLangConfig 注释）：节点未设超时控制时
	// 走通用兜底链（Instance.Timeout → flow_configs.max_timeout → 代码兜底 300）。

	// Agent 存储默认配置：driver 为空时默认 local
	if config.Agent.Storage.Driver == "" {
		config.Agent.Storage.Driver = "local"
	}
}

// validateJWT 强制 JWT 密钥显式配置：空值 / 占位符 / 内置默认值 / 过短都拒绝启动。
//
// 原因：HS256 是对称签名，密钥一旦可猜即可自签 token，而 claims 里带 schema，
// 意味着可以越过登录直接操作任意租户的后台。
//
// 支持两种写法，展开在 YAML 解析之前完成（见 expandEnv）：
//   - 直接写随机字符串： secret_key: "xxxxx"
//   - 环境变量注入：     secret_key: "${JWT_SECRET}"（未设置则展开为空串 → 此处拒绝启动）
func validateJWT(c *Config) error {
	key := c.JWT.SecretKey
	switch {
	case key == "":
		return fmt.Errorf("jwt.secret_key 为空：请填入随机密钥，或确认 ${JWT_SECRET} 这类环境变量已注入")
	case strings.Contains(strings.ToUpper(key), "CHANGE_ME"):
		return fmt.Errorf("jwt.secret_key 仍是占位符，请替换为随机密钥（openssl rand -base64 48）")
	case key == "your-secret-key" || key == "defaultSecretKey":
		return fmt.Errorf("jwt.secret_key 仍是内置默认值，必须替换为随机密钥")
	case len(key) < 32:
		return fmt.Errorf("jwt.secret_key 过短（%d 字符），至少 32 字符的随机值", len(key))
	}
	return nil
}

// validateSecurity 只校验「显式配置了 security.value_key」的情况。
//
// 留空是合法配置：此时按 jwt.secret_key 派生，与本文件之前的版本行为一致，
// 不让存量部署因为新增一段可选配置而起不来。
// 但一旦显式填了，就不能是弱值 —— 它直接决定 secret 类配置值的加密强度。
func validateSecurity(c *Config) error {
	key := strings.TrimSpace(c.Security.ValueKey)
	if key == "" {
		return nil
	}
	switch {
	case strings.Contains(strings.ToUpper(key), "CHANGE_ME"):
		return fmt.Errorf("security.value_key 仍是占位符，请替换为随机密钥（openssl rand -base64 48）")
	case len(key) < 32:
		return fmt.Errorf("security.value_key 过短（%d 字符），至少 32 字符的随机值", len(key))
	}
	return nil
}

// GetDSN 获取数据库连接字符串
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
