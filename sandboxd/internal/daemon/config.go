package daemon

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"

	"gopkg.in/yaml.v3"
)

// 交互明细日志策略（code_log 配置项）。
//
// 明细的内容是「某次调用走了哪个容器、是否复用、花了多久、容器有没有被丢弃」——
// 容器身份只在 sandboxd 侧存在（nexus 拿不到），script 是两边对账的 join key。
const (
	// CodeLogOff 不记交互明细。异常会话（不可归还）不受此开关限制，仍会记录。
	CodeLogOff = "off"
	// CodeLogSlow 只记耗时 >= code_log_slow_ms 的会话（缺省，生产建议）。
	CodeLogSlow = "slow"
	// CodeLogAll 每次会话都记（排障用）。
	//
	// ⚠️ lua 是 µs 级高频调用（循环条件 / 分支条件 / binding transform），
	// 常态开 all 会把日志写成噪声，只适合短期排障。
	CodeLogAll = "all"
)

// 网络声明档位（顶层 network）。
//
// ⚠️ 这不是开关，而是**对「启动方式」的断言**：沙箱能否联网由隔离实现的启动参数
// 决定（docker.network / nsjail.disable_network / mode 本身），这里只是把它如实说出来，
// 好让 nexus 转达给 agent。启动时会校验「断言与事实一致」，对不上就拒绝启动 ——
// 对 agent 撒谎比配错更糟：它会按错误的前提去写脚本（以为断网却写 pip install，
// 或以为能联网却把外发失败归因成代码 bug）。
const (
	// NetworkOff 沙箱内不能联网（部署级硬闸：请求要出网也拒绝）。
	NetworkOff = "off"
	// NetworkOn 沙箱内能联网（不看请求，一律出网）。
	NetworkOn = "on"
	// NetworkOnRequest 按请求裁决：缺省断网，单工请求带 egress 才出网。
	//
	// 只对 /v1/exec 有意义 —— 它是唯一「每次新建进程」的通道，网络参数能在
	// 每次启动时决定。双工（script / interactive）复用建好的隔离单元，网络在
	// 建单元时就写死了，所以它们只认 off / on，传 on-request 会在校验时报错。
	NetworkOnRequest = "on-request"
)

// Config sandboxd 配置。
//
// 这里是「沙箱资源策略」的唯一来源：隔离方式、资源上限、进程池与并发额度。
// nexus 侧不下发这些，只给出语言名与（可选的）超时。
type Config struct {
	Listen string `yaml:"listen"`
	// Token 与 nexus 侧的 sandbox.token 一致；留空表示不校验（仅限内网）。
	Token string `yaml:"token"`
	// LogDir 日志目录。
	LogDir string `yaml:"log_dir"`
	// CodeLog 交互明细日志策略：off / slow / all（见 CodeLogXxx）。
	//
	// 为什么需要它：明细是「一次调用走了哪」的唯一线索，但高频调用下全量记录
	// 只会把日志淹掉，所以缺省只记慢的，异常始终记录。
	CodeLog string `yaml:"code_log"`
	// CodeLogSlowMS slow 策略下的阈值（毫秒）。
	CodeLogSlowMS int `yaml:"code_log_slow_ms"`

	// Mode 隔离方式：none / nsjail / docker。只在这里决定。
	Mode string `yaml:"mode"`

	// Network 网络策略：沙箱内进程能否访问外网（off / on-request / on，见 NetworkXxx）。
	//
	// 它描述的是**上限**，不是事实：最终这一次能不能联网，是「部署上限 × 租户许可 ×
	// 请求意图」三者 AND 的结果（前两者在 nexus 侧，后者随 /v1/exec 请求下发）。
	//
	// 刻意有三态，而不是布尔：布尔无法同时表达「能不能出网」与「听不听请求」。
	//   off         谁都不能出网（请求要 on 也拒绝）
	//   on-request  缺省断网，请求显式要才出网（唯一能让 exec 按请求裁决的档）
	//   on          一律出网，不看请求
	//
	// ⚠️ 只作用于 /v1/exec（含技能脚本）。/v1/code 是托管代码片段通道、跨租户共享、
	// 且 Frame 里没有租户身份，出网只能全平台生效且无法按租户回退 —— 所以那条通道
	// 恒断网，不提供配置项（见 EffectiveCodeNetwork；mode=none 除外，宿主拦不住）。
	//
	// 零值是 off：安全项必须落在安全那一侧，漏写即全禁。
	Network string `yaml:"network"`
	// Networks 按隔离方式覆盖 Network（key: none / nsjail / docker）。
	//
	// 为什么需要：none 是宿主进程，断网根本做不到 —— 想在本机调试就得如实写
	// networks.none: on。声明 off 而当前 mode 实现不了，启动直接失败（见 validateNetwork）。
	//
	// ⚠️ 值类型必须是 string：yaml.v3 只在解到 bool 时才启用 YAML 1.1 的 on/off 语义，
	// 解到 string 就是字符串。若换成 map[string]any，on 会静默变成 true。
	Networks map[string]string `yaml:"networks"`

	// Probe 沙箱能力探测（见 ProbeConfig）。
	Probe ProbeConfig `yaml:"probe"`
	// Codehost codehost 宿主二进制的定位（none / nsjail / docker 通用）。
	//
	// codehost 是替代 worker.lua/py/js 的常驻宿主：sandboxd 在隔离单元里长驻它，
	// 每次 execute 由它 fork 全新解释器。二进制路径按 mode 解析（none 用宿主路径，
	// nsjail 用 rootfs 内路径），与 langs.<lang> 的 command/commands 同款机制。
	Codehost CodehostConfig `yaml:"codehost"`

	// Browser 浏览器宿主（browserhost）的定位与策略。
	//
	// 浏览器与 shell 是同一类东西：session 级常驻进程。所以它复用
	// /v1/interactive 这条通道（tool_id="browser"），不新增任何 HTTP 路由；
	// 本段只描述「镜像 / rootfs 里那个 browserhost 在哪、能不能有头」。
	Browser BrowserConfig `yaml:"browser"`
	// TmpDir worker 可写目录的宿主根目录：每个 worker 在其下建一个独占子目录，
	// 挂进沙箱充当 /tmp（同时是脚本的 cwd）。
	//
	// 启动时会把这棵目录整棵清空，运行期不做单个删除 —— 见 prepareTmpRoot。
	TmpDir string `yaml:"tmp_dir"`
	// AllowedMountRoots 允许挂载的宿主目录根列表：请求里的 Mounts[].Host
	// 必须落在其中之一之下。空列表表示不校验（启动时打警告）。
	//
	// 这是「策略」而非「领域」：sandboxd 不关心调用方的工作区长什么样，
	// 也不解析它的业务路径，只负责确保被要求挂载的路径落在被授权的范围内。
	// 多个调用方各自加一项即可。
	AllowedMountRoots []string `yaml:"allowed_mount_roots"`

	Nsjail NsjailConfig `yaml:"nsjail"`
	Docker DockerConfig `yaml:"docker"`

	// Limits 全局硬上限：所有请求的最终值都会被 clamp 到它。
	Limits LimitsConfig `yaml:"limits"`

	// Langs 语言定义：解释器入口名的真相，exec（命令 / 技能脚本）与
	// script（工作流脚本）共用。
	//
	// 为什么与 script 同级、而不放在 script 里面：「沙箱里有什么解释器、叫什么名字」
	// 是语言本身的事实，与用途无关；script 只承载「脚本执行特有的池与并发策略」。
	// 放错层级的后果是 exec 没法复用同一份定义 —— nexus 侧只好自己硬编码一份
	// （原 service/agent/tools/skillrun.go 的 interpreterByExt），两边一旦漂移
	// 就会出现「flow 能跑、技能跑不了」，且「镜像里叫什么」这个知识只有本服务有。
	Langs map[string]LangDef `yaml:"langs"`

	// Exec exec（命令执行）用途默认值。exec 永不入池。
	Exec UseConfig `yaml:"exec"`
	// Code code（工作流代码片段，/v1/code）用途默认值，以及各语言的池与并发策略。
	Code CodeConfig `yaml:"code"`

	// CodeMaxSec 一次交互的**总时长**上限（秒），兜底防止连接挂死长期占用 worker。
	//
	// ⚠️ 旧名依次是 session_idle_sec → script_max_sec，前两个名字里的 idle / script
	// 都有误导性：它不是「空闲多久就回收」——计时从 open 帧起算、全程不复位
	// （见 codeDeadline）。它是 code 通道的实际硬闸：调用方下发的超时只能收紧它，
	// 配得比它大等于没配（调用方 下发的超时尤其要注意）。
	CodeMaxSec int `yaml:"code_max_sec"`

	// InteractiveIdleSec 长交互进程的空闲回收上限（秒）。
	//
	// 长交互进程（/v1/interactive）归 session 持有、跨连接存活；空闲超过这个时长
	// （从最近一次调用起算）会被后台 janitor 回收 —— kill 进程、释放容器。它是
	// session 级常驻进程的生命周期兜底，与 code_max_sec（单次交互的硬闸）是两回事。
	InteractiveIdleSec int `yaml:"interactive_idle_sec"`

	// InteractiveEphemeralIdleSec 无 session 的临时交互进程的短空闲回收上限（秒）。
	//
	// 无 session 的调用（一次性 run）没有跨 run 持久的需求：run 内多次调用靠
	// 「间隔远小于该超时」复用进程，run 结束时 nexus 会主动 kill（快路径），
	// 异常漏发（nexus 崩溃等）时由这个短超时兜底回收。缺省 120。
	InteractiveEphemeralIdleSec int `yaml:"interactive_ephemeral_idle_sec"`

	// InteractiveJanitorSec 长交互注册表的巡检间隔（秒），缺省 60。
	//
	// 它本身不是超时，而是「超时的分辨率」：进程要等到 idle 之后的下一次巡检才被
	// 回收，所以实际回收时刻落在 (idle, idle+janitor] 之间。gateway 的粘性 TTL
	// 必须覆盖这个上界：sticky_ttl > idle + 2×janitor。
	//
	// 做成可配并随 /v1/capabilities 上报，是为了让那个不等式在网关侧**可计算**
	// —— 两个值分别在两个进程的配置文件里，网关自己不持有 idle。
	InteractiveJanitorSec int `yaml:"interactive_janitor_sec"`

	// Registry 服务注册（可选）：把本服务注册到 consul/nacos，供 sandboxg 发现。
	// 默认不启用（保持 sandboxd 零外部依赖的现状，单实例直连不需要注册）。
	Registry RegistryConfig `yaml:"registry"`

	// 以下是改名前的旧键：只用于回填 + 告警，不再出现在文档与 config.yaml 里。
	//
	// 为什么保留字段而不是直接删：线上配置是编译期外的东西，字段一删，旧值会被
	// applyDefaults 静默补成缺省 —— 而缺省恰好「看起来正常」（code_log 缺省就是
	// slow），配过 off / all 的人只会发现日志行为莫名变了。见 applyLegacyKeys。
	//
	// 改名链有三层：session（最旧）→ interact（旧）→ script（较新）→ code（当前）。
	// 回填从新到旧依次判断，多层都写时以更「新」的那层为准并明确告警旧层被忽略。
	LegacyScript            CodeConfig `yaml:"script"`
	LegacyScriptLog         string     `yaml:"script_log"`
	LegacyScriptLogSlowMS   int        `yaml:"script_log_slow_ms"`
	LegacyScriptMaxSec      int        `yaml:"script_max_sec"`
	LegacyInteract          CodeConfig `yaml:"interact"`
	LegacyInteractLog       string     `yaml:"interact_log"`
	LegacyInteractLogSlowMS int        `yaml:"interact_log_slow_ms"`
	LegacyInteractMaxSec    int        `yaml:"interact_max_sec"`
	LegacySession           CodeConfig `yaml:"session"`
	LegacySessionLog        string     `yaml:"session_log"`
	LegacySessionLogSlowMS  int        `yaml:"session_log_slow_ms"`
	LegacySessionMaxSec     int        `yaml:"session_idle_sec"`
	// LegacyAcquireTimeoutSec 顶层 acquire_timeout_sec（→ code.acquire_timeout_sec）。
	// 它只服务 code 通道，原先放顶层与 exec 配置混在一起，现已归位到 code 段。
	LegacyAcquireTimeoutSec int `yaml:"acquire_timeout_sec"`

	// Warnings 解析期的提醒（目前只有旧键回填），由调用方打印。不参与 YAML 解析。
	Warnings []string `yaml:"-"`
}

