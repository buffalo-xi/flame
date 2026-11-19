package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flame/nexus/platform/auth"
	"flame/nexus/shared/assetimport"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Cross-server asset transfer: export as a zip / import (upload or pull from a peer).
//
// The platform side does only two things here: pack a reviewed snapshot, and hand a received
// package to shared/assetimport. The whole "validate -> scrub -> write files -> write rows" chain
// lives in shared/assetimport, and the tenant-side on-demand pull uses the same implementation.

const (
	// maxPackageUpload caps the upload size. A zip can compress very well, so the limit is 128 MiB
	// (the same as tenant-side import and cross-site pulling); the extracted limits are still
	// enforced by assetpack.DefaultLimits (2000 entries / 64 MiB per file / 128 MiB total).
	maxPackageUpload = 128 << 20
)

// ExportAssetPackage GET /platform/assets/:id/package
//
// Returns a zip (package.json + files/...). It is assembled in memory first: a snapshot is capped
// at 128 MiB in total, and that memory buys a clean 400 on failure instead of a truncated half zip
// mid-stream.
func ExportAssetPackage(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load asset: "+err.Error(), "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "asset not found", "")
		return
	}
	data, err := buildAssetPackage(a)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	servePackage(c, a, data)
}

// buildAssetPackage packs one asset row into zip bytes (shared by export, peer pulls and tenant
// on-demand pulls).
func buildAssetPackage(a *sharedassets.Asset) ([]byte, error) {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		if jerr := json.Unmarshal(a.Manifest, &m); jerr != nil {
			return nil, fmt.Errorf("the asset manifest is corrupt: %w", jerr)
		}
	}
	if len(m.Files) == 0 {
		return nil, errors.New("this asset has no file manifest and cannot be exported (ask the provider to list it again)")
	}
	st, serr := packageStorage()
	if serr != nil {
		return nil, fmt.Errorf("storage not ready: %w", serr)
	}
	pkg := &assetpack.Package{
		Format:     assetpack.PackageFormat,
		ExportedAt: time.Now(),
		Origin: assetpack.PackageOrigin{
			Server:       strings.TrimSpace(config.Cfg.Server.Name),
			AssetID:      a.ID,
			SourceSchema: a.SourceSchema,
			Build:        a.Build,
			ListedBy:     a.ListedByName,
		},
		Manifest: m,
		Payload:  json.RawMessage(a.Payload),
	}
	var buf bytes.Buffer
	if werr := assetpack.WritePackage(context.Background(), st,
		paths.PlatformAssetKey(a.ID, a.Build), pkg, &buf); werr != nil {
		return nil, fmt.Errorf("failed to pack the snapshot: %w", werr)
	}
	return buf.Bytes(), nil
}

// servePackage writes the shared download response (file name kind-name-vN.zip).
func servePackage(c *gin.Context, a *sharedassets.Asset, data []byte) {
	filename := fmt.Sprintf("%s-%s-v%d.zip", a.Kind, a.Name, a.Build)
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/zip", data)
}

// packageStorage is platform storage (export, import and peer pulls all point at the same root).
func packageStorage() (storage.Storage, error) {
	return storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
}

// ImportAssetPackage POST /platform/asset-imports
//
// multipart fields: file (required), origin (optional, defaults to the server name inside the
// package), force ("1" overwrites).
// An import lands in pending by default: copying across environments never accepts "the other side
// reviewed it" as grounds for letting it through here.
func ImportAssetPackage(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "missing platform identity")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPackageUpload)
	file, ferr := c.FormFile("file")
	if ferr != nil {
		// Exceeding the limit (MaxBytesReader) and "no file selected" are different things, so they are
		// not blended into one baffling sentence
		var mbe *http.MaxBytesError
		if errors.As(ferr, &mbe) {
			xhttp.ErrCode(c, "asset.package_too_large", gin.H{"limit_mb": maxPackageUpload >> 20},
				fmt.Sprintf("the asset package exceeds the upload limit (%d MiB)", maxPackageUpload>>20), "")
			return
		}
		xhttp.ErrCode(c, "asset.package_required", nil, "select an asset package to import (multipart field name file)", "")
		return
	}
	if file.Size <= 0 || file.Size > maxPackageUpload {
		xhttp.ErrCode(c, "asset.invalid_package", gin.H{"size": file.Size, "limit": maxPackageUpload},
			fmt.Sprintf("invalid asset package size (%d bytes, limit %d)", file.Size, maxPackageUpload), "")
		return
	}
	f, oerr := file.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the uploaded file: "+oerr.Error(), "")
		return
	}
	defer func() { _ = f.Close() }()

	st, serr := packageStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	res, ierr := assetimport.Import(f, file.Size, st, assetimport.Options{
		Origin:      c.PostForm("origin"),
		Force:       c.PostForm("force") == "1" || strings.EqualFold(c.PostForm("force"), "true"),
		AutoApprove: false, // a manual upload always waits for review; auto-approval is only for trusted peers configured as auto
		ImportedBy:  info.ID,
	})
	if ierr != nil {
		var ce *assetimport.ConflictError
		if errors.As(ierr, &ce) {
			xhttp.ErrCodeData(c, "asset.build_conflict", gin.H{"build": ce.ExistingBuild}, ce.Error(), gin.H{"existing": gin.H{
				"id": ce.ExistingID, "build": ce.ExistingBuild, "status": ce.ExistingStatus,
			}}, "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, ierr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"asset": gin.H{
			"id": res.AssetID, "kind": res.Kind, "name": res.Name,
			"build": res.Build, "status": res.Status,
			"origin_server": res.Origin, "source_schema": res.SourceSchema,
		},
		"overwritten": res.Overwritten,
		"warnings":    res.Warnings,
	}, "")
}

// originView renders import provenance (a row listed locally returns nil, and the frontend uses that
// to show "listed by" instead of "source site").
func originView(a *sharedassets.Asset) gin.H {
	if !a.Imported() {
		return nil
	}
	v := gin.H{"server": a.OriginServer}
	if a.OriginAssetID.Valid {
		v["asset_id"] = a.OriginAssetID.String
	}
	if a.ImportedAt.Valid {
		v["imported_at"] = a.ImportedAt.Time
	}
	if a.ImportedBy.Valid {
		v["imported_by"] = a.ImportedBy.String
	}
	return v
}
