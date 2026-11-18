package skills

// 技能的版本：{schema}.skill_versions。
//
// 只覆盖**租户级**技能（skills.agent_id IS NULL）。私有技能没有独立版本号 ——
// 它的目录在 agent 根目录之内，版本号就是所属 agent 的版本号
// （内容落在 versions/agents/<agent>/v<n>/skills/<skill>/）。理由见 nexus/docs/terminology.md：
// 私有技能是 agent 唯一有写权的内容，给它独立版本号再加自发布权就等于开了个后门；
// 捆进 agent 版本后，改技能必须经一次 agent 发布才生效，与 D32 的「禁止自发布」一致。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// skillVersionView 列表项（不含 files 正文，只给汇总）。
type skillVersionView struct {
	Version       int       `json:"version"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	IsCurrent     bool      `json:"is_current"`
	Note          string    `json:"note"`
	CreatedBy     xsql.UUID `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     xsql.Time `json:"created_at"`
	FilesCount    int       `json:"files_count"`
	TotalBytes    int64     `json:"total_bytes"`
}

func skillVersionTable(schema string) string { return schema + ".skill_versions" }

// skillDraftKey 租户级技能的草稿目录（版本域与它平级，见 shared/paths 的版本域注释）。
func skillDraftKey(schema, name string) string {
	return path.Join(paths.SkillsKey(schema), name)
}

