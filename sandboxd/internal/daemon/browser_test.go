package daemon

import (
	"strings"
	"testing"

	"flame/tools/sandbox"
)

// 浏览器配置必须与隔离方式自洽：声明的东西必须真的能成立。
//
// 这些用例守的是同一件事 —— 不要让「配了但不可能生效」的组合活到运行期。
// 那样模型会先规划好一整轮页面操作，最后才在某个动作上炸掉。
func TestBrowserValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "nsjail 不支持浏览器",
			// 先把网络声明摆平，否则会先撞上 validateNetwork 的错
			// （这也顺带说明：校验是有顺序的，用例必须只测一件事）。
			body:    "mode: nsjail\nnetwork: on\nbrowser:\n  enabled: true\n",
			wantErr: "not supported",
		},
		{
			name:    "有头必须先允许",
			body:    "mode: none\nbrowser:\n  enabled: true\n  headed: true\n",
			wantErr: "allow_headed",
		},
		{
			name:    "docker 下不能允许有头",
			body:    "mode: docker\nbrowser:\n  enabled: true\n  allow_headed: true\n",
			wantErr: "only supported with mode",
		},
		{
			// 覆盖表不给非 none 开后门：按 mode 写 true 与写全局 true 同罪。
			name:    "docker 下按 mode 允许有头同样被拒",
			body:    "mode: docker\nnetwork: on\ndocker:\n  network: bridge\nbrowser:\n  enabled: true\n  allow_headed_modes:\n    docker: true\n",
			wantErr: "only supported with mode",
		},
		{
			name:    "拼错的 mode 键",
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

// mode=none + 显式允许有头 + headed 缺省：这是本机开发唯一成立的组合。
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

// allow_headed 与 exec_path 同款：按 mode 覆盖，未命中的 mode 回退通用值。
// 这份配置就是「一份配置同时适配本机与 docker」的标准写法。
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
		t.Error("AllowHeadedFor(docker) = true, want false（未命中应回退通用值）")
	}
	argv, err := cfg.BrowserArgv("docker")
	if err != nil {
		t.Fatalf("BrowserArgv: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "--allow-headed") {
		t.Errorf("docker 下不该带 --allow-headed: %v", argv)
	}
}

// exec_path 是「沙箱内路径」：none 是宿主路径、docker 是镜像内路径，
// 所以必须能按 mode 覆盖；没写的 mode 回退到通用值，两者都没有则交给自动探测。
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
		{"nsjail", "/opt/chrome"}, // 未覆盖 → 回退通用值
		{"none", "/opt/chrome"},   // 空值不算覆盖（空 = 自动探测，不该被当成「显式置空」）
	}
	for _, tc := range cases {
		if got := cfg.ResolveBrowserExecPath(tc.mode); got != tc.want {
			t.Errorf("mode %s: exec path = %q, want %q", tc.mode, got, tc.want)
		}
	}

	// 拼错的 mode 必须启动即失败：否则覆盖静默不生效，直到第一次 open 才炸。
	if _, err := loadYAML(t, "browser:\n  exec_paths:\n    dockerr: /usr/bin/chromium\n"); err == nil {
		t.Fatal("want error for unknown mode in browser.exec_paths, got nil")
	}
	if _, err := loadYAML(t, "browser:\n  binaries:\n    dockerr: browserhost\n"); err == nil {
		t.Fatal("want error for unknown mode in browser.binaries, got nil")
	}
}

// docker 下组装出来的 argv 必须带上镜像内的浏览器路径与 /tmp 缺省 profile
// （后者依赖 containerArgs 的 --tmpfs /tmp，两处是配套的）。
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
		t.Errorf("没开 allow_headed 却带上了 --allow-headed: %q", joined)
	}
}

// profile / downloads 同样是沙箱内路径：按 mode 覆盖，未命中回退通用值；
// profile 额外有一条「docker 缺省 /tmp/browser」—— 它必须在解析期生效，
// 不能回填进字段，否则切 mode 时会把 docker 的路径带到 none 上。
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
		t.Errorf("docker 缺省 profile = %q, want /tmp/browser", got)
	}
	if got := empty.ResolveBrowserProfileDir("none"); got != "" {
		t.Errorf("none 不该继承 docker 的 profile 缺省, got %q", got)
	}
	if got := empty.ResolveBrowserDownloadsDir("docker"); got != "" {
		t.Errorf("downloads 缺省应为空（<cwd>/downloads），got %q", got)
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

// browser.limits 只做缺省，请求里的非零字段覆盖它，最后被全局上限 clamp。
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
	// 全局上限 512 更紧，最终应被压回 512
	got := cfg.BrowserLimits(sandbox.Limits{})
	if got.MemoryMB != 512 {
		t.Errorf("clamped memory = %d, want 512", got.MemoryMB)
	}
}
