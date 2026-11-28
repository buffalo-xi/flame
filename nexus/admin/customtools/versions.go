package customtools

// Custom tool versions: {schema}.tool_versions.
//
// Only two things differ from agent / skill versions; the rest (release / list / apply /
// delete) is isomorphic:
//  1. a tool's name cannot change after creation (it is already written into
//     agent_tool_grants.ref), so there is no rename migration and version directories are
//     keyed by name only;
//  2. a tool is "the directory is the entity, TOOL.json is the definition" (see
//     shared/customtoolstore). A version must carry a valid TOOL.json, otherwise the
//     definition read back from the snapshot degrades to the table row - so if the draft
//     lacks or has a broken one at release time, write one from the merged view into the
//     version directory, and only into the snapshot, leaving the draft alone.
//
// Config values (configs) are not versioned: they are runtime configuration, not
// definition. Version rows store '{}', and applying a historical version never restores
// someone else's secrets together with it.

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

// toolVersionView is a list item. It does not return the file bodies (there may be
// hundreds); two aggregate numbers are enough.
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

// toolVerStore is the storage instance used for tool versions.
//
// It always uses the local filesystem implementation on rootDir: the tool pipeline already
// requires host paths to be reachable (the scripts are the sandbox mount source), tools do
// not run at all on an S3 deployment, and versions have no reason to grow a second backend.
func toolVerStore() storage.Storage {
	return storage.NewFilesystemStorage(config.Cfg.Storage.RootDir)
}

// toolVersionTable is the schema-qualified version table name.
func toolVersionTable(schema string) string { return schema + ".tool_versions" }

// ListToolVersions GET /customtools/:name/versions
func ListToolVersions(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")

	// Existence is judged by the policy row: if the draft directory was deleted by mistake,
	// the historical versions must still be manageable (they live in tool_versions).
	// Judging by the draft directory would make the version history 404 along with the draft.
	var toolID string
	if err := db.DB.QueryRow(`SELECT id FROM `+info.Schema+`.custom_tools WHERE name = $1`, name).
		Scan(&toolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the custom tool: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the version history: "+err.Error(), "")
		return
	}
	defer rows.Close()

	out := make([]toolVersionView, 0)
	for rows.Next() {
		var v toolVersionView
		if err := rows.Scan(&v.Version, &v.Name, &v.DisplayName, &v.Description, &v.Lang,
			&v.Entry, &v.IsCurrent, &v.Note, &v.CreatedBy, &v.CreatedAt,
			&v.FilesCount, &v.TotalBytes); err != nil {
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

// ReleaseToolVersion POST /customtools/:name/versions  body: {"note": "..."}
//
// Release = freeze the current draft directory into versions/tools/<name>/v<n> and make it
// the current version. The copy happens inside a transaction holding the entity row lock, so
// concurrent releases queue up for their number here and never compute the same n and collide
// on the primary key.
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the custom tool: "+err.Error(), "")
		return
	}
	if t.Status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot release a version; switch it back to normal first", "")
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

	// "The directory is the entity" allows a tool on disk with no table row. The version row
	// has a foreign key on tool_id, so the policy row is created first (a missing row means
	// normal status with no config, see customtoolstore).
	if t.ID == "" {
		if err := store.Upsert(t); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to register the tool: "+err.Error(), "")
			return
		}
	}
	if t.ID == "" {
		xhttp.ErrCode(c, "common.internal", nil, "the tool is not registered yet (no id), so a version cannot be released", "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()
	draftKey, err := paths.EntityDraftKey(info.Schema, paths.VersionTools, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the entity row: release serialises with apply / delete version, and a status change
	// in the middle of a release is blocked too
	var status int
	if err := tx.QueryRow(`SELECT status FROM `+info.Schema+`.custom_tools WHERE id = $1 FOR UPDATE`, t.ID).
		Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errToolNotFound(c)
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot release a version; switch it back to normal first", "")
		return
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM `+toolVersionTable(info.Schema)+
		` WHERE tool_id = $1`, t.ID).Scan(&next); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	dstKey := paths.VersionKey(info.Schema, paths.VersionTools, name, next)
	if err := versions.EnsureAbsent(ctx, st, dstKey); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	files, err := versions.Snapshot(ctx, st, draftKey, dstKey, t.Entry, nil)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "failed to freeze the version: "+err.Error(), "")
		return
	}

	// The snapshot must carry a valid TOOL.json: write the merged view and refresh the manifest digest (see the file header)
	norm := models.ToolFileOf(t)
	data, merr := models.MarshalToolFile(norm)
	if merr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "failed to build the contract file: "+merr.Error(), "")
		return
	}
	hostDst := filepath.Join(config.Cfg.Storage.RootDir, filepath.FromSlash(dstKey))
	if werr := toolfile.WriteAt(hostDst, norm); werr != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the version contract file: "+werr.Error(), "")
		return
	}
	files = versions.PutEntry(files, models.ToolFileName, data)

	rawFiles, err := versions.Encode(files)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = false WHERE tool_id = $1 AND is_current`, t.ID); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	if err := tx.Commit(); err != nil {
		_ = st.DeletePrefix(ctx, dstKey)
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
// Apply = write that version's content back into the draft directory, **leaving is_current
// alone** (production is untouched). It is separate from rollback: apply means "keep editing
// from this older version as a starting point", and a release is still needed for it to take
// effect.
//
// The tool directory is the entity itself, so it is cleared and copied back wholesale; every
// sha256 in the manifest is verified, and a mismatch means the snapshot was modified, so this
// fails instead of spreading broken files into the draft.
func ApplyToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()
	draftKey, err := paths.EntityDraftKey(info.Schema, paths.VersionTools, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	srcKey := paths.VersionKey(info.Schema, paths.VersionTools, name, target)

	// Lock the entity row: serialise with release so the draft cannot be rewritten halfway through the copy
	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot apply a version; switch it back to normal first", "")
		return
	}

	// Apply = the whole definition back to the draft: besides the script directory, the contract
	// metadata (parameter schema / entry / timeout ...) lives in the version too
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	files, err := versions.Decode(rawFiles)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	if err := st.DeletePrefix(ctx, draftKey); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to clear the draft directory: "+err.Error(), "")
		return
	}
	missing, err := assetpack.RestoreFiles(ctx, st, srcKey, draftKey, files)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to apply the version: "+err.Error(), "")
		return
	}
	// Write the contract metadata back: copying only the scripts would still show "modified"
	// after an apply (the parameter schema / description would differ).
	// configs stay untouched - they are runtime values, not definition.
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the tool contract back: "+err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	warnings := make([]string, 0, len(missing))
	for _, p := range missing {
		warnings = append(warnings, "missing file in the snapshot: "+p)
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target, "warnings": warnings}, "")
}

