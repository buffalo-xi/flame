package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 随包发布的 sandboxd/config.yaml 必须能加载。
func TestShippedConfigLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatalf("load shipped config: %v", err)
	}
	lua, ok := cfg.Code.Pools["lua"]
	if !ok {
		t.Fatalf("shipped config has no lua lang: %+v", cfg.Code.Pools)
	}
	if got := lua.Idle; got != (PoolConfig{Initial: 2, Max: 16}) {
		t.Errorf("lua idle = %+v, want {2 16}", got)
	}
	// 取槽等待：段级缺省一处，语言级不再逐条重复（重复值 = 零信息量）。
	// 15 秒是「python / js 单次脚本可能被回调拉到几十秒」这条事实推出来的（见 config.yaml 注释）。
	if cfg.Code.AcquireTimeoutSec != 15 {
		t.Errorf("code.acquire_timeout_sec = %d, want 15", cfg.Code.AcquireTimeoutSec)
	}
	if lua.AcquireTimeoutSec != 15 {
		t.Errorf("lua 未继承 script 段的取槽等待缺省：%d", lua.AcquireTimeoutSec)
	}
	// 随包配置一律是新键，不该有任何回填告警。
	if len(cfg.Warnings) != 0 {
		t.Errorf("随包配置不该产生告警：%v", cfg.Warnings)
	}
}

// 交互明细策略：缺省 slow（生产安全），off 一律不记，slow 按阈值（含等于），all 全记。
func TestScriptLogEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \"127.0.0.1:0\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	def, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if def.CodeLog != CodeLogSlow {
		t.Errorf("缺省 code_log = %q, want %q", def.CodeLog, CodeLogSlow)
	}
	if def.CodeLogSlowMS != 200 {
		t.Errorf("缺省 code_log_slow_ms = %d, want 200", def.CodeLogSlowMS)
	}

	cases := []struct {
		name   string
		mode   string
		slowMS int
		durMS  int
		want   bool
	}{
		{"off 不记短会话", CodeLogOff, 200, 1, false},
		{"off 也不记长会话", CodeLogOff, 200, 9999, false},
		{"slow 阈值之下不记", CodeLogSlow, 200, 199, false},
		{"slow 阈值之上记", CodeLogSlow, 200, 201, true},
		{"slow 等于阈值要记（含等于语义）", CodeLogSlow, 200, 200, true},
		{"all 一律记", CodeLogAll, 200, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{CodeLog: tc.mode, CodeLogSlowMS: tc.slowMS}
			if got := c.CodeLogEnabled(time.Duration(tc.durMS) * time.Millisecond); got != tc.want {
				t.Errorf("%s dur=%dms = %v, want %v", tc.mode, tc.durMS, got, tc.want)
			}
		})
	}
}

// 拼错的 script_log 必须在启动时失败，而不是静默退回缺省。
func TestScriptLogValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("script_log: always\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error for script_log=always, got nil")
	}
}

// 旧键必须回填而不是静默回落缺省 —— 这是改名最危险的那条路径：
// 旧值一旦被 applyDefaults 补掉的缺省盖住，配过 off / all 的人只会发现日志行为莫名变了。
func TestLegacyKeysBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "session_log: off\n" +
		"session_idle_sec: 300\n" +
		"session_log_slow_ms: 50\n" +
		"langs:\n  lua:\n    command: lua\n" +
		"session:\n  langs:\n    lua:\n      max_concurrent: 3\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.CodeLog != CodeLogOff {
		t.Errorf("旧键 session_log 未生效：code_log = %q, want %q", cfg.CodeLog, CodeLogOff)
	}
	if cfg.CodeLogSlowMS != 50 {
		t.Errorf("旧键 session_log_slow_ms 未生效：code_log_slow_ms = %d, want 50", cfg.CodeLogSlowMS)
	}
	if cfg.CodeMaxSec != 300 {
		t.Errorf("旧键 session_idle_sec 未生效：code_max_sec = %d, want 300", cfg.CodeMaxSec)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("旧段 session 未生效：code.pools.lua.max_concurrent = %d, want 3", got)
	}

	// 五个旧键各响一声，一个都不能漏（漏掉的那个就是将来莫名其妙失效的那个）。
	// 5 = session_log / session_idle_sec / session_log_slow_ms / session 段
	//     + 段内那次 langs → pools（session.langs 是两级更旧的写法，要跨两级搬两次）。
	if len(cfg.Warnings) != 5 {
		t.Errorf("回填告警 %d 条，want 5：%v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"session_log", "session_idle_sec", "session_log_slow_ms", "session", "code.langs"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("告警里没有提到旧键 %q：%v", key, cfg.Warnings)
		}
	}
}

