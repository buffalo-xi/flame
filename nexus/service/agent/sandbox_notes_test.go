package agent

import (
	"strings"
	"testing"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/browserinfo"
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
	if !strings.Contains(unknown, "environment: unknown") {
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
			Text:     "Python version: 3.13.5\nInstalled Python packages: openpyxl==3.1.5",
			ProbedAt: time.Now(),
		},
		FetchedAt: time.Now(),
	})
	off := a.sandboxNotes()
	for _, want := range []string{
		"docker",
		"Executable languages (configured in sandboxd; used for command execution and skill scripts, **not** the selectable languages of workflow nodes): python, js",
		"Sandbox probe result (verbatim output from sandboxd)",
		"Installed Python packages: openpyxl==3.1.5",
		"network=off",
		"cannot",
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
	if !strings.Contains(egressOn, "egress: true") || !strings.Contains(egressOn, "can") {
		t.Errorf("when egress is granted the model must be told how to use it (egress: true):\n%s", egressOn)
	}

	stubCapabilities(t, nil)
	egressOff := a.sandboxNotes()
	if !strings.Contains(egressOff, "does not have net.egress") || strings.Contains(egressOff, "egress: true") {
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
	if !strings.Contains(on, "network=on") || !strings.Contains(on, "can") {
		t.Errorf("the on-state copy should say networking is possible:\n%s", on)
	}
	if !strings.Contains(on, "read-only") {
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
	if !strings.Contains(stale, "may be out of date") || !strings.Contains(stale, "http 502") {
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
	if !strings.Contains(bothLangs, "Executable languages (configured in sandboxd; used for command execution and skill scripts, **not** the selectable languages of workflow nodes): js, lua, python, ruby") {
		t.Errorf("the executable-languages line is missing:\n%s", bothLangs)
	}
	if !strings.Contains(bothLangs, "Workflow script languages (configured in sandboxd): lua, python") {
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
	if got := a.sandboxNotes(); !strings.Contains(got, "no **workflow script language** configured") {
		t.Errorf("an empty set must be stated explicitly:\n%s", got)
	}

	// An old sandboxd (field not reported -> nil): say nothing at all, rather than "no languages".
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		Network:   netInfo(sandboxinfo.NetworkOff),
		Langs:     []string{"lua"},
		FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "Workflow script languages") {
		t.Errorf("workflow script languages must not be mentioned when the peer reports no such field:\n%s", got)
	}

	// The browser is no longer part of the sandbox: its facts come from browserinfo and are stated by browserNotes
	// (see TestBrowserNotes), never here.
	sandboxinfo.Set(sandboxinfo.Info{
		Mode: "docker", Network: netInfo(sandboxinfo.NetworkOn), FetchedAt: time.Now(),
	})
	if got := a.sandboxNotes(); strings.Contains(got, "Browser") {
		t.Errorf("the sandbox notes must not talk about the browser any more:\n%s", got)
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

	// Any one of the sandbox entry points being granted counts as "can touch the sandbox".
	for _, tool := range []string{"exec", "run_skill_script", "shell"} {
		withTool := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{tool: true}}}
		if got := withTool.sandboxNotes(); !strings.Contains(got, "Sandbox environment (isolation mode docker") {
			t.Errorf("granting %s should inject the sandbox notes, got:\n%s", tool, got)
		}
	}
	// The browser is not a sandbox entry point any more (it has its own pool), so it must not drag the sandbox facts in.
	browserOnly := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{"browser": true}}}
	if got := browserOnly.sandboxNotes(); got != "" {
		t.Errorf("granting only browser must not inject sandbox environment notes, got:\n%s", got)
	}
}

// browserNotes states the four browser states: no pool configured / pool not answered / unavailable (with the reason) /
// available (headless or headed). "Unavailable" must always carry the reason: the model would otherwise misread "the tool
// is off" as "this site cannot be opened".
//
// Order matters: BrowserConfigured / Configured are sticky package-level singletons, so "nothing configured" is judged
// first, then the pool is injected for the remaining cases.
func TestBrowserNotes(t *testing.T) {
	a := &agentImpl{}

	if got := a.browserNotes(); !strings.Contains(got, "no browser pool configured") {
		t.Errorf("an unconfigured pool must say so, got:\n%s", got)
	}

	tools.SetBrowserPool("ws://browserg:27090/v1/browser", "")
	t.Cleanup(func() { tools.SetBrowserPool("", "") })

	// Configured but never answered: "unknown", never "cannot open pages".
	if !browserinfo.Configured() {
		if got := a.browserNotes(); !strings.Contains(got, "unknown") {
			t.Errorf("a pool that has not answered must be reported as unknown, got:\n%s", got)
		}
	}

	browserinfo.Set(browserinfo.Info{Available: false, Error: "no chromium in the image"})
	disabled := a.browserNotes()
	if !strings.Contains(disabled, "unavailable") || !strings.Contains(disabled, "no chromium in the image") {
		t.Errorf("an unavailable browser must come with the reason:\n%s", disabled)
	}

	browserinfo.Set(browserinfo.Info{
		Available: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: false,
	})
	headless := a.browserNotes()
	if !strings.Contains(headless, "headless only") || !strings.Contains(headless, "snapshot") {
		t.Errorf("headless-only must state the fact and how to observe:\n%s", headless)
	}

	browserinfo.Set(browserinfo.Info{
		Available: true, Version: "Chromium 141.0", HeadlessDefault: true, HeadedAvailable: true,
	})
	if got := a.browserNotes(); !strings.Contains(got, "headed=true") {
		t.Errorf("when headed is allowed the model must be told how to enable it:\n%s", got)
	}

	// Scope: an agent without the browser tool gains nothing from these facts.
	other := &agentImpl{toolCtx: &tools.Context{AllowedTools: map[string]bool{"read_file": true}}}
	if got := other.browserNotes(); got != "" {
		t.Errorf("the browser note must not be injected without the browser tool, got:\n%s", got)
	}
}
