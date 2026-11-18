package sandboxinfo

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"flame/tools/xlog"
)

// 新格式：network 是按通道的对象，逐字段落地；语言列表叫 code_langs。
func TestNetworkInfoPerChannel(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":{"exec":"on-request","code":"off"},"langs":["lua"],"code_langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Exec != NetworkOnRequest || info.Network.Code != NetworkOff {
		t.Errorf("按通道策略解析错：%+v", info.Network)
	}
	if len(info.CodeLangs) != 1 || len(info.LegacyCodeLangs) != 0 {
		t.Errorf("code_langs 应落到新字段：%+v", info)
	}
}

// 2026-10 改名前的格式：第二条通道叫 script，语言列表叫 script_langs。
// 两处都要归一，否则「sandboxd 先升 / nexus 先升」的那段时间整个沙箱变成「未知」。
func TestNetworkInfoLegacyScriptField(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":{"exec":"on-request","script":"off"},"langs":["lua"],"script_langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Code != NetworkOff {
		t.Errorf("旧字段 script 应归一到 Code，got %+v", info.Network)
	}
	if len(info.LegacyCodeLangs) != 1 || len(info.CodeLangs) != 0 {
		t.Errorf("旧字段 script_langs 应落到兼容字段：%+v", info)
	}
}

// 旧版 sandboxd 报的是单个字符串：按 exec 档位理解，code 恒 off。
// 不做这个兼容，升级顺序就被绑死了（sandboxd 先升 → nexus 整份快照降级为「未知」）。
func TestNetworkInfoLegacyString(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":"on","langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Exec != NetworkOn || info.Network.Code != NetworkOff {
		t.Errorf("旧版字符串应映射到 exec、code=off，got %+v", info.Network)
	}
}

