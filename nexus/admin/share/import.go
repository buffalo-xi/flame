package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"flame/nexus/shared/assetimport"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxImportUpload caps the upload size (the same limit as the platform-side import and the cross-site pull: 128 MiB
// for the archive, while unpacked size / file count / single-file size stay bounded by assetpack.DefaultLimits).
const maxImportUpload = 128 << 20

// Import POST /admin/share/import
//
// multipart fields: file (required, the .zip produced by an export), name (optional, install under a new name), status (optional, 1/2).
//
// The path is "upload the bundle -> validate -> lay it out in a tenant temp directory -> the same install logic";
// only the content source differs from a marketplace install: no platform.shared_assets row and no platform
// review (the bundle is a file the user brought themselves, with no third-party trust question), and the result is this tenant's own entity.
//
// A clash is never silent: when the target tenant has a same-named entity the response is 200 + code 400 +
// data.suggested_name, and the front end retries with name= after confirmation (exactly like a marketplace install).
func Import(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	pkg, files, kind, ok := readUploadedPackage(c)
	if !ok {
		return
	}

	var payload map[string]any
	if jerr := json.Unmarshal(pkg.Payload, &payload); jerr != nil || payload == nil {
		xhttp.ErrCode(c, "asset.invalid_package", nil, "the payload in the asset bundle is not a JSON object", "")
		return
	}
	// The masking done upstream is not trusted: the bundle may have been edited by hand or produced by an older version
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = pkg.Manifest.Name
	}
	if err := validatePlanName(kind, name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	ctx := context.Background()
	snapDir := path.Join(paths.TenantsSubdir, info.Schema, ".import-tmp", uuid.NewString())
	defer func() { _ = st.DeletePrefix(context.Background(), snapDir) }()
	for rel, data := range files {
		if werr := st.WriteFile(ctx, path.Join(snapDir, rel), data); werr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to unpack the asset bundle: "+werr.Error(), "")
			return
		}
	}

	// overwrite=1 replaces a same-named entity the target tenant already has (all four kinds support it; see installRequest.Overwrite).
	// An unsupported kind is rejected outright - silently ignoring the field makes the front end think the overwrite succeeded while it only got a suggested name.
	overwrite := parseBool(c.PostForm("overwrite"))
	if overwrite && !supportsOverwrite(kind) {
		xhttp.ErrCode(c, "asset.overwrite_unsupported", map[string]any{"kind": string(kind)},
			fmt.Sprintf("%s does not support overwrite import yet: a clash returns a suggested name; retry under a new name", kindLabel(kind)), "")
		return
	}

	notices := append([]string{"this is an uploaded asset bundle (never reviewed by the platform); once installed it is this tenant's own entity"}, secretWarnings...)
	installFrom(c, info, &installSource{
		kind:     kind,
		manifest: pkg.Manifest,
		payload:  clean,
		snapDir:  snapDir,
		notices:  notices,
	}, name, installRequest{
		Name:      name,
		Status:    parseStatus(c.PostForm("status")),
		Overwrite: overwrite,
	})
}

// readUploadedPackage reads the uploaded zip and validates the bundle itself (size / manifest / paths / sha256 / kind assertion).
//
// Read-only: no temp directory, no tenant schema write. Import and Inspect share it,
// so "what the preflight showed" and "the rules the install follows" cannot diverge.
// ok=false means a response has already been written; the caller just returns.
func readUploadedPackage(c *gin.Context) (*assetpack.Package, map[string][]byte, assetpack.Kind, bool) {
	var noKind assetpack.Kind

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImportUpload)
	file, ferr := c.FormFile("file")
	if ferr != nil {
		// Exceeding the limit (MaxBytesReader) is not the same as "no file chosen"
		var mbe *http.MaxBytesError
		if errors.As(ferr, &mbe) {
			xhttp.ErrCode(c, "asset.package_too_large", map[string]any{"limit_mb": maxImportUpload >> 20},
				fmt.Sprintf("the asset bundle exceeds the upload limit (%d MiB)", maxImportUpload>>20), "")
			return nil, nil, noKind, false
		}
		xhttp.ErrCode(c, "asset.package_required", nil, "choose an asset bundle to import (multipart field name file)", "")
		return nil, nil, noKind, false
	}
	if file.Size <= 0 || file.Size > maxImportUpload {
		xhttp.ErrCode(c, "asset.invalid_package", map[string]any{"size": file.Size, "limit": maxImportUpload},
			fmt.Sprintf("invalid asset bundle size (%d bytes, limit %d)", file.Size, maxImportUpload), "")
		return nil, nil, noKind, false
	}
	f, oerr := file.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the uploaded file: "+oerr.Error(), "")
		return nil, nil, noKind, false
	}
	defer func() { _ = f.Close() }()

	// Every entry is checked against the manifest for size and sha256: the whole bundle is validated before any write, so a bad bundle leaves no trace
	pkg, files, perr := assetpack.ReadPackage(f, file.Size, assetpack.DefaultLimits())
	if perr != nil {
		xhttp.ErrCode(c, "asset.invalid_package", nil, "asset bundle validation failed: "+perr.Error(), "")
		return nil, nil, noKind, false
	}
	kind, verr := assetimport.ValidatePackage(pkg, files)
	if verr != nil {
		xhttp.ErrCode(c, "common.internal", nil, verr.Error(), "")
		return nil, nil, noKind, false
	}
	// List semantics: clicking import on the agents page and getting a skill installed only looks like a lost import.
	// The kind comes from the bundle; the kind field is only the user's claim about what they are importing, and a mismatch is stated plainly.
	if want := strings.TrimSpace(c.PostForm("kind")); want != "" && assetpack.Kind(want) != kind {
		xhttp.ErrCode(c, "asset.kind_mismatch", map[string]any{"kind": string(kind), "want": want},
			fmt.Sprintf("this is a %s bundle and cannot be imported as a %s", kindLabel(kind), kindLabel(assetpack.Kind(want))), "")
		return nil, nil, noKind, false
	}
	return pkg, files, kind, true
}

// supportsOverwrite reports which kinds support overwrite import. All four do: an overwrite touches only
// "the entity's own content", while identity / the target tenant's grants and exposure / version history stay as they are (see the installXxx comments).
// The gate stays so that narrowing it later cannot make the front end mistake a suggested name for a successful overwrite.
func supportsOverwrite(k assetpack.Kind) bool {
	return k == assetpack.KindAgent || k == assetpack.KindSkill ||
		k == assetpack.KindTool || k == assetpack.KindWorkflow
}

// kindLabel is the user-facing name of an asset kind (the same wording as the front-end list).
func kindLabel(k assetpack.Kind) string {
	switch k {
	case assetpack.KindSkill:
		return "skill"
	case assetpack.KindTool:
		return "custom tool"
	case assetpack.KindAgent:
		return "agent"
	case assetpack.KindWorkflow:
		return "workflow"
	}
	return string(k)
}

// parseBool reads an optional boolean form field: only "1" / "true" count (everything else is false).
func parseBool(raw string) bool {
	v := strings.TrimSpace(raw)
	return v == "1" || strings.EqualFold(v, "true")
}

// parseStatus parses the optional landing status (only 1 normal / 2 disabled are accepted; otherwise the per-kind default applies).
func parseStatus(raw string) *int {
	switch strings.TrimSpace(raw) {
	case "1":
		v := 1
		return &v
	case "2":
		v := 2
		return &v
	}
	return nil
}
