// Package sandboxinfo holds the sandboxd capability snapshot: whether the sandbox may reach the network (declared) and what is inside it
// (probed).
//
// Why a shared package rather than something inside the agent: fetching (HTTP + periodic refresh) and using (assembling the prompt) are two
// different things - main does the fetching, the agent does the reading. The registration style matches tools.SetSandbox (a package-level
// singleton).
//
// Why it must refresh periodically: changing sandboxd's configuration and restarting is routine (the network declaration and the image both
// change), and fetching once at nexus startup would make the prompt start lying immediately - and "the prompt must not lie" is exactly why
// this mechanism exists: the agent decides from it whether to write networked code and which libraries to use.
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

// Environment is the result of probing the sandbox environment (aligned with the JSON structure on the sandboxd side).
type Environment struct {
	// Text is the raw output of each probe (a "label: value" line each). It is deliberately not parsed into a structure - "what is inside
	// the sandbox" is the sandbox's own fact, and the reader is the LLM.
	Text     string    `json:"text"`
	ProbedAt time.Time `json:"probed_at"`
	// Error non-empty means the probe as a whole failed on the sandboxd side, and Text cannot be trusted.
	Error string `json:"error,omitempty"`
}

// Info is one capability snapshot.
type Info struct {
	// Mode is the isolation method (none / nsjail / docker).
	Mode string `json:"mode"`
	// Network is the per-channel network policy (**a deployment ceiling**, not a fact about one call). See NetworkInfo.
	Network NetworkInfo `json:"network"`
	Langs   []string    `json:"langs"`
	// CodeLangs are the languages usable as a **workflow code node** - the ones the sandbox would actually serve right now (configured in
	// code.pools on the sandboxd side and with a successfully resolved interpreter entry).
	//
	// Not the same thing as Langs: Langs are "the languages the sandbox can run", which the single-worker channel uses too (a language
	// that only runs skill scripts need not have a code pool), and choosing a code node language from it lands on unsupported_lang.
	//
	// nil and an empty slice mean different things, so do not merge them: nil = the peer did not report the field (older version / a
	// field name mismatch), empty = the peer explicitly says "not a single code language is configured". The basis is encoding/json
	// decoding `[]` into a non-nil empty slice - sandboxinfo_test.go pins that behaviour.
	CodeLangs []string `json:"code_langs"`
	// LegacyCodeLangs is the old field name (script_langs before sandboxd 2026-10).
	// It is only read for the transition: CodeLangs falls back to it when the new field is missing, so that neither "sandboxd upgraded
	// first" nor "nexus upgraded first" turns the usable language list into "unknown".
	LegacyCodeLangs []string `json:"script_langs,omitempty"`

	Environment Environment `json:"environment"`

	// Browser is the browser capability (sandboxd's measured conclusion: whether the image has Chromium and whether it can run headed).
	//
	// nil and "Enabled=false" mean different things, so do not merge them: nil = the peer did not report the field (an older sandboxd / a
	// field name mismatch), false = the peer explicitly says "unavailable" and gives a reason.
	// The former must not lead the agent to conclude "the browser is broken"; it can only say "this sandbox did not tell me".
	Browser *BrowserInfo `json:"browser,omitempty"`

	// FetchedAt is when the last **successful** fetch happened.
	FetchedAt time.Time `json:"-"`
	// Err is why the last fetch failed. When non-empty, the fields above are the last successful snapshot (possibly stale) rather than
	// "no capability" - the two mean completely different things to the agent, and the copy must keep them apart.
	Err string `json:"-"`
}

// NetworkInfo is the per-channel network policy (aligned with sandboxd's capabilities.network).
//
// Why split by channel: exec goes through the single-worker channel and creates a process per request, so the network can be switched per
// request; code is a global pooled unit whose network is baked in when the unit is created and is always off. A single combined value would
// necessarily give a wrong conclusion on one of the two channels.
//
// ⚠️ Both values are **deployment ceilings**, not "may this call reach the network": the final answer also layers on the tenant permission
// (capability.Egress) and the request flag. The prompt must speak from the combined conclusion, see the agent's sandboxNotes.
type NetworkInfo struct {
	// Exec is the policy of /v1/exec (skill scripts included): off / on-request / on.
	Exec string `json:"exec"`
	// Code is the policy of /v1/code (workflow code nodes): always off - sandboxd offers no configuration for it.
	Code string `json:"code"`
	// Script is the old field name (script before sandboxd 2026-10). Read for the transition only: UnmarshalJSON normalises it into Code,
	// and callers only look at Code.
	Script string `json:"script,omitempty"`
}

// UnmarshalJSON accepts two old formats:
//
//  1. an even older sandboxd reported a single string ("off" / "on") with no per-channel split - it is read as the exec tier, with code
//     always off;
//  2. before 2026-10 the split existed but the second channel was called "script" - it is normalised into Code.
//
// Without this compatibility the upgrade order would be frozen: were sandboxd upgraded first, nexus would fail to parse and degrade the
// whole snapshot to "unknown" (which does not lie, but leaves no information for a while). A dozen extra lines removes the need to
// coordinate a rollout order.
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

