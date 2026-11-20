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

// Interactive detail logging policy (the code_log option).
//
// The detail records are "which container a call went through, whether it was reused, how long it took, whether the
// container was dropped" — the container identity only exists on the sandboxd side (nexus cannot see it), and the
// script is the join key both sides reconcile on.
const (
	// CodeLogOff records no interactive detail. Abnormal sessions (not returnable) are not covered by this switch
	// and are still recorded.
	CodeLogOff = "off"
	// CodeLogSlow records only sessions taking >= code_log_slow_ms (the default, recommended in production).
	CodeLogSlow = "slow"
	// CodeLogAll records every session (for troubleshooting).
	//
	// ⚠️ lua is a microsecond-level high-frequency call (loop conditions / branch conditions / binding transforms),
	// so leaving all on in normal operation turns the log into noise; it is only suitable for short troubleshooting.
	CodeLogAll = "all"
)

// Network declaration tiers (top-level network).
//
// ⚠️ This is not a switch but an **assertion about how the unit is started**: whether the sandbox can reach the
// network is decided by the isolation implementation's startup arguments (docker.network / nsjail.disable_network /
// the mode itself), and this only states it truthfully so that nexus can pass it on to the agent. Startup validates
// that "the assertion matches the fact" and refuses to start when it does not — lying to the agent is worse than
// misconfiguring it: it would write scripts on a wrong premise (writing pip install while believing the network is
// off, or blaming egress failures on a code bug while believing it is on).
const (
	// NetworkOff means no network inside the sandbox (a deployment-level hard gate: a request asking for egress is
	// rejected as well).
	NetworkOff = "off"
	// NetworkOn means the sandbox has network access (requests are not consulted; always egress).
	NetworkOn = "on"
	// NetworkOnRequest decides per request: off by default, egress only when a simplex request carries egress.
	//
	// It only means anything for /v1/exec — it is the only channel that "creates a new process each time", so the
	// network can be decided at every start. Duplex channels (script / interactive) reuse a unit that is already
	// built, and the network is fixed when the unit is created, so they only accept off / on; passing on-request
	// fails validation.
	NetworkOnRequest = "on-request"
)

