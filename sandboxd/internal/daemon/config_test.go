package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The config.yaml shipped with the package must load.
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
	// Slot-wait time: one default at the section level, not repeated per language (a repeated value carries no
	// information). 15s follows from the fact that "a single python / js script can be pulled to tens of seconds by
	// a callback" (see the comment in config.yaml).
	if cfg.Code.AcquireTimeoutSec != 15 {
		t.Errorf("code.acquire_timeout_sec = %d, want 15", cfg.Code.AcquireTimeoutSec)
	}
	if lua.AcquireTimeoutSec != 15 {
		t.Errorf("lua did not inherit the slot-wait default of the script section: %d", lua.AcquireTimeoutSec)
	}
	// The shipped config uses only new keys, so there should not be any backfill warning.
	if len(cfg.Warnings) != 0 {
		t.Errorf("the shipped config must not produce warnings: %v", cfg.Warnings)
	}
}

// Interactive detail policy: slow by default (safe in production), off records nothing, slow uses the threshold
// (inclusive), all records everything.
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
		t.Errorf("default code_log = %q, want %q", def.CodeLog, CodeLogSlow)
	}
	if def.CodeLogSlowMS != 200 {
		t.Errorf("default code_log_slow_ms = %d, want 200", def.CodeLogSlowMS)
	}

	cases := []struct {
		name   string
		mode   string
		slowMS int
		durMS  int
		want   bool
	}{
		{"off does not record a short session", CodeLogOff, 200, 1, false},
		{"off does not record a long session either", CodeLogOff, 200, 9999, false},
		{"slow below the threshold does not record", CodeLogSlow, 200, 199, false},
		{"slow above the threshold records", CodeLogSlow, 200, 201, true},
		{"slow equal to the threshold records (inclusive)", CodeLogSlow, 200, 200, true},
		{"all always records", CodeLogAll, 200, 0, true},
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

// A misspelled script_log must fail at startup instead of silently falling back to the default.
func TestScriptLogValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("script_log: always\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error for script_log=always, got nil")
	}
}

// Legacy keys must be backfilled rather than silently falling back to the default — that is the most dangerous path
// of a rename: once the old value is covered by a default filled in by applyDefaults, whoever configured off / all
// would only notice the log behaviour changing for no reason.
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
		t.Errorf("old key session_log did not take effect: code_log = %q, want %q", cfg.CodeLog, CodeLogOff)
	}
	if cfg.CodeLogSlowMS != 50 {
		t.Errorf("old key session_log_slow_ms did not take effect: code_log_slow_ms = %d, want 50", cfg.CodeLogSlowMS)
	}
	if cfg.CodeMaxSec != 300 {
		t.Errorf("old key session_idle_sec did not take effect: code_max_sec = %d, want 300", cfg.CodeMaxSec)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("old section session did not take effect: code.pools.lua.max_concurrent = %d, want 3", got)
	}

	// One warning per old key, none may be missed (the missed one is the one that mysteriously stops working later).
	// 5 = session_log / session_idle_sec / session_log_slow_ms / the session section
	//     + the in-section langs → pools (session.langs is the two-levels-older form, so it is moved twice).
	if len(cfg.Warnings) != 5 {
		t.Errorf("got %d backfill warnings, want 5: %v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"session_log", "session_idle_sec", "session_log_slow_ms", "session", "code.langs"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("the warnings do not mention old key %q: %v", key, cfg.Warnings)
		}
	}
}

