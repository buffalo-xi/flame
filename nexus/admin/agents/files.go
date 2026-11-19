package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/repository/sessions"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// agent 工作区文件管理（agent 的本质是一个文件夹）
//   全部经由 storage 抽象（local / nfs / s3 通用）
//   skills/ 目录及其内容只读（由技能管理维护，文件接口不得改动）
// ============================================================================

const (
	// maxAgentFileBytes 单文件读写上限（避免把大文件塞进编辑器）。
	maxAgentFileBytes = 2 << 20 // 2 MiB
	// readonlyPrefix 工作区内只读的目录（agent 私有 skill，由 skills 表维护）。
	readonlyPrefix = "skills"
	// dirKeepFile S3 没有「空目录」，建目录时写一个占位对象使其可被枚举。
	dirKeepFile = ".keep"
	// uploadDir 会话内上传落点。刻意放在会话工作目录里（而不是临时目录或用户级 files/）：
	// 它同时是文件工具的默认根与 exec 的 cwd，只有落在这里 agent 才读得到。
	uploadDir = "uploads"
	// maxSessionUploadBytes 单次上传上限（20 MiB）：太大会让 read_file 之类的工具读不动。
	maxSessionUploadBytes = 20 << 20
	// maxRecentScanEntries 「最近文件」一次最多扫多少条（根目录 + 一层子目录）：
	// 会话目录被 agent 塞满时，不能让一次列表请求变成几十次 LIST。
	maxRecentScanEntries = 300
)

var (
	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// getStorage 懒加载 agent 存储实例（与 service/agent.InitStorage 同配置）。
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return store, storeErr
}

// isReadonlyRel 判断相对路径是否落在只读区（skills 目录及其内容）。
func isReadonlyRel(clean string) bool {
	return clean == readonlyPrefix || strings.HasPrefix(clean, readonlyPrefix+"/")
}

// agentWorkspace 返回 storage 实例 + 工作区相对 key；失败时已写响应。
func agentWorkspace(c *gin.Context) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return nil, "", false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return nil, "", false
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return nil, "", false
	}
	return st, tools.AgentsKey(info.Schema, inst.Name), true
}

// agentMutable 校验 agent 允许改动：有启用（exposed=true）端点时不允许任何改动。
// 返回 false 时已写响应，调用方直接 return 即可。
func agentMutable(c *gin.Context) bool {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return false
	}
	info := authInfo.(*auth.AuthInfo)
	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return false
	}
	// 只有正常态允许改文件内容
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "agent 已废弃，不能修改（如需修改请先切回「正常」）", "")
		return false
	}
	return true
}

// joinKey 拼接工作区内路径（带穿越校验），返回完整 key 与相对路径。
func joinKey(wsKey, rel string) (string, string, error) {
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return wsKey, "", nil
	}
	return path.Join(wsKey, clean), clean, nil
}

// authInfoOf 取当前登录租户（中间件已注入）。取不到时返回 false 且不写响应，
// 由调用方决定是跳过校验还是报错。
func authInfoOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	return info, ok && info != nil
}

// checkEntryPath 校验用户显式给出的条目路径（新建与重命名共用）。空串表示通过。
//
// 三条规则都出自「列表里必须看得见」：. 开头的名字会被 List 过滤掉，建出来就是占着 key
// 却查不到的东西；层级太深在界面上没法导航，S3 上还多一层前缀。
func checkEntryPath(clean string) string {
	if clean == "" {
		return "path is required"
	}
	if strings.HasPrefix(path.Base(clean), ".") {
		return "名字不能以 . 开头（会被列表隐藏）"
	}
	if len([]rune(path.Base(clean))) > 120 {
		return "名字过长（上限 120 字符）"
	}
	if len(strings.Split(clean, "/")) > 16 {
		return "目录层级过深（上限 16 层）"
	}
	return ""
}

// checkRename 校验「把 oldClean 改名成 newClean」本身是否成立。空串表示通过。
//
// 只做纯字符串判断，不碰存储（好测）；「源不存在」「目标已存在」由调用方查。
// 两条硬规则：
//   - 不许覆盖：重命名不是覆盖，对象存储上 CopyObject 会静默盖掉同名对象且无法回滚，
//     而用户多半只是打错字。
//   - 不许搬进自己的子树：dir → dir/sub 会先复制成功，再在删旧前缀时把刚搬过去的
//     内容一起删掉（S3 前缀匹配），本地也会以 invalid argument 失败 —— 两边都得提前挡。
func checkRename(oldClean, newClean string) string {
	if oldClean == "" {
		return "path is required"
	}
	if newClean == "" {
		return "new_path is required"
	}
	if oldClean == newClean {
		return "新名字和原名相同"
	}
	if msg := checkEntryPath(newClean); msg != "" {
		return msg
	}
	if strings.HasPrefix(newClean, oldClean+"/") {
		return "不能移动到自己的子目录里"
	}
	return ""
}

