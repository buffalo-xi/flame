package storage

import (
	"context"
	"time"
)

// LastActivity 估算某个目录的「最后活动时间」，供按时间清理的清扫逻辑使用。
//
// 优先用目录自身的 ModTime；S3 的「目录」没有这个属性（Entry.ModTime 为零值，
// 见 Entry 的注释），此时退回「子条目里最新的 ModTime」—— 对象存储上文件带
// LastModified。两者都拿不到返回 false：调用方应据此**不删**，
// 而不是把零值当成 1970 年，把整棵树当过期数据清掉。
//
// self 必须是调用方 List(父目录) 时拿到的该目录条目（避免多一次 Stat）。
func LastActivity(ctx context.Context, st Storage, dir string, self Entry) (time.Time, bool) {
	if !self.ModTime.IsZero() {
		return self.ModTime, true
	}
	children, err := st.List(ctx, dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, c := range children {
		if c.ModTime.After(newest) {
			newest = c.ModTime
		}
	}
	if newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}
