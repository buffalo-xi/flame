package skills

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"flame/nexus/admin/agents"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// skill 内容目录的文件管理（与 agent 工作区文件接口同构，只是根目录不同）
//   租户级 skill：tenants/<schema>/skills/<name>/
//   agent 私有  ：tenants/<schema>/agents/<agentName>/skills/<name>/
//   全部经由 storage 抽象（local / nfs / s3 通用）
//   入口文件 SKILL.md：可编辑，但不允许删除（运行时按它读取技能正文）
// ============================================================================

const (
	// maxSkillFileBytes 单文件读写上限（与 agent 侧一致，避免把大文件塞进编辑器）。
	maxSkillFileBytes = 2 << 20 // 2 MiB
	// skillDirKeepFile S3 没有「空目录」，建目录时写一个占位对象使其可被枚举。
	skillDirKeepFile = ".keep"
	// skillEntryFile skill 入口文件（运行时读取约定，不允许删除）。
	skillEntryFile = "SKILL.md"
	// skillSubdirName agent 工作区内私有 skill 的子目录名（与 tools 包内部常量一致）。
	skillSubdirName = "skills"
)

// skillRoot 返回 storage 实例 + 该 skill 内容目录的相对 key；失败时已写响应。
// forWrite=true 时额外过归属 agent 的状态闸：归属 agent 废弃(3) 时，它名下的
// 私有 skill 整片冻结。读路径传 false —— 废弃也能读（D4），且读不产生副作用。
func skillRoot(c *gin.Context, forWrite bool) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	s, err := skillRepo.Get(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return nil, "", false
	}
	if s == nil {
		xhttp.ErrJSON(c, "Skill not found", nil, "")
		return nil, "", false
	}
	// 写路径过双重状态闸：归属 agent + skill 自身（与 admin.go 的 Update / Delete、
	// 元工具三件套、tools/gate.go 同口径）。读路径一律不闸（D4）。
	if forWrite {
		if verr := ensureAgentModifiable(info, string(s.AgentID)); verr != nil {
			xhttp.ErrJSON(c, verr.Error(), nil, "")
			return nil, "", false
		}
		if !s.IsModifiable() {
			xhttp.ErrJSON(c, "skill 已废弃，不能修改（如需修改请先切回「正常」）", nil, "")
			return nil, "", false
		}
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return nil, "", false
	}

	agentName, aerr := resolveAgentName(info.Schema, string(s.AgentID))
	if aerr != nil {
		xhttp.ErrJSON(c, "resolve owner agent: "+aerr.Error(), nil, "")
		return nil, "", false
	}
	return st, skillDirKey(info.Schema, agentName, s.Name), true
}

// skillDirKey 返回 skill 内容目录的相对 key（agentName 为空 = 租户级共享 skill）。
func skillDirKey(schema, agentName, name string) string {
	if agentName == "" {
		return path.Join(tools.SkillsKey(schema), name)
	}
	return path.Join(tools.AgentsKey(schema, agentName), skillSubdirName, name)
}

// joinSkillKey 拼接 skill 目录内路径（带穿越校验），返回完整 key 与相对路径。
func joinSkillKey(rootKey, rel string) (string, string, error) {
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return rootKey, "", nil
	}
	return path.Join(rootKey, clean), clean, nil
}

// isSkillEntry 判断相对路径是否为入口文件 SKILL.md。
func isSkillEntry(clean string) bool {
	return strings.EqualFold(clean, skillEntryFile)
}

