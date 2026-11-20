package share

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/repository/customtools"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/secureval"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type installRequest struct {
	AssetID string `json:"asset_id"`
	Name    string `json:"name"`   // empty = keep the asset name
	Status  *int   `json:"status"` // empty = 1 (all four kinds default to enabled; custom tools are no longer disabled by default)
	// Overwrite replaces a same-named entity the target tenant already has (instead of asking for a rename).
	// All four kinds support it (see the installXxx helpers): an overwrite replaces only the content the bundle
	// brings; identity, grants, exposure configuration and version history are all kept.
	Overwrite bool `json:"overwrite"`
}

// installSource is where one install reads its content from.
//
// Both entry points share it: installing from a platform asset (snapDir = platform-assets/<id>/<ver>, with an
// asset row) and importing an uploaded bundle (its content already laid out in a tenant temp directory,
// asset nil). Conflict handling, quota, capability checks, default status and warnings are identical; only
type installSource struct {
	kind     assetpack.Kind
	manifest assetpack.Manifest
	payload  map[string]any
	snapDir  string
	asset    *sharedassets.Asset // nil = not a platform asset (an uploaded bundle); no install record
	notices  []string            // notes from the entry point (e.g. blanked credentials in an uploaded bundle), merged into the response warnings
	// overwritten is set by the installXxx helpers when the overwrite branch actually ran; returned to the front end.
	overwritten bool
}

// Install copies a platform asset into an entity of this tenant.
//
// The order is deliberately "files first, DB second": deleting files is idempotent and a DB rollback costs
// more; any failed step tries to remove the files already laid out, leaving no orphan directory behind.
func Install(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	asset, m, kind, name, rerr := resolveInstallTarget(info, req.AssetID, req.Name)
	if rerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, rerr.Error(), "")
		return
	}

	installFrom(c, info, &installSource{
		kind:     kind,
		manifest: m,
		payload:  payloadFromAsset(asset),
		snapDir:  paths.PlatformAssetKey(asset.ID, asset.Build),
		asset:    asset,
	}, name, req)
}

// resolveInstallTarget validates "can this asset be installed, and under what name".
//
// Install and InspectAsset share it: the preflight and the real install must apply the same rules, or
// the most annoying mismatch appears - "the preflight said yes, install was rejected".
// The returned error is the sentence shown to the user.
func resolveInstallTarget(info *auth.AuthInfo, assetID, wantName string) (asset *sharedassets.Asset,
	m assetpack.Manifest, kind assetpack.Kind, name string, err error) {

	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return nil, m, kind, name, errors.New("asset_id is required")
	}

	asset, err = sharedassets.Get(assetID)
	if err != nil {
		return nil, m, kind, name, errors.New(assetErrMsg("query asset", err))
	}
	if asset == nil {
		return nil, m, kind, name, errors.New("asset not found")
	}
	if asset.Status != sharedassets.StatusListed {
		return nil, m, kind, name, errors.New("this asset has not passed review yet (current status: " + asset.Status + ")")
	}
	visible, verr := sharedassets.VisibleTo(asset, info.Schema)
	if verr != nil {
		return nil, m, kind, name, errors.New("check visibility: " + verr.Error())
	}
	if !visible {
		return nil, m, kind, name, errors.New("this asset is not open to your tenant")
	}

	if len(asset.Manifest) > 0 {
		if uerr := json.Unmarshal(asset.Manifest, &m); uerr != nil {
			return nil, m, kind, name, errors.New("the asset manifest is corrupt: " + uerr.Error())
		}
	}
	// No file list means no content to install: a row alone gives an entity that exists but cannot be opened
	if len(m.Files) == 0 {
		return nil, m, kind, name, errors.New("the asset has no file list and cannot be installed (ask the provider to list it again)")
	}

	kind = assetpack.Kind(asset.Kind)
	name = strings.TrimSpace(wantName)
	if name == "" {
		name = asset.Name
	}
	// The same test as listing / export: workflow names use different wording (the name is the public tool name) but the same character set
	if verr := validatePlanName(kind, name); verr != nil {
		return nil, m, kind, name, verr
	}
	return asset, m, kind, name, nil
}

