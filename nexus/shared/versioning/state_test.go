package versioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/storage"
)

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DraftChanged 是「当前版本 + 是否有未发布改动」的判据：口径错一个方向就会说反话 ——
// 说「已发布」而线上是旧的（危险），或说「有改动」而其实一致（只是让人白点一次发布）。
func TestDraftChanged(t *testing.T) {
	ctx := context.Background()
	st := storage.NewFilesystemStorage(t.TempDir())
	dir := "tenants/t0/skills/demo"

	write := func(rel, body string) {
		t.Helper()
		if err := st.WriteFile(ctx, dir+"/"+rel, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "a")
	write("docs/spec.md", "b")

	files := []assetpack.FileEntry{
		{Path: "SKILL.md", Size: 1, SHA256: digest("a")},
		{Path: "docs/spec.md", Size: 1, SHA256: digest("b")},
	}

	changed, err := DraftChanged(ctx, st, dir, files, nil)
	if err != nil {
		t.Fatalf("DraftChanged = %v", err)
	}
	if changed {
		t.Fatal("草稿与清单一致时应为 false")
	}

	// 内容不同 → 有改动
	write("docs/spec.md", "b2")
	if changed, _ = DraftChanged(ctx, st, dir, files, nil); !changed {
		t.Fatal("文件内容变了必须是 true（否则界面会谎称已发布）")
	}
	write("docs/spec.md", "b")

	// 多一个文件 → 有改动
	write("docs/extra.md", "x")
	if changed, _ = DraftChanged(ctx, st, dir, files, nil); !changed {
		t.Fatal("草稿多出文件必须是 true")
	}
	if err := st.Delete(ctx, dir+"/docs/extra.md"); err != nil {
		t.Fatal(err)
	}

	// 少一个文件（清单里有、草稿里没有）→ 有改动
	removed := files[:1]
	if changed, _ = DraftChanged(ctx, st, dir, removed, nil); !changed {
		t.Fatal("版本里有、草稿里没有的文件必须是 true")
	}

	// 空清单 + 空目录 → 一致
	empty := storage.NewFilesystemStorage(t.TempDir())
	if changed, _ = DraftChanged(ctx, empty, "tenants/t0/tools/none", nil, nil); changed {
		t.Fatal("两边都空应为 false")
	}

	// skipDirs：被排除的目录既不参与比较、也不算多文件
	write("skills/old/SKILL.md", "deprecated")
	withSkip := append([]assetpack.FileEntry{}, files...)
	changed, err = DraftChanged(ctx, st, dir, withSkip, []string{"skills/old"})
	if err != nil {
		t.Fatalf("DraftChanged = %v", err)
	}
	if changed {
		t.Fatal("被排除的目录不应算改动（否则废弃私有技能会让 agent 永远显示「有改动」）")
	}
}
