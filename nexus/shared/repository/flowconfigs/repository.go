package repository

import "database/sql"

// FlowConfig corresponds to one platform.flow_configs row (NULL is expressed with sql.NullInt32).
type FlowConfig struct {
	TenantID          string
	MaxTimeout        sql.NullInt32
	MaxRecursionDepth sql.NullInt32
	MaxNodeCount      sql.NullInt32
	MaxExecCount      sql.NullInt32
	MaxLoopTimes      sql.NullInt32
	MaxRunRecords     sql.NullInt32
	MaxSessionRecords sql.NullInt32
}

// Repository is the flow_configs storage interface.
type Repository interface {
	// Get returns the configuration row of a tenant, or nil when there is none.
	Get(tenantID string) (*FlowConfig, error)
	// Upsert inserts or updates the configuration row of a tenant.
	Upsert(cfg *FlowConfig) error
}