// payloadFromAsset decodes the payload of an asset row into a map (bad data becomes an empty object: missing display fields do not block an install).
func payloadFromAsset(a *sharedassets.Asset) map[string]any {
	payload := map[string]any{}
	if a != nil && len(a.Payload) > 0 {
		_ = json.Unmarshal(a.Payload, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload
}

// installFrom dispatches the install by kind (shared by platform assets and uploaded bundles).
func installFrom(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// The spec gate sits before the dispatch: one gate for all four kinds, and an unknown manifest version
	// is rejected outright. Silently dropping unknown fields installs an entity that runs but behaves
	// wrongly, which is harder to diagnose than a failed install. Old bundles without a spec (=0) count as 1.
	if src.manifest.Spec > assetpack.ManifestSpec {
		xhttp.ErrCode(c, "asset.package_too_new", map[string]any{
			"spec":      src.manifest.Spec,
			"supported": assetpack.ManifestSpec,
		}, fmt.Sprintf(
			"this bundle uses a newer manifest format (spec %d, this build supports up to %d): upgrade Nexus and install again",
			src.manifest.Spec, assetpack.ManifestSpec), "")
		return
	}

	switch src.kind {
	case assetpack.KindSkill:
		installSkill(c, info, src, name, req)
	case assetpack.KindTool:
		installTool(c, info, src, name, req)
	case assetpack.KindAgent:
		installAgent(c, info, src, name, req)
	case assetpack.KindWorkflow:
		installWorkflow(c, info, src, name, req)
	default:
		xhttp.ErrCode(c, "asset.invalid_kind", map[string]any{"kind": string(src.kind)},
			"unknown asset kind: "+string(src.kind), "")
	}
}

// ===== Skills =====

// installSkill installs a skill (always as a tenant-level skill, agent_id NULL).
//
// A name clash has two resolutions: by default a suggested name so the user renames (leaving the existing entity alone);
// entity alone); with req.Overwrite it **overwrites the existing skill** - the whole directory is laid down
// again, description / metadata / command settings come from the bundle and tool dependencies are replaced
//
//	wholesale. Three things deliberately do not travel:
//	restricted + agent_skill_bindings  visibility belongs to the target tenant's grant setup (an allowlist cannot be remapped across tenants)
//	status                             lifecycle state (an overwrite replaces content, not the way the skill exists)
func installSkill(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// No "protected name" check any more: protection constrains delete / rename only, never install.
	// Placing a protected name relies on the clash path - skill-authoring already exists in the target tenant,
	exist, ferr := skillRepo.FindByName(info.Schema, name, "")
	if ferr != nil {
		xhttp.ErrCode(c, "common.internal", nil, ferr.Error(), "")
		return
	}
	var prev *models.Skill
	if exist != nil {
		if !req.Overwrite {
			// so installing the same name goes through the clash handling below and the user confirms the overwrite.
			// A clash is never silent: can_overwrite turns it into "confirm overwrite" rather than a rename hint -
			xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "skill", "name": name},
				"this tenant already has a skill named "+name, gin.H{
					"suggested_name": suggestFreeSkillName(info.Schema, name),
					"can_overwrite":  true,
				}, "")
			return
		}
		prev = exist
	}
	// Private skills are not a clash: FindByName(agentID="") only sees tenant-level rows (agent_id IS NULL).

	// Quota preflight: an overwrite adds no entity, so skip it - otherwise a tenant at the limit could not even update its own skill.
	if prev == nil {
		if cnt, qerr := quota.CountSkills(info.Schema); qerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to count skills: "+qerr.Error(), "")
			return
		} else if qerr := models.CheckQuota("skill", cnt, quota.Resolve(info.Schema).MaxSkills); qerr != nil {
			le := qerr.(*models.LimitError)
			xhttp.ErrCode(c, "quota.skill_limit_reached", le.Params(), qerr.Error(), "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	ctx := context.Background()
	dstDir := paths.SkillsKey(info.Schema) + "/" + name
	if prev != nil {
		// Overwrite: delete the whole directory, then lay it down. Stacking on top leaves files only the old version
		// version had (a removed reference document "comes back" and is packed into the next export).
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to clean the existing skill directory: "+derr.Error(), "")
			return
		}
	} else {
		// New: clear a leftover version area for the same name. If the target tenant still holds an old version
		// directory of a same-named skill (an old delete removed only SKILL.md, or the path was not cleaned up),
		// the first release of this draft as v1 hits it and EnsureAbsent reports "version exists" - an error
		// pointing nowhere near the install. Not cleared on overwrite: that history belongs to the entity itself.
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionSkills, name)); verr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to clear the old version directory of the same-named skill: "+verr.Error(), "")
			return
		}
	}
	// Only the body: _deps/ holds dependent custom tools, which install under custom_tools/, not inside the skill directory
	missing, rerr := assetpack.RestoreFiles(ctx, st, src.snapDir, dstDir, assetpack.BodyFiles(src.manifest.Files))
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the skill files: "+rerr.Error(), "")
		return
	}

	// Only "normal / deprecated" are valid landing states (a skill has just two). On overwrite the default is
	// the target's existing state: an overwrite replaces content, not lifecycle - quietly turning a deprecated
	// skill back to normal makes it suddenly usable again in lists and grants.
	status := models.StatusNormal
	if prev != nil {
		status = int(prev.Status)
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	warnings := missingWarnings(missing)
	var id string
	if prev != nil {
		// Update writes only name / description / metadata / updated_by / user_invocable / command_*:
		// restricted and status have dedicated entry points (the allowlist is maintained by bind; SetVisibility
		// cannot stand in - it DELETE all of agent_skill_bindings inside a transaction and rebuilds it).
		meta := xsql.JSONB{Data: map[string]any{}}
		if md, ok := src.payload["metadata"].(map[string]any); ok && len(md) > 0 {
			meta = xsql.JSONB{Data: md}
		}
		commandDispatch, commandTool := normalizeCommand(
			asString(src.payload["command_dispatch"]), asString(src.payload["command_tool"]))
		upd := &models.Skill{
			ID:              string(prev.ID),
			Name:            name,
			Description:     xsql.String(asString(src.payload["description"])),
			Metadata:        meta,
			UserInvocable:   xsql.Bool(asBool(src.payload["user_invocable"])),
			CommandDispatch: xsql.String(commandDispatch),
			CommandTool:     xsql.String(commandTool),
			UpdatedBy:       xsql.UUID(info.ID),
		}
		if uerr := skillRepo.Update(info.Schema, upd); uerr != nil {
			// The directory already holds the new content, so it is **not** deleted here. With an old row and new
			// files the skill still opens, and one more import aligns them; deleting would leave a "row without
			xhttp.ErrCode(c, "asset.record_write_failed", map[string]any{"kind": "skill", "name": name},
				"failed to overwrite the skill record (files were written; retry the import): "+uerr.Error(), "")
			return
		}
		id = string(prev.ID)
		src.overwritten = true

		if int(prev.Status) != status {
			if sterr := skillRepo.UpdateStatus(info.Schema, id, status); sterr != nil {
				warnings = append(warnings, "the status could not be changed to the target value; the original is kept: "+sterr.Error())
			}
		}
		// Visibility (restricted + allowlist) keeps the target's existing configuration as a pair. The one repair:
		// a target that is itself "restricted=true with no binding at all" is invisible to everyone (both
		// visibleCond arms fail), so an overwrite heals it to visible-to-everyone instead of keeping the broken pair.
		if bool(prev.Restricted) {
			if bindings, berr := skillRepo.ListBindings(info.Schema, id); berr != nil {
				warnings = append(warnings, "failed to check skill visibility: "+berr.Error())
			} else if len(bindings) == 0 {
				if rerr := skillRepo.SetRestricted(info.Schema, id, false, info.ID); rerr != nil {
					warnings = append(warnings, "failed to repair skill visibility (it is now invisible to every agent): "+rerr.Error())
				} else {
					warnings = append(warnings, "the skill was set to \"visible to grantees only\" with no grant at all (nobody could find it); it is now visible to everyone")
				}
			}
		}
		if models.IsProtectedSkill(name) {
			warnings = append(warnings, "this overwrites a built-in skill (protected name): the content comes from the bundle, while protection follows the name")
		}
		warnings = append(warnings, "an overwrite does not touch the skill's visibility grants in the target tenant (which agents may use it); adjust them under skill grants")
	} else {
		var ierr error
		id, ierr = insertInstalledSkill(info, name, status, src.payload)
		if ierr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			xhttp.ErrCode(c, "common.internal", nil, "failed to write the skill record: "+ierr.Error(), "")
			return
		}
	}

	// Install the tools the bundle brings first, then write the dependencies - reversed, the grant rows point at tools that do not exist yet
	warnings = append(warnings, installBundledTools(info, src)...)
	if prev != nil {
		warnings = append(warnings, replaceInstalledSkillGrants(info, id, src.manifest.Deps.Tools)...)
	} else {
		warnings = append(warnings, writeSkillGrants(info, id, src.manifest.Deps.Tools)...)
	}
	respondInstalled(c, info, src, name, id, missing, warnings)
}

// insertInstalledSkill writes a skill row: a fresh identity and a forced empty owner (tenant level).
//
// restricted is always false: the visibility allowlist (agent_skill_bindings) cannot be remapped across
// tenants, so true would only produce a skill nobody is granted and nobody can find (both visibleCond arms
// fail). Visibility belongs to the target tenant's grant setup, like an agent's endpoints - it never travels; set it under skill grants after installing.
func insertInstalledSkill(info *auth.AuthInfo, name string, status int, payload map[string]any) (string, error) {
	id := uuid.NewString()

	meta, err := json.Marshal(payload["metadata"])
	if err != nil || string(meta) == "null" || string(meta) == "" {
		meta = []byte("{}")
	}
	now := time.Now()
	commandDispatch, commandTool := normalizeCommand(
		asString(payload["command_dispatch"]), asString(payload["command_tool"]))

	_, err = db.DB.Exec(`INSERT INTO `+tenantTable(info.Schema, "skills")+`
		(id, name, description, created_by, updated_by, metadata, status, restricted,
		 created_at, updated_at, agent_id, user_invocable, command_dispatch, command_tool)
		VALUES ($1,$2,$3,$4::uuid,$5::uuid,$6::jsonb,$7,$8,$9,$10,NULL,$11,$12,$13)`,
		id, name, asString(payload["description"]),
		info.ID, info.ID, string(meta), status, false,
		now, now,
		asBool(payload["user_invocable"]),
		commandDispatch, commandTool)
	if err != nil {
		return "", err
	}
	return id, nil
}

