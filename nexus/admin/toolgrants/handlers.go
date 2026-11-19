// Package toolgrants exposes the management API of the tool grant system:
//   - GET  /admin/toolcatalog/                     the referenceable catalog (natural groups / preset groups / custom groups / built-in tools)
//   - GET  /admin/agents/:id/toolgrants            an agent's grants + the unrestricted switch
//   - PUT  /admin/agents/:id/toolgrants            replace the grants wholesale (the unrestricted switch included)
//   - GET  /admin/agents/:id/effective-tools       the tools actually in effect (with sources and warnings)
//   - GET  /admin/toolgroups/                      custom group list
//   - POST /admin/toolgroups/                      create / update a custom group (upsert by name)
//   - DELETE /admin/toolgroups/:name               delete a custom group (with a reference check)
//   - GET  /admin/toolgrants/usages                reference detail for a ref (the impact check before deleting / delisting)
package toolgrants

import (
	"fmt"
	"sort"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/models"
	agentsrepo "flame/nexus/shared/repository/agents"
	skillsrepo "flame/nexus/shared/repository/skills"
	repo "flame/nexus/shared/repository/toolgrants"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

var (
	grantsRepo = repo.NewRepository()
	skillRepo  = skillsrepo.NewRepository()
	agentsRepo = agentsrepo.NewRepository()
)

// authOf returns the request's auth info (the response is already written on failure).
func authOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	return info, true
}

// agentIsSupervisor reports whether an agent carries super-user power (agents.is_supervisor).
// A miss (or an error) counts as "not a supervisor", the tighter default - the catalog uses it to hide
// the admin tools reserved for supervisors (category super).
func agentIsSupervisor(info *auth.AuthInfo, agentID string) bool {
	inst, err := agentsrepo.NewRepository().GetAgent(info, agentID)
	return err == nil && inst != nil && inst.IsSupervisor
}

// ListToolCatalog builds the referenceable catalog: natural groups / preset groups / custom groups (with their expanded members) + every built-in tool.
//
// The optional agent_id query generates the catalog for that agent's identity - a non-super agent hides the
// super-only admin tools (the whole builtin:super group is omitted, as are custom / preset groups whose members are all filtered out);
// omitting it returns everything (needed by the skill tool-dependency picker, custom group editing and so on).
func ListToolCatalog(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	isSupervisor := true
	if agentID := strings.TrimSpace(c.Query("agent_id")); agentID != "" {
		isSupervisor = agentIsSupervisor(info, agentID)
	}
	// Capability view: when the platform has not enabled exec / browser the catalog still lists them but greys them out with a reason
	// (the same "visible but unusable" rule as plugins - hiding them makes people think the tool is gone).
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, isSupervisor).
		WithCapabilities(capabilitypolicy.Snapshot(info.Schema))
	groups, err := catalog.Groups()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"groups":             groups,
		"builtin_tools":      catalog.VisibleBuiltinTools(),
		"builtin_categories": toolgrant.BuiltinCategories(),
		"presets":            toolgrant.PresetGroups,
	}, "")
}

// GetAgentToolGrants reads an agent's grants and its "unrestricted tools" switch.
func GetAgentToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	grants, err := grantsRepo.ListAgentGrants(info.Schema, agentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// "Unrestricted tools" is an agent attribute (agents.tools_unrestricted) and goes through the agents repository
	unrestricted, err := agentsRepo.GetUnrestricted(info, agentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"agent_id":     agentID,
		"grants":       grants,
		"unrestricted": unrestricted,
	}, "")
}

