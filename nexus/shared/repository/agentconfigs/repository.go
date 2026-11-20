package repository

import "database/sql"

// AgentConfig corresponds to one platform.agent_configs row (NULL is expressed with sql.NullInt32).
//
// The difference from FlowConfig: these four columns **do not allow -1** (they are all safety valves, see
// sql/agent_configs.sql).
type AgentConfig struct {
	TenantID       string
	MaxIterations  sql.NullInt32
	MaxCallDepth   sql.NullInt32
	MaxTools       sql.NullInt32
	MaxOutputBytes sql.NullInt32
}

// Repository is the agent_configs storage interface.
type Repository interface {
	// Get returns the configuration row of a tenant (an empty tenantID means the system row), or nil when there is none.
	Get(tenantID string) (*AgentConfig, error)
	// Upsert inserts or updates the configuration row of a tenant.
	Upsert(cfg *AgentConfig) error
}
