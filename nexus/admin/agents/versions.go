package agents

// 智能体的版本：{schema}.agent_versions。
//
// 与 flow / skill / tool 的差别（决定了这里的快照要多做三件事）：
//  1. agent 目录里还有私有技能（agents/<a>/skills/**）—— 它们没有独立版本号，
//     随这一份 agent 版本一起冻结（见 nexus/docs/terminology.md）；
//  2. 私有技能的**元数据**（描述 / 是否用户可调用 / 可见性 / 工具依赖）也要进快照，
//     否则「只改描述不发布」就改变了模型看到的技能清单口径 —— 正文冻结、元数据不冻结
//     是自相矛盾的；
//  3. 已废弃（status=3）的私有技能不进快照：留着正文等于给废弃技能留了条复活路径，
//     正式运行时模型在 /agent 里 cat 到就会照样用。

import (
	"context"
	"database/sql"
	"encoding/json"
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

// agentVersionView 列表项（不含 files 正文，只给汇总）。
type agentVersionView struct {
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
	PrivateSkills int       `json:"private_skills"` // 快照里的私有技能条数
}

func agentVersionTable(schema string) string { return schema + ".agent_versions" }

// ListAgentVersions GET /agents/:id/versions
func ListAgentVersions(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}

	rows, err := db.DB.Query(`SELECT v.version, v.name, COALESCE(v.description,''), v.is_current,
			COALESCE(v.note,''), v.created_by, COALESCE(u.name,''), v.created_at,
			COALESCE(jsonb_array_length(v.files), 0),
			COALESCE((SELECT sum((e->>'size')::bigint) FROM jsonb_array_elements(v.files) e), 0),
			COALESCE(jsonb_array_length(v.skills), 0)
		FROM `+agentVersionTable(info.Schema)+` v
		LEFT JOIN `+info.Schema+`.users u ON u.id = v.created_by
		WHERE v.agent_id = $1
		ORDER BY v.version DESC`, inst.ID)
	if err != nil {
		xhttp.ErrJSON(c, "读取版本历史失败: "+err.Error(), nil, "")
		return
	}
	defer rows.Close()

	out := make([]agentVersionView, 0)
	for rows.Next() {
		var v agentVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.Description, &v.IsCurrent, &v.Note,
			&v.CreatedBy, &v.CreatedByName, &v.CreatedAt, &v.FilesCount, &v.TotalBytes,
			&v.PrivateSkills); err != nil {
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

// ReleaseAgentVersion POST /agents/:id/versions  body: {"note": "..."}
func ReleaseAgentVersion(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}
	if !inst.IsModifiable() {
		xhttp.ErrJSON(c, "agent 已废弃，不能发布版本（如需发布请先切回「正常」）", nil, "")
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

	ctx := context.Background()
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	srcKey := paths.AgentsKey(info.Schema, inst.Name)

	// 废弃的私有技能不进快照（正文仍留在草稿里，但正式版本不该带着它）
	skip, skerr := versioning.DeprecatedPrivateSkillDirs(info.Schema, inst.ID)
	if skerr != nil {
		xhttp.ErrJSON(c, skerr.Error(), nil, "")
		return
	}
	privates, skerr := versioning.PrivateSkillsSnapshot(info.Schema, inst.ID)
	if skerr != nil {
		xhttp.ErrJSON(c, skerr.Error(), nil, "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.agents WHERE id = $1 FOR UPDATE`, inst.ID).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "Agent instance not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrJSON(c, "agent 已废弃，不能发布版本（如需发布请先切回「正常」）", nil, "")
		return
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM `+agentVersionTable(info.Schema)+
		` WHERE agent_id = $1`, inst.ID).Scan(&next); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	dstKey := paths.VersionKey(info.Schema, paths.VersionAgents, inst.Name, next)
	if err := versions.EnsureAbsent(ctx, st, dstKey); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	files, err := versions.Snapshot(ctx, st, srcKey, dstKey, paths.AgentEntryFile, skip)
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

	if _, err := tx.Exec(`UPDATE `+agentVersionTable(info.Schema)+`
		SET is_current = false WHERE agent_id = $1 AND is_current`, inst.ID); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	var createdAt xsql.Time
	err = tx.QueryRow(`INSERT INTO `+agentVersionTable(info.Schema)+`
		(agent_id, version, name, description, config, tools_unrestricted, skill_discovery,
		 files, skills, is_current, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,true,$10,NULLIF($11,'')::uuid)
		RETURNING created_at`,
		inst.ID, next, inst.Name, inst.Description, inst.Config, inst.ToolsUnrestricted,
		inst.SkillDiscovery, rawFiles, privates, note, info.ID).Scan(&createdAt)
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
	var skills []json.RawMessage
	_ = json.Unmarshal(privates, &skills)
	xhttp.OkJSON(c, "Success", gin.H{"version": agentVersionView{
		Version: next, Name: inst.Name, Description: string(inst.Description), IsCurrent: true,
		Note: note, CreatedBy: xsql.UUID(info.ID), CreatedByName: info.Name,
		CreatedAt: createdAt, FilesCount: count, TotalBytes: total, PrivateSkills: len(skills),
	}}, "")
}

// ApplyAgentVersion POST /agents/:id/versions/:version/apply
//
// 载入 = 把该版本的定义文件写回草稿，不动 is_current。
//
// 与 skill / tool 的关键差别：**不清空 skills/**。
//   - 私有技能是独立实体（skills 表有行），版本里没有它可能只是「发布之后新建的」；
//     清掉目录会让它变成「有行、没正文」的空技能；
//   - 版本里有的私有技能由清单覆盖；版本里没有的原样保留，并在 warnings 里点名。
func ApplyAgentVersion(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}
	if !inst.IsModifiable() {
		xhttp.ErrJSON(c, "agent 已废弃，不能载入版本（如需操作请先切回「正常」）", nil, "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	ctx := context.Background()
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.agents WHERE id = $1 FOR UPDATE`, inst.ID).
		Scan(&status); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrJSON(c, "agent 已废弃，不能载入版本（如需操作请先切回「正常」）", nil, "")
		return
	}

	// 载入 = 整份定义回草稿：AGENT.md 之外，agent 元数据与私有技能元数据/依赖也在版本里
	var (
		rawFiles, rawSkills, rawConfig []byte
		desc                           string
		toolsUnrestricted, discovery   bool
	)
	err = tx.QueryRow(`SELECT files, COALESCE(skills,'[]'::jsonb), COALESCE(description,''),
			COALESCE(config,'{}'::jsonb), COALESCE(tools_unrestricted,false), COALESCE(skill_discovery,false)
		FROM `+agentVersionTable(info.Schema)+`
		WHERE agent_id = $1 AND version = $2`, inst.ID, target).
		Scan(&rawFiles, &rawSkills, &desc, &rawConfig, &toolsUnrestricted, &discovery)
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

	draftKey := paths.AgentsKey(info.Schema, inst.Name)
	srcKey := paths.VersionKey(info.Schema, paths.VersionAgents, inst.Name, target)

	// 清掉草稿里「非 skills/」的内容：AGENT.md 与附属文件按版本重建，
	// skills/ 整棵保留（见函数头注释）。
	// 目录不存在（或列举失败）时按「没什么可清」处理：真正的错误交给后面的还原去暴露
	entries, _ := st.List(ctx, draftKey)
	for _, e := range entries {
		if e.IsDir && e.Name == "skills" {
			continue
		}
		key := path.Join(draftKey, e.Name)
		_ = st.Delete(ctx, key)
		_ = st.DeletePrefix(ctx, key)
	}

	missing, err := assetpack.RestoreFiles(ctx, st, srcKey, draftKey, files)
	if err != nil {
		xhttp.ErrJSON(c, "载入版本失败: "+err.Error(), nil, "")
		return
	}
	// 元数据回写：只铺正文的话，载入完还会显示「有改动」（描述 / 配置 / 私有技能元数据仍不同）
	if _, err := tx.Exec(`UPDATE `+info.Schema+`.agents
		SET description = $2, config = $3, tools_unrestricted = $4, skill_discovery = $5,
		    updated_at = now()
		WHERE id = $1`,
		inst.ID, desc, rawConfig, toolsUnrestricted, discovery); err != nil {
		xhttp.ErrJSON(c, "写回 agent 元数据失败: "+err.Error(), nil, "")
		return
	}
	// 私有技能：只回写版本里有的那些（快照里没有的说明是发布之后新建的，保持原样）
	if err := versioning.RestoreAgentPrivateSkills(tx, info.Schema, inst.ID, rawSkills); err != nil {
		xhttp.ErrJSON(c, "写回私有技能元数据失败: "+err.Error(), nil, "")
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
	// 草稿里有、该版本里没有的私有技能：保留并点名（它们是独立实体，删了就是空技能）
	if kept, kerr := versioning.KeptPrivateSkills(ctx, st, draftKey, rawSkills); kerr == nil {
		for _, name := range kept {
			warnings = append(warnings, fmt.Sprintf("私有技能 %q 不在该版本里，草稿内容已保留（未删除）", name))
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackAgentVersion POST /agents/:id/versions/:version/rollback
//
// 把「正在使用的版本」切到该历史版本，线上立刻生效。
//
// 与 apply 的分工（和工作流一致）：rollback 只切 is_current，不新增版本、**不动草稿**；
// apply 是把该版本写回草稿（不动线上），想基于旧版本继续改就用它。
// 少了 rollback，回退只能「载入草稿 → 再发布」，既会覆盖正在改的草稿，又会凭空多一个版本号。
func RollbackAgentVersion(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}
	// 切版本会改线上行为，与发布同口径：废弃的 agent 不允许
	if !inst.IsModifiable() {
		xhttp.ErrJSON(c, "agent 已废弃，不能切换版本（如需切换请先切回「正常」）", nil, "")
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

	// 目标版本行加锁：与并发发布串行，避免「刚切过去又被发布覆盖」
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+agentVersionTable(info.Schema)+`
		WHERE agent_id = $1 AND version = $2 FOR UPDATE`, inst.ID, target).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrJSON(c, "version not found", nil, "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+agentVersionTable(info.Schema)+`
		SET is_current = false WHERE agent_id = $1 AND is_current`, inst.ID); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+agentVersionTable(info.Schema)+`
		SET is_current = true WHERE agent_id = $1 AND version = $2`, inst.ID, target); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteAgentVersion DELETE /agents/:id/versions/:version
func DeleteAgentVersion(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}
	if !inst.IsModifiable() {
		xhttp.ErrJSON(c, "agent 已废弃，不能删除版本（如需操作请先切回「正常」）", nil, "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrJSON(c, "Invalid version", nil, "")
		return
	}

	ctx := context.Background()
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var isCurrent bool
	err = tx.QueryRow(`SELECT is_current FROM `+agentVersionTable(info.Schema)+`
		WHERE agent_id = $1 AND version = $2`, inst.ID, target).Scan(&isCurrent)
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
		if err := tx.QueryRow(`SELECT count(*) FROM `+agentVersionTable(info.Schema)+
			` WHERE agent_id = $1`, inst.ID).Scan(&total); err != nil {
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

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionAgents, inst.Name, target)); err != nil {
		xhttp.ErrJSON(c, "删除版本目录失败: "+err.Error(), nil, "")
		return
	}
	if _, err := tx.Exec(`DELETE FROM `+agentVersionTable(info.Schema)+`
		WHERE agent_id = $1 AND version = $2`, inst.ID, target); err != nil {
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

func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication info not found", nil, "")
		return nil, false
	}
	ai, ok := v.(*auth.AuthInfo)
	if !ok || ai == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return ai, true
}

func mustAgent(c *gin.Context, info *auth.AuthInfo) (*models.Agent, bool) {
	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return nil, false
	}
	if inst == nil {
		xhttp.ErrJSON(c, "Agent not found", nil, "")
		return nil, false
	}
	return inst, true
}
