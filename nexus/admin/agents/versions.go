package agents

// Agent versions: {schema}.agent_versions.
//
// What differs from flow / skill / tool (and why this snapshot does three extra things):
//  1. the agent directory also holds private skills (agents/<a>/skills/**) - they have no version
//     number of their own and are frozen with this agent version (see nexus/docs/terminology.md);
//  2. the **metadata** of a private skill (description / callable by users / visibility / tool
//     dependencies) goes into the snapshot as well, otherwise "edit the description without
//     releasing" changes what the model sees - freezing the body but not the metadata contradicts itself;
//  3. deprecated private skills (status=3) stay out of the snapshot: keeping the body would leave a
//     way back in - at run time the model cats it under /agent and uses it regardless.

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

// agentVersionView is a list item: summary only, no file bodies.
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
	PrivateSkills int       `json:"private_skills"` // number of private skills in the snapshot
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the version history: "+err.Error(), "")
		return
	}
	defer rows.Close()

	out := make([]agentVersionView, 0)
	for rows.Next() {
		var v agentVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.Description, &v.IsCurrent, &v.Note,
			&v.CreatedBy, &v.CreatedByName, &v.CreatedAt, &v.FilesCount, &v.TotalBytes,
			&v.PrivateSkills); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to read the version history: "+err.Error(), "")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the version history: "+err.Error(), "")
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
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot release a version (switch it back to Active first)", "")
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

	ctx := context.Background()
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	srcKey := paths.AgentsKey(info.Schema, inst.Name)

	// Deprecated private skills stay out of the snapshot: the body remains in the draft, but a released version must not carry it.
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
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot release a version (switch it back to Active first)", "")
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to freeze the version: "+err.Error(), "")
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
// Apply = write this version's definition files back to the draft, leaving is_current alone.
//
// The key difference from skill / tool: **skills/ is not wiped**.
//   - a private skill is an entity of its own (it has a row in the skills table), so one missing
//     from the version may simply have been created after the release: wiping the directory would
//     leave an empty skill (a row without a body). The ones the version has come from the manifest.
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
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot apply a version (switch it back to Active first)", "")
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
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot apply a version (switch it back to Active first)", "")
		return
	}

	// Apply = the whole definition back to the draft: besides AGENT.md the version also holds the agent metadata and the private skill metadata and dependencies.
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

	draftKey := paths.AgentsKey(info.Schema, inst.Name)
	srcKey := paths.VersionKey(info.Schema, paths.VersionAgents, inst.Name, target)

	// Clear everything outside skills/ in the draft: AGENT.md and its sibling files are rebuilt from
	// the version, while the whole skills/ tree is kept (see the function comment).
	// A missing directory (or a failed listing) means "nothing to clear": the real error surfaces in the restore step below.
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to apply the version: "+err.Error(), "")
		return
	}
	// Metadata write-back: with the bodies alone the UI would still show "modified", because the description / config / private skill metadata would still differ.
	if _, err := tx.Exec(`UPDATE `+info.Schema+`.agents
		SET description = $2, config = $3, tools_unrestricted = $4, skill_discovery = $5,
		    updated_at = now()
		WHERE id = $1`,
		inst.ID, desc, rawConfig, toolsUnrestricted, discovery); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the agent metadata back: "+err.Error(), "")
		return
	}
	// Private skills: write back only the ones this version has (one missing from the snapshot was created after the release, leave it alone).
	if err := versioning.RestoreAgentPrivateSkills(tx, info.Schema, inst.ID, rawSkills); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the private skill metadata back: "+err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	warnings := make([]string, 0, len(missing))
	for _, p := range missing {
		warnings = append(warnings, "file missing from the snapshot: "+p)
	}
	// A private skill present in the draft but not in this version is kept and named in the warnings (it is an entity of its own; deleting it would leave an empty skill).
	if kept, kerr := versioning.KeptPrivateSkills(ctx, st, draftKey, rawSkills); kerr == nil {
		for _, name := range kept {
			warnings = append(warnings, fmt.Sprintf("private skill %q is not part of this version; the draft copy was kept (not deleted)", name))
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackAgentVersion POST /agents/:id/versions/:version/rollback
//
// Point "the version in use" at this historical version; production changes immediately.
//
// Division of labour with apply (same as flows): rollback only flips is_current, adds no version and
// **leaves the draft alone**; apply writes the version back to the draft (production untouched), which
// is what you want when continuing from an old version. Without rollback, a revert would have to be "apply then release", overwriting the draft in progress and inventing a version number.
func RollbackAgentVersion(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	inst, ok := mustAgent(c, info)
	if !ok {
		return
	}
	// Switching a version changes production behaviour, so it follows the release rule: a deprecated agent cannot do it.
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot switch versions (switch it back to Active first)", "")
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

	// Lock the target version row: this serialises against a concurrent release, so a rollback that just happened is not overwritten by it.
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+agentVersionTable(info.Schema)+`
		WHERE agent_id = $1 AND version = $2 FOR UPDATE`, inst.ID, target).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
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
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot delete a version (switch it back to Active first)", "")
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
		xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
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
			xhttp.ErrCode(c, "agent.last_version_undeletable", gin.H{"version": target}, fmt.Sprintf("v%d is the only version and cannot be deleted; release a new version before cleaning up history", target), "")
			return
		}
		xhttp.ErrJSON(c, versions.ErrCannotDeleteCurrentVersion.Error(), nil, "")
		return
	}

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionAgents, inst.Name, target)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the version directory: "+err.Error(), "")
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

// ===== internal helpers =====

func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
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
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return nil, false
	}
	return inst, true
}
