package agents

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// ============================================================================
// 本轮产物（artifacts）
//
// 定位：只报「这一轮改过工作区里的哪些文件」这个客观事实，不做价值判断。
// 价值判断（哪些是交付物）留给前端筛选或后续的模型回报，顺序不能反。
//
// 为什么用「扫目录」而不是让每个工具上报：
//   - exec / code 节点里 `python gen.py > out.csv` 这类写文件的路径是隐式的，
//     工具入参里根本没有 —— 只有扫目录才看得见；
//   - 工作区是唯一产物落点，扫一次即可覆盖全部来源；
//   - 沙箱侧上报要改 codehost 与三条 mode 链路，成本远大于收益。
//
// 代价是每轮一次 LIST。为了不让「会话目录被塞满」时把一次对话变成几十次 LIST，
// 这里沿用了「最近文件」的收敛策略：只扫根 + 一层子目录，条数封顶，跳过依赖目录。
// ============================================================================

const (
	// maxArtifactScanEntries 单次扫描最多看多少条（与 maxRecentScanEntries 同量级）。
	maxArtifactScanEntries = 300
	// maxRunArtifacts 随 RUN_FINISHED 下发的产物上限：前端是「本轮产出」区域，
	// 不是文件管理器，超出部分让用户去工作区看。
	maxRunArtifacts = 50
	// artifactClockSkew 时间前移窗口。文件在 RUN_STARTED 之前几毫秒写入、
	// 或文件系统时间精度粗（S3 按秒）时，阈值卡死会漏掉。
	artifactClockSkew = 2 * time.Second
)

// artifactSkipDirs 即使被改动也不算产物：依赖 / 缓存 / VCS 目录。
// 名字按「目录名」匹配（不看层级），命中即整棵跳过。
var artifactSkipDirs = map[string]struct{}{
	"node_modules":  {},
	"__pycache__":   {},
	".venv":         {},
	"venv":          {},
	".git":          {},
	".cache":        {},
	".pytest_cache": {},
	"dist":          {},
	".next":         {},
	"site-packages": {},
	".mypy_cache":   {},
	".ruff_cache":   {},
}

// Artifact 本轮运行在工作区里写过的一个文件。
type Artifact struct {
	Path    string    `json:"path"`     // 工作区相对路径（前端直接拿去开标签）
	Name    string    `json:"name"`     //
	Size    int64     `json:"size"`     //
	ModTime time.Time `json:"mod_time"` // 最后一次写入时间
	Action  string    `json:"action"`   // 当前恒为 modified：只报「写过」，不区分新建/覆盖
}

// runArtifacts 收集某一轮对话产生的产物。失败一律返回空切片：
// 产物是锦上添花，不能因为它让一轮已经成功的对话返回错误。
//
// since 取 RUN_STARTED 的时刻；sessionID 为空（理论上不会）时直接返回空。
func runArtifacts(schema, userID, sessionID string, since time.Time) []Artifact {
	// 返回空切片而不是 nil：JSON 序列化成 []，前端不必为 null 单独兜底。
	if sessionID == "" || userID == "" {
		return []Artifact{}
	}
	st, err := getStorage()
	if err != nil {
		return []Artifact{}
	}
	wsKey := paths.UserSessionKey(schema, userID, sessionID)
	return scanArtifacts(context.Background(), st, wsKey, since, maxRunArtifacts)
}

// scanArtifacts 扫描工作区里 mod_time >= since 的文件。
// 与 RecentSessionFiles 同一套收敛策略（根 + 一层，条数封顶）。
func scanArtifacts(ctx context.Context, st storage.Storage, wsKey string, since time.Time, limit int) []Artifact {
	if limit <= 0 {
		limit = maxRunArtifacts
	}
	threshold := since.Add(-artifactClockSkew)

	out := make([]Artifact, 0, 8)
	scanned := 0

	var collect func(dir, prefix string, depth int)
	collect = func(dir, prefix string, depth int) {
		if len(out) >= limit || scanned >= maxArtifactScanEntries {
			return
		}
		entries, lerr := st.List(ctx, dir)
		if lerr != nil {
			return // 目录不存在 / 读失败：这一层当作空
		}
		for _, e := range entries {
			if len(out) >= limit || scanned >= maxArtifactScanEntries {
				return
			}
			if strings.HasPrefix(e.Name, ".") { // 隐藏文件（含 .keep 占位）不是产物
				continue
			}
			scanned++
			rel := path.Join(prefix, e.Name)
			if e.IsDir {
				if depth == 0 {
					if _, skip := artifactSkipDirs[e.Name]; !skip {
						collect(path.Join(dir, e.Name), rel, 1)
					}
				}
				continue
			}
			// 零值时间只出现在 S3 的 CommonPrefixes（目录），对象本身都有 LastModified；
			// 真遇到零值宁可漏报也不误报 —— 否则每次都会把历史文件全列一遍。
			if e.ModTime.IsZero() || e.ModTime.Before(threshold) {
				continue
			}
			out = append(out, Artifact{
				Path:    rel,
				Name:    e.Name,
				Size:    e.Size,
				ModTime: e.ModTime,
				Action:  "modified",
			})
		}
	}
	collect(wsKey, "", 0)

	// 最近写过的排前面；同一秒写入时按路径排，保证顺序稳定（前端展示不跳）
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].Path < out[j].Path
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out
}
