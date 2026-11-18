package customtools

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 自定义工具**内容目录**的文件管理（与 admin/skills/files.go 同构）
//
//	宿主目录：{root_dir}/tenants/<schema>/tools/<name>/
//	沙箱内  ：只读挂在 /tool，入口从这里起跑
//
// 与技能那套的两点差异（都来自「目录即实体」）：
//   - TOOL.json 是**契约真源**：可读、**不可写不可删**。写入口只有 create / update
//     两个接口（它们会同时写文件与表行，并做 Validate）—— 放开裸写等于绕过校验，
//     还会让文件与表行分叉。
//   - 入口文件（TOOL.json 里的 entry）：可读写、**不可删**。删掉它，工具还在目录里
//     但一跑就报「入口不存在」，属于半死状态。
//
// 刻意不走 storage 抽象：执行侧要用宿主路径做沙箱挂载源（tools.RunCustomToolSpec
// 会 os.Stat 这个目录），toolfile 读写 TOOL.json 也用 os。走 storage 会让 S3 部署
// 出现「编辑器里看得见、跑不起来」外加同一目录两套视图。S3 下这条链路本来就不支持
// 自定义工具（见 run_skill_script 的同款报错）。
// ============================================================================

const (
	// maxToolFileBytes 单文件读写上限（与技能那边一致）。
	maxToolFileBytes = 2 << 20
	// toolKeepFile 建空目录时的占位文件（本地实现其实会真建目录，留着是为了与
	// 技能侧口径一致、也不让列表把空目录当成不存在）。
	toolKeepFile = ".keep"
)

// toolFileRoot 定位工具目录，返回 (宿主目录, 入口相对路径, ok)。
//
// forWrite=true 时过状态闸：锁定(2) / 废弃(3) 一律拒写（与 tools/gate.go、
// 元工具三件套、技能文件接口同口径）。读路径不闸 —— 只读查看不需要理由。
func toolFileRoot(c *gin.Context, forWrite bool) (string, string, bool) {
	info, ok := mustAuth(c)
	if !ok {
		return "", "", false
	}
	name := c.Param("name")
	// 名字闸：URL 里的 name 会拼进宿主路径，必须按创建时的规则校验
	if !tools.ValidName(name) || strings.Contains(name, "__") {
		xhttp.ErrJSON(c, "工具名不合法", nil, "")
		return "", "", false
	}

	dir := toolDir(info.Schema, name)
	st, serr := os.Stat(dir)
	if serr != nil || !st.IsDir() {
		xhttp.ErrJSON(c, fmt.Sprintf("自定义工具目录不存在：tools/%s/", name), nil, "")
		return "", "", false
	}

	store := storeFor(info.Schema)
	status, found, err := store.Status(name)
	if err != nil {
		xhttp.ErrJSON(c, "读取工具状态失败: "+err.Error(), nil, "")
		return "", "", false
	}
	if forWrite {
		if !found {
			xhttp.ErrJSON(c, "该目录还不是合法工具（缺 TOOL.json 或契约无效）：请先用「新建自定义工具」创建，或先修好 TOOL.json", nil, "")
			return "", "", false
		}
		if !models.IsModifiable(status) {
			xhttp.ErrJSON(c, "该工具已锁定或已废弃，不能修改（请在列表里切回「正常」）", nil, "")
			return "", "", false
		}
	}

	entry := ""
	if t, gerr := store.Get(info.Schema, name); gerr == nil && t != nil {
		entry = path.Clean(strings.ReplaceAll(t.Entry, "\\", "/"))
		if entry == "." {
			entry = ""
		}
	}
	return dir, entry, true
}

// joinToolFile 把「工具目录内的相对路径」拼成宿主绝对路径（带穿越校验）。
func joinToolFile(root, rel string) (string, string, error) {
	if strings.TrimSpace(rel) == "" {
		return root, "", nil
	}
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return root, "", nil
	}
	return filepath.Join(root, filepath.FromSlash(clean)), clean, nil
}

