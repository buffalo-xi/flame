package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versioning"
	"flame/tools/xsql"
)

// skillManager implements tools.SkillsManager: metadata and ownership go to the skills table, content goes to files per owner.
//
//	tenant level: tenants/{schema}/skills/{name}/SKILL.md
//	private:      tenants/{schema}/agents/{agentName}/skills/{name}/SKILL.md
//
// agentID / agentName / userID are frozen at construction time and cannot be overridden by the tool layer (no privilege escalation).
type skillManager struct {
	schema    string
	agentID   string // current agent uuid (written to agent_id / used for the visibility filter)
	agentName string // current agent name (builds the private skill path)
	userID    string // current user uuid (written to created_by/updated_by)
	storage   storage.Storage
	repo      skills.Repository
	agents    agents.Repository     // resolves the owning agent for direct lookups / path building
	grants    toolgrants.Repository // tool dependencies a skill declares (skill_tool_grants)
	// discovery allows discovering unbound skills (agents.skill_discovery):
	//   false (default, strict): visible = private ∪ bound (the same set as the system prompt list)
	//   true                   : visible = private ∪ bound ∪ tenant-level skills visible to everyone (progressive disclosure)
	// Note: the system prompt list **always** contains private ∪ bound only, regardless of this switch.
	discovery bool
	// isSupervisor reports whether the current agent carries the super privilege (agents.is_supervisor), frozen at construction time.
	isSupervisor bool
	// mode is the definition-reading mode: draft = the draft (editing / debugging / trial chat), current = the current version (production run).
	// It only affects **reads**: writes always land in the draft (skillPath is unchanged).
	mode tools.DefinitionMode

	// meta is the metadata reader of production runs (description / visibility / tool dependencies all come from the current version).
	// It shares one implementation with toolGrantManager - when the admin side and the runtime side disagree, triage is hardest.
	meta *skillMetaReader
}

// NewSkillManager creates a SkillManager.
//
// mode decides which copy "reading the body" reads (writes always go to the draft). Production entries pass tools.ModeCurrent; everything else
// (the editor, the debugger, trial chat, unit tests) leaves it empty, which means draft.
func NewSkillManager(schema, agentID, agentName, userID string, st storage.Storage,
	discovery bool, isSupervisor bool, mode tools.DefinitionMode) tools.SkillsManager {
	agentRepo := agents.NewRepository()
	return &skillManager{
		schema:       schema,
		agentID:      agentID,
		agentName:    agentName,
		userID:       userID,
		storage:      st,
		repo:         skills.NewRepository(),
		agents:       agentRepo,
		grants:       toolgrants.NewRepository(),
		discovery:    discovery,
		isSupervisor: isSupervisor,
		mode:         mode,
		meta:         newSkillMetaReader(schema, agentID, agentName, agentRepo, mode),
	}
}

// skillMeta is the metadata reader. Built lazily so a zero-value skillManager constructed directly in a test still works (draft criterion, no
// version table involved).
func (m *skillManager) skillMeta() *skillMetaReader {
	if m.meta == nil {
		m.meta = newSkillMetaReader(m.schema, m.agentID, m.agentName, m.agents, m.mode)
	}
	return m.meta
}

// skillPath builds the content file path from "owning agent name + skill name" (pass an empty ownerName for tenant level).
//
// The key difference from the old implementation: a private skill's directory name comes from the **owner**, not from the current agent - only
// then does the path land in the right place when the super agent edits another agent's skill across owners.
func (m *skillManager) skillPath(ownerName, skillName string) string {
	if ownerName == "" {
		return tools.SkillPath(m.schema, skillName)
	}
	return tools.AgentSkillPath(m.schema, ownerName, skillName)
}

// readPath decides which copy of the content to read: the draft, or the released current version.
//
// How it splits work with skillPath: skillPath is always the draft (used by Create / Update / Delete), while readPath resolves by run mode (used
// by read_skill / use_skill).
//
// A private skill has no version number of its own: its version is the owning agent's version, so the path lands in
// versions/agents/<owner>/v<n>/skills/<skill>/SKILL.md.
func (m *skillManager) readPath(ownerName, skillName string) (string, error) {
	if !m.mode.IsCurrent() {
		return m.skillPath(ownerName, skillName), nil
	}
	if ownerName == "" {
		_, dir, err := versioning.TenantSkillCurrentVersion(m.schema, skillName)
		if err != nil {
			return "", err
		}
		return path.Join(dir, paths.SkillEntryFile), nil
	}
	_, dir, err := versioning.AgentCurrentVersion(m.schema, ownerName)
	if err != nil {
		return "", err
	}
	return path.Join(dir, paths.AgentSkillsSubdir, skillName, paths.SkillEntryFile), nil
}

// ownerName resolves the owning agent's name (implemented in skillmeta.go: the same ownership resolution as the metadata reader).
func (m *skillManager) ownerName(ownerAgentID string) (string, error) {
	return ownerAgentName(m.schema, m.agentID, m.agentName, ownerAgentID, m.agents)
}

// checkOwnerAgent requires the owning agent to exist and be in the normal state (criterion models.IsModifiable, the same as the file gate and
// the admin side). An empty ownerID means a tenant-level skill, which has no owning agent to check.
//
// The agent itself **must** be checked too. This used to read "its own state is guaranteed at runtime", which does not hold: skipping itself
// would let a deprecated agent keep editing its own private skills, and would reach the opposite conclusion from the file tools' gate
// (checkWriteOwn in gate.go).
//
// Note that this is the "owning agent" gate - the target skill itself must still be in the normal state.
func (m *skillManager) checkOwnerAgent(ownerID string) error {
	if ownerID == "" {
		return nil
	}
	inst, err := m.agents.GetAgent(&auth.AuthInfo{Schema: m.schema}, ownerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("skill owner agent %s not found", ownerID)
		}
		return err
	}
	if !inst.IsModifiable() {
		return fmt.Errorf("agent %s is deprecated; its private skills are frozen", inst.Name)
	}
	return nil
}

