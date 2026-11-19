package assetpack

import (
	"context"
	"strconv"
	"testing"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// The precise delete on the listing / import path: only the one snapshot that just fell out of the
// window is removed, the current one is never touched.
//
// This invariant carries the whole "disk does not grow with the number of snapshots" policy: every
// listing bumps build by one, so exactly one snapshot drops out each time, deleting one is enough,
// and no scan is needed.
func TestPruneOldBuilds(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	mustWrite(t, st, paths.PlatformAssetKey("a1", 2)+"/SKILL.md", "x\n")
	mustWrite(t, st, paths.PlatformAssetKey("a1", 3)+"/SKILL.md", "x\n")

	// First listing (v1): nothing has left the window, so no directory may be touched
	if err := PruneOldBuilds(ctx, st, "a1", 1); err != nil {
		t.Fatalf("PruneOldBuilds(v1): %v", err)
	}
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 2), true)

	// Re-release to v3: v2 leaves the window and is deleted, v3 stays
	if err := PruneOldBuilds(ctx, st, "a1", 3); err != nil {
		t.Fatalf("PruneOldBuilds(v3): %v", err)
	}
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 2), false)
	mustExistCase(t, st, paths.PlatformAssetKey("a1", 3), true)
}

// Number allocation must skip a snapshot directory that already holds content: a concurrent
// re-listing, or a previous run that crashed half way, leaves a number that must not be reused —
// reuse squeezes two bodies into one tree while the row remembers only one manifest.
func TestFreeBuildSkipsOccupied(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()

	// A brand-new asset: no directory at all, so the starting number is used as is
	if n, err := FreeBuild(ctx, st, "a2", 1); err != nil || n != 1 {
		t.Fatalf("FreeBuild(empty asset) = %d, %v, want 1", n, err)
	}

	// 1 and 2 are taken: skip them and get 3
	mustWrite(t, st, paths.PlatformAssetKey("a2", 1)+"/SKILL.md", "x\n")
	mustWrite(t, st, paths.PlatformAssetKey("a2", 2)+"/SKILL.md", "x\n")
	if n, err := FreeBuild(ctx, st, "a2", 1); err != nil || n != 3 {
		t.Fatalf("FreeBuild(1,2 taken) = %d, %v, want 3", n, err)
	}
	// Starting in the middle: 5 is free, so 5 is used without counting back up
	if n, err := FreeBuild(ctx, st, "a2", 5); err != nil || n != 5 {
		t.Fatalf("FreeBuild(from=5) = %d, %v, want 5", n, err)
	}

	// Hitting the probe ceiling: fail, never silently reuse an existing directory
	for i := 1; i <= maxBuildProbe; i++ {
		mustWrite(t, st, paths.PlatformAssetKey("a3", i)+"/SKILL.md", "x\n")
	}
	if _, err := FreeBuild(ctx, st, "a3", 1); err == nil {
		t.Fatal("allocating with every probe taken must fail instead of reusing an existing directory")
	}
}

// The default retention window is "current snapshot only". Changing the policy breaks this test
// first, which is the reminder to update the cases and the snapshot documentation instead of letting
// disk quietly keep a pile of dead snapshots.
func TestBuildsKeptDefault(t *testing.T) {
	if BuildsKept != 1 {
		t.Fatalf("BuildsKept = %d, want 1", BuildsKept)
	}
}

func mustExistCase(t *testing.T, st storage.Storage, key string, want bool) {
	t.Helper()
	got, err := st.Exists(context.Background(), key)
	if err != nil {
		t.Fatalf("Exists(%s): %v", key, err)
	}
	if got != want {
		t.Fatalf("existence of %s = %v, want %v (%s)", key, got, want, strconv.Quote(key))
	}
}
