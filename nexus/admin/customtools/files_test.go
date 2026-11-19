package customtools

import (
	"os"
	"path/filepath"
	"testing"
)

// Traversal must be stopped at the path-building step: the file API takes path from the URL, and one mistake means arbitrary file reads and writes.
func TestJoinToolFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../x", "../../etc/passwd", "a/../../b", "/etc/passwd"} {
		if _, _, err := joinToolFile(root, bad); err == nil {
			t.Errorf("%q should have been rejected but passed", bad)
		}
	}
}

func TestJoinToolFileNormal(t *testing.T) {
	root := t.TempDir()

	abs, clean, err := joinToolFile(root, "")
	if err != nil || clean != "" || abs != root {
		t.Fatalf("an empty path should land on the root: abs=%q clean=%q err=%v", abs, clean, err)
	}

	abs, clean, err = joinToolFile(root, "lib/x.py")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "lib/x.py" {
		t.Errorf("clean = %q, want lib/x.py", clean)
	}
	if abs != filepath.Join(root, "lib", "x.py") {
		t.Errorf("abs = %q, want lib/x.py under the root", abs)
	}
}

// The contract file is the only path that cannot be written or deleted, and only the one at the directory root counts.
func TestIsToolContract(t *testing.T) {
	cases := map[string]bool{
		"TOOL.json":         true,
		"tool.json":         true, // file names are case-insensitive on Windows, so both spellings count
		"lib/TOOL.json":     false,
		"TOOL.json.bak":     false,
		"sub/TOOL.json/bak": false,
	}
	for in, want := range cases {
		if got := isToolContract(in); got != want {
			t.Errorf("isToolContract(%q) = %v, want %v", in, got, want)
		}
	}
}

// The entry file may be edited but not deleted, and only when it matches the entry declared in TOOL.json exactly.
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
			t.Errorf("isToolEntry(%q, %q) = %v, want %v", c.rel, c.entry, got, c.want)
		}
	}
}

// An upload keeps only the basename: a browser may send the whole path.
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
			t.Errorf("sanitizeToolFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Never overwrite: save as name-2.ext instead of silently replacing a file the tool already has.
func TestUniqueToolRel(t *testing.T) {
	dir := t.TempDir()
	if got := uniqueToolRel(dir, "a.txt"); got != "a.txt" {
		t.Fatalf("an empty directory should keep the original name, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueToolRel(dir, "a.txt"); got != "a-2.txt" {
		t.Errorf("a name clash should save as a-2.txt, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-2.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueToolRel(dir, "a.txt"); got != "a-3.txt" {
		t.Errorf("the third should save as a-3.txt, got %q", got)
	}
}
