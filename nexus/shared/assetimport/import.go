// Package assetimport materialises an "asset package" into a local snapshot plus one
// platform asset row.
//
// Three entry points share this implementation, so the "never trust the upstream" rules
// live in exactly one place:
//
//	platform admin console   upload a zip (POST /platform/asset-imports)
//	platform admin console   pull a package from a peer (POST /platform/peers/:id/import)
//	tenant asset marketplace on-demand pull when the user clicks install (POST /admin/share/pull-asset)
//
// Rules: unpack, then re-check path / size / manifest / sha256 -> re-run the protected-name
// and secret-redaction passes -> write under platform-assets/ -> insert into shared_assets
// (pending by default, listed only under the auto policy). No step may leave an orphan
// directory behind.
package assetimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/storage"

	"github.com/google/uuid"
)

// Options controls the import policy.
type Options struct {
	// Origin is the source site name; when empty the package's origin.server is used, falling
	// back to "remote".
	Origin string
	// Force overwrites an existing asset with the same origin and name (the local snapshot
	// advances by one and the row goes back to pending / listed according to AutoApprove).
	Force bool
	// AutoApprove lists the asset straight away (only for trusted peers whose install policy is
	// auto).
	AutoApprove bool
	// ImportedBy is the uuid of the actor (platform admin or tenant user); audit only.
	ImportedBy string
}

// Result is the outcome of an import.
type Result struct {
	AssetID      string
	Kind         string
	Name         string
	Build        int
	Status       string
	Origin       string
	SourceSchema string
	Overwritten  bool
	Warnings     []string
}

// ConflictError reports that an asset with the same origin and name already exists and
// overwriting was not allowed.
//
// It carries the existing row's details so the caller can turn it into an actionable prompt
// ("currently v3, overwrite?") instead of a bare conflict. How to settle it is the caller's
// decision: the platform import asks for confirmation once (and retries with force=1), the
// tenant-side remote pull returns a structured conflict so the user can click overwrite.
// That is why the **wording must not name a specific action** ("tick some checkbox"): that
// belongs to one screen and stops being true from another entry point.
type ConflictError struct {
	Origin         string
	Name           string
	ExistingID     string
	ExistingBuild  int
	ExistingStatus string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("origin %s already has an asset with the same kind and name: %s (currently v%d)",
		e.Origin, e.Name, e.ExistingBuild)
}

var (
	originRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	schemaRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

	// buildMu serialises "allocate a snapshot number -> write the files" (listing under
	// admin/share holds its own lock). Both write the same storage but never the same files:
	// listing only touches locally authored rows, import only touches imported ones.
	buildMu sync.Mutex
)