// prepareToolDeps parses and validates the tool-dependency argument:
//   - in is nil (the tool passed nothing) -> (nil, nil), and the caller reads that as "do not change";
//   - a non-super agent is refused outright - dependencies are the **trigger** that makes an allow grant take effect, so whoever can write a
//     dependency holds the grant switch;
//   - everything else goes to buildToolDeps for format and existence checks.
func (m *skillManager) prepareToolDeps(in *[]string) ([]models.SkillToolGrant, error) {
	if in == nil {
		return nil, nil
	}
	if !m.isSupervisor {
		return nil, errors.New("only the super agent may declare a skill's tool dependencies")
	}
	return m.buildToolDeps(*in)
}

// buildToolDeps validates and normalises a skill's tool dependencies (pure data handling, no database write).
//
// The same criterion as the admin side's admin/toolgrants.SetSkillToolGrants:
//   - allowGroup=false - the skill side **may not reference tool groups** (only natural groups or concrete tools, otherwise a tool group could
//     escalate indirectly), consistent with toolgrant.ValidateRef's judgement;
//   - every ref must expand to tools, otherwise it is refused - preventing a hand-slip dead ref that never takes effect;
//   - duplicates are dropped, blanks ignored; item_type is derived from the ref shape, so the caller does not pass it.
func (m *skillManager) buildToolDeps(refs []string) ([]models.SkillToolGrant, error) {
	catalog := toolgrant.NewCatalog(m.schema, m.grants, true)
	cats := toolgrant.BuiltinCategories()
	out := make([]models.SkillToolGrant, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		if err := toolgrant.ValidateRef(ref, false); err != nil {
			return nil, fmt.Errorf("invalid tool ref %q: %w", ref, err)
		}
		list, err := catalog.ToolsOfRef(ref)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("tool ref %q not found or expands to no tools", ref)
		}
		itemType := models.GrantItemTool
		if p, perr := toolgrant.ParseRef(ref); perr == nil && p.IsGroup(cats) {
			itemType = models.GrantItemGroup
		}
		seen[ref] = true
		out = append(out, models.SkillToolGrant{ItemType: itemType, Ref: ref})
	}
	return out, nil
}

// saveDeps finishes the tool dependencies: nil (not passed) changes nothing, non-nil replaces everything.
// deps was already validated by prepareToolDeps; this only writes it.
func (m *skillManager) saveDeps(skillID string, in *[]string, deps []models.SkillToolGrant) error {
	if in == nil {
		return nil
	}
	return m.grants.ReplaceSkillGrants(m.schema, skillID, deps)
}

// depListRefs returns the refs a skill currently declares (echoed back after a write: "this is what it looks like now").
func (m *skillManager) depListRefs(skillID string) ([]string, error) {
	list, err := m.grants.ListSkillGrants(m.schema, skillID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, g := range list {
		out = append(out, g.Ref)
	}
	return out, nil
}

// skillForDepWrite is the common precondition of the dependency write entries: supervisor-only + locate + owning agent writable + target in the
// normal state.
//
// Dependencies are the **trigger** that makes an allow grant take effect (whoever can write them holds the "when does it get the tool" switch),
// so like prepareToolDeps it is super-only; and since a skill definition is being written, the two state gates of definition editing apply.
func (m *skillManager) skillForDepWrite(ref tools.SkillRef) (*models.Skill, error) {
	if !m.isSupervisor {
		return nil, errors.New("only the super agent may declare a skill's tool dependencies")
	}
	s, err := m.findRef(ref)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("skill %q not found", ref.Name)
	}
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return nil, err
	}
	if !s.IsModifiable() {
		return nil, fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	if m.grants == nil {
		return nil, errors.New("tool grant repository not available")
	}
	return s, nil
}

// AddToolDep appends one tool dependency to a skill (idempotent) and returns whether anything was added plus the complete dependency list after
// the change.
//
// It complements the tool_deps argument of update_*_skill (a full replacement): here there is no need to read the current state first, and a
// missing entry cannot silently delete the other dependencies.
func (m *skillManager) AddToolDep(_ context.Context, ref tools.SkillRef, toolRef string) (*tools.SkillDepChange, error) {
	s, err := m.skillForDepWrite(ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(toolRef) == "" {
		return nil, errors.New("tool ref is empty")
	}
	// The same criterion as create / update: format + expandable + no custom group reference (see the notes on buildToolDeps)
	deps, err := m.buildToolDeps([]string{toolRef})
	if err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, errors.New("tool ref is empty")
	}
	added, err := m.grants.AddSkillGrant(m.schema, s.ID, deps[0])
	if err != nil {
		return nil, err
	}
	all, err := m.depListRefs(s.ID)
	if err != nil {
		return nil, err
	}
	return &tools.SkillDepChange{Skill: s.Name, SkillID: s.ID, Ref: deps[0].Ref, Added: added, ToolDeps: all}, nil
}

