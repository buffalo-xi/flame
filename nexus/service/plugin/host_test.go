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

// TestMain 给 xlog 一个临时目录：Registry.Refresh 会打日志，
// 而 xlog 未 Init 时 logger 是 nil（生产由 main 初始化）。
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

// 这些用例会真的起 python 子进程。缺解释器就跳过（与 SANDBOXD_TEST_* 同风格），
// 可用 PLUGIN_TEST_PYTHON 指定解释器（如 Windows 上的 python、Linux 上的 python3）。

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
	t.Skip("没有 python，跳过插件宿主测试（可用 PLUGIN_TEST_PYTHON 指定解释器）")
	return ""
}

// pluginRoot 仓库里的默认插件目录：nexus/data/plugins。
func pluginRoot(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "data", "plugins")
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Skipf("插件目录不存在：%s", p)
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
		t.Fatalf("list 失败: %v", err)
	}

	ws := manifestOf(t, res, "web_search")
	if ws == nil {
		t.Fatalf("清单里没有 web_search：%+v", res.Tools)
	}
	if ws.Kind != "tools" {
		t.Errorf("kind = %q，期望 tools", ws.Kind)
	}
	if len(ws.Parameters) == 0 {
		t.Error("工具面 parameters 为空（模型拿不到入参 schema）")
	}
	props, _ := ws.Parameters["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Error("工具面缺少 query 参数")
	}

	got := map[string]bool{}
	for _, im := range ws.Implementations {
		got[im.Name] = true
	}
	for _, want := range []string{"searxng", "bing_rss", "tavily"} {
		if !got[want] {
			t.Errorf("缺少实现 %s（实际：%v）", want, got)
		}
	}

	// 只关心 web_search 自己的扫描错误（其它插件坏掉不该让本用例失败）
	for _, e := range res.Errors {
		if strings.Contains(e.Path, "web_search") {
			t.Errorf("扫描 web_search 出错：%s: %s", e.Path, e.Error)
		}
	}
}

func TestInvokeSearXNGMapsResults(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("路径 = %q，期望 /search", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("q")
		if f := r.URL.Query().Get("format"); f != "json" {
			t.Errorf("format = %q，期望 json", f)
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
		t.Fatalf("invoke 失败: %v", err)
	}
	if gotQuery != "nexus 沙箱" {
		t.Errorf("q = %q，期望查询词被透传", gotQuery)
	}

	res, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("结果不是对象：%T %v", raw, raw)
	}
	if res["provider"] != "searxng" {
		t.Errorf("provider = %v，期望 searxng", res["provider"])
	}
	items, _ := res["results"].([]any)
	if len(items) != 2 {
		t.Fatalf("结果条数 = %d，期望按 count=2 截断", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "t1" || first["url"] != "https://e1" || first["snippet"] != "c1" {
		t.Errorf("第一条映射不对：%v", first)
	}
	if first["published"] != "2026-10-01" {
		t.Errorf("published 未透传：%v", first["published"])
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
			t.Errorf("format = %q，期望 rss", f)
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
		t.Fatalf("invoke 失败: %v", err)
	}
	res, _ := raw.(map[string]any)
	items, _ := res["results"].([]any)
	if len(items) != 2 {
		t.Fatalf("结果条数 = %d，期望按 count=2 截断", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "nexus 沙箱设计" || first["url"] != "https://e1" {
		t.Errorf("条目映射不对：%v", first)
	}
	if snip, _ := first["snippet"].(string); strings.Contains(snip, "<") || !strings.Contains(snip, "沙箱") {
		t.Errorf("snippet 没有剥掉 HTML 标签：%q", snip)
	}
	if first["published"] == nil {
		t.Error("pubDate 未映射到 published")
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
		t.Fatal("期望鉴权失败，实际成功")
	}
	if code := Code(err); code != CodeAuth {
		t.Fatalf("错误码 = %q，期望 auth（err=%v）", code, err)
	}
}

func TestInvokeMissingConfigIsConfigError(t *testing.T) {
	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	// tavily 的 api_key 是 required：没配时应当直接报 config（nexus 据此不降级）
	_, err := h.Invoke(context.Background(), "web_search", "tavily", &InvokePayload{
		Args: map[string]any{"query": "q"},
	})
	if err == nil {
		t.Fatal("期望缺配置报错，实际成功")
	}
	if code := Code(err); code != CodeConfig {
		t.Fatalf("错误码 = %q，期望 config（err=%v）", code, err)
	}
}

// 坏实现只让它自己缺席：同一工具下别的实现照常可用，其它工具也不受影响。
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
		t.Fatalf("list 失败: %v", err)
	}

	ws := manifestOf(t, res, "web_search")
	if ws == nil {
		t.Fatal("坏实现把整个工具拖没了")
	}
	for _, im := range ws.Implementations {
		if im.Name == "broken" {
			t.Error("坏实现不该出现在清单里")
		}
	}
	if len(ws.Implementations) < 3 {
		t.Errorf("好实现被误伤：%+v", ws.Implementations)
	}

	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Path, "broken") && e.Error != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("没有记录坏实现的扫描错误：%+v", res.Errors)
	}
}

// 超时必须整组杀掉并及时返回（否则一轮会话会被一个卡死的插件拖住）。
func TestInvokeTimeout(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)

	slowDir := filepath.Join(root, "tools", "sleeper")
	if err := os.MkdirAll(filepath.Join(slowDir, "slow"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(slowDir, "tool.py"), `from flame_plugin import tool

tool(name="sleeper", description="测试用：睡很久", parameters={"type": "object", "properties": {}})
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
		t.Fatal("期望超时报错，实际成功")
	}
	if code := Code(err); code != CodeNetwork {
		t.Fatalf("错误码 = %q，期望 network（err=%v）", code, err)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("超时后没有及时返回：耗时 %s", elapsed)
	}
}

func TestInvokeUnknownToolIsConfigError(t *testing.T) {
	h := newTestHost(t, pluginRoot(t), 20*time.Second)
	_, err := h.Invoke(context.Background(), "no_such_tool", "searxng", &InvokePayload{})
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}
	if code := Code(err); code != CodeConfig {
		t.Fatalf("错误码 = %q，期望 config（err=%v）", code, err)
	}
	var ce *CallError
	if errors.As(err, &ce) && !strings.Contains(ce.Message, "no_such_tool") {
		t.Errorf("错误信息没带工具名：%s", ce.Message)
	}
}

// ===== 测试工具 =====

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
		t.Fatalf("复制插件目录失败: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", path, err)
	}
}