// RegistryConfig 服务注册配置（把本服务注册到注册中心，供 gateway 发现）。
//
// 为什么可选、默认关闭：sandboxd 单实例直连时不需要注册（nexus 直接连它），
// 只有上 sandboxg 多实例时才有发现需求。默认关闭保住「sandboxd 零外部依赖」。
type RegistryConfig struct {
	Enabled bool `yaml:"enabled"`
	// Type 注册中心类型：consul | nacos。
	Type string `yaml:"type"`
	// Address 注册中心地址（如 localhost:8500）。
	Address string `yaml:"address"`
	// ServiceName 服务名（默认 sandboxd）。
	ServiceName string `yaml:"service_name"`
	// ServiceAddr 对外可达地址（gateway 连这个，跨机写内网 IP，本机写 127.0.0.1）。
	ServiceAddr string `yaml:"service_addr"`
	// ServicePort 对外端口（默认取 listen 的端口）。
	ServicePort int `yaml:"service_port"`
	// HealthCheckPath 健康检查路径（默认 /healthz）。
	HealthCheckPath string `yaml:"health_check_path"`
}

// LimitsConfig 资源上限（0 值表示不额外限制）。
type LimitsConfig struct {
	MemoryMB       int   `yaml:"memory_mb"`
	CPUCount       int   `yaml:"cpu_count"`
	MaxPIDs        int   `yaml:"max_pids"`
	MaxFileMB      int   `yaml:"max_file_mb"`
	TimeoutSec     int   `yaml:"timeout_sec"`
	MaxOutputBytes int64 `yaml:"max_output_bytes"`
}

// NsjailConfig nsjail 隔离参数（Linux）。
type NsjailConfig struct {
	Binary         string   `yaml:"binary"`
	Rootfs         string   `yaml:"rootfs"`
	UID            int      `yaml:"uid"`
	GID            int      `yaml:"gid"`
	DisableNetwork bool     `yaml:"disable_network"`
	ReadOnlyRootfs bool     `yaml:"read_only_rootfs"` // 只读；nsjail chroot 默认即 R/O，置 false 才加 --rw
	CgroupVersion  string   `yaml:"cgroup_version"`   // v1 / v2 / auto
	ExtraArgs      []string `yaml:"extra_args"`
}

// DockerConfig docker 隔离参数。
type DockerConfig struct {
	Binary    string   `yaml:"binary"`
	Image     string   `yaml:"image"`
	Network   string   `yaml:"network"`
	ReadOnly  bool     `yaml:"read_only"`
	User      string   `yaml:"user"`
	ExtraArgs []string `yaml:"extra_args"`
}

// ProbeConfig 沙箱能力探测。
//
// 启动时在沙箱内跑一组独立探针（见 probe.go），结果经 /v1/capabilities 交给 nexus
// 注入给 agent。为什么是探测而不是声明：沙箱里有什么（OS / 语言 / 模块）是**事实**，
// 声明会漂移 —— 改了 Dockerfile 装了新包却忘了改配置，agent 就会按错误的环境写脚本。
type ProbeConfig struct {
	// TimeoutSec 探测整体超时（秒）。探针并发跑，串行最坏会叠加。
	TimeoutSec int `yaml:"timeout_sec"`
	// MaxOutputBytes 单条探针的输出上限（结果会进入 LLM 上下文，必须设限）。
	MaxOutputBytes int64 `yaml:"max_output_bytes"`
}

// UseConfig 用途级默认值。
type UseConfig struct {
	Limits LimitsConfig `yaml:"limits"`
}

// CodeConfig script（工作流脚本）用途配置。
type CodeConfig struct {
	Limits LimitsConfig `yaml:"limits"`
	// AcquireTimeoutSec 取槽位等待上限（秒），本段的**缺省**（未写时 15，见 applyDefaults）：
	// 超出返回「繁忙」，绝不无限阻塞（binding transform 是嵌套调用，无限阻塞会自我死锁）。
	// 这段等待不计入任何超时预算（它发生在 ready 帧之前），只是额外尾延迟。
	//
	// 为什么在 script 段里：这是 script 专属概念 —— exec 永不池化、根本没有"取槽"
	// 这回事。各语言可用 code.pools.<lang>.acquire_timeout_sec 覆盖它。
	AcquireTimeoutSec int `yaml:"acquire_timeout_sec"`
	// Pools script 的语言池策略：key 为语言名（lua / python / js），
	// 必须在顶层 langs 里有同名定义（解释器入口在那里）。
	//
	// 一个语言条目在 = 该语言启用空闲池并对外服务；不要就把它**整条**注释掉 ——
	// 该语言的 script 请求会收到 unsupported_lang，而不是「悄悄变慢」。
	// 刻意没有「只关空闲池、语言照跑」那一档：它的唯一后果是每次现场冷启动
	// （300ms~1s）的静默性能悬崖。
	Pools map[string]CodeLang `yaml:"pools"`
	// LegacyLangs 改名前的旧键 code.langs（→ code.pools）：只用于回填 + 告警。
	LegacyLangs map[string]CodeLang `yaml:"langs"`
}

