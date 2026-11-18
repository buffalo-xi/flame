package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flame/nexus/shared/storage"
)

// scanArtifacts 的三条边界必须钉住：
//   - 只报 mod_time >= since 的文件（历史文件不算本轮产物）；
//   - 依赖目录整棵跳过、隐藏文件跳过；
//   - 只扫到「根 + 一层」，更深的目录不看（与「最近文件」同一套收敛策略）。
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
	write("report.md", now)                 // 产物
	write("uploads/a.py", now)              // 产物（一层子目录）
	write("old.txt", old)                   // 历史文件：早于 since，不算
	write(".hidden", now)                   // 隐藏文件：不算
	write("node_modules/pkg/index.js", now) // 依赖目录：整棵跳过
	write("deep/x/y/z.txt", now)            // 三层：超出扫描深度

	got := scanArtifacts(context.Background(), st, "ws", now.Add(-time.Minute), 50)
	paths := make([]string, 0, len(got))
	for _, a := range got {
		paths = append(paths, a.Path)
	}

	want := map[string]bool{"report.md": true, "uploads/a.py": true}
	if len(paths) != len(want) {
		t.Fatalf("artifacts = %v, 期望恰好 %v", paths, want)
	}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("多出非产物条目: %s（全量 %v）", p, paths)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("漏报产物: %s", p)
	}
}

// 上限必须生效：产物再多也只回 limit 条，避免一次对话把整个目录树拖出来。
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
		t.Fatalf("limit 未生效: got %d 条", len(got))
	}
}
