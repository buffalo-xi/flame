package agent

import (
	"strings"
	"testing"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/sandboxinfo"
)

// netInfo 构造按通道的网络策略：这些用例只关心 exec 这一路，script 恒 off。
func netInfo(exec string) sandboxinfo.NetworkInfo {
	return sandboxinfo.NetworkInfo{Exec: exec}
}

// stubCapabilities 替换平台能力快照到测试结束（真实实现要连库）。
// 传 nil 表示「全未开通」，与 capabilitypolicy.Snapshot 在无行时的表现一致。
func stubCapabilities(t *testing.T, caps map[string]bool) {
	t.Helper()
	old := capabilitySnapshot
	capabilitySnapshot = func(string) map[string]bool { return caps }
	t.Cleanup(func() { capabilitySnapshot = old })
}

// sandboxNotes 的判据是 sandboxd 的能力快照（环境事实），与 AllowedTools（capabilityNotes 的判据）无关，
// 所以空 agentImpl 就够。四态：未注入 / 未知 / off / on，外加「快照过期」与「探测失败」两条附注。
//
// 顺序有意义：沙箱快照是包级单例，一旦注入过 Configured() 就一直是 true，
// 所以「未注入」这一态必须最先判。
func TestSandboxNotes(t *testing.T) {
	a := &agentImpl{}

	// 未注入：宁可不提，也不要凭空告诉模型一个环境。
	if got := a.sandboxNotes(); got != "" {
		t.Errorf("未注入时应返回空串, got %q", got)
	}

	// 未知：注入过但一次都没成功拉到（Mode 为空）。此时**不能**说成「不能联网」——
	// 那会让模型白写一堆离线兜底代码，正是这套机制要避免的谎言。
	sandboxinfo.Set(sandboxinfo.Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})
	unknown := a.sandboxNotes()
	if !strings.Contains(unknown, "未知") {
		t.Errorf("从未拉到快照时应如实说未知, got %q", unknown)
	}
	if strings.Contains(unknown, "network=off") || strings.Contains(unknown, "network=on") {
		t.Errorf("未知态不该给出网络声明, got %q", unknown)
	}

	// off：必须给出「不能联网」的事实与可操作后果（装不了包），且探测原文原样透传。
	// 「配置的可执行语言」与「实测」必须分开标注 —— 混在一句话里读起来像两套事实。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:    "docker",
		Network: netInfo(sandboxinfo.NetworkOff),
		Langs:   []string{"python", "js"},
		Environment: sandboxinfo.Environment{
			Text:     "Python 版本: 3.13.5\n已装 Python 包: openpyxl==3.1.5",
			ProbedAt: time.Now(),
		},
		FetchedAt: time.Now(),
	})
	off := a.sandboxNotes()
	for _, want := range []string{
		"docker",
		"可执行语言（sandboxd 配置；用于命令执行与技能脚本，**不是工作流节点的可选语言**）：python、js",
		"沙箱实测（以下为 sandboxd 探测原文）",
		"已装 Python 包: openpyxl==3.1.5",
		"network=off",
		"不能",
	} {
		if !strings.Contains(off, want) {
			t.Errorf("off 态文案缺少 %q:\n%s", want, off)
		}
	}
	if strings.Contains(off, "network=on") {
		t.Errorf("off 态不该出现 on 的说明:\n%s", off)
	}

	// on-request：必须给**结论**，不能把档位原样丢给模型 —— 它没法据此判断该不该联网。
	// 两种结论都要选得中：租户开通了 net.egress / 没开通（fail-closed）。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOnRequest),
		FetchedAt: time.Now(),
	})

	stubCapabilities(t, map[string]bool{capability.Egress: true})
	egressOn := a.sandboxNotes()
	if !strings.Contains(egressOn, "egress: true") || !strings.Contains(egressOn, "可以") {
		t.Errorf("已开通出网时应告诉模型怎么用（egress: true）:\n%s", egressOn)
	}

	stubCapabilities(t, nil)
	egressOff := a.sandboxNotes()
	if !strings.Contains(egressOff, "未开通") || strings.Contains(egressOff, "egress: true") {
		t.Errorf("未开通出网时不该让模型以为能联网:\n%s", egressOff)
	}

	// on：仍要提醒 rootfs 只读（能联网 ≠ 能全局安装）。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "nsjail",
		Network:   netInfo(sandboxinfo.NetworkOn),
		Langs:     []string{"lua"},
		FetchedAt: time.Now(),
	})
	on := a.sandboxNotes()
	if !strings.Contains(on, "network=on") || !strings.Contains(on, "可以") {
		t.Errorf("on 态文案应说明可联网:\n%s", on)
	}
	if !strings.Contains(on, "只读") {
		t.Errorf("on 态仍须提醒 rootfs 只读（装不了全局包）:\n%s", on)
	}

	// 刷新失败但有过成功快照：保留事实并标注可能过期，而不是退化成「未知」。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"python"},
		FetchedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		Err:       "http 502",
	})
	stale := a.sandboxNotes()
	if !strings.Contains(stale, "network=off") {
		t.Errorf("刷新失败不该丢掉上一次成功的快照:\n%s", stale)
	}
	if !strings.Contains(stale, "可能已过期") || !strings.Contains(stale, "http 502") {
		t.Errorf("刷新失败须标注过期与原因:\n%s", stale)
	}

	// 探测失败：模块清单说「未知 + 原因」，但网络声明（另一个来源）照样要给。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:    "docker",
		Network: netInfo(sandboxinfo.NetworkOff),
		Environment: sandboxinfo.Environment{
			Error: "python probe timeout",
		},
		FetchedAt: time.Now(),
	})
	probeFail := a.sandboxNotes()
	if !strings.Contains(probeFail, "python probe timeout") {
		t.Errorf("探测失败须给出原因:\n%s", probeFail)
	}
	if !strings.Contains(probeFail, "network=off") {
		t.Errorf("探测失败不该影响网络声明:\n%s", probeFail)
	}

	// 工作流脚本语言与上面那条「可执行语言」（顶层 langs）**必须分开给**：顶层定义里
	// 有的语言未必开了双工池，照它去写工作流会被沙箱拒（unsupported_lang），而那要等到
	// 运行期才暴露。这里让两条的集合刻意不同，防止以后有人把两行合并成一行。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"js", "lua", "python", "ruby"},
		CodeLangs: []string{"lua", "python"},
		FetchedAt: time.Now(),
	})
	bothLangs := a.sandboxNotes()
	if !strings.Contains(bothLangs, "可执行语言（sandboxd 配置；用于命令执行与技能脚本，**不是工作流节点的可选语言**）：js、lua、python、ruby") {
		t.Errorf("缺少可执行语言一行:\n%s", bothLangs)
	}
	if !strings.Contains(bothLangs, "工作流脚本语言（sandboxd 配置）：lua、python") {
		t.Errorf("缺少工作流脚本语言一行:\n%s", bothLangs)
	}
	if !strings.Contains(bothLangs, "unsupported_lang") {
		t.Errorf("工作流脚本语言那行须说清选错语言的后果:\n%s", bothLangs)
	}

	// 明确「一个都没配」：必须照说，不能沉默 —— 沉默会被读成「随便挑」。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		CodeLangs: []string{},
		FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); !strings.Contains(got, "没有配置任何**工作流脚本语言**") {
		t.Errorf("空集必须明说:\n%s", got)
	}

	// 旧版 sandboxd（没报该字段 → nil）：整条不提，而不是当成「没有语言」。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"lua"},
		FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "工作流脚本语言") {
		t.Errorf("对端没报该字段时不该提工作流脚本语言:\n%s", got)
	}

	// 浏览器：三态必须分开说 —— 对端没报（nil）/ 明确不可用（带原因）/ 可用（有头与否）。
	// 尤其「不可用」不能省略原因：模型会把「工具没开」误判成「网站打不开」。
	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "浏览器") {
		t.Errorf("对端没报 browser 字段时不该提浏览器:\n%s", got)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{Enabled: false, Error: "no browser in image"},
	})
	disabled := a.sandboxNotes()
	if !strings.Contains(disabled, "不可用") || !strings.Contains(disabled, "no browser in image") {
		t.Errorf("浏览器不可用须给出原因:\n%s", disabled)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{
			Enabled: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: false,
		},
	})
	headless := a.sandboxNotes()
	if !strings.Contains(headless, "只能无头") || !strings.Contains(headless, "snapshot") {
		t.Errorf("只能无头时须说清事实与观测方式:\n%s", headless)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "none", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{
			Enabled: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: true,
		},
	})
	headed := a.sandboxNotes()
	if !strings.Contains(headed, "headed=true") {
		t.Errorf("允许有头时须告诉模型怎么开:\n%s", headed)
	}

	// 注入范围：只给「能碰沙箱」的 agent（判据是工具授权）。一条通往沙箱的路都没有的
	// agent，讲联网与模块没有用处，还会长出「我在沙箱里」这类凭空的说法。
	full := sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"lua"},
		CodeLangs: []string{"lua"},
		FetchedAt: time.Now(),
	}
	sandboxinfo.Set(full)

	noSandbox := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{"read_file": true, "write_file": true}}}
	if got := noSandbox.sandboxNotes(); got != "" {
		t.Errorf("没有 exec / run_skill_script / shell 时不该注入沙箱说明, got:\n%s", got)
	}

	// 判不了（没有工具上下文）：照旧注入 —— 环境事实多给比少给安全，略过只用于确知没入口。
	if got := (&agentImpl{}).sandboxNotes(); got == "" {
		t.Error("工具上下文缺失时不该略过沙箱说明")
	}

	// 两个入口任一个被授权就算「能碰沙箱」。
	for _, tool := range []string{"exec", "run_skill_script", "shell", "browser"} {
		withTool := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{tool: true}}}
		if got := withTool.sandboxNotes(); !strings.Contains(got, "沙箱环境（隔离方式 docker") {
			t.Errorf("授了 %s 应注入沙箱说明, got:\n%s", tool, got)
		}
	}
}
