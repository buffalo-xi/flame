package daemon

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"flame/tools/code"
	"flame/tools/sandbox"
)

// End-to-end acceptance of the duplex endpoint: a real sandboxd service + a real WebSocket + a real interpreter
// (none mode).
//
// What is verified is the external contract of this round's logs rework: a script's print output (success path) and its
// traceback (failure path) must both come back to the client through /v1/code's Frame.Logs.
//
// It needs a sandboxd running locally (127.0.0.1:27081 by default, overridable with SANDBOXD_TEST_CODE_URL) and skips
// otherwise. The language is python (sandboxd resolves the entry name from the config; the client only sends the
// language name).

const defaultCodeURL = "ws://127.0.0.1:27081/v1/code"

// codeE2EForTest probes sandboxd's reachability: skip when /healthz is unreachable.
func codeE2EForTest(t *testing.T) string {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_CODE_URL")
	if url == "" {
		url = defaultCodeURL
	}
	base := strings.NewReplacer("ws://", "http://", "wss://", "https://").Replace(url)
	if i := strings.Index(base, "/v1/"); i > 0 {
		base = base[:i]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_CODE_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return url
}

// TestScriptE2ELogsPassthrough verifies Frame.Logs passthrough end to end.
func TestScriptE2ELogsPassthrough(t *testing.T) {
	url := codeE2EForTest(t)
	c := code.NewClient(url, "", sandbox.Limits{})

	t.Run("success logs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		r, err := c.Execute(ctx, "python",
			"print('hello-logs')\n__return__ = 'ok'\n", nil, 30)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if r.Value != "ok" {
			t.Fatalf("result = %v (%T), want ok", r.Value, r.Value)
		}
		if !strings.Contains(r.Logs, "hello-logs") {
			t.Fatalf("logs = %q, want contain hello-logs", r.Logs)
		}
	})

	t.Run("error logs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := c.Execute(ctx, "python", "raise ValueError('boom-logs')\n", nil, 30)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		se, ok := err.(*code.Error)
		if !ok {
			t.Fatalf("err type = %T, want *code.Error", err)
		}
		if !strings.Contains(se.Message, "boom-logs") {
			t.Fatalf("message = %q, want contain boom-logs", se.Message)
		}
		if !strings.Contains(se.Logs, "Traceback") {
			t.Fatalf("logs = %q, want contain Traceback", se.Logs)
		}
	})
}
