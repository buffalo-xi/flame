package versioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/storage"
)

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DraftChanged is the criterion for "the current version + whether the draft has unreleased changes": getting the rule
// backwards in one direction speaks a lie — saying "released" while production runs the old one (dangerous), or saying
// "changed" when it actually matches (which only makes someone click release for nothing).
func TestDraftChanged(t *testing.T) {
	ctx := context.Background()
	st := storage.NewFilesystemStorage(t.TempDir())
	dir := "tenants/t0/skills/demo"

	write := func(rel, body string) {
		t.Helper()
		if err := st.WriteFile(ctx, dir+"/"+rel, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "a")
	write("docs/spec.md", "b")

	files := []assetpack.FileEntry{
		{Path: "SKILL.md", Size: 1, SHA256: digest("a")},
		{Path: "docs/spec.md", Size: 1, SHA256: digest("b")},
	}

	changed, err := DraftChanged(ctx, st, dir, files, nil)
	if err != nil {
		t.Fatalf("DraftChanged = %v", err)
	}
	if changed {
		t.Fatal("draft matching the file list should be false")
	}

	// Different content -> changed
	write("docs/spec.md", "b2")
	if changed, _ = DraftChanged(ctx, st, dir, files, nil); !changed {
		t.Fatal("changed file content must be true (otherwise the UI claims it is released)")
	}
	write("docs/spec.md", "b")

	// One extra file -> changed
	write("docs/extra.md", "x")
	if changed, _ = DraftChanged(ctx, st, dir, files, nil); !changed {
		t.Fatal("an extra file in the draft must be true")
	}
	if err := st.Delete(ctx, dir+"/docs/extra.md"); err != nil {
		t.Fatal(err)
	}

	// One missing file (in the list, not in the draft) -> changed
	removed := files[:1]
	if changed, _ = DraftChanged(ctx, st, dir, removed, nil); !changed {
		t.Fatal("a file listed in the version but missing from the draft must be true")
	}

	// Empty list + empty directory -> matching
	empty := storage.NewFilesystemStorage(t.TempDir())
	if changed, _ = DraftChanged(ctx, empty, "tenants/t0/tools/none", nil, nil); changed {
		t.Fatal("both sides empty should be false")
	}

	// skipDirs: an excluded directory neither takes part in the comparison nor counts as an extra file
	write("skills/old/SKILL.md", "deprecated")
	withSkip := append([]assetpack.FileEntry{}, files...)
	changed, err = DraftChanged(ctx, st, dir, withSkip, []string{"skills/old"})
	if err != nil {
		t.Fatalf("DraftChanged = %v", err)
	}
	if changed {
		t.Fatal("an excluded directory must not count as changed (otherwise a deprecated private skill keeps the agent showing unreleased changes forever)")
	}
}