// interact → code is the previous rename layer (one newer than session): it must be backfilled and warned about as
// well. It goes through the same backfill machinery as session, but needs the extra check that "the whole interact
// section" can be moved over too.
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
		t.Errorf("old key interact_log did not take effect: code_log = %q, want %q", cfg.CodeLog, CodeLogAll)
	}
	if cfg.CodeMaxSec != 240 {
		t.Errorf("old key interact_max_sec did not take effect: code_max_sec = %d, want 240", cfg.CodeMaxSec)
	}
	if cfg.CodeLogSlowMS != 80 {
		t.Errorf("old key interact_log_slow_ms did not take effect: code_log_slow_ms = %d, want 80", cfg.CodeLogSlowMS)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("old section interact did not take effect: code.pools.lua.max_concurrent = %d, want 3", got)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("old section interact.acquire_timeout_sec did not take effect: %d, want 7", cfg.Code.AcquireTimeoutSec)
	}

	// 4 = interact_log / interact_max_sec / interact_log_slow_ms / the interact section.
	if len(cfg.Warnings) != 4 {
		t.Errorf("got %d backfill warnings, want 4: %v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"interact_log", "interact_max_sec", "interact_log_slow_ms", "interact"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("the warnings do not mention old key %q: %v", key, cfg.Warnings)
		}
	}
}

// script → code (2026-10) is the third backfill layer and the **highest priority**.
//
// It is closest to the present and must be judged before interact / session: otherwise, with both a script and a
// session section written, the older key would take the new section first and the newer script would be treated as
// "the ignored old key".
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
		t.Errorf("old key script_log did not take effect: %q, want %q", cfg.CodeLog, CodeLogAll)
	}
	if cfg.CodeMaxSec != 240 {
		t.Errorf("old key script_max_sec did not take effect: %d, want 240", cfg.CodeMaxSec)
	}
	if cfg.CodeLogSlowMS != 80 {
		t.Errorf("old key script_log_slow_ms did not take effect: %d, want 80", cfg.CodeLogSlowMS)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 3 {
		t.Errorf("old section script did not take effect: code.pools.lua.max_concurrent = %d, want 3", got)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("old section script.acquire_timeout_sec did not take effect: %d, want 7", cfg.Code.AcquireTimeoutSec)
	}

	// 4 = script_log / script_max_sec / script_log_slow_ms / the script section.
	if len(cfg.Warnings) != 4 {
		t.Errorf("got %d backfill warnings, want 4: %v", len(cfg.Warnings), cfg.Warnings)
	}
	for _, key := range []string{"script_log", "script_max_sec", "script_log_slow_ms"} {
		if !warnsAbout(cfg.Warnings, key) {
			t.Errorf("the warnings do not mention old key %q: %v", key, cfg.Warnings)
		}
	}
	if !warnsAbout(cfg.Warnings, "config key script was renamed to code") {
		t.Errorf("the warnings do not mention the old section script: %v", cfg.Warnings)
	}
}

// When all three layers appear, the newer one must win (script > session), and the old layer must be warned as
// ignored.
func TestScriptLegacyBeatsSession(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n" +
		"script:\n  pools:\n    lua:\n      max_concurrent: 5\n" +
		"session:\n  pools:\n    lua:\n      max_concurrent: 9\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].MaxConcurrent; got != 5 {
		t.Errorf("max_concurrent = %d, want 5 (script is newer than session and must win)", got)
	}
	if !warnsAbout(cfg.Warnings, "session is ignored") {
		t.Errorf("want a \"session is ignored\" warning, got %v", cfg.Warnings)
	}
}

// When both are written the new key wins, and the ignored old key must be warned about explicitly — do not choose
// silently, otherwise the old key gives the illusion of "still in effect".
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
		t.Errorf("code_log = %q, want %q (the new key wins)", cfg.CodeLog, CodeLogAll)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "session_log") {
		t.Errorf("want one warning about session_log being ignored, got %v", cfg.Warnings)
	}
}

// With only new keys there must be no warning: the backfill criterion is "new key empty + old key present" and must
// not catch a normal config.
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
		t.Errorf("a new-key config must not produce warnings, got %v", cfg.Warnings)
	}
	if cfg.CodeLog != CodeLogOff || cfg.CodeMaxSec != 60 {
		t.Errorf("new keys did not take effect: code_log=%q code_max_sec=%d", cfg.CodeLog, cfg.CodeMaxSec)
	}
}

// warnsAbout reports whether some warning mentions an old key.
func warnsAbout(warnings []string, key string) bool {
	for _, w := range warnings {
		if strings.Contains(w, key) {
			return true
		}
	}
	return false
}

