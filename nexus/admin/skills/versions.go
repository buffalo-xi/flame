package skills

// Skill versions: {schema}.skill_versions.
//
// Only **tenant-level** skills are covered (skills.agent_id IS NULL). A private skill has no
// version number of its own - its directory lives inside the agent root, so its version number is
// the version number of the owning agent (the content lands in
// versions/agents/<agent>/v<n>/skills/<skill>/). The reasoning is in nexus/docs/terminology.md:
// a private skill is the only content the agent itself can write, so giving it an independent
// version number plus self-release rights would be a back door; bundling it into the agent version
// means a skill change only takes effect through an agent release, consistent with D32's "no
// self-release".

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

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

// skillVersionView is a list item (no file bodies, only the summary).
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

// skillDraftKey is the draft directory of a tenant-level skill (the version domain sits next to
// it, see the version-domain comment in shared/paths).
func skillDraftKey(schema, name string) string {
	return path.Join(paths.SkillsKey(schema), name)
}

// ListSkillVersions GET /skills/:id/versions
func ListSkillVersions(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "read the version history")
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the version history: "+err.Error(), "")
		return
	}
	defer rows.Close()

	out := make([]skillVersionView, 0)
	for rows.Next() {
		var v skillVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.Description, &v.IsCurrent, &v.Note,
			&v.CreatedBy, &v.CreatedByName, &v.CreatedAt, &v.FilesCount, &v.TotalBytes); err != nil {
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

// ReleaseSkillVersion POST /skills/:id/versions  body: {"note": "..."}
//
// Release = freeze skills/<name>/ into versions/skills/<name>/v<n> and make it current.
// The copy happens inside a transaction holding the entity row lock (so a concurrent rename or
// release serialises with it).
func ReleaseSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "release a version")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot release a version (switch it back to normal first)", "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	ctx := context.Background()
	srcKey := skillDraftKey(info.Schema, s.Name)

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.skills WHERE id = $1 FOR UPDATE`, s.ID).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot release a version (switch it back to normal first)", "")
		return
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM `+skillVersionTable(info.Schema)+
		` WHERE skill_id = $1`, s.ID).Scan(&next); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	dstKey := paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, next)
	if err := versions.EnsureAbsent(ctx, st, dstKey); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	files, err := versions.Snapshot(ctx, st, srcKey, dstKey, paths.SkillEntryFile, nil)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "failed to freeze the version: "+err.Error(), "")
		return
	}
	rawFiles, err := versions.Encode(files)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// Tool dependencies are the skill's **declaration** and part of its definition: they must be
	// frozen with this version. Without them, a production use_skill reads an empty dependency set
	// - the body is there, the tools never arrive, and nothing reports an error.
	deps, derr := versioning.SkillToolDepsSnapshot(info.Schema, s.ID)
	if derr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the tool dependencies: "+derr.Error(), "")
		return
	}

	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = false WHERE skill_id = $1 AND is_current`, s.ID); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
// Apply = write that version's content back into the draft directory without touching is_current
// (production keeps running the current version).
// The skill directory is the entity, so it is cleared and copied back wholesale; every manifest
// sha256 is verified.
func ApplySkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "apply a version")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot apply a version (switch it back to normal first)", "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	ctx := context.Background()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.skills WHERE id = $1 FOR UPDATE`, s.ID).
		Scan(&status); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if !models.IsModifiable(status) {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot apply a version (switch it back to normal first)", "")
		return
	}

	// Apply = the whole definition back into the draft: besides the body, the metadata and the
	// dependency declaration are frozen in the version too
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
		xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	files, err := versions.Decode(rawFiles)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	draftKey := skillDraftKey(info.Schema, s.Name)
	srcKey := paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, target)
	if err := st.DeletePrefix(ctx, draftKey); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to clear the draft directory: "+err.Error(), "")
		return
	}
	missing, err := assetpack.RestoreFiles(ctx, st, srcKey, draftKey, files)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to apply the version: "+err.Error(), "")
		return
	}
	// Write the metadata and the dependency declaration back too: copying only the body would leave
	// the UI showing "modified" after an apply (description / visibility / dependencies would still
	// differ from the version), and the user would think the apply did nothing.
	m.Name = s.Name
	if err := versioning.RestoreSkillRow(tx, info.Schema, s.ID, m); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the skill metadata back: "+err.Error(), "")
		return
	}
	// The visibility declaration rolls back with the version while the allow-list (grants) stays as
	// it is - restricted=true with an empty allow-list would mean "nobody can discover it", so it is
	// healed to visible-after-apply and named in warnings.
	visibilityReset, verr := versioning.EnsureSkillVisibility(tx, info.Schema, s.ID)
	if verr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to verify skill visibility: "+verr.Error(), "")
		return
	}
	if err := versioning.RestoreSkillDeps(tx, info.Schema, s.ID, rawDeps); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the tool dependencies back: "+err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	warnings := make([]string, 0, len(missing)+1)
	for _, p := range missing {
		warnings = append(warnings, "missing file in the snapshot: "+p)
	}
	if visibilityReset {
		warnings = append(warnings, "this version's visibility is \"allow-list\", but the current allow-list is empty (grants do not roll back with versions) - "+
			"it was set back to \"everyone can see it\" so nobody is left unable to find it")
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackSkillVersion POST /skills/:id/versions/:version/rollback
//
// Point "the version in use" at that historical version; it takes effect in production immediately.
//
// Division of labour with apply (same as flows): rollback only flips is_current, adds no version
// and **leaves the draft alone**; apply writes that version back into the draft (leaving production
// alone).
func RollbackSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "switch versions")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot switch versions (switch it back to normal first)", "")
		return
	}
	target, err := strconv.Atoi(c.Param("version"))
	if err != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var one int
	if err := tx.QueryRow(`SELECT 1 FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2 FOR UPDATE`, s.ID, target).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = false WHERE skill_id = $1 AND is_current`, s.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+skillVersionTable(info.Schema)+`
		SET is_current = true WHERE skill_id = $1 AND version = $2`, s.ID, target); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteSkillVersion DELETE /skills/:id/versions/:version
//
// History only: the current version is refused (it is the definition source of live calls) and so
// is the only version (there is nothing to switch to).
// Directory first, row second: numbers are never reused, and a row without its directory would
// make the next release hit ErrVersionExists.
func DeleteSkillVersion(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	s, ok := mustTenantSkill(c, info, "delete a version")
	if !ok {
		return
	}
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot delete a version (switch it back to normal first)", "")
		return
	}
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	ctx := context.Background()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var isCurrent bool
	err = tx.QueryRow(`SELECT is_current FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2`, s.ID, target).Scan(&isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		xhttp.ErrCode(c, "version.not_found", nil, "version not found", "")
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if isCurrent {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+skillVersionTable(info.Schema)+
			` WHERE skill_id = $1`, s.ID).Scan(&total); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if total <= 1 {
			xhttp.ErrCode(c, "skill.last_version_undeletable", gin.H{"version": target}, fmt.Sprintf("v%d is the only version and cannot be deleted; release a new version first if the history needs cleaning up", target), "")
			return
		}
		xhttp.ErrCode(c, "version.in_use", nil, versions.ErrCannotDeleteCurrentVersion.Error(), "")
		return
	}

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionSkills, s.Name, target)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the version directory: "+err.Error(), "")
		return
	}
	if _, err := tx.Exec(`DELETE FROM `+skillVersionTable(info.Schema)+`
		WHERE skill_id = $1 AND version = $2`, s.ID, target); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// ===== internal helpers =====

// authInfo reads the auth info (the handlers already in this package inline this; new code goes
// through here).
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	return info, true
}

// mustTenantSkill loads a skill and rejects a "private skill" - it has no version number of its own.
//
// The message must spell out the way out (release the owning agent), otherwise the caller is left
// with a bare "not supported".
func mustTenantSkill(c *gin.Context, info *auth.AuthInfo, action string) (*models.Skill, bool) {
	s, err := skillRepo.Get(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return nil, false
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return nil, false
	}
	if string(s.AgentID) != "" {
		// data.owner stays language-neutral: the value is the agent name (or its id when the name
		// cannot be resolved), and the wording lives in the frontend locale bundles
		owner := string(s.AgentID)
		if name, rerr := resolveAgentName(info.Schema, string(s.AgentID)); rerr == nil && strings.TrimSpace(name) != "" {
			owner = strings.TrimSpace(name)
		}
		xhttp.ErrCode(c, "skill.private_no_version", gin.H{"owner": owner, "action": action},
			fmt.Sprintf("a private skill has no version number of its own: its version is the version of agent %s. Release agent %s instead; you cannot %s for a single private skill",
				owner, owner, action), "")
		return nil, false
	}
	return s, true
}

// removeSkillVersions clears a tenant-level skill's version domain when it is deleted or recreated
// (idempotent).
//
// Private skills do not come here: they have no version directory of their own, and their content
// is cleared together with the owning agent's version directory.
func removeSkillVersions(schema, name string) {
	st, err := getStorage()
	if err != nil {
		return
	}
	_ = st.DeletePrefix(context.Background(), paths.VersionEntityKey(schema, paths.VersionSkills, name))
}
