package paths_test

import (
	"context"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// 版本目录的键是实体**当前名字**，不是 id：路径形态一旦定下，改名搬运就得跟着它。
func TestVersionKeys(t *testing.T) {
	if got, want := paths.VersionsRoot("t0"), "tenants/t0/versions"; got != want {
		t.Fatalf("VersionsRoot = %q, want %q", got, want)
	}
	if got, want := paths.VersionEntityKey("t0", paths.VersionTools, "calc"), "tenants/t0/versions/tools/calc"; got != want {
		t.Fatalf("VersionEntityKey = %q, want %q", got, want)
	}
	if got, want := paths.VersionKey("t0", paths.VersionAgents, "poet", 3), "tenants/t0/versions/agents/poet/v3"; got != want {
		t.Fatalf("VersionKey = %q, want %q", got, want)
	}
	// 私有技能没有独立版本号：它的内容在 agent 的版本目录里（这里只锁住形态）
	if got, want := paths.VersionKey("t0", paths.VersionAgents, "poet", 2), "tenants/t0/versions/agents/poet/v2"; got != want {
		t.Fatalf("agent version dir = %q, want %q", got, want)
	}
}

// 改名必须把「版本域 + 草稿域」一起搬到新名字下，旧名字下不留残骸。
func TestMoveEntityStorageMovesBothDomains(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	mustWrite(t, st, "tenants/t0/agents/poet/AGENT.md", "# poet")
	mustWrite(t, st, "tenants/t0/agents/poet/skills/verse/SKILL.md", "# verse")
	mustWrite(t, st, "tenants/t0/versions/agents/poet/v1/AGENT.md", "# v1")
	mustWrite(t, st, "tenants/t0/versions/agents/poet/v1/skills/verse/SKILL.md", "# v1 verse")

	if err := paths.MoveEntityStorage(st, "t0", paths.VersionAgents, "poet", "bard"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	for _, key := range []string{
		"tenants/t0/agents/bard/AGENT.md",
		"tenants/t0/agents/bard/skills/verse/SKILL.md",
		"tenants/t0/versions/agents/bard/v1/AGENT.md",
		"tenants/t0/versions/agents/bard/v1/skills/verse/SKILL.md",
	} {
		if ex, err := st.Exists(ctx, key); err != nil || !ex {
			t.Fatalf("%s 应在新名字下（err=%v）", key, err)
		}
	}
	for _, key := range []string{
		"tenants/t0/agents/poet",
		"tenants/t0/versions/agents/poet",
	} {
		if ex, _ := st.Exists(ctx, key); ex {
			t.Fatalf("%s 不应残留", key)
		}
	}
}

// 同名重建 / 删除实体时两个前缀都要清掉，否则新实体会继承旧历史。
func TestDeleteEntityStorageClearsBothDomains(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	mustWrite(t, st, "tenants/t0/tools/calc/TOOL.json", "{}")
	mustWrite(t, st, "tenants/t0/versions/tools/calc/v1/TOOL.json", "{}")

	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionTools, "calc"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	for _, key := range []string{"tenants/t0/tools/calc", "tenants/t0/versions/tools/calc"} {
		if ex, _ := st.Exists(ctx, key); ex {
			t.Fatalf("%s 应被清理", key)
		}
	}

	// 幂等：目录不存在时再删一次不算错误
	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionTools, "calc"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}
}

// 名字不合法一律拒绝：这些调用的后果是搬/删一整棵目录，不能靠调用方自觉。
func TestEntityStorageRejectsInvalidName(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	if err := paths.MoveEntityStorage(st, "t0", paths.VersionAgents, "..", "x"); err == nil {
		t.Fatal("MoveEntityStorage 应拒绝非法名字")
	}
	if err := paths.DeleteEntityStorage(st, "t0", paths.VersionAgents, ".."); err == nil {
		t.Fatal("DeleteEntityStorage 应拒绝非法名字")
	}
	if _, err := paths.EntityDraftKey("t0", paths.VersionKind("nope"), "x"); err == nil {
		t.Fatal("EntityDraftKey 应拒绝未知类型")
	}
}

func mustWrite(t *testing.T, st storage.Storage, key, content string) {
	t.Helper()
	if err := st.WriteFile(context.Background(), key, []byte(content)); err != nil {
		t.Fatalf("写入 %s 失败: %v", key, err)
	}
}