// SetAgentToolGrants replaces an agent's grants wholesale (optionally setting the unrestricted switch too).
// body: {"unrestricted":true,"grants":[{"grant_kind":"allow","item_type":"group","ref":"builtin:file"}]}
func SetAgentToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	var body struct {
		Unrestricted *bool `json:"unrestricted"`
		Grants       []struct {
			GrantKind string `json:"grant_kind"`
			Ref       string `json:"ref"`
			// item_type is not accepted: it is derived from the ref shape (toolgrant.ItemTypeOfRef),
			// and extra fields from the client are silently ignored by JSON binding.
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, agentIsSupervisor(info, agentID))
	grants := make([]models.AgentToolGrant, 0, len(body.Grants))
	requiredRefs := map[string]bool{}
	for _, g := range body.Grants {
		g.Ref = strings.TrimSpace(g.Ref)
		// The agent side may reference tool groups (allowGroup=true)
		if err := toolgrant.ValidateRef(g.Ref, true); err != nil {
			xhttp.ErrCode(c, "toolgrant.invalid_ref", gin.H{"ref": g.Ref}, "invalid ref "+g.Ref+": "+err.Error(), "")
			return
		}
		switch g.GrantKind {
		case models.GrantKindRequired, models.GrantKindAllow, models.GrantKindDeny:
		default:
			xhttp.ErrCode(c, "toolgrant.invalid_grant_kind", gin.H{"ref": g.Ref}, "invalid grant_kind for "+g.Ref, "")
			return
		}
		// Existence check: a ref must expand to at least one tool (groups and single tools alike)
		list, err := catalog.ToolsOfRef(g.Ref)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.ref_not_found", nil, "ref not found or has no tools: "+g.Ref, "")
			return
		}
		if g.GrantKind == models.GrantKindRequired {
			requiredRefs[g.Ref] = true
		}
		grants = append(grants, models.AgentToolGrant{
			AgentID:   agentID,
			GrantKind: g.GrantKind,
			// The shape comes from the ref (the same source as the grant_agent_tool meta tool): an item_type from the caller does not count,
			// or rows such as item_type=tool with ref=group:x would be written and quietly corrupt the UI's checkbox state.
			ItemType: toolgrant.ItemTypeOfRef(g.Ref),
			Ref:      g.Ref,
		})
	}
	// A ref marked "must be granted" cannot also be denied (that would contradict itself)
	for _, g := range grants {
		if g.GrantKind == models.GrantKindDeny && requiredRefs[g.Ref] {
			xhttp.ErrCode(c, "toolgrant.ref_required", gin.H{"ref": g.Ref}, "ref is required, cannot be denied: "+g.Ref, "")
			return
		}
	}

	if err := grantsRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if body.Unrestricted != nil {
		if err := agentsRepo.SetUnrestricted(info, agentID, *body.Unrestricted); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(grants)}, "")
}

