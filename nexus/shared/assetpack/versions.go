package assetpack

import (
	"context"
	"fmt"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// maxBuildProbe is how many numbers are probed at most when allocating one. Listing and importing
// are low-frequency manual operations, so hitting 16 numbers in a row can only mean a pile of debris
// on disk (the startup sweep collects it); making the user retry beats digging further.
const maxBuildProbe = 16

// FreeBuild finds a number starting at from whose snapshot directory holds no content yet.
//
// Why it is needed: listing and importing both do "read build from the row -> +1 -> write files
// under platform-assets/<id>/<build>" with no mutual exclusion. Concurrent re-listing (a double
// click, two admins at once) computes the same number, the two sets of content interleave inside one
// directory, and the row only remembers one manifest — then the sha256 check in RestoreFiles reports
// "the package content was modified" without pointing at the real cause. Skipping a number is safe:
// build only grows, and a gap carries no meaning.
//
// The caller must serialise the whole "allocate -> write files" section (both call sites hold a
// package-level lock): this function only guarantees that the probe saw an empty directory, it does
// not reserve the number. The cross-process (multi-replica) collision window remains and has to be
// closed with a DB row lock; this closes the most common path, "clicked twice inside one process".
func FreeBuild(ctx context.Context, st storage.Storage, assetID string, from int) (int, error) {
	if from < 1 {
		from = 1
	}
	for n := from; n < from+maxBuildProbe; n++ {
		// List rather than Exists: on S3 a "directory" is only a prefix and a HEAD finds nothing,
		// while a local List errors out when the directory does not exist — both count as a free
		// number. When writing really fails, PackFiles / WriteFile report it themselves, so probing
		// must not block listing.
		if entries, err := st.List(ctx, paths.PlatformAssetKey(assetID, n)); err != nil || len(entries) == 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%d consecutive snapshot numbers are taken (asset %s, starting at v%d); retry later",
		maxBuildProbe, assetID, from)
}

// BuildsKept is how many snapshots are kept per asset (including the current one; 1 = only the
// current one).
//
// Snapshots only serve the "current snapshot": install copies from the snapshot into the target
// tenant (a copy, not a reference — see rule 1 under admin/share), export only packages the current
// snapshot, and there is no "roll back to an older version" channel. Older snapshot directories are
// therefore dead data, and every release adds another full image (up to 128 MiB per version, see
// DefaultLimits), so the disk would only grow monotonically.
//
// Raise it to 2 if you want room to roll back one bad release.
const BuildsKept = 1

// PruneOldBuilds deletes the snapshot directory that just fell out of the retention window.
//
// It is shared by listing and importing: both do "build+1 on the same row, content laid into the new
// directory", and the old directory has no readers either way. It must be called **after** the row
// points at the new snapshot — commit the row first, then delete files, so there is no window in
// which "the row points at an empty snapshot".
//
// Only one directory is deleted precisely, with no scan: this is the hot path, and a scan would race
// with another concurrent listing over the same tree (where "the row does not point at the new
// snapshot yet and the directory is already being written" is normal). Debris that piled up
// historically is collected by the startup sweep.
//
// The caller decides what to do with the returned error: a failed delete must not fail listing or
// importing, since the content is already in place.
func PruneOldBuilds(ctx context.Context, st storage.Storage, assetID string, current int) error {
	old := current - BuildsKept
	if old < 1 {
		return nil
	}
	return st.DeletePrefix(ctx, paths.PlatformAssetKey(assetID, old))
}
