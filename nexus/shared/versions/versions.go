// Package versions 实体版本（发布快照）的公共内核。
//
// 三种实体（agent / skill / custom_tool）的版本表各自独立（列镜像各自的实体表），
// 只有这几件事完全相同，所以集中在这里：
//   - 错误语义：未发布 / 当前版不可删 / 版本目录已存在 / 说明过长
//   - 快照：把草稿目录整棵冻结成版本目录，产出 files 清单（path / size / sha256）
//   - 清单的编码与改写（工具会在快照里补写 TOOL.json，清单必须跟着改）
//
// 工作流不走这里：它的本体是 flows.diagram 一列，没有目录，
// 见 shared/repository/flows/versions.go。
package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/storage"
)

// ErrNoReleasedVersion 实体没有正式版本（版本表里没有 is_current）。
//
// 正式入口（MCP / RPC / 端点 / flow 之间的直调）遇到它必须拒绝，
// 不能悄悄退回草稿 —— 那样「正式运行用正式版本」就没有意义了。
// 调试入口（编辑页调试器 / 试跑）读的是草稿，不经过这里。
var ErrNoReleasedVersion = errors.New("尚未发布正式版本")

// ErrCannotDeleteCurrentVersion 当前版本不能删除。
//
// 它是正式调用的定义来源，也是「线上正在跑什么」的唯一记录。要删就先切到别的版本 ——
// 切换是另一个动作，删除不该兼职下线。
var ErrCannotDeleteCurrentVersion = errors.New("当前版本不能删除，请先把线上切到其它版本")

// ErrVersionExists 版本号已被占用（版本目录已存在）。
//
// 版本是不可变快照，任何情况下都不覆盖；出现它说明编号与磁盘不同步，
// 应当停下来查，而不是把旧快照盖掉。
var ErrVersionExists = errors.New("该版本号已被占用（版本目录已存在），版本不可覆盖")

// MaxNoteLen 版本说明的长度上限，与版本表的 note varchar(200) 同口径。
const MaxNoteLen = 200

// ErrNoteTooLong 版本说明超出列宽。
var ErrNoteTooLong = fmt.Errorf("版本说明最多 %d 个字符", MaxNoteLen)

// NormalizeNote 规范化版本说明：去首尾空白 + 校验长度。
//
// 刻意不静默截断：说明是给人回溯用的，悄悄砍掉后半句会让「为什么发这一版」失真。
func NormalizeNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		return "", ErrNoteTooLong
	}
	return note, nil
}

// Snapshot 把草稿目录冻结成版本目录，返回清单（按 path 升序）。
//
//	srcDir  实体草稿目录（tenants/<schema>/{agents|skills|tools}/<name>）
//	dstDir  版本目录（tenants/<schema>/versions/<kind>/<name>/v<n>）
//
// 体积闸与上架打包同口径（assetpack.DefaultLimits）：定义域都是文本，
// 触顶通常意味着有人在目录里塞了数据集或产物。
//
// 调用方负责失败清理（DeletePrefix dstDir）与「目标目录不得已存在」的前置校验：
// 本函数只写盘，不碰库、不覆盖判定。
func Snapshot(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, skipDirs []string) ([]assetpack.FileEntry, error) {
	if st == nil {
		return nil, errors.New("storage not ready")
	}
	return assetpack.PackFilesWith(ctx, st, srcDir, dstDir, entry, assetpack.DefaultLimits(),
		assetpack.PackOptions{SkipDirs: skipDirs})
}

// EnsureAbsent 校验版本目录尚不存在。
//
// 在拷贝前调用：先判后拷，避免「发现已存在时已经写了一半」。
func EnsureAbsent(ctx context.Context, st storage.Storage, dir string) error {
	ex, err := st.Exists(ctx, dir)
	if err != nil {
		return err
	}
	if ex {
		return fmt.Errorf("%s: %w", dir, ErrVersionExists)
	}
	return nil
}

// PutEntry 把一份内容写进清单：已有同路径项则更新 size / sha256，没有则追加。
//
// 存在的理由只有一个：自定义工具的版本必须自带合法的 TOOL.json（草稿里缺或坏时
// 由合并视图补写），补写之后清单里那一项的摘要就过期了 —— 不清掉它会变成
// 「安装/载入时校验和对不上」，比缺文件更难查。
func PutEntry(files []assetpack.FileEntry, rel string, data []byte) []assetpack.FileEntry {
	sum := sha256.Sum256(data)
	e := assetpack.FileEntry{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	for i := range files {
		if files[i].Path == rel {
			files[i] = e
			return files
		}
	}
	files = append(files, e)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// Encode 清单 → 版本行 files 列的 JSON。
func Encode(files []assetpack.FileEntry) ([]byte, error) {
	if files == nil {
		files = []assetpack.FileEntry{}
	}
	return json.Marshal(files)
}

// Decode 版本行 files 列 → 清单（载入版本时用它校验并还原）。
func Decode(raw []byte) ([]assetpack.FileEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var files []assetpack.FileEntry
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("解析版本文件清单失败: %w", err)
	}
	return files, nil
}

// Summary 清单的文件数与合计字节数（列表接口只回这两个数，不回整份清单）。
func Summary(files []assetpack.FileEntry) (count int, total int64) {
	for _, f := range files {
		total += f.Size
	}
	return len(files), total
}