// RollbackToolVersion POST /customtools/:name/versions/:version/rollback
//
// Point "the version in use" at that historical version; it takes effect in production
// immediately.
//
// Division of labour with apply (same as flows): rollback only flips is_current, adds no
// version and **leaves the draft alone**; apply writes that version back into the draft
// (leaving production alone).
func RollbackToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}
	t, err := storeFor(info.Schema).Get(info.Schema, name)
	if errors.Is(err, sql.ErrNoRows) || t == nil {
		errToolNotFound(c)
		return
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the custom tool: "+err.Error(), "")
		return
	}
	// Switching versions changes production behaviour, so it follows the release rule
	if t.Status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot switch versions; switch it back to normal first", "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = false WHERE tool_id = $1 AND is_current`, t.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if _, err := tx.Exec(`UPDATE `+toolVersionTable(info.Schema)+`
		SET is_current = true WHERE tool_id = $1 AND version = $2`, t.ID, target); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// DeleteToolVersion DELETE /customtools/:name/versions/:version
//
// History only: production still runs the is_current version, and the draft is untouched.
// UnreleaseToolVersion POST /customtools/:name/versions/unrelease
//
// Clears is_current only: the version row and its directory stay, the draft stays, and a production run simply has no
// definition to load until the next release. Deleting the tool needs this step first: a delete must not be the thing that
// takes the tool out of the catalog.
func UnreleaseToolVersion(c *gin.Context) {
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
		xhttp.ErrCode(c, "common.internal", nil, "failed to read the custom tool: "+err.Error(), "")
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	defer func() { _ = tx.Rollback() }()

	version, err := versions.Unrelease(tx, toolVersionTable(info.Schema), "tool_id", t.ID)
	if err != nil {
		if errors.Is(err, versions.ErrNoReleasedVersion) {
			xhttp.ErrCode(c, "version.none", nil, "nothing is released, so there is nothing to unrelease", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"version": version}, "")
}

// The current version is always rejected - it is the definition source of live calls; the only
// version is rejected too (there is nothing to switch to).
//
// The order is directory first, row second: with the row gone the directory can be re-released
// to fill the hole, while a row without a directory would make the next release hit
// ErrVersionExists (numbers are never reused, so MAX+1 lands exactly on that hole).
func DeleteToolVersion(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	target, perr := strconv.Atoi(c.Param("version"))
	if perr != nil || target <= 0 {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid version", "")
		return
	}

	ctx := context.Background()
	st := toolVerStore()

	tx, err := db.DB.Begin()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if status != models.StatusNormal {
		xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot delete a version; switch it back to normal first", "")
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
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if isCurrent {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM `+toolVersionTable(info.Schema)+
			` WHERE tool_id = $1`, toolID).Scan(&total); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if total <= 1 {
			xhttp.ErrCode(c, "tool.last_version_undeletable", gin.H{"version": target},
				fmt.Sprintf("v%d is the only version and cannot be deleted; release a new version first if the history needs cleaning up", target), "")
			return
		}
		xhttp.ErrCode(c, "version.in_use", nil, versions.ErrCannotDeleteCurrentVersion.Error(), "")
		return
	}

	if err := st.DeletePrefix(ctx, paths.VersionKey(info.Schema, paths.VersionTools, name, target)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the version directory: "+err.Error(), "")
		return
	}
	if _, err := tx.Exec(`DELETE FROM `+toolVersionTable(info.Schema)+`
		WHERE tool_id = $1 AND version = $2`, toolID, target); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := tx.Commit(); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// The last version is gone, so versions/tools/<name> is empty now: drop it too, otherwise a fully deleted history leaves
	// a path that still looks like an entity. Best effort - the row is already committed.
	versions.PruneEmptyEntityDir(ctx, st, paths.VersionEntityKey(info.Schema, paths.VersionTools, name))
	xhttp.OkJSON(c, "Success", gin.H{"version": target}, "")
}

// PurgeToolVersions clears a tool's version domain when the tool is deleted (idempotent).
//
// They are deleted along with the draft directory because version directories are keyed by
// name: recreating a same-named tool would "inherit" the old entity's history (no rows in the
// database, but the disk keeps them, and release order and packaging would both be off).
func PurgeToolVersions(schema, name string) {
	st := toolVerStore()
	_ = st.DeletePrefix(context.Background(), paths.VersionEntityKey(schema, paths.VersionTools, name))
}
