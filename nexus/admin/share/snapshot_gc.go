package share

import (
	"context"
	"path"
	"strconv"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"
)

// CleanupSnapshots 启动时清扫平台资产快照目录（一次）。收两类垃圾：
//
//   - 表里没有的资产目录：导入/上架写了一半崩掉，或行已删但 DeletePrefix 失败留下的孤儿；
//   - 超出保留窗口的旧快照；
//   - 比当前快照还新、且超过 snapshotPendingTTL 没有任何写入的残留快照
//     （写完文件、行没提交成功；刚写完的不动 —— 那可能正是别的实例在发布）。
//
// 只在启动跑：运行期由 assetpack.PruneOldBuilds 精确删除。全盘扫描会与正在进行的上架
// 抢同一个目录 —— 那时「行还没指向新快照、目录已经在写」是正常状态，扫描会误判成孤儿。
//
// 多实例共享同一存储时，最后一条是必须的：同一时刻可能有另一个实例正在发布，
// 它的新快照目录此刻「比库里当前 build 还新」但并不属于残留。
const snapshotPendingTTL = time.Hour

func CleanupSnapshots() {
	st, err := getStorage()
	if err != nil {
		xlog.Error("清理资产快照失败（存储未就绪）: %v", err)
		return
	}
	// 空条件 = 全部状态：草稿与已下架也有当前快照，不能只按 listed 判
	assets, lerr := sharedassets.List(sharedassets.Filter{})
	if lerr != nil {
		xlog.Error("清理资产快照失败（列举资产）: %v", lerr)
		return
	}
	current := make(map[string]int, len(assets))
	for _, a := range assets {
		current[a.ID] = a.Build
	}
	orphans, builds := cleanupSnapshotDirs(context.Background(), st, current)
	xlog.Info("资产快照清理完成：孤儿目录 %d 个、旧快照 %d 个", orphans, builds)
}

// cleanupSnapshotDirs 是 CleanupSnapshots 的纯存储部分（不碰 DB，便于单测）。
func cleanupSnapshotDirs(ctx context.Context, st storage.Storage, current map[string]int) (orphans, builds int) {
	ids, derr := st.List(ctx, paths.PlatformAssetsSubdir)
	if derr != nil {
		// 全新部署没有这个目录；S3 上列举一个不存在的「目录」也可能报错，都按「没有」处理
		return 0, 0
	}
	for _, id := range ids {
		if !id.IsDir {
			continue
		}
		dir := path.Join(paths.PlatformAssetsSubdir, id.Name)
		cur, known := current[id.Name]
		if !known {
			if err := st.DeletePrefix(ctx, dir); err != nil {
				xlog.Error("删除孤儿快照失败 %s: %v", dir, err)
				continue
			}
			orphans++
			continue
		}
		vers, verr := st.List(ctx, dir)
		if verr != nil {
			continue
		}
		for _, v := range vers {
			if !v.IsDir {
				continue
			}
			n, aerr := strconv.Atoi(v.Name)
			if aerr != nil {
				continue // 不是快照目录（例如人工放的东西），不猜
			}
			p := path.Join(dir, v.Name)
			switch {
			case n <= cur-assetpack.BuildsKept:
				// 掉出保留窗口的旧快照：确定没有读者（安装/导出只读当前快照），直接删。
			case n > cur:
				// 比库里当前 build 还新：可能是「写完文件、行没提交成功」的残留，
				// 也可能是**别的实例正在发布**（目录已写、行还没提交，属正常瞬时状态）。
				// 目录本身区分不了两者，只能靠时间：超过 snapshotPendingTTL 无写入才算残留。
				ts, ok := storage.LastActivity(ctx, st, p, v)
				if !ok || time.Since(ts) < snapshotPendingTTL {
					continue
				}
			default:
				continue // 仍在保留窗口内
			}
			if err := st.DeletePrefix(ctx, p); err != nil {
				xlog.Error("删除快照目录失败 %s: %v", p, err)
				continue
			}
			builds++
		}
	}
	return orphans, builds
}
