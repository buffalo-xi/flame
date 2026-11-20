package storage

import (
	"context"
	"testing"
)

// Rename in the local implementation: both files and directories must move, and a sibling whose name shares the prefix
// must not be dragged along.
// This case also pins down the design premise of the S3 branch — a single file is identified by its exact key, never by
// scanning the prefix.
func TestFilesystemRename(t *testing.T) {
	st := NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	if err := st.WriteFile(ctx, "ws/a.md", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ctx, "ws/a.md.bak", []byte("BAK")); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(ctx, "ws/a.md", "ws/b.md"); err != nil {
		t.Fatalf("renaming a single file failed: %v", err)
	}
	if data, err := st.ReadFile(ctx, "ws/b.md"); err != nil || string(data) != "A" {
		t.Errorf("new path content = %q, err = %v", data, err)
	}
	if ex, _ := st.Exists(ctx, "ws/a.md"); ex {
		t.Error("the old path still exists")
	}
	if data, err := st.ReadFile(ctx, "ws/a.md.bak"); err != nil || string(data) != "BAK" {
		t.Errorf("a sibling sharing the prefix was dragged along: %q, err = %v", data, err)
	}

	// Directory: the whole tree moves and nothing is left behind.
	if err := st.WriteFile(ctx, "ws/dir/x.md", []byte("X")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ctx, "ws/dir/sub/y.md", []byte("Y")); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(ctx, "ws/dir", "ws/moved"); err != nil {
		t.Fatalf("renaming a directory failed: %v", err)
	}
	if data, err := st.ReadFile(ctx, "ws/moved/sub/y.md"); err != nil || string(data) != "Y" {
		t.Errorf("a subdirectory did not move along: %q, err = %v", data, err)
	}
	if entries, _ := st.List(ctx, "ws/dir"); len(entries) != 0 {
		t.Errorf("the old directory still holds %d entries", len(entries))
	}

	// A missing target parent is created automatically (the same semantics as creating an entry).
	if err := st.Rename(ctx, "ws/b.md", "ws/deep/er/c.md"); err != nil {
		t.Fatalf("moving into a missing parent failed: %v", err)
	}
	if ex, _ := st.Exists(ctx, "ws/deep/er/c.md"); !ex {
		t.Error("the multi-level destination was not created")
	}

	// A missing source: idempotently nil (the API layer reports 404 through its own entryExists, not from here).
	if err := st.Rename(ctx, "ws/nope", "ws/other"); err != nil {
		t.Errorf("a missing source should return nil, got %v", err)
	}
}