// isZero 判断配置段是否「一个字都没写」：旧键回填要区分「旧段有内容」与「空段」。
//
// ⚠️ 必须算上 LegacyLangs：更旧的段 session: {langs: ...} 的内容落在它上面，
// 只看 Pools 会把「有内容的旧段」判成「一个字都没写」→ 段级回填被跳过 → 静默失效。
func (s CodeConfig) isZero() bool {
	return s.Limits == (LimitsConfig{}) && s.AcquireTimeoutSec == 0 &&
		len(s.Pools) == 0 && len(s.LegacyLangs) == 0
}

// LangDef 语言的解释器定义（顶层 langs.<lang>）。
//
// 只描述「这个语言在沙箱里怎么被起起来」，不含任何用途 / 池策略 —— 所以 exec 与
// script 都能用它。按 mode 覆盖的机制见 CommandByMode。
type LangDef struct {
	// Command 解释器入口名（通用缺省，见 CommandByMode）。
	Command string `yaml:"command"`
	// CommandByMode 按隔离方式覆盖 Command（可选，key 为 mode：none / nsjail / docker）。
	//
	// 为什么需要它：command 是「当前 mode 下的可执行入口名」，而三种 mode 的解析
	// 位置完全不同 —— docker 解析镜像内 PATH、nsjail 解析 chroot 内 rootfs、none 直接
	// 在宿主 exec。同一个名字在三种环境里未必都存在（镜像里是 Dockerfile `ln` 出来的
	// lua，rootfs 里可能只有 lua5.4，Windows 宿主上是带版本号的文件名）。
	// 以前这些差异只能写成注释，于是「配置里写的」与「实际生效的」是两回事；
	// 显式建模后由 ResolveCommand 按当前 mode 取值，注释不再承担配置职责。
	CommandByMode map[string]string `yaml:"commands"`
}

// CodehostConfig codehost 宿主二进制的定位（顶层 codehost 段）。
//
// 与 LangDef 同构：binary 是通用缺省，binaries 按 mode 覆盖。codehost 三种
// mode 都跑 —— none 是宿主可解析的路径，nsjail / docker 是 rootfs / 镜像内的路径。
type CodehostConfig struct {
	// Binary 通用缺省（如 "codehost"，取 PATH 解析）。
	Binary string `yaml:"binary"`
	// Binaries 按隔离方式覆盖 Binary（key: none / nsjail / docker）。
	Binaries map[string]string `yaml:"binaries"`
}

// BrowserConfig 浏览器宿主配置（顶层 browser 段）。
//
// 刻意没有「引擎选择」：驱动层固定 CDP（Chromium 家族）。换内核不是改一个字段
// 就能完成的事（Firefox 已移除 CDP，WebKit 只有 Playwright 的定制构建），
// 留着那么多档只会让人以为切过去就能用。
type BrowserConfig struct {
	// Enabled 是否对外提供浏览器工具。缺省 false：浏览器能出网、能读页面，
	// 属于「开了就该知情」的能力，不该靠漏写配置默认获得。
	Enabled bool `yaml:"enabled"`

	// Binary 通用缺省（如 "browserhost"，取 PATH 解析）。
	Binary string `yaml:"binary"`
	// Binaries 按隔离方式覆盖 Binary（key: none / nsjail / docker）。
	Binaries map[string]string `yaml:"binaries"`

	// ExecPath 浏览器可执行文件（**沙箱内**路径）。空 = 由 browserhost 自动探测
	// （chromium / chrome / 宿主上的 Edge）。
	//
	// 注意它和 binaries 是同一个性质：这是「隔离单元里看得见的路径」，
	// none 下是宿主路径，docker 下是镜像内路径 —— 一个字符串不可能同时成立，
	// 所以真正的取值按 mode 走 ExecPaths。
	ExecPath string `yaml:"exec_path"`
	// ExecPaths 按隔离方式覆盖 ExecPath（key: none / nsjail / docker）。
	// 典型用法：docker 写镜像里的 /usr/bin/chromium，none 写宿主上的 Chrome 绝对路径。
	ExecPaths map[string]string `yaml:"exec_paths"`

	// Headed 缺省显示方式：false = 无头（生产建议）。
	//
	// 用「headed」而不是「headless」是为了让零值就是安全值 —— bool 的零值是 false，
	// 写成 headless 的话「没写」与「显式关掉」无法区分。
	Headed bool `yaml:"headed"`
	// AllowHeaded 是否允许请求方按会话指定有头。有头需要真实显示环境，
	// 目前只支持 mode=none（sandboxd 跑在有桌面的机器上）。
	AllowHeaded bool `yaml:"allow_headed"`
	// AllowHeadedModes 按隔离方式覆盖 AllowHeaded（key: none / nsjail / docker）。
	// 命中当前 mode 用这里的值，未命中回退 AllowHeaded —— 与 exec_paths 同款规则。
	AllowHeadedModes map[string]bool `yaml:"allow_headed_modes"`

	// ProfileDir Chrome 的 user-data-dir（沙箱内路径）。
	//
	// 空 = 按 mode 取缺省：docker → /tmp/browser（容器带 --tmpfs /tmp，profile
	// 随容器销毁，不落进只读 rootfs、也不污染工作区）；none / nsjail → 临时目录，
	// 浏览器退出即删。
	ProfileDir string `yaml:"profile_dir"`
	// ProfileDirs 按隔离方式覆盖 ProfileDir（key: none / nsjail / docker）。
	ProfileDirs map[string]string `yaml:"profile_dirs"`
	// DownloadsDir 下载目录（沙箱内路径）。空 = <cwd>/downloads（即工作区）。
	DownloadsDir string `yaml:"downloads_dir"`
	// DownloadsDirs 按隔离方式覆盖 DownloadsDir（key: none / nsjail / docker）。
	//
	// 与 exec_path 同理：这是沙箱内路径，一旦写成绝对路径就只对某个 mode 成立
	// （/workspace 在 none 下会变成宿主根盘的 C:\workspace）。默认值是相对 cwd 的，
	// 天然跨 mode，所以多数部署不用配它。
	DownloadsDirs map[string]string `yaml:"downloads_dirs"`

	// Limits 浏览器进程的资源上限（缺省值 + 全局 clamp）。
	//
	// 为什么单独一段：Chrome 的进程树 + 渲染缓存与 shell 不是一个量级，
	// 拿 exec 的 512MB 去套它会在打开第二个标签时被 OOM 杀掉。
	Limits LimitsConfig `yaml:"limits"`
}

// CodeLang script 特有的语言策略（code.pools.<lang>）：只放「池与并发」，
// 不含解释器入口 —— 那是顶层 langs 的事。
//
// 这个条目存在本身 = 该语言启用空闲池并对外服务（见 CodeConfig.Pools）。
type CodeLang struct {
	// 这里刻意**没有** timeout 字段。
	//
	// 脚本超时由调用方（nexus）逐次在 open 帧里下发，本服务只用一个全局硬闸去
	// clamp（code_max_sec，见 codeDeadline）。曾经有过一个 timeout 字段，
	// 但它既没有消费点、也永远轮不到生效（nexus 每次都下发 ≥30s），
	// 只会让人以为「语言级缺省超时」在这里配 —— 已删除。
	// MaxConcurrent 并发会话上限（含嵌套调用）。
	// 必须 >= 2：binding transform 等场景是嵌套调用，会同时占用两个会话。
	MaxConcurrent int `yaml:"max_concurrent"`
	// AcquireTimeoutSec 取槽位等待上限（秒），0 表示沿用 code.acquire_timeout_sec。
	//
	// 留这一层是因为各语言的 max_concurrent 差得多（32 / 16 / 8）：lua 是 µs 级高频
	// 调用，等待越久越容易拖住工作流，值得单独配短一点。
	AcquireTimeoutSec int `yaml:"acquire_timeout_sec"`
	// Idle 空闲池参数；缺省 {0, 0}（不预热、不留驻）。
	Idle PoolConfig `yaml:"idle"`
	// LegacyPool 改名前的子段 code.pools.<lang>.pool（→ .idle）：只用于回填 + 告警。
	LegacyPool PoolConfig `yaml:"pool"`
}

// LangConfig 某语言在运行期的完整配置：入口名（已按 mode 解析）+ script 池策略。
//
// 由 Config.Languages() 合并顶层 langs（LangDef）与 code.pools（CodeLang）得到，
// 供池使用；它不是 YAML 解析目标，故不带 yaml tag。
//
// 字段刻意平铺而不是嵌入 CodeLang：Go 1.25 的语言版本不允许在结构体字面量里
// 使用「提升字段」（promoted field），平铺才能让 LangConfig{MaxConcurrent: ...}
// 这类构造写法继续可用（池的测试大量这么写）。
type LangConfig struct {
	Command string

	// 无超时字段：脚本超时由请求下发，服务端只有 code_max_sec 这个全局硬闸
	// （见 codeDeadline）。
	MaxConcurrent     int
	AcquireTimeoutSec int
	Idle              PoolConfig
}

