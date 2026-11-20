package assetpack

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/storage"
)

// An asset package: one file (a zip) that carries a reviewed snapshot across servers.
//
// Why a package rather than a remote reference: distribution means copying — once installed, the
// entity has no further tie to its source. It is the same across servers: the package carries
// everything needed to rebuild a local snapshot, so whether the peer later goes down is irrelevant
// here.
//
//	package.json  { format, exported_at, origin, manifest, payload }
//	files/<rel>   every file of the snapshot (relative paths preserved verbatim)
//
// The package deliberately **omits**: grant rows of the tenant schema, and agent_relations (uuid
// relations that cannot be remapped across environments). An agent's definition attributes
// (is_supervisor / tools_unrestricted / skill_discovery / default_language) **are in the payload**,
// because they describe how the entity runs and the importing side applies them from the package;
// `endpoints` belong to the target tenant and do not travel.
const (
	// PackageFormat identifies the package format. A change of value is a breaking change (the
	// importing side rejects old or new formats based on it).
	PackageFormat = "flame-asset/1"

	packageMetaName = "package.json"
	packageFilesDir = "files"
	// Metadata ceiling: the payload is a snapshot of one entity row, a few KB in practice; 1 MiB
	// is already very generous.
	packageMetaMax = 1 << 20
)

// PackageOrigin is the exporter's traceability information. The importing side only writes it into
// the origin_* columns for display and deduplication; it is never a runtime reference (the peer
// dropping its database or renaming does not affect assets already imported here).
type PackageOrigin struct {
	Server       string `json:"server"`                  // exporter site name (config server.name)
	AssetID      string `json:"asset_id"`                // the exporter's asset id (traceability only)
	SourceSchema string `json:"source_schema,omitempty"` // exporter tenant schema
	Build        int    `json:"build"`                   // the snapshot number the exporter held
	ListedBy     string `json:"listed_by,omitempty"`     // display-name snapshot of who listed it
}

// Package is the in-package metadata (the content of package.json).
type Package struct {
	Format     string          `json:"format"`
	ExportedAt time.Time       `json:"exported_at"`
	Origin     PackageOrigin   `json:"origin"`
	Manifest   Manifest        `json:"manifest"`
	Payload    json.RawMessage `json:"payload"`
}

// WritePackage writes a snapshot directory out as a zip stream (streamed, files are never read into
// memory).
//
// snapDir is the logical key platform-assets/<id>/<build>; every file is read back according to
// manifest.Files and its size checked against the manifest — the export is "the content the
// manifest describes", not "whatever the directory happens to hold".
func WritePackage(ctx context.Context, st storage.Storage, snapDir string, pkg *Package, w io.Writer) error {
	if pkg == nil {
		return errors.New("package is nil")
	}
	if pkg.Format == "" {
		pkg.Format = PackageFormat
	}
	if pkg.Format != PackageFormat {
		return fmt.Errorf("unsupported package format: %s", pkg.Format)
	}
	if len(pkg.Manifest.Files) == 0 {
		return fmt.Errorf("%w: the manifest is empty", ErrNoFiles)
	}
	if len(pkg.Payload) == 0 {
		pkg.Payload = json.RawMessage(`{}`)
	}
	if pkg.ExportedAt.IsZero() {
		pkg.ExportedAt = time.Now()
	}

	meta, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("failed to serialise the package metadata: %w", err)
	}

	zw := zip.NewWriter(w)
	fw, err := zw.CreateHeader(&zip.FileHeader{
		Name: packageMetaName, Method: zip.Deflate, Modified: pkg.ExportedAt,
	})
	if err != nil {
		return err
	}
	if _, err := fw.Write(meta); err != nil {
		return err
	}

	now := pkg.ExportedAt
	for _, f := range pkg.Manifest.Files {
		rel, rerr := safeRelPath(f.Path)
		if rerr != nil {
			return rerr
		}
		rc, oerr := st.ReadStream(ctx, path.Join(snapDir, rel))
		if oerr != nil {
			return fmt.Errorf("failed to read the snapshot file %s: %w", rel, oerr)
		}
		entry, cerr := zw.CreateHeader(&zip.FileHeader{
			Name: packageFilesDir + "/" + rel, Method: zip.Deflate, Modified: now,
		})
		if cerr != nil {
			_ = rc.Close()
			return cerr
		}
		n, cpErr := io.Copy(entry, rc)
		_ = rc.Close()
		if cpErr != nil {
			return fmt.Errorf("failed to write the package entry %s: %w", rel, cpErr)
		}
		if n != f.Size {
			return fmt.Errorf("the snapshot file %s does not match the manifest (manifest %d bytes, actual %d)", rel, f.Size, n)
		}
	}
	return zw.Close()
}

