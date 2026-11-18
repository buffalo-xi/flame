// Package sandboxinfo 持有 sandboxd 的能力快照：沙箱能否联网（声明）+ 沙箱里有什么（探测）。
//
// 为什么是共享包而不是塞进 agent 内部：拉取（HTTP + 定期刷新）与使用（拼提示词）是两件事
// —— main 负责拉，agent 负责读。注册风格与 tools.SetSandbox 一致（包级单例）。
//
// 为什么必须定期刷新：sandboxd 改配置重启是常态（网络声明、镜像都会变），只在 nexus
// 启动时拉一次，提示词会立刻开始撒谎 —— 而"提示词不许撒谎"正是这套机制存在的理由：
// agent 会据它决定要不要写联网代码、用哪些库。
package sandboxinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"flame/tools/xlog"
)

// Environment 沙箱环境的探测结果（与 sandboxd 侧的 JSON 结构对齐）。
type Environment struct {
	// Text 各探针输出的原文（"label: value" 行）。刻意不解析成结构 ——
	// 「沙箱里有什么」是沙箱自己的事实，读它的是 LLM。
	Text     string    `json:"text"`
	ProbedAt time.Time `json:"probed_at"`
	// Error 非空表示 sandboxd 那边探测整体失败，Text 不可信。
	Error string `json:"error,omitempty"`
}

// Info 一份能力快照。
type Info struct {
	// Mode 隔离方式（none / nsjail / docker）。
	Mode string `json:"mode"`
	// Network 按通道的网络策略（**部署上限**，不是某次调用的事实）。见 NetworkInfo。
	Network NetworkInfo `json:"network"`
	Langs   []string    `json:"langs"`
	// CodeLangs 可用作**工作流 code 节点**的语言 —— 沙箱当下真的会对外服务的那些
	// （sandboxd 侧 code.pools 里配了、解释器入口解析成功的那些）。
	//
	// 与 Langs 不是一回事：Langs 是「沙箱能跑起来的语言」，单工也会用（只跑技能脚本的
	// 语言不必开 code 池），照它去选 code 节点语言会选到 unsupported_lang。
	//
	// nil 与空切片语义不同，别合并成一条：nil = 对端没报这一项（旧版本 / 字段名对不上）、
	// 空 = 对端明确说「一个 code 语言都没配」。依据是 encoding/json 把 `[]` 解成非 nil
	// 空切片 —— 这条行为被 sandboxinfo_test.go 钉住了。
	CodeLangs []string `json:"code_langs"`
	// LegacyCodeLangs 旧字段名（sandboxd 2026-10 之前叫 script_langs）。
	// 只用于过渡期读取：CodeLangs() 在新字段缺失时回退到它，让「sandboxd 先升 / nexus
	// 先升」都不至于把可用语言列表变成「未知」。
	LegacyCodeLangs []string `json:"script_langs,omitempty"`

	Environment Environment `json:"environment"`

	// Browser 浏览器能力（sandboxd 的实测结论：镜像里有没有 Chromium、能不能有头）。
	//
	// nil 与「Enabled=false」语义不同，别合并：nil = 对端没报这一项（旧版本
	// sandboxd / 字段名对不上），false = 对方明确说「不可用」并给了原因。
	// 前者不该让 agent 得出「浏览器坏了」的结论，只能说「这个沙箱没告诉我」。
	Browser *BrowserInfo `json:"browser,omitempty"`

	// FetchedAt 最近一次**成功**拉取的时刻。
	FetchedAt time.Time `json:"-"`
	// Err 最近一次拉取失败的原因。非空时上面的字段是上一次成功的快照（可能已过期），
	// 而不是"没有能力" —— 这两件事对 agent 的意义完全不同，文案里必须分开说。
	Err string `json:"-"`
}

// NetworkInfo 按通道的网络策略（与 sandboxd 的 capabilities.network 对齐）。
//
// 为什么按通道拆：exec 走单工、每请求新建进程，网络可以按请求切；code 是全局池化
// 单元，网络在建单元时写死，且恒为 off。合成一个值必然在其中一条通道上给出错误结论。
//
// ⚠️ 这两个值都是**部署上限**，不是「这次调用能不能联网」：最终结果还要叠加租户许可
// （capability.Egress）与请求标志。提示词必须按叠加后的结论说，见 agent 的 sandboxNotes。
type NetworkInfo struct {
	// Exec /v1/exec（含技能脚本）的策略：off / on-request / on。
	Exec string `json:"exec"`
	// Code /v1/code（工作流 code 节点）的策略：恒为 off —— sandboxd 不提供配置项。
	Code string `json:"code"`
	// Script 旧字段名（sandboxd 2026-10 之前叫 script）。只用于过渡期读取：
	// UnmarshalJSON 会把它归一到 Code，调用方只看 Code。
	Script string `json:"script,omitempty"`
}