// ResolveCommand 取当前 mode 下该语言的解释器入口名：commands[mode] 优先，
// 其次通用缺省 command。
//
// 取不到就报错，而不是退化成空命令 —— 空命令要到池预热时才以
// 「refill ... failed: recv pong: EOF」的面目暴露，排查成本极高。
func (l LangDef) ResolveCommand(mode string) (string, error) {
	if cmd, ok := l.CommandByMode[mode]; ok && cmd != "" {
		return cmd, nil
	}
	if l.Command != "" {
		return l.Command, nil
	}
	if len(l.CommandByMode) == 0 {
		return "", fmt.Errorf("command is not configured (set command, or commands.%s)", mode)
	}
	modes := make([]string, 0, len(l.CommandByMode))
	for m := range l.CommandByMode {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return "", fmt.Errorf("command is not configured for mode %q (commands has: %v)", mode, modes)
}

// PoolConfig 单语言的空闲池参数（code.pools.<lang>.idle）。
//
// 开关不在这个段里 —— 是「整个语言条目在不在」（见 CodeConfig.Pools）：
// 条目在 = 该语言启用空闲池；不要就整条注释掉。
//
// 段内两个值都尊重零值：省略即 0，0 是合法语义（不预热 / 不留驻）。
// 刻意没有 enabled 布尔 —— 「写了 initial: 2 又说不要池」这种自相矛盾状态不该存在。
//
// 判「段里有没有东西」用 == PoolConfig{} 即可（零值含下面的指针，仍然可比）。
type PoolConfig struct {
	// Initial 保底空闲 worker 数：启动时预热，运行期由 refiller 维持。
	// 0 = 不预热（借出时现场启动，归还后仍会入池复用，除非 Max 也是 0）。
	Initial int `yaml:"initial"`
	// Max 空闲池容量上限（借出中的 worker 不占这个额度）。缺省 = Initial。
	// 0 = 不留驻（归还即销毁）。
	Max int `yaml:"max"`
	// LegacyEnabled 改名/改语义前存在的 enabled 布尔：只用于翻译成 Initial/Max
	// 并出声告警，不参与任何运行期判断（见 legacyIdle）。
	LegacyEnabled *bool `yaml:"enabled"`
}

// ToSandbox 转成 sandbox.Limits。
func (l LimitsConfig) ToSandbox() sandbox.Limits {
	out := sandbox.Limits{
		MemoryMB:       l.MemoryMB,
		CPUCount:       l.CPUCount,
		MaxPIDs:        l.MaxPIDs,
		MaxFileMB:      l.MaxFileMB,
		MaxOutputBytes: l.MaxOutputBytes,
	}
	if l.TimeoutSec > 0 {
		out.Timeout = time.Duration(l.TimeoutSec) * time.Second
	}
	return out
}

// expandEnv 展开配置里「定位类」字段的 ${VAR} / $VAR。
//
// 为什么需要：解释器入口、宿主二进制、rootfs / 镜像、挂载根这些在不同机器上完全不同。
// 写死绝对路径的后果是「换台机器就起不来」，而且会把某个人的本机路径随包发出去。
// 有了环境变量，「同一份 config.yaml + 不同环境」才成立。
//
// 为什么只展开这些字段、不对整份 YAML 做全局替换：token / 密码里出现 $ 是合法的，
// 全局展开会把它们悄悄改掉，而且改完还看不出来。
//
// 为什么引用未设置的变量要报错：展开成空串之后，ResolveCommand / ResolveCodehostBinary
// 会把它当成「没配」并静默回退到通用缺省 —— 覆盖没生效却毫无迹象，正是最难查的那类。
func (c *Config) expandEnv() error {
	var firstErr error

	// set 展开单个字段；一旦出错就记下第一个，后面的不再覆盖它。
	set := func(where string, p *string) {
		if firstErr != nil || p == nil || *p == "" {
			return
		}
		out := os.ExpandEnv(*p)
		if out == "" {
			firstErr = fmt.Errorf("%s: %q 引用的环境变量未设置", where, *p)
			return
		}
		*p = out
	}
	// setMap 展开一份「按 mode 覆盖」的表（key 是 mode，值同样不能展开成空）。
	setMap := func(prefix string, m map[string]string) {
		for k, v := range m {
			out := v
			set(prefix+"."+k, &out)
			if firstErr == nil {
				m[k] = out
			}
		}
	}

	set("log_dir", &c.LogDir)
	set("tmp_dir", &c.TmpDir)
	for i := range c.AllowedMountRoots {
		set(fmt.Sprintf("allowed_mount_roots[%d]", i), &c.AllowedMountRoots[i])
	}
	for name, def := range c.Langs {
		set("langs."+name+".command", &def.Command)
		setMap("langs."+name+".commands", def.CommandByMode)
		c.Langs[name] = def
	}
	set("codehost.binary", &c.Codehost.Binary)
	setMap("codehost.binaries", c.Codehost.Binaries)
	set("nsjail.binary", &c.Nsjail.Binary)
	set("nsjail.rootfs", &c.Nsjail.Rootfs)
	set("docker.binary", &c.Docker.Binary)
	set("docker.image", &c.Docker.Image)
	set("browser.binary", &c.Browser.Binary)
	setMap("browser.binaries", c.Browser.Binaries)
	set("browser.exec_path", &c.Browser.ExecPath)
	setMap("browser.exec_paths", c.Browser.ExecPaths)
	set("browser.profile_dir", &c.Browser.ProfileDir)
	setMap("browser.profile_dirs", c.Browser.ProfileDirs)
	set("browser.downloads_dir", &c.Browser.DownloadsDir)
	setMap("browser.downloads_dirs", c.Browser.DownloadsDirs)
	return firstErr
}

// LoadConfig 读取并补全默认值。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sandboxd: read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("sandboxd: parse config %s: %w", path, err)
	}
	// 环境变量展开紧跟解析：早于 applyDefaults，缺省值不参与展开（它们本来也不含 $）；
	// 早于 applyLegacyKeys，旧键回填出来的值也就不必再走一遍。
	if err := c.expandEnv(); err != nil {
		return nil, fmt.Errorf("sandboxd: config %s: %w", path, err)
	}
	// 回填必须早于 applyDefaults：后者会把空值补成缺省，一补就再也分不出
	// 「用户没写」与「用户写的是旧键」了。
	c.applyLegacyKeys()
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("sandboxd: config %s: %w", path, err)
	}
	return &c, nil
}

