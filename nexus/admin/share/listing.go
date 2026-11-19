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
	"sync"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/listingpolicy"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// buildMu serialises "allocate the build number -> write the files" (see assetpack.FreeBuild).
// Listing is a rare manual action, so one queue is simpler than a per-asset lock table.
// The cross-process window (multiple replicas) remains; that step relies on the DB row lock.
var buildMu sync.Mutex

type listingRequest struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"` // skill: id; custom_tool: name
}

// mergeGlobalVarDeps merges two batches of global-variable dependencies (deduplicated, sorted).
//
// Why merge instead of overwrite: workflow dependencies are scanned in planWorkflow (only there is the full definition at hand), while the
// generic pass scans plan.payload - a workflow payload holds only the name and description, so overwriting would
// erase the references already found.
func mergeGlobalVarDeps(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// planFor collects "all inputs one listing / export needs", per kind.
//
// Listing and export share this assembly: both read the tenant entity, allowlist the payload, declare dependencies and
// pack the whole directory tree; only the destination differs (platform snapshot vs a temporary directory -> zip). Two copies would let
// "what the bundle carries" drift from "what was listed".
func planFor(kind assetpack.Kind, info *auth.AuthInfo, ref string) (listingPlan, error) {
	switch kind {
	case assetpack.KindSkill:
		return planSkill(info, ref)
	case assetpack.KindTool:
		return planTool(info, ref)
	case assetpack.KindAgent:
		return planAgent(info, ref)
	case assetpack.KindWorkflow:
		return planWorkflow(info, ref)
	}
	return listingPlan{}, fmt.Errorf("unknown asset kind: %s", kind)
}

// CreateListing lists one definition-domain entity of this tenant as a platform asset (into review).
//
// All four kinds are enabled: skill / custom_tool / agent / workflow. An agent ships "the entity + skill names + tool refs"
// with no private-skill directory (see listingAgent); installAgent rebuilds the skill bindings and grants afterwards.
func CreateListing(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	var req listingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	kind := assetpack.Kind(strings.TrimSpace(req.Kind))
	if !kind.Valid() {
		xhttp.ErrCode(c, "asset.invalid_kind", map[string]any{"kind": string(kind)},
			"unknown asset kind (kind accepts skill / custom_tool / agent / workflow only)", "")
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		xhttp.ErrCode(c, "asset.ref_required", nil, "ref is required (skill / agent / workflow take an id, custom_tool takes a name)", "")
		return
	}

	switch kind {
	case assetpack.KindSkill:
		listingSkill(c, info, ref)
	case assetpack.KindTool:
		listingTool(c, info, ref)
	case assetpack.KindAgent:
		listingAgent(c, info, ref)
	case assetpack.KindWorkflow:
		listingWorkflow(c, info, ref)
	}
}

// listingPlan holds every input one listing needs (assembled by the per-kind listingXxx helpers).
type listingPlan struct {
	kind     assetpack.Kind
	name     string         // asset name (= the source entity name)
	sourceID string         // source entity id (a generated placeholder when the tool has no stable id; provenance only)
	entry    string         // entry file (must exist in the snapshot)
	srcDir   string         // source directory (storage logical key)
	payload  map[string]any // allowlisted entity snapshot
	deps     assetpack.Deps
	warnings []string
	skipDirs []string             // subdirectories that do not travel (none of the four kinds sets one: an agent's private skills go entirely)
	trees    []assetpack.PackTree // directory trees added to the bundle (the bodies of dependent custom tools)
	cleanup  func()               // temporary content to remove after packing (a workflow's FLOW.json staging directory); may be nil
}

// validatePlanName checks the name: every kind goes through assetpack (workflows use the wording of
// ValidateWorkflowName, because there the name is the public tool name rather than path safety; the other three are directory names).
func validatePlanName(kind assetpack.Kind, name string) error {
	if kind == assetpack.KindWorkflow {
		return assetpack.ValidateWorkflowName(name)
	}
	return assetpack.ValidateName(name)
}

// listingEntity packs, persists and responds (the shared tail of every kind).
func listingEntity(c *gin.Context, info *auth.AuthInfo, plan listingPlan) {
	if plan.cleanup != nil {
		defer plan.cleanup()
	}
	if err := validatePlanName(plan.kind, plan.name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "asset.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	// Re-listing the same name overwrites the same row and bumps build (the unique index is kind + source_schema + lower(name)).
	prev, qerr := findMine(plan.kind, info.Schema, plan.name)
	if qerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "query asset: "+qerr.Error(), "")
		return
	}
	assetID := uuid.NewString()
	build := 1
	if prev != nil {
		assetID = prev.ID
	}

	ctx := context.Background()
	// Number allocation and file writing must be serial (see assetpack.FreeBuild): two concurrent paths computing the same build would
	// interleave their content in one tree while the row records a single manifest.
	buildMu.Lock()
	if prev != nil {
		b, berr := assetpack.FreeBuild(ctx, st, assetID, prev.Build+1)
		if berr != nil {
			buildMu.Unlock()
			xhttp.ErrCode(c, "common.internal", nil, berr.Error(), "")
			return
		}
		build = b
	}
	dstDir := paths.PlatformAssetKey(assetID, build)
	files, perr := assetpack.PackFilesWith(ctx, st, plan.srcDir, dstDir, plan.entry,
		assetpack.DefaultLimits(), assetpack.PackOptions{SkipDirs: plan.skipDirs, Trees: plan.trees})
	buildMu.Unlock()
	if perr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, perr.Error(), "")
		return
	}

	// Complete the dependency manifest with global-variable references: the bundle carries only the names, and the values live in the target tenant's own global_vars.
	// Done here rather than in each plan*: agents and skills must read AGENT.md / SKILL.md under the definition directory
	// to find ${global_vars.X}, while a workflow's srcDir is the export staging directory, so scanning the payload suffices.
	plan.deps.GlobalVars = mergeGlobalVarDeps(plan.deps.GlobalVars,
		assetpack.CollectGlobalVarRefs(ctx, st, plan.srcDir, plan.payload))

	clean, secretWarnings := assetpack.StripSecrets(plan.kind, plan.payload)
	warnings := append(append([]string{}, plan.warnings...), secretWarnings...)

	payloadJSON, perr2 := assetpack.EncodeJSONB(clean)
	if perr2 != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, "encode payload: "+perr2.Error(), "")
		return
	}
	manifest := assetpack.Manifest{
		Kind:  plan.kind,
		Name:  plan.name,
		Entry: plan.entry,
		Deps:  plan.deps,
		Files: files,
	}
	// The fingerprint covers "masked payload + final file list": re-listing the same entity changes it only
	// when the content really changed (build bumped, fingerprint unchanged = the user can be told "nothing changed").
	if serr := assetpack.StampManifest(&manifest, payloadJSON); serr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, "compute digest: "+serr.Error(), "")
		return
	}
	manifestJSON, merr := assetpack.EncodeJSONB(manifest)
	if merr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrCode(c, "common.internal", nil, "encode manifest: "+merr.Error(), "")
		return
	}

	// Listing policy: review-exempt (the default) goes straight to listed; only tenants set to "review required" land in pending.
	// An unreadable policy counts as review-required (fail closed): a broken policy table should cost one extra manual confirmation,
	// never let unreviewed content into the market.
	requireReview, rerr := listingpolicy.RequireReview(info.Schema)
	if rerr != nil {
		requireReview = true
	}
	status := sharedassets.StatusPending
	if !requireReview {
		status = sharedassets.StatusListed
	}

	a := &sharedassets.Asset{
		ID:           assetID,
		Kind:         string(plan.kind),
		Name:         plan.name,
		DisplayName:  plan.name,
		Description:  asString(clean["description"]),
		SourceSchema: info.Schema,
		SourceID:     plan.sourceID,
		Build:        build,
		Status:       status,
		Visibility:   sharedassets.VisibilityAll,
		Manifest:     manifestJSON,
		Payload:      payloadJSON,
		ListedBy:     sql.NullString{String: info.ID, Valid: strings.TrimSpace(info.ID) != ""},
		ListedByName: info.Name,
	}
	if dn := asString(clean["display_name"]); dn != "" {
		a.DisplayName = dn
	}

	var werr error
	if prev != nil {
		// Re-listing follows the same policy: review-required tenants go back to pending (changed content cannot ride on the previous approval),
		// exempt tenants land as listed; both clear the previous review verdict (done in UpdateListing).
		werr = sharedassets.UpdateListing(a)
	} else {
		werr = sharedassets.Insert(a)
	}
	if werr != nil {
		// If the row was not written, remove the snapshot just laid down: a directory no row points at only looks like a stale snapshot.
		_ = st.DeletePrefix(ctx, dstDir)
		// Concurrent listing: another request already pushed the row to a higher build, so this content is void (not a server error; the user just refreshes).
		if errors.Is(werr, sharedassets.ErrStaleBuild) {
			xhttp.ErrCode(c, "asset.stale_build", map[string]any{"id": a.ID},
				"this asset was just replaced by another listing (the snapshot moved on); refresh and retry", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, "save asset: "+werr.Error(), "")
		return
	}

	// The row points at the new snapshot, so the old one has no readers left (install copies, export packs the current snapshot, and there is no rollback path).
	// Leaving it means "one re-listing = one more full image" and disk only grows.
	// A failed delete must not fail the listing: CleanupSnapshots retries at startup.
	_ = assetpack.PruneOldBuilds(ctx, st, assetID, build)

	xhttp.OkJSON(c, "Success", gin.H{
		"asset": gin.H{
			"id": assetID, "kind": string(plan.kind), "name": plan.name,
			"build": build, "status": status,
		},
		// need_review=false means the asset is already listed (the tenant is exempt); the front end adapts its copy.
		"need_review": requireReview,
		"manifest": gin.H{
			"files": len(files), "total_size": manifest.TotalSize(), "deps": len(plan.deps.Tools),
		},
		"warnings": warnings,
	}, "")
}