// Resolving the interpreter entry per mode: a commands hit wins, otherwise it falls back to the general command; when
// neither exists it must report an error (degrading to an empty command only surfaces at pool prewarming).
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
		"docker": "lua", // docker is not configured in commands -> fall back to the general default
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

	// Only commands configured, no general default: an uncovered mode must report an error
	only := LangDef{CommandByMode: map[string]string{"docker": "lua"}}
	if _, err := only.ResolveCommand("nsjail"); err == nil {
		t.Error("want error when mode neither overridden nor fallback, got nil")
	}
	// Nothing configured at all must report an error too
	if _, err := (LangDef{}).ResolveCommand("docker"); err == nil {
		t.Error("want error for empty lang def, got nil")
	}
}

// The keys of commands must be known modes: a typo must fail at startup, otherwise the override silently does
// nothing.
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

// A duplex policy must hang off a language defined at the top level: otherwise assembly cannot find the interpreter
// entry.
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

// Languages() merges the two sections into the runtime config: the entry name is resolved for the current mode and
// the policy fields are carried over as-is.
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
		t.Fatalf("Languages() is missing lua: %+v", langs)
	}
	if lua.Command != "lua" {
		t.Errorf("Command = %q, want %q (docker must fall back to the general default)", lua.Command, "lua")
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("the policy field was not carried over: max_concurrent=%d", lua.MaxConcurrent)
	}
	if lua.Idle != (PoolConfig{Initial: 2, Max: 16}) {
		t.Errorf("idle was not carried over: %+v", lua.Idle)
	}

	// An unresolvable entry name must fail at merge time (rather than waiting for pool prewarming)
	bad := &Config{
		Mode:  "docker",
		Langs: map[string]LangDef{"lua": {CommandByMode: map[string]string{"none": "lua54.exe"}}},
		Code:  CodeConfig{Pools: map[string]CodeLang{"lua": {}}},
	}
	if _, err := bad.Languages(); err == nil {
		t.Error("want error when no command resolvable for current mode, got nil")
	}
}

// The shipped config sets python's commands explicitly per mode (previously only written in a comment), and every
// language resolves to a non-empty command under the current mode.
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
		t.Errorf("python mode none = %q, %v (the local debugging path must have a value)", got, rerr)
	}

	// Under the current mode (docker) every language listed for duplex must merge into a non-empty entry name
	langs, merr := cfg.Languages()
	if merr != nil {
		t.Fatalf("Languages(): %v", merr)
	}
	if len(langs) != len(cfg.Code.Pools) {
		t.Errorf("merged %d languages, script.pools has %d", len(langs), len(cfg.Code.Pools))
	}
	for name, lang := range langs {
		if lang.Command == "" {
			t.Errorf("lang %q: the entry name is empty after merging", name)
		}
	}
}

// Locating fields support ${VAR}: switching machines needs no config edit, an environment variable override is
// enough. Referencing an unset variable must report an error — expanded into an empty string it would be taken as
// "not configured" by ResolveCommand, silently falling back to the general default, so the override does not take
// effect with no trace.
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

	// An unset variable: expandEnv runs before validate, so a complete config is not needed here
	_, err = loadYAML(t, "mode: none\nlangs:\n  python:\n    commands:\n      none: \"${FLAME_TEST_UNSET_XYZ}\"\n")
	if err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("referencing an unset variable should report an error, got %v", err)
	}
}

// loadYAML writes a config snippet to a temp file and loads it (returning the error so the case can assert on it).
func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

// luaPool assembles a minimal config of "top-level langs + a script.pools.lua fragment".
func luaPool(langBody string) string {
	return "langs:\n  lua:\n    command: lua\ncode:\n  pools:\n    lua:\n" + langBody
}

