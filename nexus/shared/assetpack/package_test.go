package assetpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildZip 手工拼一个包（用于构造「恶意/坏包」，绕开 WritePackage 的自检）。
func buildZip(t *testing.T, entries map[string]string) ([]byte, int64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes(), int64(buf.Len())
}

func demoPackage() *Package {
	md := "# demo\n"
	lib := "console.log(1)\n"
	return &Package{
		Format:  PackageFormat,
		Origin:  PackageOrigin{Server: "test", AssetID: "a1", SourceSchema: "t0", Build: 2},
		Payload: json.RawMessage(`{"name":"demo","description":"d"}`),
		Manifest: Manifest{
			Kind:  KindSkill,
			Name:  "demo",
			Entry: "SKILL.md",
			Files: []FileEntry{
				{Path: "SKILL.md", Size: int64(len(md)), SHA256: sha(md)},
				{Path: "lib/run.js", Size: int64(len(lib)), SHA256: sha(lib)},
			},
		},
	}
}

func TestPackageRoundTrip(t *testing.T) {
	st := newStorage(t)
	dir := "platform-assets/a1/2"
	mustWrite(t, st, dir+"/SKILL.md", "# demo\n")
	mustWrite(t, st, dir+"/lib/run.js", "console.log(1)\n")

	var buf bytes.Buffer
	if err := WritePackage(context.Background(), st, dir, demoPackage(), &buf); err != nil {
		t.Fatalf("WritePackage: %v", err)
	}

	pkg, files, err := ReadPackage(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatalf("ReadPackage: %v", err)
	}
	if pkg.Manifest.Kind != KindSkill || pkg.Manifest.Name != "demo" {
		t.Fatalf("清单未还原：%+v", pkg.Manifest)
	}
	if pkg.Origin.Server != "test" || pkg.Origin.Build != 2 {
		t.Fatalf("溯源信息未还原：%+v", pkg.Origin)
	}
	if string(files["SKILL.md"]) != "# demo\n" || string(files["lib/run.js"]) != "console.log(1)\n" {
		t.Fatalf("文件内容不一致：%+v", files)
	}
	if !strings.Contains(string(pkg.Payload), `"name":"demo"`) {
		t.Fatalf("payload 未还原：%s", pkg.Payload)
	}
}

// 导出端就要挡住「清单与磁盘不一致」：宁可导不出去，也不要打一个内容对不上的包。
func TestWritePackageRejectsSnapshotMismatch(t *testing.T) {
	st := newStorage(t)
	dir := "platform-assets/a1/2"
	mustWrite(t, st, dir+"/SKILL.md", "改了内容")
	mustWrite(t, st, dir+"/lib/run.js", "console.log(1)\n")

	var buf bytes.Buffer
	err := WritePackage(context.Background(), st, dir, demoPackage(), &buf)
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("期望报大小不一致，实际：%v", err)
	}
}

// 内容被改动（sha256 对不上）→ 整包拒绝。
func TestReadPackageRejectsTamperedContent(t *testing.T) {
	pkg := demoPackage()
	raw, _ := json.Marshal(pkg)
	b, size := buildZip(t, map[string]string{
		"package.json":     string(raw),
		"files/SKILL.md":   "# demo\n",
		"files/lib/run.js": "console.log(2)\n", // 与清单 sha256 不符
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "校验和不一致") {
		t.Fatalf("期望报校验和不一致，实际：%v", err)
	}
}

// 解压穿越：files/../x 必须拒绝，而不是写到自己根目录之外。
func TestReadPackageRejectsTraversal(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())
	b, size := buildZip(t, map[string]string{
		"package.json":        string(raw),
		"files/../../evil.md": "x",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "非法路径") {
		t.Fatalf("期望报非法路径，实际：%v", err)
	}
}

// 缺文件 / 多文件都不接受：清单就是契约。
func TestReadPackageRejectsManifestMismatch(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())

	// 少一个文件
	b, size := buildZip(t, map[string]string{
		"package.json":   string(raw),
		"files/SKILL.md": "# demo\n",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "实际") {
		t.Fatalf("期望报数量不一致，实际：%v", err)
	}

	// 多一个未声明的文件
	b, size = buildZip(t, map[string]string{
		"package.json":     string(raw),
		"files/SKILL.md":   "# demo\n",
		"files/lib/run.js": "console.log(1)\n",
		"files/extra.txt":  "x",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil {
		t.Fatal("期望拒绝超出清单的文件")
	}
}

// 包内出现未知顶层条目 = 有人在往包里塞东西，直接拒。
func TestReadPackageRejectsUnknownEntry(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())
	b, size := buildZip(t, map[string]string{
		"package.json":   string(raw),
		"files/SKILL.md": "# demo\n",
		"README.md":      "多余的说明",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "未知条目") {
		t.Fatalf("期望报未知条目，实际：%v", err)
	}
}

// 体积闸：单文件超限在读取阶段就被拦（不依赖 zip 里声明的大小）。
func TestReadPackageEnforcesFileSize(t *testing.T) {
	pkg := demoPackage()
	big := strings.Repeat("a", 2048)
	pkg.Manifest.Files = []FileEntry{{Path: "SKILL.md", Size: int64(len(big)), SHA256: sha(big)}}
	raw, _ := json.Marshal(pkg)
	b, size := buildZip(t, map[string]string{
		"package.json":   string(raw),
		"files/SKILL.md": big,
	})
	_, _, err := ReadPackage(bytes.NewReader(b), size, Limits{MaxFiles: 10, MaxFileSize: 1024, MaxTotal: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("期望报超限，实际：%v", err)
	}
}