// Import validates and persists the package. ra/size are the zip; st is platform storage (the
// platform side and the tenant side both point at the same storage root).
func Import(ra io.ReaderAt, size int64, st storage.Storage, opt Options) (*Result, error) {
	if st == nil {
		return nil, errors.New("storage not ready")
	}
	pkg, files, err := assetpack.ReadPackage(ra, size, assetpack.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("asset package validation failed: %w", err)
	}

	kind, err := ValidatePackage(pkg, files)
	if err != nil {
		return nil, err
	}

	// Redact again: the upstream pass is not trusted (a peer may hold an older snapshot, may
	// have been tampered with, or may not have run it at all)
	var payload map[string]any
	if err := json.Unmarshal(pkg.Payload, &payload); err != nil || payload == nil {
		return nil, errors.New("the payload in the asset package is not a JSON object")
	}
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	origin := strings.TrimSpace(opt.Origin)
	if origin == "" {
		origin = strings.TrimSpace(pkg.Origin.Server)
	}
	if origin == "" {
		origin = "remote"
	}
	if !originRe.MatchString(origin) {
		return nil, errors.New("the origin site name allows only letters, digits, dot, underscore and dash, at most 64 characters")
	}

	sourceSchema := strings.TrimSpace(pkg.Origin.SourceSchema)
	if !schemaRe.MatchString(sourceSchema) {
		// The remote schema is display-only and this host need not have a tenant of that name;
		// an invalid value must not be written into a CHECK-constrained column
		sourceSchema = "remote"
	}

	existing, err := sharedassets.FindByOriginName(origin, string(kind), sourceSchema, pkg.Manifest.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to query the existing import: %w", err)
	}
	if existing != nil && !opt.Force {
		return nil, &ConflictError{
			Origin: origin, Name: existing.Name,
			ExistingID: existing.ID, ExistingBuild: existing.Build, ExistingStatus: existing.Status,
		}
	}

	status := sharedassets.StatusPending
	if opt.AutoApprove {
		status = sharedassets.StatusListed
	}

	assetID := uuid.NewString()
	build := pkg.Origin.Build
	if build < 1 {
		build = 1
	}
	if existing != nil {
		assetID = existing.ID
		// Local snapshots only move forward: the remote package's build may be older than the
		// local one (re-importing an old package), and it must not rewind the counter
		if build <= existing.Build {
			build = existing.Build + 1
		}
	}

	ctx := context.Background()
	// Allocating the number and writing the files must be serialised (the reasoning is in
	// assetpack.FreeBuild): concurrent imports each compute the same number, the two sets of
	// content interleave inside one directory, and the row only remembers one of the two
	// manifests. When an overwrite lands on a number that already holds content (the same
	// snapshot imported twice, or a previous run that crashed half way), skip it — reusing
	// someone else's directory leaves the manifest disagreeing with the actual content.
	buildMu.Lock()
	if existing != nil {
		b, berr := assetpack.FreeBuild(ctx, st, assetID, build)
		if berr != nil {
			buildMu.Unlock()
			return nil, berr
		}
		build = b
	}
	dstDir := paths.PlatformAssetKey(assetID, build)
	var werr error
	for rel, data := range files {
		if werr = st.WriteFile(ctx, path.Join(dstDir, rel), data); werr != nil {
			break
		}
	}
	buildMu.Unlock()
	if werr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to write the asset files: %w", werr)
	}

	payloadJSON, err := json.Marshal(clean)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to serialise the payload: %w", err)
	}
	// The upstream may run an older version whose package carries no spec / digest: fill it in
	// with this host's rules so every row here has a digest, otherwise "is a re-install the same
	// content?" can never be answered between peers.
	if pkg.Manifest.Digest == "" {
		if derr := assetpack.StampManifest(&pkg.Manifest, payloadJSON); derr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			return nil, fmt.Errorf("failed to compute the content digest: %w", derr)
		}
	}
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to serialise the manifest: %w", err)
	}

	a := &sharedassets.Asset{
		ID:            assetID,
		Kind:          string(kind),
		Name:          pkg.Manifest.Name,
		DisplayName:   asString(clean["display_name"]),
		Description:   asString(clean["description"]),
		SourceSchema:  sourceSchema,
		SourceID:      assetID, // traceability id of the local entity; the remote id lives in OriginAssetID
		Build:         build,
		Status:        status,
		Visibility:    sharedassets.VisibilityAll,
		Manifest:      manifestJSON,
		Payload:       payloadJSON,
		ListedByName:  pkg.Origin.ListedBy,
		OriginServer:  origin,
		OriginAssetID: uuidOrNull(pkg.Origin.AssetID),
		ImportedAt:    sql.NullTime{Time: time.Now(), Valid: true},
		ImportedBy:    uuidOrNull(opt.ImportedBy),
	}

	if existing != nil {
		// Overwrite: reuse the row id (install records still point at the same asset) and move
		// the snapshot and content to the requested status
		err = sharedassets.UpdateImported(a)
	} else {
		err = sharedassets.Insert(a)
	}
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		// Concurrent import: another path already pushed the row to a higher number, so this
		// snapshot is unclaimed (the content itself is fine, a retry takes a fresh number)
		if errors.Is(err, sharedassets.ErrStaleBuild) {
			return nil, errors.New("another import updated this asset concurrently; retry")
		}
		return nil, fmt.Errorf("failed to write the asset record: %w", err)
	}
	// An overwriting import likewise moves the snapshot to a new directory, leaving the old one
	// without readers (install copies, export only reads the current snapshot). The retention
	// window is shared with listing; a failed delete does not fail the import, since the startup
	// sweep collects it again.
	_ = assetpack.PruneOldBuilds(ctx, st, assetID, build)

	warnings := make([]string, 0, 4)
	warnings = append(warnings, secretWarnings...)
	if existing != nil {
		warnings = append(warnings, fmt.Sprintf("overwrote the asset from origin %s; the local snapshot moved to v%d", origin, build))
	}
	if pkg.Origin.Build > 0 && pkg.Origin.Build != build {
		warnings = append(warnings, fmt.Sprintf("the package snapshot is v%d, the local record is v%d (local snapshots only move forward)",
			pkg.Origin.Build, build))
	}
	if kind == assetpack.KindAgent {
		warnings = append(warnings, "super powers / unrestricted tools / skill discovery do not travel with the package and stay off after install; "+
			"who may call this agent has to be configured again in the target tenant")
	}
	if kind == assetpack.KindWorkflow {
		warnings = append(warnings, "a workflow ships only a FLOW.json (the definition lives in the database): after install it is a draft, "+
			"the model / tool / sub-flow references in the graph have to be configured again in the target tenant, and credential variables were cleared when the asset was listed")
	}
	if len(pkg.Manifest.Deps.Skills) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"the asset declares %d skill dependencies; install matches them by name in the target tenant (a missing one is only a warning)", len(pkg.Manifest.Deps.Skills)))
	}
	if opt.AutoApprove {
		warnings = append(warnings, "this origin has auto-approve on, so the import is listed directly (imported_by is still recorded for traceability)")
	}

	return &Result{
		AssetID: assetID, Kind: string(kind), Name: pkg.Manifest.Name,
		Build: build, Status: status, Origin: origin, SourceSchema: sourceSchema,
		Overwritten: existing != nil, Warnings: warnings,
	}, nil
}