// validate 校验取值，避免拼错的策略被静默当成缺省值。
func (c *Config) validate() error {
	if err := c.validateNetwork(); err != nil {
		return err
	}
	if err := c.validateBrowser(); err != nil {
		return err
	}
	switch c.Nsjail.CgroupVersion {
	case isolate.CgroupV1, isolate.CgroupV2, isolate.CgroupAuto:
	default:
		return fmt.Errorf("nsjail.cgroup_version: unknown value %q (want %q, %q or %q)",
			c.Nsjail.CgroupVersion, isolate.CgroupV1, isolate.CgroupV2, isolate.CgroupAuto)
	}
	switch c.CodeLog {
	case "", CodeLogOff, CodeLogSlow, CodeLogAll:
	default:
		return fmt.Errorf("unknown code_log %q (want %q, %q or %q)",
			c.CodeLog, CodeLogOff, CodeLogSlow, CodeLogAll)
	}
	for name, def := range c.Langs {
		// commands 的键必须是已知 mode：拼错（如 dockerr）会让 ResolveCommand
		// 静默回退到通用 command，等于覆盖没生效却看不出来。
		for m := range def.CommandByMode {
			switch m {
			case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
			default:
				return fmt.Errorf("langs.%s: unknown mode %q in commands (want %q, %q or %q)",
					name, m, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
			}
		}
	}
	for m := range c.Codehost.Binaries {
		switch m {
		case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
		default:
			return fmt.Errorf("codehost.binaries: unknown mode %q (want %q, %q or %q)",
				m, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
		}
	}
	// 与 commands / codehost.binaries 同理：拼错的 mode 会让覆盖静默不生效，
	// 而这里的后果更隐蔽 —— 退回到通用路径后，浏览器在另一个 mode 下指向一个
	// 不存在的文件，直到第一次 open 才炸。
	for key, modes := range map[string]map[string]string{
		"browser.binaries":       c.Browser.Binaries,
		"browser.exec_paths":     c.Browser.ExecPaths,
		"browser.profile_dirs":   c.Browser.ProfileDirs,
		"browser.downloads_dirs": c.Browser.DownloadsDirs,
	} {
		for mode := range modes {
			switch mode {
			case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
			default:
				return fmt.Errorf("%s: unknown mode %q (want %q, %q or %q)",
					key, mode, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
			}
		}
	}
	// 有头的覆盖表同理：拼错的 mode 会让覆盖静默不生效，本机有头调试莫名变成无头。
	for mode := range c.Browser.AllowHeadedModes {
		switch mode {
		case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
		default:
			return fmt.Errorf("browser.allow_headed_modes: unknown mode %q (want %q, %q or %q)",
				mode, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
		}
	}
	for name, lang := range c.Code.Pools {
		// 双工策略必须挂在已定义的语言上：否则装配时取不到解释器入口。
		if _, ok := c.Langs[name]; !ok {
			return fmt.Errorf("code.pools.%s: no matching definition in top-level langs", name)
		}
		// 保底空闲数不能大于池容量：refiller 会把空闲对象补到 initial，
		// 而容量只有 max 时每次推送都落进 default 分支销毁并返回 ——
		// 结果就是每秒「起一个、杀一个」空转（L2 更贵：每秒建一个容器再销毁）。
		if lang.Idle.Initial > lang.Idle.Max {
			return fmt.Errorf("code.pools.%s.idle: initial %d > max %d — "+
				"refiller 会把空闲对象补到 initial，但池容量只有 max，结果是每秒「起一个、杀一个」空转；"+
				"请把 max 提到 >= initial（如 max: %d），或把 initial 降到 <= max",
				name, lang.Idle.Initial, lang.Idle.Max, lang.Idle.Initial)
		}
	}
	return nil
}

// EffectiveNetwork 当前 mode 下 /v1/exec 生效的网络策略：networks[mode] 优先，否则顶层 network。
//
// 它只是**上限** —— 这一次到底出不出网，还要过租户许可与请求标志（阶段 2 在
// handleExec 里裁决）。capabilities 上报的就是这个值，不是某次调用的事实。
//
// 两层都有缺省（applyDefaults 把 network 补成 off），因此总能取到值 —— 与
// ResolveCommand「取不到就报错」的情形不同，这里不存在「取不到」。
func (c *Config) EffectiveNetwork() string {
	if v, ok := c.Networks[c.Mode]; ok && v != "" {
		return v
	}
	return c.Network
}

// EffectiveCodeNetwork /v1/code 的网络策略：恒为 off，不可配。
//
// 为什么硬编码，而不是加一个 network_script 配置项：
//   - 脚本池按语言全局创建（slotPool），跨租户共享，且 wire.Frame 里没有租户身份 ——
//     sandboxd 根本无从按租户裁决，配成 on 只能「全平台一起开」；
//   - 一旦打开，某个租户的脚本失控（外发数据 / 挖矿 / 当代理）时无法只关它，
//     只能全平台回退，把所有租户的流程一起打掉；
//   - 工作流的出网需求由 http / rpc / mcp 节点承担，那里才有域名白名单、凭据、
//     限流与审计 —— 沙箱层永远做不到这些。
//
// 保留这个函数（而不是在 handler 里散落字面量 "off"），是为了让按通道上报有一个
// 单一来源，将来真要改也只有一个地方。
//
// ⚠️ mode=none 是例外：脚本跑在宿主进程上，没有隔离单元也就没有"断网"这回事，
// 实际行为就是能出网。这里必须如实报 on —— 报 off 就是让 agent 按错误前提写脚本
// （以为连不上而不敢写，或反过来把外发失败当代码 bug）。
func (c *Config) EffectiveCodeNetwork() string {
	if c.Mode == isolate.NoneName {
		return NetworkOn
	}
	return NetworkOff
}

// validateNetwork 校验网络声明的取值，以及它与「启动方式」是否一致。
//
// 判据只有一条：**声明必须与事实相符** —— off 要真禁得住，on 要真放得开。
// 校验完全在配置内部完成（比对 docker.network / nsjail.disable_network / mode），
// 不需要任何运行期探测。
//
// 盲区：docker.extra_args / nsjail.extra_args 是逃生口，塞一个 --network host 就能
// 推翻声明，而程序无从校验 —— 用了逃生口就别指望这里兜住，那属于部署方自觉。
func (c *Config) validateNetwork() error {
	if !validNetworkPolicy(c.Network) {
		return fmt.Errorf("network: unknown policy %q (want %q, %q or %q)",
			c.Network, NetworkOff, NetworkOnRequest, NetworkOn)
	}
	for mode, v := range c.Networks {
		switch mode {
		case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
		default:
			return fmt.Errorf("networks.%s: unknown mode (want %q, %q or %q)",
				mode, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
		}
		if !validNetworkPolicy(v) {
			return fmt.Errorf("networks.%s: unknown policy %q (want %q, %q or %q)",
				mode, v, NetworkOff, NetworkOnRequest, NetworkOn)
		}
	}

	switch c.EffectiveNetwork() {
	case NetworkOn:
		return c.validateNetworkOn()
	case NetworkOnRequest:
		return c.validateNetworkOnRequest()
	default:
		return c.validateNetworkOff()
	}
}

// validNetworkPolicy 三态取值是否合法。
func validNetworkPolicy(v string) bool {
	return v == NetworkOff || v == NetworkOnRequest || v == NetworkOn
}

// validateNetworkOnRequest 声明「按请求裁决」时，当前 mode 必须**真的切得动**。
//
// 判据比 off / on 更严：不是「某一态能到达」，而是「两态都能到达」。
//   - docker：--network none 与 --network <mode> 都得可用，所以 docker.network
//     不能空、也不能是 none —— 否则请求要出网时无网可给。
//   - nsjail：断网靠 clone_newnet（默认开）+ 运行时加 --iface_no_lo；出网必须
//     --disable_clone_newnet（共享宿主 netns）。后者只能写在 extra_args 里，
//     程序无法校验，所以这里只告警 —— 部署方自己保证。
//   - none：宿主进程与外部网络同源，断不了；egress=false 的请求在运行期会被拒绝。
func (c *Config) validateNetworkOnRequest() error {
	switch c.Mode {
	case isolate.DockerName:
		if n := c.Docker.Network; n == "" || n == "none" {
			return fmt.Errorf("network: %q but docker.network is %q — an egress request would have "+
				"no network to switch to; set docker.network to bridge (or another real mode), "+
				"or declare network: %q", NetworkOnRequest, n, NetworkOff)
		}
	case isolate.NsjailName:
		xlog.Error("sandboxd: network=%s with nsjail cannot grant egress without "+
			"--disable_clone_newnet in nsjail.extra_args (not blocking startup); "+
			"egress requests will still get no network", NetworkOnRequest)
	case isolate.NoneName:
		xlog.Error("sandboxd: network=%s with mode=none — the host cannot cut off the network "+
			"(not blocking startup); egress=false requests will be rejected at runtime", NetworkOnRequest)
	}
	return nil
}

// validateNetworkOff 声明「不能联网」时，当前 mode 必须真能断网。
func (c *Config) validateNetworkOff() error {
	switch c.Mode {
	case isolate.DockerName:
		// docker 的断网是 containerArgs 内置的（network: none，空值也补成 none）。
		if n := c.Docker.Network; n != "" && n != "none" {
			return fmt.Errorf("network: %q but docker.network is %q — containers would still reach the network; "+
				"set docker.network: none, or declare network: %q", NetworkOff, n, NetworkOn)
		}
	case isolate.NsjailName:
		if !c.Nsjail.DisableNetwork {
			return fmt.Errorf("network: %q but nsjail.disable_network is false — the jail would keep host networking; "+
				"set nsjail.disable_network: true, or declare network: %q", NetworkOff, NetworkOn)
		}
	default:
		// none：命令由宿主直接执行，与外部网络同源，没有任何手段可断。
		return fmt.Errorf("network: %q cannot be enforced with mode %q — commands run on the host and cannot be cut off from the network; "+
			"to run locally, declare it honestly with networks.none: %q", NetworkOff, c.Mode, NetworkOn)
	}
	return nil
}

// validateNetworkOn 声明「能联网」时，当前 mode 必须真的放得开。
func (c *Config) validateNetworkOn() error {
	switch c.Mode {
	case isolate.DockerName:
		// ⚠️ 空值会被 NewDocker 补成 none —— 声明 on 却没给网络模式，等于自相矛盾。
		n := c.Docker.Network
		if n == "" {
			n = "(unset → none)"
		}
		if c.Docker.Network == "" || c.Docker.Network == "none" {
			return fmt.Errorf("network: %q but docker.network is %s — containers would have no network at all; "+
				"set docker.network to the mode you actually want (bridge / host), or declare network: %q",
				NetworkOn, n, NetworkOff)
		}
	case isolate.NsjailName:
		if c.Nsjail.DisableNetwork {
			return fmt.Errorf("network: %q but nsjail.disable_network is true — the jail would have no network; "+
				"set nsjail.disable_network: false, or declare network: %q", NetworkOn, NetworkOff)
		}
	default:
		// none：宿主进程必然能联网，声明 on 属实。
	}
	return nil
}

// CodeLogEnabled 判断一次耗时 dur 的会话是否要记明细。
//
// 「容器不可归还」的异常会话不走这里 —— 调用方在收尾时强制记录，
// 那是排障时最需要的一类线索，不该被 off 关掉。
func (c *Config) CodeLogEnabled(dur time.Duration) bool {
	switch c.CodeLog {
	case CodeLogOff:
		return false
	case CodeLogAll:
		return true
	default: // slow
		return dur >= time.Duration(c.CodeLogSlowMS)*time.Millisecond
	}
}

// applyLegacyKeys 把改名前的旧键回填到新键上，并记录告警。
//
// ⚠️ 必须在 applyDefaults 之前调用：applyDefaults 会把空值补成缺省，一旦先跑，
// 新键已非空 → 回填判为「新键已写」→ 旧值被丢弃，而且静默 —— code_log 的
// 缺省恰好是 slow，配过 off / all 的人只会发现日志行为莫名变了。
//
// 判据只有「新键空 + 旧键有」这一条。两个都写时以新键为准并明确告警旧键被忽略：
// 不猜「谁更可信」，否则旧键会给人一种「还在生效」的错觉。
//
// 三层从新到旧依次判断：script（2026-10 的旧键）→ interact → session。
func (c *Config) applyLegacyKeys() {
	// 第零层：script → code（2026-10 更名；script 曾是当时的新键，现在成了旧键）。
	// 必须排在最前：它离当前最近，优先级最高 —— 后两层只在新键仍为空时才填。
	if c.LegacyScriptMaxSec != 0 {
		if c.CodeMaxSec == 0 {
			c.CodeMaxSec = c.LegacyScriptMaxSec
			c.warnLegacy("script_max_sec", "code_max_sec")
		} else {
			c.warnLegacyIgnored("script_max_sec", "code_max_sec")
		}
	}
	if c.LegacyScriptLog != "" {
		if c.CodeLog == "" {
			c.CodeLog = c.LegacyScriptLog
			c.warnLegacy("script_log", "code_log")
		} else {
			c.warnLegacyIgnored("script_log", "code_log")
		}
	}
	if c.LegacyScriptLogSlowMS != 0 {
		if c.CodeLogSlowMS == 0 {
			c.CodeLogSlowMS = c.LegacyScriptLogSlowMS
			c.warnLegacy("script_log_slow_ms", "code_log_slow_ms")
		} else {
			c.warnLegacyIgnored("script_log_slow_ms", "code_log_slow_ms")
		}
	}
	if !c.LegacyScript.isZero() {
		if c.Code.isZero() {
			c.Code = c.LegacyScript
			c.warnLegacy("script", "code")
		} else {
			c.warnLegacyIgnored("script", "code")
		}
	}

	// 第一层：interact → code（interact 是上一轮的旧键）。
	if c.LegacyInteractMaxSec != 0 {
		if c.CodeMaxSec == 0 {
			c.CodeMaxSec = c.LegacyInteractMaxSec
			c.warnLegacy("interact_max_sec", "code_max_sec")
		} else {
			c.warnLegacyIgnored("interact_max_sec", "code_max_sec")
		}
	}
	if c.LegacyInteractLog != "" {
		if c.CodeLog == "" {
			c.CodeLog = c.LegacyInteractLog
			c.warnLegacy("interact_log", "code_log")
		} else {
			c.warnLegacyIgnored("interact_log", "code_log")
		}
	}
	if c.LegacyInteractLogSlowMS != 0 {
		if c.CodeLogSlowMS == 0 {
			c.CodeLogSlowMS = c.LegacyInteractLogSlowMS
			c.warnLegacy("interact_log_slow_ms", "code_log_slow_ms")
		} else {
			c.warnLegacyIgnored("interact_log_slow_ms", "code_log_slow_ms")
		}
	}
	if !c.LegacyInteract.isZero() {
		if c.Code.isZero() {
			c.Code = c.LegacyInteract
			c.warnLegacy("interact", "code")
		} else {
			c.warnLegacyIgnored("interact", "code")
		}
	}

	// 第二层：session → code（session 是最早的旧键）。
	if c.LegacySessionMaxSec != 0 {
		if c.CodeMaxSec == 0 {
			c.CodeMaxSec = c.LegacySessionMaxSec
			c.warnLegacy("session_idle_sec", "code_max_sec")
		} else {
			c.warnLegacyIgnored("session_idle_sec", "code_max_sec")
		}
	}
	if c.LegacySessionLog != "" {
		if c.CodeLog == "" {
			c.CodeLog = c.LegacySessionLog
			c.warnLegacy("session_log", "code_log")
		} else {
			c.warnLegacyIgnored("session_log", "code_log")
		}
	}
	if c.LegacySessionLogSlowMS != 0 {
		if c.CodeLogSlowMS == 0 {
			c.CodeLogSlowMS = c.LegacySessionLogSlowMS
			c.warnLegacy("session_log_slow_ms", "code_log_slow_ms")
		} else {
			c.warnLegacyIgnored("session_log_slow_ms", "code_log_slow_ms")
		}
	}
	if !c.LegacySession.isZero() {
		if c.Code.isZero() {
			c.Code = c.LegacySession
			c.warnLegacy("session", "code")
		} else {
			c.warnLegacyIgnored("session", "code")
		}
	}
	c.applyLegacyAcquireTimeout()

	// ⚠️ 必须排在段级回填之后：更旧的 session: {langs: ...} 刚被搬到 c.Code 上，
	// 段内回填才知道该把哪份 langs 搬到 pools。
	c.applyCodeLegacyKeys()
}

// applyLegacyAcquireTimeout 把顶层的 acquire_timeout_sec（旧）搬到 script 段里。
//
// ⚠️ 必须排在 session 段映射**之后**。它自己是旧键，不能把"script 段非空"这件事
// 提前写进去 —— 否则「session: {langs: ...} + 顶层 acquire_timeout_sec」的配置会被
// isZero 判成"新段已写"，整个 session 段被丢弃（症状：双工语言全 unsupported_lang），
// 而这恰恰是旧代码不会发生的事（那时它有独立的顶层字段，不参与 isZero）。
func (c *Config) applyLegacyAcquireTimeout() {
	if c.LegacyAcquireTimeoutSec == 0 {
		return
	}
	if c.Code.AcquireTimeoutSec == 0 {
		c.Code.AcquireTimeoutSec = c.LegacyAcquireTimeoutSec
		c.warnLegacy("acquire_timeout_sec", "code.acquire_timeout_sec")
		return
	}
	c.warnLegacyIgnored("acquire_timeout_sec", "code.acquire_timeout_sec")
}

// applyCodeLegacyKeys 段内（code.langs → code.pools）与语言级
// （<lang>.pool → <lang>.idle，含已移除的 enabled 的翻译）的旧键回填。
func (c *Config) applyCodeLegacyKeys() {
	switch {
	case len(c.Code.Pools) == 0 && len(c.Code.LegacyLangs) > 0:
		c.Code.Pools = c.Code.LegacyLangs
		c.warnLegacy("code.langs", "code.pools")
	case len(c.Code.LegacyLangs) > 0:
		c.warnLegacyIgnored("code.langs", "code.pools")
	}

	for name, lang := range c.Code.Pools {
		// 生效的那一份里的 enabled 检测字段：翻译会把旧段整体丢掉（含指针），
		// 所以要在翻译之前把它捞出来单独看。旧段被忽略时不看旧段 —— 那是噪声。
		enabled := lang.Idle.LegacyEnabled
		switch {
		case lang.Idle == (PoolConfig{}) && lang.LegacyPool != (PoolConfig{}):
			enabled = lang.LegacyPool.LegacyEnabled
			lang.Idle = legacyIdle(lang.LegacyPool)
			c.warnLegacy("code.pools."+name+".pool", "code.pools."+name+".idle")
			// 落成 {0,0} = 旧语义的"不启用空闲池"，而用户写的 initial/max 被丢掉了 ——
			// 必须说清楚，否则那些数字看起来"配了却没生效"。
			if lang.Idle == (PoolConfig{}) {
				c.warnAbout("code.pools."+name+".pool",
					"旧语义下这个段不启用空闲池（enabled 不是 true，initial/max 都会被忽略），"+
						"已落成 idle: {initial: 0, max: 0}；要启用请写 idle.initial（例如 initial: 1）")
			}
		case lang.LegacyPool != (PoolConfig{}):
			c.warnLegacyIgnored("code.pools."+name+".pool", "code.pools."+name+".idle")
		}
		// enabled 不是「更名」而是「被 initial/max 取代」，所以文案单列一条。
		if enabled != nil {
			c.warnRemoved("code.pools."+name+".idle.enabled",
				"空闲池的开关是整个语言条目：要关掉就把该语言整条注释掉"+
					"（那会让它的 code 请求收到 unsupported_lang），不要只删 idle 段")
		}
		c.Code.Pools[name] = lang
	}
}

// legacyIdle 把旧的 pool 段按**旧语义**翻译成新的 idle 段。
//
// 旧语义里 enabled 是总开关，所以三档：
//
//	enabled 不是 true（包括没写）  → {0, 0}  不预热、不留驻（initial/max 一律被忽略）
//	enabled: true，有数值         → 数值原样（initial 大于 max 时复现旧的"钳到 max"）
//	enabled: true，无数值         → {1, 4}   复现旧缺省
//
// 返回 {0,0} 的两种情况都会由调用方额外出声，避免用户以为"配了却没生效"。
//
// ⚠️ L2（docker）下旧 enabled: false 的容器**仍然复用**（旧 newUnitPool 的
// size<=0→4 兜底不受 enabled 控制），而 {0,0} 是不复用 —— 这个方向是"更省资源"，
// 但若想保住容器复用，写 idle: {initial: 0, max: 4}。
func legacyIdle(old PoolConfig) PoolConfig {
	if old.LegacyEnabled == nil || !*old.LegacyEnabled {
		return PoolConfig{Initial: 0, Max: 0}
	}
	in, mx := old.Initial, old.Max
	if in <= 0 {
		in = 1 // 旧缺省
	}
	if mx <= 0 {
		mx = 4 // 旧缺省
	}
	if in > mx {
		in = mx // 旧 applyDefaults 会把 initial 钳到 max
	}
	return PoolConfig{Initial: in, Max: mx}
}

// warnLegacy 记录「旧键生效」的提醒。
func (c *Config) warnLegacy(oldKey, newKey string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"配置键 %s 已更名为 %s：本次按旧键生效，请更新配置（旧键后续版本移除）", oldKey, newKey))
}

// warnLegacyIgnored 记录「新旧都写了、旧键被忽略」的提醒。
func (c *Config) warnLegacyIgnored(oldKey, newKey string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"配置同时写了 %s 与 %s：以新键为准，旧键 %s 被忽略", oldKey, newKey, oldKey))
}

// warnRemoved 记录「旧键已被移除、且不是简单更名」的提醒（需要给出替代写法）。
func (c *Config) warnRemoved(oldKey, hint string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"配置键 %s 已移除：%s", oldKey, hint))
}

