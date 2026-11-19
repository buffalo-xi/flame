package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryDiscoverAndGet(t *testing.T) {
	r := NewRegistry(newTestHost(t, pluginRoot(t), 20*time.Second), []string{"read_file", "write_file"})
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh 失败: %v", err)
	}

	m, ok := r.Get("web_search")
	if !ok {
		t.Fatalf("没发现 web_search：%+v", r.Snapshot())
	}
	if len(m.Implementations) < 3 {
		t.Errorf("实现数 = %d，期望 >= 3", len(m.Implementations))
	}
	if !r.Has("WEB_SEARCH") {
		t.Error("Get 应当大小写不敏感")
	}
	if r.Has("no_such_tool") {
		t.Error("不存在的工具不该命中")
	}
	for _, e := range r.Snapshot().Errors {
		if strings.Contains(e.Path, "web_search") {
			t.Errorf("web_search 有扫描错误：%s: %s", e.Path, e.Error)
		}
	}
}

func TestRegistryRejectsBuiltinNameCollision(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	writeTool(t, root, "tools", "read_file", "read_file")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), []string{"read_file"})
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh 失败: %v", err)
	}
	if _, ok := r.Get("read_file"); ok {
		t.Fatal("插件工具不该与内置工具重名后仍然生效（分派会含糊）")
	}
	if !hasErrorMentioning(r.Snapshot().Errors, "clashes") {
		t.Errorf("没有记录重名错误：%+v", r.Snapshot().Errors)
	}
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	// 第二个目录声明同一个工具名：只保留先到的那个
	writeTool(t, root, "tools", "web_search_alias", "web_search")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh 失败: %v", err)
	}
	snap := r.Snapshot()
	count := 0
	for _, m := range snap.Tools {
		if m.Name == "web_search" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同名工具生效了 %d 份，期望 1 份", count)
	}
	if !hasErrorMentioning(snap.Errors, "duplicates") {
		t.Errorf("没有记录重名错误：%+v", snap.Errors)
	}
}

func TestRegistryRejectsUnsupportedKind(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	writeTool(t, root, "channels", "notify", "notify")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh 失败: %v", err)
	}
	if r.Has("notify") {
		t.Fatal("未知 kind 的插件不该被加载")
	}
	if !hasErrorMentioning(r.Snapshot().Errors, "unsupported") {
		t.Errorf("没有记录 kind 错误：%+v", r.Snapshot().Errors)
	}
}

// 目录没变时不该重启 python；force=true 必须真的重扫，且失败要保留上一次的结果。
func TestRegistrySkipsUnchangedDir(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)

	host := newTestHost(t, root, 10*time.Second)
	r := NewRegistry(host, nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("首次 refresh 失败: %v", err)
	}

	host.Python = "definitely-not-a-python-interpreter-xyz"
	if err := r.Refresh(context.Background(), false); err != nil {
		t.Fatalf("目录没变却被重扫了：%v", err)
	}
	if err := r.Refresh(context.Background(), true); err == nil {
		t.Fatal("force=true 应当真的重扫（这里解释器已不可用，应报错）")
	}

	if _, ok := r.Get("web_search"); !ok {
		t.Error("扫描失败不该清空上一次的成功结果")
	}
	if r.Snapshot().Err == nil {
		t.Error("扫描失败应记录到 Snapshot.Err")
	}
}

// ===== 测试工具 =====

// writeTool 在临时插件目录里造一个最小工具（tool.py + 一个实现）。
func writeTool(t *testing.T, root, kind, dirName, toolName string) {
	t.Helper()
	toolDir := filepath.Join(root, kind, dirName)
	if err := os.MkdirAll(filepath.Join(toolDir, "basic"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(toolDir, "tool.py"), fmt.Sprintf(`from flame_plugin import tool

tool(name=%q, description="测试用工具", parameters={"type": "object", "properties": {}})
`, toolName))
	writeFile(t, filepath.Join(toolDir, "basic", "plugin.py"), `from flame_plugin import impl

@impl(name="basic", display_name="Basic")
def invoke(ctx, args):
    return {"results": []}
`)
}

func hasErrorMentioning(errs []ScanError, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error, substr) {
			return true
		}
	}
	return false
}