// ValidatePackage is the semantic check of a package: kind, name, protected names, and the
// presence of the entry file.
//
// It is exported for "direct import on the tenant side" (uploading a package through
// admin/share), which does not write a platform asset row, yet the verdict on "is this package
// trustworthy" has to match the platform side word for word — otherwise the same package gets
// two different answers from two entry points.
func ValidatePackage(pkg *assetpack.Package, files map[string][]byte) (assetpack.Kind, error) {
	kind := assetpack.Kind(pkg.Manifest.Kind)
	if !kind.Valid() {
		return "", fmt.Errorf("unknown asset kind: %s", pkg.Manifest.Kind)
	}
	// A workflow writes no directory and only uses a database column for its name, but the
	// wording differs (the name is the externally visible tool name, see
	// assetpack.ValidateWorkflowName) while the character set matches the other three kinds
	if kind == assetpack.KindWorkflow {
		if err := assetpack.ValidateWorkflowName(pkg.Manifest.Name); err != nil {
			return "", err
		}
	} else if err := assetpack.ValidateName(pkg.Manifest.Name); err != nil {
		return "", err
	}
	// Protected names are deliberately no longer a rejection reason here: the list only
	// constrains deletion and renaming, not moving an asset. Name collisions are caught by the
	// duplicate check at install time (main / skill-authoring already exist in the target
	// tenant and go through suggested_name), and blocking here only means "a package exported
	// from this machine cannot be installed on another one".

	// Entry file: fixed for skill / agent, chosen by TOOL.json's entry for custom_tool (the file
	// must actually exist in the package)
	entry := kind.EntryFile()
	if entry == "" {
		entry = strings.TrimSpace(pkg.Manifest.Entry)
		if entry == "" {
			return "", errors.New("the custom tool package declares no entry file")
		}
	}
	if _, ok := files[entry]; !ok {
		return "", fmt.Errorf("the package is missing the entry file: %s", entry)
	}
	return kind, nil
}

func uuidOrNull(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	if _, err := uuid.Parse(s); err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// asString reads a string field out of the payload (a wrong value type yields the empty string
// rather than an error).
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