// 三态必须分得开：nil（对端没报这一项，旧版本）≠ 空切片（对端说「一个都没有」）。
// 依据是 encoding/json 把 `[]` 解成非 nil 空切片 —— 这条行为要是变了，这里会先红，
// 而不是等到「沙箱没配双工语言却给编辑器兜底出三个语言」才发现。
func TestCodeLangsTriState(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	t.Cleanup(func() {
		Set(old)
		if !oldConfigured {
			// Set 会把 configured 置真，这里还原成「从未注入」—— 与 agent 的
			// sandboxNotes 判据同源，别给别的测试留下脏状态。
			configured.Store(false)
		}
	})

	// 从未成功拉到（Mode 为空）：不可用，调用方该退回自己的兜底。
	Set(Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})
	if langs, ok := CodeLangs(); ok {
		t.Errorf("从未拉到快照时不该给出语言列表, got %v", langs)
	}

	// 旧版 sandboxd：没有这个字段 → nil → 不可用（退回兜底，而不是当成「没有语言」）。
	var legacy Info
	if err := json.Unmarshal([]byte(`{"mode":"docker","langs":["lua"]}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy payload: %v", err)
	}
	if legacy.CodeLangs != nil {
		t.Fatalf("字段缺失应解出 nil, got %v", legacy.CodeLangs)
	}
	legacy.FetchedAt = time.Now()
	Set(legacy)
	if langs, ok := CodeLangs(); ok {
		t.Errorf("字段缺失时不该当作「没有语言」, got %v", langs)
	}

	// 明确空集：可用但为空（提示词要照实说「沙箱没配任何工作流脚本语言」）。
	var empty Info
	if err := json.Unmarshal([]byte(`{"mode":"docker","code_langs":[]}`), &empty); err != nil {
		t.Fatalf("unmarshal empty payload: %v", err)
	}
	if empty.CodeLangs == nil {
		t.Fatal("JSON 的 [] 应解出非 nil 空切片 —— nil/空的分界就靠这条")
	}
	empty.FetchedAt = time.Now()
	Set(empty)
	langs, ok := CodeLangs()
	if !ok || len(langs) != 0 {
		t.Errorf("空集应是 (空, true), got (%v, %v)", langs, ok)
	}

	// 正常上报。
	Set(Info{Mode: "docker", CodeLangs: []string{"lua", "python"}, FetchedAt: time.Now()})
	if langs, ok := CodeLangs(); !ok || len(langs) != 2 {
		t.Errorf("正常上报应可用, got (%v, %v)", langs, ok)
	}
}

// Ready 是沙箱工具的准入：从未拉到快照（Mode 空）时不提供服务。
func TestReady(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	t.Cleanup(func() {
		Set(old)
		if !oldConfigured {
			configured.Store(false)
		}
	})

	Set(Info{})
	if Ready() {
		t.Error("从未拉到快照（Mode 空）时不该 Ready")
	}
	Set(Info{Err: "dial failed"})
	if Ready() {
		t.Error("只有错误、没有 Mode 时不该 Ready")
	}
	Set(Info{Mode: "docker"})
	if !Ready() {
		t.Error("有 Mode 时应该 Ready")
	}
}

// 冷启动：sandboxd 还没起来时不能按 interval 干等，否则 nexus 的就绪判定会带着 503
// 卡满一个刷新周期。interval 在这里刻意给到小时级 —— 只有 firstRetry 在起作用，
// 一旦这条行为回退，测试会超时而不是变慢。
func TestStartRefresherRetriesFastUntilFirstSnapshot(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	oldRetry := firstRetry
	oldInfoLogger, oldErrorLogger := xlog.InfoLogger, xlog.ErrorLogger
	// xlog 未 Init 时 logger 是 nil，刷新失败路径会打日志 —— 这里是单元测试，
	// 丢掉即可（不去 Init 是为了不落盘、不起那条后台轮转协程）。
	xlog.InfoLogger = log.New(io.Discard, "", 0)
	xlog.ErrorLogger = log.New(io.Discard, "", 0)
	t.Cleanup(func() {
		Set(old)
		if !oldConfigured {
			configured.Store(false)
		}
		firstRetry = oldRetry
		xlog.InfoLogger, xlog.ErrorLogger = oldInfoLogger, oldErrorLogger
	})

	firstRetry = 20 * time.Millisecond
	Set(Info{}) // 从未拉到快照

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"mode":"docker","network":"off","langs":["lua"],"script_langs":["lua"]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartRefresher(ctx, srv.URL, "", time.Hour)

	deadline := time.Now().Add(2 * time.Second)
	for !Ready() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !Ready() {
		t.Fatalf("前两次失败后应很快重试到成功, hits=%d", hits.Load())
	}
	// 成功后再无加急重试（下一次要等 interval），所以恰好三次。
	if got := hits.Load(); got != 3 {
		t.Errorf("拿到快照后不该继续加急重试, hits=%d", got)
	}
	// 这份 fixture 用的是旧版格式：network 是字符串、语言列表叫 script_langs。
	// 顺带钉住两条兼容路径：字符串映射到 exec（code 记 off）、旧字段名回退仍可用。
	if info := Get(); info.Network.Exec != NetworkOff || info.Network.Code != NetworkOff ||
		len(info.LegacyCodeLangs) != 1 || len(info.CodeLangs) != 0 {
		t.Errorf("快照内容应完整落地（旧版格式要能兼容）, got %+v", info)
	}
	if langs, ok := CodeLangs(); !ok || len(langs) != 1 {
		t.Errorf("旧字段 script_langs 应经回退可用, got (%v, %v)", langs, ok)
	}
}

// OS 从 capabilities 的 `系统:` 行读取沙箱操作系统；不可信/缺失时返回空串，
// 让调用方（默认 shell 选择）自己决定兜底，而不是凭空猜一个平台。
func TestOS(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	t.Cleanup(func() {
		Set(old)
		if !oldConfigured {
			configured.Store(false)
		}
	})

	cases := []struct {
		name string
		env  Environment
		want string
	}{
		{"linux os-release", Environment{Text: "系统: Debian GNU/Linux 12\nPython 版本: 3.13"}, "linux"},
		{"windows host", Environment{Text: "系统: windows/amd64"}, "windows"},
		{"探测失败 → 未知", Environment{Text: "系统: windows/amd64", Error: "boom"}, ""},
		{"没有系统行 → 未知", Environment{Text: "Python 版本: 3.13"}, ""},
		{"空 → 未知", Environment{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			Set(Info{Environment: c.env})
			if got := OS(); got != c.want {
				t.Errorf("OS() = %q, want %q", got, c.want)
			}
		})
	}
}
