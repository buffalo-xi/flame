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

// The new format: network is a per-channel object decoded field by field, and the language list is called code_langs.
func TestNetworkInfoPerChannel(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":{"exec":"on-request","code":"off"},"langs":["lua"],"code_langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Exec != NetworkOnRequest || info.Network.Code != NetworkOff {
		t.Errorf("per-channel policy was decoded wrong: %+v", info.Network)
	}
	if len(info.CodeLangs) != 1 || len(info.LegacyCodeLangs) != 0 {
		t.Errorf("code_langs should land in the new field: %+v", info)
	}
}

// The format from before the 2026-10 rename: the second channel is called script and the language list is script_langs.
// Both have to be normalised, or during "sandboxd upgraded first / nexus upgraded first" the whole sandbox becomes "unknown".
func TestNetworkInfoLegacyScriptField(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":{"exec":"on-request","script":"off"},"langs":["lua"],"script_langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Code != NetworkOff {
		t.Errorf("the legacy script field should be normalised into Code, got %+v", info.Network)
	}
	if len(info.LegacyCodeLangs) != 1 || len(info.CodeLangs) != 0 {
		t.Errorf("the legacy script_langs should land in the compatibility field: %+v", info)
	}
}

// An older sandboxd reports a single string: it is read as the exec tier, with code always off.
// Without this compatibility the upgrade order would be frozen (sandboxd upgraded first -> nexus degrades the whole snapshot to "unknown").
func TestNetworkInfoLegacyString(t *testing.T) {
	var info Info
	raw := `{"mode":"docker","network":"on","langs":["lua"]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Network.Exec != NetworkOn || info.Network.Code != NetworkOff {
		t.Errorf("the legacy string should map to exec, with code=off, got %+v", info.Network)
	}
}

// The three states must stay distinguishable: nil (the peer did not report the field, an older version) is not the same as an empty slice
// (the peer says "there are none").
// The basis is encoding/json decoding `[]` into a non-nil empty slice - if that behaviour changed, this fails first, rather than surfacing
// later as "the sandbox has no duplex language configured yet the editor offers three".
func TestCodeLangsTriState(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	t.Cleanup(func() {
		Set(old)
		if !oldConfigured {
			// Set makes configured true, so restore "never injected" here - the same source as the agent's sandboxNotes criterion, and it
			// must not leave dirty state behind for other tests.
			configured.Store(false)
		}
	})

	// Never fetched successfully (Mode empty): unusable, and the caller should fall back to its own default.
	Set(Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})
	if langs, ok := CodeLangs(); ok {
		t.Errorf("no language list should be given without a snapshot, got %v", langs)
	}

	// An older sandboxd: no such field -> nil -> unusable (fall back, not "there are no languages").
	var legacy Info
	if err := json.Unmarshal([]byte(`{"mode":"docker","langs":["lua"]}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy payload: %v", err)
	}
	if legacy.CodeLangs != nil {
		t.Fatalf("a missing field should decode to nil, got %v", legacy.CodeLangs)
	}
	legacy.FetchedAt = time.Now()
	Set(legacy)
	if langs, ok := CodeLangs(); ok {
		t.Errorf("a missing field must not be read as \"no languages\", got %v", langs)
	}

	// An explicit empty set: usable but empty (the prompt has to say "the sandbox has no workflow script language configured").
	var empty Info
	if err := json.Unmarshal([]byte(`{"mode":"docker","code_langs":[]}`), &empty); err != nil {
		t.Fatalf("unmarshal empty payload: %v", err)
	}
	if empty.CodeLangs == nil {
		t.Fatal("JSON [] should decode to a non-nil empty slice - the nil/empty boundary rests on this")
	}
	empty.FetchedAt = time.Now()
	Set(empty)
	langs, ok := CodeLangs()
	if !ok || len(langs) != 0 {
		t.Errorf("an empty set should be (empty, true), got (%v, %v)", langs, ok)
	}

	// A normal report.
	Set(Info{Mode: "docker", CodeLangs: []string{"lua", "python"}, FetchedAt: time.Now()})
	if langs, ok := CodeLangs(); !ok || len(langs) != 2 {
		t.Errorf("a normal report should be usable, got (%v, %v)", langs, ok)
	}
}

// Ready is the admission criterion for sandbox tools: with no snapshot ever fetched (Mode empty) it refuses service.
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
		t.Error("with no snapshot ever fetched (Mode empty) it must not be Ready")
	}
	Set(Info{Err: "dial failed"})
	if Ready() {
		t.Error("with only an error and no Mode it must not be Ready")
	}
	Set(Info{Mode: "docker"})
	if !Ready() {
		t.Error("with a Mode it should be Ready")
	}
}

// Cold start: while sandboxd is not up yet it must not wait a full interval, or nexus's readiness check sits on a 503 for a whole refresh
// cycle. interval is deliberately an hour here - only firstRetry is at work, so a regression makes the test time out rather than slow down.
func TestStartRefresherRetriesFastUntilFirstSnapshot(t *testing.T) {
	old := Get()
	oldConfigured := Configured()
	oldRetry := firstRetry
	oldInfoLogger, oldErrorLogger := xlog.InfoLogger, xlog.ErrorLogger
	// Without xlog.Init the loggers are nil and the failure path would log - this is a unit test, so discard it (not calling Init avoids
	// touching the disk and starting the rotation goroutine).
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
	Set(Info{}) // no snapshot ever fetched

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
		t.Fatalf("it should retry fast enough to succeed after the first two failures, hits=%d", hits.Load())
	}
	// No expedited retry once it succeeded (the next one waits a full interval), so exactly three.
	if got := hits.Load(); got != 3 {
		t.Errorf("it must not keep retrying fast after obtaining a snapshot, hits=%d", got)
	}
	// This fixture uses the legacy format: network is a string and the language list is script_langs.
	// It pins both compatibility paths as a side effect: the string maps to exec (code recorded as off), and the legacy field name is still
	// usable through the fallback.
	if info := Get(); info.Network.Exec != NetworkOff || info.Network.Code != NetworkOff ||
		len(info.LegacyCodeLangs) != 1 || len(info.CodeLangs) != 0 {
		t.Errorf("the snapshot should land completely (the legacy format must stay compatible), got %+v", info)
	}
	if langs, ok := CodeLangs(); !ok || len(langs) != 1 {
		t.Errorf("the legacy script_langs field should be usable through the fallback, got (%v, %v)", langs, ok)
	}
}

// OS reads the sandbox OS from the `系统:` line of capabilities; untrusted or missing input returns an empty string, leaving the caller (the
// default shell choice) to pick its own fallback rather than inventing a platform.
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
		{"linux os-release", Environment{Text: "OS: Debian GNU/Linux 12\nPython version: 3.13"}, "linux"},
		{"windows host", Environment{Text: "OS: windows/amd64"}, "windows"},
		{"probe failed -> unknown", Environment{Text: "OS: windows/amd64", Error: "boom"}, ""},
		{"no system line -> unknown", Environment{Text: "Python version: 3.13"}, ""},
		{"empty -> unknown", Environment{}, ""},
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