// entryExists 判断路径上有没有东西（文件或目录）。
// S3 的目录没有对象，Exists 恒为 false，所以要再用 List 看一眼里面有没有条目；
// 本地 Stat 对目录同样返回存在，这一条把两种情况都覆盖。
func entryExists(ctx context.Context, st storage.Storage, abs string) bool {
	if ex, _ := st.Exists(ctx, abs); ex {
		return true
	}
	entries, err := st.List(ctx, abs)
	return err == nil && len(entries) > 0
}

// ListAgentFiles 列出工作区某目录下的条目（path 为空 = 工作区根）。
func ListAgentFiles(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
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
			"readonly": isReadonlyRel(p),
			"size":     e.Size,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadAgentFile 读取工作区内的文件内容（skills/ 可读）。
func ReadAgentFile(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
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
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrJSON(c, "file too large to edit", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": isReadonlyRel(clean),
	}, "")
}

// ============================================================================
// 会话工作区文件接口（「工作区」= 某次会话的运行场地）
//   与上面的定义域文件接口分开：这里是产物（products），不是定义（definition）。
//   能力：列目录 / 读 / 下载 / 上传 / 删除。
//   刻意**没有**新建与就地编辑：工作区归 agent 所有，界面只是浏览面 + 投喂面；
//   多一个写者就会和正在运行的 agent 抢同一个目录（无锁），误写代价不可逆。
// ============================================================================

// sessionWorkspace 由 query 的 session_id 解析出该会话的工作目录 key。
//
// 归属校验与 UploadSessionFile 同口径：会话必须属于路由上的这个 agent。
// 缺了这一步，任何人凭一个 session_id 就能借任意 agent 的 /session/* 路由读写别人的会话
// 工作区 —— 返回的路径是按会话真正的 owner user 拼的，等于直接指到对方目录里。
func sessionWorkspace(c *gin.Context) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	inst, ierr := repo.GetAgent(info, c.Param("id"))
	if ierr != nil {
		xhttp.ErrJSON(c, ierr.Error(), nil, "")
		return nil, "", false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return nil, "", false
	}

	// 统一走 lookupSessionForUser：空 / 非 UUID / 不存在 / 不是本人
	// 四类失败一次判完，对外合并成一句话（否则这里就成了探测 sid 是否存在的接口）。
	sess, lerr := lookupSessionForUser(info, info.Schema, c.Query("session_id"))
	if lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return nil, "", false
	}
	if sess.UserID == "" {
		xhttp.ErrJSON(c, "session has no owner user", nil, "")
		return nil, "", false
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return nil, "", false
	}
	return st, paths.UserSessionKey(info.Schema, sess.UserID, sess.ID), true
}

// ListSessionFiles 列出会话工作目录下的条目（path 为空 = 目录根）。
func ListSessionFiles(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
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
		item := gin.H{
			"name":     e.Name,
			"path":     path.Join(clean, e.Name),
			"is_dir":   e.IsDir,
			"readonly": true, // 内容不在界面里编辑（可下载 / 可删除；上传走 upload 接口）
			"size":     e.Size,
		}
		// S3 的「目录」来自 CommonPrefixes，没有修改时间；零值就不下发，前端按空处理
		if !e.ModTime.IsZero() {
			item["mod_time"] = e.ModTime
		}
		items = append(items, item)
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadSessionFile 读取会话工作目录内的文件内容（只读）。
func ReadSessionFile(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
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
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrJSON(c, "file too large to view", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": true,
	}, "")
}

