package customtools

import (
	"os"
	"path/filepath"
	"testing"
)

// 穿越必须被挡在拼路径这一步：文件接口的 path 来自 URL，拼错一次就是任意文件读写。
func TestJoinToolFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../x", "../../etc/passwd", "a/../../b", "/etc/passwd"} {
		if _, _, err := joinToolFile(root, bad); err == nil {
			t.Errorf("%q 应被拒绝，实际通过", bad)
		}
	}
}

func TestJoinToolFileNormal(t *testing.T) {
	root := t.TempDir()

	abs, clean, err := joinToolFile(root, "")
	if err != nil || clean != "" || abs != root {
		t.Fatalf("空路径应落到根：abs=%q clean=%q err=%v", abs, clean, err)
	}

	abs, clean, err = joinToolFile(root, "lib/x.py")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "lib/x.py" {
		t.Errorf("clean = %q，期望 lib/x.py", clean)
	}
	if abs != filepath.Join(root, "lib", "x.py") {
		t.Errorf("abs = %q，期望落在根下的 lib/x.py", abs)
	}
}

// 契约文件是唯一不可写、不可删的路径；判断必须只认「目录根部」的那一个。
func TestIsToolContract(t *testing.T) {
	cases := map[string]bool{
		"TOOL.json":         true,
		"tool.json":         true, // Windows 上大小写不敏感，两种都要认
		"lib/TOOL.json":     false,
		"TOOL.json.bak":     false,
		"sub/TOOL.json/bak": false,
	}
	for in, want := range cases {
		if got := isToolContract(in); got != want {
			t.Errorf("isToolContract(%q) = %v，期望 %v", in, got, want)
		}
	}
}

// 入口文件：可改不可删。只有与 TOOL.json 声明的 entry 完全一致才算。
func TestIsToolEntry(t *testing.T) {
	cases := []struct {
		rel, entry string
		want       bool
	}{
		{"main.py", "main.py", true},
		{"scripts/main.py", "scripts/main.py", true},
		{"lib/main.py", "main.py", false},
		{"main.py", "", false},
		{"", "main.py", false},
		{"main.py", "main.pyc", false},
	}
	for _, c := range cases {
		if got := isToolEntry(c.rel, c.entry); got != c.want {
			t.Errorf("isToolEntry(%q, %q) = %v，期望 %v", c.rel, c.entry, got, c.want)
		}
	}
}

// 上传文件名只保留 basename：浏览器可能带上整条路径。
func TestSanitizeToolFileName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":  "passwd",
		`C:\tmp\a\data.csv`: "data.csv",
		"rules.py":          "rules.py",
		"":                  "",
		"..":                "",
	}
	for in, want := range cases {
		if got := sanitizeToolFileName(in); got != want {
			t.Errorf("sanitizeToolFileName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 同名不覆盖：另存为 name-2.ext，绝不悄悄盖掉工具里已有的文件。
func TestUniqueToolRel(t *testing.T) {
	dir := t.TempDir()
	if got := uniqueToolRel(dir, "a.txt"); got != "a.txt" {
		t.Fatalf("空目录应原名落盘，实际 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueToolRel(dir, "a.txt"); got != "a-2.txt" {
		t.Errorf("重名应另存 a-2.txt，实际 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-2.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueToolRel(dir, "a.txt"); got != "a-3.txt" {
		t.Errorf("第三个应另存 a-3.txt，实际 %q", got)
	}
}
