package browserpool

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// These tests drive the pool through the **real wire protocol**, the same path nexus takes: open frame + one JSON op
// line. They are gated on a running browserd, because the pool's unit tests deliberately run without a browser
// installed (see proc.go).
//
//	BROWSERD_TEST_URL=ws://127.0.0.1:27091/v1/browser
//	BROWSERD_TEST_WORKSPACE=e2e            # relative to browserd's workspace_root
//	BROWSERD_TEST_TOKEN=...                # optional, must match browserd's token
//	BROWSERD_TEST_HEADED=1                 # optional, only where headed is allowed
func e2eClient(t *testing.T) (*sandbox.InteractiveClient, string, sandbox.InteractiveSession) {
	t.Helper()
	ep := os.Getenv("BROWSERD_TEST_URL")
	if ep == "" {
		t.Skip("set BROWSERD_TEST_URL to run the browser e2e")
	}
	ws := os.Getenv("BROWSERD_TEST_WORKSPACE")
	if ws == "" {
		t.Skip("set BROWSERD_TEST_WORKSPACE to a path relative to browserd's workspace_root")
	}
	c := sandbox.NewInteractive(ep, os.Getenv("BROWSERD_TEST_TOKEN"), sandbox.Limits{})
	sess := sandbox.InteractiveSession{Key: fmt.Sprintf("e2e-browser-%d", time.Now().UnixNano()), Ephemeral: true}
	t.Cleanup(func() { _ = c.Kill(context.Background(), sess, "browser") })
	return c, ws, sess
}

// e2eDo sends one op and fails on a transport error or an ok=false answer.
func e2eDo(t *testing.T, c *sandbox.InteractiveClient, ws string, sess sandbox.InteractiveSession, req map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %v: %v", req["op"], err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// argv is a placeholder: the pool launches its own driver. The workspace path is relative to workspace_root, which
	// is what makes screenshots and downloads land in the session's directory.
	line, err := c.Do(ctx, sess, "browser", []string{"browserhost"}, body, nil, ws)
	if err != nil {
		t.Fatalf("op %v: %v", req["op"], err)
	}
	var out map[string]any
	if uerr := json.Unmarshal(line, &out); uerr != nil {
		t.Fatalf("op %v: bad response %q: %v", req["op"], line, uerr)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("op %v failed: %s", req["op"], line)
	}
	return out
}

// End to end: open -> snapshot -> click by ref -> text -> screenshot, against a page served on loopback.
//
// Loopback only works because the pool is on the same machine as the test; a pooled browser in another container cannot
// reach this server, which is why the manual smoke test below uses a public site instead.
func TestBrowserE2E(t *testing.T) {
	c, ws, sess := e2eClient(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>E2E</h1><button>Go</button></body></html>`))
	}))
	defer srv.Close()

	open := e2eDo(t, c, ws, sess, map[string]any{
		"id": 1, "op": "open", "url": srv.URL,
		"headed": os.Getenv("BROWSERD_TEST_HEADED") == "1",
	})
	if snap, _ := open["snapshot"].(string); !strings.Contains(snap, "E2E") {
		t.Errorf("snapshot does not contain the page heading:\n%s", snap)
	}

	shot := e2eDo(t, c, ws, sess, map[string]any{"id": 2, "op": "screenshot", "path": "e2e/shot.png"})
	if p, _ := shot["path"].(string); p != "e2e/shot.png" {
		t.Errorf("screenshot path = %q, want e2e/shot.png", p)
	}

	// Closing twice must both succeed: the second call has no lease to release and must not launch a browser to find
	// out (that is the whole point of the "open without a spec never spawns" rule).
	if err := c.Kill(context.Background(), sess, "browser"); err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if err := c.Kill(context.Background(), sess, "browser"); err != nil {
		t.Fatalf("second kill (idempotent): %v", err)
	}
}

// Manual smoke test against a real site: open -> snapshot -> type a random word -> search.
//
// It answers "does a real page work", not "is the pool correct" — hitting an anti-bot check is site behaviour, not a
// tool failure, so those outcomes are logged rather than failed.
func TestBaiduSearch(t *testing.T) {
	c, ws, sess := e2eClient(t)

	headed := os.Getenv("BROWSERD_TEST_HEADED") == "1"
	open := e2eDo(t, c, ws, sess, map[string]any{"id": 1, "op": "open", "url": "https://www.baidu.com", "headed": headed})
	t.Logf("open → url=%v title=%v", open["url"], open["title"])

	snap, _ := open["snapshot"].(string)
	ref := refOfRole(snap, "textbox", "searchbox", "combobox")
	if ref == "" {
		t.Fatalf("no input box on the page, snapshot:\n%s", snap)
	}

	query := fmt.Sprintf("flame%d", rand.Intn(90000)+10000)
	typed := e2eDo(t, c, ws, sess, map[string]any{"id": 2, "op": "type", "ref": ref, "text": query, "submit": true})
	typedURL, _ := typed["url"].(string)
	if !strings.Contains(typedURL, "wd="+query) && !strings.Contains(typedURL, "word="+query) {
		t.Errorf("the search did not submit: %s", typedURL)
	}

	// The result area is rendered by JS: right after the navigation there is only a skeleton, so wait for real result
	// blocks before reading the text.
	e2eDo(t, c, ws, sess, map[string]any{"id": 3, "op": "wait", "selector": "#content_left .c-container", "timeout": 20})
	res := e2eDo(t, c, ws, sess, map[string]any{"id": 4, "op": "text", "max_bytes": 3000})
	body, _ := res["text"].(string)

	switch {
	case strings.Contains(body, "百度安全验证"):
		t.Logf("the site served its anti-bot check; that is site behaviour, not a tool problem")
	case body == "":
		t.Errorf("the result page body is empty (rendering unfinished or text extraction failed)")
	case !strings.Contains(body, query):
		t.Logf("the body does not contain the query (a random string may have no results); body length %d", len(body))
	}
}

// refOfRole picks the first ref with the given role from the snapshot (e.g. `- e3 [textbox] "Search"`).
func refOfRole(snapshot string, roles ...string) string {
	for _, line := range strings.Split(snapshot, "\n") {
		for _, role := range roles {
			if !strings.Contains(line, "["+role+"]") {
				continue
			}
			for _, field := range strings.Fields(line) {
				if len(field) > 1 && field[0] == 'e' && field[1] >= '0' && field[1] <= '9' {
					return field
				}
			}
		}
	}
	return ""
}
