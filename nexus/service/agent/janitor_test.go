package agent

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"flame/nexus/shared/storage"
)

// withTempStorage points the package-level agentStorage at a local temporary directory for the test.
func withTempStorage(t *testing.T) (root string, restore func()) {
	t.Helper()
	root = t.TempDir()
	old := agentStorage
	agentStorage = storage.NewFilesystemStorage(root)
	return root, func() { agentStorage = old }
}

// mkRunDir creates a run workspace under root and writes one artifact file into it.
func mkRunDir(t *testing.T, root, relDir string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(relDir))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relDir, err)
	}
	if err := os.WriteFile(filepath.Join(abs, "out.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s/out.txt: %v", relDir, err)
	}
}

// ageDir moves a directory and the files inside it to before the given time.
func ageDir(t *testing.T, root, relDir string, at time.Time) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(relDir))
	if err := os.Chtimes(filepath.Join(abs, "out.txt"), at, at); err != nil {
		t.Fatalf("chtimes file %s: %v", relDir, err)
	}
	if err := os.Chtimes(abs, at, at); err != nil {
		t.Fatalf("chtimes dir %s: %v", relDir, err)
	}
}

// TestCleanupExpiredRuns is the core regression of the janitor: only workspaces older than runsTTL are deleted,
// and an active workspace (possibly being used by another instance) must be left untouched.
func TestCleanupExpiredRuns(t *testing.T) {
	root, restore := withTempStorage(t)
	defer restore()

	runsDir := path.Join("tenants", "t1", "runs")
	oldRun := path.Join(runsDir, "old-run")
	newRun := path.Join(runsDir, "new-run")
	mkRunDir(t, root, oldRun)
	mkRunDir(t, root, newRun)

	// Move old-run entirely beyond the TTL (both the directory and the file inside it, covering both time criteria).
	ageDir(t, root, oldRun, time.Now().Add(-runsTTL-time.Minute))

	cleanupExpiredRuns(context.Background(), runsDir)

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(oldRun))); !os.IsNotExist(err) {
		t.Errorf("a workspace older than %s should be deleted, stat result: %v", runsTTL, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(newRun))); err != nil {
		t.Errorf("an active workspace must not be deleted: %v", err)
	}
}

// TestCleanupExpiredRunsEmptyDir also has to reclaim an empty workspace: with no child file to consult,
// it falls back to the directory's own time (local disk / NFS have that attribute).
func TestCleanupExpiredRunsEmptyDir(t *testing.T) {
	root, restore := withTempStorage(t)
	defer restore()

	runsDir := path.Join("tenants", "t1", "runs")
	abs := filepath.Join(root, filepath.FromSlash(path.Join(runsDir, "empty-run")))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-runsTTL - time.Minute)
	if err := os.Chtimes(abs, past, past); err != nil {
		t.Fatal(err)
	}

	cleanupExpiredRuns(context.Background(), runsDir)
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("an empty but expired workspace should be deleted, got: %v", err)
	}
}

// TestCleanupExpiredRunsMissingDir returns silently when the directory does not exist (the normal shape of a fresh deployment).
func TestCleanupExpiredRunsMissingDir(t *testing.T) {
	_, restore := withTempStorage(t)
	defer restore()

	cleanupExpiredRuns(context.Background(), path.Join("tenants", "t1", "runs"))
}