// Config is the sandboxd configuration.
//
// This is the single source of truth for "sandbox resource policy": isolation mode, resource caps, process pools
// and concurrency budgets. nexus does not send any of it, it only supplies language names and (optionally) a
// timeout.
type Config struct {
	Listen string `yaml:"listen"`
	// Token must match sandbox.token on the nexus side; empty means no checking (internal network only).
	Token string `yaml:"token"`
	// LogDir is the log directory.
	LogDir string `yaml:"log_dir"`
	// CodeLog is the interactive detail logging policy: off / slow / all (see CodeLogXxx).
	//
	// Why it is needed: the detail records are the only clue to "where a call went", but under high-frequency
	// calls recording everything would only drown the log, so by default only slow ones are recorded and abnormal
	// ones are always recorded.
	CodeLog string `yaml:"code_log"`
	// CodeLogSlowMS is the threshold of the slow policy, in milliseconds.
	CodeLogSlowMS int `yaml:"code_log_slow_ms"`

	// Mode is the isolation mode: none / nsjail / docker. Decided here only.
	Mode string `yaml:"mode"`

	// Network is the network policy: whether processes inside the sandbox can reach the outside (off / on-request /
	// on, see NetworkXxx).
	//
	// It describes the **ceiling**, not the fact: whether this particular call has network access is the AND of
	// "deployment ceiling × tenant permission × request intent" (the first two live on the nexus side, the last is
	// sent with the /v1/exec request).
	//
	// Three states on purpose rather than a boolean: a boolean cannot express "can it reach the network" and
	// "does it listen to requests" at the same time.
	//   off          nobody gets egress (a request asking for on is rejected)
	//   on-request   off by default, egress only when the request explicitly asks (the only tier that lets exec
	//                decide per request)
	//   on           always egress, requests are ignored
	//
	// ⚠️ It only applies to /v1/exec (including skill scripts). /v1/code is the managed snippet channel, shared
	// across tenants, and the Frame carries no tenant identity, so egress there could only be switched on for the
	// whole platform with no per-tenant fallback — that channel is therefore always offline and has no option (see
	// EffectiveCodeNetwork; mode=none excepted, where the host cannot cut it off).
	//
	// The zero value is off: a security setting must land on the safe side, so omitting it disables everything.
	Network string `yaml:"network"`
	// Networks overrides Network per isolation mode (key: none / nsjail / docker).
	//
	// Why it is needed: none is a host process and cutting the network off is simply impossible — debugging locally
	// requires writing networks.none: on honestly. Declaring off where the current mode cannot implement it fails
	// startup (see validateNetwork).
	//
	// ⚠️ The value type must be string: yaml.v3 only enables the YAML 1.1 on/off semantics when decoding into a
	// bool, so decoding into a string keeps it a string. With map[string]any, on would silently become true.
	Networks map[string]string `yaml:"networks"`

	// Probe is the sandbox capability probe (see ProbeConfig).
	Probe ProbeConfig `yaml:"probe"`
	// Codehost locates the codehost host binary (shared by none / nsjail / docker).
	//
	// codehost is the long-lived host replacing worker.lua/py/js: sandboxd keeps it alive inside the isolation unit
	// and every execute forks a fresh interpreter from it. The binary path is resolved per mode (the host path under
	// none, the rootfs path under nsjail), the same mechanism as langs.<lang> command/commands.
	Codehost CodehostConfig `yaml:"codehost"`

	// Browser locates the browser host (browserhost) and carries its policy.
	//
	// The browser is the same kind of thing as a shell: a session-level long-lived process. So it reuses the
	// /v1/interactive channel (tool_id="browser") and adds no HTTP route; this section only describes "where the
	// browserhost in the image / rootfs is, and whether headed is possible".
	Browser BrowserConfig `yaml:"browser"`
	// TmpDir is the host root of the worker writable directories: every worker creates an exclusive subdirectory
	// under it, which is mounted into the sandbox as /tmp (and is the script's cwd at the same time).
	//
	// Startup empties this whole tree; nothing is deleted individually at runtime — see prepareTmpRoot.
	TmpDir string `yaml:"tmp_dir"`
	// AllowedMountRoots is the list of allowed host directory roots: a request's Mounts[].Host must fall under one
	// of them. An empty list means no checking (a warning is logged at startup).
	//
	// This is "policy", not "domain": sandboxd does not care what the caller's workspace looks like and does not
	// parse its business paths, it only ensures the paths it is asked to mount fall inside the authorised range.
	// Every caller just adds its own entry.
	AllowedMountRoots []string `yaml:"allowed_mount_roots"`

	Nsjail NsjailConfig `yaml:"nsjail"`
	Docker DockerConfig `yaml:"docker"`

	// Limits is the global hard cap: every request's final value is clamped to it.
	Limits LimitsConfig `yaml:"limits"`

	// Langs holds the language definitions: the truth about interpreter entry names, shared by exec (commands /
	// skill scripts) and script (workflow scripts).
	//
	// Why it sits at the top level instead of inside script: "which interpreters the sandbox has and what they are
	// called" is a fact about the language itself and has nothing to do with the use case; script only carries "the
	// pool and concurrency policy specific to script execution". Putting it on the wrong level meant exec could not
	// reuse the same definition — the nexus side had to hardcode one of its own (the former
	// service/agent/tools/skillrun.go interpreterByExt), and once the two drifted, "the flow runs but the skill does
	// not" appeared, while "what it is called in the image" is knowledge only this service has.
	Langs map[string]LangDef `yaml:"langs"`

	// Exec holds the defaults for the exec (command execution) use case. exec is never pooled.
	Exec UseConfig `yaml:"exec"`
	// Code holds the defaults for the code (workflow snippet, /v1/code) use case, plus each language's pool and
	// concurrency policy.
	Code CodeConfig `yaml:"code"`

	// CodeMaxSec is the cap on the **total duration** of one interaction (seconds), a backstop that keeps a hung
	// connection from holding a worker for a long time.
	//
	// ⚠️ The old names were session_idle_sec → script_max_sec in turn; the idle / script in both names was
	// misleading: this is not "reclaim after being idle" — the clock starts at the open frame and never resets (see
	// codeDeadline). It is the real hard gate of the code channel: a caller-supplied timeout can only tighten it,
	// and configuring it larger is the same as not configuring it (a caller-supplied timeout especially).
	CodeMaxSec int `yaml:"code_max_sec"`

	// InteractiveIdleSec is the idle reclaim cap of long-lived interactive processes (seconds).
	//
	// Long-lived interactive processes (/v1/interactive) are held by the session and outlive the connection; being
	// idle longer than this (counted from the most recent call) makes the background janitor reclaim them — kill
	// the process, release the container. This is the lifetime backstop of session-level long-lived processes and is
	// a different thing from code_max_sec (the hard gate of a single interaction).
	//
	// Why 5 minutes and not 30: a session container is the most expensive idle resource (a resident Chromium alone
	// costs hundreds of MB), while the state it preserves barely reaches past the current task. The browser profile
	// lives in the container's tmpfs and dies with it, so a long idle never buys persistent logins; headed browsers
	// (the only case where a human is actually watching) exist only under mode=none, i.e. local development.
	// In production (docker, headless) the window between "the model stopped" and "the container goes away" is
	// waste, so the default is kept short and deployments that want longer local sessions raise it in their config.
	InteractiveIdleSec int `yaml:"interactive_idle_sec"`

	// InteractiveEphemeralIdleSec is the short idle reclaim cap of temporary interactive processes without a
	// session (seconds).
	//
	// A call without a session (a one-shot run) has no need to persist across runs: repeated calls inside a run
	// reuse the process by "the gap being far smaller than this timeout", and at the end of the run nexus kills it
	// proactively (the fast path). When that notification is lost (a nexus crash and the like), this short timeout
	// is the backstop. Default 120.
	InteractiveEphemeralIdleSec int `yaml:"interactive_ephemeral_idle_sec"`

	// InteractiveJanitorSec is the sweep interval of the long-lived interactive registry (seconds), default 60.
	//
	// It is not a timeout itself but the "resolution of the timeout": a process is reclaimed on the first sweep
	// after it went idle, so the actual reclaim time falls in (idle, idle+janitor]. The gateway's sticky TTL must
	// cover that upper bound: sticky_ttl > idle + 2×janitor.
	//
	// It is configurable and reported through /v1/capabilities so that the inequality is **computable** on the
	// gateway side — the two values live in two different processes' config files, and the gateway does not hold
	// idle itself.
	InteractiveJanitorSec int `yaml:"interactive_janitor_sec"`

	// Registry is the optional service registration: register this service with consul/nacos for sandboxg to
	// discover. Disabled by default (keeping sandboxd free of external dependencies; a single instance connected
	// directly needs no registration).
	Registry RegistryConfig `yaml:"registry"`

	// The following are the pre-rename keys: used only for backfill + warning, no longer present in the docs or
	// config.yaml.
	//
	// Why keep the fields instead of deleting them: the live config lives outside the build, and once a field is
	// gone the old value is silently filled with the default by applyDefaults — and the default happens to "look
	// normal" (code_log defaults to slow), so whoever configured off / all would only notice the log behaviour
	// changing for no reason. See applyLegacyKeys.
	//
	// The rename chain has three layers: session (oldest) → interact (old) → script (newer) → code (current). The
	// backfill is judged from new to old, and when several layers are written the "newer" one wins with an explicit
	// warning that the old one is ignored.
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
	// LegacyAcquireTimeoutSec is the top-level acquire_timeout_sec (→ code.acquire_timeout_sec). It only serves the
	// code channel and used to sit at the top level mixed with the exec config; it now belongs to the code section.
	LegacyAcquireTimeoutSec int `yaml:"acquire_timeout_sec"`

	// Warnings holds parse-time notices (currently only legacy-key backfill), printed by the caller. Not part of
	// YAML decoding.
	Warnings []string `yaml:"-"`
}

// RegistryConfig is the service registration config (register this service with a registry for the gateway to
// discover).
//
// Why optional and off by default: a single sandboxd connected directly needs no registration (nexus connects to it
// directly); only multiple sandboxg instances create a discovery need. Off by default keeps "sandboxd has no
// external dependencies".
type RegistryConfig struct {
	Enabled bool `yaml:"enabled"`
	// Type is the registry type: consul | nacos.
	Type string `yaml:"type"`
	// Address is the registry address (for example localhost:8500).
	Address string `yaml:"address"`
	// ServiceName is the service name (sandboxd by default).
	ServiceName string `yaml:"service_name"`
	// ServiceAddr is the externally reachable address (the gateway connects to this; use the intranet IP across
	// machines, 127.0.0.1 locally).
	ServiceAddr string `yaml:"service_addr"`
	// ServicePort is the external port (the port of listen by default).
	ServicePort int `yaml:"service_port"`
	// HealthCheckPath is the health check path (/healthz by default).
	HealthCheckPath string `yaml:"health_check_path"`
}

