package assetpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"flame/nexus/shared/storage"
)

// PackFiles copies the whole srcDir into dstDir and returns the file manifest (sorted by path, with
// sha256).
//
//	srcDir  tenants/<source>/skills|tools|agents/<name>
//	dstDir  platform-assets/<assetID>/<build>
//
// When entry is non-empty the source directory must contain that file (SKILL.md for a skill,
// AGENT.md for an agent, TOOL.json's entry for a tool), otherwise ErrMissingEntry is returned —
// "listed successfully but unusable after install" is worse than a failed listing.
//
// The gates are checked before copying: a file over the ceiling, too many files, or a total over the
// ceiling are all rejected **before writing**, so half a body never lands in the snapshot directory
// (the caller does DeletePrefix as a fallback, but there is no reason to create garbage).
func PackFiles(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, lim Limits) ([]FileEntry, error) {
	return PackFilesWith(ctx, st, srcDir, dstDir, entry, lim, PackOptions{})
}

// PackFilesWith is PackFiles plus the ability to skip some subdirectories and hang extra directory
// trees in (see PackOptions).
func PackFilesWith(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, lim Limits, opts PackOptions) ([]FileEntry, error) {
	lim = lim.withDefaults()

	var (
		files    []FileEntry
		total    int64
		hasEntry bool
	)

	// addTree copies one source directory wholesale under the in-package prefix, accumulating the
	// size gates and the manifest. An empty prefix means the main directory (entry is only validated
	// against it).
	addTree := func(dir, prefix string, skipDirs []string, required bool) error {
		ok, e := st.Exists(ctx, dir)
		if e != nil {
			return fmt.Errorf("failed to scan the asset directory: %w", e)
		}
		if !ok {
			if !required {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrNoFiles, dir)
		}

		skip := func(rel string) bool {
			for _, d := range skipDirs {
				if d = strings.Trim(strings.TrimSpace(d), "/"); d != "" &&
					(rel == d || strings.HasPrefix(rel, d+"/")) {
					return true
				}
			}
			return false
		}

		return walkFiles(ctx, st, dir, "", skip, func(rel string, size int64) error {
			outRel := rel
			if prefix != "" {
				outRel = path.Join(prefix, rel)
			}
			if size > lim.MaxFileSize {
				return fmt.Errorf("%w: %s (%d bytes, limit %d)", ErrFileTooLarge, outRel, size, lim.MaxFileSize)
			}
			if len(files) >= lim.MaxFiles {
				return fmt.Errorf("%w (already %d files, limit %d)", ErrTooManyFiles, len(files), lim.MaxFiles)
			}
			if total+size > lim.MaxTotal {
				return fmt.Errorf("%w (%d bytes so far, this file %d bytes, limit %d bytes)",
					ErrTotalTooLarge, total, size, lim.MaxTotal)
			}

			data, rerr := st.ReadFile(ctx, path.Join(dir, rel))
			if rerr != nil {
				return fmt.Errorf("failed to read %s: %w", rel, rerr)
			}
			if werr := st.WriteFile(ctx, path.Join(dstDir, outRel), data); werr != nil {
				return fmt.Errorf("failed to write the snapshot %s: %w", outRel, werr)
			}

			sum := sha256.Sum256(data)
			files = append(files, FileEntry{
				Path:   outRel,
				Size:   int64(len(data)),
				SHA256: hex.EncodeToString(sum[:]),
			})
			total += int64(len(data))
			if outRel == entry {
				hasEntry = true
			}
			return nil
		})
	}

	if err := addTree(srcDir, "", opts.SkipDirs, true); err != nil {
		return nil, err
	}
	for _, t := range opts.Trees {
		if err := addTree(t.SrcDir, strings.Trim(t.DstPrefix, "/"), nil, t.Required); err != nil {
			return nil, err
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoFiles, srcDir)
	}
	if entry != "" && !hasEntry {
		return nil, fmt.Errorf("%w: %s has no %s", ErrMissingEntry, srcDir, entry)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// BodyFiles picks the asset body out of the manifest (dropping the _deps/ attachments).
//
// Install has to handle the two separately: the body lands in the asset's own directory, the
// attachments land in custom_tools/.
func BodyFiles(files []FileEntry) []FileEntry {
	out := make([]FileEntry, 0, len(files))
	for _, f := range files {
		if isAttachment(f.Path) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Attachments extracts the in-package attachments, grouped by "attachment group name" (only custom
// tools today: the group name is the tool name).
//
// The returned FileEntry values **keep the full in-package path** (_deps/custom_tools/<name>/…):
// install writes them with RestoreFilesUnder using the same prefix, so callers never assemble paths
// themselves.
func Attachments(files []FileEntry) map[string][]FileEntry {
	prefix := BundledToolsPrefix + "/"
	out := map[string][]FileEntry{}
	for _, f := range files {
		if !isAttachment(f.Path) {
			continue
		}
		rel := strings.TrimPrefix(f.Path, prefix)
		name, rest, ok := strings.Cut(rel, "/")
		if !ok || name == "" || rest == "" {
			continue
		}
		out[name] = append(out[name], f)
	}
	return out
}

// ToolAttachmentPrefix is the in-package prefix of one attachment tool (for RestoreFilesUnder).
func ToolAttachmentPrefix(name string) string {
	return BundledToolsPrefix + "/" + name
}

func isAttachment(p string) bool {
	return p == BundledToolsPrefix || strings.HasPrefix(p, BundledToolsPrefix+"/")
}

// RestoreFiles copies the snapshot directory to the target directory according to the manifest and
// returns the files the manifest lists but the snapshot lacks.
//
// A missing file is **not an error**: a snapshot may have lost auxiliary files to an older snapshot
// or manual cleanup, and as long as the entry file is there the install still works; the caller
// folds missing into the response warnings as is (never swallow it silently). A content that
// disagrees with the manifest (sha256 mismatch) yields ErrChecksum — that is storage corruption or a
// concurrent write, and carrying on would only spread broken files into the target tenant.
func RestoreFiles(ctx context.Context, st storage.Storage, snapDir, dstDir string, files []FileEntry) (missing []string, err error) {
	return RestoreFilesUnder(ctx, st, snapDir, dstDir, "", files)
}

// RestoreFilesUnder is RestoreFiles, but strips prefix from every manifest path before writing.
//
// It is used for in-package attachments: the manifest says _deps/custom_tools/<name>/main.py while
// the target is custom_tools/<name>/main.py. An empty prefix is equivalent to RestoreFiles.
func RestoreFilesUnder(ctx context.Context, st storage.Storage, snapDir, dstDir, prefix string, files []FileEntry) (missing []string, err error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, f := range files {
		rel := f.Path
		if prefix != "" {
			if !strings.HasPrefix(f.Path, prefix) {
				continue // not under this prefix: not one of the files to write here
			}
			rel = strings.TrimPrefix(f.Path, prefix)
		}
		if rel == "" {
			continue
		}
		src := path.Join(snapDir, f.Path)
		ok, e := st.Exists(ctx, src)
		if e != nil {
			return missing, fmt.Errorf("failed to check the snapshot %s: %w", f.Path, e)
		}
		if !ok {
			missing = append(missing, f.Path)
			continue
		}

		data, e := st.ReadFile(ctx, src)
		if e != nil {
			return missing, fmt.Errorf("failed to read the snapshot %s: %w", f.Path, e)
		}
		if f.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != f.SHA256 {
				return missing, fmt.Errorf("%w: %s", ErrChecksum, f.Path)
			}
		}
		if e := st.WriteFile(ctx, path.Join(dstDir, rel), data); e != nil {
			return missing, fmt.Errorf("failed to write %s: %w", rel, e)
		}
	}
	return missing, nil
}

// walkFiles walks a directory recursively, handing the callback the relative path and file size
// (directories are not reported).
//
// Each level is sorted by name so the manifest and the traversal order are stable: the manifest goes
// into the DB and onto the review page, and listing the same content twice has to produce the same
// manifest (otherwise diffs and "did it change" decisions are pure noise).
func walkFiles(ctx context.Context, st storage.Storage, dir, rel string, skip func(rel string) bool, fn func(rel string, size int64) error) error {
	entries, err := st.List(ctx, dir)
	if err != nil {
		return fmt.Errorf("failed to list the directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	for _, e := range entries {
		childRel := path.Join(rel, e.Name)
		if skip != nil && skip(childRel) {
			continue
		}
		childKey := path.Join(dir, e.Name)
		if e.IsDir {
			if err := walkFiles(ctx, st, childKey, childRel, skip, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(childRel, e.Size); err != nil {
			return err
		}
	}
	return nil
}
