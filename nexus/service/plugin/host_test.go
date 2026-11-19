package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flame/tools/xlog"
)

// TestMain gives xlog a temporary directory: Registry.Refresh logs, and the logger is nil while xlog
// has not been initialised (production initialises it in main).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "plugin-test-logs")
	if err == nil {
		_ = xlog.Init(dir, "plugin-test")
	}
	code := m.Run()
	_ = xlog.Close()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

// These cases really do start python subprocesses. They are skipped when no interpreter is available
// (the same style as SANDBOXD_TEST_*), and PLUGIN_TEST_PYTHON may name one explicitly (python on
// Windows, python3 on Linux).

func pythonBin(t *testing.T) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("PLUGIN_TEST_PYTHON")); v != "" {
		return v
	}
	for _, name := range []string{"python", "python3"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no python available, skipping the plugin host tests (set PLUGIN_TEST_PYTHON to name an interpreter)")
	return ""
}

// pluginRoot is the default plugin directory in the repository: nexus/data/plugins.
func pluginRoot(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "data", "plugins")
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Skipf("the plugin directory does not exist: %s", p)
	}
	return p
}

func newTestHost(t *testing.T, dir string, timeout time.Duration) *Host {
	return NewHost(dir, pythonBin(t), timeout)
}

func manifestOf(t *testing.T, res *ListResult, name string) *Manifest {
	t.Helper()
	for i := range res.Tools {
		if res.Tools[i].Name == name {
			return &res.Tools[i]
		}
	}
	return nil
}

func TestListDiscoversWebSearch(t *testing.T) {
	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	res, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	ws := manifestOf(t, res, "web_search")
	if ws == nil {
		t.Fatalf("web_search is missing from the manifest: %+v", res.Tools)
	}
	if ws.Kind != "tools" {
		t.Errorf("kind = %q, want tools", ws.Kind)
	}
	if len(ws.Parameters) == 0 {
		t.Error("the tool surface has empty parameters (the model gets no argument schema)")
	}
	props, _ := ws.Parameters["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Error("the tool surface is missing the query parameter")
	}

	got := map[string]bool{}
	for _, im := range ws.Implementations {
		got[im.Name] = true
	}
	for _, want := range []string{"searxng", "bing_rss", "tavily"} {
		if !got[want] {
			t.Errorf("implementations are missing %s (got: %v)", want, got)
		}
	}

	// Only web_search's own scan errors matter (another plugin breaking must not fail this case)
	for _, e := range res.Errors {
		if strings.Contains(e.Path, "web_search") {
			t.Errorf("scanning web_search failed: %s: %s", e.Path, e.Error)
		}
	}
}

func TestInvokeSearXNGMapsResults(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("q")
		if f := r.URL.Query().Get("format"); f != "json" {
			t.Errorf("format = %q, want json", f)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "t1", "url": "https://e1", "content": "c1", "publishedDate": "2026-10-01"},
				{"title": "t2", "url": "https://e2", "content": "c2", "publishedDate": ""},
				{"title": "t3", "url": "https://e3", "content": "c3"},
				{"title": "t4", "url": "https://e4", "content": "c4"},
			},
		})
	}))
	defer srv.Close()

	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	raw, err := h.Invoke(context.Background(), "web_search", "searxng", &InvokePayload{
		Config: map[string]any{"endpoint": srv.URL},
		Args:   map[string]any{"query": "nexus 沙箱", "count": 2},
	})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if gotQuery != "nexus 沙箱" {
		t.Errorf("q = %q, want the query passed through", gotQuery)
	}

	res, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("the result is not an object: %T %v", raw, raw)
	}
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v, want searxng", res["provider"])
	}
	items, _ := res["results"].([]any)
	if len(items) != 2 {
		t.Fatalf("result count = %d, want it truncated to count=2", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "t1" || first["url"] != "https://e1" || first["snippet"] != "c1" {
		t.Errorf("the first entry is mapped wrongly: %v", first)
	}
	if first["published"] != "2026-10-01" {
		t.Errorf("published was not passed through: %v", first["published"])
	}
}

func TestInvokeBingRSSParsesXML(t *testing.T) {
	const rss = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0"><channel>
<item><title>nexus 沙箱设计</title><link>https://e1</link>
<description>&lt;b&gt;沙箱&lt;/b&gt; 与 &lt;em&gt;隔离&lt;/em&gt; 的设计说明</description>
<pubDate>Mon, 19 Oct 2026 08:00:00 GMT</pubDate></item>
<item><title>第二篇</title><link>https://e2</link><description>摘要二</description></item>
<item><title>第三篇</title><link>https://e3</link><description>摘要三</description></item>
</channel></rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("format"); f != "rss" {
			t.Errorf("format = %q, want rss", f)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rss))
	}))
	defer srv.Close()

	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	raw, err := h.Invoke(context.Background(), "web_search", "bing_rss", &InvokePayload{
		Config: map[string]any{"endpoint": srv.URL},
		Args:   map[string]any{"query": "nexus 沙箱", "count": 2},
	})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	res, _ := raw.(map[string]any)
	items, _ := res["results"].([]any)
	if len(items) != 2 {
		t.Fatalf("result count = %d, want it truncated to count=2", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "nexus 沙箱设计" || first["url"] != "https://e1" {
		t.Errorf("the entry is mapped wrongly: %v", first)
	}
	if snip, _ := first["snippet"].(string); strings.Contains(snip, "<") || !strings.Contains(snip, "沙箱") {
		t.Errorf("the snippet still carries HTML tags: %q", snip)
	}
	if first["published"] == nil {
		t.Error("pubDate was not mapped to published")
	}
}

func TestInvokeTavilyAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid api key"}`))
	}))
	defer srv.Close()

	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	_, err := h.Invoke(context.Background(), "web_search", "tavily", &InvokePayload{
		Config: map[string]any{"api_key": "bad", "endpoint": srv.URL},
		Args:   map[string]any{"query": "q"},
	})
	if err == nil {
		t.Fatal("want an authentication failure, got success")
	}
	if code := Code(err); code != CodeAuth {
		t.Fatalf("error code = %q, want auth (err=%v)", code, err)
	}
}