// LimitsConfig holds resource caps (the zero value means no extra limit).
type LimitsConfig struct {
	MemoryMB       int   `yaml:"memory_mb"`
	CPUCount       int   `yaml:"cpu_count"`
	MaxPIDs        int   `yaml:"max_pids"`
	MaxFileMB      int   `yaml:"max_file_mb"`
	TimeoutSec     int   `yaml:"timeout_sec"`
	MaxOutputBytes int64 `yaml:"max_output_bytes"`
}

// NsjailConfig holds the nsjail isolation parameters (Linux).
type NsjailConfig struct {
	Binary         string   `yaml:"binary"`
	Rootfs         string   `yaml:"rootfs"`
	UID            int      `yaml:"uid"`
	GID            int      `yaml:"gid"`
	DisableNetwork bool     `yaml:"disable_network"`
	ReadOnlyRootfs bool     `yaml:"read_only_rootfs"` // read-only; an nsjail chroot is R/O by default, false adds --rw
	CgroupVersion  string   `yaml:"cgroup_version"`   // v1 / v2 / auto
	ExtraArgs      []string `yaml:"extra_args"`
}

// DockerConfig holds the docker isolation parameters.
type DockerConfig struct {
	Binary    string   `yaml:"binary"`
	Image     string   `yaml:"image"`
	Network   string   `yaml:"network"`
	ReadOnly  bool     `yaml:"read_only"`
	User      string   `yaml:"user"`
	ExtraArgs []string `yaml:"extra_args"`
}

// ProbeConfig configures the sandbox capability probe.
//
// At startup a set of independent probes runs inside the sandbox (see probe.go), and the result is handed to nexus
// through /v1/capabilities for injection into the agent. Why probe instead of declare: what is inside the sandbox
// (OS / languages / modules) is a **fact**, and declarations drift — installing a new package in the Dockerfile
// while forgetting the config makes the agent write scripts against a wrong environment.
type ProbeConfig struct {
	// TimeoutSec is the overall probe timeout (seconds). The probes run concurrently; serially the worst case
	// would add up.
	TimeoutSec int `yaml:"timeout_sec"`
	// MaxOutputBytes is the output cap of a single probe (the result enters the LLM context, so it must be capped).
	MaxOutputBytes int64 `yaml:"max_output_bytes"`
}

// UseConfig holds per-use-case defaults.
type UseConfig struct {
	Limits LimitsConfig `yaml:"limits"`
}

// CodeConfig is the configuration of the code (workflow snippet) use case.
type CodeConfig struct {
	Limits LimitsConfig `yaml:"limits"`
	// AcquireTimeoutSec is the cap on waiting for a slot (seconds), the **default** of this section (15 when
	// unset, see applyDefaults): beyond it the request gets "busy" rather than blocking forever (binding transforms
	// are nested calls, and blocking forever would deadlock on itself). This wait is not part of any timeout budget
	// (it happens before the ready frame), it is only extra tail latency.
	//
	// Why it lives in the script section: this is a script-specific concept — exec is never pooled, so "acquiring a
	// slot" does not exist there at all. Each language can override it with code.pools.<lang>.acquire_timeout_sec.
	AcquireTimeoutSec int `yaml:"acquire_timeout_sec"`
	// Pools holds the script language pool policy: keyed by language name (lua / python / js), which must have a
	// definition of the same name in the top-level langs (the interpreter entry is there).
	//
	// A language entry being present = that language enables the idle pool and serves requests; to drop it, comment
	// out the **whole** entry — script requests for that language then get unsupported_lang instead of "silently
	// getting slower". There is deliberately no "disable the idle pool but keep the language" tier: its only
	// consequence is a silent performance cliff of a cold start (300ms~1s) on every call.
	Pools map[string]CodeLang `yaml:"pools"`
	// LegacyLangs is the pre-rename key code.langs (→ code.pools): used only for backfill + warning.
	LegacyLangs map[string]CodeLang `yaml:"langs"`
}

// isZero reports whether the config section is "completely unwritten": the legacy backfill must tell "the old
// section has content" apart from "an empty section".
//
// ⚠️ LegacyLangs must be counted: the content of the even older `session: {langs: ...}` lands on it, and looking at
// Pools alone would judge "an old section with content" as "completely unwritten" → the section-level backfill is
// skipped → it silently does nothing.
func (s CodeConfig) isZero() bool {
	return s.Limits == (LimitsConfig{}) && s.AcquireTimeoutSec == 0 &&
		len(s.Pools) == 0 && len(s.LegacyLangs) == 0
}

// LangDef is a language's interpreter definition (top-level langs.<lang>).
//
// It only describes "how this language is started inside the sandbox" and carries no use-case / pool policy — which
// is why both exec and script can use it. The per-mode override mechanism is described at CommandByMode.
type LangDef struct {
	// Command is the interpreter entry name (the general default, see CommandByMode).
	Command string `yaml:"command"`
	// CommandByMode overrides Command per isolation mode (optional, keyed by mode: none / nsjail / docker).
	//
	// Why it is needed: command is "the executable entry name under the current mode", and the three modes resolve
	// it in completely different places — docker resolves the in-image PATH, nsjail resolves the rootfs inside the
	// chroot, none execs on the host. The same name does not necessarily exist in all three environments (the image
	// has whatever lua the Dockerfile `ln`ed, a rootfs may only have lua5.4, and a Windows host has a
	// version-suffixed filename). These differences used to live in comments only, so "what the config said" and
	// "what actually took effect" were two different things; now it is modelled explicitly and ResolveCommand picks
	// the value for the current mode, so comments no longer carry configuration duties.
	CommandByMode map[string]string `yaml:"commands"`
}

// CodehostConfig locates the codehost host binary (top-level codehost section).
//
// Isomorphic to LangDef: binary is the general default and binaries overrides it per mode. codehost runs under all
// three modes — the path under none is host-resolvable, under nsjail / docker it is inside the rootfs / image.
type CodehostConfig struct {
	// Binary is the general default (for example "codehost", resolved through PATH).
	Binary string `yaml:"binary"`
	// Binaries overrides Binary per isolation mode (key: none / nsjail / docker).
	Binaries map[string]string `yaml:"binaries"`
}