// CreateSessionEntry 在会话工作区里新建文件或目录。
//
// 与定义域的 CreateAgentEntry 同构，差别有三处：
//   - 不挂 agentMutable：会话工作区是运行产物，agent 废弃时也照常可写；
//   - 新建文件可以带初始内容 —— 面板不提供就地编辑（ReadSessionFile 恒 readonly），
//     内容只能在创建时一次性写进去；
//   - 拒绝 . 开头的名字：ListSessionFiles 会跳过这类条目（.keep 占位就靠它隐藏），
//     放行等于建出一个"列表里看不见、还占着 key"的东西。
//
// 子目录：path 允许带 /（如 reports/2026），中间层由存储层自然建出 ——
// 本地 WriteFile 会 MkdirAll 父链，S3 的前缀本身就是目录。
// S3 无「空目录」：建目录时写一个 .keep 占位对象使其可被枚举。
//
// 冲突一律报错、不静默改名：上传是"投喂一个文件"（名字冲突另存 name-2），
// 这里是用户显式命名，悄悄改名比报错更难用。要覆盖已有文件请先删（有二次确认）再建。
func CreateSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		IsDir   bool   `json:"is_dir"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if msg := checkEntryPath(clean); msg != "" {
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}
	if !body.IsDir && len(body.Content) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, fmt.Sprintf("内容过大（上限 %d MB）", maxAgentFileBytes>>20), "")
		return
	}
	ctx := context.Background()
	if ex, _ := st.Exists(ctx, abs); ex {
		xhttp.ErrCode(c, "agent.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if !body.IsDir {
		// 目录不占空间（S3 的 .keep 是 0 字节），只有写文件才需要过配额
		if _, qerr := quota.Admit(ctx, st, authInfoSchema(c), wsKey, authInfoID(c), int64(len(body.Content))); qerr != nil {
			respondQuotaErr(c, qerr)
			return
		}
	}
	if body.IsDir {
		err = st.WriteFile(ctx, path.Join(abs, dirKeepFile), nil)
	} else {
		err = st.WriteFile(ctx, abs, []byte(body.Content))
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !body.IsDir {
		quota.Commit(ctx, authInfoSchema(c), wsKey, authInfoID(c), int64(len(body.Content)))
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":   clean,
		"is_dir": body.IsDir,
		"size":   len(body.Content),
	}, "")
}

// DeleteSessionEntry 删除会话工作区里的文件或目录（产物清理）。
//
// 与定义域的 DeleteAgentEntry 有一处关键差别：这里**不拒绝任何前缀**。
// 定义域的 skills/ 由技能表维护所以只读；会话工作区里的东西（含 uploads/）全是产物，
// 属于会话所有者，允许清理。
//
// 注意：目录是递归删除且不可恢复，前端必须二次确认并写清路径。
func DeleteSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	// 删除前先算它占了多少，删成功后把用量扣回来（否则删了文件额度也不回来）
	freed := entrySize(context.Background(), st, abs)
	// 文件与目录各删一次（两者都幂等、都容忍不存在）：
	//   本地：Delete = os.Remove（非空目录会失败，忽略即可），DeletePrefix = os.RemoveAll（通吃）
	//   S3  ：Delete 删对象，DeletePrefix 删前缀（目录）
	// 注意：不能用 Exists 区分文件/目录 —— 本地 os.Stat 对目录同样返回存在。
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	// 校验是否真的删掉（本地有效；S3 目录无对象，Exists 恒为 false）
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrJSON(c, "delete failed: "+clean, nil, "")
		return
	}
	if freed > 0 {
		quota.Commit(context.Background(), authInfoSchema(c), wsKey, authInfoID(c), -freed)
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// entrySize 估算一个条目占的字节数：文件走 Stat，目录走递归求和。
// 只用于把配额计数扣回来，估不准也只是让下一次校准多扫一次，不影响正确性。
func entrySize(ctx context.Context, st storage.Storage, key string) int64 {
	if sz, _, err := st.Stat(ctx, key); err == nil {
		return sz
	}
	w, ok := st.(storage.Walker)
	if !ok {
		return 0
	}
	var total int64
	_ = w.WalkPrefix(ctx, key, func(_ string, size int64, _ time.Time) error {
		total += size
		return nil
	})
	return total
}

// RenameSessionEntry 重命名 / 移动会话工作区里的文件或目录（body: {path, new_path}）。
//
// 为什么单开一个接口，而不是让前端「下载 + 重新新建」：新建的 content 是字符串，
// 二进制文件（xlsx / zip / 图片 / 音视频）走不了那条路，而它们恰恰最需要改名。
//
// 与定义域不同，这里不拒绝任何前缀：会话工作区里全是产物，uploads/ 里的东西也能改。
func RenameSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		NewPath string `json:"new_path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	oldAbs, oldClean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	newAbs, newClean, err := joinKey(wsKey, body.NewPath)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if msg := checkRename(oldClean, newClean); msg != "" {
		xhttp.ErrJSON(c, msg, nil, "")
		return
	}
	ctx := context.Background()
	if !entryExists(ctx, st, oldAbs) {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "not found: "+oldClean, "")
		return
	}
	// 覆盖一律拒绝：见 checkRename 的注释
	if entryExists(ctx, st, newAbs) {
		xhttp.ErrJSON(c, "already exists: "+newClean, nil, "")
		return
	}
	if rerr := st.Rename(ctx, oldAbs, newAbs); rerr != nil {
		xhttp.ErrJSON(c, "rename failed: "+rerr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": newClean}, "")
}

// DownloadSessionFile 以附件形式下载会话工作目录里的文件（登录态直连，供预览/脚本调用）。
// 浏览器导航式下载走 DownloadByTicket（带票、不需要 Authorization 头）。
func DownloadSessionFile(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	ServeDownload(c, st, abs, clean)
}

// ============================================================================
// 预签名下载：开关 + 探针
//
//	presign_download: false                            → off（默认），S3 也走 nexus 转发
//	presign_download: true                             → on，信任部署者的断言
//	presign_download: true + presign_probe_key: <key>  → auto，探针通过才用
//
// 三态由两个字段拼出来，不引入 off/auto/on 字符串：YAML 1.1 里 on/off 本身就是布尔
// 字面量，解码到 string 要多一层兼容解析；而「有没有配 probe_key」已把 auto 说清楚了。
// ============================================================================

const (
	// presignRetryAfter 探针失败后的冷却时长：分不清「不支持」与「端点/凭据临时故障」，
	// 一刀切永久禁用会让一次抖动变成永久降级。
	presignRetryAfter = 10 * time.Minute
	// presignProbeFileName 探针签进 URL 的下载名（纯 ASCII，验的是「参数被尊重」，
	// 中文名的 filename* 编码由 storage.contentDisposition 单独保证）。
	presignProbeFileName = "probe.txt"
	presignProbeTimeout  = 5 * time.Second
)

var (
	probeMu   sync.Mutex
	probeOK   bool
	probeNext time.Time // 探针失败后的冷却截止时间
)

// presignAllowed 预签名下载当前是否可用；false 即走降级（本进程发字节）。
func presignAllowed() bool {
	h := config.Cfg.Agent.HTTP
	if !h.PresignDownload {
		return false
	}
	if config.Cfg.Agent.Storage.Driver != "s3" {
		return false // local / nfs 没有「外部可达的临时链接」，签了也是空串
	}
	if strings.TrimSpace(h.PresignProbeKey) == "" {
		return true // 没配探针：部署者已实测过，信任断言
	}
	return presignProbePassed()
}

// presignProbePassed 用真实对象验一次「预签名 + 响应头覆盖」是否被这家实现尊重。
//
// 两个不能省的点：
//  1. 必须用 GET。SigV4 把 HTTP 方法签进 canonical request，拿 GET 预签名 URL 发 HEAD
//     会直接 403 —— 探针反而会把「支持」误判成「不支持」。所以探针对象要够小
//     （0 字节最好），读一个字节就断，代价可忽略。
//  2. 状态码 200 之外还要看 Content-Disposition：有些实现既不报错也不覆盖响应头，
//     表现为「能下载但文件名照旧乱码」，只查状态码会漏掉这一种。
func presignProbePassed() bool {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probeOK {
		return true
	}
	if time.Now().Before(probeNext) {
		return false
	}
	fail := func(reason string) bool {
		probeNext = time.Now().Add(presignRetryAfter)
		xlog.Error("预签名探针失败，%s 内降级为 nexus 转发: %s", presignRetryAfter, reason)
		return false
	}

	key := strings.TrimSpace(config.Cfg.Agent.HTTP.PresignProbeKey)
	st, serr := getStorage()
	if serr != nil {
		return fail("storage not ready: " + serr.Error())
	}
	u, perr := st.PresignedGetURL(context.Background(), key, config.Cfg.Agent.HTTP.PresignTTLDuration(), presignProbeFileName)
	if perr != nil {
		return fail("presign: " + perr.Error())
	}
	if u == "" {
		return fail("backend 不支持预签名")
	}

	req, rerr := http.NewRequest(http.MethodGet, u, nil)
	if rerr != nil {
		return fail("bad url: " + rerr.Error())
	}
	resp, derr := (&http.Client{Timeout: presignProbeTimeout}).Do(req)
	if derr != nil {
		return fail("请求失败: " + derr.Error())
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1) // 只读一个字节，别把整个对象拉下来

	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(strings.ToLower(cd), "attachment") {
		return fail("响应头未被覆盖（Content-Disposition=" + cd + "）")
	}
	probeOK = true
	xlog.Info("预签名探针通过 key=%s", key)
	return true
}

// presignDownload 尝试把字节交付甩给对象存储。返回 true 表示已写响应，调用方直接 return。
//
// 预签名是「加速」不是「功能」：后端返回空串（local/nfs）或签名出错时静默退回本进程发送，
// 绝不为它把下载做成 500。
func presignDownload(c *gin.Context, st storage.Storage, abs, name string) bool {
	if !presignAllowed() {
		return false
	}
	u, err := st.PresignedGetURL(context.Background(), abs, config.Cfg.Agent.HTTP.PresignTTLDuration(), name)
	if err != nil || u == "" {
		return false
	}
	// no-store：别让浏览器缓存这个 302 —— 票是一次性的，链接会过期
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, u)
	return true
}