// warnAbout 记录一条与某个键相关的说明（既不是更名，也不是键被移除）。
func (c *Config) warnAbout(key, msg string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf("配置键 %s：%s", key, msg))
}

// applyDefaults 补全缺省值。
func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":27081"
	}
	if c.LogDir == "" {
		c.LogDir = "logs"
	}
	if c.CodeLog == "" {
		c.CodeLog = CodeLogSlow
	}
	if c.CodeLogSlowMS <= 0 {
		c.CodeLogSlowMS = 200
	}
	if c.Mode == "" {
		c.Mode = "none"
	}
	// 网络声明。安全项落在安全那一侧：漏写 network 就是全禁。
	//
	// 但 mode=none 是个例外，必须区分「没表态」与「显式声明」：
	// 宿主进程与外部网络同源，断网根本做不到 —— 把「没表态」当成「不能联网」会让
	// 最小配置（只写 mode: none）直接起不来。所以没表态时按事实把 networks.none
	// 补成 on（这是代码级事实，不是替用户做策略选择）；一旦显式声明过，就原样保留，
	// 交给 validateNetwork 判断是否自洽（声明 off 而 none 做不到 → 启动失败）。
	explicitNetwork := c.Network != ""
	if !explicitNetwork {
		c.Network = NetworkOff
	}
	if c.Mode == isolate.NoneName && !explicitNetwork {
		if c.Networks == nil {
			c.Networks = make(map[string]string, 1)
		}
		if _, ok := c.Networks[isolate.NoneName]; !ok {
			c.Networks[isolate.NoneName] = NetworkOn
		}
	}
	if c.Probe.TimeoutSec <= 0 {
		c.Probe.TimeoutSec = 30
	}
	if c.Probe.MaxOutputBytes <= 0 {
		c.Probe.MaxOutputBytes = 8192
	}
	if c.Codehost.Binary == "" {
		// 缺省 "codehost"（取 PATH 解析）；none 下 PATH 没有时用 binaries.none 指到宿主路径。
		c.Codehost.Binary = "codehost"
	}
	if c.Browser.Binary == "" {
		c.Browser.Binary = "browserhost"
	}
	// 注意：browser.profile_dir 的 docker 缺省（/tmp/browser）**故意放在
	// ResolveBrowserProfileDir 里**，不在这里回填。回填进字段会让它变成
	// 「通用值」，切 mode 时把 docker 的路径带到 none 上。
	if c.TmpDir == "" {
		c.TmpDir = "run/tmp"
	}
	if c.Code.AcquireTimeoutSec <= 0 {
		// 15 而不是 5：python / js 的并发额度只有 16 / 8，单次脚本可能被回调拉到
		// 几十秒，突发时 5s 的等待会把排在后面的请求全部推成「繁忙」。
		// 这段等待不占任何超时预算（见 handle_code.go），只是额外尾延迟。
		c.Code.AcquireTimeoutSec = 15
	}
	if c.CodeMaxSec <= 0 {
		c.CodeMaxSec = 120
	}
	if c.InteractiveIdleSec <= 0 {
		c.InteractiveIdleSec = 1800 // 30 分钟：跨连接常驻进程的空闲回收兜底
	}
	if c.InteractiveEphemeralIdleSec <= 0 {
		c.InteractiveEphemeralIdleSec = 120 // 2 分钟：无 session 临时进程的短兜底
	}
	if c.InteractiveJanitorSec <= 0 {
		c.InteractiveJanitorSec = 60 // 1 分钟：空闲回收的巡检分辨率
	}
	if c.Nsjail.CgroupVersion == "" {
		c.Nsjail.CgroupVersion = isolate.CgroupAuto
	}

	for name, lang := range c.Code.Pools {
		if lang.MaxConcurrent <= 0 {
			lang.MaxConcurrent = 8
		}
		// 语言级覆盖：0/缺省 → 取 script 段的缺省。
		if lang.AcquireTimeoutSec <= 0 {
			lang.AcquireTimeoutSec = c.Code.AcquireTimeoutSec
		}
		// 池随语言条目存在而存在 —— 这里只补 idle 的缺省，不判断「要不要池」。
		//
		// Max 跟随 Initial（而不是固定 4）：既保住「显式 initial 不被改写」，
		// 也不需要区分「没写 max」与「写 max: 0」（两者都是 0、都表示不留驻）。
		// 两个值都取 0 时就是「不预热/不留驻」，refiller 与归还路径自然空转。
		if lang.Idle.Max == 0 {
			lang.Idle.Max = lang.Idle.Initial
		}
		c.Code.Pools[name] = lang
	}
}

