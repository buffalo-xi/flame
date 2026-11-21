// Package skills provides the skill management API (CRUD + status changes).
// Metadata lives in the skills table and content in the agent storage backend (the driver comes from config.yaml).
package skills

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"sync"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/skeleton"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

var (
	skillRepo = skills.NewRepository()
	agentRepo = agents.NewRepository()

	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// ensureAgentModifiable requires the owner agent to exist and be in the normal state;
// an empty agentID means tenant level and needs no check. Create, update, delete, visibility changes and file writes all
// go through here (the test is models.IsModifiable, matching skillManager.checkOwnerAgent,
// checkWrite / checkWriteOwn in tools/gate.go - miss one and the rules diverge).
// A deprecated (3) agent is never writable.
func ensureAgentModifiable(info *auth.AuthInfo, agentID string) error {
	if agentID == "" {
		return nil
	}
	oa, err := agentRepo.GetAgent(info, agentID)
	if err != nil {
		return err
	}
	if oa == nil {
		return fmt.Errorf("skill owner agent %s not found", agentID)
	}
	if !oa.IsModifiable() {
		return fmt.Errorf("owner agent %q is deprecated; its private skills cannot be changed", oa.Name)
	}
	return nil
}

// ensureOwnerAgentModifiable requires the owner agent of an existing skill to be writable (see ensureAgentModifiable).
func ensureOwnerAgentModifiable(info *auth.AuthInfo, s *models.Skill) error {
	if s == nil {
		return nil
	}
	return ensureAgentModifiable(info, string(s.AgentID))
}

// getStorage lazily builds the agent storage instance (the same configuration as service/agent.InitStorage).
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Storage)
	})
	return store, storeErr
}

// contentPath returns the skill content file path (tenant level or agent private).
func contentPath(schema, agentName, name string) string {
	if agentName == "" {
		return tools.SkillPath(schema, name)
	}
	return tools.AgentSkillPath(schema, agentName, name)
}

// resolveAgentName looks up an agent name from agent_id; an empty agent_id returns an empty string (tenant level).
func resolveAgentName(schema, agentID string) (string, error) {
	if agentID == "" {
		return "", nil
	}
	inst, err := agentRepo.GetAgent(&auth.AuthInfo{Schema: schema}, agentID)
	if err != nil {
		return "", err
	}
	return inst.Name, nil
}

// skillToMap turns a Skill into a response map (content is supplied by the caller).
func skillToMap(s *models.Skill, content string) gin.H {
	return gin.H{
		"id":               s.ID,
		"name":             s.Name,
		"description":      s.Description,
		"metadata":         s.Metadata,
		"user_invocable":   s.UserInvocable,
		"command_dispatch": s.CommandDispatch,
		"command_tool":     s.CommandTool,
		"status":           s.Status,
		"restricted":       s.Restricted,
		"agent_id":         s.AgentID,
		"updated_by_name":  s.UpdatedByName,
		"agent_name":       s.AgentName,
		"protected":        models.IsProtectedSkill(s.Name),
		"created_at":       s.CreatedAt,
		"updated_at":       s.UpdatedAt,
		"content":          content,
	}
}