// findMine looks up "a same-kind, same-name asset **listed by this tenant**" (to detect a re-listing); it returns (nil, nil) when there is none.
//
// OriginLocal cannot be dropped: on one machine "listed by me" and "imported by someone else" can share a name and source_schema
// (the unique key includes origin_server). Without it, a re-listing would hit the imported row and rewrite it as its own content -
// corrupting provenance (origin_server still names another site) and leaving the genuine original row stuck on an old snapshot.
func findMine(kind assetpack.Kind, schema, name string) (*sharedassets.Asset, error) {
	list, err := sharedassets.List(sharedassets.Filter{
		Kind: string(kind), SourceSchema: schema, OriginLocal: true,
	})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// ===== Skills =====

func listingSkill(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planSkill(info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	listingEntity(c, info, plan)
}

func planSkill(info *auth.AuthInfo, id string) (listingPlan, error) {
	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		return listingPlan{}, err
	}
	if s == nil {
		return listingPlan{}, errors.New("Skill not found")
	}

	// A private skill cannot be exported or listed on its own: its body lives in agents/<agent>/skills/<name> (see the convention atop package paths), so
	// leaving that agent would force "promote to tenant level" - ownership lost for good and the visibility allowlist left behind;
	// importing it back gives either something nobody can use or an orphan copy sharing the private name. The right channel is to ship it with its
	// agent (see payload.private_skills in planAgent); to share it, promote it to a tenant-level skill first.
	if string(s.AgentID) != "" {
		agentName := string(s.AgentName)
		if agentName == "" {
			agentName = "deleted agent"
		}
		return listingPlan{}, fmt.Errorf(
			"skill %s is private to \"%s\" and cannot be exported or listed on its own: export the agent it belongs to (private skills travel with their agent), or promote it to a tenant-level skill first",
			s.Name, agentName)
	}

	// Protected names and deprecated state only warn; they never block listing or export: content and identity are separate things -
	// permissions, super-user power and ownership never travel (see planAgent and installAgent).
	plan := listingPlan{
		kind:     assetpack.KindSkill,
		name:     string(s.Name),
		sourceID: string(s.ID),
		entry:    "SKILL.md",
		// tenant-level skill: tenants/<schema>/skills/<name>
		srcDir: paths.SkillsKey(info.Schema) + "/" + string(s.Name),
	}
	if models.IsProtectedSkill(string(s.Name)) {
		plan.warnings = append(plan.warnings,
			"this is a built-in skill (protected name): what is distributed is a copy of the content, and the target tenant gains no special identity")
	}
	if int(s.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "this skill is deprecated: the bundle holds its current content")
	}

	payload, berr := portableSkillPayload(info.Schema, string(s.ID))
	if berr != nil {
		return listingPlan{}, fmt.Errorf("read skill row: %w", berr)
	}

	grants, gerr := grantRepo.ListSkillGrants(info.Schema, string(s.ID))
	if gerr != nil {
		return listingPlan{}, fmt.Errorf("read skill grants: %w", gerr)
	}
	deps := assetpack.Deps{Tools: make([]assetpack.ToolDep, 0, len(grants))}
	for _, g := range grants {
		deps.Tools = append(deps.Tools, assetpack.ToolDep{Type: g.ItemType, Ref: g.Ref})
	}

	plan.payload = payload
	plan.deps = deps
	if err := attachToolBundles(info, &plan); err != nil {
		return listingPlan{}, err
	}
	return plan, nil
}

