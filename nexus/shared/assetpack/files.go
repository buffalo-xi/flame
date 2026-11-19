package assetpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"flame/nexus/shared/storage"
)

// PackFiles 把 srcDir 整棵拷进 dstDir，返回文件清单（按 path 升序，含 sha256）。
//
//	srcDir  tenants/<source>/skills|tools|agents/<name>
//	dstDir  platform-assets/<assetID>/<build>
//
// entry 非空时要求源目录里存在该文件（skill 的 SKILL.md、agent 的 AGENT.md、tool 的
// TOOL.json entry），否则返回 ErrMissingEntry —— 「上架成功但装上去跑不了」比上架失败更糟。
//
// 闸值先判后拷：单文件超限、文件数超限、合计超限都在**写入前**拒绝，避免把半份内容
// 写进快照目录（调用方虽然会 DeletePrefix 兜底，但没必要制造垃圾）。
func PackFiles(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, lim Limits) ([]FileEntry, error) {
	return PackFilesWith(ctx, st, srcDir, dstDir, entry, lim, PackOptions{})
}

// PackFilesWith 同 PackFiles，但可以排除若干子目录、并额外挂进若干目录树
// （见 PackOptions）。
func PackFilesWith(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, lim Limits, opts PackOptions) ([]FileEntry, error) {
	lim = lim.withDefaults()

	var (
		files    []FileEntry
		total    int64
		hasEntry bool
	)

	// addTree 把一个源目录整棵拷进包内 prefix 下，同时累计体积闸与清单。
	// prefix 为空 = 主目录（entry 只对主目录校验）。
	addTree := func(dir, prefix string, skipDirs []string, required bool) error {
		ok, e := st.Exists(ctx, dir)
		if e != nil {
			return fmt.Errorf("failed to scan the asset directory: %w", e)
		}
		if !ok {
			if !required {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrNoFiles, dir)
		}

		skip := func(rel string) bool {
			for _, d := range skipDirs {
				if d = strings.Trim(strings.TrimSpace(d), "/"); d != "" &&
					(rel == d || strings.HasPrefix(rel, d+"/")) {
					return true
				}
			}
			return false
		}

		return walkFiles(ctx, st, dir, "", skip, func(rel string, size int64) error {
			outRel := rel
			if prefix != "" {
				outRel = path.Join(prefix, rel)
			}
			if size > lim.MaxFileSize {
				return fmt.Errorf("%w: %s (%d bytes, limit %d)", ErrFileTooLarge, outRel, size, lim.MaxFileSize)
			}
			if len(files) >= lim.MaxFiles {
				return fmt.Errorf("%w (already %d files, limit %d)", ErrTooManyFiles, len(files), lim.MaxFiles)
			}
			if total+size > lim.MaxTotal {
				return fmt.Errorf("%w (%d bytes so far, this file %d bytes, limit %d bytes)",
					ErrTotalTooLarge, total, size, lim.MaxTotal)
			}

			data, rerr := st.ReadFile(ctx, path.Join(dir, rel))
			if rerr != nil {
				return fmt.Errorf("failed to read %s: %w", rel, rerr)
			}
			if werr := st.WriteFile(ctx, path.Join(dstDir, outRel), data); werr != nil {
				return fmt.Errorf("failed to write the snapshot %s: %w", outRel, werr)
			}

			sum := sha256.Sum256(data)
			files = append(files, FileEntry{
				Path:   outRel,
				Size:   int64(len(data)),
				SHA256: hex.EncodeToString(sum[:]),
			})
			total += int64(len(data))
			if outRel == entry {
				hasEntry = true
			}
			return nil
		})
	}

	if err := addTree(srcDir, "", opts.SkipDirs, true); err != nil {
		return nil, err
	}
	for _, t := range opts.Trees {
		if err := addTree(t.SrcDir, strings.Trim(t.DstPrefix, "/"), nil, t.Required); err != nil {
			return nil, err
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoFiles, srcDir)
	}
	if entry != "" && !hasEntry {
		return nil, fmt.Errorf("%w: %s has no %s", ErrMissingEntry, srcDir, entry)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// BodyFiles 从清单里摘出资产本体（去掉 _deps/ 附件）。
//
// 安装时要分开处理：本体落进资产自己的目录，附件落进 custom_tools/。
func BodyFiles(files []FileEntry) []FileEntry {
	out := make([]FileEntry, 0, len(files))
	for _, f := range files {
		if isAttachment(f.Path) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Attachments 摘下包内附件，按「附件组名」分组（目前只有自定义工具：组名 = 工具名）。
//
// 返回的 FileEntry **保留完整包内路径**（_deps/custom_tools/<name>/…）：安装时用
// RestoreFilesUnder 配合同一个前缀落盘，不用在调用方拼路径。
func Attachments(files []FileEntry) map[string][]FileEntry {
	prefix := BundledToolsPrefix + "/"
	out := map[string][]FileEntry{}
	for _, f := range files {
		if !isAttachment(f.Path) {
			continue
		}
		rel := strings.TrimPrefix(f.Path, prefix)
		name, rest, ok := strings.Cut(rel, "/")
		if !ok || name == "" || rest == "" {
			continue
		}
		out[name] = append(out[name], f)
	}
	return out
}

// ToolAttachmentPrefix 某个附件工具的包内前缀（拼 RestoreFilesUnder 用）。
func ToolAttachmentPrefix(name string) string {
	return BundledToolsPrefix + "/" + name
}

func isAttachment(p string) bool {
	return p == BundledToolsPrefix || strings.HasPrefix(p, BundledToolsPrefix+"/")
}

// RestoreFiles 按清单把快照目录拷到目标目录，返回清单里有、快照里没有的文件。
//
// 缺失**不返回错误**：快照可能因为历史快照或人工清理少了附属文件，只要入口文件在，
// 装上去仍然可用；调用方把 missing 原样并入响应的 warnings 即可（不要静默吞掉）。
// 内容与清单不一致（sha256 对不上）则报 ErrChecksum —— 那是存储损坏或并发写入，
// 继续装只会把坏文件铺到目标租户。
func RestoreFiles(ctx context.Context, st storage.Storage, snapDir, dstDir string, files []FileEntry) (missing []string, err error) {
	return RestoreFilesUnder(ctx, st, snapDir, dstDir, "", files)
}

// RestoreFilesUnder 同 RestoreFiles，但落盘前把每个清单路径的 prefix 去掉。
//
// 用于包内附件：清单里是 _deps/custom_tools/<name>/main.py，目标是 custom_tools/<name>/main.py。
// prefix 为空等价于 RestoreFiles。
func RestoreFilesUnder(ctx context.Context, st storage.Storage, snapDir, dstDir, prefix string, files []FileEntry) (missing []string, err error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, f := range files {
		rel := f.Path
		if prefix != "" {
			if !strings.HasPrefix(f.Path, prefix) {
				continue // 不在这个前缀下：不属于本次要落的文件
			}
			rel = strings.TrimPrefix(f.Path, prefix)
		}
		if rel == "" {
			continue
		}
		src := path.Join(snapDir, f.Path)
		ok, e := st.Exists(ctx, src)
		if e != nil {
			return missing, fmt.Errorf("failed to check the snapshot %s: %w", f.Path, e)
		}
		if !ok {
			missing = append(missing, f.Path)
			continue
		}

		data, e := st.ReadFile(ctx, src)
		if e != nil {
			return missing, fmt.Errorf("failed to read the snapshot %s: %w", f.Path, e)
		}
		if f.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != f.SHA256 {
				return missing, fmt.Errorf("%w: %s", ErrChecksum, f.Path)
			}
		}
		if e := st.WriteFile(ctx, path.Join(dstDir, rel), data); e != nil {
			return missing, fmt.Errorf("failed to write %s: %w", rel, e)
		}
	}
	return missing, nil
}

// walkFiles 递归遍历目录，回调拿到相对路径与文件大小（目录不回调）。
//
// 每层按名字排序，保证清单与遍历顺序稳定：manifest 进 DB、进审核页，同一份内容
// 每次上架都该产出同一份清单（否则 diff 与「是否变化」判断全是噪音）。
func walkFiles(ctx context.Context, st storage.Storage, dir, rel string, skip func(rel string) bool, fn func(rel string, size int64) error) error {
	entries, err := st.List(ctx, dir)
	if err != nil {
		return fmt.Errorf("failed to list the directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	for _, e := range entries {
		childRel := path.Join(rel, e.Name)
		if skip != nil && skip(childRel) {
			continue
		}
		childKey := path.Join(dir, e.Name)
		if e.IsDir {
			if err := walkFiles(ctx, st, childKey, childRel, skip, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(childRel, e.Size); err != nil {
			return err
		}
	}
	return nil
}
