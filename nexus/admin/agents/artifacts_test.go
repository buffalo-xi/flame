package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flame/nexus/shared/storage"
)

// Three boundaries of scanArtifacts must be pinned down:
//   - only files with mod_time >= since are reported (older files are not this round's output);
//   - dependency directories are skipped entirely, hidden files are skipped;
//   - only the root and one level below are scanned, deeper directories are not (the same narrowing as "recent files").
func TestScanArtifacts(t *testing.T) {
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)

	ws := filepath.Join(root, "ws")
	old := time.Now().Add(-24 * time.Hour)

	write := func(rel string, mod time.Time) {
		t.Helper()
		full := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(full, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
	}

	now := time.Now()
	write("report.md", now)                 // produced
	write("uploads/a.py", now)              // produced (one level down)
	write("old.txt", old)                   // older than since, not counted
	write(".hidden", now)                   // hidden, not counted
	write("node_modules/pkg/index.js", now) // dependency directory, skipped entirely
	write("deep/x/y/z.txt", now)            // three levels deep, beyond the scan depth

	got := scanArtifacts(context.Background(), st, "ws", now.Add(-time.Minute), 50)
	paths := make([]string, 0, len(got))
	for _, a := range got {
		paths = append(paths, a.Path)
	}

	want := map[string]bool{"report.md": true, "uploads/a.py": true}
	if len(paths) != len(want) {
		t.Fatalf("artifacts = %v, want exactly %v", paths, want)
	}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("unexpected non-artifact entry: %s (all: %v)", p, paths)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missed artifact: %s", p)
	}
}

// The cap must hold: however many artifacts exist, only limit are returned, so one conversation cannot drag out the whole tree.
func TestScanArtifactsLimit(t *testing.T) {
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		name := filepath.Join(ws, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := scanArtifacts(context.Background(), st, "ws", time.Now().Add(-time.Minute), 3)
	if len(got) != 3 {
		t.Fatalf("the limit did not apply: got %d items", len(got))
	}
}
