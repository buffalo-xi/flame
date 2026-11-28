package versions

import (
	"context"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// Deleting the last version must not leave versions/<kind>/<name> behind: the row is gone, so the path has to go too.
func TestPruneEmptyEntityDir(t *testing.T) {
	ctx := context.Background()
	st := storage.NewFilesystemStorage(t.TempDir())
	dir := paths.VersionEntityKey("t0", paths.VersionSkills, "joke-writing")

	// A directory that does not exist is not an error: deleting versions is idempotent.
	PruneEmptyEntityDir(ctx, st, dir)

	for _, p := range []string{"v1/SKILL.md", "v2/SKILL.md"} {
		if err := st.WriteFile(ctx, dir+"/"+p, []byte("# "+p)); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	// A directory that still holds a version is exactly what a release concurrent with the delete looks like: it must survive.
	if err := st.DeletePrefix(ctx, dir+"/v1"); err != nil {
		t.Fatalf("delete v1: %v", err)
	}
	PruneEmptyEntityDir(ctx, st, dir)
	if ex, err := st.Exists(ctx, dir); err != nil || !ex {
		t.Fatalf("%s must survive while it still holds a version (exists=%v err=%v)", dir, ex, err)
	}

	// The last version is gone: the entity directory goes with it, and the kind root stays.
	if err := st.DeletePrefix(ctx, dir+"/v2"); err != nil {
		t.Fatalf("delete v2: %v", err)
	}
	PruneEmptyEntityDir(ctx, st, dir)
	if ex, err := st.Exists(ctx, dir); err != nil || ex {
		t.Fatalf("%s should have been removed (exists=%v err=%v)", dir, ex, err)
	}
	if ex, _ := st.Exists(ctx, paths.VersionsRoot("t0")+"/skills"); !ex {
		t.Fatalf("the kind root must stay: it is structural, not an entity")
	}
}

// plainStorage stands in for a backend without directories (S3): it has the interface but not the optional capability.
type plainStorage struct{ storage.Storage }

// Without the capability the call is a no-op rather than an error - S3 has nothing to remove, and a caller must not have to
// branch on the driver.
func TestPruneEmptyEntityDirSkipsBackendWithoutCapability(t *testing.T) {
	ctx := context.Background()
	st := storage.NewFilesystemStorage(t.TempDir())
	dir := paths.VersionEntityKey("t0", paths.VersionTools, "calc")

	if err := st.WriteFile(ctx, dir+"/v1/TOOL.json", []byte("{}")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := st.DeletePrefix(ctx, dir+"/v1"); err != nil {
		t.Fatalf("delete v1: %v", err)
	}

	PruneEmptyEntityDir(ctx, plainStorage{st}, dir)
	if ex, err := st.Exists(ctx, dir); err != nil || !ex {
		t.Fatalf("a backend without the capability must be left alone (exists=%v err=%v)", ex, err)
	}
}
