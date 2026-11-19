package agent

import (
	"context"
	"path"
	"time"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/tenants"
	"flame/tools/xlog"
)

// runsTTL is how long a temporary run workspace is kept: a run directory older than this counts as "left over from last time".
//
// Why time instead of ownership: with several instances sharing one storage (NFS / S3) there is no reliable evidence whether a directory
// still has an active run **on another instance** - liveness is only known to this process's runRegistry. 24 hours is far longer than a
// single run (multi-round tool calls and long reasoning included), so a workspace in normal use is never touched, and only those left
// behind by a crash / a missed delete are reclaimed.
const runsTTL = 24 * time.Hour

// CleanupOrphanRuns removes the temporary directories left behind by session-less calls (called once at startup).
//
// What changed from before: runs/ is no longer deleted wholesale. With several instances sharing one storage that would also delete
// workspaces **currently in use** by other instances; now only run directories whose last activity is older than runsTTL are deleted.
//
// Directory shapes:
//
//	tenants/{schema}/users/{uid}/runs/{run_id}/   temporary workspace with a user
//	tenants/{schema}/runs/{run_id}/               fallback workspace without a user
func CleanupOrphanRuns() {
	if agentStorage == nil {
		return
	}
	schemas, err := tenants.ListSchemas()
	if err != nil {
		xlog.Error("failed to clean temporary run directories (listing tenants): %v", err)
		return
	}
	ctx := context.Background()
	for _, sc := range schemas {
		// Tenant-level fallback directory
		cleanupExpiredRuns(ctx, path.Join(paths.TenantsSubdir, sc, paths.RunsSubdir))

		// User-level temporary directory: users/{uid}/runs/
		usersDir := path.Join(paths.TenantsSubdir, sc, paths.UsersSubdir)
		users, err := agentStorage.List(ctx, usersDir)
		if err != nil {
			// This tenant has no user directory yet: not an error. Other errors are logged and skipped.
			xlog.Error("failed to list user directories schema=%s: %v", sc, err)
			continue
		}
		for _, u := range users {
			if !u.IsDir {
				continue
			}
			cleanupExpiredRuns(ctx, path.Join(usersDir, u.Name, paths.RunsSubdir))
		}
	}
	xlog.Info("temporary run directory cleanup finished (workspaces older than %s)", runsTTL)
}

// cleanupExpiredRuns deletes the run directories under dir whose last activity is older than runsTTL.
// It returns silently when dir does not exist (a fresh deployment / no session-less call ever happened).
func cleanupExpiredRuns(ctx context.Context, dir string) {
	entries, err := agentStorage.List(ctx, dir)
	if err != nil {
		return
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		p := path.Join(dir, e.Name)
		ts, ok := storage.LastActivity(ctx, agentStorage, p, e)
		if !ok {
			// No timestamp, no delete: leaving garbage behind beats deleting a possibly active workspace on a guess.
			continue
		}
		if time.Since(ts) < runsTTL {
			continue
		}
		if err := agentStorage.DeletePrefix(ctx, p); err != nil {
			xlog.Error("failed to clean temporary run directory %s: %v", p, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		xlog.Info("temporary run directory cleanup %s: removed %d directories older than %s", dir, removed, runsTTL)
	}
}
