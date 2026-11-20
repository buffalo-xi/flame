package repository

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// PostgresRepository is the PostgreSQL implementation of agent_configs storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// tableName returns the full name of platform.agent_configs.
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".agent_configs"
}

// columns are the selected columns (tenant_id excluded).
const columns = "max_iterations, max_call_depth, max_tools, max_output_bytes"

// Get returns the configuration row of a tenant, or nil when there is none.
func (r *PostgresRepository) Get(tenantID string) (*AgentConfig, error) {
	var cfg AgentConfig
	err := db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(
		&cfg.MaxIterations, &cfg.MaxCallDepth, &cfg.MaxTools, &cfg.MaxOutputBytes,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg.TenantID = tenantID
	return &cfg, nil
}

// Upsert inserts or updates the configuration row of a tenant.
func (r *PostgresRepository) Upsert(cfg *AgentConfig) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (
			tenant_id, max_iterations, max_call_depth, max_tools, max_output_bytes, updated_at
		) VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			max_iterations = EXCLUDED.max_iterations,
			max_call_depth = EXCLUDED.max_call_depth,
			max_tools = EXCLUDED.max_tools,
			max_output_bytes = EXCLUDED.max_output_bytes,
			updated_at = NOW()`,
		cfg.TenantID, cfg.MaxIterations, cfg.MaxCallDepth, cfg.MaxTools, cfg.MaxOutputBytes,
	)
	return err
}
