package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// BrowserInfo is the browser capability snapshot (the browser section of /v1/capabilities).
//
// Why this item exists instead of letting the model "just try a call": whether the image actually has Chromium is
// only known by the sandbox itself. Without it, the model misreads "the browser is unavailable" as "the page will not
// open" and retries in the wrong direction — the same reason as langs / environment.
type BrowserInfo struct {
	Enabled bool `json:"enabled"`
	// Engine / Version come from an actual `browserhost --selftest` run (not a config statement).
	Engine  string `json:"engine,omitempty"`
	Version string `json:"version,omitempty"`
	// HeadlessDefault reports whether this deployment defaults to headless; HeadedAvailable whether headed is
	// possible under the current isolation mode.
	HeadlessDefault bool `json:"headless_default"`
	HeadedAvailable bool `json:"headed_available"`
	// A non-empty Error is the reason it is unavailable (not enabled / binary missing / no browser in the image).
	Error string `json:"error,omitempty"`
}

// browserProbeTimeout caps the self-test. It only runs `<browserhost> --selftest` (which internally runs
// `chromium --version`), it never launches the browser; under docker the cost is mostly building the container.
const browserProbeTimeout = 30 * time.Second

const (
	// browserProbeRetryInterval is the retry interval after a failed probe. Failures are mostly environmental
	// (the browser is being updated, existing sessions are busy, a container just started), and a quick retry
	// usually heals them.
	browserProbeRetryInterval = 30 * time.Second
	// browserProbeRefreshInterval is the re-check interval while it is available. Under docker every probe builds a
	// container, so it does not follow the call rate; once every few minutes is enough.
	browserProbeRefreshInterval = 5 * time.Minute
)

// startBrowserProbe starts the asynchronous capability probe (retrying on failure, re-checking periodically on
// success).
//
// Asynchronous on purpose: a failed probe must not block sandboxd startup (the browser is an optional capability),
// but the result must be reported honestly — so an "unavailable, probing" state is stored before it completes
// rather than optimistically claiming "available". An optimistic default would make the model start clicking pages
// while the image is not ready yet.
//
// Why it cannot probe only once: the result is broadcast continuously through /v1/capabilities, and failures are
// usually temporary. Probing once means "one failure = the browser is disabled forever until a restart" — exactly
// the amplification that caused the `bad output ""` incident.
func (s *Server) startBrowserProbe() {
	if !s.cfg.Browser.Enabled {
		info := BrowserInfo{Enabled: false, Error: "disabled by config (browser.enabled: false)"}
		s.browser.Store(&info)
		return
	}
	// Outside none it can never be true (validateBrowser already rejects it), so `Mode == "none"` is not repeated.
	headed := s.cfg.AllowHeadedFor(s.cfg.Mode)
	initial := BrowserInfo{
		Enabled:         false,
		HeadlessDefault: !s.cfg.Browser.Headed,
		HeadedAvailable: headed,
		Error:           "probing browser version",
	}
	s.browser.Store(&initial)

	go func() {
		prevReady := false
		lastErr := ""
		for {
			info, ok := s.probeBrowserOnce(headed)
			s.browser.Store(&info)

			switch {
			case ok && !prevReady:
				xlog.Info("sandboxd: browser ready (%s, headed_available=%v)", info.Version, info.HeadedAvailable)
			case !ok && info.Error != lastErr:
				// The same error is logged only when it first appears: the retry interval is 30s, and logging every
				// attempt would turn the log into noise.
				xlog.Error("sandboxd: browser unavailable: %s", info.Error)
			}

			prevReady = ok
			if ok {
				lastErr = ""
			} else {
				lastErr = info.Error
			}

			wait := browserProbeRetryInterval
			if ok {
				wait = browserProbeRefreshInterval
			}
			time.Sleep(wait)
		}
	}()
}

// probeBrowserOnce runs `<browserhost> --selftest` once and returns the capability snapshot plus whether it is
// available.
//
// It goes through BrowserArgv rather than assembling its own command: the probe must see the same exec_path as a real
// call (resolved per mode), otherwise "the probe says available but open cannot find the browser" happens.
func (s *Server) probeBrowserOnce(headed bool) (BrowserInfo, bool) {
	info := BrowserInfo{
		HeadlessDefault: !s.cfg.Browser.Headed,
		HeadedAvailable: headed,
	}

	argv, err := s.cfg.BrowserArgv(s.cfg.Mode)
	if err != nil {
		info.Error = err.Error()
		return info, false
	}
	argv = append(argv, "--selftest")

	ctx, cancel := context.WithTimeout(context.Background(), browserProbeTimeout)
	defer cancel()

	// stderr must be captured: the failure reason almost only appears there (wrong binary version, missing
	// libraries, no browser in the image). It used to be io.Discard, which degraded the symptom to a single
	// `bad output ""`.
	errBuf := &cappedWriter{n: 4096}

	spec := &sandbox.Spec{
		Argv:   argv,
		Stderr: errBuf,
		Limits: s.cfg.BrowserLimits(sandbox.Limits{}),
	}
	proc, err := s.exec.Start(ctx, spec)
	if err != nil {
		info.Error = fmt.Sprintf("start browserhost --selftest: %v", err)
		return info, false
	}
	out, _ := io.ReadAll(io.LimitReader(proc.Stdout(), 4096))
	_ = proc.Wait()

	var payload struct {
		Engine  string `json:"engine"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &payload); err != nil || payload.Version == "" {
		info.Error = fmt.Sprintf("browser selftest: bad output %q", truncateText(string(out), 200))
		if tail := strings.TrimSpace(errBuf.String()); tail != "" {
			info.Error += "; stderr: " + truncateText(tail, 300)
		}
		return info, false
	}

	info.Enabled = true
	info.Engine = payload.Engine
	info.Version = payload.Version
	return info, true
}

// cappedWriter keeps only the first n bytes (the stderr buffer used by the probe): what is wanted is the beginning
// of "why it will not start", not a log that could grow without bound.
type cappedWriter struct {
	buf []byte
	n   int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if remain := w.n - len(w.buf); remain > 0 {
		if len(p) > remain {
			w.buf = append(w.buf, p[:remain]...)
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return string(w.buf) }

// BrowserSnapshot returns the current browser capability snapshot (an unavailable state while still probing).
func (s *Server) BrowserSnapshot() BrowserInfo {
	if p := s.browser.Load(); p != nil {
		return *p
	}
	return BrowserInfo{Error: "browser not probed"}
}

// truncateText truncates over-long text (only used for logs / error messages).
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
