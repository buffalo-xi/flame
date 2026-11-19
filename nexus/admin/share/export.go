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
// 把一个**本租户实体**现场打成资产包（zip）下载：skill / agent / workflow 用 id，
// custom_tool 用名字。与「上架到平台」的区别是它不落平台快照、不进审核、不需要平台表，
// 只产出一个人能带走的文件 —— 换台机器、换个站点，用「导入」装回去。
//
// 打包内容与上架走同一份组装逻辑（planFor）：payload 白名单化、依赖声明、凭据脱敏都一样，
// 所以「导出后再导入」与「上架后安装」得到的是同一份东西。
func Export(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	kind := assetpack.Kind(strings.TrimSpace(c.Query("kind")))
	if !kind.Valid() {
		xhttp.ErrCode(c, "asset.invalid_kind", map[string]any{"kind": string(kind)},
			"未知的资产类型（kind 只支持 skill / custom_tool / agent / workflow）", "")
		return
	}
	ref := strings.TrimSpace(c.Query("ref"))
	if ref == "" {
		xhttp.ErrCode(c, "asset.ref_required", nil, "缺少 ref（skill / agent / workflow 用 id，custom_tool 用名字）", "")
		return
	}

	plan, err := planFor(kind, info, ref)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if plan.cleanup != nil {
		defer plan.cleanup()
	}
	if err := validatePlanName(plan.kind, plan.name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	// 导出不需要快照：临时目录只为复用 PackFilesWith（它边拷边算 sha256 与体积闸）
	ctx := context.Background()
	tmpDir := path.Join(paths.TenantsSubdir, info.Schema, ".export-tmp", uuid.NewString())
	defer func() { _ = st.DeletePrefix(context.Background(), tmpDir) }()

	files, perr := assetpack.PackFilesWith(ctx, st, plan.srcDir, tmpDir, plan.entry,
		assetpack.DefaultLimits(), assetpack.PackOptions{SkipDirs: plan.skipDirs, Trees: plan.trees})
	if perr != nil {
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}

	// 与上架同一份依赖清单：导出包也带 global_vars 名字，导入方能预检缺哪个变量
	plan.deps.GlobalVars = mergeGlobalVarDeps(plan.deps.GlobalVars,
		assetpack.CollectGlobalVarRefs(ctx, st, plan.srcDir, plan.payload))

	clean, secretWarnings := assetpack.StripSecrets(plan.kind, plan.payload)
	payloadJSON, jerr := json.Marshal(clean)
	if jerr != nil {
		xhttp.ErrJSON(c, "encode payload: "+jerr.Error(), nil, "")
		return
	}

	manifest := assetpack.Manifest{
		Kind:  plan.kind,
		Name:  plan.name,
		Entry: plan.entry,
		Deps:  plan.deps,
		Files: files,
	}
	// 与上架同一份指纹：导出包与平台快照描述的是同一份内容，指纹必须一致
	if serr := assetpack.StampManifest(&manifest, payloadJSON); serr != nil {
		xhttp.ErrJSON(c, "compute digest: "+serr.Error(), nil, "")
		return
	}

	pkg := &assetpack.Package{
		Format:     assetpack.PackageFormat,
		ExportedAt: time.Now(),
		Origin: assetpack.PackageOrigin{
			// 没有 platform-assets 行可引用：来源只记站点 / 租户 / 人，够对方展示溯源
			Server:       strings.TrimSpace(config.Cfg.Server.Name),
			SourceSchema: info.Schema,
			ListedBy:     info.Name,
		},
		Manifest: manifest,
		Payload:  payloadJSON,
	}

	// 先在内存里拼完再发：合计上限 128 MiB，这点内存换「出错能干干净净报错」
	var buf bytes.Buffer
	if werr := assetpack.WritePackage(ctx, st, tmpDir, pkg, &buf); werr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "打包失败: "+werr.Error(), "")
		return
	}

	// 提示（脱敏了哪些字段、哪些私有技能没带走）没地方放进 zip：走响应头，前端解码后提示
	warnings := append(append([]string{}, plan.warnings...), secretWarnings...)
	serveExport(c, plan.kind, plan.name, warnings, buf.Bytes())
}

// serveExport 下载响应。四类资产名现在都只允许 ASCII，但仍有中文的历史包 / 手写包，
// 所以 Content-Disposition 继续按 RFC 5987 编码，并留一个纯 ASCII 兜底名给不认 filename* 的老客户端。
func serveExport(c *gin.Context, kind assetpack.Kind, name string, warnings []string, data []byte) {
	filename := fmt.Sprintf("%s-%s.zip", kind, name)
	ascii := fmt.Sprintf("%s-%s.zip", kind, asciiFallback(name))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, rfc5987Escape(filename)))
	c.Header("X-Export-Warnings", encodeWarnings(warnings))
	c.Data(http.StatusOK, "application/zip", data)
}

// asciiFallback 把非 ASCII / 不适合进 header 的字符换成 "-"（只用于兜底文件名）。
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

// rfc5987Escape 按 RFC 5987 编码（QueryEscape 的空格是 "+"，这里要 %20）。
func rfc5987Escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// encodeWarnings 把提示串成一行（每条 URL 编码，用 "|" 分隔），保证 header 只含 ASCII。
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
