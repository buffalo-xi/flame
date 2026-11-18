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

// maxImportUpload 上传包体积上限（与平台侧导入、跨站拉包同一口径：压缩包给 128 MiB，
// 解压后的体积 / 文件数 / 单文件大小仍由 assetpack.DefaultLimits 兜住）。
const maxImportUpload = 128 << 20

// Import POST /admin/share/import
//
// multipart 字段：file（必填，导出得到的 .zip）、name（可选，改名安装）、status（可选，1/2）。
//
// 走的是「上传包 → 校验 → 铺进本租户临时目录 → 同一套安装逻辑」，与市场安装的差别只有
// 内容来源：不写 platform.shared_assets、不需要平台审核（包是用户自己带上来的文件，
// 没有第三方可信度问题），装完就是本租户自己的实体。
//
// 冲突不静默：目标租户已有同名实体时回 200 + code 400 + data.suggested_name，
// 由前端确认后带 name= 重试（与市场安装完全一致）。
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
		xhttp.ErrJSON(c, "资产包里的 payload 不是 JSON 对象", nil, "")
		return
	}
	// 不信任上游做过的那一遍脱敏：包可能被手工改过，或是更老的版本导出的
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = pkg.Manifest.Name
	}
	if err := validatePlanName(kind, name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	ctx := context.Background()
	snapDir := path.Join(paths.TenantsSubdir, info.Schema, ".import-tmp", uuid.NewString())
	defer func() { _ = st.DeletePrefix(context.Background(), snapDir) }()
	for rel, data := range files {
		if werr := st.WriteFile(ctx, path.Join(snapDir, rel), data); werr != nil {
			xhttp.ErrJSON(c, "展开资产包失败: "+werr.Error(), nil, "")
			return
		}
	}

	// overwrite=1：目标租户已有同名实体时覆盖它（四类都支持，见 installRequest.Overwrite）。
	// 不支持的类型明确拒绝 —— 悄悄忽略这个字段会让前端以为「覆盖成功」，实际只拿到一个建议名。
	overwrite := parseBool(c.PostForm("overwrite"))
	if overwrite && !supportsOverwrite(kind) {
		xhttp.ErrJSON(c, fmt.Sprintf("%s暂不支持覆盖导入：同名时会给出建议名，改名后重试即可", kindLabel(kind)), nil, "")
		return
	}

	notices := append([]string{"这是一个上传的资产包（未经过平台审核），装上后就是本租户自己的实体"}, secretWarnings...)
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

// readUploadedPackage 读上传的 zip 并校验「包本身」（体积 / 清单 / 路径 / sha256 / 类型断言）。
//
// 只读：不铺临时目录、不碰租户 schema。Import 与 Inspect 共用这一份，
// 保证「装之前看到的结论」与「装的时候按的规矩」不是两套。
// 返回 ok=false 表示已经写过响应，调用方直接 return。
func readUploadedPackage(c *gin.Context) (*assetpack.Package, map[string][]byte, assetpack.Kind, bool) {
	var noKind assetpack.Kind

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImportUpload)
	file, ferr := c.FormFile("file")
	if ferr != nil {
		// 超限（MaxBytesReader）与「没选文件」是两回事
		var mbe *http.MaxBytesError
		if errors.As(ferr, &mbe) {
			xhttp.ErrJSON(c, fmt.Sprintf("资产包超过上传上限（%d MiB）", maxImportUpload>>20), nil, "")
			return nil, nil, noKind, false
		}
		xhttp.ErrJSON(c, "请选择要导入的资产包（multipart 字段名 file）", nil, "")
		return nil, nil, noKind, false
	}
	if file.Size <= 0 || file.Size > maxImportUpload {
		xhttp.ErrJSON(c, fmt.Sprintf("资产包大小非法（%d 字节，上限 %d）", file.Size, maxImportUpload), nil, "")
		return nil, nil, noKind, false
	}
	f, oerr := file.Open()
	if oerr != nil {
		xhttp.ErrJSON(c, "读取上传文件失败: "+oerr.Error(), nil, "")
		return nil, nil, noKind, false
	}
	defer func() { _ = f.Close() }()

	// 包内容按清单逐条核对体积与 sha256：先整包验完再做任何写入，坏包不留痕迹
	pkg, files, perr := assetpack.ReadPackage(f, file.Size, assetpack.DefaultLimits())
	if perr != nil {
		xhttp.ErrJSON(c, "资产包校验失败: "+perr.Error(), nil, "")
		return nil, nil, noKind, false
	}
	kind, verr := assetimport.ValidatePackage(pkg, files)
	if verr != nil {
		xhttp.ErrJSON(c, verr.Error(), nil, "")
		return nil, nil, noKind, false
	}
	// 列表语义：在「智能体」页点导入却装进来一个技能，只会让人以为装丢了。
	// 类型以包为准，kind 只是「用户以为自己在导什么」的断言，不符就明说。
	if want := strings.TrimSpace(c.PostForm("kind")); want != "" && assetpack.Kind(want) != kind {
		xhttp.ErrJSON(c, fmt.Sprintf("这是%s的资产包，不能作为%s导入", kindLabel(kind), kindLabel(assetpack.Kind(want))), nil, "")
		return nil, nil, noKind, false
	}
	return pkg, files, kind, true
}

// supportsOverwrite 哪些类型支持「同名覆盖导入」。四类都支持：覆盖只动「这个实体自己的
// 东西」，身份 / 目标租户的授权与对外暴露配置 / 版本历史一律保留（见各 installXxx 的注释）。
// 这道闸留着是为了以后收窄时前端不会误以为「覆盖成功」而实际只拿到建议名。
func supportsOverwrite(k assetpack.Kind) bool {
	return k == assetpack.KindAgent || k == assetpack.KindSkill ||
		k == assetpack.KindTool || k == assetpack.KindWorkflow
}

// kindLabel 资产类型的中文名（用户话术，与前端列表一致）。
func kindLabel(k assetpack.Kind) string {
	switch k {
	case assetpack.KindSkill:
		return "技能"
	case assetpack.KindTool:
		return "自定义工具"
	case assetpack.KindAgent:
		return "智能体"
	case assetpack.KindWorkflow:
		return "工作流"
	}
	return string(k)
}

// parseBool 读可选布尔表单字段：只认 "1" / "true"（其余一律 false）。
func parseBool(raw string) bool {
	v := strings.TrimSpace(raw)
	return v == "1" || strings.EqualFold(v, "true")
}

// parseStatus 解析可选的落点状态（只接受 1 正常 / 2 停用；其余按各类型的缺省处理）。
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
