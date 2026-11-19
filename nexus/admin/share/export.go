package share

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/paths"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Export GET /admin/share/export?kind=&ref=
//
// Packages one **entity of this tenant** into an asset bundle (zip) on the fly: skill / agent / workflow by id,
// custom_tool by name. Unlike listing on the platform it writes no platform snapshot, goes through no review and needs no platform table,
// it just produces a file a person can carry: another machine, another site, install it with "import".
//
// Packaging shares the assembly logic with listing (planFor): payload allowlisting, dependency declaration and credential masking are identical,
// so "export then import" and "list then install" produce the same thing.
func Export(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	kind := assetpack.Kind(strings.TrimSpace(c.Query("kind")))
	if !kind.Valid() {
		xhttp.ErrCode(c, "asset.invalid_kind", map[string]any{"kind": string(kind)},
			"unknown asset kind (kind accepts skill / custom_tool / agent / workflow only)", "")
		return
	}
	ref := strings.TrimSpace(c.Query("ref"))
	if ref == "" {
		xhttp.ErrCode(c, "asset.ref_required", nil, "ref is required (skill / agent / workflow take an id, custom_tool takes a name)", "")
		return
	}

	plan, err := planFor(kind, info, ref)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if plan.cleanup != nil {
		defer plan.cleanup()
	}
	if err := validatePlanName(plan.kind, plan.name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	// Export needs no snapshot: the temp directory only exists to reuse PackFilesWith (which hashes and size-checks while copying).
	ctx := context.Background()
	tmpDir := path.Join(paths.TenantsSubdir, info.Schema, ".export-tmp", uuid.NewString())
	defer func() { _ = st.DeletePrefix(context.Background(), tmpDir) }()

	files, perr := assetpack.PackFilesWith(ctx, st, plan.srcDir, tmpDir, plan.entry,
		assetpack.DefaultLimits(), assetpack.PackOptions{SkipDirs: plan.skipDirs, Trees: plan.trees})
	if perr != nil {
		xhttp.ErrCode(c, "common.internal", nil, perr.Error(), "")
		return
	}

	// The same dependency manifest as listing: the bundle carries the global_vars names so the importer can pre-check which variable is missing.
	plan.deps.GlobalVars = mergeGlobalVarDeps(plan.deps.GlobalVars,
		assetpack.CollectGlobalVarRefs(ctx, st, plan.srcDir, plan.payload))

	clean, secretWarnings := assetpack.StripSecrets(plan.kind, plan.payload)
	payloadJSON, jerr := json.Marshal(clean)
	if jerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "encode payload: "+jerr.Error(), "")
		return
	}

	manifest := assetpack.Manifest{
		Kind:  plan.kind,
		Name:  plan.name,
		Entry: plan.entry,
		Deps:  plan.deps,
		Files: files,
	}
	// The same fingerprint as listing: bundle and platform snapshot describe the same content, so the fingerprint must match.
	if serr := assetpack.StampManifest(&manifest, payloadJSON); serr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "compute digest: "+serr.Error(), "")
		return
	}

	pkg := &assetpack.Package{
		Format:     assetpack.PackageFormat,
		ExportedAt: time.Now(),
		Origin: assetpack.PackageOrigin{
			// There is no platform-assets row to reference, so the source only records site / tenant / person, enough to show provenance.
			Server:       strings.TrimSpace(config.Cfg.Server.Name),
			SourceSchema: info.Schema,
			ListedBy:     info.Name,
		},
		Manifest: manifest,
		Payload:  payloadJSON,
	}

	// Assemble in memory before sending: the total cap is 128 MiB, and that memory buys a clean error instead of a half-written response.
	var buf bytes.Buffer
	if werr := assetpack.WritePackage(ctx, st, tmpDir, pkg, &buf); werr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "packaging failed: "+werr.Error(), "")
		return
	}

	// The notes (which fields were masked, which private skills did not travel) have no place in the zip, so they go in a response header for the front end to decode.
	warnings := append(append([]string{}, plan.warnings...), secretWarnings...)
	serveExport(c, plan.kind, plan.name, warnings, buf.Bytes())
}

// serveExport writes the download. All four asset names are ASCII-only now, but older or hand-made
// bundles may still be non-ASCII, so Content-Disposition keeps the RFC 5987 encoding plus a plain ASCII fallback for old clients that ignore filename*.
func serveExport(c *gin.Context, kind assetpack.Kind, name string, warnings []string, data []byte) {
	filename := fmt.Sprintf("%s-%s.zip", kind, name)
	ascii := fmt.Sprintf("%s-%s.zip", kind, asciiFallback(name))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, rfc5987Escape(filename)))
	c.Header("X-Export-Warnings", encodeWarnings(warnings))
	c.Data(http.StatusOK, "application/zip", data)
}

// asciiFallback replaces non-ASCII or header-unfriendly characters with "-" (the fallback file name only).
func asciiFallback(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "asset"
	}
	return out
}

// rfc5987Escape encodes per RFC 5987 (QueryEscape turns a space into "+", here it must be %20).
func rfc5987Escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// encodeWarnings joins the notes into one line (each URL-encoded, separated by "|") so the header stays ASCII.
func encodeWarnings(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, url.QueryEscape(w))
		}
	}
	return strings.Join(out, "|")
}