// portableSkillPayload reads the to_jsonb of a skills row and narrows it to the allowlisted payload.
//
// It uses to_jsonb rather than assigning models.Skill field by field: the database supplies column names
// and types, so a new column is not silently dropped because this side forgot to keep up (the real
// filtering happens in assetpack.PortableSkill's allowlist).
func portableSkillPayload(schema, id string) (map[string]any, error) {
	var raw []byte
	if err := db.DB.QueryRow(
		`SELECT to_jsonb(x) FROM `+tenantTable(schema, "skills")+` x WHERE x.id = $1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return assetpack.PortableSkill(row), nil
}

// portablePrivateSkills reads an agent's private skill rows and narrows them to entries that can be rebuilt
// in the target tenant.
//
// Each item = the PortableSkill allowlist + tool_grants (that private skill's own tool dependencies).
// No id / agent_id / timestamps: the target tenant regenerates them, with ownership pointing at the new agent.
// Deprecated rows are skipped (same as ListPrivateNames): rebuilding a deprecated skill is pointless.
func portablePrivateSkills(schema, agentID string) ([]map[string]any, error) {
	if agentID == "" {
		return nil, nil
	}
	rows, err := db.DB.Query(`SELECT to_jsonb(x) FROM `+tenantTable(schema, "skills")+` x
		WHERE x.agent_id = $1 AND x.status <> 3 ORDER BY x.name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, 4)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		item := assetpack.PortableSkill(row)

		grants, gerr := grantRepo.ListSkillGrants(schema, asString(row["id"]))
		if gerr != nil {
			return nil, gerr
		}
		deps := make([]map[string]any, 0, len(grants))
		for _, g := range grants {
			deps = append(deps, map[string]any{"type": g.ItemType, "ref": g.Ref})
		}
		item["tool_grants"] = deps
		out = append(out, item)
	}
	return out, rows.Err()
}

