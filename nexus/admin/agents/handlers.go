package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agentrelations "flame/nexus/shared/repository/agentrelations"
	repository "flame/nexus/shared/repository/agents"
	flowsrepo "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sessions"
	skills "flame/nexus/shared/repository/skills"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/nexus/shared/skeleton"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// Package-level repository instance.
var (
	repo     = repository.NewRepository()
	relRepo  = agentrelations.NewRepository()
	flowRepo = flowsrepo.NewPostgresRepository()
)

// validateAgentNameUnique checks the agent name first: it must not clash with another agent or a flow.
// A non-empty excludeID means a rename: ignore the entity's own row.
func validateAgentNameUnique(info *auth.AuthInfo, name string, excludeID string) error {
	exist, err := repo.ExistsName(info, name, excludeID)
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("agent %q already exists", name)
	}
	flowExist, err := flowRepo.ExistsFlowName(info, name)
	if err != nil {
		return err
	}
	if flowExist {
		return fmt.Errorf("workflow %q already exists; agent name must be unique against workflows", name)
	}
	return nil
}

// agentToMap renders an Agent as a response map.
func agentToMap(instance *models.Agent) gin.H {
	return gin.H{
		"id":          instance.ID,
		"name":        instance.Name,
		"description": instance.Description,
		"config":      instance.Config,
		"status":      instance.Status,
		"endpoints":   instance.Endpoints,
		// The two agent-level switches (same-named columns, both carried by the agents table).
		"tools_unrestricted": instance.ToolsUnrestricted,
		"skill_discovery":    instance.SkillDiscovery,
		// Default reply language (agents.default_language; empty = not configured, so the caller's tag wins).
		"default_language": instance.DefaultLanguage,
		"created_by":       instance.CreatedBy,
		"updated_by":       instance.UpdatedBy,
		"updated_by_name":  instance.UpdatedByName,
		// The two criteria are orthogonal and must not stand in for each other (see shared/models/protected.go):
		// protected     = cannot be deleted or renamed (the preinstalled list, currently main only);
		// is_supervisor = the super-power identity (all built-in tools by default, reads the draft on every entry point, more than one allowed).
		"protected":     models.IsProtectedAgent(instance.Name),
		"is_supervisor": instance.IsSupervisor,
		"created_at":    instance.CreatedAt,
		"updated_at":    instance.UpdatedAt,
	}
}

// ListAgents lists agents.
func ListAgents(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")

	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	// Decode the request parameters.
	var req repository.ListAgentsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}

	// Query the data.
	instances, total, err := repo.ListAgents(authInfo.(*auth.AuthInfo), &req)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Current version numbers in one batch query (the cards show vN); a read failure does not affect the list itself.
	curVers, _ := versioning.CurrentVersions(authInfo.(*auth.AuthInfo).Schema, "agent_versions")

	var items []gin.H
	for i := range instances {
		item := agentToMap(&instances[i])
		// 0 = no released version yet (the card shows "not released").
		item["current_version"] = curVers[instances[i].ID]
		items = append(items, item)
	}

	// Build the response.
	response := gin.H{
		"items": items,
		"total": total,
		"page":  req.Page,
		"limit": req.Limit,
	}

	xhttp.OkJSON(c, "Success", response, "")
}