// ExecLimits 计算单工请求的最终上限：
//
//	用途默认 → 请求覆盖（非零字段）→ 全局 clamp
func (c *Config) ExecLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Exec.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// ScriptLimits 计算双工请求的最终上限（同上）。
func (c *Config) ScriptLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Code.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// Languages 合并「顶层语言定义」与「双工策略」，产出运行期配置（入口名按当前 mode 解析）。
//
// 只产出 code.pools 里列出的语言 —— 它们才是本服务要池化并服务的双工语言；
// 顶层 langs 里多出来的定义（例如只给单工脚本用的语言）不出现在这里，
// 而 pools 里缺席的语言会对调用方报 unsupported_lang（这就是「整条注释掉」的效果）。
// 任何一处取不到入口名，都在启动阶段就失败（而不是等池预热）。
func (c *Config) Languages() (map[string]LangConfig, error) {
	out := make(map[string]LangConfig, len(c.Code.Pools))
	for name, sl := range c.Code.Pools {
		def, ok := c.Langs[name]
		if !ok {
			return nil, fmt.Errorf("lang %q: no definition in top-level langs", name)
		}
		cmd, err := def.ResolveCommand(c.Mode)
		if err != nil {
			return nil, fmt.Errorf("lang %q: %w", name, err)
		}
		out[name] = LangConfig{
			Command:           cmd,
			MaxConcurrent:     sl.MaxConcurrent,
			AcquireTimeoutSec: sl.AcquireTimeoutSec,
			Idle:              sl.Idle,
		}
	}
	return out, nil
}

