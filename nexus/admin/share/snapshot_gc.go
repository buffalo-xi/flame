package share

import (
	"context"
	"path"
	"strconv"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"
)

// CleanupSnapshots sweeps the platform asset snapshot directories at startup (once). It collects this litter:
//
//   - asset directories with no row: left by an import / listing that crashed halfway, or by a deleted row whose DeletePrefix failed;
//   - snapshots older than the retention window;
//   - snapshots newer than the current one with no write for longer than snapshotPendingTTL
//     (files written, row never committed; freshly written ones are left alone - another instance may be publishing right now).
//
// It runs at startup only: at run time assetpack.PruneOldBuilds deletes precisely. A full sweep would race a
// listing in progress - "the row does not point at the new snapshot yet, the directory is already being written" is normal then, and a sweep would misread it as an orphan.
//
// When several instances share one storage the last rule is essential: another instance may be publishing at this
// very moment, and its new snapshot directory is "newer than the current build in the database" without being litter.
const snapshotPendingTTL = time.Hour

func CleanupSnapshots() {
	st, err := getStorage()
	if err != nil {
		xlog.Error("failed to clean asset snapshots (storage not ready): %v", err)
		return
	}
	// An empty filter = every status: drafts and delisted assets have a current snapshot too, so listed alone will not do
	assets, lerr := sharedassets.List(sharedassets.Filter{})
	if lerr != nil {
		xlog.Error("failed to clean asset snapshots (listing assets): %v", lerr)
		return
	}
	current := make(map[string]int, len(assets))
	for _, a := range assets {
		current[a.ID] = a.Build
	}
	orphans, builds := cleanupSnapshotDirs(context.Background(), st, current)
	xlog.Info("asset snapshot cleanup done: %d orphan directories, %d old snapshots", orphans, builds)
}

// cleanupSnapshotDirs is the storage-only part of CleanupSnapshots (no DB access, so it is easy to unit test).
func cleanupSnapshotDirs(ctx context.Context, st storage.Storage, current map[string]int) (orphans, builds int) {
	ids, derr := st.List(ctx, paths.PlatformAssetsSubdir)
	if derr != nil {
		// A fresh deployment has no such directory, and listing a missing "directory" on S3 can error too; both count as empty
		return 0, 0
	}
	for _, id := range ids {
		if !id.IsDir {
			continue
		}
		dir := path.Join(paths.PlatformAssetsSubdir, id.Name)
		cur, known := current[id.Name]
		if !known {
			if err := st.DeletePrefix(ctx, dir); err != nil {
				xlog.Error("failed to delete orphan snapshot %s: %v", dir, err)
				continue
			}
			orphans++
			continue
		}
		vers, verr := st.List(ctx, dir)
		if verr != nil {
			continue
		}
		for _, v := range vers {
			if !v.IsDir {
				continue
			}
			n, aerr := strconv.Atoi(v.Name)
			if aerr != nil {
				continue // not a snapshot directory (something placed by hand, say); do not guess
			}
			p := path.Join(dir, v.Name)
			switch {
			case n <= cur-assetpack.BuildsKept:
				// A snapshot that fell out of the retention window: certainly unread (install / export read the current snapshot only), so delete it.
			case n > cur:
				// Newer than the current build in the database: it may be litter from "files written, row never committed",
				// or **another instance publishing right now** (directory written, row not committed - a normal transient state).
				// The directory alone cannot tell them apart, so time decides: litter only if nothing was written for snapshotPendingTTL.
				ts, ok := storage.LastActivity(ctx, st, p, v)
				if !ok || time.Since(ts) < snapshotPendingTTL {
					continue
				}
			default:
				continue // still inside the retention window
			}
			if err := st.DeletePrefix(ctx, p); err != nil {
				xlog.Error("failed to delete snapshot directory %s: %v", p, err)
				continue
			}
			builds++
		}
	}
	return orphans, builds
}