// writeSkillGrants rebuilds the tool dependencies from the manifest and returns the items to report.
//
// A missing ref deliberately does not fail the whole install: the skill body is usually still useful and the
// user can add the missing tools under tool grants. Say so explicitly, though, so nobody assumes they are live.
func writeSkillGrants(info *auth.AuthInfo, skillID string, deps []assetpack.ToolDep) []string {
	if len(deps) == 0 {
		return nil
	}
	var warnings []string
	failed := 0
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		if _, err := grantRepo.AddSkillGrant(info.Schema, skillID,
			models.SkillToolGrant{SkillID: skillID, ItemType: d.Type, Ref: d.Ref}); err != nil {
			failed++
			warnings = append(warnings, fmt.Sprintf("failed to write tool dependency %s: %v", d.Ref, err))
		}
	}
	if failed == 0 {
		warnings = append(warnings, fmt.Sprintf(
			"wrote %d tool dependencies as declared; any whose tool is missing in this tenant will not take effect - adjust them under tool grants", len(deps)))
	}
	return warnings
}

// replaceInstalledSkillGrants replaces the skill's tool dependencies **wholesale** on an overwrite install.
//
// The only difference from writeSkillGrants is the last step (ReplaceSkillGrants instead of one
// AddSkillGrant per item): an overwrite must drop dependencies the bundle does not declare, or the old ones keep applying to the new body.
func replaceInstalledSkillGrants(info *auth.AuthInfo, skillID string, deps []assetpack.ToolDep) []string {
	grants := make([]models.SkillToolGrant, 0, len(deps))
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		grants = append(grants, models.SkillToolGrant{SkillID: skillID, ItemType: d.Type, Ref: d.Ref})
	}
	if err := grantRepo.ReplaceSkillGrants(info.Schema, skillID, grants); err != nil {
		return []string{"failed to write tool dependencies: " + err.Error()}
	}
	if len(grants) == 0 {
		return []string{"tool dependencies were cleared per the bundle (it declares no tools)"}
	}
	return []string{fmt.Sprintf(
		"tool dependencies were replaced wholesale per the bundle (%d); any whose tool is missing in this tenant will not take effect - adjust them under tool grants", len(grants))}
}

// suggestFreeSkillName suggests a name nobody in this tenant uses (up to 50 attempts).
func suggestFreeSkillName(schema, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		s, err := skillRepo.FindByName(schema, cand, "")
		if err != nil || s == nil {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== Custom tools =====

// installTool installs a script tool.
//
// The big difference from skills: **the contract lives in a file** (TOOL.json). So after writing the files the
// contract is read back from the target directory and used to build the table row (configs empty, status enabled
// by default) instead of copying fields from the payload - which would let the file and the row disagree again.
//
// A name clash has two resolutions: by default a suggested name; with req.Overwrite the existing tool is
// overwritten - the scripts are laid down again and the contract fields come from the bundle, but **the configuration values and the creator are kept** (the configuration is credentials, and the bundle's configs are empty).
func installTool(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)
	_, derr := store.Get("", name)
	dirExists := derr == nil
	if derr != nil && !errors.Is(derr, sql.ErrNoRows) {
		xhttp.ErrCode(c, "common.internal", nil, "check tool: "+derr.Error(), "")
		return
	}
	// Check the table row too: a directory without a row (leftover from history) also counts as existing; an overwrite uses it to keep the config and the creator
	prev, rerr := customtools.NewRepository().Get(info.Schema, name)
	if rerr != nil && !errors.Is(rerr, sql.ErrNoRows) {
		xhttp.ErrCode(c, "common.internal", nil, "check tool: "+rerr.Error(), "")
		return
	}
	if (dirExists || prev != nil) && !req.Overwrite {
		xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "custom_tool", "name": name},
			"this tenant already has a tool named "+name, gin.H{
				"suggested_name": suggestFreeToolName(store, name),
				"can_overwrite":  true,
			}, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	// Usable right after install (no longer disabled by default): an install is an explicit user action, so the
	// tool must be callable afterwards; anyone who wants to review the script can disable it once installed.
	// On overwrite the default is the target's existing state - an overwrite replaces the script, not the decision about whether it is usable.
	status := models.StatusNormal
	if prev != nil {
		status = prev.Status
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 2) {
		status = *req.Status
	}

	if dirExists && prev == nil {
		// A directory without a row (leftover from history): clear it before laying files down, so old files do not
		// mix into this install. The row does not exist yet, so placeTool cannot see prev; do it here for it.
		if derr := st.DeletePrefix(context.Background(), paths.ToolsKey(info.Schema)+"/"+name); derr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to clean the leftover tool directory: "+derr.Error(), "")
			return
		}
	}

	id, missing, warnings, perr := placeTool(context.Background(), st, info, src,
		src.manifest.Files, "", name, status, prev)
	if perr != nil {
		xhttp.ErrCode(c, "common.internal", nil, perr.Error(), "")
		return
	}
	respondInstalled(c, info, src, name, id, missing, warnings)
}

// placeTool lays a tool from the snapshot into this tenant's directory and creates or overwrites its row (prev non-nil = overwrite).
//
// The marketplace install and "tools brought along by an agent / skill" share it: the contract lives in a file
// (TOOL.json), so the files must land first and the contract is read back from the target directory; every row
// field follows the contract - copying from the payload would let file and row disagree. The capability check lives here too: installing a tool that cannot run is more confusing than a failed install.
//
// prefix is used for bundle attachments (_deps/custom_tools/<name>/) and is empty for a normal install.
func placeTool(ctx context.Context, st storage.Storage, info *auth.AuthInfo, src *installSource,
	files []assetpack.FileEntry, prefix, name string, status int, prev *models.CustomTool) (id string, missing, warnings []string, err error) {

	caps := capabilitypolicy.Snapshot(info.Schema)
	if !caps[capability.Exec] {
		return "", nil, nil, errors.New("this tenant has not enabled the one-shot execution (sandbox.exec) capability, so a script tool would install but never run")
	}

	dstDir := paths.ToolsKey(info.Schema) + "/" + name
	if prev != nil {
		// Overwrite: delete the whole directory, then lay it down. Stacking on top leaves scripts and data files
		// only the old version had (files removed from the bundle "come back" and travel into the next export).
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			return "", nil, nil, fmt.Errorf("failed to clean the existing tool directory: %w", derr)
		}
	} else {
		// New: the version area is keyed by name, so a leftover directory makes the first release of this tool as
		// v1 hit EnsureAbsent (an error pointing nowhere near the install). This path also serves bundled custom tools, so both are handled once.
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionTools, name)); verr != nil {
			return "", nil, nil, fmt.Errorf("failed to clear the old version directory of the same-named tool: %w", verr)
		}
	}

	missing, rerr := assetpack.RestoreFilesUnder(ctx, st, src.snapDir, dstDir, prefix, files)
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("failed to write the tool files: %w", rerr)
	}

	tf, cerr := adoptToolContract(ctx, st, dstDir, name)
	if cerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("the tool contract is unusable: %w", cerr)
	}
	if tf.Egress && !caps[capability.Egress] {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, errors.New("this tool declares egress but this tenant has not enabled net.egress")
	}

	t := models.CustomTool{
		Name:      name,
		Status:    status,
		Configs:   xsql.JSONB{Data: map[string]any{}}, // values never travel with the asset; the user fills them in
		Egress:    tf.Egress,
		CreatedBy: info.ID,
	}
	if prev != nil {
		// Overwrite: identity, creator and configuration values must all be kept - the configuration is credentials,
		// the bundle's configs are empty (values never travel), and clearing them would break the tool on the spot.
		t.ID = prev.ID
		t.CreatedBy = prev.CreatedBy
		t.Configs = prev.Configs
	}
	tf.Apply(&t)

	repo := customtools.NewRepository()
	if prev != nil {
		if uerr := repo.Update(info.Schema, &t); uerr != nil {
			// The directory already holds the new script while the row is old: do not delete the directory (that gives "directory without row"); retrying the import aligns them
			return "", nil, nil, fmt.Errorf("failed to overwrite the tool record (files were written; retry the import): %w", uerr)
		}
	} else if cerr := repo.Create(info.Schema, &t); cerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("failed to write the tool record: %w", cerr)
	}

	warnings = missingWarnings(missing)
	if len(tf.ConfigFields) > 0 {
		if prev != nil {
			warnings = append(warnings, fmt.Sprintf(
				"this tool declares %d settings: existing values are kept as they are, and any setting the contract adds must be filled in", len(tf.ConfigFields)))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"this tool has %d settings to fill in; a call fails while they are empty", len(tf.ConfigFields)))
		}
	}
	return t.ID, missing, warnings, nil
}