// UnmarshalJSON 兼容两种旧格式：
//
//  1. 更早的 sandboxd 报的是单个字符串（"off" / "on"），没有按通道拆分 —— 按 exec
//     档位理解，code 恒 off；
//  2. 2026-10 之前按通道拆了，但第二条通道叫 "script" —— 归一到 Code。
//
// 不做兼容的代价是升级顺序被绑死：sandboxd 先升则 nexus 解析失败、整份快照降级为
// 「未知」（不会撒谎，但暂时没有信息）。多写这十几行就不必协调上线顺序。
func (n *NetworkInfo) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n.Exec, n.Code, n.Script = s, NetworkOff, ""
		return nil
	}
	type plain NetworkInfo
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*n = NetworkInfo(p)
	if n.Code == "" {
		n.Code = n.Script
	}
	return nil
}

// BrowserInfo 浏览器能力快照（与 sandboxd 的 daemon.BrowserInfo 对齐）。
type BrowserInfo struct {
	Enabled         bool   `json:"enabled"`
	Engine          string `json:"engine,omitempty"`
	Version         string `json:"version,omitempty"`
	HeadlessDefault bool   `json:"headless_default"`
	HeadedAvailable bool   `json:"headed_available"`
	Error           string `json:"error,omitempty"`
}

// Network 策略取值（与 sandboxd 的 daemon.NetworkXxx 对齐）。
const (
	// NetworkOff 不能联网（含「请求要出网也拒绝」）。
	NetworkOff = "off"
	// NetworkOn 一律能联网（不看请求）。
	NetworkOn = "on"
	// NetworkOnRequest 按请求裁决，只对 exec 通道有意义：实际能否出网取决于租户许可。
	//
	// 提示词里**不要**把这一档原样告诉 agent —— 它无法据此判断自己该不该写联网代码，
	// 必须由 agent 侧叠加租户能力算出结论（见 agent 的 sandboxNotes）。
	NetworkOnRequest = "on-request"
)

var (
	current    atomic.Pointer[Info]
	configured atomic.Bool
)

// Set 注入一份能力快照。
func Set(i Info) {
	current.Store(&i)
	configured.Store(true)
}

// Get 取当前快照；从未注入过时返回零值（调用方用 Configured 区分）。
func Get() Info {
	if p := current.Load(); p != nil {
		return *p
	}
	return Info{}
}

// Configured 是否注入过。未注入时 agent 侧不该给出任何环境说明
// （宁可不提，也不要凭空告诉它一个环境）。
func Configured() bool { return configured.Load() }

// Ready 沙箱是否可用作服务：至少成功拉到过一次能力快照（Mode 非空）。
//
// 这是沙箱类工具（exec / shell）的准入判据 —— 不知道沙箱是什么环境，就不要在
// 里面跑命令。注意与 Err 区分：Err 非空只是「最近一次刷新失败」，此时 Mode 仍非空，
// 快照是上一次成功的结果（可能过期但可用），不构成拒绝服务的理由。
func Ready() bool { return Get().Mode != "" }

// CodeLangs 取「可用作工作流 code 节点的语言」，ok 表示这份快照能不能用。
//
// ok=false 的两种情况合一：从未成功拉到（Mode 为空，与 agent 的 sandboxNotes 同口径）；
// 或对端没报语言列表（更旧的 sandboxd 两个字段都没有）。两种都不该猜 ——
// 调用方要么退回自己的兜底（编辑器），要么干脆不提（提示词）。
//
// 返回（空, true）是合法结果：沙箱在，且它一个 code 语言都没配。
func CodeLangs() ([]string, bool) {
	info := Get()
	if info.Mode == "" {
		return nil, false
	}
	langs := info.CodeLangs
	if langs == nil {
		// 旧版 sandboxd 只报 script_langs：回退读取，避免「沙箱在、却报语言未知」。
		langs = info.LegacyCodeLangs
	}
	if langs == nil {
		return nil, false
	}
	return langs, true
}

