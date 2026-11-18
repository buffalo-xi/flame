package daemon

import (
	"io"
	"log"
	"strings"
	"testing"

	"flame/sandboxd/isolate"
	"flame/tools/xlog"
)

// silenceLogs 把 xlog 静音到测试结束：on-request 的两条「本部署做不到」提示走 Error 级，
// 不清掉会把测试输出刷满。
func silenceLogs(t *testing.T) {
	t.Helper()
	oldInfo, oldErr := xlog.InfoLogger, xlog.ErrorLogger
	xlog.InfoLogger = log.New(io.Discard, "", 0)
	xlog.ErrorLogger = log.New(io.Discard, "", 0)
	t.Cleanup(func() { xlog.InfoLogger, xlog.ErrorLogger = oldInfo, oldErr })
}

// 三态各自都要被校验到：off 要真断得住，on 要真放得开，
// on-request 要**两态都能到达**（比前两者更严，所以单独一条判据）。
func TestValidateNetworkPolicies(t *testing.T) {
	silenceLogs(t)

	cases := []struct {
		name    string
		cfg     Config
		wantErr string // 空 = 期望通过
	}{
		{
			name: "off + docker none",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOff, Docker: DockerConfig{Network: "none"}},
		},
		{
			name:    "off + docker bridge（断不住）",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOff, Docker: DockerConfig{Network: "bridge"}},
			wantErr: "would still reach the network",
		},
		{
			name: "on + docker bridge",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOn, Docker: DockerConfig{Network: "bridge"}},
		},
		{
			name:    "on + docker 未设（会被补成 none）",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOn},
			wantErr: "no network at all",
		},
		{
			name: "on-request + docker bridge",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOnRequest, Docker: DockerConfig{Network: "bridge"}},
		},
		{
			name:    "on-request + docker none（请求出网时无网可切）",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOnRequest, Docker: DockerConfig{Network: "none"}},
			wantErr: "no network to switch to",
		},
		{
			// none 模式下断网做不到，但 egress=false 的请求可以由运行期拒绝 ——
			// 所以启动只告警，不阻断。
			name: "on-request + mode=none（只告警）",
			cfg:  Config{Mode: isolate.NoneName, Network: NetworkOnRequest},
		},
		{
			name:    "非法取值",
			cfg:     Config{Mode: isolate.DockerName, Network: "maybe", Docker: DockerConfig{Network: "bridge"}},
			wantErr: "unknown policy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validateNetwork()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("不该报错，got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v，want 含 %q", err, tc.wantErr)
			}
		})
	}
}

// networks 里的脏值不能因为「没被当前 mode 选中」就放行；
// 反过来，被选中时它必须真的覆盖顶层值。
func TestValidateNetworkModeOverride(t *testing.T) {
	silenceLogs(t)

	c := Config{
		Mode:     isolate.DockerName,
		Network:  NetworkOn,
		Networks: map[string]string{isolate.NoneName: "sometimes"},
		Docker:   DockerConfig{Network: "bridge"},
	}
	if err := c.validateNetwork(); err == nil || !strings.Contains(err.Error(), "networks.none") {
		t.Fatalf("networks 里的非法取值应被拒，got %v", err)
	}

	c = Config{
		Mode:     isolate.NoneName,
		Network:  NetworkOnRequest,
		Networks: map[string]string{isolate.NoneName: NetworkOn},
	}
	if got := c.EffectiveNetwork(); got != NetworkOn {
		t.Errorf("EffectiveNetwork = %q, want %q（networks 按 mode 优先）", got, NetworkOn)
	}
	if err := c.validateNetwork(); err != nil {
		t.Errorf("覆盖成 on 后应通过，got %v", err)
	}
}

// /v1/code 在有隔离的部署下恒为 off 且不提供配置项 —— 这条钉住「不是漏了配置，是有意不给」。
//
// 反证：池按语言全局创建、跨租户共享，且 wire.Frame 里没有租户身份。一旦可配成 on，
// 某个租户脚本失控时无法只关它，只能全平台回退。
func TestScriptNetworkPolicy(t *testing.T) {
	silenceLogs(t)

	for _, policy := range []string{NetworkOff, NetworkOnRequest, NetworkOn} {
		c := Config{Mode: isolate.DockerName, Network: policy, Docker: DockerConfig{Network: "bridge"}}
		if got := c.EffectiveCodeNetwork(); got != NetworkOff {
			t.Errorf("network=%s 时 script 通道仍应为 off，got %q", policy, got)
		}
	}

	// mode=none 是例外：脚本跑在宿主进程上，没有隔离单元也就没有断网这回事。
	// 必须如实报 on —— 报 off 会让 agent 按错误前提写代码。
	c := Config{Mode: isolate.NoneName, Network: NetworkOnRequest}
	if got := c.EffectiveCodeNetwork(); got != NetworkOn {
		t.Errorf("mode=none 时 script 应如实报 %q（宿主拦不住），got %q", NetworkOn, got)
	}
}