// bundledToolTrees turns the dependencies that point at **this tenant's custom tools** into bundle attachments.
//
// Only custom:<name> counts: those are the only ones under tools/ with a body. Every other scope is not this tenant's file -
// builtin ships in code, plugin is an on-disk plugin, mcp/rpc/local are remote connections and group/preset are just groups, so
// they work in the target tenant as they are and nothing can be carried. One level is enough: a custom tool depends only on capabilities
// (sandbox.exec / egress), never on another custom tool, so there is no dependency graph.
//
// This decision has bitten twice, both times showing up as "packing failed for no reason, or carried nothing":
//   - filtering only builtin: treats plugin:web_search as a directory name, and a colon is an illegal file-name character on Windows,
//     so "no body to carry" becomes "cannot open the file" and the whole listing fails;
//   - conversely, taking custom:<name> with its prefix as the name means tools/custom:xxx does not exist, the contract read returns
//     ErrNoRows and is skipped silently - not one custom tool travels, and nothing here looks wrong.
//
// The returned bundles serve display only (tool_bundles in the bundle); install goes by the file manifest.
func bundledToolTrees(info *auth.AuthInfo, deps assetpack.Deps) ([]assetpack.PackTree, []map[string]any, error) {
	if len(deps.Tools) == 0 {
		return nil, nil, nil
	}
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)
	trees := make([]assetpack.PackTree, 0, len(deps.Tools))
	bundles := make([]map[string]any, 0, len(deps.Tools))
	seen := make(map[string]bool, len(deps.Tools))

	for _, d := range deps.Tools {
		name := customToolNameOfRef(d.Ref)
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true

		t, err := store.Get("", name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // a declared ref with no body (the tool was deleted): not a reason to fail the pack
			}
			return nil, nil, err
		}
		trees = append(trees, assetpack.PackTree{
			SrcDir:    paths.ToolsKey(info.Schema) + "/" + name,
			DstPrefix: assetpack.BundledToolsPrefix + "/" + name,
			Required:  true, // a dependency is declared but the body is missing: better to fail the listing
		})
		bundles = append(bundles, map[string]any{
			"name":         t.Name,
			"display_name": t.DisplayName,
			"description":  t.Description,
		})
	}
	return trees, bundles, nil
}