// BrowserConfig is the browser host config (top-level browser section).
//
// There is deliberately no "engine selection": the driver layer is fixed on CDP (the Chromium family). Switching
// engines is not something one field can do (Firefox removed CDP, and WebKit only exists as Playwright's custom
// build), and offering so many tiers would only make people believe switching over just works.
type BrowserConfig struct {
	// Enabled says whether the browser tool is offered. Default false: a browser can reach the network and read
	// pages, which is a capability that "you should know about when you turn it on", not something obtained by
	// omitting the config.
	Enabled bool `yaml:"enabled"`

	// Binary is the general default (for example "browserhost", resolved through PATH).
	Binary string `yaml:"binary"`
	// Binaries overrides Binary per isolation mode (key: none / nsjail / docker).
	Binaries map[string]string `yaml:"binaries"`

	// ExecPath is the browser executable (**an in-sandbox** path). Empty = discovered by browserhost itself
	// (chromium / chrome / Edge on the host).
	//
	// Note that it has the same nature as binaries: it is "a path visible inside the isolation unit", a host path
	// under none and an in-image path under docker — one string cannot hold for both, so the effective value goes
	// through ExecPaths per mode.
	ExecPath string `yaml:"exec_path"`
	// ExecPaths overrides ExecPath per isolation mode (key: none / nsjail / docker). Typical use: docker writes
	// /usr/bin/chromium from the image, none writes the absolute path of Chrome on the host.
	ExecPaths map[string]string `yaml:"exec_paths"`

	// Headed is the default display mode: false = headless (recommended in production).
	//
	// It is spelled "headed" rather than "headless" so that the zero value is the safe value — the zero value of a
	// bool is false, and with "headless" there would be no way to tell "unset" from "explicitly off".
	Headed bool `yaml:"headed"`
	// AllowHeaded says whether a caller may request headed per session. Headed needs a real display environment and
	// is currently only supported with mode=none (sandboxd running on a machine with a desktop).
	AllowHeaded bool `yaml:"allow_headed"`
	// AllowHeadedModes overrides AllowHeaded per isolation mode (key: none / nsjail / docker). The value here is
	// used when the current mode has an entry, otherwise it falls back to AllowHeaded — the same rule as
	// exec_paths.
	AllowHeadedModes map[string]bool `yaml:"allow_headed_modes"`

	// ProfileDir is Chrome's user-data-dir (an in-sandbox path).
	//
	// Empty = a per-mode default: docker → /tmp/browser (the container has --tmpfs /tmp, so the profile dies with
	// the container, never lands in the read-only rootfs and never pollutes the workspace); none / nsjail → a temp
	// directory removed when the browser exits.
	ProfileDir string `yaml:"profile_dir"`
	// ProfileDirs overrides ProfileDir per isolation mode (key: none / nsjail / docker).
	ProfileDirs map[string]string `yaml:"profile_dirs"`
	// DownloadsDir is the download directory (an in-sandbox path). Empty = <cwd>/downloads (that is, the
	// workspace).
	DownloadsDir string `yaml:"downloads_dir"`
	// DownloadsDirs overrides DownloadsDir per isolation mode (key: none / nsjail / docker).
	//
	// Same reasoning as exec_path: this is an in-sandbox path, and an absolute one only holds for one mode
	// (/workspace under none becomes C:\workspace on the host root disk). The default is relative to cwd and
	// therefore cross-mode by nature, so most deployments do not need to configure it.
	DownloadsDirs map[string]string `yaml:"downloads_dirs"`

	// Limits is the resource cap of the browser process (defaults + global clamp).
	//
	// Why a separate section: Chrome's process tree plus rendering cache is a different order of magnitude from a
	// shell, and applying exec's 512MB to it gets it OOM-killed when the second tab opens.
	Limits LimitsConfig `yaml:"limits"`
}

// CodeLang is a script-specific language policy (code.pools.<lang>): pool and concurrency only, no interpreter
// entry — that belongs to the top-level langs.
//
// The existence of this entry is itself the signal = that language enables the idle pool and serves requests (see
// CodeConfig.Pools).
type CodeLang struct {
	// There is deliberately **no** timeout field here.
	//
	// The script timeout is sent by the caller (nexus) in the open frame on every call, and this service only clamps
	// it with one global hard gate (code_max_sec, see codeDeadline). A timeout field used to exist, but it had no
	// consumer and could never take effect (nexus always sends >= 30s), it only made people believe a
	// "language-level default timeout" was configured here — it has been removed.
	// MaxConcurrent is the cap on concurrent sessions (nested calls included).
	// It must be >= 2: scenarios like binding transforms are nested calls and occupy two sessions at once.
	MaxConcurrent int `yaml:"max_concurrent"`
	// AcquireTimeoutSec caps waiting for a slot (seconds); 0 means it inherits code.acquire_timeout_sec.
	//
	// This level exists because max_concurrent differs a lot between languages (32 / 16 / 8): lua is a
	// microsecond-level high-frequency call, and the longer it waits the more it holds up the workflow, so it is
	// worth configuring a shorter one for it.
	AcquireTimeoutSec int `yaml:"acquire_timeout_sec"`
	// Idle holds the idle pool parameters; default {0, 0} (no prewarming, nothing kept resident).
	Idle PoolConfig `yaml:"idle"`
	// LegacyPool is the pre-rename sub-section code.pools.<lang>.pool (→ .idle): used only for backfill + warning.
	LegacyPool PoolConfig `yaml:"pool"`
}

// LangConfig is a language's complete configuration at runtime: the entry name (already resolved for the mode) plus
// the script pool policy.
//
// It is produced by Config.Languages() by merging the top-level langs (LangDef) with code.pools (CodeLang) for the
// pool to use; it is not a YAML decoding target, hence no yaml tags.
//
// The fields are deliberately flat instead of embedding CodeLang: the language version of Go 1.25 does not allow
// promoted fields in struct literals, and flat fields keep constructions like LangConfig{MaxConcurrent: ...} working
// (the pool tests write a lot of those).
type LangConfig struct {
	Command string

	// No timeout field: the script timeout is sent with the request, and the server only has code_max_sec as a
	// global hard gate (see codeDeadline).
	MaxConcurrent     int
	AcquireTimeoutSec int
	Idle              PoolConfig
}

// ResolveCommand returns the interpreter entry name of the language under the current mode: commands[mode] first,
// then the general default command.
//
// It reports an error when nothing can be resolved instead of degrading to an empty command — an empty command only
// surfaces at pool prewarm time as "refill ... failed: recv pong: EOF", which is extremely expensive to diagnose.
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