// RemoveToolDep removes one tool dependency of a skill (idempotent) and returns the number of deleted rows plus the complete dependency list
// after the change.
//
// Only the ref syntax is validated, not whether it still expands - a dangling dependency left behind by a deleted tool / connection must be
// removable (the same consideration as revoke_agent_tool).
func (m *skillManager) RemoveToolDep(_ context.Context, ref tools.SkillRef, toolRef string) (*tools.SkillDepChange, error) {
	s, err := m.skillForDepWrite(ref)
	if err != nil {
		return nil, err
	}
	toolRef = strings.TrimSpace(toolRef)
	if toolRef == "" {
		return nil, errors.New("tool ref is empty")
	}
	if err := toolgrant.ValidateRef(toolRef, false); err != nil {
		return nil, fmt.Errorf("invalid tool ref %q: %w", toolRef, err)
	}
	removed, err := m.grants.RemoveSkillGrant(m.schema, s.ID, toolRef)
	if err != nil {
		return nil, err
	}
	all, err := m.depListRefs(s.ID)
	if err != nil {
		return nil, err
	}
	return &tools.SkillDepChange{Skill: s.Name, SkillID: s.ID, Ref: toolRef, Removed: removed, ToolDeps: all}, nil
}

// findRef locates a skill by SkillRef:
//
//	ID non-empty    -> by id (globally unique); without a scope it is a cross-owner operation, super agent only
//	Scope non-empty -> by "the space the tool declares" + name:
//	                   agent=private space (owner in OwnerID, empty = self) / tenant=tenant-level shared
//	both empty      -> by the current agent's visibility criterion (read_skill)
func (m *skillManager) findRef(ref tools.SkillRef) (*models.Skill, error) {
	if ref.ID != "" {
		// skills.id is a uuid column: putting a name (or any non-UUID) into skill_id makes Postgres report 22P02
		// (invalid input syntax for type uuid) instead of "no such row", and that SQL error gives the model no guidance at all.
		// So it is refused by shape here, pointing back to the correct usage (use name for a name).
		if !isUUID(ref.ID) {
			return nil, fmt.Errorf("skill_id %q is not a valid id; use the name argument to locate by name", ref.ID)
		}
		// Without a scope, locating by id is the cross-owner entry (read_skill's skill_id), hence super-only;
		// with a scope (the own / shared tools) checkSkillScope validates ownership, so anyone may use it.
		if ref.Scope == "" && !m.isSupervisor {
			return nil, errors.New("only the super agent may operate on a skill by id")
		}
		return m.repo.Get(m.schema, ref.ID)
	}
	switch ref.Scope {
	case tools.SkillScopeTenant:
		return m.repo.FindByName(m.schema, ref.Name, "") // "" = tenant level
	case tools.SkillScopeAgent:
		target := m.agentID
		if ref.OwnerID != "" {
			// An explicit owner (the *_agent_skill family): locating another agent's private skill, super agent only
			if !m.isSupervisor {
				return nil, errors.New("only the super agent may locate another agent's private skills")
			}
			target = ref.OwnerID
		}
		if target == "" {
			return nil, errors.New("no agent context for private skill")
		}
		return m.repo.FindByName(m.schema, ref.Name, target)
	}
	return m.findByName(ref.Name)
}

// checkSkillScope validates that "the space the tool declares" matches "the target skill's real ownership".
func (m *skillManager) checkSkillScope(scope, targetOwnerID string, s *models.Skill) error {
	if s == nil {
		return nil
	}
	return checkSkillScope(m.isSupervisor, m.agentID, targetOwnerID, scope, s)
}

// checkSkillScope is a pure function: the own family may only touch **its own private** skills, the shared family only **tenant-level shared**
// skills, and the agent_skill family (non-empty targetOwnerID) only **the named agent's private** skills.
//
// Orthogonal to tool categorisation (skill-own / skill-admin ...), and both are needed: categorisation decides "who may use which tool", this
// decides "which space's data the tool may touch". The own family is open to everyone (and with discovery on it can "see" shared skills), so
// without an ownership check a non-super agent could reach a shared skill through the visibility criterion; conversely, when a super agent hands
// a shared skill to an own tool, it is corrected here back to the dual tool - the error text names the tool that should be used, steering the
// model's wrong pick.
//
//	empty scope: no space declared (read_skill), no check; s == nil (not located) is reported as not found by the caller.
func checkSkillScope(isSupervisor bool, selfAgentID, targetOwnerID, scope string, s *models.Skill) error {
	if scope == "" || s == nil {
		return nil
	}
	owner := string(s.AgentID)
	switch scope {
	case tools.SkillScopeTenant: // shared space: super agent only
		if !isSupervisor {
			return errors.New("only the super agent may work with shared skills")
		}
		if owner == "" {
			return nil
		}
		return errors.New("this is a private skill; use the *_own_skill tools instead")
	case tools.SkillScopeAgent: // private space
		// An explicit owner (the *_agent_skill family): super agent only, and it must land in that owner's private space
		if targetOwnerID != "" {
			if !isSupervisor {
				return errors.New("you may only work with your own private skills")
			}
			if owner == "" {
				return errors.New("this is a tenant-level shared skill; use the *_shared_skill tools instead")
			}
			if owner != targetOwnerID {
				return errors.New("the skill does not belong to the given agent; check its ownership")
			}
			return nil
		}
		if owner != "" && owner == selfAgentID {
			return nil
		}
		if isSupervisor {
			if owner == "" {
				return errors.New("this is a tenant-level shared skill; use the *_shared_skill tools instead")
			}
			// The text points at the new tool: this path has been upgraded from "file content only" to "full management through the meta tools"
			return errors.New("this is another agent's private skill; use the *_agent_skill tools (super agent only), or edit the file directly with read_file + write_file / edit_file")
		}
		return errors.New("you may only work with your own private skills")
	}
	return fmt.Errorf("unknown skill scope %q", scope)
}