// Both idle values respect the zero value: an explicit 0 is not covered by a default (applyDefaults used to turn
// initial: 0 into 1 without a word).
func TestIdleZeroIsPreserved(t *testing.T) {
	cases := []struct {
		name string
		idle string
		want PoolConfig
	}{
		{"no prewarming but kept resident", "      idle:\n        initial: 0\n        max: 8\n", PoolConfig{Initial: 0, Max: 8}},
		{"no prewarming, nothing resident", "      idle:\n        initial: 0\n        max: 0\n", PoolConfig{}},
		{"only max written", "      idle:\n        max: 8\n", PoolConfig{Initial: 0, Max: 8}},
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
				t.Errorf("a new-key config must not have warnings: %v", cfg.Warnings)
			}
		})
	}
}

// The max default follows initial (rather than a fixed 4): writing only initial does not change it into another
// value, and there is no need to tell "max not written" apart from "max: 0" (both are 0 and both mean nothing is
// kept resident).
func TestIdleMaxDefaultsToInitial(t *testing.T) {
	cases := []struct {
		name string
		idle string
		want PoolConfig
	}{
		{"whole section omitted", "", PoolConfig{}},
		{"only initial written", "      idle:\n        initial: 2\n", PoolConfig{Initial: 2, Max: 2}},
		{"initial greater than 4", "      idle:\n        initial: 8\n", PoolConfig{Initial: 8, Max: 8}},
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

// initial > max must fail at startup: otherwise the refiller would top idle objects up to initial but cannot push
// them into a pool with capacity max, spinning "start one, kill one" every second (at L2, a container created and
// destroyed every second).
func TestIdleInitialGreaterThanMaxRejected(t *testing.T) {
	body := luaPool("      idle:\n        initial: 8\n        max: 4\n")
	if _, err := loadYAML(t, body); err == nil {
		t.Fatal("want error for initial > max, got nil")
	}
}

// Language-level old key: pools.<lang>.pool → pools.<lang>.idle.
//
// Under the old semantics enabled was the master switch, so the translation must be faithful:
//   - enabled is not true (including unwritten) → the old behaviour was "idle pool not enabled", initial/max were
//     ignored;
//   - enabled: true → filled from the old defaults (initial 1 / max 4), with initial > max reproducing the old
//     clamp.
//
// Every tier must speak up, especially "numbers configured but ignored".
func TestIdleLegacyPoolBackfill(t *testing.T) {
	cases := []struct {
		name      string
		pool      string
		want      PoolConfig
		wantWarns int
		mention   string
	}{
		{"enabled: true with numbers → numbers as-is",
			"        enabled: true\n        initial: 2\n        max: 16\n", PoolConfig{Initial: 2, Max: 16}, 2, "enabled"},
		{"enabled: true without numbers → the old default {1,4}",
			"        enabled: true\n", PoolConfig{Initial: 1, Max: 4}, 2, "enabled"},
		{"enabled: true with initial > max → the old clamp reproduced",
			"        enabled: true\n        initial: 5\n", PoolConfig{Initial: 4, Max: 4}, 2, "enabled"},
		{"enabled: false → no prewarming, nothing resident",
			"        enabled: false\n        initial: 2\n", PoolConfig{}, 3, "did not enable the idle pool"},
		{"enabled unwritten (the old semantics also did not enable it)",
			"        initial: 2\n        max: 16\n", PoolConfig{}, 2, "did not enable the idle pool"},
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
				t.Errorf("got %d warnings, want %d: %v", len(cfg.Warnings), tc.wantWarns, cfg.Warnings)
			}
			if !warnsAbout(cfg.Warnings, "code.pools.lua.pool") {
				t.Errorf("the warnings should say which old key it was: %v", cfg.Warnings)
			}
			if !warnsAbout(cfg.Warnings, tc.mention) {
				t.Errorf("the warnings should mention %q: %v", tc.mention, cfg.Warnings)
			}
		})
	}
}