// installBundledTools installs the custom tools bundled in a package into this tenant (enabled, same as a single install).
//
// Three "do nots" matter more than the "dos":
//   - never overwrite a same-named tool in the target - the local copy may have edited scripts or filled in
//     settings, and clobbering someone's tool while installing an agent is unacceptable (overwrite only applies to the single-install entry point; the bundled path always passes nil);
//   - a missing sandbox.exec / net.egress skips the tool with an explanation instead of failing the install;
//   - a failed single tool is only a warning: the agent itself is already installed, and rolling back for it is not worth it.
func installBundledTools(info *auth.AuthInfo, src *installSource) []string {
	attachments := assetpack.Attachments(src.manifest.Files)
	if len(attachments) == 0 {
		return nil
	}

	st, serr := getStorage()
	if serr != nil {
		return []string{"storage not ready, bundled custom tools were not installed: " + serr.Error()}
	}
	ctx := context.Background()
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)

	names := make([]string, 0, len(attachments))
	for name := range attachments {
		names = append(names, name)
	}
	sort.Strings(names)

	var warnings []string
	installed := make([]string, 0, len(names))
	for _, name := range names {
		// The name goes straight into a directory path: attachments come from a zip and cannot be assumed valid
		if err := assetpack.ValidateName(name); err != nil {
			warnings = append(warnings, "bundled custom tool name is invalid, not installed: "+name)
			continue
		}
		if _, gerr := store.Get("", name); gerr == nil {
			warnings = append(warnings, fmt.Sprintf(
				"this tenant already has a tool named %s; the local copy is kept (the bundled one did not overwrite it)", name))
			continue
		} else if !errors.Is(gerr, sql.ErrNoRows) {
			warnings = append(warnings, fmt.Sprintf("failed to check tool %s, not installed: %v", name, gerr))
			continue
		}

		_, missing, w, perr := placeTool(ctx, st, info, src, attachments[name],
			assetpack.ToolAttachmentPrefix(name), name, models.StatusNormal, nil)
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("failed to install bundled custom tool %s: %v", name, perr))
			continue
		}
		warnings = append(warnings, missing...)
		warnings = append(warnings, w...)
		installed = append(installed, name)
	}
	if len(installed) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"installed %d custom tools with the bundle (%s), all enabled; disable one under custom tools to pause it",
			len(installed), strings.Join(installed, ", ")))
	}
	return warnings
}

// adoptToolContract reads TOOL.json back from the target directory, rewrites it with the target name and validates it.
//
// Installing under a new name must rewrite name inside the contract: Validate requires "contract name ==
// directory name", and without the rewrite every load of this tool reports a name / directory mismatch.
func adoptToolContract(ctx context.Context, st storage.Storage, dir, name string) (*models.ToolFile, error) {

	raw, err := st.ReadFile(ctx, path.Join(dir, models.ToolFileName))
	if err != nil {
		return nil, fmt.Errorf("%s is missing", models.ToolFileName)
	}
	tf, err := models.ParseToolFile(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tf.Name) != name {
		tf.Name = name
		b, merr := models.MarshalToolFile(tf)
		if merr != nil {
			return nil, merr
		}
		if werr := st.WriteFile(ctx, path.Join(dir, models.ToolFileName), b); werr != nil {
			return nil, werr
		}
	}
	if err := tf.Validate(name); err != nil {
		return nil, err
	}
	return tf, nil
}