// OS 从沙箱环境探测原文里读取操作系统（"windows" / "linux"）；读不到返回空串。
//
// capabilities 已经用 `系统: …` 这一行给出 OS（见 sandboxd 的 probeHost /
// formatOSRelease），所以这里不需要再猜、也不该用 nexus 宿主的 runtime.GOOS：
// 沙箱可能跑在另一个平台上。只读这一行、不做任何能力推断，因此与「Text 原样给
// LLM 读」并不冲突。
//
// 探测失败（Error 非空）时返回空串 —— 不可信的文本不能拿来选 shell。
func OS() string {
	info := Get()
	if info.Environment.Error != "" {
		return ""
	}
	for _, line := range strings.Split(info.Environment.Text, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "系统: ")
		if !ok {
			continue
		}
		if strings.Contains(strings.ToLower(v), "windows") {
			return "windows"
		}
		// os-release 只存在于 Linux 系；能读到 `系统:` 行且不是 Windows 即 Linux。
		return "linux"
	}
	return ""
}

// MaxBodyBytes 响应体读取上限：这段文本会进 LLM 上下文，别被异常响应撑爆。
const MaxBodyBytes = 1 << 20

// FetchTimeout 单次拉取的上限。sandboxd 是内网服务，给足即可 ——
// 拖太久会挤掉刷新间隔，也拖慢优雅退出的收敛。
const FetchTimeout = 10 * time.Second

// firstRetry 尚未拿到任何快照时的重试间隔（拿到第一份后回到 interval）。
//
// 为什么要单独一档：StartRefresher 起来就立刻拉一次，若此刻 sandboxd 还没就绪
// （部署顺序不定、重启是常态），按 interval 算，下一次要等满一分钟 —— 而 nexus 的
// 就绪判定认这份快照，整个进程会带着 503 干等。冷启动赌的是「对端只是起得慢」，
// 成本是一次内网 GET。
//
// 反过来，快照已在手时失败不再加急：那多半是真的坏了，缩短重试只会放大日志和负载，
// 而且此时 Ready() 仍为真（快照过期但可用），没有必要急着换一份。
//
// 是变量而非常量，只为测试能把等待压到毫秒级。
var firstRetry = 2 * time.Second

// Fetch 从 sandboxd 拉取一次能力快照。
func Fetch(ctx context.Context, endpoint, token string) (Info, error) {
	if endpoint == "" {
		return Info{}, errors.New("sandboxinfo: capabilities endpoint is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Info{}, fmt.Errorf("sandboxinfo: build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("sandboxinfo: get %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("sandboxinfo: get %s: http %d", endpoint, resp.StatusCode)
	}

	var payload Info
	if derr := json.NewDecoder(io.LimitReader(resp.Body, MaxBodyBytes)).Decode(&payload); derr != nil {
		return Info{}, fmt.Errorf("sandboxinfo: decode %s: %w", endpoint, derr)
	}
	payload.FetchedAt = time.Now()
	return payload, nil
}

// StartRefresher 启动后台刷新：立即拉一次，之后每 interval 一次，直到 ctx 结束。
//
// 失败**不覆盖**上一次成功的数据，只把 Err 记上 —— 让 agent 拿到"可能过期的真实环境"，
// 比直接退化成"未知"有用得多（文案会注明可能过期）。只在 成功↔失败 切换时打日志，
// 免得 sandboxd 没起来时把日志刷成噪声。
func StartRefresher(ctx context.Context, endpoint, token string, interval time.Duration) {
	if endpoint == "" {
		xlog.Error("sandboxinfo: capabilities endpoint is empty — agent 将看不到沙箱环境")
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}

	go func() {
		failing := false
		for {
			info, err := Fetch(ctx, endpoint, token)
			if err != nil {
				last := Get()
				last.Err = err.Error()
				Set(last)
				if !failing {
					xlog.Error("sandboxinfo: refresh failed: %v", err)
					failing = true
				}
			} else {
				Set(info)
				if failing {
					xlog.Info("sandboxinfo: refresh recovered (mode=%s network=%s)", info.Mode, info.Network)
					failing = false
				}
			}

			wait := interval
			if !Ready() {
				wait = firstRetry
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}