// In-section old key: code.langs → code.pools (the key cross-constraint still applies).
func TestCodeLangsLegacyBackfill(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n" +
		"code:\n  langs:\n    lua:\n      max_concurrent: 3\n      idle:\n        initial: 2\n        max: 8\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	lua, ok := cfg.Code.Pools["lua"]
	if !ok {
		t.Fatalf("the old section langs was not moved to pools: %+v", cfg.Code)
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("max_concurrent = %d, want 3", lua.MaxConcurrent)
	}
	if lua.Idle != (PoolConfig{Initial: 2, Max: 8}) {
		t.Errorf("idle = %+v, want {2 8}", lua.Idle)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "code.langs") {
		t.Errorf("want one code.langs → code.pools warning, got %v", cfg.Warnings)
	}
}

// enabled is still written inside idle: the key was removed, so speak up but leave the user's numbers alone.
func TestEnabledInsideIdleWarns(t *testing.T) {
	body := luaPool("      idle:\n        enabled: true\n        initial: 0\n        max: 4\n")
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].Idle; got.Initial != 0 || got.Max != 4 {
		t.Errorf("idle = %+v, want {0 4} (enabled must not rewrite the numbers)", got)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "enabled") {
		t.Errorf("want one warning about enabled, got %v", cfg.Warnings)
	}
}

// Top-level acquire_timeout_sec (old) → script.acquire_timeout_sec (new), inherited per language.
func TestAcquireTimeoutLegacyBackfill(t *testing.T) {
	cfg, err := loadYAML(t, "acquire_timeout_sec: 7\n"+luaPool(""))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.AcquireTimeoutSec; got != 7 {
		t.Errorf("code.acquire_timeout_sec = %d, want 7", got)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 7 {
		t.Errorf("the language level did not inherit the section default: %d, want 7", got)
	}
	if len(cfg.Warnings) != 1 || !warnsAbout(cfg.Warnings, "code.acquire_timeout_sec") {
		t.Errorf("want one \"renamed to code.acquire_timeout_sec\" warning, got %v", cfg.Warnings)
	}
}

// Both written: the one in the code section wins, with an explicit warning that the old key is ignored.
func TestAcquireTimeoutNewWins(t *testing.T) {
	body := "acquire_timeout_sec: 7\nlangs:\n  lua:\n    command: lua\n" +
		"code:\n  acquire_timeout_sec: 3\n  pools:\n    lua:\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.AcquireTimeoutSec; got != 3 {
		t.Errorf("code.acquire_timeout_sec = %d, want 3 (the new key wins)", got)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 3 {
		t.Errorf("language level = %d, want 3", got)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "is ignored") {
		t.Errorf("want one \"old key is ignored\" warning, got %v", cfg.Warnings)
	}
}

// Ordering regression guard: the top-level acquire_timeout_sec is an **old key**, and the backfill must not write
// "the code section is non-empty" ahead of time — otherwise a config of "session: {langs: ...} +
// acquire_timeout_sec" would be judged by isZero as "the new section is written" and the whole session section would
// be dropped (symptom: every duplex call for that language gets unsupported_lang).
func TestAcquireTimeoutLegacyDoesNotBlockSessionSection(t *testing.T) {
	body := "acquire_timeout_sec: 7\nlangs:\n  lua:\n    command: lua\n" +
		"session:\n  langs:\n    lua:\n      max_concurrent: 3\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	lua, ok := cfg.Code.Pools["lua"]
	if !ok {
		t.Fatalf("the session section was dropped (acquire_timeout_sec must not take part in the isZero check): %+v", cfg.Code)
	}
	if lua.MaxConcurrent != 3 {
		t.Errorf("session.langs did not take effect: max_concurrent = %d, want 3", lua.MaxConcurrent)
	}
	if cfg.Code.AcquireTimeoutSec != 7 {
		t.Errorf("acquire_timeout_sec was not backfilled: %d, want 7", cfg.Code.AcquireTimeoutSec)
	}
	// Three: session → script, code.langs → code.pools,
	//        acquire_timeout_sec → code.acquire_timeout_sec.
	if len(cfg.Warnings) != 3 {
		t.Errorf("got %d backfill warnings, want 3: %v", len(cfg.Warnings), cfg.Warnings)
	}
}

// A language-level override beats the script section default; a language without one inherits the default.
func TestAcquireTimeoutLanguageOverride(t *testing.T) {
	body := "langs:\n  lua:\n    command: lua\n  js:\n    command: node\n" +
		"code:\n  acquire_timeout_sec: 7\n  pools:\n    lua:\n      acquire_timeout_sec: 2\n    js:\n"
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].AcquireTimeoutSec; got != 2 {
		t.Errorf("lua's override = %d, want 2", got)
	}
	if got := cfg.Code.Pools["js"].AcquireTimeoutSec; got != 7 {
		t.Errorf("js should inherit the section default = %d, want 7", got)
	}
}

