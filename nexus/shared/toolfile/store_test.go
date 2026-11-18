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

// Write → Read 必须一致；路径落在 tenants/{schema}/tools/{name}/TOOL.json。
func TestWriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := WriteOf(root, "t0", fixture()); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if _, err := os.Stat(Path(root, "t0", "demo")); err != nil {
		t.Fatalf("契约文件没落盘: %v", err)
	}
	if !strings.HasSuffix(Path(root, "t0", "demo"), models.ToolFileName) {
		t.Fatalf("文件名不对: %s", Path(root, "t0", "demo"))
	}

	f, err := Read(root, "t0", "demo")
	if err != nil || f == nil {
		t.Fatalf("读失败: %v %v", f, err)
	}
	if f.Name != "demo" || f.Description != "示例工具" {
		t.Fatalf("内容不对: %+v", f)
	}
}

// 缺文件不是错误：灰度期大多数行还没有 TOOL.json。
func TestReadMissingIsNotError(t *testing.T) {
	f, err := Read(t.TempDir(), "t0", "absent")
	if err != nil || f != nil {
		t.Fatalf("缺文件应返回 (nil,nil)，实际 %v %v", f, err)
	}
}

// ReadAt 与 WriteAt 对称：正式运行从版本目录读契约，口径必须和写版本时一致。
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
		t.Fatalf("内容不对: %+v", f)
	}
	if miss, err := ReadAt(t.TempDir()); err != nil || miss != nil {
		t.Fatalf("缺文件应返回 (nil,nil)，实际 %v %v", miss, err)
	}
}

// 文件坏了 → 回退表定义 + warning，工具仍然可用（不能因为描述坏了让工具消失）。
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
		t.Fatalf("应回退表定义，实际 %s", c.Source)
	}
	if len(c.Warnings) == 0 {
		t.Fatal("回退时必须有 warning，否则没人知道文件坏了")
	}
	if tool.Description != "示例工具" {
		t.Errorf("回退后不该改动表行: %+v", tool)
	}
}

// 文件优先：盘上的契约覆盖表行里的旧值。
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
		t.Fatalf("应采用文件契约，实际 %s（warnings=%v）", c.Source, c.Warnings)
	}
	if tool.Description != "文件里的新描述" {
		t.Errorf("文件没覆盖表行: %q", tool.Description)
	}
}

// name 与目录名不一致 → 回退表 + warning（不静默接受）。
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
		t.Fatalf("名字不一致应回退并告警: %+v", c)
	}
	if tool.Description != "示例工具" {
		t.Errorf("回退后不该采用文件内容: %q", tool.Description)
	}
}

// 入口文件缺失只告警，不拦截：执行时会给出更准确的错误（试跑能看到）。
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
		t.Fatalf("应采用文件契约: %+v", c)
	}
	if len(c.Warnings) == 0 || !strings.Contains(c.Warnings[0], "main.js") {
		t.Fatalf("应提示入口缺失: %+v", c.Warnings)
	}
}

// 迁移幂等：第一次补齐，第二次不再写。
func TestEnsureAllIsIdempotent(t *testing.T) {
	root := t.TempDir()
	list := []models.CustomTool{*fixture()}

	written, errs := EnsureAll(root, "t0", list)
	if written != 1 || len(errs) != 0 {
		t.Fatalf("首次应补写 1 份，实际 written=%d errs=%v", written, errs)
	}
	written, errs = EnsureAll(root, "t0", list)
	if written != 0 || len(errs) != 0 {
		t.Fatalf("第二次不该再写，实际 written=%d errs=%v", written, errs)
	}
}

// 名字只能是一级目录名：防止 Path 跳出 tools/ 或覆盖租户根目录。
func TestSafeName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "..", ".", "a/b", `a\b`} {
		if err := WriteOf(root, "t0", &models.CustomTool{Name: name}); err == nil {
			t.Errorf("名字 %q 应被拒绝", name)
		}
	}
}