// ServeDownload 下载的统一实现，按部署形态四级分派：
//  0. S3 + presign_download → 302 到预签名 URL，字节由对象存储发（Range / 续传天然支持）；
//  1. nginx（agent.http.x_accel_prefix 非空）→ 回 X-Accel-Redirect，字节由 nginx 发；
//  2. local / nfs → http.ServeContent（自带 Range / 206 / If-Modified-Since）；
//  3. 其它（S3 关掉预签名时）→ 无 Range 的流式转发。
func ServeDownload(c *gin.Context, st storage.Storage, abs, clean string) {
	ctx := context.Background()
	name := path.Base(clean)

	// 先拿元信息：既做上限检查，也给 ServeContent 提供 modTime（Content-Length 由它补）
	size, mod, serr := st.Stat(ctx, abs)
	if serr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	limit := config.Cfg.Agent.HTTP.MaxDownloadBytes()
	if size > limit {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": limit >> 20}, fmt.Sprintf("文件过大（上限 %d MB）", limit>>20), "")
		return
	}

	// 0) 对象存储预签名：换了交付方式不等于换了规则，上限检查必须在它前面
	if presignDownload(c, st, abs, name) {
		return
	}

	// 中文名必须走 RFC 5987 的 filename*，否则浏览器存下来是乱码
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")

	if prefix := config.Cfg.Agent.HTTP.XAccelPrefix; prefix != "" {
		c.Header("X-Accel-Redirect", accelPath(prefix, abs))
		c.Status(http.StatusOK)
		return
	}

	if seekable, ok := st.(storage.SeekableStorage); ok {
		f, oerr := seekable.OpenSeekCloser(ctx, abs)
		if oerr != nil {
			xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
			return
		}
		defer f.Close()
		http.ServeContent(c.Writer, c.Request, name, mod, f)
		return
	}

	rc, rerr := st.ReadStream(ctx, abs)
	if rerr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	defer rc.Close()
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	// 已经开始写响应，中途出错只能放弃（客户端断开是常态，不算错误）
	_, _ = io.Copy(c.Writer, rc)
}

