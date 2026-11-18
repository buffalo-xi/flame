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

// mkBuildDir 建一个快照目录 platform-assets/<assetID>/<build>/ 并放一个文件。
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

// ageDirTo 把目录及其内文件一起改到指定时间（目录与文件都改，覆盖两种判据）。
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

// TestCleanupSnapshotDirs 覆盖四条分支：
//   - 表里没有的资产目录 → 删（孤儿）
//   - 掉出保留窗口的旧快照 → 删
//   - 比当前 build 还新但刚写入 → 留（可能是别的实例正在发布）
//   - 比当前 build 还新且超过 snapshotPendingTTL → 删（真残留）
func TestCleanupSnapshotDirs(t *testing.T) {
	root := t.TempDir()
	st := storage.NewFilesystemStorage(root)

	// 资产 a1 当前 build=2，保留窗口 BuildsKept=1 → build 1 应被删。
	oldBuild := mkBuildDir(t, root, "a1", 1)
	current := mkBuildDir(t, root, "a1", 2)
	freshPending := mkBuildDir(t, root, "a1", 3)
	stalePending := mkBuildDir(t, root, "a1", 4)
	orphanAsset := mkBuildDir(t, root, "gone", 1)

	// 只有「比当前还新且放了很久」的那份算残留。
	ageDirTo(t, root, stalePending, time.Now().Add(-snapshotPendingTTL-time.Minute))

	orphans, builds := cleanupSnapshotDirs(context.Background(), st, map[string]int{"a1": 2})

	if orphans != 1 {
		t.Errorf("孤儿资产目录数 = %d，期望 1", orphans)
	}
	if builds != 2 {
		t.Errorf("清理的快照数 = %d，期望 2（旧窗口 1 个 + 过期残留 1 个）", builds)
	}
	for _, rel := range []string{oldBuild, stalePending, orphanAsset} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s 应被删除，实际 stat: %v", rel, err)
		}
	}
	for _, rel := range []string{current, freshPending} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s 应保留: %v", rel, err)
		}
	}
}
