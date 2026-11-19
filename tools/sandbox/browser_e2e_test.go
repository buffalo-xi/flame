package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// End to end: nexus Do -> sandboxd /v1/interactive (tool_id=browser,
// argv replaced by sandboxd with the configured browserhost) -> Chromium in the isolation unit.
//
// Gate: SANDBOXD_TEST_BROWSER_URL=ws://127.0.0.1:27081/v1/interactive
// (SANDBOXD_TEST_TOKEN optional). Only meaningful with mode=none: the page is served on the test
// process's loopback port, which a browser inside a container cannot reach.
func TestBrowserE2E(t *testing.T) {
	ep := os.Getenv("SANDBOXD_TEST_BROWSER_URL")
	if ep == "" {
		t.Skip("set SANDBOXD_TEST_BROWSER_URL to run browser e2e")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>E2E</h1><button>Go</button></body></html>`))
	}))
	defer srv.Close()

	// The mount point has to sit under sandboxd's allowed_mount_roots, so the workspace comes from an
	// environment variable and must not be the system temp dir (production only allows the storage root).
	ws := os.Getenv("BROWSER_TEST_WORKSPACE")
	if ws == "" {
		t.Skip("set BROWSER_TEST_WORKSPACE to a dir under sandboxd's allowed_mount_roots")
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}

	c := NewInteractive(ep, os.Getenv("SANDBOXD_TEST_TOKEN"), Limits{})
	sess := InteractiveSession{Key: fmt.Sprintf("e2e-browser-%d", time.Now().UnixNano()), Ephemeral: true}
	mounts := []Mount{{Host: ws, Guest: "/workspace"}}
	defer func() {
		_ = c.Kill(context.Background(), sess, "browser")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	line, err := c.Do(ctx, sess, "browser", []string{"browserhost"},
		mustJSON(t, map[string]any{"id": 1, "op": "open", "url": srv.URL}), mounts, "/workspace")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	res := decode(t, line)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("open not ok: %s", line)
	}
	snap, _ := res["snapshot"].(string)
	if !strings.Contains(snap, `[button] "Go"`) {
		t.Fatalf("no button in the snapshot:\n%s", snap)
	}

	// Take the button's ref from the snapshot and click it - this proves the model's workflow really runs.
	ref := refFor(snap, "Go")
	if ref == "" {
		t.Fatalf("no ref in the snapshot:\n%s", snap)
	}
	line, err = c.Do(ctx, sess, "browser", []string{"browserhost"},
		mustJSON(t, map[string]any{"id": 2, "op": "click", "ref": ref}), mounts, "/workspace")
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if ok, _ := decode(t, line)["ok"].(bool); !ok {
		t.Fatalf("click not ok: %s", line)
	}

	// The browser shares the interactive registry with the shell: after a kill the same session starts again.
	if err := c.Kill(ctx, sess, "browser"); err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(line, &out); err != nil {
		t.Fatalf("bad response %q: %v", line, err)
	}
	return out
}

// refFor finds the ref of the element whose name matches (e.g. `- e3 [button] "Go"`).
func refFor(snapshot, name string) string {
	for _, line := range strings.Split(snapshot, "\n") {
		if !strings.Contains(line, `"`+name+`"`) {
			continue
		}
		for _, field := range strings.Fields(line) {
			if len(field) > 1 && field[0] == 'e' && field[1] >= '0' && field[1] <= '9' {
				return field
			}
		}
	}
	return ""
}
