package daemon

import (
	"strings"
	"testing"

	"flame/tools/sandbox"
)

// The browser config must be consistent with the isolation mode: whatever is declared must actually be possible.
//
// These cases all guard the same thing — do not let a "configured but can never take effect" combination survive
// into runtime. The model would first plan a whole round of page operations and only then blow up on some action.
func TestBrowserValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "nsjail does not support the browser",
			// Settle the network declaration first, otherwise validateNetwork fails before we get here (which also
			// shows the checks are ordered, so a case must test exactly one thing).
			body:    "mode: nsjail\nnetwork: on\nbrowser:\n  enabled: true\n",
			wantErr: "not supported",
		},
		{
			name:    "headed must be allowed first",
			body:    "mode: none\nbrowser:\n  enabled: true\n  headed: true\n",
			wantErr: "allow_headed",
		},
		{
			name:    "docker cannot allow headed",
			body:    "mode: docker\nbrowser:\n  enabled: true\n  allow_headed: true\n",
			wantErr: "only supported with mode",
		},
		{
			// The override table is no back door for non-none modes: writing true for a mode is as bad as writing
			// the global true.
			name:    "docker rejected the same way when headed is allowed per mode",
			body:    "mode: docker\nnetwork: on\ndocker:\n  network: bridge\nbrowser:\n  enabled: true\n  allow_headed_modes:\n    docker: true\n",
			wantErr: "only supported with mode",
		},
		{
			name:    "misspelled mode key",
			body:    "browser:\n  allow_headed_modes:\n    dockerr: true\n",
			wantErr: "unknown mode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAML(t, tc.body)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// mode=none + headed explicitly allowed + headed on: the only combination that holds for local development.
func TestBrowserHeadedAllowedOnlyOnNone(t *testing.T) {
	cfg, err := loadYAML(t, "mode: none\nbrowser:\n  enabled: true\n  allow_headed: true\n  headed: true\n")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Browser.Enabled || !cfg.Browser.Headed || !cfg.Browser.AllowHeaded {
		t.Fatalf("browser config not preserved: %+v", cfg.Browser)
	}

	argv, err := cfg.BrowserArgv(cfg.Mode)
	if err != nil {
		t.Fatalf("BrowserArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"browserhost", "--allow-headed", "--default-headed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv = %q, want it to contain %q", joined, want)
		}
	}
}

// allow_headed works like exec_path: override per mode, and a mode with no entry falls back to the general value.
// This config is the canonical way to write "one config that fits both the host and docker".
func TestBrowserAllowHeadedPerMode(t *testing.T) {
	cfg, err := loadYAML(t, "mode: none\nbrowser:\n  enabled: true\n"+
		"  allow_headed: false\n  allow_headed_modes:\n    none: true\n")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.AllowHeadedFor("none") {
		t.Error("AllowHeadedFor(none) = false, want true")
	}
	if cfg.AllowHeadedFor("docker") {
		t.Error("AllowHeadedFor(docker) = true, want false (a missing entry must fall back to the general value)")
	}
	argv, err := cfg.BrowserArgv("docker")
	if err != nil {
		t.Fatalf("BrowserArgv: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "--allow-headed") {
		t.Errorf("docker must not carry --allow-headed: %v", argv)
	}
}

// exec_path is an "in-sandbox path": the host path under none, the image path under docker, so it must be
// overridable per mode; a mode with no entry falls back to the general value, and when neither exists it is left
// to automatic discovery.
func TestBrowserExecPathPerMode(t *testing.T) {
	cfg := &Config{Browser: BrowserConfig{
		ExecPath: "/opt/chrome",
		ExecPaths: map[string]string{
			"docker": "/usr/bin/chromium",
			"none":   "",
		},
	}}

	cases := []struct {
		mode string
		want string
	}{
		{"docker", "/usr/bin/chromium"},
		{"nsjail", "/opt/chrome"}, // no entry -> fall back to the general value
		{"none", "/opt/chrome"},   // an empty value is not an override (empty = auto-discover, not "explicitly cleared")
	}
	for _, tc := range cases {
		if got := cfg.ResolveBrowserExecPath(tc.mode); got != tc.want {
			t.Errorf("mode %s: exec path = %q, want %q", tc.mode, got, tc.want)
		}
	}

	// A misspelled mode must fail at startup: otherwise the override silently does nothing until the first open.
	if _, err := loadYAML(t, "browser:\n  exec_paths:\n    dockerr: /usr/bin/chromium\n"); err == nil {
		t.Fatal("want error for unknown mode in browser.exec_paths, got nil")
	}
	if _, err := loadYAML(t, "browser:\n  binaries:\n    dockerr: browserhost\n"); err == nil {
		t.Fatal("want error for unknown mode in browser.binaries, got nil")
	}
}

// The argv assembled under docker must carry the in-image browser path and the /tmp default profile (the latter
// relies on containerArgs' --tmpfs /tmp, so the two are a pair).
func TestBrowserArgvDocker(t *testing.T) {
	cfg := &Config{Mode: "docker", Browser: BrowserConfig{
		Enabled:   true,
		Binary:    "browserhost",
		ExecPaths: map[string]string{"docker": "/usr/bin/chromium"},
	}}
	argv, err := cfg.BrowserArgv("docker")
	if err != nil {
		t.Fatalf("BrowserArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"browserhost",
		"--exec-path /usr/bin/chromium",
		"--profile /tmp/browser",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv = %q, want it to contain %q", joined, want)
		}
	}
	if strings.Contains(joined, "--allow-headed") {
		t.Errorf("--allow-headed present although allow_headed is off: %q", joined)
	}
}

// profile / downloads are in-sandbox paths as well: override per mode, fall back to the general value on a miss;
// profile has one extra rule — the docker default /tmp/browser — and it must apply at resolve time, never be
// written back into the field, otherwise switching modes would drag the docker path onto none.
func TestBrowserPathsPerMode(t *testing.T) {
	cfg := &Config{Browser: BrowserConfig{
		ProfileDir:    "/prof",
		ProfileDirs:   map[string]string{"docker": "/d/prof"},
		DownloadsDir:  "/dl",
		DownloadsDirs: map[string]string{"docker": "/workspace/dl"},
	}}

	for _, tc := range []struct {
		mode string
		prof string
		dl   string
	}{
		{"docker", "/d/prof", "/workspace/dl"},
		{"none", "/prof", "/dl"},
		{"nsjail", "/prof", "/dl"},
	} {
		if got := cfg.ResolveBrowserProfileDir(tc.mode); got != tc.prof {
			t.Errorf("mode %s: profile = %q, want %q", tc.mode, got, tc.prof)
		}
		if got := cfg.ResolveBrowserDownloadsDir(tc.mode); got != tc.dl {
			t.Errorf("mode %s: downloads = %q, want %q", tc.mode, got, tc.dl)
		}
	}

	empty := &Config{}
	if got := empty.ResolveBrowserProfileDir("docker"); got != "/tmp/browser" {
		t.Errorf("docker default profile = %q, want /tmp/browser", got)
	}
	if got := empty.ResolveBrowserProfileDir("none"); got != "" {
		t.Errorf("none must not inherit the docker profile default, got %q", got)
	}
	if got := empty.ResolveBrowserDownloadsDir("docker"); got != "" {
		t.Errorf("the downloads default should be empty (<cwd>/downloads), got %q", got)
	}

	for _, body := range []string{
		"browser:\n  profile_dirs:\n    dockerr: /tmp/x\n",
		"browser:\n  downloads_dirs:\n    dockerr: /tmp/x\n",
	} {
		if _, err := loadYAML(t, body); err == nil {
			t.Errorf("want error for unknown mode in %q, got nil", body)
		}
	}
}

// browser.limits only provides defaults; a non-zero field in the request overrides them, and the global cap
// clamps the result in the end.
func TestBrowserLimitsOverrideAndClamp(t *testing.T) {
	cfg, err := loadYAML(t, "mode: none\n"+
		"limits:\n  memory_mb: 512\n"+
		"browser:\n  enabled: true\n  limits:\n    memory_mb: 2048\n")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Browser.Limits.MemoryMB; got != 2048 {
		t.Errorf("browser.limits.memory_mb = %d, want 2048", got)
	}
	// The global cap of 512 is tighter, so the result must be squeezed back to 512.
	got := cfg.BrowserLimits(sandbox.Limits{})
	if got.MemoryMB != 512 {
		t.Errorf("clamped memory = %d, want 512", got.MemoryMB)
	}
}
