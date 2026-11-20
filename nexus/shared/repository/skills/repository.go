// Package skills provides data access to the skills table.
// A skill's metadata and ownership (agent_id) live in the database; the content lives in files (the path is derived from
// the skill name and its owner).
package skills

import "flame/nexus/shared/models"

// Repository is the data access interface for skills.
type Repository interface {
	// Create creates a skill (the ID is generated when empty) and returns the id.
	Create(schema string, s *models.Skill) (string, error)
	// Update updates name/description/metadata/status/updated_by by id,
	// and user_invocable/command_dispatch/command_tool.
	Update(schema string, s *models.Skill) error
	// Delete deletes by id.
	Delete(schema, id string) error
	// Get fetches by id; missing returns (nil, nil).
	Get(schema, id string) (*models.Skill, error)
	// FindVisibleByName looks up a name in the visible scope: private first (agent_id=agentID),
	// then tenant-level (agent_id IS NULL). Missing returns (nil, nil).
	FindVisibleByName(schema, name, agentID string) (*models.Skill, error)
	// ListVisible lists the visible skills (tenant-level + private, status<>3 i.e. no deprecated ones),
	// sorted by name. Tenant-level skills are narrowed by agent_skill_bindings (see visibleCond).
	ListVisible(schema, agentID string) ([]models.Skill, error)
	// ListInjected lists the skills "injected to this agent": its private skills plus the tenant-level skills it is
	// bound to. The difference from ListVisible: a tenant-level skill visible to everyone (restricted=false) is not
	// injected automatically; only an explicit binding (agent_skill_bindings) counts as "this agent uses this skill".
	ListInjected(schema, agentID string) ([]models.Skill, error)
	// FindInjectedByName looks up a name inside the "injected scope" (the same rule as ListInjected).
	FindInjectedByName(schema, name, agentID string) (*models.Skill, error)
	// ListForSupervisor lists the skills "fully visible to the super agent": its private skills ∪ **every tenant-level**
	// skill (status<>3), ignoring restricted / the allowlist. The super agent is the skill administrator and must be able
	// to enumerate everything (otherwise it could change what it cannot see).
	ListForSupervisor(schema, agentID string) ([]models.Skill, error)
	// FindForSupervisorByName looks a name up as the super agent: its private skills first, then any tenant-level one
	// (ignoring the allowlist); missing returns (nil, nil).
	FindForSupervisorByName(schema, name, agentID string) (*models.Skill, error)
	// ListBindingNamesBySkills returns the allowlisted agent names of several skills in one batch (grouped by skill_id;
	// a skill that does not appear has none).
	ListBindingNamesBySkills(schema string, skillIDs []string) (map[string][]string, error)
	// GetAgentSkillDiscovery reads the "unbound skills are discoverable" switch (a missing agent returns false, nil).
	GetAgentSkillDiscovery(schema, agentID string) (bool, error)
	// SetAgentSkillDiscovery sets the "unbound skills are discoverable" switch.
	SetAgentSkillDiscovery(schema, agentID string, discovery bool) error
	// List lists skills (admin side: paging + filters), returning the page and the total.
	List(schema string, req *ListRequest) ([]models.Skill, int, error)
	// FindByName looks a name up inside a given ownership scope (every status, used for the duplicate-name check).
	// An empty agentID means tenant-level (agent_id IS NULL). Missing returns (nil, nil).
	FindByName(schema, name, agentID string) (*models.Skill, error)
	// UpdateStatus updates status only (enable / disable).
	UpdateStatus(schema, id string, status int) error
	// DeleteByAgentID deletes every private skill record of one agent (cascade when the agent is deleted).
	DeleteByAgentID(schema, agentID string) error

	// ListBindings lists the agents granted a skill (agent_name included), sorted by agent name.
	ListBindings(schema, skillID string) ([]models.AgentSkillBinding, error)
	// SetVisibility sets visibility: the restricted switch plus the allowlist of granted agents (replaced wholesale in a
	// transaction). restricted=false means visible to everyone; true means visible only to the allowlisted agents.
	SetVisibility(schema, skillID string, restricted bool, agentIDs []string, operatorID string) error
	// SetRestricted changes only the skills.restricted column (the allowlist is untouched — it is maintained by
	// bind / unbind).
	//
	// Note: **do not** reuse SetVisibility for this — it DELETEs the whole agent_skill_bindings table and rebuilds it
	// inside a transaction, wiping the allowlist that bind_skill built.
	SetRestricted(schema, skillID string, restricted bool, operatorID string) error
	// ListBindingCounts returns the number of granted agents for the given skill set (for the admin list; a skill that
	// does not appear has 0).
	ListBindingCounts(schema string, skillIDs []string) (map[string]int, error)

	// ListSkillIDsByAgent lists the skill ids bound to an agent (for the agent-side "available skills" configuration),
	// sorted by skill name.
	ListSkillIDsByAgent(schema, agentID string) ([]string, error)
	// ReplaceByAgent replaces an agent's skill bindings wholesale (the agent-side view of the same
	// agent_skill_bindings table). Only "existing tenant-level skills" are accepted (a private skill is inherently
	// visible to its owner only, so binding it is meaningless); only this agent's rows are touched, so the same skill's
	// grants to other agents are unaffected.
	ReplaceByAgent(schema, agentID string, skillIDs []string, operatorID string) error
	// AddBinding idempotently appends one skill binding; only "existing tenant-level skills" are accepted.
	// Reports whether anything was really added (false = the skill is missing / not tenant-level / already bound).
	AddBinding(schema, agentID, skillID, operatorID string) (bool, error)
	// RemoveBinding idempotently removes one skill binding; reports whether anything was really removed.
	RemoveBinding(schema, agentID, skillID string) (bool, error)
	// ListBoundSkillNames lists the skill names bound to an agent (sorted by name, only skills that still exist).
	ListBoundSkillNames(schema, agentID string) ([]string, error)
	// ListPrivateNames lists an agent's **private** skill names (sorted by name, deprecated ones excluded).
	ListPrivateNames(schema, agentID string) ([]string, error)
}

// ListRequest holds the skill list filters (admin side).
type ListRequest struct {
	Page    int
	Limit   int
	AgentID string // non-empty: only that agent's private skills; empty: everything
	Status  *int   // nil: any status
	Keyword string // fuzzy match on name / description
}

// NewRepository creates the Repository (PostgreSQL implementation).
func NewRepository() Repository {
	return &PostgresRepository{}
}