// GetAgentEffectiveTools returns the tools actually in effect (degradation flags, sources, warnings), for UI preview and diagnosis.
func GetAgentEffectiveTools(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	agentID := c.Param("id")

	// The same rule as at run time: candidates come only from skills "activated this turn" (after the model calls use_skill).
	// A preview happens outside a run, where no "activated" state exists, so candidates are always empty and
	// this reflects the tool set **at run start** (required only). allow rows light up only once the model activates the skill that declares them;
	// which skill backs each allow row is in grant_views of read_agent (pending + via_skill).
	visible := []toolgrant.VisibleSkill{}

	// The instance is fetched along the way: both the identity test and the "unrestricted tools" switch (an agent attribute) come from it, saving a query
	agent, err := agentsRepo.GetAgent(info, agentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	isSupervisor := agent != nil && agent.IsSupervisor
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, isSupervisor).
		WithCapabilities(capabilitypolicy.Snapshot(info.Schema))
	res, err := toolgrant.NewResolver(catalog, info.Schema, visible, agent.ToolsUnrestricted).Resolve(agentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if res.Degraded {
		// Degraded = allow rows exist but no candidate. In a preview that is not a fault but "on-demand has not fired yet":
		// without a word about it the admin assumes the grants are gone.
		res.Warnings = append(res.Warnings, "this previews the tool set at run start: allow grants take effect only after the model calls use_skill to activate the skill that declares them")
	}

	// With no grants configured the run time ignores the resolution above, and the preview must reflect that:
	//   - a super agent defaults to "unrestricted tools + every built-in tool" (no longer driven by config.tools)
	//   - any other agent binds no tools by default
	grants, grantsErr := grantsRepo.ListAgentGrants(info.Schema, agentID)
	noGrants := grantsErr == nil && len(grants) == 0
	if noGrants {
		if !res.Unrestricted || isSupervisor {
			res.NoGrants = true
		}
		if isSupervisor {
			res.Unrestricted = true
		}
	}

	// A super agent's "open by default" now rests on **one** test: zero grant rows (the bootstrap fuse).
	// It shares its source with the zero-row fallback in agent_impl.resolveGrantedTools, or
	// "the preview says 2, the run binds everything" would drift apart.
	// The migration gate ("no core group in the grant rows") was removed once the backfill completed.
	if isSupervisor && noGrants {
		res.BuiltinDefault = true
		res.FinalTools = mergeSupervisorBuiltins(res.FinalTools, res.DenyRefs, catalog.VisibleBuiltinTools())
		res.FinalRefs = res.FinalRefs[:0]
		res.FinalNames = res.FinalNames[:0]
		for _, t := range res.FinalTools {
			res.FinalRefs = append(res.FinalRefs, t.Ref)
			res.FinalNames = append(res.FinalNames, t.Name)
		}
		// A super agent's built-in tools are the inherent baseline: platform-disabled ones are still listed (marked "not enabled by the platform")
		// but never bound at run time - left unsaid, "effective tools" promises something that will not happen.
		if blocked := unavailableRefs(res.FinalTools); len(blocked) > 0 {
			res.Warnings = append(res.Warnings, "not enabled by the platform, so they are not bound at run time: "+strings.Join(blocked, ", "))
		}
	}

	xhttp.OkJSON(c, "Success", res, "")
}

// mergeSupervisorBuiltins folds the built-in tools into a super agent's tool set (deny wins, deduplicated by name and sorted).
//
// builtins must come from the **capability-aware catalog** (catalog.VisibleBuiltinTools),
// never from the package-level toolgrant.BuiltinTools(): its Available is always true,
// so a platform-disabled browser / exec would show as usable.
func mergeSupervisorBuiltins(list []toolgrant.ToolInfo, denyRefs []string, builtins []toolgrant.ToolInfo) []toolgrant.ToolInfo {
	denied := make(map[string]bool, len(denyRefs))
	for _, ref := range denyRefs {
		denied[ref] = true
	}
	seen := make(map[string]bool, len(list))
	out := make([]toolgrant.ToolInfo, 0, len(list)+len(builtins))
	for _, t := range list {
		if denied[t.Ref] || seen[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		out = append(out, t)
	}
	for _, t := range builtins {
		if denied[t.Ref] || seen[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// unavailableRefs lists the refs that are unavailable (one combined warning; one per item would flood the screen).
func unavailableRefs(list []toolgrant.ToolInfo) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		if !t.Available {
			out = append(out, t.Ref)
		}
	}
	return out
}

// GetSkillToolGrants reads the tool dependencies a skill declares (a skill only declares a need; it grants nothing).
func GetSkillToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	skillID := c.Param("id")

	grants, err := grantsRepo.ListSkillGrants(info.Schema, skillID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"skill_id": skillID, "items": grants}, "")
}

// SetSkillToolGrants replaces a skill's tool dependencies wholesale.
// body: {"items":[{"item_type":"group","ref":"builtin:file"}]}
// Unlike the agent side, custom groups cannot be referenced (that would let a hand-made skill escalate itself) and there is no required / allow / deny.
func SetSkillToolGrants(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	skillID := c.Param("id")

	var body struct {
		Items []struct {
			Ref string `json:"ref"`
			// item_type is not accepted: it is derived from the ref shape
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	// A skill's tool dependencies are a tenant-level declaration of need, independent of any agent identity:
	// the full catalog is kept (builtin:super included) so agent-authoring / skill-authoring can declare admin tools.
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, true)
	grants := make([]models.SkillToolGrant, 0, len(body.Items))
	seen := map[string]bool{}
	for _, it := range body.Items {
		it.Ref = strings.TrimSpace(it.Ref)
		if seen[it.Ref] {
			continue
		}
		// allowGroup=false: the skill side may not reference tool groups
		if err := toolgrant.ValidateRef(it.Ref, false); err != nil {
			xhttp.ErrCode(c, "toolgrant.invalid_ref", gin.H{"ref": it.Ref}, "invalid ref "+it.Ref+": "+err.Error(), "")
			return
		}
		list, err := catalog.ToolsOfRef(it.Ref)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.ref_not_found", nil, "ref not found or has no tools: "+it.Ref, "")
			return
		}
		seen[it.Ref] = true
		grants = append(grants, models.SkillToolGrant{SkillID: skillID, ItemType: toolgrant.ItemTypeOfRef(it.Ref), Ref: it.Ref})
	}

	if err := grantsRepo.ReplaceSkillGrants(info.Schema, skillID, grants); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(grants)}, "")
}