// isToolContract 判断是否为契约文件 TOOL.json（大小写不敏感，Windows 上两种都认）。
func isToolContract(rel string) bool {
	return strings.EqualFold(path.Clean(rel), models.ToolFileName)
}

// isToolEntry 判断是否为 TOOL.json 声明的入口文件。
func isToolEntry(rel, entry string) bool {
	if entry == "" || rel == "" {
		return false
	}
	return strings.EqualFold(path.Clean(rel), path.Clean(entry))
}

// ListToolFiles GET /customtools/:name/files?path=
func ListToolFiles(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	entries, lerr := os.ReadDir(abs)
	if lerr != nil {
		// 目录不存在视为空列表：与技能侧同口径，不把「还没建子目录」当错误
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}, "entry": entry}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") { // 跳过隐藏文件（含 .keep 占位）
			continue
		}
		p := path.Join(clean, e.Name())
		var size int64
		if fi, ierr := e.Info(); ierr == nil {
			size = fi.Size()
		}
		items = append(items, gin.H{
			"name":   e.Name(),
			"path":   p,
			"is_dir": e.IsDir(),
			"size":   size,
			// readonly = 不可删除（入口文件）；locked = 连内容也不能改（TOOL.json）
			"readonly": isToolContract(p) || isToolEntry(p, entry),
			"locked":   isToolContract(p),
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items, "entry": entry}, "")
}

// ReadToolFile GET /customtools/:name/file?path=
func ReadToolFile(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	st, serr := os.Stat(abs)
	if serr != nil || st.IsDir() {
		xhttp.ErrJSON(c, "file not found", nil, "")
		return
	}
	if st.Size() > maxToolFileBytes {
		xhttp.ErrJSON(c, "file too large to edit", nil, "")
		return
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		xhttp.ErrJSON(c, "file not found", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": isToolContract(clean) || isToolEntry(clean, entry),
		"locked":   isToolContract(clean),
	}, "")
}