// suggestFreeToolName suggests a tool name free in this tenant (a directory or a table row both count as taken).
func suggestFreeToolName(store *customtoolstore.Store, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		if _, err := store.Get("", cand); errors.Is(err, sql.ErrNoRows) {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== Agents =====

// installAgent installs an agent: the body files, one agents row, private skills, skill bindings and tool grants.
//
// Private skills are restored with the directory, and restorePrivateSkills rebuilds their rows from
// payload.private_skills (with ownership pointing at the new agent).
//
// Three switches (is_supervisor / tools_unrestricted / skill_discovery) and the default reply language
// (default_language) **travel with the bundle**: they decide how the agent runs in a tenant, and leaving them behind
// installs a copy that behaves differently. The cost is
// that "import" and "ticking a box on the edit page" reach the same permission surface - they always were
// the same surface (the same tenant admin API), so no extra gate is added; the result is written into warnings so it is visible.
//
// Only these stay behind:
//
//	agent_relations  cannot be remapped across tenants; the install tells the user to reconfigure it
//	endpoints        exposure belongs to the target tenant; an overwrite keeps the old value
//
// A name clash has two resolutions: by default a suggested name so the user renames (leaving the existing entity alone);
// with req.Overwrite it **overwrites the existing agent** - files / configuration / private skills / skill bindings / tool grants / the three switches all come from the bundle.
func installAgent(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// No "protected name" check (main and friends): protection constrains delete / rename only. main already
	// exists in the target tenant, so installing the same name goes through the clash handling below (rename or confirm the overwrite); no extra "name taken" gate is needed.
	exist, eerr := agentRepo.ExistsName(info, name, "")
	if eerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "check name: "+eerr.Error(), "")
		return
	}
	var prev *models.Agent
	if exist {
		if !req.Overwrite {
			// can_overwrite turns this into "confirm overwrite" rather than a rename hint: overwriting an existing agent
			// is destructive, so the user must confirm and retry with overwrite=1.
			xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "agent", "name": name},
				"this tenant already has an agent named "+name, gin.H{
					"suggested_name": suggestFreeAgentName(info, name),
					"can_overwrite":  true,
				}, "")
			return
		}
		p, perr := agentRepo.FindByName(info, name)
		if perr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "read agent: "+perr.Error(), "")
			return
		}
		if p == nil {
			exist = false // deleted concurrently between the existence check and the read: fall back to create
		} else {
			prev = p
		}
	}
	// Agents and workflows share one namespace (the create endpoints validate it that way): letting it through
	// would create an entity where a lookup by name always hits the other one. An overwrite cannot meet a
	// same-named workflow (same namespace), so only the create path checks.
	if !exist {
		if flowExist, ferr := flowRepo.ExistsFlowName(info, name); ferr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "check name: "+ferr.Error(), "")
			return
		} else if flowExist {
			xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "workflow", "name": name},
				"this tenant already has a workflow named "+name+"; an agent and a workflow cannot share a name", gin.H{
					"suggested_name": suggestFreeAgentName(info, name),
				}, "")
			return
		}
	}

	// Quota preflight: the same gate as "new agent", so an over-limit tenant does not lay files down first.
	// An overwrite adds no entity, so skip it - otherwise a tenant at the limit could not even update its own agent.
	if !exist {
		if cnt, qerr := quota.CountAgents(info.Schema); qerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to count agents: "+qerr.Error(), "")
			return
		} else if qerr := models.CheckQuota("agent", cnt, quota.Resolve(info.Schema).MaxAgents); qerr != nil {
			le := qerr.(*models.LimitError)
			xhttp.ErrCode(c, "quota.agent_limit_reached", le.Params(), qerr.Error(), "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	ctx := context.Background()
	dstDir := paths.AgentsKey(info.Schema, name)
	if exist {
		// Overwrite: delete the whole directory, then lay it down. Stacking on top leaves files only the old version
		// had (deleted scripts / old private skill directories "come back" and are packed into the next export).
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to clean the existing agent directory: "+derr.Error(), "")
			return
		}
	} else {
		// New: clear a leftover version area for the same name - a leftover directory makes the first release as v1
		// hit EnsureAbsent (an error pointing nowhere near the import). Version directories live in
		// versions/agents/<name>, which do not overlap the draft directory; not deleted on overwrite (that history belongs to the entity itself).
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionAgents, name)); verr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to clear the old version directory of the same-named agent: "+verr.Error(), "")
			return
		}
	}
	// Only the body: _deps/ holds dependent custom tools, which install under custom_tools/, not inside the agent directory
	missing, rerr := assetpack.RestoreFiles(ctx, st, src.snapDir, dstDir, assetpack.BodyFiles(src.manifest.Files))
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the agent files: "+rerr.Error(), "")
		return
	}

	payload := src.payload

	// An agent has two states: normal / deprecated
	status := 1
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	cfgMap := map[string]any{}
	if v, ok := payload["config"].(map[string]any); ok && len(v) > 0 {
		cfgMap = v
	}
	cfg := xsql.JSONB{Data: cfgMap}

	// The three switches follow the bundle. For an old bundle without these fields, an overwrite keeps the
	// target's existing value and a create takes false - asserting false directly would silently switch off in
	// one overwrite what an admin turned on. They are the same permission surface as ticking the boxes on the edit page: whoever can call this API can change them anyway.
	wantSupervisor := flagFrom(payload, "is_supervisor", prev != nil && prev.IsSupervisor)
	wantToolsUnrestricted := flagFrom(payload, "tools_unrestricted", prev != nil && prev.ToolsUnrestricted)
	wantSkillDiscovery := flagFrom(payload, "skill_discovery", prev != nil && prev.SkillDiscovery)
	// The default reply language: an old bundle without the key keeps the target's value on overwrite (never silently
	// forget a language an admin configured).
	wantDefaultLanguage := asString(payload["default_language"])
	if _, ok := payload["default_language"]; !ok && prev != nil {
		wantDefaultLanguage = prev.DefaultLanguage
	}

	ag := &models.Agent{
		Name:              name,
		Description:       xsql.String(asString(payload["description"])),
		Config:            cfg,
		Status:            xsql.Int(status),
		IsSupervisor:      wantSupervisor,
		ToolsUnrestricted: wantToolsUnrestricted,
		SkillDiscovery:    wantSkillDiscovery,
		DefaultLanguage:   wantDefaultLanguage,
	}

	var id string
	warnings := missingWarnings(missing)
	// On overwrite, credential keys the bundle blanked take the target's existing value: the same rule as
	// placeTool keeping configs and workflows keeping env_vars secrets by name - credentials are blanked at
	// listing time, and replacing the whole map would wipe the api_key / token the target already has.
	// An overwrite replaces content, not credentials; configuring them once was work, so do not lose them on a version update.
	if prev != nil {
		if target, isMap := prev.Config.Data.(map[string]any); isMap {
			if kept := assetpack.PreserveSecretKeys(cfgMap, target); kept > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"kept %d credential settings that already existed in this tenant (the bundle had them blanked at listing time)", kept))
			}
		}

		// UpdateAgent overwrites every field, so endpoints must be filled back with the old value first (exposure
		// belongs to this tenant and must not be wiped just because the bundle lacks it); the two switches follow the bundle and are not filled back.
		ag.Endpoints = prev.Endpoints
		if uerr := agentRepo.UpdateAgent(info, prev.ID, ag); uerr != nil {
			// The directory already holds the new content, so it is **not** deleted here. With an old row and new files
			// the agent still runs (AGENT.md is the new body), and one more import aligns them; deleting would leave a
			// "row without files" entity, worse than a mismatch.
			xhttp.ErrCode(c, "asset.record_write_failed", map[string]any{"kind": "agent", "name": name},
				"failed to overwrite the agent record (files were written; retry the import): "+uerr.Error(), "")
			return
		}
		id = prev.ID
		src.overwritten = true
		// Super-user power is its own column (UpdateAgent deliberately does not write it; see its comment), so align it here.
		// Demoting "the last super agent of this tenant" is refused by the repository - that is protection, not a fault; keep the old value and report it.
		if prev.IsSupervisor != wantSupervisor {
			if serr := agentRepo.SetSupervisor(info, prev.ID, wantSupervisor); serr != nil {
				warnings = append(warnings, "the super-agent switch could not be set from the bundle ("+serr.Error()+"); the previous setting is kept")
			} else if wantSupervisor {
				warnings = append(warnings, "the entity is now a super agent per the bundle (holds every built-in tool and reads and writes drafts from any entry point)")
			} else {
				warnings = append(warnings, "the super-agent identity was removed per the bundle")
			}
		}
		// On overwrite, clear this agent's private skill rows first: the files of private skills the bundle does not
		// have are already gone with the directory, and a leftover row is a shell that opens onto nothing (and pollutes lists and the injection manifest).
		if derr := skillRepo.DeleteByAgentID(info.Schema, id); derr != nil {
			warnings = append(warnings, "failed to clean the existing private skill records: "+derr.Error())
		}
		if models.IsProtectedAgent(name) {
			warnings = append(warnings, "this overwrites a built-in agent (protected name): content and switches come from the bundle, while protection follows the name")
		}
	} else {
		var cerr error
		id, cerr = agentRepo.CreateAgent(info, ag, "") // empty creatorAgentID = do not write agent_relations
		if cerr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			xhttp.ErrCode(c, "common.internal", nil, "failed to write the agent record: "+cerr.Error(), "")
			return
		}
		if wantSupervisor {
			warnings = append(warnings, "the entity is now a super agent per the bundle (holds every built-in tool and reads and writes drafts from any entry point)")
		}
	}
	// Private skill rebuild: they belong to this agent's own directory (their files were restored with it), not to a binding
	warnings = append(warnings, restorePrivateSkills(info, id, payload["private_skills"])...)
	if prev != nil {
		// Overwrite: bindings are replaced **wholesale** per the bundle (add-only would keep skills the bundle does not have attached to this agent)
		warnings = append(warnings, replaceInstalledSkillBindings(info, id, src.manifest.Deps.Skills)...)
	} else {
		warnings = append(warnings, bindInstalledSkills(info, id, src.manifest.Deps.Skills)...)
	}
	// Install the tools the bundle brings first, then write the grants - reversed, the grant rows point at tools that do not exist yet
	warnings = append(warnings, installBundledTools(info, src)...)
	if prev != nil {
		warnings = append(warnings, replaceAgentGrants(info, id, src.manifest.Deps.Tools)...)
	} else {
		warnings = append(warnings, writeAgentGrants(info, id, src.manifest.Deps.Tools)...)
	}
	// Call relations are the reverse grant "who may call me" and cannot be remapped across tenants; say so, or the user expects it to be callable right away
	warnings = append(warnings, "call grants (who may call this agent) do not migrate with the asset; reconfigure them under call grants in the target tenant")

	respondInstalled(c, info, src, name, id, missing, warnings)
}