// PoolConfig holds the idle pool parameters of one language (code.pools.<lang>.idle).
//
// The switch is not inside this section — it is "whether the whole language entry is there" (see CodeConfig.Pools):
// the entry being present = that language enables the idle pool; to drop it, comment out the whole entry.
//
// Both values in the section respect the zero value: omitting gives 0, and 0 is a legitimate meaning (no prewarming
// / nothing kept resident). There is deliberately no enabled boolean — a self-contradictory state like "wrote
// initial: 2 but says no pool" should not exist.
//
// "Is there anything in the section" can be judged with == PoolConfig{} (the zero value contains the pointer below
// and is still comparable).
type PoolConfig struct {
	// Initial is the guaranteed number of idle workers: prewarmed at startup and maintained by the refiller at
	// runtime. 0 = no prewarming (started on lend, still returned to the pool and reused after being given back,
	// unless Max is 0 too).
	Initial int `yaml:"initial"`
	// Max is the idle pool capacity (workers that are lent out do not count against it). Default = Initial.
	// 0 = nothing kept resident (destroyed on return).
	Max int `yaml:"max"`
	// LegacyEnabled is the enabled boolean that existed before the rename / semantic change: used only to translate
	// into Initial/Max with an audible warning, and not consulted by any runtime decision (see legacyIdle).
	LegacyEnabled *bool `yaml:"enabled"`
}

// ToSandbox converts into sandbox.Limits.
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

// expandEnv expands ${VAR} / $VAR in the "locating" fields of the config.
//
// Why it is needed: interpreter entries, host binaries, rootfs / images and mount roots are completely different on
// different machines. Hardcoding absolute paths means "it does not start on another machine", and it ships someone's
// local paths with the package. With environment variables, "the same config.yaml + different environments" works.
//
// Why only these fields and not a global replacement over the whole YAML: a $ inside a token / password is legal,
// and a global expansion would change it silently, with no way to tell afterwards.
//
// Why referencing an unset variable is an error: expanded into an empty string, ResolveCommand /
// ResolveCodehostBinary would treat it as "not configured" and silently fall back to the general default — an
// override that did not take effect with no trace at all, exactly the hardest kind to find.
func (c *Config) expandEnv() error {
	var firstErr error

	// set expands a single field; on error the first one is kept and later ones do not overwrite it.
	set := func(where string, p *string) {
		if firstErr != nil || p == nil || *p == "" {
			return
		}
		out := os.ExpandEnv(*p)
		if out == "" {
			firstErr = fmt.Errorf("%s: %q references an environment variable that is not set", where, *p)
			return
		}
		*p = out
	}
	// setMap expands a "per-mode override" table (the key is the mode; the value must not expand to empty either).
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

// LoadConfig reads the config and fills in the defaults.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sandboxd: read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("sandboxd: parse config %s: %w", path, err)
	}
	// Environment expansion follows decoding immediately: before applyDefaults, so defaults do not participate (they
	// contain no $ anyway); and before applyLegacyKeys, so backfilled values do not have to go through it again.
	if err := c.expandEnv(); err != nil {
		return nil, fmt.Errorf("sandboxd: config %s: %w", path, err)
	}
	// The backfill must come before applyDefaults: the latter fills empty values with defaults, and once filled there
	// is no way to tell "the user did not write it" from "the user wrote the old key".
	c.applyLegacyKeys()
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("sandboxd: config %s: %w", path, err)
	}
	return &c, nil
}

// validate checks the values so that a misspelled policy is not silently taken as a default.
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
		// The keys of commands must be known modes: a typo (dockerr) makes ResolveCommand fall back to the general
		// command silently, which means the override did not take effect with no sign of it.
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
	// Same reasoning as commands / codehost.binaries: a misspelled mode makes the override silently ineffective, and
	// here the consequence is subtler — after falling back to the general path, the browser points at a file that
	// does not exist under the other mode, and it only blows up on the first open.
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
	// Same for the headed override table: a misspelled mode makes the override silently ineffective, and local
	// headed debugging mysteriously turns headless.
	for mode := range c.Browser.AllowHeadedModes {
		switch mode {
		case isolate.NoneName, isolate.NsjailName, isolate.DockerName:
		default:
			return fmt.Errorf("browser.allow_headed_modes: unknown mode %q (want %q, %q or %q)",
				mode, isolate.NoneName, isolate.NsjailName, isolate.DockerName)
		}
	}
	for name, lang := range c.Code.Pools {
		// A duplex policy must hang off a defined language: otherwise assembly cannot find the interpreter entry.
		if _, ok := c.Langs[name]; !ok {
			return fmt.Errorf("code.pools.%s: no matching definition in top-level langs", name)
		}
		// The guaranteed idle count cannot exceed the pool capacity: the refiller tops the idle objects up to
		// initial, but with a capacity of only max every push hits the default branch, is destroyed and returned —
		// the result is a "start one, kill one" spin every second (worse at L2: a container created and destroyed
		// every second).
		if lang.Idle.Initial > lang.Idle.Max {
			return fmt.Errorf("code.pools.%s.idle: initial %d > max %d — "+
				"the refiller tops idle objects up to initial but the pool capacity is only max, so it spins "+
				"starting and killing one object every second; "+
				"raise max to >= initial (for example max: %d), or lower initial to <= max",
				name, lang.Idle.Initial, lang.Idle.Max, lang.Idle.Initial)
		}
	}
	return nil
}

// EffectiveNetwork is the network policy in effect for /v1/exec under the current mode: networks[mode] first,
// otherwise the top-level network.
//
// It is only the **ceiling** — whether this particular call egresses still has to pass the tenant permission and the
// request flag (stage 2 decides in handleExec). capabilities reports this value, not the fact of one call.
//
// Both layers have defaults (applyDefaults fills network with off), so a value is always available — unlike
// ResolveCommand's "report an error when nothing resolves", "nothing resolves" cannot happen here.
func (c *Config) EffectiveNetwork() string {
	if v, ok := c.Networks[c.Mode]; ok && v != "" {
		return v
	}
	return c.Network
}