// ListSkillFiles 列出 skill 内容目录下的条目（path 为空 = 根目录）。
func ListSkillFiles(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	entries, lerr := st.List(context.Background(), abs)
	if lerr != nil {
		// 目录不存在视为空列表（S3 下空目录本身不存在）
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") { // 跳过隐藏文件（含 .keep 占位）
			continue
		}
		p := path.Join(clean, e.Name)
		items = append(items, gin.H{
			"name":     e.Name,
			"path":     p,
			"is_dir":   e.IsDir,
			"readonly": isSkillEntry(p), // 入口文件：可编辑但不可删除
			"size":     e.Size,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadSkillFile 读取 skill 目录内的文件内容。
func ReadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	data, rerr := st.ReadFile(context.Background(), abs)
	if rerr != nil {
		xhttp.ErrJSON(c, "file not found", nil, "")
		return
	}
	if len(data) > maxSkillFileBytes {
		xhttp.ErrJSON(c, "file too large to edit", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": false, // 入口文件也允许编辑内容，只是不允许删除
	}, "")
}

// WriteSkillFile 写入/覆盖 skill 目录内的文件。
func WriteSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
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
	abs, clean, err := joinSkillKey(rootKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if len(body.Content) > maxSkillFileBytes {
		xhttp.ErrJSON(c, "content too large", nil, "")
		return
	}
	// SKILL.md 里的 ${global_vars.X} 会随正文进模型上下文：密钥类型必须拦在保存这一步
	// （见 globalvars.ValidateDocRefs）。非 .md 文件（脚本、数据）不走文本替换，不校验。
	if strings.HasSuffix(strings.ToLower(clean), ".md") {
		if v, exists := c.Get("authInfo"); exists {
			if info, ok := v.(*auth.AuthInfo); ok && info != nil {
				if vErr := globalvars.ValidateDocRefs(info.Schema, body.Content); vErr != nil {
					xhttp.ErrJSON(c, vErr.Error(), nil, "")
					return
				}
			}
		}
	}
	if werr := st.WriteFile(context.Background(), abs, []byte(body.Content)); werr != nil {
		xhttp.ErrJSON(c, werr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// CreateSkillEntry 新建文件或目录（S3 无「空目录」：建目录时写 .keep 占位）。
func CreateSkillEntry(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
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
	abs, clean, err := joinSkillKey(rootKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isSkillEntry(clean) {
		xhttp.ErrJSON(c, skillEntryFile+" already exists", nil, "")
		return
	}
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrJSON(c, "already exists", nil, "")
		return
	}
	if body.IsDir {
		err = st.WriteFile(context.Background(), path.Join(abs, skillDirKeepFile), []byte{})
	} else {
		err = st.WriteFile(context.Background(), abs, []byte{})
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// DeleteSkillEntry 删除 skill 目录内的文件或目录（入口文件 SKILL.md 拒绝删除）。
func DeleteSkillEntry(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isSkillEntry(clean) {
		xhttp.ErrJSON(c, skillEntryFile+" is the skill entry and cannot be deleted", nil, "")
		return
	}
	// 文件与目录各删一次（两者都幂等、都容忍不存在）：
	//   本地：Delete = os.Remove（非空目录会失败，忽略即可），DeletePrefix = os.RemoveAll（通吃）
	//   S3  ：Delete 删对象，DeletePrefix 删前缀（目录）
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrJSON(c, "delete failed: "+clean, nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// UploadSkillFile 上传文件到 skill 内容目录的指定子目录（multipart：file + dir）。
//
// 与会话工作区那条 UploadSessionFile 同一套语义，两处差别：
//   - 没有 uploads/ 兜底：技能目录本身就是内容根，dir 不传（或为空）即技能根目录；
//   - 上限是 maxSkillFileBytes（2 MiB，与编辑器读写同口径），不是会话的 20 MiB。
//
// 同名不覆盖：另存为 name-2.ext —— 覆盖等于删掉技能里已有的文件，
// 那种事必须走删除（界面有二次确认），不能藏在上传动作里悄悄发生。
func UploadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrJSON(c, "file is required", nil, "")
		return
	}
	tooLarge := fmt.Sprintf("文件过大（上限 %d MB）", maxSkillFileBytes>>20)
	if fh.Size > maxSkillFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	// 落点由「有没有提交 dir 字段」决定：不传 = 技能根目录（这里刻意没有 uploads/ 兜底）
	rawDir, _ := c.GetPostForm("dir")
	_, cleanDir, derr := joinSkillKey(rootKey, strings.TrimSpace(rawDir))
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	name := sanitizeSkillFileName(fh.Filename)
	if name == "" {
		xhttp.ErrJSON(c, "invalid file name", nil, "")
		return
	}
	rel := uniqueSkillRel(context.Background(), st, rootKey, cleanDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+oerr.Error(), nil, "")
		return
	}
	defer src.Close()
	// 多读一个字节用于判定「声明的大小不可信」的极端情况
	data, rerr := io.ReadAll(io.LimitReader(src, maxSkillFileBytes+1))
	if rerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+rerr.Error(), nil, "")
		return
	}
	if len(data) > maxSkillFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	if werr := st.WriteFile(context.Background(), path.Join(rootKey, rel), data); werr != nil {
		xhttp.ErrJSON(c, "Failed to save upload: "+werr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"name": path.Base(rel),
		"path": rel,
		"size": len(data),
	}, "")
}

// sanitizeSkillFileName 只保留文件名本身（浏览器可能带完整路径），去掉控制字符并限制长度。
// 与 admin/agents 的 sanitizeUploadName 同源；两包各自持有，不为一个纯字符串函数引入跨包依赖。
func sanitizeSkillFileName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	// ".." 必须挡掉：path.Join(skill根, "..") 会塌回技能目录之外
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	rs := []rune(name)
	if len(rs) > 120 {
		ext := path.Ext(name)
		tail := []rune(ext)
		if len(tail) >= 20 { // 扩展名离谱长就整段丢弃，别为了后缀把主名挤没
			tail, ext = nil, ""
		}
		name = string(rs[:120-len(tail)]) + ext
	}
	return name
}

// uniqueSkillRel 同名不覆盖：name、name-2.ext、name-3.ext…
// dir 是技能目录内的相对目录，返回值带上该前缀，调用方直接 path.Join(rootKey, rel) 即可。
func uniqueSkillRel(ctx context.Context, st storage.Storage, rootKey, dir, name string) string {
	taken := map[string]bool{}
	if entries, err := st.List(ctx, path.Join(rootKey, dir)); err == nil {
		for _, e := range entries {
			taken[e.Name] = true
		}
	}
	if !taken[name] {
		return path.Join(dir, name)
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !taken[cand] {
			return path.Join(dir, cand)
		}
	}
	return path.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
}

// DownloadSkillFile 流式下载技能内容目录里的文件（拖出到桌面的取字节入口）。
// 直接复用 agents 包的 ServeDownload：四级分派（预签名 / X-Accel / ServeContent / 流式）
// 与部署形态强相关，各写一份迟早会走偏；skills 包不反向依赖 agents，不会成环。
func DownloadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, jerr := joinSkillKey(rootKey, c.Query("path"))
	if jerr != nil {
		xhttp.ErrJSON(c, jerr.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	agents.ServeDownload(c, st, abs, clean)
}

// defaultSkillMD 新建 skill 时的默认正文（与 agent 入口文件同样的做法）。
func defaultSkillMD(name, desc string) string {
	return "# " + name + "\n\n" + strings.TrimSpace(desc) + "\n\n" +
		"## 何时使用\n\n（描述这个技能适用于什么场景）\n\n" +
		"## 技能内容\n\n（写给模型看的说明、步骤或规范）\n"
}