// BrowserInfo is the browser capability snapshot (aligned with sandboxd's daemon.BrowserInfo).
type BrowserInfo struct {
	Enabled         bool   `json:"enabled"`
	Engine          string `json:"engine,omitempty"`
	Version         string `json:"version,omitempty"`
	HeadlessDefault bool   `json:"headless_default"`
	HeadedAvailable bool   `json:"headed_available"`
	Error           string `json:"error,omitempty"`
}

// Network policy values (aligned with sandboxd's daemon.NetworkXxx).
const (
	// NetworkOff means no network at all (including refusing a request that asks for one).
	NetworkOff = "off"
	// NetworkOn means always allowed, regardless of the request.
	NetworkOn = "on"
	// NetworkOnRequest decides per request and is only meaningful for the exec channel: whether egress actually happens depends on the
	// tenant permission.
	//
	// The prompt must **not** pass this tier to the agent verbatim - it cannot decide from it whether to write networked code; the agent
	// side has to combine it with the tenant capability (see the agent's sandboxNotes).
	NetworkOnRequest = "on-request"
)

var (
	current    atomic.Pointer[Info]
	configured atomic.Bool
)

// Set injects a capability snapshot.
func Set(i Info) {
	current.Store(&i)
	configured.Store(true)
}

// Get returns the current snapshot; a zero value when nothing was ever injected (callers tell the two apart with Configured).
func Get() Info {
	if p := current.Load(); p != nil {
		return *p
	}
	return Info{}
}

// Configured reports whether a snapshot was ever injected. Before that the agent side must say nothing about the environment - better to
// stay silent than to invent an environment for it.
func Configured() bool { return configured.Load() }

// Ready reports whether the sandbox is usable as a service: at least one capability snapshot has been fetched successfully (Mode non-empty).
//
// This is the admission criterion for sandbox tools (exec / shell): without knowing what environment the sandbox is, do not run commands
// in it. Note the difference from Err: a non-empty Err only means "the last refresh failed" - Mode is still non-empty and the snapshot is
// the last successful one (stale but usable), which is no reason to refuse service.
func Ready() bool { return Get().Mode != "" }

// CodeLangs returns "the languages usable as a workflow code node"; ok says whether this snapshot is usable.
//
// ok=false merges two cases: it was never fetched successfully (Mode empty, the same criterion as the agent's sandboxNotes), or the peer
// reported no language list (an even older sandboxd has neither field). Neither may be guessed - the caller either falls back to its own
// default (the editor) or says nothing (the prompt).
//
// Returning (empty, true) is a legal result: the sandbox is there and has no code language configured.
func CodeLangs() ([]string, bool) {
	info := Get()
	if info.Mode == "" {
		return nil, false
	}
	langs := info.CodeLangs
	if langs == nil {
		// An older sandboxd reports script_langs only: read the fallback, so it is not "the sandbox is there but the languages are
		// unknown".
		langs = info.LegacyCodeLangs
	}
	if langs == nil {
		return nil, false
	}
	return langs, true
}

// OS reads the operating system ("windows" / "linux") out of the raw sandbox environment probe; it returns an empty string when unreadable.
//
// capabilities already gives the OS on the `系统: …` line (see sandboxd's probeHost / formatOSRelease), so there is no need to guess here,
// and nexus's host runtime.GOOS must not be used either: the sandbox may run on another platform. It reads that one line and infers no
// capability, which is why it does not conflict with "hand Text to the LLM as is".
//
// When the probe failed (a non-empty Error) it returns an empty string - untrusted text must not be used to pick a shell.
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
		// os-release only exists on the Linux family; a readable `系统:` line that is not Windows means Linux.
		return "linux"
	}
	return ""
}

// MaxBodyBytes caps how much of the response body is read: this text goes into the LLM context, so an abnormal response must not blow it up.
const MaxBodyBytes = 1 << 20

// FetchTimeout is the ceiling for one fetch. sandboxd is an internal service, so a generous value is fine - dragging on would eat into the
// refresh interval and slow down the convergence of a graceful shutdown.
const FetchTimeout = 10 * time.Second

// firstRetry is the retry interval while no snapshot has been obtained yet (it returns to interval once the first one is in).
//
// Why a separate tier: StartRefresher fetches immediately on start, and if sandboxd is not ready at that moment (the deploy order is not
// fixed and restarts are routine), the next attempt at interval would be a full minute away - while nexus's readiness check trusts this
// snapshot, so the whole process would sit there returning 503. A cold start is betting on "the peer is merely slow", at the cost of one
// internal GET.
//
// Conversely, a failure once a snapshot is in hand is not expedited: it is probably genuinely broken, and a shorter retry only amplifies
// logs and load - and Ready() is still true at that point (a stale but usable snapshot), so there is no need to hurry a replacement.
//
// It is a variable rather than a constant only so tests can shrink the wait to milliseconds.
var firstRetry = 2 * time.Second

// Fetch pulls one capability snapshot from sandboxd.
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

// StartRefresher starts the background refresh: fetch immediately, then once per interval until ctx ends.
//
// A failure **does not overwrite** the last successful data and only records Err - handing the agent a "possibly stale but real
// environment" is far more useful than degrading to "unknown" (the copy notes that it may be stale). It logs only on success<->failure
// transitions, so an absent sandboxd does not turn the log into noise.
func StartRefresher(ctx context.Context, endpoint, token string, interval time.Duration) {
	if endpoint == "" {
		xlog.Error("sandboxinfo: capabilities endpoint is empty - the agent will see no sandbox environment")
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
