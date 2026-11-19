package customtools

// 自定义工具的版本：{schema}.tool_versions。
//
// 与 agent / skill 的差别只有两点，其余（发布 / 列表 / 载入 / 删除）同构：
//  1. 工具的 name 创建后不可改（已写进 agent_tool_grants.ref），所以没有改名搬运，
//     版本目录只按 name 键；
//  2. 工具是「目录即实体、TOOL.json 是定义」（见 shared/customtoolstore）。版本必须
//     自带合法 TOOL.json，否则从快照读出来的定义会退化到表行 —— 发布时若草稿里缺或坏，
//     用合并视图补写一份进版本目录，并且只写快照，不动草稿。
//
// 配置值（configs）不进版本：它是运行时配置，不是定义。版本行里写 '{}'，
// 载入历史版本也不会把别人的密钥一起恢复出来。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/toolfile"
	"flame/nexus/shared/versions"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// toolVersionView 列表项。不返回 files 正文（可能上百条），只给两个汇总数。
type toolVersionView struct {
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	Lang        string    `json:"lang"`
	Entry       string    `json:"entry"`
	IsCurrent   bool      `json:"is_current"`
	Note        string    `json:"note"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   xsql.Time `json:"created_at"`
	FilesCount  int       `json:"files_count"`
	TotalBytes  int64     `json:"total_bytes"`
}

// toolVerStore 工具版本用的存储实例。
//
// 固定用 rootDir 上的本地文件系统实现：工具链路本来就要求宿主路径可达
// （脚本是沙箱的挂载源），S3 部署下工具本身就跑不起来，版本没必要另开一套后端。
func toolVerStore() storage.Storage {
	return storage.NewFilesystemStorage(config.Cfg.Agent.RootDir)
}

// toolVersionTable 版本表的 schema 限定名。
func toolVersionTable(schema string) string { return schema + ".tool_versions" }

// ListToolVersions GET /customtools/:name/versions
func ListToolVersions(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")

	// 存在性以「策略行」为准：草稿目录被误删时，历史版本仍要能管理（版本在 tool_versions 里）。
	// 用草稿目录判断会让版本历史跟着草稿一起 404。
	var toolID string
	if err := db.DB.QueryRow(`SELECT id FROM `+info.Schema+`.custom_tools WHERE name = $1`, name).
		Scan(&toolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "读取自定义工具失败: "+err.Error(), "")
		return
	}

	rows, err := db.DB.Query(`SELECT v.version, v.name, COALESCE(v.display_name,''),
			COALESCE(v.description,''), COALESCE(v.lang,''), COALESCE(v.entry,''),
			v.is_current, COALESCE(v.note,''), COALESCE(v.created_by,''), v.created_at,
			COALESCE(jsonb_array_length(v.files), 0),
			COALESCE((SELECT sum((e->>'size')::bigint) FROM jsonb_array_elements(v.files) e), 0)
		FROM `+toolVersionTable(info.Schema)+` v
		JOIN `+info.Schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1
		ORDER BY v.version DESC`, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取版本历史失败: "+err.Error(), "")
		return
	}
	defer rows.Close()

	out := make([]toolVersionView, 0)
	for rows.Next() {
		var v toolVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.DisplayName, &v.Description, &v.Lang,
			&v.Entry, &v.IsCurrent, &v.Note, &v.CreatedBy, &v.CreatedAt,
			&v.FilesCount, &v.TotalBytes); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "读取版本历史失败: "+err.Error(), "")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取版本历史失败: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"versions": out}, "")
}