// restorePrivateSkills rebuilds the private skills that travelled with an agent into private rows of the new agent.
//
// RestoreFiles has laid the files into agents/<name>/skills/<skill>/ - but "a directory with no skills row"
// is completely invisible at run time: the skill list does not show it, the file tools find no entry and the agent never loads the body.
// So files and rows must come in pairs.
//
// A failure does not roll back the install: the agent itself is already in place, so one missing private skill
// is only a warning (the user can add it by hand), while failing the whole install would throw away what is already correct.
func restorePrivateSkills(info *auth.AuthInfo, agentID string, raw any) []string {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	var warnings []string
	restored := make([]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(asString(m["name"]))
		if !paths.ValidName(name) {
			warnings = append(warnings, "private skill name is invalid, not rebuilt: "+name)
			continue
		}

		commandDispatch, commandTool := normalizeCommand(asString(m["command_dispatch"]), asString(m["command_tool"]))
		sk := &models.Skill{
			Name:            name,
			Description:     xsql.String(asString(m["description"])),
			Metadata:        xsql.JSONB{Data: map[string]any{}},
			Status:          xsql.Int(models.StatusNormal),
			Restricted:      xsql.Bool(asBool(m["restricted"])),
			UserInvocable:   xsql.Bool(asBool(m["user_invocable"])),
			CommandDispatch: xsql.String(commandDispatch),
			CommandTool:     xsql.String(commandTool),
			AgentID:         xsql.UUID(agentID), // ownership points at the new agent, not the source one
			CreatedBy:       xsql.UUID(info.ID),
			UpdatedBy:       xsql.UUID(info.ID),
		}
		if md, ok := m["metadata"].(map[string]any); ok && len(md) > 0 {
			sk.Metadata = xsql.JSONB{Data: md}
		}

		id, err := skillRepo.Create(info.Schema, sk)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to rebuild private skill %s: %v", name, err))
			continue
		}
		restored = append(restored, name)
		warnings = append(warnings, writeSkillGrants(info, id, toolDepsOf(m["tool_grants"]))...)
	}
	// Report success too: when private skills are not visible after an install the user assumes they were left behind, and silent success is the hardest kind to diagnose
	if len(restored) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"rebuilt %d private skills from the bundle (%s)", len(restored), strings.Join(restored, ", ")))
	}
	return warnings
}

// toolDepsOf turns the [{type, ref}] entries in the bundle back into tool dependency declarations (bad items are skipped).
func toolDepsOf(raw any) []assetpack.ToolDep {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]assetpack.ToolDep, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, assetpack.ToolDep{Type: asString(m["type"]), Ref: asString(m["ref"])})
		}
	}
	return out
}

// bindInstalledSkills binds skills to the new agent by name.
//
// Only name matching works: a skill id from another tenant means nothing. And only same-named "tenant-level"
// skills count (those with an empty agent_id, the established FindByName rule) - someone else's private skill is invisible anyway, so it is skipped with a warning;
// a silently missing binding turns "why does this agent not follow the docs" into a long investigation.
func bindInstalledSkills(info *auth.AuthInfo, agentID string, names []string) []string {
	var warnings []string
	for _, sname := range names {
		s, err := skillRepo.FindByName(info.Schema, sname, "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to look up skill %s, not bound: %v", sname, err))
			continue
		}
		if s == nil {
			warnings = append(warnings, "the target tenant has no same-named skill, not bound: "+sname)
			continue
		}
		// Binding a deprecated skill equals binding a missing one: leave a note so nobody has to dig into "why is it not loaded"
		if int(s.Status) == models.StatusDeprecated {
			warnings = append(warnings, "the target tenant's same-named skill is deprecated, not bound: "+sname)
			continue
		}
		if _, berr := skillRepo.AddBinding(info.Schema, agentID, string(s.ID), info.ID); berr != nil {
			warnings = append(warnings, fmt.Sprintf("failed to bind skill %s: %v", sname, berr))
		}
	}
	if len(names) > 0 {
		warnings = append(warnings, fmt.Sprintf("attempted to bind %d skills by name (see above for the ones missing)", len(names)))
	}
	return warnings
}

// replaceInstalledSkillBindings replaces skill bindings **wholesale** on an overwrite install.
//
// The only difference from bindInstalledSkills is the last step (ReplaceByAgent instead of one AddBinding per
// item): an overwrite must unbind skills the bundle does not have, or the old bindings keep applying to the new content.
// Name matching follows the per-item version: only tenant-level same-named skills, with missing / deprecated ones skipped and reported.
func replaceInstalledSkillBindings(info *auth.AuthInfo, agentID string, names []string) []string {
	var warnings []string
	ids := make([]string, 0, len(names))
	for _, sname := range names {
		s, err := skillRepo.FindByName(info.Schema, sname, "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to look up skill %s, not bound: %v", sname, err))
			continue
		}
		if s == nil {
			warnings = append(warnings, "the target tenant has no same-named skill, not bound: "+sname)
			continue
		}
		if int(s.Status) == models.StatusDeprecated {
			warnings = append(warnings, "the target tenant's same-named skill is deprecated, not bound: "+sname)
			continue
		}
		ids = append(ids, string(s.ID))
	}
	if err := skillRepo.ReplaceByAgent(info.Schema, agentID, ids, info.ID); err != nil {
		return append(warnings, "failed to rebuild skill bindings: "+err.Error())
	}
	warnings = append(warnings, fmt.Sprintf(
		"skill bindings were replaced wholesale per the bundle (%d kept; see above for the missing ones)", len(ids)))
	return warnings
}

// agentGrantsOf turns dependency declarations into grant rows.
//
// grant_kind defaults to allow: older assets may not record it, and defaulting to required would mean a deny can never be taken back.
func agentGrantsOf(agentID string, deps []assetpack.ToolDep) []models.AgentToolGrant {
	grants := make([]models.AgentToolGrant, 0, len(deps))
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		kind := d.GrantKind
		if kind == "" {
			kind = models.GrantKindAllow
		}
		grants = append(grants, models.AgentToolGrant{
			AgentID: agentID, GrantKind: kind, ItemType: d.Type, Ref: d.Ref,
		})
	}
	return grants
}

// writeAgentGrants writes tool grants on a fresh install (replacing everything).
//
// An empty manifest is skipped outright: a fresh install goes through CreateAgent, which already materialised the
// default grants (the read-write group of a normal agent / the core group of a supervisor), and replacing them with nothing here would wipe what was just seeded.
func writeAgentGrants(info *auth.AuthInfo, agentID string, deps []assetpack.ToolDep) []string {
	if len(deps) == 0 {
		return nil
	}
	grants := agentGrantsOf(agentID, deps)
	if len(grants) == 0 {
		return nil
	}
	if err := grantRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		return []string{"failed to write tool grants: " + err.Error()}
	}
	return []string{fmt.Sprintf(
		"wrote %d tool grants; any whose tool is missing in this tenant will not take effect - adjust them under tool grants", len(grants))}
}