// WriteToolFile PUT /customtools/:name/file
func WriteToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	abs, clean, err := joinToolFile(root, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isToolContract(clean) {
		// 契约只有一个写口：update_custom_tool / 表单（它会同时写文件与表行、并做 Validate）
		xhttp.ErrJSON(c, "TOOL.json 是工具契约，请用上方表单修改（直接改文件会绕过校验并与库里的行分叉）", nil, "")
		return
	}
	if len(body.Content) > maxToolFileBytes {
		xhttp.ErrJSON(c, "content too large", nil, "")
		return
	}
	if st, serr := os.Stat(abs); serr == nil && st.IsDir() {
		xhttp.ErrJSON(c, "目标是目录，不能当文件写入", nil, "")
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		xhttp.ErrJSON(c, "创建目录失败: "+err.Error(), nil, "")
		return
	}
	if werr := os.WriteFile(abs, []byte(body.Content), 0o644); werr != nil {
		xhttp.ErrJSON(c, werr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// CreateToolEntry POST /customtools/:name/file —— 新建文件或目录。
func CreateToolEntry(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	var body struct {
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	abs, clean, err := joinToolFile(root, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isToolContract(clean) {
		xhttp.ErrJSON(c, models.ToolFileName+" 由「新建自定义工具」生成，不能手工创建", nil, "")
		return
	}
	if isToolEntry(clean, entry) {
		xhttp.ErrJSON(c, "这是当前入口文件，已存在；要改入口请改表单里的 entry", nil, "")
		return
	}
	if _, serr := os.Stat(abs); serr == nil {
		xhttp.ErrJSON(c, "already exists", nil, "")
		return
	}
	if body.IsDir {
		if err = os.MkdirAll(abs, 0o755); err == nil {
			// 占位文件：与技能侧一致，避免「空目录算不算存在」的分歧
			err = os.WriteFile(filepath.Join(abs, toolKeepFile), []byte{}, 0o644)
		}
	} else {
		if merr := os.MkdirAll(filepath.Dir(abs), 0o755); merr != nil {
			xhttp.ErrJSON(c, "创建目录失败: "+merr.Error(), nil, "")
			return
		}
		err = os.WriteFile(abs, []byte{}, 0o644)
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// DeleteToolEntry DELETE /customtools/:name/file?path=
func DeleteToolEntry(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isToolContract(clean) {
		xhttp.ErrJSON(c, models.ToolFileName+" 是工具契约，不能删除（要删工具请删整个工具）", nil, "")
		return
	}
	if isToolEntry(clean, entry) {
		xhttp.ErrJSON(c, "入口文件不能删除：删了工具还在，但一跑就报「入口不存在」", nil, "")
		return
	}
	if rerr := os.RemoveAll(abs); rerr != nil {
		xhttp.ErrJSON(c, "delete failed: "+rerr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// UploadToolFile POST /customtools/:name/upload（multipart：file + dir）
//
// 同名不覆盖：另存为 name-2.ext —— 覆盖等于删掉工具里已有的文件，
// 那种事必须走删除（界面有二次确认），不能藏在上传动作里悄悄发生。
func UploadToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrJSON(c, "file is required", nil, "")
		return
	}
	tooLarge := fmt.Sprintf("文件过大（上限 %d MB）", maxToolFileBytes>>20)
	if fh.Size > maxToolFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	absDir, cleanDir, derr := joinToolFile(root, strings.TrimSpace(c.PostForm("dir")))
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	name := sanitizeToolFileName(fh.Filename)
	if name == "" {
		xhttp.ErrJSON(c, "invalid file name", nil, "")
		return
	}
	if isToolContract(path.Join(cleanDir, name)) {
		xhttp.ErrJSON(c, "不能覆盖契约文件 "+models.ToolFileName, nil, "")
		return
	}
	rel := uniqueToolRel(absDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+oerr.Error(), nil, "")
		return
	}
	defer src.Close()
	// 多读一个字节用于判定「声明的大小不可信」的极端情况
	data, rerr := io.ReadAll(io.LimitReader(src, maxToolFileBytes+1))
	if rerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+rerr.Error(), nil, "")
		return
	}
	if len(data) > maxToolFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	if werr := os.WriteFile(filepath.Join(absDir, rel), data, 0o644); werr != nil {
		xhttp.ErrJSON(c, "Failed to save upload: "+werr.Error(), nil, "")
		return
	}
	out := rel
	if cleanDir != "" {
		out = path.Join(cleanDir, rel)
	}
	xhttp.OkJSON(c, "Success", gin.H{"name": rel, "path": out, "size": len(data)}, "")
}

// DownloadToolFile GET /customtools/:name/download?path=
//
// 拖出到桌面时取字节。只服务文件，不服务目录。
func DownloadToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	st, serr := os.Stat(abs)
	if serr != nil || st.IsDir() {
		xhttp.ErrJSON(c, "file not found", nil, "")
		return
	}
	f, oerr := os.Open(abs)
	if oerr != nil {
		xhttp.ErrJSON(c, "file not found", nil, "")
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(clean)))
	http.ServeContent(c.Writer, c.Request, path.Base(clean), st.ModTime(), f)
}

// sanitizeToolFileName 只保留文件名本身（浏览器可能带完整路径），去掉控制字符并限制长度。
func sanitizeToolFileName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	rs := []rune(name)
	if len(rs) > 120 {
		ext := path.Ext(name)
		tail := []rune(ext)
		if len(tail) >= 20 {
			tail, ext = nil, ""
		}
		name = string(rs[:120-len(tail)]) + ext
	}
	return name
}

// uniqueToolRel 同名不覆盖：name、name-2.ext、name-3.ext…
// 返回的是目录内的相对文件名（调用方直接拼 dir）。
func uniqueToolRel(dir, name string) string {
	taken := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			taken[e.Name()] = true
		}
	}
	if !taken[name] {
		return name
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !taken[cand] {
			return cand
		}
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
}
