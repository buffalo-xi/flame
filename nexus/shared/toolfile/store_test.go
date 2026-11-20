package toolfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

func fixture() *models.CustomTool {
	return &models.CustomTool{
		Name:        "demo",
		DisplayName: "示例",
		Description: "示例工具",
		Parameters:  xsql.JSONB{Data: map[string]any{"type": "object", "properties": map[string]any{}}},
		Lang:        "python",
		Entry:       "main.py",
		Status:      models.StatusNormal,
	}
}

// Write -> Read must be lossless; the path lands on tenants/{schema}/tools/{name}/TOOL.json.
func TestWriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := WriteOf(root, "t0", fixture()); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat(Path(root, "t0", "demo")); err != nil {
		t.Fatalf("the contract file was not written to disk: %v", err)
	}
	if !strings.HasSuffix(Path(root, "t0", "demo"), models.ToolFileName) {
		t.Fatalf("wrong file name: %s", Path(root, "t0", "demo"))
	}

	f, err := Read(root, "t0", "demo")
	if err != nil || f == nil {
		t.Fatalf("read failed: %v %v", f, err)
	}
	if f.Name != "demo" || f.Description != "示例工具" {
		t.Fatalf("content mismatch: %+v", f)
	}
}

// A missing file is not an error: during the rollout most rows do not have a TOOL.json yet.
func TestReadMissingIsNotError(t *testing.T) {
	f, err := Read(t.TempDir(), "t0", "absent")
	if err != nil || f != nil {
		t.Fatalf("a missing file should return (nil, nil), got %v %v", f, err)
	}
}

// ReadAt and WriteAt are symmetric: a production run reads the contract from the version directory, so it must use the
// same rules as when the version was written.
func TestReadAtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAt(dir, &models.ToolFile{Description: "v1", Lang: "python", Entry: "main.py"}); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	f, err := ReadAt(dir)
	if err != nil || f == nil {
		t.Fatalf("ReadAt = %v, %v", f, err)
	}
	if f.Description != "v1" || f.Entry != "main.py" {
		t.Fatalf("content mismatch: %+v", f)
	}
	if miss, err := ReadAt(t.TempDir()); err != nil || miss != nil {
		t.Fatalf("a missing file should return (nil, nil), got %v %v", miss, err)
	}
}

// A broken file -> fall back to the table definition plus a warning, and the tool stays usable (a broken description must
// not make the tool disappear).
func TestLoadFallsBackOnBrokenFile(t *testing.T) {
	root := t.TempDir()
	tool := fixture()
	dir := Dir(root, "t0", tool.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, models.ToolFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Load(root, "t0", tool)
	if c.Source != "table" {
		t.Fatalf("should fall back to the table definition, got %s", c.Source)
	}
	if len(c.Warnings) == 0 {
		t.Fatal("a fallback must carry a warning, or nobody would know the file is broken")
	}
	if tool.Description != "示例工具" {
		t.Errorf("a fallback must not modify the table row: %+v", tool)
	}
}

// The file wins: the on-disk contract overrides the stale value in the table row.
func TestLoadFileWinsOverTable(t *testing.T) {
	root := t.TempDir()
	tool := fixture()
	if err := Write(root, "t0", tool.Name, &models.ToolFile{
		Description: "文件里的新描述",
		Lang:        "python",
		Entry:       "main.py",
	}); err != nil {
		t.Fatal(err)
	}

	c := Load(root, "t0", tool)
	if c.Source != "file" {
		t.Fatalf("should use the file contract, got %s (warnings=%v)", c.Source, c.Warnings)
	}
	if tool.Description != "文件里的新描述" {
		t.Errorf("the file did not override the table row: %q", tool.Description)
	}
}

// A name that disagrees with the directory -> fall back to the table plus a warning (never accept it silently).
func TestLoadRejectsNameMismatch(t *testing.T) {
	root := t.TempDir()
	tool := fixture()
	if err := Write(root, "t0", tool.Name, &models.ToolFile{
		Name:        "someone-else",
		Description: "x",
		Lang:        "python",
		Entry:       "main.py",
	}); err != nil {
		t.Fatal(err)
	}

	c := Load(root, "t0", tool)
	if c.Source != "table" || len(c.Warnings) == 0 {
		t.Fatalf("a name mismatch should fall back and warn: %+v", c)
	}
	if tool.Description != "示例工具" {
		t.Errorf("a fallback must not adopt the file's content: %q", tool.Description)
	}
}

// A missing entry file only warns, never blocks: execution reports a more accurate error (and a trial run shows it).
func TestLoadWarnsMissingEntry(t *testing.T) {
	root := t.TempDir()
	tool := fixture()
	if err := Write(root, "t0", tool.Name, &models.ToolFile{
		Description: "d",
		Lang:        "js",
		Entry:       "main.js",
	}); err != nil {
		t.Fatal(err)
	}

	c := Load(root, "t0", tool)
	if c.Source != "file" {
		t.Fatalf("should use the file contract: %+v", c)
	}
	if len(c.Warnings) == 0 || !strings.Contains(c.Warnings[0], "main.js") {
		t.Fatalf("a missing entry should be reported: %+v", c.Warnings)
	}
}

// The migration is idempotent: the first pass fills in, the second writes nothing.
func TestEnsureAllIsIdempotent(t *testing.T) {
	root := t.TempDir()
	list := []models.CustomTool{*fixture()}

	written, errs := EnsureAll(root, "t0", list)
	if written != 1 || len(errs) != 0 {
		t.Fatalf("the first pass should write one file, got written=%d errs=%v", written, errs)
	}
	written, errs = EnsureAll(root, "t0", list)
	if written != 0 || len(errs) != 0 {
		t.Fatalf("the second pass should write nothing, got written=%d errs=%v", written, errs)
	}
}

// A name may only be a single directory level: this keeps Path from escaping tools/ or overwriting the tenant root.
func TestSafeName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "..", ".", "a/b", `a\b`} {
		if err := WriteOf(root, "t0", &models.CustomTool{Name: name}); err == nil {
			t.Errorf("name %q should be refused", name)
		}
	}
}
