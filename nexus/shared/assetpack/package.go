package assetpack

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/storage"
)

// 资产包：跨服务器搬运一份已审核快照的单一文件（zip）。
//
// 为什么是「包」而不是「远程引用」：分发语义是复制 —— 装完的实体与来源再无关系。
// 跨服务器同理：包里带够重建一份本地快照所需的全部东西，收下之后对端停不停机与本机无关。
//
//	package.json  { format, exported_at, origin, manifest, payload }
//	files/<rel>   快照里的每个文件（相对路径原样保留）
//
// 包里刻意**不含**：租户 schema 的授权行、agent_relations（uuid 关系，跨环境无法重映射）。
// 智能体的三个开关（is_supervisor / tools_unrestricted / skill_discovery）**在 payload 里**：
// 它们描述「实体怎么跑」，导入端按包设置；`endpoints` 属于目标租户，不随包走。
const (
	// PackageFormat 包格式标识。值变更 = 破坏性变更（导入端按它拒绝旧/新格式）。
	PackageFormat = "flame-asset/1"

	packageMetaName = "package.json"
	packageFilesDir = "files"
	// 元信息上限：payload 是实体行快照，实测几 KB；1 MiB 已经非常宽松。
	packageMetaMax = 1 << 20
)

// PackageOrigin 导出方溯源信息。导入端只把它写进 origin_* 列做展示与去重，
// 不构成任何运行期引用（对端删库/改名都不影响本机已导入的资产）。
type PackageOrigin struct {
	Server       string `json:"server"`                  // 导出方站点名（config server.name）
	AssetID      string `json:"asset_id"`                // 导出方的 asset id（仅溯源）
	SourceSchema string `json:"source_schema,omitempty"` // 导出方租户 schema
	Build        int    `json:"build"`                   // 导出方那份资产的快照号
	ListedBy     string `json:"listed_by,omitempty"`     // 上架人展示名快照
}

// Package 包内元信息（package.json 的内容）。
type Package struct {
	Format     string          `json:"format"`
	ExportedAt time.Time       `json:"exported_at"`
	Origin     PackageOrigin   `json:"origin"`
	Manifest   Manifest        `json:"manifest"`
	Payload    json.RawMessage `json:"payload"`
}

// WritePackage 把快照目录写成 zip 流（边读边写，不把文件读进内存）。
//
// snapDir 是 platform-assets/<id>/<build> 的逻辑 key；每个文件按 manifest.Files 读回，
// 顺手核对字节数与清单一致 —— 导出的是「清单所说的那份内容」，不是目录里当前有什么。
func WritePackage(ctx context.Context, st storage.Storage, snapDir string, pkg *Package, w io.Writer) error {
	if pkg == nil {
		return errors.New("package is nil")
	}
	if pkg.Format == "" {
		pkg.Format = PackageFormat
	}
	if pkg.Format != PackageFormat {
		return fmt.Errorf("不支持的包格式：%s", pkg.Format)
	}
	if len(pkg.Manifest.Files) == 0 {
		return fmt.Errorf("%w：清单为空", ErrNoFiles)
	}
	if len(pkg.Payload) == 0 {
		pkg.Payload = json.RawMessage(`{}`)
	}
	if pkg.ExportedAt.IsZero() {
		pkg.ExportedAt = time.Now()
	}

	meta, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("序列化包元信息失败: %w", err)
	}

	zw := zip.NewWriter(w)
	fw, err := zw.CreateHeader(&zip.FileHeader{
		Name: packageMetaName, Method: zip.Deflate, Modified: pkg.ExportedAt,
	})
	if err != nil {
		return err
	}
	if _, err := fw.Write(meta); err != nil {
		return err
	}

	now := pkg.ExportedAt
	for _, f := range pkg.Manifest.Files {
		rel, rerr := safeRelPath(f.Path)
		if rerr != nil {
			return rerr
		}
		rc, oerr := st.ReadStream(ctx, path.Join(snapDir, rel))
		if oerr != nil {
			return fmt.Errorf("读取快照文件 %s: %w", rel, oerr)
		}
		entry, cerr := zw.CreateHeader(&zip.FileHeader{
			Name: packageFilesDir + "/" + rel, Method: zip.Deflate, Modified: now,
		})
		if cerr != nil {
			_ = rc.Close()
			return cerr
		}
		n, cpErr := io.Copy(entry, rc)
		_ = rc.Close()
		if cpErr != nil {
			return fmt.Errorf("写入包内文件 %s: %w", rel, cpErr)
		}
		if n != f.Size {
			return fmt.Errorf("快照文件 %s 与清单不一致（清单 %d 字节，实际 %d）", rel, f.Size, n)
		}
	}
	return zw.Close()
}