// interact → code 是上一轮更名的旧键（比 session 新一层）：也必须回填 + 告警。
// 它和 session 走的是同一套回填机制，但要多验证一层「interact 段」也能整段搬过来。
func TestInteractLegacyBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "interact_log: all\n" +
		"interact_max_sec: 240\n" +
		"interact_log_slow_ms: 80\n" +
		"langs:\n  lua:\n    command: lua\n" +
		"interact:\n  acquire_timeout_sec: 7\n  pools:\n    lua:\n      max_concurrent: 3\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.CodeLog != CodeLogAll {
		t.Errorf("旧键 interact_log 未生效：code_log = %q, want %q", cfg.CodeLog, CodeLogAll)
	}
	if cfg.CodeMaxSec != 240 {
		t.Errorf("旧键 interact_max_sec 未生效：code_max_sec = %d, want 240", cfg.CodeMaxSec)
	}
	if cfg.CodeLogSlowMS != 80 {
		t.Errorf("旧键 interact_log_slow_ms 未生效：code_log_slow_ms = %d, want 80", cfg.CodeLogSlowMS)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("旧段 interact 未生效：code.pools.lua.max_concurrent = %d, want 3", got)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("旧段 interact.acquire_timeout_sec 未生效：%d, want 7", cfg.Code.AcquireTimeoutSec)
	}

	// 4 = interact_log / interact_max_sec / interact_log_slow_ms / interact 段。
	if len(cfg.Warnings) != 4 {
		t.Errorf("回填告警 %d 条，want 4：%v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"interact_log", "interact_max_sec", "interact_log_slow_ms", "interact"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("告警里没有提到旧键 %q：%v", key, cfg.Warnings)
		}
	}
}

// script → code（2026-10）是第三层回填，也是**第一优先级**。
//
// 它离当前最近，必须排在 interact / session 之前判断：否则同时写 script 段与
// session 段时，更旧的键会先把新段占掉，较新的 script 反被当成「旧键被忽略」。
func TestScriptToCodeLegacyBackfill(t *testing.T) {
	body := "script_log: all\n" +
		"script_max_sec: 240\n" +
		"script_log_slow_ms: 80\n" +
		"langs:\n  lua:\n    command: lua\n" +
		"script:\n  acquire_timeout_sec: 7\n  pools:\n    lua:\n      max_concurrent: 3\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.CodeLog != CodeLogAll {
		t.Errorf("旧键 script_log 未生效：%q, want %q", cfg.CodeLog, CodeLogAll)
	}
	if cfg.CodeMaxSec != 240 {
		t.Errorf("旧键 script_max_sec 未生效：%d, want 240", cfg.CodeMaxSec)
	}
	if cfg.CodeLogSlowMS != 80 {
		t.Errorf("旧键 script_log_slow_ms 未生效：%d, want 80", cfg.CodeLogSlowMS)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("旧段 script 未生效：code.pools.lua.max_concurrent = %d, want 3", got)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("旧段 script.acquire_timeout_sec 未生效：%d, want 7", cfg.Code.AcquireTimeoutSec)
	}

	// 4 = script_log / script_max_sec / script_log_slow_ms / script 段。
	if len(cfg.Warnings) != 4 {
		t.Errorf("回填告警 %d 条，want 4：%v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"script_log", "script_max_sec", "script_log_slow_ms"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("告警里没有提到旧键 %q：%v", key, cfg.Warnings)
		}
	}
	if !warnsAbout(cfg.Warnings, "配置键 script 已更名为 code") {
		t.Errorf("告警里没有提到旧段 script：%v", cfg.Warnings)
	}
}

// 三层同时出现时，较新的那层必须赢（script > session），且旧层要明确告警被忽略。
func TestScriptLegacyBeatsSession(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n" +
		"script:\n  pools:\n    lua:\n      max_concurrent: 5\n" +
		"session:\n  pools:\n    lua:\n      max_concurrent: 9\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 5 {
		t.Errorf("max_concurrent = %d, want 5（script 比 session 新，应赢）", got)
	}
	if !warnsAbout(cfg.Warnings, "session 被忽略") {
		t.Errorf("want 一条「session 被忽略」的告警，got %v", cfg.Warnings)
	}
}