// replaceAgentGrants writes tool grants on an overwrite install: **the old grants are cleared even when the bundle declares none**.
// An overwrite means "the bundle decides", and keeping old grants would run the new content on the old tool visibility.
func replaceAgentGrants(info *auth.AuthInfo, agentID string, deps []assetpack.ToolDep) []string {
	grants := agentGrantsOf(agentID, deps)
	if err := grantRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		return []string{"failed to write tool grants: " + err.Error()}
	}
	if len(grants) == 0 {
		return []string{"tool grants were cleared per the bundle (it declares no tools)"}
	}
	return []string{fmt.Sprintf(
		"tool grants were replaced wholesale per the bundle (%d); any whose tool is missing in this tenant will not take effect", len(grants))}
}

// suggestFreeAgentName suggests an agent name free in this tenant (up to 50 attempts).
func suggestFreeAgentName(info *auth.AuthInfo, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		exist, err := agentRepo.ExistsName(info, cand, "")
		if err != nil || !exist {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== Workflows =====

// installWorkflow installs a workflow: it creates a new {schema}.flows row from FLOW.json in the snapshot.
//
// The difference from the other three kinds: no files are written (the definition lives only in the database),
// so the "files first, DB second" order degrades to a single INSERT and a failure needs no directory cleanup.
//
// An install lands as normal (status=1). Earlier it landed as 2, meant as "disabled", but for flows 2 used to
// mean **locked** (read-only, still runnable), which is neither disabled nor editable - a workflow has no
// "disabled" state at all (2 has been removed from workflows / agents / skills and now only means "disabled" for custom tools).
// The need to check references became a warning instead of a locked row.
func installWorkflow(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	prev, perr := flowRepo.FindByName(info, name)
	if perr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "check name: "+perr.Error(), "")
		return
	}
	if prev != nil {
		if !req.Overwrite {
			xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "workflow", "name": name},
				"this tenant already has a workflow named "+name, gin.H{
					"suggested_name": suggestFreeFlowName(info, name),
					"can_overwrite":  true,
				}, "")
			return
		}
		// A deprecated workflow cannot be redefined (UpdateFlow refuses): say so up front instead of after the user confirms
		if int(prev.Status) == models.StatusDeprecated {
			xhttp.ErrCode(c, "asset.target_deprecated", map[string]any{"kind": "workflow", "name": name},
				"the same-named workflow in this tenant is deprecated; switch it back to normal before overwriting", "")
			return
		}
	} else {
		// Workflows and agents share one namespace (the create endpoints validate it that way): letting it through
		// would create an entity where a lookup by name always hits the other one. An overwrite targets the same entity and cannot meet an agent, so only the create path checks.
		if agentExist, aerr := agentRepo.ExistsName(info, name, ""); aerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "check name: "+aerr.Error(), "")
			return
		} else if agentExist {
			xhttp.ErrCodeData(c, "asset.name_conflict", map[string]any{"kind": "agent", "name": name},
				"this tenant already has an agent named "+name+"; a workflow and an agent cannot share a name", gin.H{
					"suggested_name": suggestFreeFlowName(info, name),
				}, "")
			return
		}
		// Quota preflight: the same gate as "new workflow", so an over-limit tenant does not create a row and roll back; an overwrite adds no entity, so it is skipped
		if cnt, qerr := quota.CountFlows(info.Schema); qerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to count workflows: "+qerr.Error(), "")
			return
		} else if qerr := models.CheckQuota("flow", cnt, quota.Resolve(info.Schema).MaxFlows); qerr != nil {
			le := qerr.(*models.LimitError)
			xhttp.ErrCode(c, "quota.flow_limit_reached", le.Params(), qerr.Error(), "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}
	ctx := context.Background()
	raw, rerr := st.ReadFile(ctx, path.Join(src.snapDir, assetpack.WorkflowFileName))
	if rerr != nil {
		xhttp.ErrCode(c, "asset.invalid_package", map[string]any{"file": assetpack.WorkflowFileName},
			"the snapshot has no workflow definition ("+assetpack.WorkflowFileName+"): ask the provider to list it again", "")
		return
	}
	var def assetpack.WorkflowSnapshot
	if uerr := json.Unmarshal(raw, &def); uerr != nil {
		xhttp.ErrCode(c, "asset.invalid_package", nil, "the workflow definition is corrupt: "+uerr.Error(), "")
		return
	}

	// Overwriting a same-named workflow keeps the secret values the target already has.
	//
	// Secrets in a bundle are blanked at listing time (credentials must not cross tenants), but "the bundle has
	// none" is not "the target should be wiped": an overwrite replaces content, not credentials. The target's
	// values are filled back by variable name where the bundle is empty; variables the target does not have (secrets added now) stay empty and must be filled in.
	preservedSecrets := false
	if prev != nil {
		merged, merr := secureval.MergeEnvVarSecrets(jsonbBytes(prev.EnvVars), def.EnvVars)
		if merr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to merge environment variables: "+merr.Error(), "")
			return
		}
		if string(merged) != string(def.EnvVars) {
			preservedSecrets = true
		}
		def.EnvVars = json.RawMessage(merged)
	}

	f := &models.Flow{
		Name:              name,
		Description:       xsql.String(def.Description),
		Diagram:           jsonbFromRaw(def.Diagram),
		Arguments:         jsonbFromRaw(def.Arguments),
		Outputs:           jsonbFromRaw(def.Outputs),
		EnvVars:           jsonbFromRaw(def.EnvVars),
		FlowVars:          jsonbFromRaw(def.FlowVars),
		Timeout:           xsql.Int(def.Timeout),
		MaxRecursionDepth: xsql.Int(def.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(def.MaxNodeCount),
		MaxExecCount:      xsql.Int(def.MaxExecCount),
		MaxRunRecords:     xsql.Int(def.MaxRunRecords),
		MaxSessionRecords: xsql.Int(def.MaxSessionRecords),
	}

	// Only "normal / deprecated" are valid landing states; on overwrite the default is the target's existing state (an overwrite replaces content, not lifecycle)
	status := models.StatusNormal
	if prev != nil {
		status = int(prev.Status)
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	warnings := []string{
		"model / tool / sub-flow references in the workflow do not migrate with the asset; check them one by one before running",
	}
	switch {
	case prev == nil:
		warnings = append(warnings,
			"credential variables (env_vars and the api_key values in nodes) were blanked at listing time and must be filled in again")
	case len(def.EnvVars) == 0:
		// The whole env_vars block in the bundle is empty, so there is nothing to merge against and the target's
		// existing variables are removed by this overwrite. That must not be reported as "kept" - it would suggest the secrets are still there.
		warnings = append(warnings,
			"the bundle carries no environment variables, so this overwrite removes the target's existing ones (secrets included); reconfigure them")
	case preservedSecrets:
		warnings = append(warnings,
			"the same-named workflow was overwritten: the tenant's existing secret values were kept by variable name; secret variables added by the bundle are empty and must be filled in")
	default:
		warnings = append(warnings,
			"the same-named workflow was overwritten: the bundle's secret variables are empty and there is no previous value to reuse, so they must be filled in")
	}

	var id string
	if prev != nil {
		// UpdateFlow writes no status / endpoints / breakpoints and does not touch flow_versions: exposure, debug
		// state and release history all belong to the target tenant. An overwrite changes the draft - the public entry point runs the released version.
		if uerr := flowRepo.UpdateFlow(info, prev.ID, f); uerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to overwrite the workflow definition: "+uerr.Error(), "")
			return
		}
		id = prev.ID
		src.overwritten = true
		warnings = append(warnings,
			"an overwrite replaces only the draft (diagram / variables / limits): released versions, exposed endpoints and breakpoints stay as they are; "+
				"release a new version in the editor to make the new content live")
		if status != int(prev.Status) {
			if uerr := flowRepo.UpdateStatus(info, id, status); uerr != nil {
				warnings = append(warnings, "the status could not be changed to the target value; the original is kept: "+uerr.Error())
			}
		}
	} else {
		var cerr error
		id, cerr = flowRepo.CreateFlow(info, f)
		if cerr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to write the workflow record: "+cerr.Error(), "")
			return
		}
		// CreateFlow's column default is already normal (1); write again only when the caller explicitly asks for deprecated
		if status != models.StatusNormal {
			if uerr := flowRepo.UpdateStatus(info, id, status); uerr != nil {
				warnings = append(warnings, "the workflow was created but its status could not be changed to the target value: "+uerr.Error())
			}
		}
	}
	respondInstalled(c, info, src, name, id, nil, warnings)
}