// EffectiveCodeNetwork is the network policy of /v1/code: always off, not configurable.
//
// Why hardcoded instead of adding a network_script option:
//   - the script pool is created globally per language (slotPool), shared across tenants, and wire.Frame carries no
//     tenant identity — sandboxd simply has no way to decide per tenant, and configuring on could only switch it on
//     for the whole platform;
//   - once opened, a runaway tenant script (exfiltrating data / mining / acting as a proxy) could not be shut down
//     for that tenant alone, only rolled back platform-wide, taking every tenant's workflows down with it;
//   - the workflow's egress needs are served by the http / rpc / mcp nodes, and that is where the domain allowlist,
//     credentials, rate limiting and auditing live — the sandbox layer can never do those.
//
// The function is kept (rather than scattering the literal "off" through the handlers) so that the per-channel
// report has a single source, and a future change has exactly one place to go.
//
// ⚠️ mode=none is the exception: scripts run on the host process, so there is no isolation unit and no such thing as
// "no network" — the actual behaviour is that it can egress. It must be reported honestly as on here; reporting off
// would make the agent write scripts on a wrong premise (afraid to write because it believes there is no network, or
// blaming egress failures on a code bug).
func (c *Config) EffectiveCodeNetwork() string {
	if c.Mode == isolate.NoneName {
		return NetworkOn
	}
	return NetworkOff
}

// validateNetwork checks the values of the network declaration and whether they agree with "how the unit is
// started".
//
// There is only one criterion: **the declaration must match the fact** — off must really hold, on must really be
// open. The check is done entirely inside the config (comparing docker.network / nsjail.disable_network / mode); it
// needs no runtime probing.
//
// Blind spot: docker.extra_args / nsjail.extra_args are escape hatches, and stuffing in a --network host overturns
// the declaration with no way for the program to check — if you use the escape hatch, do not count on this catching
// it; that is on the deployer.
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

// validNetworkPolicy reports whether a three-state value is legal.
func validNetworkPolicy(v string) bool {
	return v == NetworkOff || v == NetworkOnRequest || v == NetworkOn
}

// validateNetworkOnRequest: when declaring "decide per request", the current mode must **really be able to switch**.
//
// The criterion is stricter than for off / on: not "one state is reachable" but "both states are reachable".
//   - docker: both --network none and --network <mode> must be usable, so docker.network must not be empty and must
//     not be none — otherwise an egress request has no network to be given.
//   - nsjail: cutting the network relies on clone_newnet (on by default) plus --iface_no_lo at runtime; egress needs
//     --disable_clone_newnet (sharing the host netns). The latter can only be written in extra_args and the program
//     cannot check it, so this only warns — the deployer is responsible.
//   - none: a host process shares the outside network and cannot be cut off; egress=false requests are rejected at
//     runtime.
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

// validateNetworkOff: when declaring "no network", the current mode must really be able to cut it off.
func (c *Config) validateNetworkOff() error {
	switch c.Mode {
	case isolate.DockerName:
		// docker's network cutoff is built into containerArgs (network: none; an empty value is filled with none).
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
		// none: commands are executed directly by the host, sharing the outside network, with no means to cut it
		// off.
		return fmt.Errorf("network: %q cannot be enforced with mode %q — commands run on the host and cannot be cut off from the network; "+
			"to run locally, declare it honestly with networks.none: %q", NetworkOff, c.Mode, NetworkOn)
	}
	return nil
}

// validateNetworkOn: when declaring "network available", the current mode must really be open.
func (c *Config) validateNetworkOn() error {
	switch c.Mode {
	case isolate.DockerName:
		// ⚠️ An empty value is filled with none by NewDocker — declaring on without giving a network mode is
		// self-contradictory.
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
		// none: a host process necessarily has network access, so declaring on is truthful.
	}
	return nil
}

// CodeLogEnabled decides whether a session lasting dur should have its detail recorded.
//
// Abnormal "container not returnable" sessions do not go through here — the caller force-records them at wrap-up,
// because that is the most needed kind of clue in troubleshooting and must not be switched off by off.
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

// applyLegacyKeys backfills the pre-rename keys onto the new ones and records warnings.
//
// ⚠️ It must be called before applyDefaults: applyDefaults fills empty values with defaults, and once it has run the
// new key is non-empty → the backfill judges "the new key is written" → the old value is dropped, silently — and
// code_log's default happens to be slow, so whoever configured off / all would only notice the log behaviour
// changing for no reason.
//
// The only criterion is "new key empty + old key present". When both are written the new key wins with an explicit
// warning that the old key is ignored: do not guess "which one is more trustworthy", otherwise the old key would
// give the illusion of "still in effect".
//
// The three layers are judged from new to old: script (the 2026-10 old key) → interact → session.
func (c *Config) applyLegacyKeys() {
	// Layer zero: script → code (renamed in 2026-10; script was the new key at the time and is now an old one). It
	// must come first: it is closest to the present and has the highest priority — the two later layers only fill
	// when the new key is still empty.
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

	// Layer one: interact → code (interact was the previous round's old key).
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

	// Layer two: session → code (session is the oldest old key).
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

	// ⚠️ It must come after the section-level backfill: the older session: {langs: ...} has just been moved onto
	// c.Code, and only then does the in-section backfill know which langs to move to pools.
	c.applyCodeLegacyKeys()
}

// applyLegacyAcquireTimeout moves the top-level acquire_timeout_sec (old) into the script section.
//
// ⚠️ It must come **after** the session section mapping. It is itself an old key and must not write "the script
// section is non-empty" ahead of time — otherwise a config of "session: {langs: ...} + top-level
// acquire_timeout_sec" would be judged by isZero as "the new section is written", and the whole session section
// would be dropped (symptom: every duplex language gets unsupported_lang), which is exactly what the old code did
// not do (it had an independent top-level field then and did not take part in isZero).
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