// accelPath 把存储 key 转成 X-Accel-Redirect 用的 URI：逐段转义。
// 整段 PathEscape 会把 "/" 也编码成 %2F，nginx 就找不到文件了。
func accelPath(prefix, key string) string {
	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.Join(segs, "/")
}

// WriteAgentFile 写入/覆盖工作区内的文件（skills/ 拒绝）。
func WriteAgentFile(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrJSON(c, "skills directory is read-only", nil, "")
		return
	}
	if len(body.Content) > maxAgentFileBytes {
		xhttp.ErrJSON(c, "content too large", nil, "")
		return
	}
	// 文档里的 ${global_vars.X} 会被展开进 system prompt：密钥类型的变量必须拦在保存这一步，
	// 否则它一旦进了 AGENT.md 就会进模型上下文与运行记录（见 globalvars.ValidateDocRefs）。
	if strings.HasSuffix(strings.ToLower(clean), ".md") {
		if info, ok := authInfoOf(c); ok {
			if vErr := globalvars.ValidateDocRefs(info.Schema, body.Content); vErr != nil {
				xhttp.ErrJSON(c, vErr.Error(), nil, "")
				return
			}
		}
	}
	if werr := st.WriteFile(context.Background(), abs, []byte(body.Content)); werr != nil {
		xhttp.ErrJSON(c, werr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// DownloadAgentFile 流式下载定义域里的文件。
// 用途：文件列表「拖出到桌面」要拿原始字节做 data: 载荷；编辑器只读不等于不能取得原件。
func DownloadAgentFile(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, jerr := joinKey(wsKey, c.Query("path"))
	if jerr != nil {
		xhttp.ErrJSON(c, jerr.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	ServeDownload(c, st, abs, clean)
}

// CreateAgentEntry 新建文件或目录（skills/ 拒绝）。
// S3 无「空目录」：建目录时写一个 .keep 占位对象。
func CreateAgentEntry(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrJSON(c, "skills directory is read-only", nil, "")
		return
	}
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "agent.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if body.IsDir {
		err = st.WriteFile(context.Background(), path.Join(abs, dirKeepFile), []byte{})
	} else {
		err = st.WriteFile(context.Background(), abs, []byte{})
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// UploadAgentFile 上传文件到 agent 定义域的指定子目录（multipart：file + dir）。
//
// 与技能侧 UploadSkillFile 同构，差别在两道闸：
//   - agentMutable：有启用端点（exposed）时定义域不可改，与其它写接口一致；
//   - skills/ 只读：技能目录由技能表维护，文件接口不得改动。
//
// 落点没有 uploads/ 兜底：定义域根就是内容根，dir 不传即根目录。
// 同名不覆盖：另存 name-2.ext —— 覆盖等于删掉已有文件，要替换请先删（有二次确认）再传。
func UploadAgentFile(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrJSON(c, "file is required", nil, "")
		return
	}
	tooLarge := fmt.Sprintf("文件过大（上限 %d MB）", maxAgentFileBytes>>20)
	if fh.Size > maxAgentFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	// 落点由「有没有提交 dir 字段」决定：不传 = 定义域根目录
	rawDir, _ := c.GetPostForm("dir")
	_, cleanDir, derr := joinKey(wsKey, strings.TrimSpace(rawDir))
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	if cleanDir != "" && isReadonlyRel(cleanDir) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "skills/ 目录只读，请到技能编辑页修改", "")
		return
	}
	name := sanitizeUploadName(fh.Filename)
	if name == "" {
		xhttp.ErrJSON(c, "invalid file name", nil, "")
		return
	}
	ctx := context.Background()
	rel := uniqueUploadRel(ctx, st, wsKey, cleanDir, name)
	// rel 带着 dir 前缀，dir 已挡过；这里再兜一次，防止将来改坏 uniqueUploadRel 的语义
	if isReadonlyRel(rel) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "skills/ 目录只读，请到技能编辑页修改", "")
		return
	}

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+oerr.Error(), nil, "")
		return
	}
	defer src.Close()
	// 多读一个字节用于判定「声明的大小不可信」的极端情况
	data, rerr := io.ReadAll(io.LimitReader(src, maxAgentFileBytes+1))
	if rerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+rerr.Error(), nil, "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	if werr := st.WriteFile(ctx, path.Join(wsKey, rel), data); werr != nil {
		xhttp.ErrJSON(c, "Failed to save upload: "+werr.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"name": path.Base(rel),
		"path": rel,
		"size": len(data),
	}, "")
}

