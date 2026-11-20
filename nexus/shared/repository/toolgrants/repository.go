// Package toolgrants provides data access to the tool grant system:
//   - tool groups (tool_groups / tool_group_items)
//   - agent tool grants (agent_tool_grants)
//   - skill tool dependencies (skill_tool_grants)
//
// Semantics:
//   - grant_kind = required (must import, always injected) / allow (usable) / deny (explicitly refused, outranking allow)
//   - allow and deny are mutually exclusive for the same ref; the layer above guarantees it on write (setting allow
//     deletes the deny of the same ref and vice versa)
//   - a tool group member may only be a native group or a single tool; it must not reference another tool group (the
//     database CHECK is the backstop)
package toolgrants

import "flame/nexus/shared/models"

// Repository is the tool grant repository interface (it operates on the tenant schema).
type Repository interface {
	// ===== custom tool groups =====
	// ListGroups lists every custom group (members included).
	ListGroups(schema string) ([]models.ToolGroup, error)
	// GetGroup returns a custom group by name (members included); missing returns (nil, nil).
	GetGroup(schema, name string) (*models.ToolGroup, error)
	// UpsertGroup creates / updates a custom group by name and replaces its members wholesale (transaction), returning
	// the group id.
	UpsertGroup(schema, name, description, userID string, items []models.ToolGroupItem) (string, error)
	// DeleteGroup deletes a custom group by name (its members cascade).
	DeleteGroup(schema, name string) error

	// ===== agent tool grants =====
	// ListAgentGrants lists every grant of an agent.
	ListAgentGrants(schema, agentID string) ([]models.AgentToolGrant, error)
	// ReplaceAgentGrants replaces an agent's grants wholesale (transaction).
	ReplaceAgentGrants(schema, agentID, userID string, grants []models.AgentToolGrant) error
	// UpsertAgentGrant adds / changes one grant of an agent (transaction: delete the old row by ref first, then insert).
	// Changing the kind for the same ref overwrites it — which inherently guarantees one row per ref (allow and required
	// never coexist).
	UpsertAgentGrant(schema, agentID, userID string, g models.AgentToolGrant) error
	// DeleteAgentGrant removes one grant of an agent precisely, returning the number of rows deleted (for an idempotent
	// report).
	DeleteAgentGrant(schema, agentID, ref string) (int64, error)

	// ===== skill tool dependencies =====
	// ListSkillGrants lists the tool dependencies a skill declares.
	ListSkillGrants(schema, skillID string) ([]models.SkillToolGrant, error)
	// ReplaceSkillGrants replaces a skill's tool dependencies wholesale (transaction).
	ReplaceSkillGrants(schema, skillID string, grants []models.SkillToolGrant) error
	// AddSkillGrant appends one dependency (ON CONFLICT DO NOTHING). Reports whether anything was really inserted
	// (idempotent).
	AddSkillGrant(schema, skillID string, g models.SkillToolGrant) (bool, error)
	// RemoveSkillGrant removes one dependency by ref (across item_type), returning the number of rows deleted.
	// It deliberately does not check that the ref exists — a dangling dependency left after a tool / connection was
	// deleted must still be cleanable.
	RemoveSkillGrant(schema, skillID, ref string) (int64, error)

	// ===== reverse lookup (reference checks before deleting a connection / endpoint / custom group) =====
	// CountRefUsages counts how many times a ref is used in agent grants, skill dependencies and custom group members.
	CountRefUsages(schema, ref string) (int, error)
	// CountRefPrefixUsages counts the usages of a ref and its sub-items (ref:xxx), for example when deleting the
	// connection fs it counts both "mcp:fs" and "mcp:fs:*".
	CountRefPrefixUsages(schema, ref string) (int, error)
	// ListRefUsages returns the usage details of a ref (for the admin side's "referenced in N places").
	// With prefix=true sub-items match as well (e.g. ref=mcp:fs also counts mcp:fs:read_file).
	ListRefUsages(schema, ref string, prefix bool) (*RefUsages, error)
}

// RefUsageAgent is one agent grant reference.
type RefUsageAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GrantKind string `json:"grant_kind"` // required | allow | deny
	Ref       string `json:"ref"`        // the ref actually matched (a sub-item for a prefix query)
}

// RefUsageSkill is one skill dependency reference.
type RefUsageSkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// RefUsageGroup is one custom group member reference.
type RefUsageGroup struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// RefUsages holds a ref's usage details: who uses it (agent grants / skill dependencies / custom group members).
type RefUsages struct {
	Ref    string          `json:"ref"`
	Prefix bool            `json:"prefix"`
	Total  int             `json:"total"`
	Agents []RefUsageAgent `json:"agents"`
	Skills []RefUsageSkill `json:"skills"`
	Groups []RefUsageGroup `json:"groups"`
}