// applyCodeLegacyKeys backfills the in-section (code.langs → code.pools) and language-level
// (<lang>.pool → <lang>.idle, including the translation of the removed enabled) old keys.
func (c *Config) applyCodeLegacyKeys() {
	switch {
	case len(c.Code.Pools) == 0 && len(c.Code.LegacyLangs) > 0:
		c.Code.Pools = c.Code.LegacyLangs
		c.warnLegacy("code.langs", "code.pools")
	case len(c.Code.LegacyLangs) > 0:
		c.warnLegacyIgnored("code.langs", "code.pools")
	}

	for name, lang := range c.Code.Pools {
		// The enabled detection field of the effective copy: the translation drops the whole old section (pointer
		// included), so it must be fished out before translating. When the old section is ignored it is not looked
		// at — that would be noise.
		enabled := lang.Idle.LegacyEnabled
		switch {
		case lang.Idle == (PoolConfig{}) && lang.LegacyPool != (PoolConfig{}):
			enabled = lang.LegacyPool.LegacyEnabled
			lang.Idle = legacyIdle(lang.LegacyPool)
			c.warnLegacy("code.pools."+name+".pool", "code.pools."+name+".idle")
			// Landing as {0,0} = the old semantics of "idle pool not enabled", while the initial/max the user wrote
			// are dropped — that must be stated, otherwise those numbers look like "configured but not taking
			// effect".
			if lang.Idle == (PoolConfig{}) {
				c.warnAbout("code.pools."+name+".pool",
					"under the old semantics this section did not enable the idle pool (enabled was not true, so "+
						"initial/max were ignored); it has been resolved to idle: {initial: 0, max: 0}; to enable "+
						"it write idle.initial (for example initial: 1)")
			}
		case lang.LegacyPool != (PoolConfig{}):
			c.warnLegacyIgnored("code.pools."+name+".pool", "code.pools."+name+".idle")
		}
		// enabled was not "renamed" but "replaced by initial/max", so its message is a separate one.
		if enabled != nil {
			c.warnRemoved("code.pools."+name+".idle.enabled",
				"the idle pool switch is the whole language entry: to turn it off, comment out the entire entry "+
					"(that makes its code requests get unsupported_lang), do not just delete the idle section")
		}
		c.Code.Pools[name] = lang
	}
}

// legacyIdle translates the old pool section into the new idle section under the **old semantics**.
//
// Under the old semantics enabled was the master switch, so there are three cases:
//
//	enabled is not true (including unwritten)  → {0, 0}  no prewarming, nothing resident (initial/max ignored)
//	enabled: true, with numbers                 → the numbers as-is (initial > max reproduces the old clamp to max)
//	enabled: true, without numbers              → {1, 4}  reproduces the old default
//
// Both cases returning {0,0} are additionally announced by the caller, so the user does not believe "configured but
// not taking effect".
//
// ⚠️ At L2 (docker) containers of the old enabled: false were **still reused** (the old newUnitPool's size<=0→4
// fallback was not controlled by enabled), while {0,0} does not reuse — that direction is "cheaper on resources",
// but to keep container reuse write idle: {initial: 0, max: 4}.
func legacyIdle(old PoolConfig) PoolConfig {
	if old.LegacyEnabled == nil || !*old.LegacyEnabled {
		return PoolConfig{Initial: 0, Max: 0}
	}
	in, mx := old.Initial, old.Max
	if in <= 0 {
		in = 1 // the old default
	}
	if mx <= 0 {
		mx = 4 // the old default
	}
	if in > mx {
		in = mx // the old applyDefaults clamped initial to max
	}
	return PoolConfig{Initial: in, Max: mx}
}

// warnLegacy records the "old key took effect" notice.
func (c *Config) warnLegacy(oldKey, newKey string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"config key %s was renamed to %s: this run uses the old key, please update the config (the old key is removed in a later release)", oldKey, newKey))
}

// warnLegacyIgnored records the "both were written, the old key is ignored" notice.
func (c *Config) warnLegacyIgnored(oldKey, newKey string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"config sets both %s and %s: the new key wins, old key %s is ignored", oldKey, newKey, oldKey))
}

// warnRemoved records the "the old key was removed and it is not a plain rename" notice (a replacement is needed).
func (c *Config) warnRemoved(oldKey, hint string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"config key %s was removed: %s", oldKey, hint))
}

// warnAbout records a remark about a key (neither a rename nor a removal).
func (c *Config) warnAbout(key, msg string) {
	c.Warnings = append(c.Warnings, fmt.Sprintf("config key %s: %s", key, msg))
}

// applyDefaults fills in the default values.
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
	// The network declaration. A security setting lands on the safe side: omitting network disables everything.
	//
	// But mode=none is an exception and "no statement" must be told apart from "explicit declaration": a host
	// process shares the outside network and cutting it off is simply impossible — treating "no statement" as "no
	// network" would make the minimal config (only mode: none) fail to start. So without a statement, networks.none
	// is filled with on according to the fact (that is a code-level fact, not making a policy choice for the user);
	// once explicitly declared it is kept as-is and handed to validateNetwork to judge consistency (declaring off
	// where none cannot do it → startup fails).
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
		// Default "codehost" (resolved through PATH); when PATH lacks it under none, point binaries.none at the host
		// path.
		c.Codehost.Binary = "codehost"
	}
	if c.Browser.Binary == "" {
		c.Browser.Binary = "browserhost"
	}
	// Note: the docker default of browser.profile_dir (/tmp/browser) is **deliberately kept in
	// ResolveBrowserProfileDir** and not filled here. Filling it into the field would turn it into a "general value"
	// and drag the docker path onto none when the mode changes.
	if c.TmpDir == "" {
		c.TmpDir = "run/tmp"
	}
	if c.Code.AcquireTimeoutSec <= 0 {
		// 15 rather than 5: python / js only have concurrency budgets of 16 / 8, a single script can be pulled to
		// tens of seconds by callbacks, and a 5s wait would push everything queued behind it into "busy" during a
		// burst. This wait does not consume any timeout budget (see handle_code.go), it is only extra tail latency.
		c.Code.AcquireTimeoutSec = 15
	}
	if c.CodeMaxSec <= 0 {
		c.CodeMaxSec = 120
	}
	if c.InteractiveIdleSec <= 0 {
		c.InteractiveIdleSec = 300 // 5 minutes: the idle reclaim backstop of cross-connection resident processes (see the field comment)
	}
	if c.InteractiveEphemeralIdleSec <= 0 {
		c.InteractiveEphemeralIdleSec = 120 // 2 minutes: the short backstop of temporary processes without a session
	}
	if c.InteractiveJanitorSec <= 0 {
		c.InteractiveJanitorSec = 60 // 1 minute: the sweep resolution of idle reclamation
	}
	if c.Nsjail.CgroupVersion == "" {
		c.Nsjail.CgroupVersion = isolate.CgroupAuto
	}

	for name, lang := range c.Code.Pools {
		if lang.MaxConcurrent <= 0 {
			lang.MaxConcurrent = 8
		}
		// Language-level override: 0/default → take the default of the script section.
		if lang.AcquireTimeoutSec <= 0 {
			lang.AcquireTimeoutSec = c.Code.AcquireTimeoutSec
		}
		// The pool exists as long as the language entry does — this only fills the idle defaults and does not decide
		// "whether a pool is wanted".
		//
		// Max follows Initial (rather than a fixed 4): it keeps "an explicit initial is not rewritten" and needs no
		// distinction between "max not written" and "max: 0" (both are 0 and both mean nothing is kept resident).
		// When both are 0 it is "no prewarming / nothing resident", and the refiller and the return path idle
		// naturally.
		if lang.Idle.Max == 0 {
			lang.Idle.Max = lang.Idle.Initial
		}
		c.Code.Pools[name] = lang
	}
}