// Both written: the new key wins, with an explicit warning that the old key is ignored (do not guess which one is
// more trustworthy).
func TestIdleNewWinsOverPool(t *testing.T) {
	body := luaPool("      idle:\n        initial: 1\n        max: 4\n      pool:\n        initial: 9\n        max: 9\n")
	cfg, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Code.Pools["lua"].Idle; got != (PoolConfig{Initial: 1, Max: 4}) {
		t.Errorf("idle = %+v, want {1 4} (the new key wins)", got)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "is ignored") {
		t.Errorf("want one \"old key is ignored\" warning, got %v", cfg.Warnings)
	}
}

// tmp_dir is cleaned at startup, so a value that would take the machine (or the deployment) down with it has to fail at
// load time instead of being discovered on the next restart.
func TestConfigPathValidation(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := filepath.Abs(string(filepath.Separator))
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}
	parent := filepath.Dir(cwd)

	type tc struct {
		name string
		body string
		want string
	}
	cases := []tc{
		{"tmp_dir is a filesystem root", "tmp_dir: '" + root + "'\n", "filesystem root"},
		{"log_dir is a filesystem root", "log_dir: '" + root + "'\n", "filesystem root"},
		{"tmp_dir is the cwd", "tmp_dir: .\n", "working directory"},
	}
	if parent != root {
		cases = append(cases, tc{"tmp_dir contains the cwd", "tmp_dir: '" + parent + "'\n", "working directory"})
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// The wording depends on whether the home directory happens to contain the cwd, but both messages end with the
		// same advice — which is the part the operator needs to read.
		cases = append(cases, tc{"tmp_dir is the home directory", "tmp_dir: '" + home + "'\n", "dedicated directory"})
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadYAML(t, c.body)
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to contain %q", err, c.want)
			}
		})
	}
}

// The directory settings are resolved against the startup cwd: the startup log, the validation and prepareTmpRoot must
// all see the same absolute path, and "logs" must not silently mean "wherever the process happened to start".
func TestConfigPathsResolvedToAbsolute(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "tmp")
	logs := filepath.Join(t.TempDir(), "logs")
	t.Setenv("SANDBOXD_TEST_TMP", tmp)
	t.Setenv("SANDBOXD_TEST_LOG", logs)
	cfg, err := loadYAML(t, "log_dir: \"${SANDBOXD_TEST_LOG}\"\ntmp_dir: \"${SANDBOXD_TEST_TMP}\"\n")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.TmpDir != tmp {
		t.Errorf("tmp_dir = %q, want %q (expansion, then absolute)", cfg.TmpDir, tmp)
	}
	if cfg.LogDir != logs {
		t.Errorf("log_dir = %q, want %q", cfg.LogDir, logs)
	}

	// Both defaults are written relative in the shipped config, so the default path must be absolute too.
	def, err := loadYAML(t, "listen: \"127.0.0.1:0\"\n")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if !filepath.IsAbs(def.TmpDir) || !filepath.IsAbs(def.LogDir) {
		t.Errorf("defaults are not absolute: log_dir=%q tmp_dir=%q", def.LogDir, def.TmpDir)
	}
	if filepath.Base(def.TmpDir) != "tmp" || filepath.Base(def.LogDir) != "logs" {
		t.Errorf("defaults lost their names: log_dir=%q tmp_dir=%q", def.LogDir, def.TmpDir)
	}
}
