package assetpack

import (
	"context"
	"strconv"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// 上架/导入路径上的精确删除：只删「刚掉出窗口」的那一个快照，当前快照绝不动。
//
// 这条不变量撑起整套「磁盘不随快照数增长」的策略：每次上架 build+1，
// 因此每次只有一个快照掉出窗口，删一个就够，不需要扫描。
func TestPruneOldBuilds(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	mustWrite(t, st, paths.PlatformAssetKey("a1", 2)+"/SKILL.md", "x\n")
	mustWrite(t, st, paths.PlatformAssetKey("a1", 3)+"/SKILL.md", "x\n")

	// 首次上架（v1）：没有掉出窗口的快照，不该动任何目录
	if err := PruneOldBuilds(ctx, st, "a1", 1); err != nil {
		t.Fatalf("PruneOldBuilds(v1): %v", err)
	}
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 2), true)

	// 重发到 v3：v2 掉出窗口被删，v3 留着
	if err := PruneOldBuilds(ctx, st, "a1", 3); err != nil {
		t.Fatalf("PruneOldBuilds(v3): %v", err)
	}
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 2), false)
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 3), true)
}

// 取号必须跳过「已经有内容的快照目录」：并发重新上架、或上次写了一半崩掉，
// 都会留下一个不该被复用的号 —— 复用会让两份内容挤在同一棵树上，而行只记住一份的清单。
func TestFreeBuildSkipsOccupied(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	// 全新资产：没有任何目录，从起点号直接用
	if n, err := FreeBuild(ctx, st, "a2", 1); err != nil || n != 1 {
		t.Fatalf("FreeBuild(空资产) = %d, %v, 期望 1", n, err)
	}

	// 1、2 已占：跳过它们拿到 3
	mustWrite(t, st, paths.PlatformAssetKey("a2", 1)+"/SKILL.md", "x\n")
	mustWrite(t, st, paths.PlatformAssetKey("a2", 2)+"/SKILL.md", "x\n")
	if n, err := FreeBuild(ctx, st, "a2", 1); err != nil || n != 3 {
		t.Fatalf("FreeBuild(1,2 已占) = %d, %v, 期望 3", n, err)
	}
	// 从中间起算：5 是空的就直接用 5，不回头重数
	if n, err := FreeBuild(ctx, st, "a2", 5); err != nil || n != 5 {
		t.Fatalf("FreeBuild(from=5) = %d, %v, 期望 5", n, err)
	}

	// 连撞到上限：报错，绝不静默复用已有目录
	for i := 1; i <= maxBuildProbe; i++ {
		mustWrite(t, st, paths.PlatformAssetKey("a3", i)+"/SKILL.md", "x\n")
	}
	if _, err := FreeBuild(ctx, st, "a3", 1); err == nil {
		t.Fatal("连续占满上限时应报错，不能复用已有目录")
	}
}

// 保留窗口的缺省值是「只留当前快照」。改策略时这条会先失败，
// 提醒同步改用例与 snapshot 相关文档，而不是让磁盘悄悄多留一堆死快照。
func TestBuildsKeptDefault(t *testing.T) {
	if BuildsKept != 1 {
		t.Fatalf("BuildsKept = %d，期望 1", BuildsKept)
	}
}

func mustExistCase(t *testing.T, st storage.Storage, key string, want bool) {
	t.Helper()
	got, err := st.Exists(context.Background(), key)
	if err != nil {
		t.Fatalf("Exists(%s): %v", key, err)
	}
	if got != want {
		t.Fatalf("%s 存在性 = %v, 期望 %v (%s)", key, got, want, strconv.Quote(key))
	}
}
