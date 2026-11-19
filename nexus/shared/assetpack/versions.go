package assetpack

import (
	"context"
	"fmt"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// maxBuildProbe 取号时最多往后探几个号。上架 / 导入都是低频人工操作，
// 连撞 16 个号只可能是盘上堆了一串残骸（启动清扫会收），让用户重试比继续往后刨干净。
const maxBuildProbe = 16

// FreeBuild 从 from 起找一个「还没有快照内容」的号。
//
// 为什么需要它：上架与导入都是「读行里的 build → +1 → 往 platform-assets/<id>/<build>
// 写文件」，中间没有任何互斥。并发重新上架（双击、两个管理员同时点）会算出同一个号，
// 两份内容交错写进同一棵目录，而行只记住其中一份的清单 —— 安装时 RestoreFiles 的
// sha256 校验就会报「包内容被改动过」，指不到真正的原因。跳号是安全的：build 只增不减，
// 空号不承载任何语义。
//
// 调用方必须把「取号 → 写文件」整段串行化（两处调用各持一把包级锁）：本函数只保证
// 探测时看到的是「目录里还没有东西」，不替调用方占号。跨进程（多副本部署）的撞号窗口
// 仍在，那一步要靠 DB 行锁；这里先把「同一个进程里点两下」这条最常见路径收口。
func FreeBuild(ctx context.Context, st storage.Storage, assetID string, from int) (int, error) {
	if from < 1 {
		from = 1
	}
	for n := from; n < from+maxBuildProbe; n++ {
		// 用 List 而不是 Exists：S3 上「目录」只是一个前缀，HEAD 拿不到；
		// 本地 List 在目录不存在时报错 —— 两种都按「空号」处理。真写不进去时
		// PackFiles / WriteFile 会自己报错，别让探测把上架卡死。
		if entries, err := st.List(ctx, paths.PlatformAssetKey(assetID, n)); err != nil || len(entries) == 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%d consecutive snapshot numbers are taken (asset %s, starting at v%d); retry later",
		maxBuildProbe, assetID, from)
}

// BuildsKept 每个资产保留几个快照（含当前快照，1 = 只留当前）。
//
// 快照只服务「当前快照」：安装是从快照复制到目标租户（复制不引用，见 admin/share 的硬规矩 1）、
// 导出只打当前快照、也没有「回滚到旧版」的通道。因此更旧的快照目录是死数据 ——
// 而每发一版就多一份完整镜像（单版上限 128 MiB，见 DefaultLimits），磁盘只会单调增长。
//
// 想留一次误发回滚的余地就把它改成 2。
const BuildsKept = 1

// PruneOldBuilds 删除刚掉出保留窗口的那个快照快照目录。
//
// 上架与导入两条路径共用：两者都是「同一行 build+1、内容铺进新目录」，旧目录同样没有读者。
// 必须在行已经指向新快照**之后**调用 —— 先提交行再删文件，中间不存在
// 「行指向一个空快照」的窗口。
//
// 只精确删一个目录，不做扫描：这是热路径，扫描会与并发的另一次上架抢同一棵树
// （那时「行还没指向新快照、目录已经在写」是正常状态）。历史上已经堆积的快照由启动清扫收。
//
// 返回 error 由调用方决定怎么处理：删不掉不该让上架/导入失败，内容已经在了。
func PruneOldBuilds(ctx context.Context, st storage.Storage, assetID string, current int) error {
	old := current - BuildsKept
	if old < 1 {
		return nil
	}
	return st.DeletePrefix(ctx, paths.PlatformAssetKey(assetID, old))
}
