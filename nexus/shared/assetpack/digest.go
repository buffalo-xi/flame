package assetpack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ManifestSpec 当前 manifest / payload 的**语义**版本。
//
// 与 PackageFormat 的分工：
//   - PackageFormat（flame-asset/1）只保证「这个 zip 的容器与元信息结构能被解析」；
//   - ManifestSpec 管「解析出来的字段我认不认识」：给 manifest 或 payload 加字段、加枚举值、
//     改语义时都要 +1，导入端据此对「比本程序更新」的包明确拒绝，而不是把不认识的字段
//     静默丢掉、装出一个行为不对的实体。
//
// 判据：spec 缺省（老包没有这个字段）按 1 处理；spec > ManifestSpec → 拒绝安装。
const ManifestSpec = 1

// StampManifest 补齐 Spec 与 Digest（幂等：已有的 Digest 在内容未变时保持不变）。
//
// 上架、导出、跨站拉取三条产出资产包的路径都必须调它 —— 否则同一个包在不同路径下
// 指纹不一致，「内容是否相同」的判断立刻失真。
func StampManifest(m *Manifest, payload []byte) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.Spec <= 0 {
		m.Spec = ManifestSpec
	}
	d, err := digestOf(*m, payload)
	if err != nil {
		return err
	}
	m.Digest = d
	return nil
}

// digestOf 算内容指纹：payload（归一化 JSON）+ 依赖声明 + 文件清单（按 path 排序）。
//
// 刻意把「名字 / 依赖 / 文件路径与摘要」都算进去，而**不**算时间、来源站点、build 号 ——
// 那些一变就换指纹的话，「同一份内容重新上架」也会被当成新内容。
// 归一化 JSON 是关键：payload 是 map，直接 Marshal 会按 Go 的键序输出，语义相同但字节不同的
// 两份 payload 必须得到同一个指纹。
func digestOf(m Manifest, payload []byte) (string, error) {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return "", fmt.Errorf("payload 不是合法 JSON: %w", err)
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("payload 归一化失败: %w", err)
	}
	deps, err := json.Marshal(normalizeDeps(m.Deps))
	if err != nil {
		return "", fmt.Errorf("依赖声明序列化失败: %w", err)
	}

	h := sha256.New()
	fmt.Fprintf(h, "flame-asset-digest/1\x00kind=%s\x00name=%s\x00entry=%s\x00", m.Kind, m.Name, m.Entry)
	h.Write(canonical)
	h.Write([]byte{0})
	h.Write(deps)

	// 文件清单按 path 排序再算：目录遍历顺序不同不代表内容不同
	files := append([]FileEntry(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		fmt.Fprintf(h, "\x00%s\x00%d\x00%s", f.Path, f.Size, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeDeps 把依赖声明排成稳定顺序：Tools / Skills / Agents / Requires 语义上都是集合，
// 声明顺序不同不代表内容不同。只在副本上排序 —— manifest 要按原样存库与展示。
func normalizeDeps(d Deps) Deps {
	d.Tools = append([]ToolDep(nil), d.Tools...)
	sort.Slice(d.Tools, func(i, j int) bool {
		a, b := d.Tools[i], d.Tools[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.GrantKind < b.GrantKind
	})
	d.Skills = sortedCopy(d.Skills)
	d.PrivateSkills = sortedCopy(d.PrivateSkills)
	d.Agents = sortedCopy(d.Agents)
	d.Requires = sortedCopy(d.Requires)
	d.GlobalVars = sortedCopy(d.GlobalVars)
	return d
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

// ShortDigest 指纹的展示形态（前 8 位）。空指纹返回空串：老包没有指纹，别显示成 "00000000"。
func ShortDigest(d string) string {
	if len(d) < 8 {
		return d
	}
	return d[:8]
}