// ErrUnsupportedLang 请求的语言在顶层 langs 里没有定义。
// 用哨兵错误是为了让调用方能把它映射成协议错误码（unsupported_lang）。
var ErrUnsupportedLang = errors.New("unsupported language")

// ResolveLangCommand 取某语言在当前 mode 下的解释器入口名（单工用）。
//
// 与 Languages() 的区别：那条只服务 code.pools 列出的双工语言；这条面向顶层
// langs 的全部定义 —— 单工用到的语言（例如只跑技能脚本的）不一定要开双工池。
func (c *Config) ResolveLangCommand(lang string) (string, error) {
	def, ok := c.Langs[lang]
	if !ok {
		return "", fmt.Errorf("%w: %q (no definition in langs)", ErrUnsupportedLang, lang)
	}
	return def.ResolveCommand(c.Mode)
}

// ResolveCodehostBinary 取当前 mode 下 codehost 二进制的入口名：binaries[mode] 优先，
// 其次通用缺省 binary。与 ResolveCommand 同款「取不到就报错」—— L3 起 codehost 是
// 脚本执行的前提，静默退化成空只会让第一次调用脚本时才以「start: exec: no command」爆炸。
func (c *Config) ResolveCodehostBinary(mode string) (string, error) {
	if b, ok := c.Codehost.Binaries[mode]; ok && b != "" {
		return b, nil
	}
	if c.Codehost.Binary != "" {
		return c.Codehost.Binary, nil
	}
	return "", fmt.Errorf("codehost binary not configured (set codehost.binary, or codehost.binaries.%s)", mode)
}

// ResolveBrowserBinary 取当前 mode 下 browserhost 二进制的入口名：
// binaries[mode] 优先，其次通用缺省 binary。与 ResolveCodehostBinary 同款
// 「取不到就报错」—— 静默退化成空会让第一次调用浏览器时才以
// 「start: exec: no command」爆炸，而那时模型已经把页面操作计划好了。
func (c *Config) ResolveBrowserBinary(mode string) (string, error) {
	if b, ok := c.Browser.Binaries[mode]; ok && b != "" {
		return b, nil
	}
	if c.Browser.Binary != "" {
		return c.Browser.Binary, nil
	}
	return "", fmt.Errorf("browserhost binary not configured (set browser.binary, or browser.binaries.%s)", mode)
}

// BrowserLimits 计算浏览器进程的最终上限：
//
//	browser.limits → 请求覆盖（非零字段）→ 全局 clamp
func (c *Config) BrowserLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Browser.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// ResolveBrowserExecPath 取当前 mode 下的浏览器可执行文件路径。
//
// 回退顺序与 langs 的 command/commands 一致：exec_paths[mode] → exec_path → 空。
// 空不是错误：browserhost 会在沙箱内自动探测（docker 的 chromium、宿主的 Chrome/Edge），
// 这也正是「没配就别猜」的默认值。
func (c *Config) ResolveBrowserExecPath(mode string) string {
	if p, ok := c.Browser.ExecPaths[mode]; ok && p != "" {
		return p
	}
	return c.Browser.ExecPath
}

// ResolveBrowserProfileDir 取当前 mode 下的 Chrome profile 目录。
//
// 顺序：profile_dirs[mode] → profile_dir → mode 缺省（docker 用 /tmp/browser）→ 空。
// 空不是错误：browserhost 会建一个临时目录并在退出时删掉。
func (c *Config) ResolveBrowserProfileDir(mode string) string {
	if p, ok := c.Browser.ProfileDirs[mode]; ok && p != "" {
		return p
	}
	if c.Browser.ProfileDir != "" {
		return c.Browser.ProfileDir
	}
	if mode == isolate.DockerName {
		// 只读 rootfs 下唯一随容器销毁的可写位置（见 containerArgs 的 --tmpfs /tmp）。
		return "/tmp/browser"
	}
	return ""
}

// ResolveBrowserDownloadsDir 取当前 mode 下的下载目录。
// 顺序：downloads_dirs[mode] → downloads_dir → 空（browserhost 用 <cwd>/downloads）。
func (c *Config) ResolveBrowserDownloadsDir(mode string) string {
	if p, ok := c.Browser.DownloadsDirs[mode]; ok && p != "" {
		return p
	}
	return c.Browser.DownloadsDir
}

// AllowHeadedFor 取 mode 下允许有头的实际值：allow_headed_modes[mode] 优先，
// 未命中回退 allow_headed。
func (c *Config) AllowHeadedFor(mode string) bool {
	if v, ok := c.Browser.AllowHeadedModes[mode]; ok {
		return v
	}
	return c.Browser.AllowHeaded
}

// BrowserArgv 组装 browserhost 的命令行。
//
// 为什么由 sandboxd 组装而不是 nexus 传：沙箱内的路径（镜像里 browserhost 叫什么、
// profile 放哪）只有本服务知道，与 langs / codehost 的同一条理由。
func (c *Config) BrowserArgv(mode string) ([]string, error) {
	bin, err := c.ResolveBrowserBinary(mode)
	if err != nil {
		return nil, err
	}
	argv := []string{bin}
	if p := c.ResolveBrowserExecPath(mode); p != "" {
		argv = append(argv, "--exec-path", p)
	}
	if p := c.ResolveBrowserProfileDir(mode); p != "" {
		argv = append(argv, "--profile", p)
	}
	if p := c.ResolveBrowserDownloadsDir(mode); p != "" {
		argv = append(argv, "--downloads", p)
	}
	if c.AllowHeadedFor(mode) {
		argv = append(argv, "--allow-headed")
	}
	if c.Browser.Headed {
		argv = append(argv, "--default-headed")
	}
	return argv, nil
}

// validateBrowser 校验浏览器配置与当前隔离方式是否自洽。
//
// 判据与 validateNetwork 相同：**声明必须与事实一致**。
//   - nsjail 的 rootfs 里没有 Chromium 所需的整套运行时（/proc、/dev/shm、
//     字体、user namespace），本版本不支持；开着就直接拒绝启动，而不是等到
//     模型点开页面才失败。
//   - 有头需要真实显示环境，目前只有 mode=none（sandboxd 跑在有桌面的机器上）
//     成立；docker / nsjail 里传 headed 只会得到一个看不见的窗口。
func (c *Config) validateBrowser() error {
	if !c.Browser.Enabled {
		return nil
	}
	if c.Mode == isolate.NsjailName {
		return fmt.Errorf("browser: not supported with mode %q — the jail rootfs has no Chromium runtime "+
			"(/proc, /dev/shm, fonts); set browser.enabled: false, or use mode docker / none",
			isolate.NsjailName)
	}
	if c.Browser.Headed && !c.AllowHeadedFor(c.Mode) {
		return fmt.Errorf("browser.headed: true requires browser.allow_headed: true — " +
			"a visible window is a policy decision, not a default")
	}
	if c.AllowHeadedFor(c.Mode) && c.Mode != isolate.NoneName {
		return fmt.Errorf("browser.allow_headed: true is only supported with mode %q (a real display); "+
			"mode %q has no visible window — set allow_headed: false",
			isolate.NoneName, c.Mode)
	}
	_, err := c.ResolveBrowserBinary(c.Mode)
	return err
}

// Clamp 把 v 限制在 ceiling 之内（ceiling 中为 0 的维度表示不限制）。
func Clamp(v, ceiling sandbox.Limits) sandbox.Limits {
	out := v
	if ceiling.MemoryMB > 0 && (out.MemoryMB <= 0 || out.MemoryMB > ceiling.MemoryMB) {
		out.MemoryMB = ceiling.MemoryMB
	}
	if ceiling.CPUCount > 0 && (out.CPUCount <= 0 || out.CPUCount > ceiling.CPUCount) {
		out.CPUCount = ceiling.CPUCount
	}
	if ceiling.MaxPIDs > 0 && (out.MaxPIDs <= 0 || out.MaxPIDs > ceiling.MaxPIDs) {
		out.MaxPIDs = ceiling.MaxPIDs
	}
	if ceiling.MaxFileMB > 0 && (out.MaxFileMB <= 0 || out.MaxFileMB > ceiling.MaxFileMB) {
		out.MaxFileMB = ceiling.MaxFileMB
	}
	if ceiling.Timeout > 0 && (out.Timeout <= 0 || out.Timeout > ceiling.Timeout) {
		out.Timeout = ceiling.Timeout
	}
	if ceiling.MaxOutputBytes > 0 && (out.MaxOutputBytes <= 0 || out.MaxOutputBytes > ceiling.MaxOutputBytes) {
		out.MaxOutputBytes = ceiling.MaxOutputBytes
	}
	return out
}
