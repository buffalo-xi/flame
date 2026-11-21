package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveCodehostBinary pins down the normalisation contract for the codehost binary path.
// The core point is "an in-container absolute path must be passed through verbatim": filepath.Abs is platform
// dependent and on Windows would turn /usr/local/bin/codehost into d:\usr\..., making docker / nsjail exec fail.
func TestResolveCodehostBinary(t *testing.T) {
	t.Run("container absolute path passthrough", func(t *testing.T) {
		got, err := resolveCodehostBinary("/usr/local/bin/codehost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/usr/local/bin/codehost" {
			t.Fatalf("got %q, want /usr/local/bin/codehost (must not go through filepath.Abs)", got)
		}
	})

	t.Run("bare command name", func(t *testing.T) {
		got, err := resolveCodehostBinary("codehost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "codehost" {
			t.Fatalf("got %q, want codehost", got)
		}
	})

	t.Run("host relative path becomes absolute", func(t *testing.T) {
		rel := filepath.Join("bin", "codehost.exe")
		got, err := resolveCodehostBinary(rel)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("got %q, want absolute path", got)
		}
	})
}

// prepareTmpRoot must never delete an entry it did not create. The old "empty the whole tree" behaviour made a wrong
// tmp_dir destructive; validatePaths is the first line of defence, and this is the second — it does not rely on it.
func TestPrepareTmpRootKeepsForeignEntries(t *testing.T) {
	root := t.TempDir()
	workerDir := filepath.Join(root, "codehost-1234")
	foreignFile := filepath.Join(root, "notes.txt")
	if err := os.MkdirAll(filepath.Join(workerDir, "sub"), 0o755); err != nil {
		t.Fatalf("create worker dir: %v", err)
	}
	if err := os.WriteFile(foreignFile, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	if err := prepareTmpRoot(root); err != nil {
		t.Fatalf("prepareTmpRoot: %v", err)
	}
	if _, err := os.Stat(workerDir); !os.IsNotExist(err) {
		t.Errorf("worker dir %s should have been removed, stat err = %v", workerDir, err)
	}
	if _, err := os.Stat(foreignFile); err != nil {
		t.Errorf("foreign entry %s must be left alone: %v", foreignFile, err)
	}
}

// A missing root is created (first start); a root that is a regular file is a configuration error, and saying so beats
// an MkdirAll "not a directory".
func TestPrepareTmpRootCreatesMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "run", "tmp")
	if err := prepareTmpRoot(root); err != nil {
		t.Fatalf("prepareTmpRoot: %v", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("tmp root was not created: %v", err)
	}
}

func TestPrepareTmpRootRejectsRegularFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, nil, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := prepareTmpRoot(root); err == nil {
		t.Fatal("prepareTmpRoot on a regular file must fail")
	}
}
