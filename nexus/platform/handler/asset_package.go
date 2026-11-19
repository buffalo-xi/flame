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

// 跨服务器的资产搬运：导出为 zip / 导入（上传或从对端拉取）。
//
// 平台侧在这里只做两件事：把已审核快照打成包、把收到的包交给 shared/assetimport。
// 「校验 → 脱敏 → 落盘 → 写库」那一整套在 shared/assetimport，租户侧的按需拉取用的是同一份实现。

const (
	// maxPackageUpload 上传体积上限。zip 压缩比可以很高，所以给到 128 MiB（与租户侧导入、
	// 跨站拉包同一口径）；解压后的闸仍由 assetpack.DefaultLimits（2000 个 / 单文件 64 MiB / 合计 128 MiB）兜住。
	maxPackageUpload = 128 << 20
)

// ExportAssetPackage GET /platform/assets/:id/package
//
// 回一个 zip（package.json + files/…）。先在内存里拼完再发：快照合计上限 128 MiB，
// 这点内存换「出错能干干净净报 400」，比流到一半截断一个半个 zip 好得多。
func ExportAssetPackage(c *gin.Context) {
	a, err := sharedassets.Get(c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load asset: "+err.Error(), nil, "")
		return
	}
	if a == nil {
		xhttp.ErrCode(c, "asset.not_found", nil, "资产不存在", "")
		return
	}
	data, err := buildAssetPackage(a)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	servePackage(c, a, data)
}

// buildAssetPackage 把一行资产打包成 zip 字节（导出、对端拉取、租户按需拉取共用）。
func buildAssetPackage(a *sharedassets.Asset) ([]byte, error) {
	var m assetpack.Manifest
	if len(a.Manifest) > 0 {
		if jerr := json.Unmarshal(a.Manifest, &m); jerr != nil {
			return nil, fmt.Errorf("资产清单损坏: %w", jerr)
		}
	}
	if len(m.Files) == 0 {
		return nil, errors.New("该资产没有文件清单，无法导出（请让提供方重新上架）")
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
		return nil, fmt.Errorf("打包快照失败: %w", werr)
	}
	return buf.Bytes(), nil
}

// servePackage 统一的下载响应（文件名 kind-name-vN.zip）。
func servePackage(c *gin.Context, a *sharedassets.Asset, data []byte) {
	filename := fmt.Sprintf("%s-%s-v%d.zip", a.Kind, a.Name, a.Build)
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/zip", data)
}

// packageStorage 平台存储（导出、导入、对端拉取都指向同一个根）。
func packageStorage() (storage.Storage, error) {
	return storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
}

// ImportAssetPackage POST /platform/asset-imports
//
// multipart 字段：file（必填）、origin（可选，缺省取包内 server 名）、force（"1" 覆盖）。
// 导入结果默认 pending：跨环境复制不接受「对面审过了」作为本机的放行依据。
func ImportAssetPackage(c *gin.Context) {
	info, ok := auth.FromContext(c)
	if !ok {
		xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "缺少平台身份")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPackageUpload)
	file, ferr := c.FormFile("file")
	if ferr != nil {
		// 超限（MaxBytesReader）与「没选文件」是两回事，别糊成一句让人摸不着头脑的话
		var mbe *http.MaxBytesError
		if errors.As(ferr, &mbe) {
			xhttp.ErrCode(c, "asset.package_too_large", gin.H{"limit_mb": maxPackageUpload >> 20},
				fmt.Sprintf("资产包超过上传上限（%d MiB）", maxPackageUpload>>20), "")
			return
		}
		xhttp.ErrCode(c, "asset.package_required", nil, "请选择要导入的资产包（multipart 字段名 file）", "")
		return
	}
	if file.Size <= 0 || file.Size > maxPackageUpload {
		xhttp.ErrCode(c, "asset.invalid_package", gin.H{"size": file.Size, "limit": maxPackageUpload},
			fmt.Sprintf("资产包大小非法（%d 字节，上限 %d）", file.Size, maxPackageUpload), "")
		return
	}
	f, oerr := file.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取上传文件失败: "+oerr.Error(), "")
		return
	}
	defer func() { _ = f.Close() }()

	st, serr := packageStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	res, ierr := assetimport.Import(f, file.Size, st, assetimport.Options{
		Origin:      c.PostForm("origin"),
		Force:       c.PostForm("force") == "1" || strings.EqualFold(c.PostForm("force"), "true"),
		AutoApprove: false, // 人工上传一律待审，自动通过只给「配置为 auto 的可信对端」
		ImportedBy:  info.ID,
	})
	if ierr != nil {
		var ce *assetimport.ConflictError
		if errors.As(ierr, &ce) {
			xhttp.ErrJSON(c, ce.Error(), gin.H{"existing": gin.H{
				"id": ce.ExistingID, "build": ce.ExistingBuild, "status": ce.ExistingStatus,
			}}, "")
			return
		}
		xhttp.ErrJSON(c, ierr.Error(), nil, "")
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

// originView 导入溯源（本机上架的行返回 nil，前端据此决定显示「上架方」还是「来源站点」）。
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
