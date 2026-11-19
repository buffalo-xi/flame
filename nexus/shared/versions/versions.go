// Package versions is the shared core of entity versions (release snapshots).
//
// The three entity kinds (agent / skill / custom_tool) each have their own version table (whose columns mirror the entity table), and only
// these few things are identical, so they are collected here:
//   - error semantics: not released / the current version cannot be deleted / the version directory already exists / the note is too long
//   - snapshot: freeze the draft directory wholesale into a version directory and produce a files manifest (path / size / sha256)
//   - encoding and rewriting that manifest (a tool writes TOOL.json into the snapshot, and the manifest must follow)
//
// Workflows do not go through here: their body is the flows.diagram column, with no directory - see
// shared/repository/flows/versions.go.
package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/storage"
)

// ErrNoReleasedVersion means the entity has no released version (the version table has no is_current).
//
// A production entry (a direct call across MCP / RPC / an endpoint / a flow) must refuse on it and must not quietly fall back to the draft -
// that would make "production runs use the released version" meaningless.
// Debug entries (the editor debugger / a test run) read the draft and never come through here.
var ErrNoReleasedVersion = errors.New("no released version yet")

// ErrCannotDeleteCurrentVersion means the current version cannot be deleted.
//
// It is the definition source of production calls and the only record of "what is running in production". To delete it, switch to another
// version first - switching is a different action, and a delete should not also mean taking something offline.
var ErrCannotDeleteCurrentVersion = errors.New("the current version cannot be deleted; switch production to another version first")

// ErrVersionExists means the version number is taken (the version directory already exists).
//
// A version is an immutable snapshot and is never overwritten under any circumstance; seeing this means the numbering and the disk are out
// of sync, so stop and investigate rather than overwrite the old snapshot.
var ErrVersionExists = errors.New("that version number is taken (the version directory already exists); versions are immutable")

// MaxNoteLen is the length limit of a version note, matching the version tables' note varchar(200).
const MaxNoteLen = 200

// ErrNoteTooLong means the version note exceeds the column width.
var ErrNoteTooLong = fmt.Errorf("the version note is limited to %d characters", MaxNoteLen)

// NormalizeNote normalises a version note: trim the edges and validate the length.
//
// It deliberately does not truncate silently: a note exists for people to trace back, and quietly cutting off the second half would distort
// "why was this version released".
func NormalizeNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		return "", ErrNoteTooLong
	}
	return note, nil
}

// Snapshot freezes the draft directory into a version directory and returns the manifest (sorted by path ascending).
//
//	srcDir  the entity draft directory (tenants/<schema>/{agents|skills|tools}/<name>)
//	dstDir  the version directory (tenants/<schema>/versions/<kind>/<name>/v<n>)
//
// The size gate follows the same criterion as listing packaging (assetpack.DefaultLimits): the definition domain is text, and hitting the
// ceiling usually means someone dropped a dataset or an artefact into the directory.
//
// The caller owns failure cleanup (DeletePrefix dstDir) and the "target must not already exist" pre-check: this function only writes to
// disk and touches neither the database nor the overwrite decision.
func Snapshot(ctx context.Context, st storage.Storage, srcDir, dstDir, entry string, skipDirs []string) ([]assetpack.FileEntry, error) {
	if st == nil {
		return nil, errors.New("storage not ready")
	}
	return assetpack.PackFilesWith(ctx, st, srcDir, dstDir, entry, assetpack.DefaultLimits(),
		assetpack.PackOptions{SkipDirs: skipDirs})
}

// EnsureAbsent verifies that the version directory does not exist yet.
//
// Call it before copying: check first, copy second, so that "half of it is already written by the time the collision is noticed" cannot
// happen.
func EnsureAbsent(ctx context.Context, st storage.Storage, dir string) error {
	ex, err := st.Exists(ctx, dir)
	if err != nil {
		return err
	}
	if ex {
		return fmt.Errorf("%s: %w", dir, ErrVersionExists)
	}
	return nil
}

// PutEntry writes one piece of content into the manifest: an entry with the same path has its size / sha256 updated, otherwise one is
// appended.
//
// It exists for one reason only: a custom tool's version must carry a valid TOOL.json (written by the merged view when the draft lacks one
// or it is broken), and after that write the entry's digest in the manifest is stale - leaving it would turn into "the checksum does not
// match when installing / loading", which is harder to diagnose than a missing file.
func PutEntry(files []assetpack.FileEntry, rel string, data []byte) []assetpack.FileEntry {
	sum := sha256.Sum256(data)
	e := assetpack.FileEntry{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	for i := range files {
		if files[i].Path == rel {
			files[i] = e
			return files
		}
	}
	files = append(files, e)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// Encode turns a manifest into the JSON for a version row's files column.
func Encode(files []assetpack.FileEntry) ([]byte, error) {
	if files == nil {
		files = []assetpack.FileEntry{}
	}
	return json.Marshal(files)
}

// Decode turns a version row's files column back into a manifest (used to verify and restore when loading a version).
func Decode(raw []byte) ([]assetpack.FileEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var files []assetpack.FileEntry
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("failed to parse the version file manifest: %w", err)
	}
	return files, nil
}

// Summary returns the manifest's file count and total bytes (a list endpoint returns just these two numbers, not the whole manifest).
func Summary(files []assetpack.FileEntry) (count int, total int64) {
	for _, f := range files {
		total += f.Size
	}
	return len(files), total
}