// GetAgent returns one agent.
func GetAgent(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	s, err := repo.GetAgent(authInfo.(*auth.AuthInfo), id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	body := agentToMap(s)
	// Version state: the editor header distinguishes "not released / released but modified / identical to production" (same rule as flows).
	// Besides files it also compares the private skill metadata - that is part of the definition too.
	body["current_version"], body["dirty"] = 0, false
	if st, serr := getStorage(); serr == nil {
		v, dirty := versioning.AgentVersionState(context.Background(), st, authInfo.(*auth.AuthInfo).Schema, s.ID, s.Name)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// agentRequest is the create / update payload.
type agentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Config      string `json:"config"`
	Status      int    `json:"status"` // 1=active | 3=deprecated
	Endpoints   string `json:"endpoints"`
	// The two agent-level switches are pointers so "not sent" (nil) is distinguishable from an explicit false.
	// On update an unsent switch keeps the stored value (UpdateAgent is a full-column UPDATE, see the fallback there).
	ToolsUnrestricted *bool `json:"tools_unrestricted"`
	SkillDiscovery    *bool `json:"skill_discovery"`
	// DefaultLanguage is the agent's default reply language (agents.default_language, a BCP-47 tag). A pointer for
	// the same reason as the switches: "not sent" keeps the stored value, an explicit empty string clears it.
	DefaultLanguage *string `json:"default_language"`
	// IsSupervisor is the super-power flag (agents.is_supervisor). The admin API is the human management
	// surface and one of the authoritative entry points for it (the other is update_agent, called by the
	// supervisor itself). Updates go through the single-column SetSupervisor, never through UpdateAgent.
	IsSupervisor *bool `json:"is_supervisor"`
	// Call grants (create only): who the new agent may call / who may call it.
	CanCallIDs  []string `json:"can_call_ids"`
	CalledByIDs []string `json:"called_by_ids"`
	// Lang picks the language of the generated AGENT.md skeleton (create only, never stored).
	// Absent or unknown means the English skeleton (see shared/skeleton.Normalize).
	Lang string `json:"lang"`
}

// toModel turns the request into an Agent.
func (req *agentRequest) toModel() models.Agent {
	m := models.Agent{
		Name:        req.Name,
		Description: xsql.String(req.Description),
		Status:      xsql.Int(req.Status),
	}
	if req.Config != "" {
		m.Config = xsql.JSONB{Data: req.Config}
	}
	if req.Endpoints != "" {
		m.Endpoints = xsql.JSONB{Data: req.Endpoints}
	}
	if req.ToolsUnrestricted != nil {
		m.ToolsUnrestricted = *req.ToolsUnrestricted
	}
	if req.SkillDiscovery != nil {
		m.SkillDiscovery = *req.SkillDiscovery
	}
	if req.IsSupervisor != nil {
		m.IsSupervisor = *req.IsSupervisor
	}
	if req.DefaultLanguage != nil {
		m.DefaultLanguage = *req.DefaultLanguage
	}
	return m
}

// normalizeDefaultLanguage trims and validates agents.default_language (an empty value clears it).
//
// A malformed tag is rejected instead of stored: the runtime reads an unknown value as "not configured", so accepting it
// would look like the setting had been applied and done nothing.
func (req *agentRequest) normalizeDefaultLanguage() error {
	if req.DefaultLanguage == nil {
		return nil
	}
	raw := strings.TrimSpace(*req.DefaultLanguage)
	if raw == "" {
		req.DefaultLanguage = &raw
		return nil
	}
	tag, _ := core.NormalizeOutputLanguage(raw)
	if tag == "" {
		return fmt.Errorf("default_language %q is not a valid BCP-47 language tag (e.g. zh-CN, en, ja)", raw)
	}
	req.DefaultLanguage = &tag
	return nil
}

// checkJSONPayload reports the first JSON text field that is not valid JSON.
// config and endpoints travel as strings; xsql.JSONB refuses to write anything
// that is not valid JSON, so the check keeps the failure at the request layer
// instead of letting it surface as a database error.
func (req *agentRequest) checkJSONPayload() (string, bool) {
	for _, f := range []struct {
		name string
		val  string
	}{
		{"config", req.Config},
		{"endpoints", req.Endpoints},
	} {
		if f.val != "" && !json.Valid([]byte(f.val)) {
			return f.name, false
		}
	}
	return "", true
}

// CreateAgent creates an agent.
func CreateAgent(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	info := authInfo.(*auth.AuthInfo)

	// config / endpoints are JSON text: reject malformed values here, before any
	// storage is deleted or written.
	if field, ok := req.checkJSONPayload(); !ok {
		xhttp.ErrCode(c, "common.invalid_params", gin.H{"field": field}, field+" must be valid JSON", "")
		return
	}
	if lerr := req.normalizeDefaultLanguage(); lerr != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, lerr.Error(), "")
		return
	}

	// Validate first: name shape and no clash with another agent or a flow, before anything is written.
	req.Name = strings.TrimSpace(req.Name)
	if !tools.ValidName(req.Name) {
		xhttp.ErrCode(c, "agent.invalid_name", nil, "invalid agent name (letters, digits and hyphens only, max 64)", "")
		return
	}
	if err := validateAgentNameUnique(info, req.Name, ""); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Recreate under the same name: drop the version directory the previous entity left behind (no row in the DB does not mean nothing on disk).
	if st, serr := getStorage(); serr == nil {
		_ = st.DeletePrefix(context.Background(),
			paths.VersionEntityKey(info.Schema, paths.VersionAgents, req.Name))
	}

	// Only two states are accepted; absent means active (the UI always sends 1, this covers direct API calls).
	if req.Status == 0 {
		req.Status = models.StatusNormal
	}
	if req.Status != models.StatusNormal && req.Status != models.StatusDeprecated {
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1 (active) or 3 (deprecated)", "")
		return
	}

	agent := req.toModel()

	// Quota check: the per-tenant agent cap.
	schema := info.Schema
	cnt, err := quota.CountAgents(schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if qerr := models.CheckQuota("agent", cnt, quota.Resolve(schema).MaxAgents); qerr != nil {
		// A full quota is not an internal error: report it as quota.agent_limit_reached so the UI can word it.
		le := qerr.(*models.LimitError)
		xhttp.ErrCode(c, "quota.agent_limit_reached", le.Params(), qerr.Error(), "")
		return
	}

	// Insert (created by a human, so there is no creator agent and no automatic grant).
	id, err := repo.CreateAgent(info, &agent, "")
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Explicit grants (optional): out-edges (who it calls) + in-edges (who calls it).
	if len(req.CanCallIDs) > 0 || len(req.CalledByIDs) > 0 {
		// Out- and in-edges are treated alike and may include the supervisor (it can be called; this grant alone decides whether it may call).
		canCall := filterValidAgentIDs(info, id, req.CanCallIDs)
		calledBy := filterValidAgentIDs(info, id, req.CalledByIDs)
		if err := relRepo.ReplaceByCaller(info.Schema, id, canCall, info.ID); err != nil {
			xhttp.ErrCode(c, "agent.relations_failed", gin.H{"id": id}, "agent created but relations failed: "+err.Error(), "")
			return
		}
		if err := relRepo.ReplaceByCallee(info.Schema, id, calledBy, info.ID); err != nil {
			xhttp.ErrCode(c, "agent.relations_failed", gin.H{"id": id}, "agent created but relations failed: "+err.Error(), "")
			return
		}
	}

	// Make sure the workspace has an entry file (an agent is a folder, so editing can start right away).
	// The entry file name comes from shared/paths.AgentEntryFile (single source of truth): it matches the
	// convention of loadSystemPrompt / create_agent / read_agent at run time, and is deliberately not
	// configurable (a setting that silently does nothing is worse than none).
	entryFile := paths.AgentEntryFile
	if st, serr := getStorage(); serr == nil {
		key := path.Join(tools.AgentsKey(info.Schema, agent.Name), entryFile)
		if ex, _ := st.Exists(context.Background(), key); !ex {
			_ = st.WriteFile(context.Background(), key,
				[]byte(skeleton.AgentMD(skeleton.Normalize(req.Lang), agent.Name, req.Description)))
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// filterValidAgentIDs drops blanks, the agent itself and unknown ids, and returns the de-duplicated list.
//
// The supervisor is no longer excluded: it can be called, and this grant alone decides whether it may
// call (same rule as caller.go - a grant row allows it, identity is irrelevant).
func filterValidAgentIDs(info *auth.AuthInfo, selfID string, ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || id == selfID || seen[id] {
			continue
		}
		inst, err := repo.GetAgent(info, id)
		if err != nil || inst == nil {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// GetAgentRelations returns the call grants (out: who it may call; in: who may call it).
func GetAgentRelations(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	callable, err := relRepo.ListByCaller(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	callers, err := relRepo.ListByCallee(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"can_call":  callable, // who this agent may call
		"called_by": callers,  // who may call this agent
	}, "")
}

// SetAgentRelations replaces both directions of the call grants.
// body: {"can_call_ids":["uuid",...],"called_by_ids":["uuid",...]}; an empty array clears that direction.
func SetAgentRelations(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		CanCallIDs  []string `json:"can_call_ids"`
		CalledByIDs []string `json:"called_by_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}
	// Only an active agent may change its definition (exposing an endpoint never freezes anything).
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot be modified (switch it back to Active first)", "")
		return
	}

	// Out- and in-edges are treated alike and may include the supervisor (it can be called; this grant alone decides whether it may call).
	canCall := filterValidAgentIDs(info, id, body.CanCallIDs)
	calledBy := filterValidAgentIDs(info, id, body.CalledByIDs)

	if err := relRepo.ReplaceByCaller(info.Schema, id, canCall, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if err := relRepo.ReplaceByCallee(info.Schema, id, calledBy, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"can_call": len(canCall), "called_by": len(calledBy)}, "")
}

// GetAgentSkillBindings reads the skills bound to this agent (the agent side of agent_skill_bindings).
// Returns {"skill_ids":[...]}; tenant-wide skills may be bound in advance, so switching a skill to a whitelist keeps the grant.
func GetAgentSkillBindings(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	skillRepo := skills.NewRepository()
	ids, err := skillRepo.ListSkillIDsByAgent(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	discovery, derr := skillRepo.GetAgentSkillDiscovery(info.Schema, id)
	if derr != nil {
		xhttp.ErrCode(c, "common.internal", nil, derr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"skill_ids": ids, "discovery": discovery}, "")
}

// SetAgentSkillBindings replaces the skill bindings of this agent.
// body: {"skill_ids":["uuid",...]}; an empty array clears them (the agent then only sees tenant-wide skills).
func SetAgentSkillBindings(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		SkillIDs []string `json:"skill_ids"`
		// An omitted Discovery (nil) keeps the stored value; only an explicit value changes the "may discover unbound skills" switch.
		Discovery *bool `json:"discovery"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	inst, err := repo.GetAgent(info, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}
	// Only an active agent may change its definition (same rule as the call grants).
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot be modified (switch it back to Active first)", "")
		return
	}

	skillRepo := skills.NewRepository()
	if err := skillRepo.ReplaceByAgent(info.Schema, id, body.SkillIDs, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if body.Discovery != nil {
		if err := skillRepo.SetAgentSkillDiscovery(info.Schema, id, *body.Discovery); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"count": len(body.SkillIDs)}, "")
}

// UpdateAgent updates an agent.
func UpdateAgent(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	id := c.Param("id")

	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)

	// Load the old instance first: its name drives the directory migration on rename.
	old, err := repo.GetAgent(ai, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// Only an active agent may change its definition (renames and directory moves included).
	if old != nil && !old.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "this agent is deprecated, cannot be modified (switch it back to Active first)", "")
		return
	}

	// Rename: run every check first, then write the DB / move the directory (no partial success).
	if old != nil && req.Name != old.Name {
		req.Name = strings.TrimSpace(req.Name)
		// A protected built-in agent (main) cannot be renamed.
		if models.IsProtectedAgent(old.Name) {
			xhttp.ErrCode(c, "agent.protected", gin.H{"name": old.Name}, "protected agent "+old.Name+" cannot be renamed", "")
			return
		}
		if !tools.ValidName(req.Name) {
			xhttp.ErrCode(c, "agent.invalid_name", nil, "invalid agent name (letters, digits and hyphens only, max 64)", "")
			return
		}
		if err := validateAgentNameUnique(ai, req.Name, id); err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
	}

	// config / endpoints are string fields in the request; an empty string means "not provided this time".
	// UpdateAgent is a full-column UPDATE, so passing an empty string writes NULL (config is NOT NULL)
	// and fails; fall back to the stored value, or to a safe default ({} / []) when there is none.
	if req.Config == "" {
		req.Config = "{}"
		if old != nil && !old.Config.IsNil() {
			if b, jerr := json.Marshal(old.Config); jerr == nil {
				req.Config = string(b)
			}
		}
	}
	if req.Endpoints == "" {
		req.Endpoints = "[]"
		if old != nil && !old.Endpoints.IsNil() {
			if b, jerr := json.Marshal(old.Endpoints); jerr == nil {
				req.Endpoints = string(b)
			}
		}
	}

	// Same check as CreateAgent; the fallback above already yields valid JSON,
	// so this only rejects what the client actually sent.
	if field, ok := req.checkJSONPayload(); !ok {
		xhttp.ErrCode(c, "common.invalid_params", gin.H{"field": field}, field+" must be valid JSON", "")
		return
	}
	if lerr := req.normalizeDefaultLanguage(); lerr != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, lerr.Error(), "")
		return
	}

	// Only two states are accepted; absent keeps the stored one (a full-column UPDATE must not zero the status).
	if req.Status == 0 && old != nil {
		req.Status = int(old.Status)
	}
	if req.Status != models.StatusNormal && req.Status != models.StatusDeprecated {
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1 (active) or 3 (deprecated)", "")
		return
	}

	agent := req.toModel()
	// The two agent-level switches keep the stored value when the request omits them, so a full-column UPDATE does not silently clear them.
	// The dedicated endpoints still write them directly (skill visibility via SetAgentSkillBindings, the tool surface via toolgrants.SetUnrestricted).
	if old != nil {
		if req.ToolsUnrestricted == nil {
			agent.ToolsUnrestricted = old.ToolsUnrestricted
		}
		if req.SkillDiscovery == nil {
			agent.SkillDiscovery = old.SkillDiscovery
		}
		// The same for the default reply language: a full-column UPDATE would otherwise clear a value the request
		// never mentioned (one rename would wipe it).
		if req.DefaultLanguage == nil {
			agent.DefaultLanguage = old.DefaultLanguage
		}
	}

	// Rename: **move the disk first, then the DB** (disk holds content, the DB is the index; see the caller contract of paths.MoveEntityStorage).
	// Version directories are keyed by name: changing the DB before the disk would make a production call assemble an empty version directory under the new name.
	renamed := old != nil && old.Name != "" && agent.Name != "" && old.Name != agent.Name
	if renamed {
		st, serr := getStorage()
		if serr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "storage not ready, cannot migrate the definition/version directories: "+serr.Error(), "")
			return
		}
		if rerr := paths.MoveEntityStorage(st, ai.Schema, paths.VersionAgents, old.Name, agent.Name); rerr != nil {
			xhttp.ErrCode(c, "agent.rename_failed", nil, "failed to migrate the definition/version directories, so the rename did not take effect: "+rerr.Error(), "")
			return
		}
	}

	// Update the row.
	err = repo.UpdateAgent(ai, id, &agent)
	if err != nil {
		if renamed {
			// The DB write failed, so move the disk back: never leave new names on disk and old ones in the DB.
			if st, serr := getStorage(); serr == nil {
				if rerr := paths.MoveEntityStorage(st, ai.Schema, paths.VersionAgents, agent.Name, old.Name); rerr != nil {
					xlog.Error("failed to roll back the definition/version directories schema=%s %s->%s: %v", ai.Schema, agent.Name, old.Name, rerr)
				}
			}
		}
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// The super-power flag is a single-column update: UpdateAgent deliberately does not touch is_supervisor
	// (a full-column overwrite would silently write the false the request never sent, so one description edit would clear the privilege).
	// Demoting the last supervisor of the tenant is rejected by the repository; that error is passed through as is.
	if req.IsSupervisor != nil && (old == nil || old.IsSupervisor != *req.IsSupervisor) {
		if serr := repo.SetSupervisor(ai, id, *req.IsSupervisor); serr != nil {
			xhttp.ErrCode(c, "common.internal", nil, serr.Error(), "")
			return
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id}, "")
}