// ListSkills lists skills (paged and filtered).
func ListSkills(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req struct {
		Page    int    `json:"page"`
		Limit   int    `json:"limit"`
		AgentID string `json:"agent_id"`
		Status  *int   `json:"status"`
		Keyword string `json:"keyword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request parameters", "")
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	list, total, err := skillRepo.List(info.Schema, &skills.ListRequest{
		Page:    req.Page,
		Limit:   req.Limit,
		AgentID: req.AgentID,
		Status:  req.Status,
		Keyword: req.Keyword,
	})
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Fetch the grant counts in one query, so the list can show "only N agents / nobody can see it"
	ids := make([]string, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	counts, err := skillRepo.ListBindingCounts(info.Schema, ids)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Current versions: one batch query on two tables - a tenant-level skill uses its own version, a private skill has none,
	// its body being frozen into the owner agent's current version, so the agent's number is used (version_source says which).
	skillVers, _ := versioning.CurrentVersions(info.Schema, "skill_versions")
	agentVers, _ := versioning.CurrentVersions(info.Schema, "agent_versions")

	items := make([]gin.H, 0, len(list))
	for i := range list {
		m := skillToMap(&list[i], "")
		m["bound_count"] = counts[list[i].ID]
		// 0 = no released version yet (the card shows "not released"; a private skill follows its agent, so an unreleased agent means 0)
		if string(list[i].AgentID) != "" {
			m["current_version"] = agentVers[string(list[i].AgentID)]
			m["version_source"] = "agent"
		} else {
			m["current_version"] = skillVers[list[i].ID]
			m["version_source"] = "skill"
		}
		items = append(items, m)
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items, "total": total, "page": req.Page, "limit": req.Limit}, "")
}

// GetSkill returns one skill (content included).
func GetSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	agentName, err := resolveAgentName(info.Schema, string(s.AgentID))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	content := ""
	if st, serr := getStorage(); serr == nil {
		if data, rerr := st.ReadFile(context.Background(), contentPath(info.Schema, agentName, s.Name)); rerr == nil {
			content = string(data)
		}
	}
	body := skillToMap(s, content)
	// Version state: the editor header must distinguish "not released / released with changes / matches production" (same as flows).
	// A private skill is always 0 - its version follows its agent, and the UI shows "follows the agent version".
	body["current_version"], body["dirty"] = 0, false
	if st, serr := getStorage(); serr == nil {
		v, dirty := versioning.SkillVersionState(context.Background(), st, info.Schema, s)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// skillRequest is the create / update body.
type skillRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	AgentID         string `json:"agent_id"` // empty = tenant level
	Content         string `json:"content"`
	Restricted      *bool  `json:"restricted"` // nil = default true (enterprise default deny: no grant, no visibility)
	UserInvocable   bool   `json:"user_invocable"`
	CommandDispatch string `json:"command_dispatch"`
	CommandTool     string `json:"command_tool"`
	// Lang is the language used to generate the SKILL.md skeleton when the body is empty (never stored).
	// Absent / unrecognised -> an English skeleton (see shared/skeleton.Normalize).
	Lang string `json:"lang"`
}

// normalizeCommandInput folds the user-command definition into one of its two valid shapes before it is stored.
//
// The dispatch mode is a closed set ("" or "tool"), and the pair is repaired as a pair:
//   - a target tool without a dispatch mode is not a configuration - the field is hidden in the UI at that point, so a stale
//     value there is invisible to the user and must not survive the save;
//   - "tool" with an empty target is an incomplete draft the editor passes through while the user is still filling the form.
//     It is kept rather than rejected: rejecting it would fail the very click that selects "tool", and the run side simply
//     does not take over until the target is set (see agentImpl.dispatchCommand).
//
// Unknown dispatch values are refused: nothing consumes them, and letting them through would store a setting that looks
// configured and behaves as "off".
func normalizeCommandInput(dispatch, tool string) (string, string, error) {
	dispatch = strings.TrimSpace(dispatch)
	tool = strings.TrimSpace(tool)
	switch dispatch {
	case "":
		return "", "", nil
	case "tool":
		return "tool", tool, nil
	default:
		return "", "", fmt.Errorf("unknown command_dispatch %q (only an empty value and \"tool\" are supported)", dispatch)
	}
}

// CreateSkill creates a skill.
func CreateSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	var req skillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !tools.ValidName(req.Name) {
		xhttp.ErrCode(c, "skill.invalid_name", nil, "Invalid skill name (letters, digits and hyphens only, at most 64 characters)", "")
		return
	}
	// Duplicate check within the owner scope (any status)
	exist, err := skillRepo.FindByName(info.Schema, req.Name, req.AgentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if exist != nil {
		xhttp.ErrCode(c, "skill.name_taken", nil, "skill already exists", "")
		return
	}

	// Recreating the same name: clear the version directory the previous entity left (no row in the DB does not mean no directory on disk).
	// A private skill has no version directory of its own, so this step is unnecessary.
	if req.AgentID == "" {
		removeSkillVersions(info.Schema, req.Name)
	}

	// A non-modifiable owner agent gets no new private skill (same rule as Update / Delete and the meta tools)
	if verr := ensureAgentModifiable(info, req.AgentID); verr != nil {
		xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
		return
	}
	agentName, err := resolveAgentName(info.Schema, req.AgentID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Quota check: the per-tenant skill cap (must run before writing content, or an orphan file is left behind)
	cnt, err := quota.CountSkills(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if qerr := models.CheckQuota("skill", cnt, quota.Resolve(info.Schema).MaxSkills); qerr != nil {
		// A full quota is not an internal error: report it as quota.skill_limit_reached so the UI can word it.
		le := qerr.(*models.LimitError)
		xhttp.ErrCode(c, "quota.skill_limit_reached", le.Params(), qerr.Error(), "")
		return
	}

	st, err := getStorage()
	if err != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+err.Error(), "")
		return
	}
	// Write the entry file first, then the row; a failed insert removes the file.
	// An empty body gets the default skeleton (the same as AGENT.md when an agent is created), so a new skill is editable at once.
	entry := req.Content
	if strings.TrimSpace(entry) == "" {
		entry = skeleton.SkillMD(skeleton.Normalize(req.Lang), req.Name, req.Description)
	}
	if err := st.WriteFile(context.Background(), contentPath(info.Schema, agentName, req.Name), []byte(entry)); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	commandDispatch, commandTool, verr := normalizeCommandInput(req.CommandDispatch, req.CommandTool)
	if verr != nil {
		xhttp.ErrCode(c, "skill.invalid_command", nil, verr.Error(), "")
		return
	}

	// Visibility defaults to true (enterprise default deny: no explicit grant, no visibility); only tenant-level skills are affected
	restricted := true
	if req.Restricted != nil {
		restricted = *req.Restricted
	}

	id, err := skillRepo.Create(info.Schema, &models.Skill{
		Name:            req.Name,
		Description:     xsql.String(req.Description),
		AgentID:         xsql.UUID(req.AgentID),
		Restricted:      xsql.Bool(restricted),
		CreatedBy:       xsql.UUID(info.ID),
		UpdatedBy:       xsql.UUID(info.ID),
		UserInvocable:   xsql.Bool(req.UserInvocable),
		CommandDispatch: xsql.String(commandDispatch),
		CommandTool:     xsql.String(commandTool),
	})
	if err != nil {
		_ = st.Delete(context.Background(), contentPath(info.Schema, agentName, req.Name))
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	// bound_agents is returned explicitly (an empty array, never a missing key), so the console can tell "nobody is bound
	// yet" apart from "this response says nothing about bindings". Unlike the meta-tool create path, this handler writes
	// no binding at all, so a fresh tenant-level skill really is unbound.
	bound := []string{}
	if bindings, berr := skillRepo.ListBindings(info.Schema, id); berr == nil {
		for i := range bindings {
			bound = append(bound, string(bindings[i].AgentName))
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"id": id, "bound_agents": bound}, "")
}

// UpdateSkill updates a skill (name / description / content; a rename migrates the content files).
func UpdateSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var req skillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	// A non-modifiable owner agent freezes its private skills entirely (same rule as skills.go / tools/gate.go)
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
		return
	}
	// Only the normal state allows redefining, as with agents / flows
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot be modified (switch it back to normal first)", "")
		return
	}

	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		newName = s.Name
	}
	if newName != s.Name {
		// A built-in protected skill cannot be renamed (other fields such as the description can change)
		if models.IsProtectedSkill(s.Name) {
			xhttp.ErrCode(c, "skill.protected", gin.H{"name": s.Name}, "protected skill "+s.Name+" cannot be renamed", "")
			return
		}
		if !tools.ValidName(newName) {
			xhttp.ErrCode(c, "skill.invalid_name", nil, "Invalid skill name (letters, digits and hyphens only, at most 64 characters)", "")
			return
		}
		exist, err := skillRepo.FindByName(info.Schema, newName, string(s.AgentID))
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
			return
		}
		if exist != nil && exist.ID != s.ID {
			xhttp.ErrCode(c, "skill.name_taken", nil, "skill already exists", "")
			return
		}
	}

	agentName, err := resolveAgentName(info.Schema, string(s.AgentID))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	oldPath := contentPath(info.Schema, agentName, s.Name)

	commandDispatch, commandTool, verr := normalizeCommandInput(req.CommandDispatch, req.CommandTool)
	if verr != nil {
		xhttp.ErrCode(c, "skill.invalid_command", nil, verr.Error(), "")
		return
	}

	oldName := s.Name
	s.Name = newName
	if req.Description != "" {
		s.Description = xsql.String(req.Description)
	}
	// Command fields are overwritten wholesale (an empty value clears them, which is how command dispatch is cancelled)
	s.UserInvocable = xsql.Bool(req.UserInvocable)
	s.CommandDispatch = xsql.String(commandDispatch)
	s.CommandTool = xsql.String(commandTool)
	s.UpdatedBy = xsql.UUID(info.ID)

	if err := skillRepo.Update(info.Schema, s); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	newPath := contentPath(info.Schema, agentName, newName)
	// Metadata only, no content, no rename -> the file is left alone
	if req.Content == "" && oldPath == newPath {
		xhttp.OkJSON(c, "Success", nil, "")
		return
	}

	st, err := getStorage()
	if err != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+err.Error(), "")
		return
	}
	content := []byte(req.Content)
	// Rename: the **whole skill directory** moves together.
	//
	// A skill directory may hold extra files and subdirectories (see the file API in this package's files.go), and moving only SKILL.md
	// would leave those resources orphaned in the old directory. Same rule as Update in service/agent/skills.go.
	// When the move fails (target exists / crosses devices) it falls back to "read old + write new + delete old", at least keeping the entry file.
	if oldPath != newPath {
		if rerr := st.Rename(context.Background(), path.Dir(oldPath), path.Dir(newPath)); rerr != nil {
			if data, rerr2 := st.ReadFile(context.Background(), oldPath); rerr2 == nil {
				if werr := st.WriteFile(context.Background(), newPath, data); werr != nil {
					xhttp.ErrCode(c, "common.internal", nil, werr.Error(), "")
					return
				}
			}
			_ = st.Delete(context.Background(), oldPath)
		}
		// Tenant-level skills: the version directory is keyed by name and must move with the draft,
		// or the history stays under the old name and a path built from the current name finds nothing.
		// A private skill has no version directory of its own (it follows its agent), so this does not apply.
		if string(s.AgentID) == "" {
			verOld := paths.VersionEntityKey(info.Schema, paths.VersionSkills, oldName)
			verNew := paths.VersionEntityKey(info.Schema, paths.VersionSkills, newName)
			if rerr := st.Rename(context.Background(), verOld, verNew); rerr != nil {
				xhttp.ErrCode(c, "skill.rename_failed", nil, "the skill was renamed but the version directory could not be moved (retry or move it by hand): "+rerr.Error(), "")
				return
			}
		}
	}

	if len(content) == 0 {
		if oldPath != newPath {
			xhttp.OkJSON(c, "Success", nil, "") // the files moved with the directory
			return
		}
		if data, rerr := st.ReadFile(context.Background(), oldPath); rerr == nil {
			content = data
		}
	}
	if err := st.WriteFile(context.Background(), newPath, content); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}

// DeleteSkill deletes a skill (row + content files).
func DeleteSkill(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	// A non-modifiable owner agent freezes its private skills entirely (same rule as skills.go / tools/gate.go)
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
		return
	}
	// A deprecated skill cannot be deleted (same rule as agents / flows)
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot be deleted (switch it back to normal first)", "")
		return
	}

	// A built-in protected skill cannot be deleted
	if models.IsProtectedSkill(s.Name) {
		xhttp.ErrCode(c, "skill.protected", gin.H{"name": s.Name}, "protected skill "+s.Name+" cannot be deleted", "")
		return
	}

	if err := skillRepo.Delete(info.Schema, id); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	// Best effort: remove the whole content directory (SKILL.md and any other files); skipped when the owner agent is gone
	if agentName, rerr := resolveAgentName(info.Schema, string(s.AgentID)); rerr == nil {
		if st, serr := getStorage(); serr == nil {
			_ = st.DeletePrefix(context.Background(), skillDirKey(info.Schema, agentName, s.Name))
		}
	}
	// Tenant-level skills: clear the version area with them. It is keyed by name, and keeping it lets a recreate inherit the old history.
	// A private skill has no version directory of its own (its content lives in the agent's), so nothing to do here.
	if string(s.AgentID) == "" {
		removeSkillVersions(info.Schema, s.Name)
	}
	xhttp.OkJSON(c, "Skill deleted successfully", id, "")
}

// SetSkillStatus switches a skill's status (1 = normal | 3 = deprecated, the same two states as agents / flows).
// A skill has no service endpoint, so there is no delist cascade: once deprecated, an agent no longer sees or injects it.
func SetSkillStatus(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
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
		xhttp.ErrCode(c, "skill.invalid_status", nil, "status must be 1 (normal) or 3 (deprecated)", "")
		return
	}

	// A non-normal owner agent also blocks status changes on its private skills (same rule as the other admin entry points).
	// The skill's own status is **not** checked on purpose: that is the field being changed, and checking it would make deprecation irreversible (deadlock).
	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
		return
	}

	if err := skillRepo.UpdateStatus(info.Schema, id, body.Status); err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", nil, "")
}

// GetSkillVisibility returns a skill's visibility configuration (the restricted switch + granted agents).
// It only means something for tenant-level skills; a private skill's visibility follows its owner.
func GetSkillVisibility(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}

	list, err := skillRepo.ListBindings(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"items":      list,
		"restricted": s.Restricted, // straight from skills.restricted
		"agent_id":   s.AgentID,    // non-empty = a private skill
	}, "")
}

// SetSkillVisibility sets visibility: the restricted switch + the granted agent allowlist (replaced wholesale in a transaction).
// body: {"restricted": true, "agent_ids": ["uuid-A","uuid-B"]}
// With restricted=false the bindings are ignored (the rows stay, so the switch can be flipped back).
func SetSkillVisibility(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)
	id := c.Param("id")

	var body struct {
		Restricted bool     `json:"restricted"`
		AgentIDs   []string `json:"agent_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}

	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return
	}
	// A non-modifiable owner agent freezes its private skills entirely (same rule as skills.go / tools/gate.go)
	if verr := ensureOwnerAgentModifiable(info, s); verr != nil {
		xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
		return
	}
	// Visibility is part of the definition: it cannot change while the skill is deprecated
	if !s.IsModifiable() {
		xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot be modified (switch it back to normal first)", "")
		return
	}
	// A private skill's visibility follows its owner and cannot be narrowed here
	if s.AgentID != "" {
		xhttp.ErrCode(c, "skill.private_no_visibility", nil, "private skill does not support visibility config", "")
		return
	}

	// Deduplicate and check that each agent exists
	seen := map[string]bool{}
	agentIDs := make([]string, 0, len(body.AgentIDs))
	for _, aid := range body.AgentIDs {
		if aid == "" || seen[aid] {
			continue
		}
		if _, aerr := agentRepo.GetAgent(&auth.AuthInfo{Schema: info.Schema}, aid); aerr != nil {
			xhttp.ErrCode(c, "agent.not_found", nil, "agent not found: "+aid, "")
			return
		}
		seen[aid] = true
		agentIDs = append(agentIDs, aid)
	}

	if err := skillRepo.SetVisibility(info.Schema, id, body.Restricted, agentIDs, info.ID); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"restricted": body.Restricted, "count": len(agentIDs)}, "")
}