// 新旧都写时以新键为准，且必须明确告警旧键被忽略 —— 不能静默二选一，
// 否则旧键会给人一种「还在生效」的错觉。
func TestLegacyKeysNewWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "code_log: all\nsession_log: off\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.CodeLog != CodeLogAll {
		t.Errorf("code_log = %q, want %q（新键优先）", cfg.CodeLog, CodeLogAll)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "session_log") {
		t.Errorf("want 一条关于 session_log 被忽略的告警，got %v", cfg.Warnings)
	}
}

// 只写新键时不该有任何告警：回填的判据是「新键空 + 旧键有」，不能误伤正常配置。
func TestNoWarningsWithoutLegacyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "code_log: off\ncode_max_sec: 60\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("新键配置不该产生告警，got %v", cfg.Warnings)
	}
	if cfg.CodeLog != CodeLogOff || cfg.CodeMaxSec != 60 {
		t.Errorf("新键未生效：script_log=%q script_max_sec=%d", cfg.CodeLog, cfg.CodeMaxSec)
	}
}

// warnsAbout 判断告警里是否提到过某个旧键。
func warnsAbout(warnings []string, key string) bool {
	for _, w := range warnings {
		if strings.Contains(w, key) {
			return true
		}
	}
	return false
}

// 按 mode 取解释器入口名：commands 命中优先，否则回退通用 command；
// 两者都没有必须报错（退化成空命令会在池预热时才暴露）。
func TestResolveCommand(t *testing.T) {
	lang := LangDef{
		Command: "lua",
		CommandByMode: map[string]string{
			"none":   "d:/tools/lua5.4/lua54.exe",
			"nsjail": "lua5.4",
		},
	}
	for mode, want := range map[string]string{
		"none":   "d:/tools/lua5.4/lua54.exe",
		"nsjail": "lua5.4",
		"docker": "lua", // commands 未配 docker → 回退通用缺省
	} {
		got, err := lang.ResolveCommand(mode)
		if err != nil {
			t.Errorf("mode %q: unexpected error: %v", mode, err)
			continue
		}
		if got != want {
			t.Errorf("mode %q: got %q, want %q", mode, got, want)
		}
	}

	// 只配 commands、没有通用缺省：未覆盖的 mode 必须报错
	only := LangDef{CommandByMode: map[string]string{"docker": "lua"}}
	if _, err := only.ResolveCommand("nsjail"); err == nil {
		t.Error("want error when mode neither overridden nor fallback, got nil")
	}
	// 完全没配也必须报错
	if _, err := (LangDef{}).ResolveCommand("docker"); err == nil {
		t.Error("want error for empty lang def, got nil")
	}
}

// commands 的键必须是已知 mode：拼错要启动即失败，否则覆盖静默失效。
func TestCommandsUnknownModeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "langs:\n  lua:\n    command: lua\n    commands:\n      dockerr: lua\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error for unknown mode in commands, got nil")
	}
}

// 双工策略必须挂在顶层已定义的语言上：否则装配时取不到解释器入口。
func TestCodeLangRequiresTopLevelDef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "code:\n  pools:\n    lua:\n      max_concurrent: 3\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error when script.pools has no matching top-level langs entry, got nil")
	}
}

// Languages() 把两段合并成运行期配置：入口名按当前 mode 解析，策略字段原样带上。
func TestLanguagesMerge(t *testing.T) {
	cfg := &Config{
		Mode: "docker",
		Langs: map[string]LangDef{
			"lua": {Command: "lua", CommandByMode: map[string]string{"none": "lua54.exe"}},
		},
		Code: CodeConfig{Pools: map[string]CodeLang{
			"lua": {MaxConcurrent: 3, Idle: PoolConfig{Initial: 2, Max: 16}},
		}},
	}
	langs, err := cfg.Languages()
	if err != nil {
		t.Fatalf("Languages(): %v", err)
	}
	lua, ok := langs["lua"]
	if !ok {
		t.Fatalf("Languages() 缺少 lua: %+v", langs)
	}
	if lua.Command != "lua" {
		t.Errorf("Command = %q, want %q（docker 下应回退通用缺省）", lua.Command, "lua")
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("策略字段未带过来: max_concurrent=%d", lua.MaxConcurrent)
	}
	if lua.Idle != (PoolConfig{Initial: 2, Max: 16}) {
		t.Errorf("idle 未带过来: %+v", lua.Idle)
	}

	// 取不到入口名必须在合并阶段就报错（而不是等池预热）
	bad := &Config{
		Mode:  "docker",
		Langs: map[string]LangDef{"lua": {CommandByMode: map[string]string{"none": "lua54.exe"}}},
		Code:  CodeConfig{Pools: map[string]CodeLang{"lua": {}}},
	}
	if _, err := bad.Languages(); err == nil {
		t.Error("want error when no command resolvable for current mode, got nil")
	}
}