// ListSkillVersions GET /skills/:id/versions
func ListSkillVersions(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "读取版本历史")
	if !ok {
		return
	}

	rows, err := db.DB.Query(`SELECT v.version, v.name, COALESCE(v.description,''), v.is_current,
			COALESCE(v.note,''), v.created_by, COALESCE(u.name,''), v.created_at,
			COALESCE(jsonb_array_length(v.files), 0),
			COALESCE((SELECT sum((e->>'size')::bigint) FROM jsonb_array_elements(v.files) e), 0)
		FROM `+skillVersionTable(info.Schema)+` v
		LEFT JOIN `+info.Schema+`.users u ON u.id = v.created_by
		WHERE v.skill_id = $1
		ORDER BY v.version DESC`, s.ID)
	if err != nil {
		xhttp.ErrJSON(c, "读取版本历史失败: "+err.Error(), nil, "")
		return
	}
	defer rows.Close()

	out := make([]skillVersionView, 0)
	for rows.Next() {
		var v skillVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.Description, &v.IsCurrent, &v.Note,
			&v.CreatedBy, &v.CreatedByName, &v.CreatedAt, &v.FilesCount, &v.TotalBytes); err != nil {
			xhttp.ErrJSON(c, "读取版本历史失败: "+err.Error(), nil, "")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		xhttp.ErrJSON(c, "读取版本历史失败: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"versions": out}, "")
}

// ReleaseSkillVersion POST /skills/:id/versions  body: {"note": "..."}
//
// 发布 = 把 skills/<name>/ 冻结成 versions/skills/<name>/v<n> 并设为当前。
// 拷贝在事务内、持实体行锁（与他人改名 / 并发发布串行）。
func ReleaseSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "发布版本")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrJSON(c, "skill 已废弃，不能发布版本（如需发布请先切回「正常」）", nil, "")
		return
	}

	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	note, err := versions.NormalizeNote(body.Note)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	ctx := context.Background()
	srcKey := skillDraftKey(info.Schema, s.Name)

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.skills WHERE id = $1 FOR UPDATE`, s.ID).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "Skill not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrJSON(c, "skill 已废弃，不能发布版本（如需发布请先切回「正常」）", nil, "")
		return
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM `+skillVersionTable(info.Schema)+
		` WHERE skill_id = $1`, s.ID).Scan(&next); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	dstKey := paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, next)
	if err := versions.EnsureAbsent(ctx, st, dstKey); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	files, err := versions.Snapshot(ctx, st, srcKey, dstKey, paths.SkillEntryFile, nil)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, "冻结版本失败: "+err.Error(), nil, "")
		return
	}
	rawFiles, err := versions.Encode(files)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	// 工具依赖是技能的**声明**，属定义：必须随这一版冻结。
	// 漏了它，正式运行的 use_skill 会读到一个空依赖集 —— 正文在、工具不来，且没有任何报错。
	deps, derr := versioning.SkillToolDepsSnapshot(info.Schema, s.ID)
	if derr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, "读取工具依赖失败: "+derr.Error(), nil, "")
		return
	}

	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = false WHERE skill_id = $1 AND is_current`, s.ID); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	var createdAt xsql.Time
	err = tx.QueryRow(`INSERT INTO `+skillVersionTable(info.Schema)+`
		(skill_id, version, name, description, metadata, user_invocable, command_dispatch,
		 command_tool, restricted, files, tool_deps, is_current, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true,$12,NULLIF($13,'')::uuid)
		RETURNING created_at`,
		s.ID, next, s.Name, s.Description, s.Metadata, s.UserInvocable, s.CommandDispatch,
		s.CommandTool, s.Restricted, rawFiles, deps, note, info.ID).Scan(&createdAt)
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
	xhttp.OkJSON(c, "Success", gin.H{"version": skillVersionView{
		Version: next, Name: s.Name, Description: string(s.Description), IsCurrent: true,
		Note: note, CreatedBy: xsql.UUID(info.ID), CreatedByName: info.Name,
		CreatedAt: createdAt, FilesCount: count, TotalBytes: total,
	}}, "")
}

// ApplySkillVersion POST /skills/:id/versions/:version/apply
//
// 载入 = 该版本内容写回草稿目录，不动 is_current（线上仍跑当前版本）。
// 技能目录就是实体，先清空再整份拷回；清单 sha256 逐条校验。
func ApplySkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "载入版本")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrJSON(c, "skill 已废弃，不能载入版本（如需操作请先切回「正常」）", nil, "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	ctx := context.Background()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.skills WHERE id = $1 FOR UPDATE`, s.ID).
		Scan(&status); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrJSON(c, "skill 已废弃，不能载入版本（如需操作请先切回「正常」）", nil, "")
		return
	}

	// 载入 = 整份定义回草稿：正文之外，元数据与依赖声明也在版本里冻结着
	var (
		rawFiles []byte
		rawDeps  []byte
		rawMeta  []byte
		m        versioning.SkillMeta
	)
	err = tx.QueryRow(`SELECT files, COALESCE(description,''), COALESCE(metadata,'{}'::jsonb),
			COALESCE(user_invocable,false), COALESCE(command_dispatch,''), COALESCE(command_tool,''),
			COALESCE(restricted,true), COALESCE(tool_deps,'[]'::jsonb)
		FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2`, s.ID, target).
		Scan(&rawFiles, &m.Description, &rawMeta, &m.UserInvocable,
			&m.CommandDispatch, &m.CommandTool, &m.Restricted, &rawDeps)
	m.Metadata = rawMeta
	if errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrJSON(c, "version not found", nil, "")
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

	draftKey := skillDraftKey(info.Schema, s.Name)
	srcKey := paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, target)
	if err := st.DeletePrefix(ctx, draftKey); err != nil {
		xhttp.ErrJSON(c, "清空草稿目录失败: "+err.Error(), nil, "")
		return
	}
	missing, err := assetpack.RestoreFiles(ctx, st, srcKey, draftKey, files)
	if err != nil {
		xhttp.ErrJSON(c, "载入版本失败: "+err.Error(), nil, "")
		return
	}
	// 元数据与依赖声明一并回写：只铺正文的话，载入完界面还会显示「有改动」
	// （描述 / 可见性 / 依赖仍与版本不同），用户会以为载入没生效。
	m.Name = s.Name
	if err := versioning.RestoreSkillRow(tx, info.Schema, s.ID, m); err != nil {
		xhttp.ErrJSON(c, "写回技能元数据失败: "+err.Error(), nil, "")
		return
	}
	// 可见性声明随版本回滚，白名单（授权）取当前 —— restricted=true 且白名单为空时
	// 会变成「谁都发现不了」，载入后自愈成全员可见，并在 warnings 里点名。
	visibilityReset, verr := versioning.EnsureSkillVisibility(tx, info.Schema, s.ID)
	if verr != nil {
		xhttp.ErrJSON(c, "校验技能可见性失败: "+verr.Error(), nil, "")
		return
	}
	if err := versioning.RestoreSkillDeps(tx, info.Schema, s.ID, rawDeps); err != nil {
		xhttp.ErrJSON(c, "写回工具依赖失败: "+err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	warnings := make([]string, 0, len(missing)+1)
	for _, p := range missing {
		warnings = append(warnings, "快照里缺少文件："+p)
	}
	if visibilityReset {
		warnings = append(warnings, "该版本的可见性是「白名单」，但当前白名单为空（授权不随版本回滚）——"+
			"已退回「全员可见」，避免谁都发现不了它")
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackSkillVersion POST /skills/:id/versions/:version/rollback
//
// 把「正在使用的版本」切到该历史版本，线上立刻生效。
//
// 与 apply 的分工（和工作流一致）：rollback 只切 is_current，不新增版本、**不动草稿**；
// apply 是把该版本写回草稿（不动线上）。
func RollbackSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "切换版本")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrJSON(c, "skill 已废弃，不能切换版本（如需切换请先切回「正常」）", nil, "")
		return
	}
	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2 FOR UPDATE`, s.ID, target).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "version not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = false WHERE skill_id = $1 AND is_current`, s.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = true WHERE skill_id = $1 AND version = $2`, s.ID, target); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteSkillVersion DELETE /skills/:id/versions/:version
//
// 只清历史：当前版拒绝（正式调用的定义来源），唯一版拒绝（没有别的版本可切）。
// 先删目录、后删行：编号不复用，行没了目录在会让下次发布撞上 ErrVersionExists。
func DeleteSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "删除版本")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrJSON(c, "skill 已废弃，不能删除版本（如需操作请先切回「正常」）", nil, "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	ctx := context.Background()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var isCurrent bool
	err = tx.QueryRow(`SELECT is_current FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2`, s.ID, target).Scan(&isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrJSON(c, "version not found", nil, "")
		return
	}
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if isCurrent {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+skillVersionTable(info.Schema)+
			` WHERE skill_id = $1`, s.ID).Scan(&total); err != nil {
			xhttp.ErrJSON(c, err.Error(), nil, "")
			return
		}
		if total <= 1 {
			xhttp.ErrJSON(c, fmt.Sprintf("v%d 是唯一的版本，不能删除；如需清理历史请先发布一个新版本", target), nil, "")
			return
		}
		xhttp.ErrJSON(c, versions.ErrCannotDeleteCurrentVersion.Error(), nil, "")
		return
	}

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, target)); err != nil {
		xhttp.ErrJSON(c, "删除版本目录失败: "+err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`DELETE FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2`, s.ID, target); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// ===== 内部小工具 =====

// authInfo 取认证信息（本包原有 handler 各自内联这一段，新代码统一走这里）。
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return info, true
}

// mustTenantSkill 取 skill 并挡掉「私有技能」—— 它没有独立版本号。
//
// 文案要把出路说清楚（发布所属 agent），否则调用方只会看到一句「不支持」。
func mustTenantSkill(c *gin.Context, info *auth.AuthInfo, action string) (*models.Skill, bool) {
	s, err := skillRepo.Get(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return nil, false
	}
	if s == nil {
		xhttp.ErrJSON(c, "Skill not found", nil, "")
		return nil, false
	}
	if string(s.AgentID) != "" {
		owner := ""
		if name, rerr := resolveAgentName(info.Schema, string(s.AgentID)); rerr == nil {
			owner = name
		}
		hint := "所属智能体"
		if owner != "" {
			hint = "智能体「" + owner + "」"
		}
		xhttp.ErrJSON(c, fmt.Sprintf(
			"私有技能没有独立版本号：它的版本就是%s的版本。请先发布%s，不能%s单个私有技能",
			hint, hint, action), nil, "")
		return nil, false
	}
	return s, true
}

// removeSkillVersions 删除 / 重建租户级技能时清掉它的版本域（幂等）。
//
// 私有技能不走这里：它没有独立版本目录，内容随所属 agent 的版本目录一起清。
func removeSkillVersions(schema, name string) {
	st, err := getStorage()
	if err != nil {
		return
	}
	_ = st.DeletePrefix(context.Background(), paths.VersionEntityKey(schema, paths.VersionSkills, name))
}
