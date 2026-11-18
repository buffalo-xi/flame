package agent

import (
	"context"
	"path"
	"time"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/tenants"
	"flame/tools/xlog"
)

// runsTTL 临时运行场地的保留时长：早于这个时间的 run 目录视为「上次遗留」。
//
// 为什么按时间而不是按归属：多实例共享同一 storage（NFS / S3）时，没有可靠凭据
// 判断某个目录在**别的实例**上是否还有活跃 run —— 活跃信息只在本进程的
// runRegistry 里。24 小时远大于单次 run 的实际时长（含多轮工具调用与长推理），
// 所以正常运行中的场地不会被碰到，只有崩溃 / 漏删留下的才会被收走。
const runsTTL = 24 * time.Hour

// CleanupOrphanRuns 清理无 session 调用遗留的临时目录（启动时调用一次）。
//
// 与改造前的区别：不再整棵删除 runs/。多实例共享同一存储时，整棵删除会连带
// 删掉别的实例**正在跑**的场地；现在只删「最后活动时间早于 runsTTL」的 run 目录。
//
// 目录形态：
//
//	tenants/{schema}/users/{uid}/runs/{run_id}/   有 user 的临时场地
//	tenants/{schema}/runs/{run_id}/               无 user 的兜底场地
func CleanupOrphanRuns() {
	if agentStorage == nil {
		return
	}
	schemas, err := tenants.ListSchemas()
	if err != nil {
		xlog.Error("清理临时运行目录失败（列举租户）: %v", err)
		return
	}
	ctx := context.Background()
	for _, sc := range schemas {
		// 租户级兜底目录
		cleanupExpiredRuns(ctx, path.Join(paths.TenantsSubdir, sc, paths.RunsSubdir))

		// user 级临时目录：users/{uid}/runs/
		usersDir := path.Join(paths.TenantsSubdir, sc, paths.UsersSubdir)
		users, err := agentStorage.List(ctx, usersDir)
		if err != nil {
			// 该租户还没有任何用户目录：不是错误。其它错误记一笔再跳过。
			xlog.Error("列举用户目录失败 schema=%s: %v", sc, err)
			continue
		}
		for _, u := range users {
			if !u.IsDir {
				continue
			}
			cleanupExpiredRuns(ctx, path.Join(usersDir, u.Name, paths.RunsSubdir))
		}
	}
	xlog.Info("临时运行目录清理完成（超过 %s 的场地）", runsTTL)
}

// cleanupExpiredRuns 删除 dir 下「最后活动时间早于 runsTTL」的 run 目录。
// dir 不存在（全新部署 / 从未发生无 session 调用）时静默返回。
func cleanupExpiredRuns(ctx context.Context, dir string) {
	entries, err := agentStorage.List(ctx, dir)
	if err != nil {
		return
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		p := path.Join(dir, e.Name)
		ts, ok := storage.LastActivity(ctx, agentStorage, p, e)
		if !ok {
			// 拿不到时间就不删：宁可留下垃圾，也不能凭猜测删掉可能活跃的场地。
			continue
		}
		if time.Since(ts) < runsTTL {
			continue
		}
		if err := agentStorage.DeletePrefix(ctx, p); err != nil {
			xlog.Error("清理临时运行目录失败 %s: %v", p, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		xlog.Info("临时运行目录清理 %s：删除 %d 个超过 %s 的目录", dir, removed, runsTTL)
	}
}