// ReadPackage 读回并**完整校验**一个包：体积/文件数闸、路径安全、清单与内容一一对应、sha256。
//
// 校验全在这里做完，调用方拿到 (pkg, files) 后只需落盘与写库 —— 这是「不信上游」的落点：
// 对端说包里有 3 个文件，就以包里的实际内容为准逐条比对，不一致整包拒绝（不做部分导入）。
func ReadPackage(ra io.ReaderAt, size int64, lim Limits) (*Package, map[string][]byte, error) {
	lim = lim.withDefaults()
	if size <= 0 {
		return nil, nil, errors.New("资产包为空")
	}

	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, nil, fmt.Errorf("不是合法的 zip 包: %w", err)
	}
	if len(zr.File) > lim.MaxFiles+1 { // +1 = package.json
		return nil, nil, fmt.Errorf("%w（上限 %d 个）", ErrTooManyFiles, lim.MaxFiles)
	}

	var (
		pkg   *Package
		files = make(map[string][]byte, len(zr.File))
		total int64
	)
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") { // 目录项，跳过
			continue
		}
		switch {
		case name == packageMetaName:
			b, rerr := readZipEntry(f, packageMetaMax)
			if rerr != nil {
				return nil, nil, rerr
			}
			var p Package
			if jerr := json.Unmarshal(b, &p); jerr != nil {
				return nil, nil, fmt.Errorf("%s 解析失败: %w", packageMetaName, jerr)
			}
			pkg = &p

		case strings.HasPrefix(name, packageFilesDir+"/"):
			rel := strings.TrimPrefix(name, packageFilesDir+"/")
			if _, rerr := safeRelPath(rel); rerr != nil {
				return nil, nil, rerr
			}
			if _, dup := files[rel]; dup {
				return nil, nil, fmt.Errorf("包内文件重复：%s", rel)
			}
			if len(files) >= lim.MaxFiles {
				return nil, nil, fmt.Errorf("%w（上限 %d 个）", ErrTooManyFiles, lim.MaxFiles)
			}
			if int64(f.UncompressedSize64) > lim.MaxFileSize {
				return nil, nil, fmt.Errorf("%w：%s（%d 字节，上限 %d）",
					ErrFileTooLarge, rel, f.UncompressedSize64, lim.MaxFileSize)
			}
			total += int64(f.UncompressedSize64)
			if total > lim.MaxTotal {
				return nil, nil, fmt.Errorf("包体超过合计上限（%d 字节，上限 %d）", total, lim.MaxTotal)
			}
			b, rerr := readZipEntry(f, lim.MaxFileSize)
			if rerr != nil {
				return nil, nil, rerr
			}
			files[rel] = b

		default:
			return nil, nil, fmt.Errorf("包内出现未知条目：%s", name)
		}
	}

	if pkg == nil {
		return nil, nil, fmt.Errorf("包内缺少 %s", packageMetaName)
	}
	if pkg.Format != PackageFormat {
		return nil, nil, fmt.Errorf("不支持的包格式：%s（当前支持 %s）", pkg.Format, PackageFormat)
	}
	if len(pkg.Payload) == 0 {
		pkg.Payload = json.RawMessage(`{}`)
	}
	if err := pkg.verify(files); err != nil {
		return nil, nil, err
	}
	return pkg, files, nil
}

// verify 清单与实际内容一一对应：数量、字节数、sha256 三项都对上才算可信。
func (p *Package) verify(files map[string][]byte) error {
	if len(p.Manifest.Files) == 0 {
		return fmt.Errorf("%w：清单为空", ErrNoFiles)
	}
	if len(p.Manifest.Files) != len(files) {
		return fmt.Errorf("清单声明 %d 个文件，包内实际 %d 个", len(p.Manifest.Files), len(files))
	}
	for _, f := range p.Manifest.Files {
		rel, err := safeRelPath(f.Path)
		if err != nil {
			return err
		}
		data, ok := files[rel]
		if !ok {
			return fmt.Errorf("清单里的文件在包内不存在：%s", rel)
		}
		if int64(len(data)) != f.Size {
			return fmt.Errorf("文件 %s 大小与清单不一致（清单 %d，实际 %d）", rel, f.Size, len(data))
		}
		if f.SHA256 != "" {
			sum := sha256.Sum256(data)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
				return fmt.Errorf("文件 %s 校验和不一致（包内容被改动过？）", rel)
			}
		}
	}
	return nil
}

// safeRelPath 校验包内相对路径。
//
// 只接受「规范化后的 / 分隔相对路径」：绝对路径、含 .. 段、含反斜杠、以及
// a/../b 这类需要 clean 才安全的写法一律拒绝 —— 这些正是解压穿越的常见手法。
func safeRelPath(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("包内出现空路径")
	}
	if strings.ContainsAny(rel, "\\\x00") {
		return "", fmt.Errorf("非法路径（含反斜杠或空字节）：%s", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("非法路径（绝对路径）：%s", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("非法路径（越出根目录）：%s", rel)
	}
	if clean != rel {
		return "", fmt.Errorf("非法路径（未规范化）：%s", rel)
	}
	return clean, nil
}

// readZipEntry 读一个条目，超限即报错（zip 声明的大小可造假，所以按实际读到的字节数再判一次）。
func readZipEntry(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("读取包内条目 %s: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, fmt.Errorf("读取包内条目 %s: %w", f.Name, err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("包内条目过大：%s（上限 %d 字节）", f.Name, max)
	}
	return b, nil
}