// jsonbFromRaw turns a raw fragment from FLOW.json into a jsonb column value; an empty fragment / null becomes nil.
func jsonbFromRaw(raw json.RawMessage) xsql.JSONB {
	if len(raw) == 0 || string(raw) == "null" {
		return xsql.JSONB{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return xsql.JSONB{}
	}
	return xsql.JSONB{Data: v}
}

// jsonbBytes returns the raw bytes of a JSONB (Data may be []byte / RawMessage / string).
// The inverse of jsonbFromRaw above: merging env_vars needs the raw form, not a decoded map.
func jsonbBytes(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case json.RawMessage:
		return []byte(v)
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(j.Data)
		if err != nil {
			return nil
		}
		return b
	}
}

// suggestFreeFlowName suggests a workflow name free in this tenant (up to 50 attempts).
// Both namespaces must be avoided: workflows and agents share one lookup-by-name table.
func suggestFreeFlowName(info *auth.AuthInfo, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		flowExist, ferr := flowRepo.ExistsFlowName(info, cand)
		agentExist, aerr := agentRepo.ExistsName(info, cand, "")
		if ferr == nil && aerr == nil && !flowExist && !agentExist {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== Shared tail =====

// respondInstalled responds and writes the install record (a failed record write is only reported, never fails the install).
//
// Importing an uploaded bundle has no platform asset row (src.asset is nil), so it must not write an install record -
// the table means "installed from a platform asset", and inventing a row with a non-existent asset_id only marks the wrong things as installed.
func respondInstalled(c *gin.Context, info *auth.AuthInfo, src *installSource, name, id string, missing, warnings []string) {
	// Missing global variables must be said here: the install does not fail at once, the first run of a related
	// node fails, and the user then sees a node error with no visible link to the install.
	warnings = append(warnings, missingGlobalVarWarnings(info, src.manifest.Deps.GlobalVars)...)

	// Before writing, note whether the last install carried the same content: after the record is written this
	// fingerprint becomes the new one. Report only, never block - a matching digest says "the two bundles are the same", not that the local entity was not edited since.
	if src.overwritten && src.manifest.Digest != "" {
		if prevDigest, derr := sharedassets.LatestInstallDigest(info.Schema, id); derr == nil &&
			prevDigest == src.manifest.Digest {
			warnings = append(warnings, "this overwrite carries the same content as the previous install (digest "+
				assetpack.ShortDigest(src.manifest.Digest)+"): unless the local copy changed since, the overwrite brought nothing new")
		}
	}
	if a := src.asset; a != nil {
		if err := sharedassets.RecordInstall(a.ID, info.Schema, a.Build, name, id, info.ID, info.Name, src.manifest.Digest); err != nil {
			warnings = append(warnings, "failed to write the install record (the install is unaffected): "+err.Error())
		}
	}
	body := gin.H{
		"installed":     gin.H{"name": name, "id": id},
		"kind":          string(src.kind),
		"warnings":      append(append([]string{}, src.notices...), warnings...),
		"skipped_files": missing,
		// The front end switches its copy from "imported" to "imported as an overwrite"; an overwrite is destructive, so the response must say so
		"overwritten": src.overwritten,
		// Content digest and manifest spec version: shown for provenance, and the basis for "same bundle" decisions
		"digest": src.manifest.Digest,
		"spec":   src.manifest.Spec,
	}
	if a := src.asset; a != nil {
		body["from_asset"] = a.ID
		body["from_build"] = a.Build
		body["source_schema"] = a.SourceSchema
		body["source_by_name"] = a.ListedByName
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// DeleteInstall deletes one install record (DELETE /admin/share/installs/:asset_id/:installed_id).
//
// It deletes the record only and **never uninstalls** the skills / tools / agents already installed in the tenant.
// The reason a tenant may delete it: when the entity was deleted, or the install landed on something undesirable,
// the record keeps the asset marked as installed, blocking a reinstall or misrepresenting the snapshot state. Deleting the record settles the books.
func DeleteInstall(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	assetID := strings.TrimSpace(c.Param("asset_id"))
	installedID := strings.TrimSpace(c.Param("installed_id"))
	if assetID == "" || installedID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "asset_id / installed_id are required", "")
		return
	}
	deleted, err := sharedassets.DeleteInstall(assetID, info.Schema, installedID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("delete install record", err), "")
		return
	}
	if !deleted {
		xhttp.ErrCode(c, "asset.install_not_found", map[string]any{"asset_id": assetID, "installed_id": installedID},
			"the install record does not exist (it may have been deleted)", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
}

// ClearInstalls DELETE /admin/share/installs - clears every install record of this tenant.
//
// Also deletes records only, never the entities. One DELETE instead of one call per record: clearing either
// succeeds or fails as a whole, while a half-finished clear leaves records that are harder to explain.
func ClearInstalls(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	n, err := sharedassets.ClearInstalls(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, assetErrMsg("clear install records", err), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": n}, "")
}

// missingGlobalVarWarnings lists the global variables the bundle references but this tenant lacks.
//
// Report only, never block: variables may be created after the install, or tried elsewhere first; but "what
// happens when one is missing" must be clear (the related node fails outright, it is not a silent empty value).
func missingGlobalVarWarnings(info *auth.AuthInfo, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	missing := make([]string, 0, len(names))
	for _, name := range names {
		v, err := globalVarRepo.Get(info.Schema, name)
		if err != nil || v == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"this asset references %d global variables this tenant does not have (%s): create them under Settings -> Global variables, or the related nodes fail outright when they run",
		len(missing), strings.Join(missing, ", "))}
}

func missingWarnings(missing []string) []string {
	out := make([]string, 0, len(missing))
	for _, f := range missing {
		out = append(out, "missing in the snapshot, skipped: "+f)
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// flagFrom reads a boolean switch from the bundle.
//
// A missing field (an older bundle) uses fallback rather than false: an overwrite import would otherwise
// silently switch off the super agent / unrestricted tools / skill discovery an admin turned on - that is a downgrade, not a default.
func flagFrom(payload map[string]any, key string, fallback bool) bool {
	if v, ok := payload[key].(bool); ok {
		return v
	}
	return fallback
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// normalizeCommand drops an unusable user-command definition on import.
//
// An asset pack may come from a newer or older build, so an unknown dispatch mode is ignored rather than rejected: refusing
// the whole install over one optional frontmatter field would be worse, and a value nobody consumes would be a lie. The
// pair is also repaired as a pair - a dispatch without a target tool (or the reverse) is no command at all.
func normalizeCommand(dispatch, tool string) (string, string) {
	dispatch = strings.TrimSpace(dispatch)
	tool = strings.TrimSpace(tool)
	if dispatch != "tool" || tool == "" {
		return "", ""
	}
	return dispatch, tool
}
