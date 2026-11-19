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
		t.Fatalf("refresh failed: %v", err)
	}

	m, ok := r.Get("web_search")
	if !ok {
		t.Fatalf("web_search was not discovered: %+v", r.Snapshot())
	}
	if len(m.Implementations) < 3 {
		t.Errorf("implementation count = %d, want >= 3", len(m.Implementations))
	}
	if !r.Has("WEB_SEARCH") {
		t.Error("Get should be case-insensitive")
	}
	if r.Has("no_such_tool") {
		t.Error("a tool that does not exist must not match")
	}
	for _, e := range r.Snapshot().Errors {
		if strings.Contains(e.Path, "web_search") {
			t.Errorf("web_search has a scan error: %s: %s", e.Path, e.Error)
		}
	}
}

func TestRegistryRejectsBuiltinNameCollision(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	writeTool(t, root, "tools", "read_file", "read_file")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), []string{"read_file"})
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if _, ok := r.Get("read_file"); ok {
		t.Fatal("a plugin tool must not take effect while it clashes with a built-in tool (the dispatch would be ambiguous)")
	}
	if !hasErrorMentioning(r.Snapshot().Errors, "clashes") {
		t.Errorf("the name clash was not recorded: %+v", r.Snapshot().Errors)
	}
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	// A second directory declares the same tool name: only the first one is kept
	writeTool(t, root, "tools", "web_search_alias", "web_search")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	snap := r.Snapshot()
	count := 0
	for _, m := range snap.Tools {
		if m.Name == "web_search" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d copies of the same tool took effect, want 1", count)
	}
	if !hasErrorMentioning(snap.Errors, "duplicates") {
		t.Errorf("the duplicate name was not recorded: %+v", snap.Errors)
	}
}

func TestRegistryRejectsUnsupportedKind(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)
	writeTool(t, root, "channels", "notify", "notify")

	r := NewRegistry(newTestHost(t, root, 20*time.Second), nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if r.Has("notify") {
		t.Fatal("a plugin of an unknown kind must not be loaded")
	}
	if !hasErrorMentioning(r.Snapshot().Errors, "unsupported") {
		t.Errorf("the kind error was not recorded: %+v", r.Snapshot().Errors)
	}
}

// An unchanged directory must not restart python; force=true has to rescan for real, and a failure has
// to keep the previous result.
func TestRegistrySkipsUnchangedDir(t *testing.T) {
	root := t.TempDir()
	copyTree(t, pluginRoot(t), root)

	host := newTestHost(t, root, 10*time.Second)
	r := NewRegistry(host, nil)
	if err := r.Refresh(context.Background(), true); err != nil {
		t.Fatalf("the first refresh failed: %v", err)
	}

	host.Python = "definitely-not-a-python-interpreter-xyz"
	if err := r.Refresh(context.Background(), false); err != nil {
		t.Fatalf("an unchanged directory was rescanned anyway: %v", err)
	}
	if err := r.Refresh(context.Background(), true); err == nil {
		t.Fatal("force=true should really rescan (the interpreter is unusable here, so it must fail)")
	}

	if _, ok := r.Get("web_search"); !ok {
		t.Error("a failed scan must not clear the last success")
	}
	if r.Snapshot().Err == nil {
		t.Error("a failed scan should be recorded in Snapshot.Err")
	}
}

// ===== test helpers =====

// writeTool builds a minimal tool (tool.py plus one implementation) in a temporary plugin directory.
func writeTool(t *testing.T, root, kind, dirName, toolName string) {
	t.Helper()
	toolDir := filepath.Join(root, kind, dirName)
	if err := os.MkdirAll(filepath.Join(toolDir, "basic"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(toolDir, "tool.py"), fmt.Sprintf(`from flame_plugin import tool

tool(name=%q, description="test fixture tool", parameters={"type": "object", "properties": {}})
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