// DeleteAgent deletes an agent.
func DeleteAgent(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	// Load the instance first: its name drives the directory cleanup.
	inst, err := repo.GetAgent(ai, id)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// A protected built-in agent cannot be deleted.
	if inst != nil && models.IsProtectedAgent(inst.Name) {
		xhttp.ErrCode(c, "agent.protected", gin.H{"name": inst.Name}, "protected agent "+inst.Name+" cannot be deleted", "")
		return
	}
	// A deprecated agent is deletable: deprecating is the explicit "take it out of service" step (it retracts the
	// endpoints), so the reverse rule - "switch it back to Active, then delete" - would put it back online just to
	// remove it. What still blocks a delete is a released version (handled by the repository): unrelease first.

	// Delete the rows: the private skill records, the grant links, the protocol endpoints and the agents row are handled
	// in one repository transaction (skills.agent_id and svc_endpoints.target_id have no foreign key, so DB-level
	// cascading cannot be relied on).
	if err := repo.DeleteAgent(ai, id); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		// A released version is what production runs: the delete is refused and the answer names the version, so the UI
		// can say "v3 is still live, unrelease it first" instead of a generic failure.
		var released *versions.ErrHasReleasedVersion
		if errors.As(err, &released) {
			xhttp.ErrCode(c, "agent.has_release", gin.H{"version": released.Version}, released.Error(), "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Clean the definition directory and the version domain (through storage, local / s3 alike).
	// A DB cascade cannot delete disk: a version directory left behind under that name would be inherited by a recreate.
	if inst != nil && inst.Name != "" {
		if st, serr := getStorage(); serr == nil {
			if rerr := paths.DeleteEntityStorage(st, ai.Schema, paths.VersionAgents, inst.Name); rerr != nil {
				xlog.Error("failed to clean the definition/version directories schema=%s name=%s: %v", ai.Schema, inst.Name, rerr)
			}
		}
	}

	// The agent's sessions go with it: once the owner is gone the console can no longer list (or delete) them, yet they
	// keep counting against the user's session quota and keep their workspaces on disk - a user at the cap would be locked
	// out with no way to clean up. Best-effort: the row is already gone, so a failure is logged rather than rolled back.
	if n, derr := sessions.NewRepository().DeleteByOwner(ai.Schema, models.SessionOwnerTypeAgent, id); derr != nil {
		xlog.Error("failed to delete the sessions of the deleted agent schema=%s agent=%s: %v", ai.Schema, id, derr)
	} else if n > 0 {
		xlog.Info("deleted %d session(s) together with agent schema=%s agent=%s", n, ai.Schema, id)
	}

	xhttp.OkJSON(c, "Agent instance deleted successfully", id, "")
}

// SetAgentStatus switches the agent state (1=active | 3=deprecated, see models.StatusXxx).
//
// Switching to deprecated retracts every endpoint of the agent (exposed=false, the records stay):
// a deprecated agent must not serve outside. Switching back to active leaves the endpoints alone (exposure is independent of the definition state).
func SetAgentStatus(c *gin.Context) {
	// Tenant from the request context.
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}

	ai := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	switch body.Status {
	case models.StatusNormal, models.StatusDeprecated:
	default:
		xhttp.ErrCode(c, "agent.invalid_status", nil, "status must be 1 (active) or 3 (deprecated)", "")
		return
	}

	if err := repo.UpdateStatus(ai, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Only switching to deprecated retracts the endpoints: a deprecated agent must not serve outside.
	// Switching to active does not retract: exposure and editability are separate, and restoring edits must not take production down.
	// The state is already changed, so a failed retract is logged instead of rolled back (the next switch or a manual retract recovers).
	if body.Status == models.StatusDeprecated {
		if uerr := svcendpoints.NewRepository().RetractByTarget(ai, "agent", id); uerr != nil {
			xlog.Error("failed to retract the agent endpoints id=%s: %v", id, uerr)
		}
	}

	xhttp.OkJSON(c, "Success", gin.H{"id": id, "status": body.Status}, "")
}