// DeleteAgentEntry 删除工作区内的文件或目录（skills/ 拒绝）。
func DeleteAgentEntry(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if clean == "" {
		xhttp.ErrJSON(c, "path is required", nil, "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrJSON(c, "skills directory is read-only", nil, "")
		return
	}
	// 文件与目录各删一次（两者都幂等、都容忍不存在）：
	//   本地：Delete = os.Remove（非空目录会失败，忽略即可），DeletePrefix = os.RemoveAll（通吃）
	//   S3  ：Delete 删对象，DeletePrefix 删前缀（目录）
	// 注意：不能用 Exists 区分文件/目录 —— 本地 os.Stat 对目录同样返回存在。
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	// 校验是否真的删掉（本地有效；S3 目录无对象，Exists 恒为 false）
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrJSON(c, "delete failed: "+clean, nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// ============================================================================
// 会话工作区：上传（chat 输入框的「+」）
//   与上面的浏览接口同一落点（会话工作目录），区别只在于这里是**写**。
// ============================================================================

// UploadSessionFile 接收 multipart（file + 可选 session_id），写进会话工作区的 uploads/。
//
// 会话不存在时先建会话（新对话还没发第一条消息就先选文件的场景），并把 session_id 回给前端复用 ——
// 否则文件会落在一个「随后被丢弃」的会话目录里，agent 的 cwd 却是另一个目录，永远读不到。
func UploadSessionFile(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrJSON(c, "file is required", nil, "")
		return
	}
	tooLarge := fmt.Sprintf("文件过大（上限 %d MB）", maxSessionUploadBytes>>20)
	if fh.Size > maxSessionUploadBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}

	sessRepo := sessions.NewRepository()
	sid := strings.TrimSpace(c.PostForm("session_id"))
	var sess *models.Session
	if sid == "" {
		// chat 输入框「+」的老行为：没带会话就顺手建一个
		sid, err = sessRepo.Create(info.Schema, models.SessionOwnerTypeAgent, inst.ID, info.ID, "")
		if err != nil {
			respondQuotaErr(c, err) // 会话数超限 → 507
			return
		}
		sess, err = sessRepo.Get(info.Schema, sid)
		if err != nil {
			xhttp.ErrJSON(c, "Failed to get session: "+err.Error(), nil, "")
			return
		}
	} else {
		// 统一走 lookupSessionForUser：非 UUID / 不存在 / 不是本人
		// 都归到同一套响应
		sess, err = lookupSessionForUser(info, info.Schema, sid)
		if err != nil {
			respondSessionErr(c, err, notOwnedSession)
			return
		}
	}
	if sess == nil {
		xhttp.ErrJSON(c, notOwnedSession, nil, "")
		return
	}
	if sess.UserID == "" {
		xhttp.ErrJSON(c, "session has no owner user", nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	name := sanitizeUploadName(fh.Filename)
	if name == "" {
		xhttp.ErrJSON(c, "invalid file name", nil, "")
		return
	}
	wsKey := paths.UserSessionKey(info.Schema, sess.UserID, sid)
	// 落点目录：前端工作区面板传当前浏览的目录（所见即所得），chat 的「+」不传 → 仍落 uploads/。
	// 判据必须是「有没有提交 dir 字段」，不能是「值是否为空」：在根目录上传时传的就是空串，
	// 按值判空会把根目录的上传兜底进 uploads/，用户看到的落点和实际位置对不上。
	// 必须过 joinKey：它会清洗并挡住 ../ 越界，不允许跳出会话目录。
	rawDir, hasDir := c.GetPostForm("dir")
	if !hasDir {
		rawDir = uploadDir
	}
	dir := strings.TrimSpace(rawDir)
	_, cleanDir, derr := joinKey(wsKey, dir)
	if derr != nil {
		xhttp.ErrJSON(c, derr.Error(), nil, "")
		return
	}
	rel := uniqueUploadRel(context.Background(), st, wsKey, cleanDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+oerr.Error(), nil, "")
		return
	}
	defer src.Close()
	// 多读一个字节用于判定「声明的大小不可信」的极端情况
	data, rerr := io.ReadAll(io.LimitReader(src, maxSessionUploadBytes+1))
	if rerr != nil {
		xhttp.ErrJSON(c, "Failed to read upload: "+rerr.Error(), nil, "")
		return
	}
	if len(data) > maxSessionUploadBytes {
		xhttp.ErrJSON(c, tooLarge, nil, "")
		return
	}
	// 配额预检：超限时先按「最旧优先」淘汰该会话的旧产物，腾不出来才拒绝（507）。
	evicted, qerr := quota.Admit(context.Background(), st, info.Schema, wsKey, sess.UserID, int64(len(data)))
	if qerr != nil {
		respondQuotaErr(c, qerr)
		return
	}
	if werr := st.WriteFile(context.Background(), path.Join(wsKey, rel), data); werr != nil {
		xhttp.ErrJSON(c, "Failed to save upload: "+werr.Error(), nil, "")
		return
	}
	// 写成功才记账（失败不记，避免"文件没落地却占了额度"）
	quota.Commit(context.Background(), info.Schema, wsKey, sess.UserID, int64(len(data)))
	if evicted == nil {
		evicted = []string{}
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"session_id": sid,
		"name":       path.Base(rel),
		"path":       rel,
		"size":       len(data),
		// 因超出会话配额被自动清理的旧文件（相对会话根），前端据此提示用户
		"evicted": evicted,
	}, "")
}

// authInfoSchema 取当前登录租户的 schema（配额解析要用）。
// 走到这里时 sessionWorkspace 已经校验过 authInfo 必然存在，取不到只可能是代码改动引入的疏漏。
func authInfoSchema(c *gin.Context) string {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok2 := v.(*auth.AuthInfo); ok2 && info != nil {
			return info.Schema
		}
	}
	return ""
}

