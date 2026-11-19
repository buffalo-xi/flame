package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/versioning"
	"flame/tools/xsql"
)

// ===== Metadata of production runs (P1.5) =====
//
// Freezing the body but not the metadata would let "edit one description, no release needed" change the catalog the model sees; freezing
// what is displayed but not what takes effect would let "add one dependency to a skill in the draft" still light it up in production.
//
// This reader is the single implementation: skillManager (the resident catalog / read_skill / use_skill) and toolGrantManager (the
// automatic kind of grant_agent_tool, the via_skill hint of read_agent) both take their data from it. One source keeps "the admin side
// says it lights up after use_skill, the runtime says it never lights up" from happening.
// Draft mode does nothing - the editor has to see the copy being edited.

// skillMetaReader reads the skill metadata snapshot of the **current version** (private skills are cached per owning agent).
type skillMetaReader struct {
	schema    string
	agentID   string // current agent uuid (tells "is this my private skill", saving a reverse lookup)
	agentName string // current agent name
	agents    agents.Repository
	mode      tools.DefinitionMode // current = production run; zero value = draft

	// mu / private cache "the metadata snapshot of private skills per owning agent" in production runs.
	// Nobody releases a new version during one run, so one cache is enough; nil means that owner has no usable snapshot.
	mu      sync.Mutex
	private map[string]map[string]versioning.SkillMeta
}

// newSkillMetaReader creates the reader (schema / identity / mode are frozen at construction time).
func newSkillMetaReader(schema, agentID, agentName string, repo agents.Repository, mode tools.DefinitionMode) *skillMetaReader {
	return &skillMetaReader{
		schema:    schema,
		agentID:   agentID,
		agentName: agentName,
		agents:    repo,
		mode:      mode,
	}
}

// versionMeta takes a skill's metadata snapshot in the current version (ownerName is the owning agent's name, empty = tenant level).
// It returns false in draft mode or when the skill has no current version - the caller keeps the value from the database.
func (r *skillMetaReader) versionMeta(s *models.Skill, ownerName string) (versioning.SkillMeta, bool) {
	if r == nil || !r.mode.IsCurrent() {
		return versioning.SkillMeta{}, false
	}
	if ownerName == "" {
		got, err := versioning.TenantSkillMeta(r.schema, []string{s.ID})
		if err != nil {
			return versioning.SkillMeta{}, false
		}
		meta, ok := got[s.ID]
		return meta, ok
	}
	return r.privateMetaFor(ownerName, s.Name)
}

// ownerMeta resolves ownerName from the skill row's own owner and then takes the snapshot.
// A failed cross-owner lookup (the owning agent no longer exists, say) counts as "no version information" and the caller falls back.
func (r *skillMetaReader) ownerMeta(s *models.Skill) (versioning.SkillMeta, bool) {
	// Check the mode before resolving the owner: draft mode must not touch the database at all (a hot path in the editor).
	if r == nil || !r.mode.IsCurrent() {
		return versioning.SkillMeta{}, false
	}
	ownerName, err := ownerAgentName(r.schema, r.agentID, r.agentName, string(s.AgentID), r.agents)
	if err != nil {
		return versioning.SkillMeta{}, false
	}
	return r.versionMeta(s, ownerName)
}

// depsOf returns the tool dependencies a skill declares in the current version.
//
//	ok=false -- no version information (draft mode / the skill is not released yet) -> the caller falls back to the live table;
//	ok=true  -- authoritative: even an empty list means this version really declares no dependency, so the database must not be queried again.
func (r *skillMetaReader) depsOf(s models.Skill) ([]string, bool) {
	if r == nil || !r.mode.IsCurrent() {
		return nil, false
	}
	meta, ok := r.ownerMeta(&s)
	if !ok {
		return nil, false
	}
	return meta.ToolRefs(), true
}

// privateMetaFor takes a private skill's metadata in the owning agent's current version (cached per owner).
func (r *skillMetaReader) privateMetaFor(ownerName, skillName string) (versioning.SkillMeta, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.private == nil {
		r.private = make(map[string]map[string]versioning.SkillMeta, 2)
	}
	snapshot, loaded := r.private[ownerName]
	if !loaded {
		// Failures (including "not released") are recorded as nil: no repeated database reads within this run, and no fallback to the draft
		snapshot, _ = versioning.AgentPrivateSkillMeta(r.schema, ownerName)
		r.private[ownerName] = snapshot
	}
	meta, ok := snapshot[skillName]
	return meta, ok
}

// runtimeMeta replaces the skills' metadata in the list with the current version in bulk (for the resident system-prompt catalog).
// Draft mode returns the list unchanged - the editor and the debugger have to see the copy being edited.
//
// A production run **drops the skills missing from the current version**: the catalog must share its source with the /skill mount and
// read_skill / use_skill. A private skill added after the release, or a tenant-level skill that never had a version, has neither body nor
// scripts in the version directory, so keeping it in the prompt only invites a read that is bound to fail.
func (r *skillMetaReader) runtimeMeta(list []models.Skill) []models.Skill {
	if r == nil || !r.mode.IsCurrent() || len(list) == 0 {
		return list
	}

	// Tenant level: one bulk query against the current version; private: a snapshot per owning agent (self hits the cache)
	ids := make([]string, 0, len(list))
	for i := range list {
		if string(list[i].AgentID) == "" {
			ids = append(ids, list[i].ID)
		}
	}
	tenant := map[string]versioning.SkillMeta{}
	if len(ids) > 0 {
		if got, err := versioning.TenantSkillMeta(r.schema, ids); err == nil {
			tenant = got
		}
	}

	out := make([]models.Skill, 0, len(list))
	for i := range list {
		s := list[i]
		if string(s.AgentID) == "" {
			meta, ok := tenant[s.ID]
			if !ok {
				continue // not in this version -> not in the catalog
			}
			applySkillMeta(&s, meta)
			out = append(out, s)
			continue
		}
		ownerName, err := ownerAgentName(r.schema, r.agentID, r.agentName, string(s.AgentID), r.agents)
		if err != nil {
			continue // the owner cannot even be resolved: a production run does not accept it
		}
		meta, ok := r.privateMetaFor(ownerName, s.Name)
		if !ok {
			continue
		}
		applySkillMeta(&s, meta)
		out = append(out, s)
	}
	return out
}

// ownerAgentName resolves the owning agent's name: empty -> tenant level (returns ""); self -> the current name directly; other -> a
// database lookup.
func ownerAgentName(schema, selfAgentID, selfAgentName, ownerAgentID string, repo agents.Repository) (string, error) {
	if ownerAgentID == "" {
		return "", nil
	}
	if ownerAgentID == selfAgentID {
		return selfAgentName, nil
	}
	inst, err := repo.GetAgent(&auth.AuthInfo{Schema: schema}, ownerAgentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("skill owner agent %s not found", ownerAgentID)
		}
		return "", err
	}
	return inst.Name, nil
}

// applySkillMeta copies the metadata from the version onto the entity row (definition fields only; id / status / timestamps are
// untouched).
func applySkillMeta(s *models.Skill, meta versioning.SkillMeta) {
	s.Description = xsql.String(meta.Description)
	if len(meta.Metadata) > 0 {
		s.Metadata = xsql.JSONB{Data: json.RawMessage(meta.Metadata)}
	}
	s.UserInvocable = xsql.Bool(meta.UserInvocable)
	s.CommandDispatch = xsql.String(meta.CommandDispatch)
	s.CommandTool = xsql.String(meta.CommandTool)
	s.Restricted = xsql.Bool(meta.Restricted)
}