// ReadPackage reads a package back and **fully validates** it: size / file-count gates, path
// safety, manifest and content matching one to one, sha256.
//
// All validation happens here so the caller receives (pkg, files) and only has to persist them —
// this is where "never trust the upstream" lands: if a peer says the package holds 3 files, the
// actual content is compared entry by entry, and any mismatch rejects the whole package (there is
// no partial import).
func ReadPackage(ra io.ReaderAt, size int64, lim Limits) (*Package, map[string][]byte, error) {
	lim = lim.withDefaults()
	if size <= 0 {
		return nil, nil, errors.New("the asset package is empty")
	}

	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, nil, fmt.Errorf("not a valid zip archive: %w", err)
	}
	if len(zr.File) > lim.MaxFiles+1 { // +1 = package.json
		return nil, nil, fmt.Errorf("%w (limit %d files)", ErrTooManyFiles, lim.MaxFiles)
	}

	var (
		pkg   *Package
		files = make(map[string][]byte, len(zr.File))
		total int64
	)
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") { // a directory entry, skip
			continue
		}
		switch {
		case name == packageMetaName:
			b, rerr := readZipEntry(f, packageMetaMax)
			if rerr != nil {
				return nil, nil, rerr
			}
			var p Package
			if jerr := json.Unmarshal(b, &p); jerr != nil {
				return nil, nil, fmt.Errorf("failed to parse %s: %w", packageMetaName, jerr)
			}
			pkg = &p

		case strings.HasPrefix(name, packageFilesDir+"/"):
			rel := strings.TrimPrefix(name, packageFilesDir+"/")
			if _, rerr := safeRelPath(rel); rerr != nil {
				return nil, nil, rerr
			}
			if _, dup := files[rel]; dup {
				return nil, nil, fmt.Errorf("duplicate file in the package: %s", rel)
			}
			if len(files) >= lim.MaxFiles {
				return nil, nil, fmt.Errorf("%w (limit %d files)", ErrTooManyFiles, lim.MaxFiles)
			}
			if int64(f.UncompressedSize64) > lim.MaxFileSize {
				return nil, nil, fmt.Errorf("%w: %s (%d bytes, limit %d)",
					ErrFileTooLarge, rel, f.UncompressedSize64, lim.MaxFileSize)
			}
			total += int64(f.UncompressedSize64)
			if total > lim.MaxTotal {
				return nil, nil, fmt.Errorf("the package exceeds the total limit (%d bytes, limit %d)", total, lim.MaxTotal)
			}
			b, rerr := readZipEntry(f, lim.MaxFileSize)
			if rerr != nil {
				return nil, nil, rerr
			}
			files[rel] = b

		default:
			return nil, nil, fmt.Errorf("unknown entry in the package: %s", name)
		}
	}

	if pkg == nil {
		return nil, nil, fmt.Errorf("the package is missing %s", packageMetaName)
	}
	if pkg.Format != PackageFormat {
		return nil, nil, fmt.Errorf("unsupported package format: %s (this build supports %s)", pkg.Format, PackageFormat)
	}
	if len(pkg.Payload) == 0 {
		pkg.Payload = json.RawMessage(`{}`)
	}
	if err := pkg.verify(files); err != nil {
		return nil, nil, err
	}
	return pkg, files, nil
}

// verify checks the manifest against the actual content one to one: only when count, sizes and
// sha256 all agree is the package trustworthy.
func (p *Package) verify(files map[string][]byte) error {
	if len(p.Manifest.Files) == 0 {
		return fmt.Errorf("%w: the manifest is empty", ErrNoFiles)
	}
	if len(p.Manifest.Files) != len(files) {
		return fmt.Errorf("the manifest declares %d file(s), the package holds %d", len(p.Manifest.Files), len(files))
	}
	for _, f := range p.Manifest.Files {
		rel, err := safeRelPath(f.Path)
		if err != nil {
			return err
		}
		data, ok := files[rel]
		if !ok {
			return fmt.Errorf("a file listed in the manifest is missing from the package: %s", rel)
		}
		if int64(len(data)) != f.Size {
			return fmt.Errorf("the size of %s does not match the manifest (manifest %d, actual %d)", rel, f.Size, len(data))
		}
		if f.SHA256 != "" {
			sum := sha256.Sum256(data)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
				return fmt.Errorf("the checksum of %s does not match (was the package modified?)", rel)
			}
		}
	}
	return nil
}

// safeRelPath validates a relative path inside the package.
//
// Only "already normalised, /-separated relative paths" are accepted: absolute paths, paths
// containing a .. segment, backslashes, and forms such as a/../b that need cleaning to be safe are
// all rejected — those are the usual archive-slip tricks.
func safeRelPath(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("the package contains an empty path")
	}
	if strings.ContainsAny(rel, "\\\x00") {
		return "", fmt.Errorf("invalid path (backslash or NUL byte): %s", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("invalid path (absolute): %s", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid path (escapes the root): %s", rel)
	}
	if clean != rel {
		return "", fmt.Errorf("invalid path (not cleaned): %s", rel)
	}
	return clean, nil
}

// readZipEntry reads one entry and fails when it exceeds the limit (the size declared in the zip can
// lie, so the bytes actually read are checked again).
func readZipEntry(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read the package entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the package entry %s: %w", f.Name, err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("package entry too large: %s (limit %d bytes)", f.Name, max)
	}
	return b, nil
}