// ReleaseToolVersion POST /customtools/:name/versions  body: {"note": "..."}
//
// 发布 = 把当前草稿目录冻结成 versions/tools/<name>/v<n> 并设为当前版本。
// 拷贝在事务内、持实体行锁：并发发布在这里排队取号，不会算出同一个 n 撞主键。
func ReleaseToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")

	store := storeFor(info.Schema)
	t, err := store.Get(info.Schema, name)
	if errors.Is(err, sql.ErrNoRows) || t == nil {
		errToolNotFound(c)
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取自定义工具失败: "+err.Error(), "")
		return
	}
	if t.Status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "该工具已锁定或已废弃，不能发布版本（如需发布请先切回「正常」）", "")
		return
	}

	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	note, err := versions.NormalizeNote(body.Note)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	// 「目录即实体」允许盘上有工具、表里没行。版本行的 tool_id 有外键，
	// 所以发布前先把策略行补齐（缺行 = 正常态、无配置，见 customtoolstore）。
	if t.ID == "" {
		if err := store.Upsert(t); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "登记工具失败: "+err.Error(), "")
			return
		}
	}
	if t.ID == "" {
		xhttp.ErrCode(c, "common.internal", nil, "工具尚未登记（缺少 id），无法发布版本", "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()
	draftKey, err := paths.EntityDraftKey(info.Schema, paths.VersionTools, name)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	// 锁实体行：发布与「载入/删除版本」串行，也挡住发布途中的状态变更
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.custom_tools WHERE id = $1 FOR UPDATE`, t.ID).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "该工具已锁定或已废弃，不能发布版本（如需发布请先切回「正常」）", "")
		return
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM `+toolVersionTable(info.Schema)+
		` WHERE tool_id = $1`, t.ID).Scan(&next); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	dstKey := paths.VersionKey(info.Schema, paths.VersionTools, name, next)
	if err := versions.EnsureAbsent(ctx, st, dstKey); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	files, err := versions.Snapshot(ctx, st, draftKey, dstKey, t.Entry, nil)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "冻结版本失败: "+err.Error(), "")
		return
	}

	// 快照必须自带合法 TOOL.json：补写合并视图并同步清单摘要（见文件头注释）。
	norm := models.ToolFileOf(t)
	data, merr := models.MarshalToolFile(norm)
	if merr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "生成契约文件失败: "+merr.Error(), "")
		return
	}
	hostDst := filepath.Join(config.Cfg.Agent.RootDir, filepath.FromSlash(dstKey))
	if werr := toolfile.WriteAt(hostDst, norm); werr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "写入版本契约文件失败: "+werr.Error(), "")
		return
	}
	files = versions.PutEntry(files, models.ToolFileName, data)

	rawFiles, err := versions.Encode(files)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = false WHERE tool_id = $1 AND is_current`, t.ID); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	var createdAt xsql.Time
	err = tx.QueryRow(`INSERT INTO `+toolVersionTable(info.Schema)+`
		(tool_id, version, name, display_name, description, parameters, output, lang, entry,
		 timeout_ms, max_output_bytes, egress, config_fields, configs, files,
		 is_current, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'{}'::jsonb,$14,true,$15,$16)
		RETURNING created_at`,
		t.ID, next, name, t.DisplayName, t.Description, t.Parameters, t.Output, t.Lang, t.Entry,
		t.TimeoutMS, t.MaxOutputBytes, t.Egress, t.ConfigFields, rawFiles, note, info.Name).
		Scan(&createdAt)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	if err := tx.Commit(); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	count, total := versions.Summary(files)
	xhttp.OkJSON(c, "Success", gin.H{"version": toolVersionView{
		Version: next, Name: name, DisplayName: t.DisplayName, Description: t.Description,
		Lang: t.Lang, Entry: t.Entry, IsCurrent: true, Note: note,
		CreatedBy: info.Name, CreatedAt: createdAt, FilesCount: count, TotalBytes: total,
	}}, "")
}

// ApplyToolVersion POST /customtools/:name/versions/:version/apply
//
// 载入 = 把该版本的内容写回草稿目录，**不动** is_current（线上不受影响）。
// 与「回滚」分开：载入是「以旧版本为起点继续改」，改完仍要再发布才生效。
//
// 工具目录就是实体本身，所以先清空再整份拷回；清单里的 sha256 逐条校验，
// 对不上说明快照被改过，直接失败而不是把坏文件铺到草稿里。
func ApplyToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()
	draftKey, err := paths.EntityDraftKey(info.Schema, paths.VersionTools, name)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	srcKey := paths.VersionKey(info.Schema, paths.VersionTools, name, target)

	// 锁实体行：与发布串行，避免「拷到一半草稿被改写」
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.custom_tools WHERE name = $1 FOR UPDATE`, name).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "该工具已锁定或已废弃，不能载入版本（如需操作请先切回「正常」）", "")
		return
	}

	// 载入 = 整份定义回草稿：脚本目录之外，契约元数据（参数 schema / 入口 / 超时…）也在版本里
	var (
		rawFiles, rawParams, rawOutput, rawFields []byte
		displayName, desc, lang, entry            string
		timeoutMS, maxOutput                      int
		egress                                    bool
	)
	err = tx.QueryRow(`SELECT v.files, COALESCE(v.display_name,''), COALESCE(v.description,''),
			COALESCE(v.parameters,'{}'::jsonb), v.output, COALESCE(v.lang,'python'),
			COALESCE(v.entry,'main.py'), COALESCE(v.timeout_ms,60000),
			COALESCE(v.max_output_bytes,65536), COALESCE(v.egress,false),
			COALESCE(v.config_fields,'[]'::jsonb)
		FROM `+toolVersionTable(info.Schema)+` v
		JOIN `+info.Schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.version = $2`, name, target).
		Scan(&rawFiles, &displayName, &desc, &rawParams, &rawOutput, &lang, &entry,
			&timeoutMS, &maxOutput, &egress, &rawFields)
	if errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
		return
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	files, err := versions.Decode(rawFiles)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	if err := st.DeletePrefix(ctx, draftKey); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "清空草稿目录失败: "+err.Error(), "")
		return
	}
	missing, err := assetpack.RestoreFiles(ctx, st, srcKey, draftKey, files)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "载入版本失败: "+err.Error(), "")
		return
	}
	// 契约元数据回写：只铺脚本的话，载入完仍显示「有改动」（参数 schema / 描述仍不同）。
	// configs 不动 —— 那是运行时填的值，不是定义。
	var output any
	if len(rawOutput) > 0 {
		output = string(rawOutput)
	}
	if _, err := tx.Exec(`UPDATE `+info.Schema+`.custom_tools
		SET display_name = $2, description = $3, parameters = $4, output = $5,
		    lang = $6, entry = $7, timeout_ms = $8, max_output_bytes = $9,
		    egress = $10, config_fields = $11, updated_at = now()
		    WHERE name = $1`,
		name, displayName, desc, string(rawParams), output, lang, entry,
		timeoutMS, maxOutput, egress, string(rawFields)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "写回工具契约失败: "+err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	warnings := make([]string, 0, len(missing))
	for _, p := range missing {
		warnings = append(warnings, "快照里缺少文件："+p)
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackToolVersion POST /customtools/:name/versions/:version/rollback
//
// 把「正在使用的版本」切到该历史版本，线上立刻生效。
//
// 与 apply 的分工（和工作流一致）：rollback 只切 is_current，不新增版本、**不动草稿**；
// apply 是把该版本写回草稿（不动线上）。
func RollbackToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}
	t, err := storeFor(info.Schema).Get(info.Schema, name)
	if errors.Is(err, sql.ErrNoRows) || t == nil {
		errToolNotFound(c)
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取自定义工具失败: "+err.Error(), "")
		return
	}
	// 切版本会改线上行为，与发布同口径
	if t.Status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "该工具已锁定或已废弃，不能切换版本（如需切换请先切回「正常」）", "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+toolVersionTable(info.Schema)+`
		WHERE tool_id = $1 AND version = $2 FOR UPDATE`, t.ID, target).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = false WHERE tool_id = $1 AND is_current`, t.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = true WHERE tool_id = $1 AND version = $2`, t.ID, target); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteToolVersion DELETE /customtools/:name/versions/:version
//
// 只清历史：线上跑的还是 is_current 那一版，草稿也不动。
// 当前版本一律拒绝 —— 它是正式调用的定义来源；唯一版本也拒绝（没有别的版本可切）。
//
// 顺序是先删目录、后删行：行在目录没了还可以重发一次补上，目录在行没了会让
// 下一次发布撞上 ErrVersionExists（编号不复用，MAX+1 会正好落到这个洞上）。
func DeleteToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.custom_tools WHERE name = $1 FOR UPDATE`, name).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "该工具已锁定或已废弃，不能删除版本（如需操作请先切回「正常」）", "")
		return
	}

	var toolID string
	var isCurrent bool
	err = tx.QueryRow(`SELECT v.tool_id, v.is_current FROM `+toolVersionTable(info.Schema)+` v
		JOIN `+info.Schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.version = $2`, name, target).Scan(&toolID, &isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
		return
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if isCurrent {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+toolVersionTable(info.Schema)+
			` WHERE tool_id = $1`, toolID).Scan(&total); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if total <= 1 {
			xhttp.ErrCode(c, "tool.last_version_undeletable", gin.H{"version": target},
				fmt.Sprintf("v%d 是唯一的版本，不能删除；如需清理历史请先发布一个新版本", target), "")
			return
		}
		xhttp.ErrJSON(c, versions.ErrCannotDeleteCurrentVersion.Error(), nil, "")
		return
	}

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionTools, name, target)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "删除版本目录失败: "+err.Error(), "")
		return
	}
	if _, err := tx.Exec(`DELETE FROM `+toolVersionTable(info.Schema)+`
		WHERE tool_id = $1 AND version = $2`, toolID, target); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// PurgeToolVersions 删除工具时清掉它的版本域（幂等）。
//
// 与草稿目录一起删的理由：版本目录按 name 键，同名重建会「继承」旧实体的历史
// （DB 里虽然没有行，但盘上留着，发布顺序与打包都会对不上）。
func PurgeToolVersions(schema, name string) {
	st := toolVerStore()
	_ = st.DeletePrefix(context.Background(), paths.VersionEntityKey(schema, paths.VersionTools, name))
}