// 随包配置里 python 用 commands 显式区分各 mode（此前只写在注释里），
// 且当前 mode 下每个语言都能解析出非空命令。
func TestShippedConfigResolvesCommands(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatalf("load shipped config: %v", err)
	}

	py, ok := cfg.Langs["python"]
	if !ok {
		t.Fatal("shipped config has no python lang in top-level langs")
	}
	for mode, want := range map[string]string{
		"docker": "python",
		"nsjail": "python3",
	} {
		got, rerr := py.ResolveCommand(mode)
		if rerr != nil {
			t.Errorf("python mode %q: %v", mode, rerr)
			continue
		}
		if got != want {
			t.Errorf("python mode %q: got %q, want %q", mode, got, want)
		}
	}
	if got, rerr := py.ResolveCommand("none"); rerr != nil || got == "" {
		t.Errorf("python mode none = %q, %v（本机调试路径必须有值）", got, rerr)
	}

	// 当前 mode（docker）下双工列出的每个语言都必须能合并出非空入口名
	langs, merr := cfg.Languages()
	if merr != nil {
		t.Fatalf("Languages(): %v", merr)
	}
	if len(langs) != len(cfg.Code.Pools) {
		t.Errorf("合并后 %d 个语言，script.pools 有 %d 个", len(langs), len(cfg.Code.Pools))
	}
	for name, lang := range langs {
		if lang.Command == "" {
			t.Errorf("lang %q: 合并后入口名为空", name)
		}
	}
}

// 定位类字段支持 ${VAR}：换机器不必改配置，用环境变量覆盖即可。
// 引用了未设置的变量必须报错 —— 展开成空串会被 ResolveCommand 当成「没配」，
// 静默回退到通用缺省，覆盖没生效却毫无迹象。
func TestConfigExpandsEnv(t *testing.T) {
	t.Setenv("FLAME_TEST_PY", "C:/opt/python/python.exe")
	t.Setenv("FLAME_TEST_HOST", "D:/opt/host/codehost.exe")

	const body = `mode: none
network: on
langs:
  python:
    command: python
    commands:
      none: "${FLAME_TEST_PY}"
code:
  pools:
    python:
      idle:
        initial: 0
        max: 0
codehost:
  binary: codehost
  binaries:
    none: "${FLAME_TEST_HOST}"
`
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Langs["python"].CommandByMode["none"]; got != "C:/opt/python/python.exe" {
		t.Errorf("langs.python.commands.none = %q", got)
	}
	if got := cfg.Codehost.Binaries["none"]; got != "D:/opt/host/codehost.exe" {
		t.Errorf("codehost.binaries.none = %q", got)
	}

	// 未设置的变量：expandEnv 在 validate 之前就跑，所以这里不必给完整配置
	_, err = loadYAML(t, "mode: none\nlangs:\n  python:\n    commands:\n      none: \"${FLAME_TEST_UNSET_XYZ}\"\n")
	if err == nil || !strings.Contains(err.Error(), "环境变量未设置") {
		t.Errorf("引用未设置的变量应当报错，got %v", err)
	}
}

// loadYAML 把一段配置写进临时文件并加载（返回错误以便用例自行断言）。
func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

// luaPool 拼一份「顶层 langs + script.pools.lua 片段」的最小配置。
func luaPool(langBody string) string {
	return "langs:\n  lua:\n    command: lua\ncode:\n  pools:\n    lua:\n" + langBody
}

// idle 的两个值都尊重零值：显式 0 不会被缺省盖掉
// （曾经的 applyDefaults 会把 initial: 0 悄悄改成 1）。
func TestIdleZeroIsPreserved(t *testing.T) {
	cases := []struct {
		name string
		idle string
		want PoolConfig
	}{
		{"不预热但留驻", "      idle:\n        initial: 0\n        max: 8\n", PoolConfig{Initial: 0, Max: 8}},
		{"不预热也不留驻", "      idle:\n        initial: 0\n        max: 0\n", PoolConfig{}},
		{"只写 max", "      idle:\n        max: 8\n", PoolConfig{Initial: 0, Max: 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadYAML(t, luaPool(tc.idle))
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if got := cfg.Code.Pools["lua"].Idle; got != tc.want {
				t.Errorf("idle = %+v, want %+v", got, tc.want)
			}
			if len(cfg.Warnings) != 0 {
				t.Errorf("新键配置不该有告警：%v", cfg.Warnings)
			}
		})
	}
}

