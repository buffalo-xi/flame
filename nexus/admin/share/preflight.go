package share

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	globalvarsrepo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// globalVarRepo is the global-variable store (used only for "is a dependency declared in the bundle present").
var globalVarRepo = globalvarsrepo.NewRepository()

// depItem is the check result of one dependency.
type depItem struct {
	Kind string `json:"kind"` // skill | tool | agent | capability | global_var
	// Scope is the source kind of a tool dependency: custom | mcp | rpc | local | builtin | plugin | group | preset.
	// Empty for skills / agents / capabilities (Kind already says it).
	Scope string `json:"scope,omitempty"`
	Ref   string `json:"ref"`
	// Via names who wants this dependency: empty = declared by the entity itself; "skill:foo" = a tool dependency declared by skill foo.
	Via string `json:"via,omitempty"`
	// State takes the dep* constants.
	State string `json:"state"`
	// Total / Items are the expansion of a group ref (Items keeps the first 8, for display).
	Total  int      `json:"total,omitempty"`
	Items  []string `json:"items,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

const (
	depOK         = "ok"         // present and usable in this tenant
	depBundled    = "bundled"    // shipped with the bundle, installed alongside it
	depDisabled   = "disabled"   // present but currently unusable (connection disabled / endpoint not exposed / capability not enabled / tool disabled)
	depDeprecated = "deprecated" // present but deprecated
	depMissing    = "missing"    // not present in this tenant
)

// depItemsShown caps how many member names a group ref shows (the list is for people, not machines).
const depItemsShown = 8

// refScopeLabel maps a ref prefix to a human type name (same wording as the grant panel).
var refScopeLabel = map[string]string{
	toolgrant.ScopeBuiltin: "built-in tool",
	toolgrant.ScopeMCP:     "MCP tool",
	toolgrant.ScopeRPC:     "RPC tool",
	toolgrant.ScopeLocal:   "local endpoint tool",
	toolgrant.ScopePlugin:  "plugin tool",
	toolgrant.ScopeCustom:  "custom tool",
	toolgrant.ScopeGroup:   "tool group",
	toolgrant.ScopePreset:  "preset group",
	"global_var":           "global variable",
}

// Inspect POST /admin/share/import/inspect
//
// Before installing, show "what this bundle depends on and whether this tenant has it". The install itself only records
// what is missing in warnings (a missing skill skips its binding, a missing tool ref points at nothing), so the user only learns "3 items did not take effect" afterwards.
// The dependency manifest is static inside the bundle (manifest.deps + payload), so it can be checked before anything is touched.
//
// Read-only: no temporary directory, no platform row, no write to the tenant schema.
func Inspect(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	pkg, files, kind, ok := readUploadedPackage(c)
	if !ok {
		return
	}

	payload := map[string]any{}
	if jerr := json.Unmarshal(pkg.Payload, &payload); jerr != nil {
		payload = map[string]any{}
	}
	// The same masking as Import: the check must look at "the payload that will really be installed",
	// or the preflight identity (a super-agent switch, say) may differ from what is actually installed.
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = pkg.Manifest.Name
	}

	deps := inspectDeps(info, pkg.Manifest, clean, files)
	deps = append(deps, inspectCapabilities(info, pkg.Manifest.Deps.Requires)...)
	missing := 0
	for _, d := range deps {
		if d.State == depMissing {
			missing++
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"kind":     string(kind),
		"name":     name,
		"deps":     deps,
		"summary":  depSummary(deps),
		"missing":  missing,
		"existing": existingEntity(info, kind, name),
		"bundled":  bundledSummary(pkg.Manifest),
		"notices":  secretWarnings,
		"notes":    inspectNotes(kind),
		// Content digest / manifest spec version: empty or 0 for old bundles, shown only when set
		"digest": pkg.Manifest.Digest,
		"spec":   pkg.Manifest.Spec,
	}, "")
}

// inspectInstallRequest is the preflight input for a marketplace install.
type inspectInstallRequest struct {
	AssetID string `json:"asset_id"`
	Name    string `json:"name"`
}

// InspectAsset POST /admin/share/install/inspect
//
// Same source as Inspect (the uploaded-bundle preflight): before installing, lay out "does this tenant already have a same-named entity, and does it have what the bundle needs".
// This should be visible before clicking install in the marketplace, not counted in warnings afterwards.
// Read-only: no directories, no tenant schema write, no install log entry.
func InspectAsset(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var req inspectInstallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	asset, m, kind, name, rerr := resolveInstallTarget(info, req.AssetID, req.Name)
	if rerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, rerr.Error(), "")
		return
	}
	// The same masking as Import: look at "the payload that will really be installed", or identities such as a super agent are read wrong
	payload, notices := assetpack.StripSecrets(kind, payloadFromAsset(asset))

	deps := inspectDeps(info, m, payload, nil)
	deps = append(deps, inspectCapabilities(info, m.Deps.Requires)...)
	missing := 0
	for _, d := range deps {
		if d.State == depMissing {
			missing++
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"kind":     string(kind),
		"name":     name,
		"deps":     deps,
		"summary":  depSummary(deps),
		"missing":  missing,
		"existing": existingEntity(info, kind, name),
		"bundled":  bundledSummary(m),
		"notices":  notices,
		"notes":    inspectNotes(kind),
		// Content digest / manifest spec version: computed at listing time and carried in the manifest; empty for older local assets
		"digest": m.Digest,
		"spec":   m.Spec,
	}, "")
}

// inspectDeps checks each dependency against this tenant.
//
// Tool dependencies are looked up by ref prefix (see refScopeLabel): group refs at group level, single tools at leaf level.
// It only covers dependencies the bundle declares - run-time dynamic choice (a model picking a skill or tool on the fly) cannot
// be decided statically, so it is better to say less than to pretend (the notes say so explicitly).
func inspectDeps(info *auth.AuthInfo, m assetpack.Manifest, payload map[string]any,
	files map[string][]byte) []depItem {

	items := make([]depItem, 0, len(m.Deps.Skills)*2+len(m.Deps.Tools)+len(m.Deps.Agents))
	attachments := assetpack.Attachments(m.Files)

	// The catalog's view must match "what this entity will be after install": only a supervisor sees the admin groups,
	// otherwise every admin group would be reported missing.
	cat := toolgrant.NewCatalog(info.Schema, grantRepo, flagFrom(payload, "is_supervisor", false))

	// Group status table: built only when a group dependency exists (Groups() queries every MCP / RPC connection for its tool list)
	var groups map[string]toolgrant.GroupInfo
	if needsGroups(m.Deps.Tools) {
		if gs, gerr := cat.Groups(); gerr == nil {
			groups = make(map[string]toolgrant.GroupInfo, len(gs))
			for _, g := range gs {
				groups[g.Ref] = g
			}
		}
	}

	// Skills: first whether this tenant has it, then spread "the tools this skill declares" into transitive dependencies -
	// the skill being present while its tools are missing still means those tools cannot be injected at run time.
	for _, sname := range m.Deps.Skills {
		skill := inspectSkill(info, sname)
		items = append(items, skill)
		if skill.State != depOK {
			continue
		}
		s, serr := skillRepo.FindByName(info.Schema, sname, "")
		if serr != nil || s == nil {
			continue
		}
		grants, gerr := grantRepo.ListSkillGrants(info.Schema, string(s.ID))
		if gerr != nil {
			continue
		}
		for _, g := range grants {
			item := inspectTool(cat, groups, assetpack.ToolDep{Type: g.ItemType, Ref: g.Ref}, attachments)
			item.Via = "skill:" + sname
			items = append(items, item)
		}
	}

	// Tool dependencies declared by the entity itself
	for _, d := range m.Deps.Tools {
		items = append(items, inspectTool(cat, groups, d, attachments))
	}

	for _, aname := range m.Deps.Agents {
		items = append(items, inspectCalledAgent(info, aname))
	}

	// Global variables: the bundle carries only names, never values - which one is missing must be said before installing,
	// otherwise it shows up as "install looked fine, the first node run failed".
	for _, name := range m.Deps.GlobalVars {
		items = append(items, inspectGlobalVar(info, name))
	}
	return items
}

// inspectGlobalVar checks one global-variable dependency (is this name in {schema}.global_vars).
//
// Only existence, never the value: an empty value is a choice too (a deliberate empty string),
// and "empty value" behaves completely differently from "variable missing" at run time (the first runs, the second fails hard).
func inspectGlobalVar(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "global_var", Ref: name}
	v, err := globalVarRepo.Get(info.Schema, name)
	if err != nil {
		item.State, item.Detail = depMissing, "failed to read the global variable: "+err.Error()
		return item
	}
	if v == nil {
		item.State = depMissing
		item.Detail = "this tenant does not have this global variable; create it under Settings -> Global variables first"
		return item
	}
	item.State = depOK
	item.Detail = "present in this tenant"
	if v.Kind == "secret" {
		// Say clearly that "present" is not "correct": the importer uses its own value
		item.Detail = "present in this tenant (secret type: uses your value here, never travels with the bundle)"
	}
	return item
}

// needsGroups reports whether the group status table is needed (only for group-shaped refs).
func needsGroups(deps []assetpack.ToolDep) bool {
	for _, d := range deps {
		if p, err := toolgrant.ParseRef(d.Ref); err == nil && p.IsGroup(toolgrant.BuiltinCategories()) {
			return true
		}
	}
	return false
}

// inspectTool checks one tool dependency.
//
// The branch order matters:
//  1. a custom tool shipped with the bundle (its body is inside, so it exists after install) - recognise it first, do not report it missing;
//  2. group shape -> check group status (present / enabled / how many members it expands to);
//  3. single-tool shape -> check the leaf (present / usable / why not).
func inspectTool(cat *toolgrant.Catalog, groups map[string]toolgrant.GroupInfo,
	d assetpack.ToolDep, attachments map[string][]assetpack.FileEntry) depItem {

	item := depItem{Kind: "tool", Ref: d.Ref}

	p, perr := toolgrant.ParseRef(d.Ref)
	if perr != nil {
		item.State, item.Detail = depMissing, "cannot parse the ref: "+perr.Error()
		return item
	}
	item.Scope = p.Scope

	if p.Scope == toolgrant.ScopeCustom {
		if _, ok := attachments[p.Subject]; ok {
			item.State = depBundled
			item.Detail = "shipped with the bundle: installed alongside it (enabled), its settings must be filled in by hand"
			return item
		}
	}

	if p.IsGroup(toolgrant.BuiltinCategories()) {
		return inspectToolGroup(groups, item, d.GrantKind)
	}

	list, err := cat.ToolsOfRef(d.Ref)
	if err != nil {
		item.State, item.Detail = depMissing, "expansion failed: "+err.Error()
		return item
	}
	if len(list) == 0 {
		item.State = depMissing
		item.Detail = missingRefHint(item.Scope, p.Subject)
		return item
	}
	leaf := list[0]
	if !leaf.Available {
		item.State, item.Detail = depDisabled, unavailableText(leaf)
		return item
	}
	item.State, item.Total = depOK, 1
	item.Items = []string{leaf.Name}
	item.Detail = "present in this tenant: " + refScopeLabel[item.Scope] + " " + leaf.Name
	if d.GrantKind != "" {
		item.Detail += "; grant kind: " + grantKindLabel(d.GrantKind)
	}
	return item
}

// inspectToolGroup handles a group ref (mcp:fs / rpc:erp / local:rpc / group:x / preset:x / builtin:file).
//
// It must query Groups() rather than only ToolsOfRef: when a connection exists but is disabled, ToolsOfRef still returns members
// (Available=false), while a connection that does not exist yields an empty list - the two must be told apart,
// or the user fixes the wrong layer (editing a connection vs creating one).
func inspectToolGroup(groups map[string]toolgrant.GroupInfo, item depItem, grantKind string) depItem {
	g, ok := groups[item.Ref]
	if !ok {
		item.State = depMissing
		item.Detail = missingRefHint(item.Scope, strings.SplitN(item.Ref, ":", 2)[1])
		return item
	}

	item.Total = len(g.Items)
	for i, t := range g.Items {
		if i >= depItemsShown {
			break
		}
		item.Items = append(item.Items, t.Name)
	}
	if !g.Available {
		item.State = depDisabled
		item.Detail = strings.TrimSpace(g.UnavailableReason)
		if item.Detail == "" {
			item.Detail = "the " + refScopeLabel[item.Scope] + " is currently unavailable"
		}
		if item.Total > 0 {
			item.Detail += fmt.Sprintf(" (%d tools in the group)", item.Total)
		}
		return item
	}
	if item.Total == 0 {
		item.State = depDisabled
		item.Detail = "the group exists but expands to no tools right now (the connection has none / every member was deleted)"
		return item
	}
	item.State = depOK
	item.Detail = fmt.Sprintf("this tenant already has the %s, expanding to %d tools", refScopeLabel[item.Scope], item.Total)
	// For a mixed group (common in custom groups) spell out where the members come from: some from an MCP connection, some tenant scripts
	if src := sourceBreakdown(g.Items); src != "" {
		item.Detail += "：" + src
	}
	if grantKind != "" {
		item.Detail += "; grant kind: " + grantKindLabel(grantKind)
	}
	return item
}

func inspectSkill(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "skill", Ref: name}
	s, err := skillRepo.FindByName(info.Schema, name, "")
	if err != nil {
		item.State, item.Detail = depMissing, "query failed: "+err.Error()
		return item
	}
	if s == nil {
		item.State = depMissing
		item.Detail = "this tenant has no same-named tenant-level skill: this binding is skipped on install, so create one first (or bind it by hand afterwards)"
		return item
	}
	if int(s.Status) == models.StatusDeprecated {
		item.State, item.Detail = depDeprecated, "the same-named skill is deprecated: the binding is skipped on install, restore the skill and retry"
		return item
	}
	item.State, item.Detail = depOK, "this tenant already has a same-named skill; the install binds it by name automatically"
	return item
}

func inspectCalledAgent(info *auth.AuthInfo, name string) depItem {
	item := depItem{Kind: "agent", Ref: name}
	a, err := agentRepo.FindByName(info, name)
	if err != nil {
		item.State, item.Detail = depMissing, "query failed: "+err.Error()
		return item
	}
	if a == nil {
		item.State = depMissing
		item.Detail = "this tenant has no same-named agent: the call grant cannot be rebuilt, so create the agent after installing"
		return item
	}
	if int(a.Status) == models.StatusDeprecated {
		item.State, item.Detail = depDeprecated, "the same-named agent is deprecated: re-granting will not make it callable"
		return item
	}
	item.State, item.Detail = depOK, "this tenant already has a same-named agent (call grants must be reconfigured separately; they are not rebuilt)"
	return item
}

// inspectCapabilities checks the capabilities the bundle requires (sandbox.exec / net.egress) against this tenant.
func inspectCapabilities(info *auth.AuthInfo, requires []string) []depItem {
	if len(requires) == 0 {
		return nil
	}
	caps := capabilitypolicy.Snapshot(info.Schema)
	out := make([]depItem, 0, len(requires))
	for _, key := range requires {
		item := depItem{Kind: "capability", Ref: key}
		if caps[key] {
			item.State, item.Detail = depOK, "enabled in this tenant"
		} else {
			item.State = depMissing
			item.Detail = "this tenant has not enabled \"" + capability.Label(key) + "\": tools depending on it install but cannot run; a platform admin must enable it"
		}
		out = append(out, item)
	}
	return out
}

// existingEntity reports whether a same-named entity exists (the front end warns "will overwrite" or "rename it" up front).
func existingEntity(info *auth.AuthInfo, kind assetpack.Kind, name string) any {
	switch kind {
	case assetpack.KindAgent:
		if a, err := agentRepo.FindByName(info, name); err == nil && a != nil {
			return gin.H{"name": a.Name, "status": int(a.Status), "can_overwrite": true}
		}
	case assetpack.KindSkill:
		// Only a tenant-level same-named skill counts: private skills are a different namespace, so they do not clash (and an overwrite cannot reach them)
		if s, err := skillRepo.FindByName(info.Schema, name, ""); err == nil && s != nil {
			return gin.H{"name": s.Name, "status": int(s.Status), "can_overwrite": supportsOverwrite(assetpack.KindSkill)}
		}
	case assetpack.KindTool:
		// A custom tool "exists" by its directory (the same source as the executor and the grant catalog)
		if _, err := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema).Get("", name); err == nil {
			return gin.H{"name": name, "can_overwrite": supportsOverwrite(assetpack.KindTool)}
		}
	case assetpack.KindWorkflow:
		if ok, err := flowRepo.ExistsFlowName(info, name); err == nil && ok {
			return gin.H{"name": name, "can_overwrite": supportsOverwrite(assetpack.KindWorkflow)}
		}
	}
	return nil
}

// bundledSummary lists what ships inside the bundle and is installed with it (not an external dependency, but the user must see it).
func bundledSummary(m assetpack.Manifest) gin.H {
	tools := make([]string, 0)
	for name := range assetpack.Attachments(m.Files) {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	privates := append([]string{}, m.Deps.PrivateSkills...)
	sort.Strings(privates)
	return gin.H{"custom_tools": tools, "private_skills": privates}
}

// depSummary tallies by kind (the front end can render "MCP 3 items (1 missing)").
func depSummary(deps []depItem) []gin.H {
	type tally struct{ total, missing int }
	byKey := map[string]*tally{}
	order := make([]string, 0, 8)
	for _, d := range deps {
		key := d.Kind
		if d.Kind == "tool" && d.Scope != "" {
			key = d.Scope
		}
		t, ok := byKey[key]
		if !ok {
			t = &tally{}
			byKey[key] = t
			order = append(order, key)
		}
		t.total++
		if d.State == depMissing {
			t.missing++
		}
	}
	out := make([]gin.H, 0, len(order))
	for _, key := range order {
		label := refScopeLabel[key]
		if label == "" {
			label = key
		}
		t := byKey[key]
		out = append(out, gin.H{"key": key, "label": label, "total": t.total, "missing": t.missing})
	}
	return out
}

func inspectNotes(kind assetpack.Kind) []string {
	notes := []string{"the dependency check only covers what the bundle declares; run-time dynamic choice (a model picking a skill or tool on the fly) cannot be decided statically"}
	if kind == assetpack.KindWorkflow {
		notes = append(notes,
			"model / tool / sub-flow references inside workflow nodes are not recorded in the bundle; check them one by one in the editor after installing")
	}
	return notes
}

// sourceBreakdown groups members by source, e.g. "mcp:fs x3, custom:foo x1".
func sourceBreakdown(items []toolgrant.ToolInfo) string {
	count := map[string]int{}
	for _, t := range items {
		src := t.Source
		if src == "" {
			src = "unknown"
		}
		count[src]++
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, count[k]))
	}
	return strings.Join(parts, ", ")
}

// missingRefHint gives an actionable sentence for a missing dependency (per kind, not a blanket "does not exist").
func missingRefHint(scope, subject string) string {
	switch scope {
	case toolgrant.ScopeMCP:
		return "MCP connection \"" + subject + "\" does not exist (never configured / deleted): this grant will not take effect after install"
	case toolgrant.ScopeRPC:
		return "RPC connection \"" + subject + "\" does not exist (never configured / deleted): this grant will not take effect after install"
	case toolgrant.ScopeLocal:
		return "this tenant has no rpc endpoint named \"" + subject + "\" (a workflow must expose the rpc protocol first)"
	case toolgrant.ScopeCustom:
		return "this tenant does not have this custom tool: create a same-named tool, or delete this grant"
	case toolgrant.ScopePlugin:
		return "the local plugin registry has no \"" + subject + "\" (not installed / dropped by the name-clash rule)"
	case toolgrant.ScopeGroup:
		return "this tenant does not have this custom tool group (the group is gone, or every member was deleted)"
	case toolgrant.ScopePreset:
		return "this build has no such preset group"
	default:
		return "this build has no such ref"
	}
}

// unavailableText is the shared "present but unusable" copy (ToolInfo already classifies the reason).
func unavailableText(t toolgrant.ToolInfo) string {
	reason := strings.TrimSpace(t.UnavailableReason)
	if reason == "" {
		reason = "this tool is currently unavailable"
	}
	return t.Name + " is unavailable: " + reason
}

func grantKindLabel(kind string) string {
	switch kind {
	case models.GrantKindRequired:
		return "required (always granted)"
	case models.GrantKindDeny:
		return "deny (blocked)"
	default:
		return "allow (per the skill's declaration)"
	}
}
