package agent

import (
	"strings"
	"testing"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/sandboxinfo"
)

// netInfo builds a per-channel network policy: these cases only care about exec, script stays off.
func netInfo(exec string) sandboxinfo.NetworkInfo {
	return sandboxinfo.NetworkInfo{Exec: exec}
}

// stubCapabilities replaces the platform capability snapshot until the test ends (the real implementation needs the database).
// Passing nil means "nothing granted", matching capabilitypolicy.Snapshot when there are no rows.
func stubCapabilities(t *testing.T, caps map[string]bool) {
	t.Helper()
	old := capabilitySnapshot
	capabilitySnapshot = func(string) map[string]bool { return caps }
	t.Cleanup(func() { capabilitySnapshot = old })
}

// sandboxNotes is judged by sandboxd's capability snapshot (environment facts), unrelated to AllowedTools (the criterion of capabilityNotes),
// so an empty agentImpl is enough. Four states: never injected / unknown / off / on, plus the two annotations "snapshot is stale" and "probe
// failed".
//
// The order matters: the sandbox snapshot is a package-level singleton, and once something has been injected Configured() stays true forever,
// so the "never injected" state must be judged first.
func TestSandboxNotes(t *testing.T) {
	a := &agentImpl{}

	// Never injected: better to stay silent than to invent an environment for the model.
	if got := a.sandboxNotes(); got != "" {
		t.Errorf("should return an empty string when nothing was injected, got %q", got)
	}

	// Unknown: injected but never fetched successfully (empty Mode). It must **not** be phrased as "no network" - that would make the model
	// write a pile of offline fallback code, exactly the lie this mechanism exists to avoid.
	sandboxinfo.Set(sandboxinfo.Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})
	unknown := a.sandboxNotes()
	if !strings.Contains(unknown, "未知") {
		t.Errorf("should say it is unknown when no snapshot was ever fetched, got %q", unknown)
	}
	if strings.Contains(unknown, "network=off") || strings.Contains(unknown, "network=on") {
		t.Errorf("the unknown state must not declare a network mode, got %q", unknown)
	}

	// off: must give the "no network" fact and its actionable consequence (cannot install packages), and pass the raw probe text through.
	// "Configured executable languages" and "measured" must be labelled separately - merged into one sentence they read like two sets of facts.
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
			t.Errorf("the off-state copy is missing %q:\n%s", want, off)
		}
	}
	if strings.Contains(off, "network=on") {
		t.Errorf("the off state must not mention on:\n%s", off)
	}

	// on-request: it must give the **conclusion**, not hand the tier to the model as is - the model cannot decide from it whether to go online.
	// Both conclusions must be selectable: the tenant has net.egress / does not (fail-closed).
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOnRequest),
		FetchedAt: time.Now(),
	})

	stubCapabilities(t, map[string]bool{capability.Egress: true})
	egressOn := a.sandboxNotes()
	if !strings.Contains(egressOn, "egress: true") || !strings.Contains(egressOn, "可以") {
		t.Errorf("when egress is granted the model must be told how to use it (egress: true):\n%s", egressOn)
	}

	stubCapabilities(t, nil)
	egressOff := a.sandboxNotes()
	if !strings.Contains(egressOff, "未开通") || strings.Contains(egressOff, "egress: true") {
		t.Errorf("without egress the model must not believe it can go online:\n%s", egressOff)
	}

	// on: still remind that the rootfs is read-only (network access != global installs).
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "nsjail",
		Network:   netInfo(sandboxinfo.NetworkOn),
		Langs:     []string{"lua"},
		FetchedAt: time.Now(),
	})
	on := a.sandboxNotes()
	if !strings.Contains(on, "network=on") || !strings.Contains(on, "可以") {
		t.Errorf("the on-state copy should say networking is possible:\n%s", on)
	}
	if !strings.Contains(on, "只读") {
		t.Errorf("the on state must still warn that the rootfs is read-only (no global installs):\n%s", on)
	}

	// Refresh failed but a successful snapshot exists: keep the facts and mark them possibly stale, rather than degrading to "unknown".
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"python"},
		FetchedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		Err:       "http 502",
	})
	stale := a.sandboxNotes()
	if !strings.Contains(stale, "network=off") {
		t.Errorf("a failed refresh must not drop the last successful snapshot:\n%s", stale)
	}
	if !strings.Contains(stale, "可能已过期") || !strings.Contains(stale, "http 502") {
		t.Errorf("a failed refresh must mark staleness and the reason:\n%s", stale)
	}

	// Probe failed: the module list says "unknown + why", but the network declaration (another source) is still given.
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
		t.Errorf("a failed probe must give the reason:\n%s", probeFail)
	}
	if !strings.Contains(probeFail, "network=off") {
		t.Errorf("a failed probe must not affect the network declaration:\n%s", probeFail)
	}

	// Workflow script languages and the "executable languages" line above (top-level langs) **must be given separately**: a language in the
	// top-level definition may have no duplex pool, and a workflow written against it is rejected by the sandbox (unsupported_lang), which only
	// surfaces at runtime. The two sets are deliberately different here, so nobody merges the lines later.
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"js", "lua", "python", "ruby"},
		CodeLangs: []string{"lua", "python"},
		FetchedAt: time.Now(),
	})
	bothLangs := a.sandboxNotes()
	if !strings.Contains(bothLangs, "可执行语言（sandboxd 配置；用于命令执行与技能脚本，**不是工作流节点的可选语言**）：js、lua、python、ruby") {
		t.Errorf("the executable-languages line is missing:\n%s", bothLangs)
	}
	if !strings.Contains(bothLangs, "工作流脚本语言（sandboxd 配置）：lua、python") {
		t.Errorf("the workflow-script-languages line is missing:\n%s", bothLangs)
	}
	if !strings.Contains(bothLangs, "unsupported_lang") {
		t.Errorf("the workflow-script-languages line must state the consequence of a wrong choice:\n%s", bothLangs)
	}

	// Say "nothing configured" explicitly: silence would read as "pick anything".
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		CodeLangs: []string{},
		FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); !strings.Contains(got, "没有配置任何**工作流脚本语言**") {
		t.Errorf("an empty set must be stated explicitly:\n%s", got)
	}

	// An old sandboxd (field not reported -> nil): say nothing at all, rather than "no languages".
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"lua"},
		FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "工作流脚本语言") {
		t.Errorf("workflow script languages must not be mentioned when the peer reports no such field:\n%s", got)
	}

	// Browser: three states must be stated separately - not reported by the peer (nil) / explicitly unavailable (with a reason) / available
	// (headed or not). Above all "unavailable" must not omit the reason: the model would misread "the tool is off" as "the site cannot be opened".
	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "浏览器") {
		t.Errorf("the browser must not be mentioned when the peer reports no browser field:\n%s", got)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{Enabled: false, Error: "no browser in image"},
	})
	disabled := a.sandboxNotes()
	if !strings.Contains(disabled, "不可用") || !strings.Contains(disabled, "no browser in image") {
		t.Errorf("an unavailable browser must come with the reason:\n%s", disabled)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{
			Enabled: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: false,
		},
	})
	headless := a.sandboxNotes()
	if !strings.Contains(headless, "只能无头") || !strings.Contains(headless, "snapshot") {
		t.Errorf("headless-only must state the fact and how to observe:\n%s", headless)
	}

	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "none", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
		Browser: &sandboxinfo.BrowserInfo{
			Enabled: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: true,
		},
	})
	headed := a.sandboxNotes()
	if !strings.Contains(headed, "headed=true") {
		t.Errorf("when headed mode is allowed the model must be told how to enable it:\n%s", headed)
	}

	// Injection scope: only for agents that can touch the sandbox (the criterion is tool grants). For an agent with no route into the sandbox,
	// talking about networking and modules is useless and would grow claims like "I am in the sandbox".
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
		t.Errorf("the sandbox notes must not be injected without exec / run_skill_script / shell, got:\n%s", got)
	}

	// Cannot be decided (no tool context): still injected - environment facts are safer to over-share, and skipping is only for a known-empty
	// entry list.
	if got := (&agentImpl{}).sandboxNotes(); got == "" {
		t.Error("the sandbox notes must not be skipped when the tool context is missing")
	}

	// Any one of the two entry points being granted counts as "can touch the sandbox".
	for _, tool := range []string{"exec", "run_skill_script", "shell", "browser"} {
		withTool := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{tool: true}}}
		if got := withTool.sandboxNotes(); !strings.Contains(got, "沙箱环境（隔离方式 docker") {
			t.Errorf("granting %s should inject the sandbox notes, got:\n%s", tool, got)
		}
	}
}