// ExecLimits computes the final cap of a simplex request:
//
//	use-case default → request override (non-zero fields) → global clamp
func (c *Config) ExecLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Exec.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// ScriptLimits computes the final cap of a duplex request (as above).
func (c *Config) ScriptLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Code.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// Languages merges "the top-level language definitions" with "the duplex policy" into the runtime configuration (the
// entry name resolved for the current mode).
//
// It only produces the languages listed in code.pools — those are the duplex languages this service pools and
// serves; extra definitions in the top-level langs (for example a language only used by simplex scripts) do not
// appear here, and a language missing from pools reports unsupported_lang to the caller (that is the effect of
// "commenting out the whole entry"). Any entry name that cannot be resolved fails at startup (rather than waiting
// for pool prewarming).
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

// ErrUnsupportedLang means the requested language has no definition in the top-level langs.
// A sentinel error is used so the caller can map it onto a protocol code (unsupported_lang).
var ErrUnsupportedLang = errors.New("unsupported language")

// ResolveLangCommand returns the interpreter entry name of a language under the current mode (for simplex use).
//
// The difference from Languages(): that one only serves the duplex languages listed in code.pools; this one covers
// every definition in the top-level langs — a language used only for simplex work (for example running skill
// scripts) does not have to enable a duplex pool.
func (c *Config) ResolveLangCommand(lang string) (string, error) {
	def, ok := c.Langs[lang]
	if !ok {
		return "", fmt.Errorf("%w: %q (no definition in langs)", ErrUnsupportedLang, lang)
	}
	return def.ResolveCommand(c.Mode)
}

// ResolveCodehostBinary returns the entry name of the codehost binary under the current mode: binaries[mode] first,
// then the general default binary. Same "report an error when nothing resolves" as ResolveCommand — since L3
// codehost is a prerequisite for script execution, and degrading to empty silently only blows up at the first script
// call as "start: exec: no command".
func (c *Config) ResolveCodehostBinary(mode string) (string, error) {
	if b, ok := c.Codehost.Binaries[mode]; ok && b != "" {
		return b, nil
	}
	if c.Codehost.Binary != "" {
		return c.Codehost.Binary, nil
	}
	return "", fmt.Errorf("codehost binary not configured (set codehost.binary, or codehost.binaries.%s)", mode)
}

// ResolveBrowserBinary returns the entry name of the browserhost binary under the current mode: binaries[mode]
// first, then the general default binary. Same "report an error when nothing resolves" as ResolveCodehostBinary —
// degrading to empty silently only blows up at the first browser call as "start: exec: no command", by which time
// the model has already planned the page operations.
func (c *Config) ResolveBrowserBinary(mode string) (string, error) {
	if b, ok := c.Browser.Binaries[mode]; ok && b != "" {
		return b, nil
	}
	if c.Browser.Binary != "" {
		return c.Browser.Binary, nil
	}
	return "", fmt.Errorf("browserhost binary not configured (set browser.binary, or browser.binaries.%s)", mode)
}

// BrowserLimits computes the final cap of the browser process:
//
//	browser.limits → request override (non-zero fields) → global clamp
func (c *Config) BrowserLimits(req sandbox.Limits) sandbox.Limits {
	return Clamp(sandbox.MergeLimits(c.Browser.Limits.ToSandbox(), req), c.Limits.ToSandbox())
}

// ResolveBrowserExecPath returns the browser executable path under the current mode.
//
// The fallback order matches command/commands in langs: exec_paths[mode] → exec_path → empty.
// Empty is not an error: browserhost discovers it inside the sandbox (chromium under docker, Chrome/Edge on the
// host), which is exactly the "do not guess when it is not configured" default.
func (c *Config) ResolveBrowserExecPath(mode string) string {
	if p, ok := c.Browser.ExecPaths[mode]; ok && p != "" {
		return p
	}
	return c.Browser.ExecPath
}

// ResolveBrowserProfileDir returns the Chrome profile directory under the current mode.
//
// Order: profile_dirs[mode] → profile_dir → mode default (docker uses /tmp/browser) → empty.
// Empty is not an error: browserhost creates a temp directory and removes it on exit.
func (c *Config) ResolveBrowserProfileDir(mode string) string {
	if p, ok := c.Browser.ProfileDirs[mode]; ok && p != "" {
		return p
	}
	if c.Browser.ProfileDir != "" {
		return c.Browser.ProfileDir
	}
	if mode == isolate.DockerName {
		// The only writable location that dies with the container under a read-only rootfs (see --tmpfs /tmp in
		// containerArgs).
		return "/tmp/browser"
	}
	return ""
}

// ResolveBrowserDownloadsDir returns the download directory under the current mode.
// Order: downloads_dirs[mode] → downloads_dir → empty (browserhost uses <cwd>/downloads).
func (c *Config) ResolveBrowserDownloadsDir(mode string) string {
	if p, ok := c.Browser.DownloadsDirs[mode]; ok && p != "" {
		return p
	}
	return c.Browser.DownloadsDir
}

// AllowHeadedFor returns the effective "headed allowed" value for a mode: allow_headed_modes[mode] first, falling
// back to allow_headed when there is no entry.
func (c *Config) AllowHeadedFor(mode string) bool {
	if v, ok := c.Browser.AllowHeadedModes[mode]; ok {
		return v
	}
	return c.Browser.AllowHeaded
}

// BrowserArgv assembles the browserhost command line.
//
// Why sandboxd assembles it instead of nexus passing it: only this service knows the in-sandbox paths (what
// browserhost is called in the image, where the profile goes), the same reasoning as langs / codehost.
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

// validateBrowser checks whether the browser config is consistent with the current isolation mode.
//
// The criterion is the same as validateNetwork: **the declaration must match the fact**.
//   - the nsjail rootfs does not have the whole runtime Chromium needs (/proc, /dev/shm, fonts, user namespace), so
//     this version does not support it; enabling it refuses startup outright instead of failing when the model opens
//     a page.
//   - headed needs a real display environment, which currently only holds for mode=none (sandboxd running on a
//     machine with a desktop); passing headed under docker / nsjail only yields an invisible window.
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

// Clamp limits v to ceiling (a 0 dimension in ceiling means no limit).
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