// max 缺省跟随 initial（不是固定 4）：只写 initial 不会被改成别的值，
// 也不需要区分「没写 max」与「写 max: 0」（两者都是 0、都表示不留驻）。
func TestIdleMaxDefaultsToInitial(t *testing.T) {
	cases := []struct {
		name string
		idle string
		want PoolConfig
	}{
		{"整段省略", "", PoolConfig{}},
		{"只写 initial", "      idle:\n        initial: 2\n", PoolConfig{Initial: 2, Max: 2}},
		{"initial 大于 4", "      idle:\n        initial: 8\n", PoolConfig{Initial: 8, Max: 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadYAML(t, luaPool(tc.idle))
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if got := cfg.Code.Pools["lua"].Idle; got != tc.want {
				t.Errorf("idle = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// initial > max 必须启动即失败：否则 refiller 会把空闲对象补到 initial 却推不进
// 容量为 max 的池，每秒「起一个、杀一个」空转（L2 下是每秒建一个容器再销毁）。
func TestIdleInitialGreaterThanMaxRejected(t *testing.T) {
	body := luaPool("      idle:\n        initial: 8\n        max: 4\n")
	if _, err := loadYAML(t, body); err == nil {
		t.Fatal("want error for initial > max, got nil")
	}
}

// 语言级旧键：pools.<lang>.pool → pools.<lang>.idle。
//
// 旧语义里 enabled 是总开关，所以翻译必须忠实：
//   - enabled 不是 true（含没写）→ 旧的行为是"不启用空闲池"，initial/max 一律被忽略；
//   - enabled: true → 按旧缺省补全（initial 1 / max 4），initial > max 时复现旧的钳制。
//
// 每一档都要出声，尤其"配了数值却被忽略"的那一档。
func TestIdleLegacyPoolBackfill(t *testing.T) {
	cases := []struct {
		name      string
		pool      string
		want      PoolConfig
		wantWarns int
		mention   string
	}{
		{"enabled: true + 数值 → 数值原样",
			"        enabled: true\n        initial: 2\n        max: 16\n", PoolConfig{Initial: 2, Max: 16}, 2, "enabled"},
		{"enabled: true 无数值 → 旧缺省 {1,4}",
			"        enabled: true\n", PoolConfig{Initial: 1, Max: 4}, 2, "enabled"},
		{"enabled: true 且 initial > max → 复现旧钳制",
			"        enabled: true\n        initial: 5\n", PoolConfig{Initial: 4, Max: 4}, 2, "enabled"},
		{"enabled: false → 不预热不留驻",
			"        enabled: false\n        initial: 2\n", PoolConfig{}, 3, "不启用空闲池"},
		{"没写 enabled（旧语义同样是不启用）",
			"        initial: 2\n        max: 16\n", PoolConfig{}, 2, "不启用空闲池"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadYAML(t, luaPool("      pool:\n"+tc.pool))
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if got := cfg.Code.Pools["lua"].Idle; got != tc.want {
				t.Errorf("idle = %+v, want %+v", got, tc.want)
			}
			if len(cfg.Warnings) != tc.wantWarns {
				t.Errorf("告警 %d 条，want %d：%v", len(cfg.Warnings), tc.wantWarns, cfg.Warnings)
			}
			if !warnsAbout(cfg.Warnings, "code.pools.lua.pool") {
				t.Errorf("告警里应指出是哪个旧键：%v", cfg.Warnings)
			}
			if !warnsAbout(cfg.Warnings, tc.mention) {
				t.Errorf("告警里应提到 %q：%v", tc.mention, cfg.Warnings)
			}
		})
	}
}

// 段内旧键：code.langs → code.pools（key 的交叉约束照旧生效）。
func TestCodeLangsLegacyBackfill(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n" +
		"code:\n  langs:\n    lua:\n      max_concurrent: 3\n      idle:\n        initial: 2\n        max: 8\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	lua, ok := cfg.Code.Pools["lua"]
	if !ok {
		t.Fatalf("旧段 langs 未搬到 pools：%+v", cfg.Code)
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("max_concurrent = %d, want 3", lua.MaxConcurrent)
	}
	if lua.Idle != (PoolConfig{Initial: 2, Max: 8}) {
		t.Errorf("idle = %+v, want {2 8}", lua.Idle)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "code.langs") {
		t.Errorf("want 一条 code.langs → code.pools 的告警，got %v", cfg.Warnings)
	}
}

// idle 里还写着 enabled：键已移除，出声但不影响用户写的数值。
func TestEnabledInsideIdleWarns(t *testing.T) {
	body := luaPool("      idle:\n        enabled: true\n        initial: 0\n        max: 4\n")
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].Idle; got.Initial != 0 || got.Max != 4 {
		t.Errorf("idle = %+v, want {0 4}（enabled 不该改写数值）", got)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "enabled") {
		t.Errorf("want 一条关于 enabled 的告警，got %v", cfg.Warnings)
	}
}

// 顶层 acquire_timeout_sec（旧）→ script.acquire_timeout_sec（新），并逐语言继承。
func TestAcquireTimeoutLegacyBackfill(t *testing.T) {
	cfg, err := loadYAML(t, "acquire_timeout_sec: 7\n"+luaPool(""))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.AcquireTimeoutSec; got != 7 {
		t.Errorf("code.acquire_timeout_sec = %d, want 7", got)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 7 {
		t.Errorf("语言级未继承段级缺省：%d, want 7", got)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "code.acquire_timeout_sec") {
		t.Errorf("want 一条「更名到 code.acquire_timeout_sec」的告警，got %v", cfg.Warnings)
	}
}

// 新旧同写：以 code 段里的为准，并明确告警旧键被忽略。
func TestAcquireTimeoutNewWins(t *testing.T) {
	body := "acquire_timeout_sec: 7\nlangs:\n  lua:\n    command: lua\n" +
		"code:\n  acquire_timeout_sec: 3\n  pools:\n    lua:\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.AcquireTimeoutSec; got != 3 {
		t.Errorf("code.acquire_timeout_sec = %d, want 3（新键优先）", got)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 3 {
		t.Errorf("语言级 = %d, want 3", got)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "被忽略") {
		t.Errorf("want 一条「旧键被忽略」的告警，got %v", cfg.Warnings)
	}
}

// 顺序回归守卫：顶层的 acquire_timeout_sec 是**旧键**，回填不能提前把「code 段非空」
// 这件事写进去 —— 否则「session: {langs: ...} + acquire_timeout_sec」的配置会被
// isZero 判成"新段已写"，整个 session 段被丢弃（症状：该语言双工全 unsupported_lang）。
func TestAcquireTimeoutLegacyDoesNotBlockSessionSection(t *testing.T) {
	body := "acquire_timeout_sec: 7\nlangs:\n  lua:\n    command: lua\n" +
		"session:\n  langs:\n    lua:\n      max_concurrent: 3\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	lua, ok := cfg.Code.Pools["lua"]
	if !ok {
		t.Fatalf("session 段被丢弃了（acquire_timeout_sec 不该参与 isZero 判定）：%+v", cfg.Code)
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("session.langs 未生效：max_concurrent = %d, want 3", lua.MaxConcurrent)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("acquire_timeout_sec 未回填：%d, want 7", cfg.Code.AcquireTimeoutSec)
	}
	// 三条：session → script、code.langs → code.pools、
	//       acquire_timeout_sec → script.acquire_timeout_sec。
	if len(cfg.Warnings) != 3 {
		t.Errorf("回填告警 %d 条，want 3：%v", len(cfg.Warnings), cfg.Warnings)
	}
}

// 语言级覆盖优先于 script 段的缺省；没写的语言继承缺省。
func TestAcquireTimeoutLanguageOverride(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n  js:\n    command: node\n" +
		"code:\n  acquire_timeout_sec: 7\n  pools:\n    lua:\n      acquire_timeout_sec: 2\n    js:\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 2 {
		t.Errorf("lua 的覆盖值 = %d, want 2", got)
	}
	if got := cfg.Code.Pools["js"].AcquireTimeoutSec; got != 7 {
		t.Errorf("js 该继承段级缺省 = %d, want 7", got)
	}
}

// 新旧同写：以新键为准，并明确告警旧键被忽略（不猜谁更可信）。
func TestIdleNewWinsOverPool(t *testing.T) {
	body := luaPool("      idle:\n        initial: 1\n        max: 4\n      pool:\n        initial: 9\n        max: 9\n")
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].Idle; got != (PoolConfig{Initial: 1, Max: 4}) {
		t.Errorf("idle = %+v, want {1 4}（新键优先）", got)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "被忽略") {
		t.Errorf("want 一条「旧键被忽略」的告警，got %v", cfg.Warnings)
	}
}