// authInfoID 取当前登录用户的 uuid（用于按用户解析会话空间上限）。
// 会话文件接口已由 lookupSessionForUser 保证会话归属就是本人，所以这里取登录用户即可。
func authInfoID(c *gin.Context) string {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok2 := v.(*auth.AuthInfo); ok2 && info != nil {
			return info.ID
		}
	}
	return ""
}

// respondQuotaErr 把配额错误转成 507（带上 scope/metric/used/limit），
// 其他错误仍走普通 400。前端据 code 区分「空间满」与参数错误，给出「去清理」引导。
func respondQuotaErr(c *gin.Context, err error) {
	var qe *quota.ExceededError
	if errors.As(err, &qe) {
		// 507 有两种情形（会话空间满 / 会话数满），文案不同：带 error 编码让前端各翻各的
		xhttp.ErrQuotaCode(c, quotaErrCode(qe), nil, qe.Error(), gin.H{
			"scope":  qe.Scope,
			"metric": qe.Metric,
			"used":   qe.Used,
			"limit":  qe.Limit,
		}, "")
		return
	}
	xhttp.ErrJSON(c, err.Error(), nil, "")
}

// quotaErrCode 把配额错误的 metric 映射成业务编码（见 nexus/docs/error-codes.md）。
func quotaErrCode(qe *quota.ExceededError) string {
	switch qe.Metric {
	case "bytes":
		return "quota.session_storage_exceeded"
	case "sessions":
		return "quota.user_sessions_exceeded"
	}
	return "quota.exceeded"
}

