package agents

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
)

// ============================================================================
// Artifacts produced by the current turn
//
// Scope: report the objective fact of "which workspace files this turn touched",
// and make no value judgement. Deciding which of them are deliverables belongs
// to frontend filtering or to a later model report - the order cannot be flipped.
//
// Why scan the directory instead of letting every tool report its writes:
//   - write paths such as `python gen.py > out.csv` inside exec / code nodes are
//     implicit and never appear in the tool arguments - only a scan sees them;
//   - the workspace is the single landing zone for artifacts, so one scan covers
//     every source;
//   - reporting from the sandbox side would mean touching codehost and three mode
//     chains, far more cost than benefit.
//
// The price is one LIST per turn. To keep a packed session directory from turning
// a single reply into dozens of LISTs, this reuses the "recent files" convergence
// policy: scan the root plus one level, cap the entry count, skip dependency dirs.
// ============================================================================

const (
	// maxArtifactScanEntries bounds a single scan (same order of magnitude as maxRecentScanEntries).
	maxArtifactScanEntries = 300
	// maxRunArtifacts caps the artifacts sent with RUN_FINISHED: the frontend area is
	// "what this turn produced", not a file manager, so the rest is left to the workspace.
	maxRunArtifacts = 50
	// artifactClockSkew is the backwards window. Files written a few milliseconds before
	// RUN_STARTED, or coarse filesystem timestamps (S3 is second-granular), would be
	// missed by a hard threshold.
	artifactClockSkew = 2 * time.Second
)

// artifactSkipDirs are never artifacts even when modified: dependency / cache / VCS dirs.
// Names match by directory name (level is ignored); a hit skips the whole subtree.
var artifactSkipDirs = map[string]struct{}{
	"node_modules":  {},
	"__pycache__":   {},
	".venv":         {},
	"venv":          {},
	".git":          {},
	".cache":        {},
	".pytest_cache": {},
	"dist":          {},
	".next":         {},
	"site-packages": {},
	".mypy_cache":   {},
	".ruff_cache":   {},
}

// Artifact is one file this run wrote inside the workspace.
type Artifact struct {
	Path    string    `json:"path"`     // workspace-relative path (the frontend opens it directly)
	Name    string    `json:"name"`     //
	Size    int64     `json:"size"`     //
	ModTime time.Time `json:"mod_time"` // last write time
	Action  string    `json:"action"`   // always modified: only "written" is reported, create and overwrite are not distinguished
}

// runArtifacts collects the artifacts of one turn. It always returns an empty slice on
// failure: artifacts are a bonus, and must never turn an otherwise successful turn into
// an error.
//
// since is the RUN_STARTED timestamp; an empty sessionID (which should not happen)
// returns empty right away.
func runArtifacts(schema, userID, sessionID string, since time.Time) []Artifact {
	// An empty slice rather than nil: it serialises to [], so the frontend needs no null path.
	if sessionID == "" || userID == "" {
		return []Artifact{}
	}
	st, err := getStorage()
	if err != nil {
		return []Artifact{}
	}
	wsKey := paths.UserSessionKey(schema, userID, sessionID)
	return scanArtifacts(context.Background(), st, wsKey, since, maxRunArtifacts)
}

// scanArtifacts scans the workspace for files with mod_time >= since.
// It uses the same convergence policy as RecentSessionFiles (root plus one level, capped count).
func scanArtifacts(ctx context.Context, st storage.Storage, wsKey string, since time.Time, limit int) []Artifact {
	if limit <= 0 {
		limit = maxRunArtifacts
	}
	threshold := since.Add(-artifactClockSkew)

	out := make([]Artifact, 0, 8)
	scanned := 0

	var collect func(dir, prefix string, depth int)
	collect = func(dir, prefix string, depth int) {
		if len(out) >= limit || scanned >= maxArtifactScanEntries {
			return
		}
		entries, lerr := st.List(ctx, dir)
		if lerr != nil {
			return // missing directory / read failure: treat this level as empty
		}
		for _, e := range entries {
			if len(out) >= limit || scanned >= maxArtifactScanEntries {
				return
			}
			if strings.HasPrefix(e.Name, ".") { // hidden files (including .keep placeholders) are not artifacts
				continue
			}
			scanned++
			rel := path.Join(prefix, e.Name)
			if e.IsDir {
				if depth == 0 {
					if _, skip := artifactSkipDirs[e.Name]; !skip {
						collect(path.Join(dir, e.Name), rel, 1)
					}
				}
				continue
			}
			// A zero time only shows up for S3 CommonPrefixes (directories); every object has
			// LastModified. When it does happen, under-reporting beats over-reporting - otherwise
			// every turn would list the whole history again.
			if e.ModTime.IsZero() || e.ModTime.Before(threshold) {
				continue
			}
			out = append(out, Artifact{
				Path:    rel,
				Name:    e.Name,
				Size:    e.Size,
				ModTime: e.ModTime,
				Action:  "modified",
			})
		}
	}
	collect(wsKey, "", 0)

	// Newest first; ties on the same second fall back to path so the order is stable (no jumping UI)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].Path < out[j].Path
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out
}
