package sandbox

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"os"
)

// Manual smoke test: a real site (Baidu) through the whole chain - open -> snapshot -> type a random word -> search.
//
// Gate: SANDBOXD_TEST_BROWSER_URL=ws://127.0.0.1:27080/v1/interactive
// Only suitable with mode=none (browser and test process on one machine, sharing loopback).
func TestBaiduSearch(t *testing.T) {
	ep := os.Getenv("SANDBOXD_TEST_BROWSER_URL")
	if ep == "" {
		t.Skip("set SANDBOXD_TEST_BROWSER_URL to run")
	}

	// Must sit under sandboxd's allowed_mount_roots (nexus/data on a local box).
	wsDir := os.Getenv("BROWSER_TEST_WORKSPACE")
	if wsDir == "" {
		t.Skip("set BROWSER_TEST_WORKSPACE to a dir under sandboxd's allowed_mount_roots")
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Logf("workspace: %s", wsDir)

	c := NewInteractive(ep, "", Limits{})
	sess := InteractiveSession{Key: fmt.Sprintf("baidu-%d", time.Now().UnixNano()), Ephemeral: true}
	mounts := []Mount{{Host: wsDir, Guest: "/workspace"}}
	defer func() { _ = c.Kill(context.Background(), sess, "browser") }()

	do := func(id int, req map[string]any) map[string]any {
		t.Helper()
		req["id"] = id
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		line, err := c.Do(ctx, sess, "browser", []string{"browserhost"}, mustJSON(t, req), mounts, "/workspace")
		if err != nil {
			t.Fatalf("op %v: %v", req["op"], err)
		}
		res := decode(t, line)
		if ok, _ := res["ok"].(bool); !ok {
			t.Fatalf("op %v failed: %s", req["op"], line)
		}
		return res
	}

	headed := os.Getenv("BROWSER_TEST_HEADED") == "1"
	open := do(1, map[string]any{"op": "open", "url": "https://www.baidu.com", "headed": headed})
	t.Logf("open → url=%v title=%v", open["url"], open["title"])
	t.Logf("snapshot:\n%s", open["snapshot"])

	snap, _ := open["snapshot"].(string)
	ref := refOfRole(snap, "textbox", "searchbox", "combobox")
	if ref == "" {
		t.Fatalf("no input box on the page, snapshot:\n%s", snap)
	}

	query := fmt.Sprintf("flame%d", rand.Intn(90000)+10000)
	t.Logf("query: %s (ref=%s)", query, ref)

	typed := do(2, map[string]any{"op": "type", "ref": ref, "text": query, "submit": true})
	t.Logf("type → url=%v", typed["url"])

	// Did the search really submit: look for wd=<query> in the address bar.
	// That is a different question from whether Baidu shows results - hitting the anti-bot check is not a tool failure.
	typedURL, _ := typed["url"].(string)
	if !strings.Contains(typedURL, "wd="+query) && !strings.Contains(typedURL, "word="+query) {
		t.Errorf("the search did not submit: %s", typedURL)
	}

	// The result area is rendered by JS: right after the navigation there is only a skeleton (innerText is
	// almost empty), so wait for real result blocks under #content_left or the page looks empty.
	do(3, map[string]any{"op": "wait", "selector": "#content_left .c-container", "timeout": 20})

	typeRes := do(4, map[string]any{"op": "text", "max_bytes": 3000})
	url, _ := typeRes["url"].(string)
	body, _ := typeRes["text"].(string)
	t.Logf("result → url=%v", url)
	t.Logf("text (first 400 chars):\n%.400s", body)

	switch {
	case strings.Contains(body, "百度安全验证"):
		t.Logf("Baidu served its anti-bot check (slider); that is site behaviour, not a tool problem")
	case body == "":
		t.Errorf("the result page body is empty (rendering unfinished or text extraction failed)")
	case !strings.Contains(body, query):
		t.Logf("the body does not contain the query (a random string may simply have no results); body length %d", len(body))
	}

	shot := do(5, map[string]any{"op": "screenshot", "path": "baidu-search.png"})
	t.Logf("screenshot → %v (%v bytes)", shot["path"], shot["bytes"])
}

// refOfRole picks the first ref with the given role from the snapshot (e.g. `- e3 [textbox] "百度一下"`).
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