func TestInvokeMissingConfigIsConfigError(t *testing.T) {
	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	// tavily's api_key is required: without it the call must report config straight away (nexus does
	// not fall back on that)
	_, err := h.Invoke(context.Background(), "web_search", "tavily", &InvokePayload{
		Args: map[string]any{"query": "q"},
	})
	if err == nil {
		t.Fatal("want a missing-config failure, got success")
	}
	if code := Code(err); code != CodeConfig {
		t.Fatalf("error code = %q, want config (err=%v)", code, err)
	}
}

// A broken implementation only takes itself out: the other implementations of the same tool keep
// working, and other tools are unaffected.
func TestBrokenImplIsIsolated(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)

	badDir := filepath.Join(root, "tools", "web_search", "broken")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "plugin.py"), []byte("def oops(:\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestHost(t, root, 20*time.Second)
	res, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	ws := manifestOf(t, res, "web_search")
	if ws == nil {
		t.Fatal("a broken implementation took the whole tool down")
	}
	for _, im := range ws.Implementations {
		if im.Name == "broken" {
			t.Error("the broken implementation should not appear in the manifest")
		}
	}
	if len(ws.Implementations) < 3 {
		t.Errorf("good implementations were hit too: %+v", ws.Implementations)
	}

	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Path, "broken") && e.Error != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("no scan error was recorded for the broken implementation: %+v", res.Errors)
	}
}

// A timeout must kill the whole group and return promptly (otherwise one stuck plugin stalls a whole
// session).
func TestInvokeTimeout(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)

	slowDir := filepath.Join(root, "tools", "sleeper")
	if err := os.MkdirAll(filepath.Join(slowDir, "slow"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(slowDir, "tool.py"), `from flame_plugin import tool

tool(name="sleeper", description="test fixture: sleeps for a long time", parameters={"type": "object", "properties": {}})
`)
	writeFile(t, filepath.Join(slowDir, "slow", "plugin.py"), `import time
from flame_plugin import impl

@impl(name="slow", display_name="Slow")
def invoke(ctx, args):
    time.sleep(10)
    return {"results": []}
`)

	h := newTestHost(t, root, 700*time.Millisecond)
	start := time.Now()
	_, err := h.Invoke(context.Background(), "sleeper", "slow", &InvokePayload{Args: map[string]any{}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want a timeout failure, got success")
	}
	if code := Code(err); code != CodeNetwork {
		t.Fatalf("error code = %q, want network (err=%v)", code, err)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("the call did not return promptly after the timeout: took %s", elapsed)
	}
}

func TestInvokeUnknownToolIsConfigError(t *testing.T) {
	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	_, err := h.Invoke(context.Background(), "no_such_tool", "searxng", &InvokePayload{})
	if err == nil {
		t.Fatal("want a failure, got success")
	}
	if code := Code(err); code != CodeConfig {
		t.Fatalf("error code = %q, want config (err=%v)", code, err)
	}
	var ce *CallError
	if errors.As(err, &ce) && !strings.Contains(ce.Message, "no_such_tool") {
		t.Errorf("the message does not name the tool: %s", ce.Message)
	}
}

// ===== test helpers =====

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(p, "__pycache__") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("failed to copy the plugin directory: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