// ListToolGroups lists custom groups (members included).
func ListToolGroups(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	groups, err := grantsRepo.ListGroups(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": groups}, "")
}

// UpsertToolGroup creates / updates a custom group (upsert by name, members replaced wholesale).
// body: {"name":"read-only tools","description":"...","items":[{"ref":"builtin:file"}]}
// (item_type is derived from the ref and not accepted)
func UpsertToolGroup(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Items       []struct {
			Ref string `json:"ref"`
			// item_type is not accepted: it is derived from the ref shape
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		xhttp.ErrCode(c, "toolgrant.name_required", nil, "name is required", "")
		return
	}
	// Group name validity (reusing the ref validator: group:<name>)
	if err := toolgrant.ValidateRef(toolgrant.ScopeGroup+":"+body.Name, true); err != nil {
		xhttp.ErrCode(c, "toolgrant.invalid_group_name", nil, "invalid group name: "+err.Error(), "")
		return
	}

	// A custom group is a tenant-level resource, so it is validated against the full catalog (run time filters by agent identity again; see Resolve on the agent side).
	catalog := toolgrant.NewCatalog(info.Schema, grantsRepo, true)
	items := make([]models.ToolGroupItem, 0, len(body.Items))
	for _, it := range body.Items {
		it.Ref = strings.TrimSpace(it.Ref)
		// A group member may not reference a tool group (no nesting)
		if err := toolgrant.ValidateRef(it.Ref, false); err != nil {
			xhttp.ErrCode(c, "toolgrant.invalid_item", gin.H{"ref": it.Ref}, "invalid item "+it.Ref+": "+err.Error(), "")
			return
		}
		list, err := catalog.ToolsOfRef(it.Ref)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if len(list) == 0 {
			xhttp.ErrCode(c, "toolgrant.item_not_found", nil, "item not found or has no tools: "+it.Ref, "")
			return
		}
		items = append(items, models.ToolGroupItem{ItemType: toolgrant.ItemTypeOfRef(it.Ref), Ref: it.Ref})
	}

	id, err := grantsRepo.UpsertGroup(info.Schema, body.Name, body.Description, info.ID, items)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "name": body.Name, "items": len(items)}, "")
}

// DeleteToolGroup deletes a custom group (refused while an agent grant, skill dependency or another group references it).
func DeleteToolGroup(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "toolgrant.name_required", nil, "name is required", "")
		return
	}

	n, err := grantsRepo.CountRefUsages(info.Schema, toolgrant.ScopeGroup+":"+name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if n > 0 {
		xhttp.ErrCode(c, "toolgrant.group_in_use", gin.H{"count": n}, fmt.Sprintf("group is referenced by %d place(s), remove them first", n), "")
		return
	}
	if err := grantsRepo.DeleteGroup(info.Schema, name); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"name": name}, "")
}

// GetRefUsages returns the references to a ref (agent grants / skill dependencies / custom group members).
// query: ref=<ref> (required); prefix=1 also counts children (ref=mcp:fs then matches mcp:fs:read_file too).
// Use: the "referenced N times" count on the tools and endpoints page, and the impact preview before a delete.
func GetRefUsages(c *gin.Context) {
	info, ok := authOf(c)
	if !ok {
		return
	}
	ref := strings.TrimSpace(c.Query("ref"))
	if ref == "" {
		xhttp.ErrCode(c, "toolgrant.ref_required", nil, "ref is required", "")
		return
	}
	prefix := c.Query("prefix") == "1" || c.Query("prefix") == "true"

	res, err := grantsRepo.ListRefUsages(info.Schema, ref, prefix)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", res, "")
}