// Create creates a skill: writes the content file + registers the row.
func (m *skillManager) Create(ctx context.Context, in tools.SkillInput) (string, error) {
	// Name rule backstop: letters, digits and hyphens only, at most 64 characters
	if !tools.ValidName(in.Name) {
		return "", fmt.Errorf("invalid skill name %q (letters, digits and hyphens only, at most 64 characters)", in.Name)
	}
	scope := in.Scope
	if scope == "" {
		scope = tools.SkillScopeAgent
	}
	// Defence in depth: the right to create a shared skill is closed off by the tool category (skill-admin), and the identity is checked again
	// here - even with a misconfigured category, a non-super agent cannot create a shared skill.
	if scope == tools.SkillScopeTenant && !m.isSupervisor {
		return "", errors.New("only the super agent may create a shared skill")
	}
	ownerID := m.agentID
	switch scope {
	case tools.SkillScopeTenant:
		ownerID = "" // NULL = tenant level
	case tools.SkillScopeAgent:
		if in.OwnerID != "" {
			// Explicit owner: create a private skill for another agent (super agent only); the category already closes this off, identity is
			// checked again here
			if !m.isSupervisor {
				return "", errors.New("only the super agent may create private skills for another agent")
			}
			ownerID = in.OwnerID
		}
		if ownerID == "" {
			return "", errors.New("no agent context for private skill")
		}
	}
	// The owning agent must exist and be editable (the database is only really queried for a cross-owner case)
	if err := m.checkOwnerAgent(ownerID); err != nil {
		return "", err
	}

	// Tool dependencies: only the super agent may declare them; validate first (a failure writes not a single byte), then write after the row is
	// registered
	deps, err := m.prepareToolDeps(in.ToolDeps)
	if err != nil {
		return "", err
	}

	// Duplicate check: look up in the **target space** - private and shared are two independent namespaces, each only needs to be unique on its
	// own (looking up through the visibility criterion would misjudge one's own private skill with the same name as "already exists").
	exist, err := m.repo.FindByName(m.schema, in.Name, ownerID)
	if err != nil {
		return "", err
	}
	if exist != nil {
		return "", fmt.Errorf("skill %q already exists", in.Name)
	}

	// Quota check: the per-tenant skill cap (must happen before the content file is written, or an orphan file is left behind)
	cnt, err := quota.CountSkills(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("skill", cnt, quota.Resolve(m.schema).MaxSkills); err != nil {
		return "", err
	}

	// Write the content file first (a different location per owner): the directory name comes from the **owner**, so the path lands in the right
	// place when building a skill for another agent
	ownerName, err := m.ownerName(ownerID)
	if err != nil {
		return "", err
	}
	// Self-heal historical leftovers: reaching here means the duplicate check (FindByName) already passed - no skill of that name is in the
	// database, so a directory of that name on disk can only be debris left by the old "delete only removes SKILL.md". Clean it up first, or
	// WriteFile reuses the old directory and the old auxiliary files "come back to life" inside the new skill.
	if err := m.purgeStaleSkillDir(ctx, ownerName, in.Name); err != nil {
		return "", err
	}
	// Tenant-level skills also have their version domain keyed by name: if it is not cleaned, recreating with the same name collides with the
	// old directory when releasing v1 (the EnsureAbsent precondition reports version exists, an error pointing nowhere near here).
	// A private skill has no version domain of its own and must not be deleted here (that is the owning agent's history snapshot).
	if scope == tools.SkillScopeTenant {
		verKey := paths.VersionEntityKey(m.schema, paths.VersionSkills, in.Name)
		if verr := m.storage.DeletePrefix(ctx, verKey); verr != nil {
			return "", fmt.Errorf("failed to clean the version directory of the skill with the same name (%s): %w", verKey, verr)
		}
	}
	if err := m.storage.WriteFile(ctx, m.skillPath(ownerName, in.Name), []byte(in.Content)); err != nil {
		return "", err
	}

	// Visibility: when an agent creates a "tenant-level shared" skill it is narrowed by default (enterprise default-deny: no grant, no
	// visibility), and the creator itself is granted afterwards (otherwise it could not even use it).
	// A private skill's restricted flag is not consulted (visibility follows agent_id), so it stays false.
	restricted := scope == tools.SkillScopeTenant

	id, err := m.repo.Create(m.schema, &models.Skill{
		Name:        in.Name,
		Description: xsql.String(in.Description),
		AgentID:     xsql.UUID(ownerID),
		Restricted:  xsql.Bool(restricted),
		CreatedBy:   xsql.UUID(m.userID),
		UpdatedBy:   xsql.UUID(m.userID),
	})
	if err != nil {
		return "", err
	}
	// Shared skill: grant the creator automatically, so "created means usable"
	if restricted && m.agentID != "" {
		if err := m.repo.SetVisibility(m.schema, id, true, []string{m.agentID}, m.userID); err != nil {
			return "", err
		}
	}
	// Tool dependencies: written after registration (validation already happened above; a failure here only affects the dependencies)
	if err := m.saveDeps(id, in.ToolDeps, deps); err != nil {
		return "", err
	}
	return id, nil
}

// Update updates a skill: content, description or name (a rename migrates the content file too).
// The target space is declared by the caller (the own / shared tools); locating and ownership checks both follow it.
func (m *skillManager) Update(ctx context.Context, in tools.SkillInput) error {
	// Locate: by the space the tool declares + name (or skill_id); only without a scope does it fall back to the visibility criterion
	s, err := m.findRef(tools.SkillRef{ID: in.ID, Name: in.Name, Scope: in.Scope, OwnerID: in.OwnerID})
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("skill %q not found", in.Name)
	}
	// Space check: own may only touch its own private skills, shared only tenant-level ones, agent_skill only the named agent's private ones
	if err := m.checkSkillScope(in.Scope, in.OwnerID, s); err != nil {
		return err
	}
	// The owning agent must exist and be editable (the same criterion as the file gate / the admin side)
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return err
	}
	// Only the normal state may change a definition (consistent with the agent / flow side: models.IsModifiable)
	if !s.IsModifiable() {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	// Tool dependencies: only the super agent may declare them; validate first and write at the end (all three exits go through saveDeps)
	deps, err := m.prepareToolDeps(in.ToolDeps)
	if err != nil {
		return err
	}

	// Owner name (decides where the content file lives): the agent itself uses the current name, a cross-agent case is looked up
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return err
	}
	oldPath := m.skillPath(ownerName, s.Name)
	oldName := s.Name // s.Name is overwritten by the rename, and moving the version directory needs the old name

	// (1) Rename: validation + collision check + update the table columns
	if in.NewName != "" && in.NewName != s.Name {
		// Built-in protected skill: no renaming (other fields such as the description may change)
		if models.IsProtectedSkill(s.Name) {
			return fmt.Errorf("protected skill %q cannot be renamed", s.Name)
		}
		if !tools.ValidName(in.NewName) {
			return fmt.Errorf("invalid skill name %q (letters, digits and hyphens only)", in.NewName)
		}
		// Rename collision check: inside the **target space** (private / shared each unique); with skill_id the target ownership is used
		var exist *models.Skill
		if in.ID != "" || in.Scope != "" {
			exist, err = m.repo.FindByName(m.schema, in.NewName, string(s.AgentID))
		} else {
			exist, err = m.repo.FindVisibleByName(m.schema, in.NewName, m.agentID)
		}
		if err != nil {
			return err
		}
		if exist != nil {
			return fmt.Errorf("skill %q already exists", in.NewName)
		}
		s.Name = in.NewName
	}

	if in.Description != "" {
		s.Description = xsql.String(in.Description)
	}
	s.UpdatedBy = xsql.UUID(m.userID)

	// The visibility switch only means something for tenant-level shared skills: **validate first, then write**.
	// Doing it after repo.Update would turn "an invalid argument" into "metadata already changed, then an error", leaving a half-written state.
	if in.Restricted != nil && string(s.AgentID) != "" {
		return fmt.Errorf("restricted only applies to tenant-level shared skills: %q is a private skill (visibility follows the owning agent)", s.Name)
	}

	// (2) Update the metadata (name included)
	if err := m.repo.Update(m.schema, s); err != nil {
		return err
	}

	// (2') Visibility switch (tenant-level shared skills only, ownership already validated above): only skills.restricted is changed.
	// The whitelist is maintained by bind_skill / unbind_skill, and SetVisibility must **never** stand in for it here - it clears and rebuilds
	// agent_skill_bindings in a transaction, so flipping only the switch would delete every existing binding.
	if in.Restricted != nil {
		if err := m.repo.SetRestricted(m.schema, s.ID, *in.Restricted, m.userID); err != nil {
			return err
		}
	}

	newPath := m.skillPath(ownerName, s.Name)

	// Metadata only, no content passed and no rename -> the file is untouched, but the tool dependencies may still change
	if len(in.Content) == 0 && oldPath == newPath {
		return m.saveDeps(s.ID, in.ToolDeps, deps)
	}

	// (3) Rename: move the **whole skill directory**.
	//
	// Why not keep "write the new SKILL.md + delete the old SKILL.md": a skill directory may now hold auxiliary files and subdirectories (the
	// "/" root addressing of the file tools, see checkWriteOwn in tools/gate.go), and moving only the entry file would leave those resources
	// orphaned in the old directory. Rename is a directory-level operation that works on local / nfs / s3 alike (the same source as
	// paths.RenameAgentDir).
	//
	// When it cannot move (the target directory already exists / a cross-device move), fall back to "read old + write new + delete old", which
	// at least keeps the entry file.
	renamed := oldPath != newPath
	if renamed {
		// Move the version domain first: version directories are keyed by name, so failing to move it must **leave the draft alone** -
		// otherwise the draft sits under the new name while the versions still hang under the old one, and a production run assembling a path
		// from the new name comes up empty.
		//   tenant level: the whole versions/skills/<old> tree (all historical versions)
		//   private:      versions/agents/<owner>/v*/skills/<old> (one directory per version)
		if ownerName == "" {
			verOld := paths.VersionEntityKey(m.schema, paths.VersionSkills, oldName)
			verNew := paths.VersionEntityKey(m.schema, paths.VersionSkills, s.Name)
			if rerr := m.storage.Rename(ctx, verOld, verNew); rerr != nil {
				return fmt.Errorf("failed to move the skill version directory (%s -> %s): %w", verOld, verNew, rerr)
			}
		} else if rerr := paths.RenamePrivateSkillDirs(m.storage, m.schema, ownerName, oldName, s.Name); rerr != nil {
			return rerr
		}
		if rerr := m.storage.Rename(ctx, path.Dir(oldPath), path.Dir(newPath)); rerr != nil {
			if data, rerr2 := m.storage.ReadFile(ctx, oldPath); rerr2 == nil {
				if werr := m.storage.WriteFile(ctx, newPath, data); werr != nil {
					return werr
				}
			}
			_ = m.storage.Delete(ctx, oldPath)
		}
	}

	// (4) Content: keep the original file when none is passed. On a rename the file already moved with the directory, so no re-read or rewrite
	// is needed.
	content := []byte(in.Content)
	if len(content) == 0 {
		if renamed {
			return m.saveDeps(s.ID, in.ToolDeps, deps)
		}
		data, rerr := m.storage.ReadFile(ctx, oldPath)
		if rerr != nil {
			// The original file cannot be read: leave the content alone (the old behaviour, no error), but the dependencies still have to land
			return m.saveDeps(s.ID, in.ToolDeps, deps)
		}
		content = data
	}
	if err := m.storage.WriteFile(ctx, newPath, content); err != nil {
		return err
	}
	return m.saveDeps(s.ID, in.ToolDeps, deps)
}