// customToolNameOfRef extracts the short name of a custom tool that may travel, and returns "" for everything else.
//
// The test looks at the ref's scope, not the row's item_type - the latter is derived from the ref and legacy rows may disagree.
// It is a separate function so a unit test can pin it down: a mistake here either fails the listing (a plugin: prefix taken as a directory name)
// or silently drops content (a custom: prefix taken as a directory name; the contract read returns ErrNoRows and is skipped).
func customToolNameOfRef(ref string) string {
	parsed, err := toolgrant.ParseRef(ref)
	if err != nil || parsed.Scope != toolgrant.ScopeCustom {
		return ""
	}
	return parsed.Subject // neither the directory name nor the tool short name carries the custom: prefix
}

// ===== Agents =====

// listingAgent lists one agent.
//
// Three switches **travel with the bundle** (is_supervisor / tools_unrestricted / skill_discovery): they describe
// "how this agent runs" and are applied on install in the target tenant; `warnings` names them, so nothing is downgraded silently.
// Listing and export share this assembly, so "export then import" and "list then install" produce the same thing.
//
// Only agent_relations is **deliberately left out**: it expresses "who may call this", a uuid relation that cannot be
// remapped across tenants, so it must be reconfigured in the target tenant after install.
//
// Private skills **travel along**: the files in agents/<agent>/skills/** (the whole tree is packed) and their metadata in
// payload.private_skills, rebuilt on install as private skills of the new agent (new ids, ownership pointing at it).
// Files without metadata will not do: a directory with no skills row is invisible at run time.
func listingAgent(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planAgent(info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	listingEntity(c, info, plan)
}

func planAgent(info *auth.AuthInfo, id string) (listingPlan, error) {
	a, err := agentRepo.GetAgent(info, id)
	if err != nil {
		return listingPlan{}, err
	}
	if a == nil {
		return listingPlan{}, errors.New("Agent not found")
	}
	// No entity-state gate at all: super-user power, protected names and deprecated state only warn; nothing blocks listing or export.
	plan := listingPlan{
		kind:     assetpack.KindAgent,
		name:     a.Name,
		sourceID: a.ID,
		entry:    "AGENT.md",
		srcDir:   paths.AgentsKey(info.Schema, a.Name),
		payload: map[string]any{
			"name":        a.Name,
			"description": string(a.Description),
			"config":      a.Config.Data,
			// The three switches travel with the content: they decide how the agent runs in a tenant, and leaving them out
			// would install a copy that behaves differently in the target tenant (beyond what the author
			// expects). All three are written explicitly (including false), so
			// an older reader falls back to "keep the target's existing value / false for a new one"; see installAgent.
			"is_supervisor":      a.IsSupervisor,
			"tools_unrestricted": a.ToolsUnrestricted,
			"skill_discovery":    a.SkillDiscovery,
		},
	}
	if a.IsSupervisor {
		plan.warnings = append(plan.warnings,
			"this agent is a super agent: the switch travels with the bundle, so the same-named entity in the target tenant is a super agent too (the importer confirms at install time)")
	}
	if models.IsProtectedAgent(a.Name) {
		plan.warnings = append(plan.warnings,
			"this is a built-in agent (protected name): protection follows the name, not the bundle; what travels is the content and the switches")
	}
	if int(a.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "this agent is deprecated: the bundle holds its current content")
	}

	if a.ToolsUnrestricted || a.SkillDiscovery {
		plan.warnings = append(plan.warnings,
			"the \"unrestricted tools\" and \"discover unbound skills\" switches travel with the bundle and take effect in the target tenant")
	}

	// Private skills travel whole: the files in agents/<agent>/skills/** (no skipDirs) and the metadata in the payload,
	// rebuilt into skills rows on install - files without rows are just an invisible orphan directory in the target tenant.
	privateSkills, perr := portablePrivateSkills(info.Schema, id)
	if perr != nil {
		return listingPlan{}, fmt.Errorf("read private skills: %w", perr)
	}
	privates := make([]string, 0, len(privateSkills))
	for _, ps := range privateSkills {
		privates = append(privates, asString(ps["name"]))
	}
	if len(privates) > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"the %d private skills of this agent travel with the bundle (%s) and stay private to it in the target tenant",
			len(privates), strings.Join(privates, ", ")))
	}
	plan.payload["private_skills"] = privateSkills

	bound, berr := skillRepo.ListBoundSkillNames(info.Schema, id)
	if berr != nil {
		return listingPlan{}, fmt.Errorf("read skill bindings: %w", berr)
	}

	grants, gerr := grantRepo.ListAgentGrants(info.Schema, id)
	if gerr != nil {
		return listingPlan{}, fmt.Errorf("read agent grants: %w", gerr)
	}
	deps := assetpack.Deps{
		Skills:        bound,
		PrivateSkills: privates,
		Tools:         make([]assetpack.ToolDep, 0, len(grants)),
	}
	for _, g := range grants {
		deps.Tools = append(deps.Tools, assetpack.ToolDep{
			Type: g.ItemType, Ref: g.Ref, GrantKind: g.GrantKind,
		})
	}
	// "who I may call" is recorded as a dependency too: the grant rows do not travel (uuid relations cannot be remapped),
	// but the install must be able to check "does the target tenant have that agent" - otherwise only a run-time call_agent
	// failure reveals it (the pre-import dependency check compares against this list).
	calls, cerr := relationRepo.ListCallableByCaller(info.Schema, id)
	if cerr != nil {
		return listingPlan{}, fmt.Errorf("read call relations: %w", cerr)
	}
	deps.Agents = make([]string, 0, len(calls))
	for _, c := range calls {
		deps.Agents = append(deps.Agents, c.Name)
	}
	if len(deps.Agents) > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"this agent may call %d other agents (%s); the call grants themselves do not migrate, so reconfigure them under call grants after install",
			len(deps.Agents), strings.Join(deps.Agents, ", ")))
	}
	plan.deps = deps
	if err := attachToolBundles(info, &plan); err != nil {
		return listingPlan{}, err
	}
	return plan, nil
}

