package versions

import (
	"strings"
	"testing"

	"flame/nexus/shared/assetpack"
)

// 说明过长要拒绝而不是静默截断：它是给人回溯「为什么发这一版」用的。
func TestNormalizeNote(t *testing.T) {
	if got, err := NormalizeNote("  修了个边界  "); err != nil || got != "修了个边界" {
		t.Fatalf("NormalizeNote = %q, %v", got, err)
	}
	if _, err := NormalizeNote(strings.Repeat("版", MaxNoteLen+1)); err != ErrNoteTooLong {
		t.Fatalf("超长应返回 ErrNoteTooLong，实际 %v", err)
	}
	// 多字节按字符计：200 个汉字正好压线
	if _, err := NormalizeNote(strings.Repeat("版", MaxNoteLen)); err != nil {
		t.Fatalf("压线长度不应报错: %v", err)
	}
}

// 工具发布时会往快照里补写 TOOL.json：清单里那一项的摘要必须跟着更新，
// 否则载入时 sha256 对不上，表现成「快照坏了」。
func TestPutEntryUpdatesExisting(t *testing.T) {
	files := []assetpack.FileEntry{
		{Path: "TOOL.json", Size: 2, SHA256: "old"},
		{Path: "main.py", Size: 10, SHA256: "py"},
	}
	out := PutEntry(files, "TOOL.json", []byte(`{"name":"calc"}`))
	if len(out) != 2 {
		t.Fatalf("同路径应替换而不是追加，实际 %d 条", len(out))
	}
	if out[0].Path != "TOOL.json" || out[0].SHA256 == "old" || out[0].Size != int64(len(`{"name":"calc"}`)) {
		t.Fatalf("TOOL.json 项未更新: %+v", out[0])
	}
	if out[1].Path != "main.py" {
		t.Fatalf("顺序或内容被破坏: %+v", out)
	}
}

func TestPutEntryAppendsSorted(t *testing.T) {
	files := []assetpack.FileEntry{{Path: "main.py", Size: 1, SHA256: "x"}}
	out := PutEntry(files, "TOOL.json", []byte("{}"))
	if len(out) != 2 || out[0].Path != "TOOL.json" {
		t.Fatalf("应追加并按 path 升序: %+v", out)
	}
}

func TestSummary(t *testing.T) {
	n, total := Summary([]assetpack.FileEntry{{Size: 3}, {Size: 4}})
	if n != 2 || total != 7 {
		t.Fatalf("Summary = %d, %d", n, total)
	}
}