// sanitizeUploadName 只保留文件名本身（浏览器可能带完整路径），去掉控制字符并限制长度。
func sanitizeUploadName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	// ".." 必须挡掉：path.Join(工作区, "uploads/..") 会塌回工作区根，等于写到 uploads/ 之外
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

// uniqueUploadRel 同名不覆盖：name、name-2.ext、name-3.ext…
// dir 是工作区内的相对目录（chat 的「+」传 uploads，工作区面板传当前浏览的目录），
// 返回值带上该前缀，调用方直接 path.Join(wsKey, rel) 即可。
//
// 刻意不做覆盖：覆盖等于删掉 agent 已经产出的文件，是破坏性操作，
// 不该藏在一个上传动作里悄悄发生 —— 要替换请先删再传（删除有二次确认）。
func uniqueUploadRel(ctx context.Context, st storage.Storage, wsKey, dir, name string) string {
	taken := map[string]bool{}
	if entries, err := st.List(ctx, path.Join(wsKey, dir)); err == nil {
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

// attachmentPrompt 把本轮附件拼成给模型看的说明；没有可用附件时返回空串。
//
// 只认 uploads/ 下的会话工作区相对路径：越界（../、绝对路径）与不存在的文件一律跳过 ——
// 报错会让正常对话变得脆弱，但把模型指向一个不存在的文件更糟。
func attachmentPrompt(st storage.Storage, schema, userID, sessionID string, files []string) string {
	if st == nil || userID == "" || len(files) == 0 {
		return ""
	}
	wsKey := paths.UserSessionKey(schema, userID, sessionID)
	lines := make([]string, 0, len(files))
	for _, f := range files {
		clean, cerr := tools.SafePath(f)
		if cerr != nil || clean == "" || !strings.HasPrefix(clean, uploadDir+"/") {
			continue
		}
		if ok, _ := st.Exists(context.Background(), path.Join(wsKey, clean)); !ok {
			continue
		}
		lines = append(lines, "- "+clean)
	}
	if len(lines) == 0 {
		return ""
	}
	return "【用户上传的文件】已放在工作区（路径相对你的工作区根目录，也是 exec 的 cwd）：\n" +
		strings.Join(lines, "\n") +
		"\n需要时用 read_file / list_files 等工具读取，不要凭文件名猜内容。"
}

// ============================================================================
// 工作区「最近文件」：根目录 + 一层子目录，按修改时间倒序
// ============================================================================

// RecentFileItem 最近文件列表的一项（path 为会话工作区相对路径）。
type RecentFileItem struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// RecentSessionFiles 列出会话工作区里最近改动/生成的文件，供工作台「新生成的文件」标签使用。
//
// 只扫根目录与一层子目录：agent 的产物基本落在根目录、uploads/、outputs/ 这类浅层目录，
// 深度再加就是成倍的 LIST 请求（S3 按次计费），收益很小。
func RecentSessionFiles(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	limit := 30
	if v, aerr := strconv.Atoi(c.Query("limit")); aerr == nil && v > 0 {
		limit = v
	}
	if limit > 100 {
		limit = 100
	}

	ctx := context.Background()
	files := make([]RecentFileItem, 0, limit)
	scanned := 0

	var collect func(dir, prefix string, depth int)
	collect = func(dir, prefix string, depth int) {
		entries, lerr := st.List(ctx, dir)
		if lerr != nil {
			return // 目录不存在 / 读失败：这一层当作空
		}
		for _, e := range entries {
			if scanned >= maxRecentScanEntries {
				return
			}
			if strings.HasPrefix(e.Name, ".") { // 跳过隐藏文件（含 .keep 占位）
				continue
			}
			scanned++
			rel := path.Join(prefix, e.Name)
			if e.IsDir {
				if depth == 0 {
					collect(path.Join(dir, e.Name), rel, 1)
				}
				continue
			}
			files = append(files, RecentFileItem{Path: rel, Name: e.Name, Size: e.Size, ModTime: e.ModTime})
		}
	}
	collect(wsKey, "", 0)

	// 新的在前；时间相同（同一秒写入）时按路径排，保证顺序稳定
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Path < files[j].Path
		}
		return files[i].ModTime.After(files[j].ModTime)
	})
	if len(files) > limit {
		files = files[:limit]
	}
	xhttp.OkJSON(c, "Success", gin.H{"files": files, "scanned": scanned}, "")
}
