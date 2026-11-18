package storage

import (
	"context"
	"testing"
)

// 本地实现的重命名：文件与目录都要能搬，且「同名前缀」的兄弟文件不许被牵连。
// 这条用例同时钉住了 S3 分支的设计前提 —— 单文件靠精确 key 判定，不按前缀扫。
func TestFilesystemRename(t *testing.T) {
	st := NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	if err := st.WriteFile(ctx, "ws/a.md", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ctx, "ws/a.md.bak", []byte("BAK")); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(ctx, "ws/a.md", "ws/b.md"); err != nil {
		t.Fatalf("单文件重命名失败: %v", err)
	}
	if data, err := st.ReadFile(ctx, "ws/b.md"); err != nil || string(data) != "A" {
		t.Errorf("新路径内容 = %q, err = %v", data, err)
	}
	if ex, _ := st.Exists(ctx, "ws/a.md"); ex {
		t.Error("旧路径仍然存在")
	}
	if data, err := st.ReadFile(ctx, "ws/a.md.bak"); err != nil || string(data) != "BAK" {
		t.Errorf("同前缀的兄弟文件被牵连: %q, err = %v", data, err)
	}

	// 目录：整棵搬走，旧位置不留残。
	if err := st.WriteFile(ctx, "ws/dir/x.md", []byte("X")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ctx, "ws/dir/sub/y.md", []byte("Y")); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(ctx, "ws/dir", "ws/moved"); err != nil {
		t.Fatalf("目录重命名失败: %v", err)
	}
	if data, err := st.ReadFile(ctx, "ws/moved/sub/y.md"); err != nil || string(data) != "Y" {
		t.Errorf("子目录未跟着搬: %q, err = %v", data, err)
	}
	if entries, _ := st.List(ctx, "ws/dir"); len(entries) != 0 {
		t.Errorf("旧目录还留着 %d 个条目", len(entries))
	}

	// 目标父目录不存在时自动建出（与新建条目的语义一致）。
	if err := st.Rename(ctx, "ws/b.md", "ws/deep/er/c.md"); err != nil {
		t.Fatalf("搬到不存在的父目录失败: %v", err)
	}
	if ex, _ := st.Exists(ctx, "ws/deep/er/c.md"); !ex {
		t.Error("多层落点未建出")
	}

	// 源不存在：幂等返回 nil（接口层自己用 entryExists 报 404，不靠这里）。
	if err := st.Rename(ctx, "ws/nope", "ws/other"); err != nil {
		t.Errorf("源不存在应返回 nil, got %v", err)
	}
}
