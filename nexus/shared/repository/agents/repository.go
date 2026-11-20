package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListAgentsRequest holds the agent list request parameters.
type ListAgentsRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// Repository is the agent storage interface.
type Repository interface {
	// ListAgents lists every agent.
	ListAgents(authInfo *auth.AuthInfo, req *ListAgentsRequest) ([]models.Agent, int, error)

	// GetAgent returns one agent by id.
	GetAgent(authInfo *auth.AuthInfo, id string) (*models.Agent, error)
	// FindByName returns an agent by name (unique within the tenant); missing returns (nil, nil).
	// The difference from ExistsName: an overwriting import (asset pack / marketplace install) needs the existing row's id
	// and identity switches, which an existence check cannot provide.
	FindByName(authInfo *auth.AuthInfo, name string) (*models.Agent, error)
	// ExistsName reports whether an agent of the same name already exists in the tenant (excludeID, when non-empty, ignores
	// the row itself; used by the rename duplicate check).
	ExistsName(authInfo *auth.AuthInfo, name string, excludeID string) (bool, error)
	// CreateAgent creates an agent. When creatorAgentID is non-empty, the new agent is granted to its creator
	// (agent_relations) in the same transaction, so "whoever created it can call it" holds.
	CreateAgent(authInfo *auth.AuthInfo, agent *models.Agent, creatorAgentID string) (string, error)
	// UpdateAgent updates an agent.
	UpdateAgent(authInfo *auth.AuthInfo, id string, agent *models.Agent) error
	// DeleteAgent deletes an agent; its grants and private skill records are cleaned up in the same transaction
	// (skills.agent_id has no foreign key, so a database-level cascade cannot be relied on).
	DeleteAgent(authInfo *auth.AuthInfo, id string) error
	// UpdateStatus updates status only (1=active enabled | 2=disabled).
	UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error
	// GetUnrestricted reads an agent's "unrestricted tools" switch (agents.tools_unrestricted;
	// a missing agent returns false, nil).
	GetUnrestricted(authInfo *auth.AuthInfo, agentID string) (bool, error)
	// SetUnrestricted sets an agent's "unrestricted tools" switch (single-column UPDATE; no other column is touched).
	SetUnrestricted(authInfo *auth.AuthInfo, agentID string, unrestricted bool) error
	// SetOutputLanguage sets agents.default_language (single-column UPDATE, so no caller has to carry the other
	// columns over); an empty tag clears it (NULL = not configured).
	SetOutputLanguage(authInfo *auth.AuthInfo, agentID, tag string) error
	// SetSupervisor sets an agent's super-privilege attribute (single-column UPDATE).
	// Revoking is refused for the "last supervisor" (otherwise the tenant permanently loses its management surface).
	SetSupervisor(authInfo *auth.AuthInfo, agentID string, isSupervisor bool) error
}