// attachToolBundles attaches the bodies of dependent custom tools to the bundle: files into trees, metadata into the payload.
//
// Shared by agents and skills: both may depend on custom tools, whose directories live outside their own, so
// without this the target tenant would get only a grant row pointing at a tool that is not there.
func attachToolBundles(info *auth.AuthInfo, plan *listingPlan) error {
	trees, bundles, err := bundledToolTrees(info, plan.deps)
	if err != nil {
		return fmt.Errorf("read tool deps: %w", err)
	}
	plan.trees = trees
	plan.payload["tool_bundles"] = bundles
	if len(bundles) > 0 {
		names := make([]string, 0, len(bundles))
		for _, b := range bundles {
			names = append(names, asString(b["name"]))
		}
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"the %d dependent custom tools travel with the bundle (%s) and are installed alongside it (enabled)",
			len(bundles), strings.Join(names, ", ")))
	}
	return nil
}

// ===== Custom tools =====

// listingTool lists one script tool.
//
// A tool's definition of record is TOOL.json in its directory (see the models.ToolFile comment), so the file wins here:
// read the contract, validate, pack the whole directory. The payload carries only the two or three display fields; parameters, language and entry
// all travel inside TOOL.json with the files, and the target tenant parses them back into a row.
func listingTool(c *gin.Context, info *auth.AuthInfo, name string) {
	plan, err := planTool(info, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	listingEntity(c, info, plan)
}

func planTool(info *auth.AuthInfo, name string) (listingPlan, error) {
	if err := assetpack.ValidateName(name); err != nil {
		return listingPlan{}, err
	}
	st, serr := getStorage()
	if serr != nil {
		return listingPlan{}, fmt.Errorf("storage not ready: %w", serr)
	}

	ctx := context.Background()
	dir := paths.ToolsKey(info.Schema) + "/" + name
	raw, rerr := st.ReadFile(ctx, path.Join(dir, models.ToolFileName))
	if rerr != nil {
		return listingPlan{}, fmt.Errorf("the tool is missing its contract file %s and cannot be listed", models.ToolFileName)
	}
	tf, perr := models.ParseToolFile(raw)
	if perr != nil {
		return listingPlan{}, fmt.Errorf("failed to parse %s: %w", models.ToolFileName, perr)
	}
	if err := tf.Validate(name); err != nil {
		return listingPlan{}, err
	}

	// The table row only supplies status and id (the definition lives in the file); a directory without a row is fine and counts as normal.
	sourceID := uuid.NewString()
	status := models.StatusNormal
	if t, gerr := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema).Get("", name); gerr == nil && t != nil {
		sourceID = t.ID
		status = int(t.Status)
	} else if gerr != nil && !errors.Is(gerr, sql.ErrNoRows) {
		return listingPlan{}, fmt.Errorf("read tool row: %w", gerr)
	}
	// No entity-state gate: deprecated state only warns.
	plan := listingPlan{
		kind:     assetpack.KindTool,
		name:     name,
		sourceID: sourceID,
		entry:    tf.Entry,
		srcDir:   dir,
		payload: map[string]any{
			"name":         name,
			"display_name": tf.DisplayName,
			"description":  tf.Description,
		},
		deps: assetpack.Deps{
			Requires: []string{"sandbox.exec"},
			Egress:   tf.Egress,
		},
	}
	if status == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "this tool is deprecated: the bundle holds its current content")
	}
	if tf.Egress {
		plan.warnings = append(plan.warnings, "this tool declares egress: the target tenant must enable net.egress to install it")
	}
	return plan, nil
}