// purgeStaleSkillDir removes the historical leftovers of a "skill directory with the same name" (nothing happens when the directory is absent).
//
// It is called after Create's duplicate check (FindByName) has passed: no skill of that name is in the database, so a directory of that name on
// disk can only be debris left by the old "delete only removes SKILL.md". Keeping it makes WriteFile reuse the old directory and lets the old
// auxiliary files "come back to life" inside the new skill. It is a separate method so a unit test without a database can cover it directly
// (Create itself checks quotas first and cannot run as a pure in-memory test).
func (m *skillManager) purgeStaleSkillDir(ctx context.Context, ownerName, name string) error {
	dir := path.Dir(m.skillPath(ownerName, name))
	ok, err := m.storage.Exists(ctx, dir)
	if err != nil || !ok {
		// Do not conclude anything from a failed stat here: proceed with the write when there is no leftover, and let WriteFile report a real problem
		return nil
	}
	return m.storage.DeletePrefix(ctx, dir)
}

// Delete deletes a skill (table row + files).
func (m *skillManager) Delete(ctx context.Context, ref tools.SkillRef) error {
	// Locate: by the space the tool declares + name (or skill_id); only without a scope does it fall back to the visibility criterion
	s, err := m.findRef(ref)
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("skill %q not found", ref.Name)
	}
	// Space check: own may only delete its own private skills, shared only tenant-level ones, agent_skill only the named agent's private ones
	if err := m.checkSkillScope(ref.Scope, ref.OwnerID, s); err != nil {
		return err
	}
	// The owning agent must exist and be editable (the same criterion as the file gate / the admin side)
	if err := m.checkOwnerAgent(string(s.AgentID)); err != nil {
		return err
	}
	// A built-in protected skill cannot be deleted
	if models.IsProtectedSkill(s.Name) {
		return fmt.Errorf("protected skill %q cannot be deleted", s.Name)
	}
	// Only the normal state may be deleted
	if !s.IsModifiable() {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", s.Name)
	}
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return err
	}
	if err := m.repo.Delete(m.schema, s.ID); err != nil {
		return err
	}
	// Delete the **whole skill directory** (SKILL.md plus auxiliary files / subdirectories), the same criterion as admin/skills.
	// skillPath points at the entry file .../skills/{name}/SKILL.md, and deleting only that would leave the auxiliary files and an empty
	// directory behind; recreating a skill of the same name would then make Create's WriteFile reuse the old directory, so the old auxiliary
	// files "come back to life" inside the new skill. DeletePrefix returns nil when the directory is absent, so this is idempotent by nature.
	if err := m.storage.DeletePrefix(ctx, path.Dir(m.skillPath(ownerName, s.Name))); err != nil {
		return err
	}
	// Tenant-level skills have their own version domain (keyed by name): if it is not cleaned, "delete then recreate with the same name" collides
	// with the old directory when releasing v1 (EnsureAbsent reports version exists). A private skill's versions belong to the owning agent's
	// snapshot, and deleting a draft skill must not touch historical versions.
	if string(s.AgentID) == "" {
		if verr := m.storage.DeletePrefix(ctx, paths.VersionEntityKey(m.schema, paths.VersionSkills, s.Name)); verr != nil {
			return fmt.Errorf("failed to clean the skill version directory: %w", verr)
		}
	}
	return nil
}

