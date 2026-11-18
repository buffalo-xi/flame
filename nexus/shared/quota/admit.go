package quota

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/storage"
	"flame/tools/xlog"
)

const (
	// evictMaxPerCall 单次请求最多删多少个文件：删太多会把上传/写文件拖成秒级，
	// 剩下的交给下一次写入继续淘汰（配额是渐进的，不需要一次清空）。
	evictMaxPerCall = 50
	// evictSkipRecent 最近这段时间内改过的文件不删 —— 很可能就是 agent 正在写的产物。
	evictSkipRecent = 5 * time.Minute
)

// ExceededError 配额不足。调用方（HTTP handler）据此返回 507，
// 带上 scope / metric / used / limit 让前端能给出「去清理」这类引导。
type ExceededError struct {
	Scope  string // session | user
	Metric string // bytes | sessions
	Used   int64
	Limit  int64
}

func (e *ExceededError) Error() string {
	if e.Metric == "sessions" {
		return fmt.Sprintf("会话数量已达上限（%d），请删除不再使用的会话后再试", e.Limit)
	}
	return fmt.Sprintf("会话工作区空间已满（%s / %s），请清理不再需要的文件后重试",
		humanBytes(e.Used), humanBytes(e.Limit))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	for _, u := range units {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", f/unit)
}

type fileInfo struct {
	rel  string
	size int64
	mod  time.Time
}

// Admit 写入前的配额预检：必要时扫描会话目录并按「最旧优先」淘汰，直到这次写入放得下。
//
// scopeKey 是会话工作区的存储相对路径（paths.UserSessionKey 的结果）；
// delta 是本次要新增的字节数（覆盖写请传 new-old，可能为负）。
// 返回被删掉的文件（相对存储根的路径），调用方可以把它们回给前端做提示。
//
// 调用约定（顺序很重要）：
//
//	evicted, err := quota.Admit(...)   // 预检 + 淘汰
//	if err != nil { return err }       // 507：一个文件都不写
//	写入文件
//	quota.Commit(...)                  // 只有写成功才记账，失败不记，计数不会虚高
//
// userID 用于取「该用户」的会话空间上限（user_quotas 覆盖租户值）；
// 传空串时退化为租户值。
func Admit(ctx context.Context, st storage.Storage, schema, scopeKey, userID string, delta int64) ([]string, error) {
	lim := ResolveUserSessionStorage(schema, userID)
	if lim == -1 || st == nil || scopeKey == "" {
		return nil, nil // 不限制 / 不是会话工作区：完全不碰配额
	}
	if delta <= 0 {
		return nil, nil // 覆盖成更小、或删除：不会让用量增加
	}

	s := GetStore()
	u, ok, _ := s.Get(ctx, schema, scopeKey)

	if !ok || u.Bytes+delta > lim || expired(u, lim) {
		total, files, err := scanFiles(ctx, st, scopeKey)
		if err != nil {
			// 扫描失败不阻断写入：配额是保护性功能，统计出问题时
			// 让用户先写进去，比因为统计失败而拒绝服务更合理。
			xlog.Error("[QUOTA] 扫描会话目录失败 schema=%s key=%s: %v", schema, scopeKey, err)
			return nil, nil
		}
		evicted, enough := evictOldest(ctx, st, scopeKey, files, &total, lim, delta)
		// 扫描值即当前真实值：回写基准，让后续写入继续走 O(1) 快路径
		if serr := s.Set(ctx, schema, scopeKey, Usage{Bytes: total, VerifiedAt: time.Now()}); serr != nil {
			xlog.Error("[QUOTA] 回写用量失败 schema=%s key=%s: %v", schema, scopeKey, serr)
		}
		if !enough {
			xlog.Error("[QUOTA_ALERT] 会话空间不足 schema=%s key=%s used=%d limit=%d delta=%d",
				schema, scopeKey, total, lim, delta)
			return evicted, &ExceededError{Scope: "session", Metric: "bytes", Used: total, Limit: lim}
		}
		if len(evicted) > 0 {
			xlog.Error("[QUOTA_ALERT] 已淘汰旧文件 schema=%s key=%s count=%d", schema, scopeKey, len(evicted))
		}
	}
	return nil, nil
}

// Commit 写成功后记账（delta 为本次实际新增的字节）。
// 写入失败时**不要**调用，这样计数不会出现「文件没落地但额度被扣」的假超限。
func Commit(ctx context.Context, schema, scopeKey, userID string, delta int64) {
	if delta == 0 || scopeKey == "" {
		return
	}
	if ResolveUserSessionStorage(schema, userID) == -1 {
		return // 不限制时不维护计数
	}
	if err := GetStore().Add(ctx, schema, scopeKey, delta); err != nil {
		xlog.Error("[QUOTA] 记账失败 schema=%s key=%s delta=%d: %v", schema, scopeKey, delta, err)
	}
}

// Reset 会话删除时清掉计数。
func Reset(ctx context.Context, schema, scopeKey string) {
	if scopeKey == "" {
		return
	}
	if err := GetStore().Reset(ctx, schema, scopeKey); err != nil {
		xlog.Error("[QUOTA] 清理用量失败 schema=%s key=%s: %v", schema, scopeKey, err)
	}
}

// Snapshot 返回某会话当前的用量与上限（工作区工具条的用量条用）。
// 只读计数，不扫描；计数缺失时返回 0。
func Snapshot(ctx context.Context, schema, scopeKey, userID string) (used, limit int64) {
	limit = ResolveUserSessionStorage(schema, userID)
	if scopeKey == "" {
		return 0, limit
	}
	if u, ok, _ := GetStore().Get(ctx, schema, scopeKey); ok {
		return u.Bytes, limit
	}
	return 0, limit
}

// CheckUserSessions 检查某用户的会话数是否已达上限。超限返回 *ExceededError。
// 计数失败时放行（fail-open）：配额不该因为一次 DB 抖动就阻断业务。
func CheckUserSessions(schema, ownerType, userID string) error {
	lim := ResolveUserSessions(schema, userID) // 用户覆盖值 → 租户值（clamp）
	if lim == -1 || userID == "" {
		return nil
	}
	n, err := CountUserSessions(schema, ownerType, userID)
	if err != nil {
		xlog.Error("[QUOTA] 统计会话数失败 schema=%s uid=%s: %v", schema, userID, err)
		return nil
	}
	if n >= lim {
		xlog.Error("[QUOTA_ALERT] 会话数超限 schema=%s uid=%s used=%d limit=%d", schema, userID, n, lim)
		return &ExceededError{Scope: "user", Metric: "sessions", Used: int64(n), Limit: int64(lim)}
	}
	return nil
}

// expired 判断计数是否需要重新校准：
//   - 超过 CalibrateAfter：可能有 exec 直写没被记账
//   - 接近上限且距上次校准超过 NearLimitInterval：避免"以为是 92% 其实已经 99%"
func expired(u Usage, lim int64) bool {
	if u.VerifiedAt.IsZero() {
		return true
	}
	age := time.Since(u.VerifiedAt)
	if age > CalibrateAfter {
		return true
	}
	if lim > 0 && u.Bytes*10 >= lim*9 && age > NearLimitInterval {
		return true
	}
	return false
}

// scanFiles 递归枚举会话工作区，返回总字节数与文件列表（顺序不定）。
func scanFiles(ctx context.Context, st storage.Storage, key string) (int64, []fileInfo, error) {
	w, ok := st.(storage.Walker)
	if !ok {
		return 0, nil, fmt.Errorf("storage driver does not support WalkPrefix")
	}
	var total int64
	files := make([]fileInfo, 0, 64)
	err := w.WalkPrefix(ctx, key, func(rel string, size int64, mod time.Time) error {
		total += size
		files = append(files, fileInfo{rel: rel, size: size, mod: mod})
		return nil
	})
	return total, files, err
}

// evictOldest 按 mtime 升序删除，直到 total+delta <= lim 或没有可删的了。
//
// 保护规则：不删会话根下 skills/ 里的文件、不删最近 evictSkipRecent 内改过的文件、
// 单次最多删 evictMaxPerCall 个。返回被删列表与"是否已腾出足够空间"。
func evictOldest(ctx context.Context, st storage.Storage, key string, files []fileInfo,
	total *int64, lim, delta int64) ([]string, bool) {
	if *total+delta <= lim {
		return nil, true
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	now := time.Now()
	evicted := make([]string, 0, 8)
	for _, f := range files {
		if *total+delta <= lim || len(evicted) >= evictMaxPerCall {
			break
		}
		sub := strings.TrimPrefix(f.rel, key+"/")
		if sub == "skills" || strings.HasPrefix(sub, "skills/") {
			continue // 技能包属于 agent 定义的一部分，不当作产物删
		}
		if !f.mod.IsZero() && now.Sub(f.mod) < evictSkipRecent {
			continue // 可能是 agent 正在写的文件
		}
		if err := st.Delete(ctx, f.rel); err != nil {
			xlog.Error("[QUOTA] 淘汰文件失败 key=%s: %v", f.rel, err)
			continue
		}
		*total -= f.size
		// 对外给相对会话根的路径（前端提示用），删除时用的仍是完整 key
		evicted = append(evicted, sub)
	}
	return evicted, *total+delta <= lim
}
