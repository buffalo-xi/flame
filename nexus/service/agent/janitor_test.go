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

// withTempStorage 把包级 agentStorage 临时指向一个本地临时目录。
func withTempStorage(t *testing.T) (root string, restore func()) {
	t.Helper()
	root = t.TempDir()
	old := agentStorage
	agentStorage = storage.NewFilesystemStorage(root)
	return root, func() { agentStorage = old }
}

// mkRunDir 在 root 下建一个 run 场地并写一个产物文件。
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

// ageDir 把一个目录及其内的文件一起改到指定时间之前。
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

// TestCleanupExpiredRuns 是 janitor 的核心回归：只删超过 runsTTL 的场地，
// 活跃场地（可能是别的实例正在跑的）必须原样保留。
func TestCleanupExpiredRuns(t *testing.T) {
	root, restore := withTempStorage(t)
	defer restore()

	runsDir := path.Join("tenants", "t1", "runs")
	oldRun := path.Join(runsDir, "old-run")
	newRun := path.Join(runsDir, "new-run")
	mkRunDir(t, root, oldRun)
	mkRunDir(t, root, newRun)

	// old-run 整体挪到 TTL 之外（目录与内文件都改，覆盖两种时间判据）。
	ageDir(t, root, oldRun, time.Now().Add(-runsTTL-time.Minute))

	cleanupExpiredRuns(context.Background(), runsDir)

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(oldRun))); !os.IsNotExist(err) {
		t.Errorf("超过 %s 的场地应被删除，实际 stat 结果: %v", runsTTL, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(newRun))); err != nil {
		t.Errorf("活跃场地不应被删除: %v", err)
	}
}

// TestCleanupExpiredRunsEmptyDir 空场地也要能收：没有子文件可参考时，
// 退回到目录自身的时间（本地盘 / NFS 有该属性）。
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
		t.Errorf("空但过期的场地应被删除，实际: %v", err)
	}
}

// TestCleanupExpiredRunsMissingDir 目录不存在时静默返回（全新部署的正常形态）。
func TestCleanupExpiredRunsMissingDir(t *testing.T) {
	_, restore := withTempStorage(t)
	defer restore()

	cleanupExpiredRuns(context.Background(), path.Join("tenants", "t1", "runs"))
}