// ===== Workflows =====

// listingWorkflow lists one workflow.
//
// A workflow's body is one JSONB row in {schema}.flows (diagram + variables + limits); there is no directory on disk.
// So that the "pack -> review -> bundle -> import -> install" file pipeline can be reused as is, the whole row is serialised into
// FLOW.json in a temporary directory, handed to listingEntity for packing, and removed afterwards -
// the snapshot (platform-assets/...) is the only source of truth, and no temporary directory stays in the tenant tree.
//
// Three things are deliberately left out (see the assetpack.WorkflowSnapshot comment): id / status / endpoints,
// and created_by. An install lands as normal; the consumer checks the dependencies and then releases a version.
func listingWorkflow(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planWorkflow(info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	listingEntity(c, info, plan)
}

func planWorkflow(info *auth.AuthInfo, id string) (listingPlan, error) {
	f, err := flowRepo.GetFlow(info, id)
	if err != nil {
		return listingPlan{}, fmt.Errorf("read workflow: %w", err)
	}
	if f == nil {
		return listingPlan{}, errors.New("workflow not found")
	}
	// No entity-state gate: deprecated state only warns.
	plan := listingPlan{
		kind:     assetpack.KindWorkflow,
		name:     f.Name,
		sourceID: f.ID,
		entry:    assetpack.WorkflowFileName,
	}
	if int(f.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "this workflow is deprecated: the bundle holds its current content")
	}

	def := map[string]any{
		"name":                f.Name,
		"description":         string(f.Description),
		"diagram":             f.Diagram.Data,
		"arguments":           f.Arguments.Data,
		"outputs":             f.Outputs.Data,
		"env_vars":            f.EnvVars.Data,
		"flow_vars":           f.FlowVars.Data,
		"timeout":             int(f.Timeout),
		"max_recursion_depth": int(f.MaxRecursionDepth),
		"max_node_count":      int(f.MaxNodeCount),
		"max_exec_count":      int(f.MaxExecCount),
		"max_run_records":     int(f.MaxRunRecords),
		"max_session_records": int(f.MaxSessionRecords),
	}
	// The whole definition goes into FLOW.json, so credentials are scanned across all of it (env_vars and the api_key values in node configs).
	clean, secretWarnings := assetpack.StripSecrets(assetpack.KindWorkflow, def)
	raw, merr := json.Marshal(clean)
	if merr != nil {
		return listingPlan{}, fmt.Errorf("encode workflow: %w", merr)
	}
	// Dependencies are scanned here: a workflow's plan.payload holds only the name and description, while global-variable references sit in
	// the diagram and env_vars (see the plan.payload assignment below). Without this,
	// a workflow asset's GlobalVars would always be empty and an import would never report a missing variable.
	// It scans the masked clean copy: reference names survive and inline credentials are already blanked.
	plan.deps.GlobalVars = assetpack.CollectGlobalVarRefs(context.Background(), nil, "", clean)

	st, serr := getStorage()
	if serr != nil {
		return listingPlan{}, fmt.Errorf("storage not ready: %w", serr)
	}
	ctx := context.Background()
	tmpDir := path.Join(paths.TenantsSubdir, info.Schema, ".listing-tmp", uuid.NewString())
	if werr := st.WriteFile(ctx, path.Join(tmpDir, assetpack.WorkflowFileName), raw); werr != nil {
		_ = st.DeletePrefix(context.Background(), tmpDir)
		return listingPlan{}, fmt.Errorf("failed to write the workflow snapshot: %w", werr)
	}

	plan.warnings = append(plan.warnings,
		"model / tool / sub-flow references in the workflow do not migrate with the asset; reconfigure them one by one in the editor after installing",
		"an install lands as a draft you can edit; enable it once the references are checked")
	plan.warnings = append(plan.warnings, secretWarnings...)
	plan.srcDir = tmpDir
	plan.payload = map[string]any{
		"name":        f.Name,
		"description": string(f.Description),
	}
	// The temporary directory only lets a workflow reuse the file pipeline; the snapshot / bundle is the source of truth, so it must be removed at the end.
	plan.cleanup = func() { _ = st.DeletePrefix(context.Background(), tmpDir) }
	return plan, nil
}
