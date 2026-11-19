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

// buildZip assembles a package by hand (for building malicious / broken packages, bypassing
// WritePackage's own checks).
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
		t.Fatalf("the manifest was not restored: %+v", pkg.Manifest)
	}
	if pkg.Origin.Server != "test" || pkg.Origin.Build != 2 {
		t.Fatalf("the origin information was not restored: %+v", pkg.Origin)
	}
	if string(files["SKILL.md"]) != "# demo\n" || string(files["lib/run.js"]) != "console.log(1)\n" {
		t.Fatalf("file content mismatch: %+v", files)
	}
	if !strings.Contains(string(pkg.Payload), `"name":"demo"`) {
		t.Fatalf("the payload was not restored: %s", pkg.Payload)
	}
}

// The export side has to stop "manifest disagreeing with disk": better to fail the export than to
// hand out a package whose content does not match.
func TestWritePackageRejectsSnapshotMismatch(t *testing.T) {
	st := newStorage(t)
	dir := "platform-assets/a1/2"
	mustWrite(t, st, dir+"/SKILL.md", "changed content")
	mustWrite(t, st, dir+"/lib/run.js", "console.log(1)\n")

	var buf bytes.Buffer
	err := WritePackage(context.Background(), st, dir, demoPackage(), &buf)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want a size mismatch error, got: %v", err)
	}
}

// Modified content (sha256 mismatch) -> the whole package is rejected.
func TestReadPackageRejectsTamperedContent(t *testing.T) {
	pkg := demoPackage()
	raw, _ := json.Marshal(pkg)
	b, size := buildZip(t, map[string]string{
		"package.json":     string(raw),
		"files/SKILL.md":   "# demo\n",
		"files/lib/run.js": "console.log(2)\n", // does not match the manifest sha256
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want a checksum mismatch error, got: %v", err)
	}
}

// Archive slip: files/../x must be rejected rather than written outside the root.
func TestReadPackageRejectsTraversal(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())
	b, size := buildZip(t, map[string]string{
		"package.json":        string(raw),
		"files/../../evil.md": "x",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("want an invalid path error, got: %v", err)
	}
}

// Missing and extra files are both unacceptable: the manifest is the contract.
func TestReadPackageRejectsManifestMismatch(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())

	// One file short
	b, size := buildZip(t, map[string]string{
		"package.json":   string(raw),
		"files/SKILL.md": "# demo\n",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "declares") {
		t.Fatalf("want a count mismatch error, got: %v", err)
	}

	// One file too many, not declared
	b, size = buildZip(t, map[string]string{
		"package.json":     string(raw),
		"files/SKILL.md":   "# demo\n",
		"files/lib/run.js": "console.log(1)\n",
		"files/extra.txt":  "x",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil {
		t.Fatal("want the files beyond the manifest to be rejected")
	}
}

// An unknown top-level entry in the package means someone is stuffing things in: reject outright.
func TestReadPackageRejectsUnknownEntry(t *testing.T) {
	raw, _ := json.Marshal(demoPackage())
	b, size := buildZip(t, map[string]string{
		"package.json":   string(raw),
		"files/SKILL.md": "# demo\n",
		"README.md":      "extra notes",
	})
	if _, _, err := ReadPackage(bytes.NewReader(b), size, Limits{}); err == nil ||
		!strings.Contains(err.Error(), "unknown entry") {
		t.Fatalf("want an unknown entry error, got: %v", err)
	}
}

// The size gate: a file over the ceiling is caught while reading (it does not trust the size declared
// inside the zip).
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
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("want an over-limit error, got: %v", err)
	}
}
