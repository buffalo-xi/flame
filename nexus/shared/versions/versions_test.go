package versions

import (
	"strings"
	"testing"

	"flame/nexus/shared/assetpack"
)

// An over-long note must be refused rather than silently truncated: it exists so people can trace back "why was this version released".
func TestNormalizeNote(t *testing.T) {
	if got, err := NormalizeNote("  修了个边界  "); err != nil || got != "修了个边界" {
		t.Fatalf("NormalizeNote = %q, %v", got, err)
	}
	if _, err := NormalizeNote(strings.Repeat("版", MaxNoteLen+1)); err != ErrNoteTooLong {
		t.Fatalf("an over-long note should return ErrNoteTooLong, got %v", err)
	}
	// Multi-byte characters count as characters: 200 of them sit exactly on the line
	if _, err := NormalizeNote(strings.Repeat("版", MaxNoteLen)); err != nil {
		t.Fatalf("exactly the limit should not error: %v", err)
	}
}

// Publishing a tool writes TOOL.json into the snapshot, so that entry's digest in the manifest must be updated too - otherwise the sha256
// does not match when loading, which shows up as "the snapshot is broken".
func TestPutEntryUpdatesExisting(t *testing.T) {
	files := []assetpack.FileEntry{
		{Path: "TOOL.json", Size: 2, SHA256: "old"},
		{Path: "main.py", Size: 10, SHA256: "py"},
	}
	out := PutEntry(files, "TOOL.json", []byte(`{"name":"calc"}`))
	if len(out) != 2 {
		t.Fatalf("the same path should be replaced rather than appended, got %d entries", len(out))
	}
	if out[0].Path != "TOOL.json" || out[0].SHA256 == "old" || out[0].Size != int64(len(`{"name":"calc"}`)) {
		t.Fatalf("the TOOL.json entry was not updated: %+v", out[0])
	}
	if out[1].Path != "main.py" {
		t.Fatalf("the order or content was damaged: %+v", out)
	}
}

func TestPutEntryAppendsSorted(t *testing.T) {
	files := []assetpack.FileEntry{{Path: "main.py", Size: 1, SHA256: "x"}}
	out := PutEntry(files, "TOOL.json", []byte("{}"))
	if len(out) != 2 || out[0].Path != "TOOL.json" {
		t.Fatalf("it should append and keep the order by path ascending: %+v", out)
	}
}

func TestSummary(t *testing.T) {
	n, total := Summary([]assetpack.FileEntry{{Size: 3}, {Size: 4}})
	if n != 2 || total != 7 {
		t.Fatalf("Summary = %d, %d", n, total)
	}
}
