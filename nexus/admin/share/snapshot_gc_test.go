package share

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// mkBuildDir creates a snapshot directory platform-assets/<assetID>/<build>/ with one file in it.
func mkBuildDir(t *testing.T, root, assetID string, build int) string {
	t.Helper()
	rel := paths.PlatformAssetKey(assetID, build)
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(filepath.Join(abs, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s/f.txt: %v", rel, err)
	}
	return rel
}

// ageDirTo moves a directory and the files inside it to the given time (both, to cover either criterion).
func ageDirTo(t *testing.T, root, rel string, at time.Time) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Chtimes(filepath.Join(abs, "f.txt"), at, at); err != nil {
		t.Fatalf("chtimes file %s: %v", rel, err)
	}
	if err := os.Chtimes(abs, at, at); err != nil {
		t.Fatalf("chtimes dir %s: %v", rel, err)
	}
}

// TestCleanupSnapshotDirs covers four branches:
//   - a directory whose asset is not in the table -> delete (orphan)
//   - an old snapshot outside the retention window -> delete
//   - newer than the current build but just written -> keep (another instance may be releasing)
//   - newer than the current build and older than snapshotPendingTTL -> delete (a real leftover)
func TestCleanupSnapshotDirs(t *testing.T) {
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)

	// Asset a1 is at build=2 with BuildsKept=1, so build 1 must be deleted.
	oldBuild := mkBuildDir(t, root, "a1", 1)
	current := mkBuildDir(t, root, "a1", 2)
	freshPending := mkBuildDir(t, root, "a1", 3)
	stalePending := mkBuildDir(t, root, "a1", 4)
	orphanAsset := mkBuildDir(t, root, "gone", 1)

	// Only the copy that is newer than the current build and has been sitting there counts as a leftover.
	ageDirTo(t, root, stalePending, time.Now().Add(-snapshotPendingTTL-time.Minute))

	orphans, builds := cleanupSnapshotDirs(context.Background(), st, map[string]int{"a1": 2})

	if orphans != 1 {
		t.Errorf("orphan asset directories = %d, want 1", orphans)
	}
	if builds != 2 {
		t.Errorf("cleaned snapshots = %d, want 2 (1 outside the window + 1 expired leftover)", builds)
	}
	for _, rel := range []string{oldBuild, stalePending, orphanAsset} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted, stat: %v", rel, err)
		}
	}
	for _, rel := range []string{current, freshPending} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s should have been kept: %v", rel, err)
		}
	}
}