// SetAgentSkillDiscovery sets an agent's "discover unbound skills" switch (super agent only).
//
// true: that agent can also see tenant-level skills visible to everyone that are not explicitly bound (progressive disclosure);
// false: the strict criterion, private ∪ bound only.
func (m *skillManager) SetAgentSkillDiscovery(ctx context.Context, agentID string, discovery bool) error {
	if !m.isSupervisor {
		return errors.New("only the super agent may change another agent's skill visibility")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	return m.repo.SetAgentSkillDiscovery(m.schema, agentID, discovery)
}

// resolveTenantSkill resolves a **tenant-level** skill by name (agent_id IS NULL).
// Binding only accepts tenant-level skills: a private skill is only visible to its owning agent by nature, so binding it means nothing.
func (m *skillManager) resolveTenantSkill(name string) (*models.Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("skill name is empty")
	}
	s, err := m.repo.FindByName(m.schema, name, "") // "" = tenant level
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("skill %q not found (only tenant-level skills are accepted)", name)
	}
	return s, nil
}

// BindAgentSkill appends one skill binding to an agent (idempotent: nothing changes when already bound).
// Super agent only; the target agent's existence and mutability are checked by the tool layer first, and the identity is checked once more
// here as a backstop.
func (m *skillManager) BindAgentSkill(ctx context.Context, agentID, skillName string) error {
	if !m.isSupervisor {
		return errors.New("only the super agent may bind skills to another agent")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	s, err := m.resolveTenantSkill(skillName)
	if err != nil {
		return err
	}
	_, err = m.repo.AddBinding(m.schema, agentID, s.ID, m.userID)
	return err
}

// UnbindAgentSkill removes one skill binding of an agent (idempotent: nothing changes when not bound). Super agent only.
func (m *skillManager) UnbindAgentSkill(ctx context.Context, agentID, skillName string) error {
	if !m.isSupervisor {
		return errors.New("only the super agent may unbind skills from another agent")
	}
	if agentID == "" {
		return errors.New("target agent id is empty")
	}
	s, err := m.resolveTenantSkill(skillName)
	if err != nil {
		return err
	}
	_, err = m.repo.RemoveBinding(m.schema, agentID, s.ID)
	return err
}

// ListAgentSkills lists the skill names an agent currently has bound (read-only); an empty agentID returns an empty list.
func (m *skillManager) ListAgentSkills(ctx context.Context, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	return m.repo.ListBoundSkillNames(m.schema, agentID)
}

// ListAgentPrivateSkills lists an agent's private skill names (by uuid, read-only).
// Only the super agent may look at another agent's - a non-super agent should not even see their names.
func (m *skillManager) ListAgentPrivateSkills(ctx context.Context, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	if agentID != m.agentID && !m.isSupervisor {
		return nil, errors.New("only the super agent may list another agent's private skills")
	}
	return m.repo.ListPrivateNames(m.schema, agentID)
}

// List lists the skills visible to the current agent (used by the list_skills tool):
//
//	super agent: its own private ∪ **all tenant-level** skills (restricted / whitelist ignored) - it manages skills, so it must be able to
//	             enumerate everything, otherwise "can change it but cannot see it" appears (update_shared_skill / read_skill(by id) work
//	             while list_skills does not list it); the whitelist is filled in for management.
//	other agents: discovery=false (strict) -> private ∪ bound; discovery=true -> plus the tenant-level skills visible to everyone.
func (m *skillManager) List(ctx context.Context) ([]tools.SkillInfo, error) {
	if m.isSupervisor {
		skills, err := m.repo.ListForSupervisor(m.schema, m.agentID)
		if err != nil {
			return nil, err
		}
		infos := toSkillInfos(skills)
		m.fillBoundAgents(infos)
		return infos, nil
	}
	var (
		list []models.Skill
		err  error
	)
	if m.discovery {
		list, err = m.repo.ListVisible(m.schema, m.agentID)
	} else {
		list, err = m.repo.ListInjected(m.schema, m.agentID)
	}
	if err != nil {
		return nil, err
	}
	return toSkillInfos(list), nil
}

// ListCommands lists the skills exposed as user commands ("/<name>") for this agent.
//
// The visibility criterion is List's, deliberately: a command is "the user picks a skill for this agent", never "the user
// grants it something new". A wider list (say, every tenant-level skill) would let "/" reach a shared skill that
// `restricted` keeps away from this agent, which is an escalation path rather than a convenience.
func (m *skillManager) ListCommands(ctx context.Context) ([]tools.SkillInfo, error) {
	list, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	return commandSkills(list), nil
}

// commandSkills narrows a visibility list to the skills a user may invoke: user_invocable and not deprecated. It takes the
// list it is given (it never widens it), which is what keeps a command from reaching a skill the agent cannot use.
func commandSkills(list []tools.SkillInfo) []tools.SkillInfo {
	out := make([]tools.SkillInfo, 0, len(list))
	for _, s := range list {
		if !s.UserInvocable || s.Status == int(models.StatusDeprecated) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// fillBoundAgents fills in the whitelisted agent names of tenant-level skills in bulk (super agent view only; private skills are skipped).
// One query fetches all of them (no per-row N+1); a failure does not affect the main flow - the list is still usable and the whitelist simply
// stays empty.
func (m *skillManager) fillBoundAgents(infos []tools.SkillInfo) {
	ids := make([]string, 0, len(infos))
	for i := range infos {
		if infos[i].Scope == tools.SkillScopeTenant {
			ids = append(ids, infos[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	names, err := m.repo.ListBindingNamesBySkills(m.schema, ids)
	if err != nil {
		return
	}
	for i := range infos {
		if infos[i].Scope != tools.SkillScopeTenant {
			continue
		}
		if v := names[infos[i].ID]; len(v) > 0 {
			infos[i].BoundAgents = v
		}
	}
}

// ListInjected lists the skills "injected into the current agent" (private + bound).
// Independent of the discovery switch: the system prompt's resident list always uses this criterion.
func (m *skillManager) ListInjected(ctx context.Context) ([]tools.SkillInfo, error) {
	list, err := m.repo.ListInjected(m.schema, m.agentID)
	if err != nil {
		return nil, err
	}
	// Production run: description / visibility / dependencies come from the current version (freezing the body but not the description is no
	// freezing at all)
	return toSkillInfos(m.skillMeta().runtimeMeta(list)), nil
}

// findByName looks up a single skill by the current visibility criterion.
//
//	super agent: its own private skills first, then **any tenant-level** one (whitelist ignored), aligned with List's full-visibility criterion -
//	             guaranteeing "listed means readable by name", so "can change it but cannot see it" no longer happens.
//	other agents: with discovery=false an unbound shared skill counts as non-existent.
func (m *skillManager) findByName(name string) (*models.Skill, error) {
	if m.isSupervisor {
		return m.repo.FindForSupervisorByName(m.schema, name, m.agentID)
	}
	if m.discovery {
		return m.repo.FindVisibleByName(m.schema, name, m.agentID)
	}
	return m.repo.FindInjectedByName(m.schema, name, m.agentID)
}

// toSkillInfos converts in bulk (keeping the input order).
func toSkillInfos(list []models.Skill) []tools.SkillInfo {
	out := make([]tools.SkillInfo, 0, len(list))
	for _, s := range list {
		out = append(out, toSkillInfo(s))
	}
	return out
}

// Get reads the skill detail (content included). The visibility criterion is the same as List (an unbound shared skill is unreadable in strict
// mode).
func (m *skillManager) Get(ctx context.Context, ref tools.SkillRef) (*tools.SkillDetail, error) {
	s, err := m.findRef(ref)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	ownerName, err := m.ownerName(string(s.AgentID))
	if err != nil {
		return nil, err
	}
	// Production run: metadata and tool dependencies come from the current version (the body is read from the version directory too, see readPath)
	meta, hasMeta := m.skillMeta().versionMeta(s, ownerName)
	if hasMeta {
		applySkillMeta(s, meta)
	}
	var content []byte
	readKey, rerr := m.readPath(ownerName, s.Name)
	if rerr != nil {
		// Production run but the skill has no released version: the body is unavailable, the metadata is still returned.
		// No falling back to the draft - see the notes on readPath.
		content = nil
	} else {
		content, err = m.storage.ReadFile(ctx, readKey)
		if err != nil {
			content = nil // missing content is not an error, the metadata is still returned
		}
	}
	// ${global_vars.X} inside a skill body is expanded too: a skill is a definition that travels with the asset pack, and the hardcoded endpoint
	// addresses / table names it would otherwise contain are exactly what we are avoiding. A failed expansion must be an error - otherwise the
	// model works against the literal "${global_vars.X}".
	body := string(content)
	if body != "" {
		expanded, xerr := globalvars.Expand(m.schema, body)
		if xerr != nil {
			return nil, fmt.Errorf("failed to expand global variables in %s of skill %s: %w", paths.SkillEntryFile, s.Name, xerr)
		}
		body = expanded
	}
	detail := &tools.SkillDetail{
		SkillInfo: toSkillInfo(*s),
		Content:   body,
		ToolDeps:  []string{}, // always an array: with no dependencies this must serialise to [], never null
	}
	if hasMeta {
		// A dependency is a declaration, part of the definition (whether it can really be called still follows the current grant)
		if deps := meta.ToolRefs(); len(deps) > 0 {
			detail.ToolDeps = deps
		}
	} else {
		m.fillToolDeps(s, detail)
	}
	m.fillVisibility(s, detail)
	return detail, nil
}

// fillToolDeps fills in the tool dependencies a skill declares (for read_skill / use_skill display).
//
//   - the declared refs are returned as they are (no group expansion, no dangling entries dropped): what is shown is the definition itself, and
//     group expansion is the Resolver's job; a dangling ref is better left in place for diagnosis (revoke_skill_tool can still remove it).
//   - a read failure only degrades to an empty array: failing to read the dependencies must not also make the body unavailable (the same
//     criterion as "missing content is not an error").
//
// No identity distinction: dependencies are part of the skill definition, so a non-super agent gets them too - they answer "what will my agent
// gain next round", and the activation result is always narrowed by its own allow grants, so seeing them cannot light up anything outside the
// grants.
func (m *skillManager) fillToolDeps(s *models.Skill, detail *tools.SkillDetail) {
	if m.grants == nil {
		return
	}
	refs, err := m.depListRefs(s.ID)
	if err != nil {
		return
	}
	detail.ToolDeps = refs // depListRefs guarantees non-nil (no dependencies = empty slice)
}

// fillVisibility fills in the current visibility state (for read_skill display):
//   - Restricted only means something for tenant-level shared skills (a private skill's visibility follows the owning agent);
//   - BoundAgents = the whitelist (agent_skill_bindings from the skill side), filled in for the super agent only.
func (m *skillManager) fillVisibility(s *models.Skill, detail *tools.SkillDetail) {
	if string(s.AgentID) != "" {
		return
	}
	detail.Restricted = bool(s.Restricted)
	if !m.isSupervisor {
		return
	}
	bindings, err := m.repo.ListBindings(m.schema, s.ID)
	if err != nil {
		return
	}
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names = append(names, string(b.AgentName))
	}
	detail.BoundAgents = names
}

// FindForWrite looks a skill up by "owner + name" (not narrowed by the current agent's visibility), so the cross-domain write gate can decide
// existence. An empty ownerAgentID means tenant level; missing returns (nil, nil). It uses the all-state FindByName (the same as the duplicate
// check).
func (m *skillManager) FindForWrite(_ context.Context, ownerAgentID, skillName string) (*tools.SkillInfo, error) {
	s, err := m.repo.FindByName(m.schema, skillName, ownerAgentID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	info := toSkillInfo(*s)
	return &info, nil
}

// toSkillInfo converts and derives the scope (an empty agent_id means tenant level);
// restricted only means something for tenant-level shared skills (a private skill's visibility follows the owning agent and is always false).
func toSkillInfo(s models.Skill) tools.SkillInfo {
	scope := tools.SkillScopeAgent
	restricted := false
	if s.AgentID == "" {
		scope = tools.SkillScopeTenant
		restricted = bool(s.Restricted)
	}
	return tools.SkillInfo{
		ID:          s.ID,
		Name:        s.Name,
		Description: string(s.Description),
		Scope:       scope,
		Status:      int(s.Status),
		Restricted:  restricted,
		// The user-command definition travels with the skill (in a production run these three come from the current version,
		// see applySkillMeta): it is part of the definition, not a runtime fact.
		UserInvocable:   bool(s.UserInvocable),
		CommandDispatch: string(s.CommandDispatch),
		CommandTool:     string(s.CommandTool),
	}
}
