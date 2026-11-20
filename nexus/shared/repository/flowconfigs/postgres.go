package repository

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// PostgresRepository is the PostgreSQL implementation of flow_configs storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// tableName returns the full name of platform.flow_configs.
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".flow_configs"
}

// columns are the selected columns (tenant_id excluded).
const columns = "max_timeout, max_recursion_depth, max_node_count, max_exec_count, max_loop_times, max_run_records, max_session_records"

// Get returns the configuration row of a tenant, or nil when there is none.
func (r *PostgresRepository) Get(tenantID string) (*FlowConfig, error) {
	var cfg FlowConfig
	err := db.DB.QueryRow(
		`SELECT `+columns+` FROM `+tableName()+` WHERE tenant_id = $1`,
		tenantID,
	).Scan(
		&cfg.MaxTimeout, &cfg.MaxRecursionDepth, &cfg.MaxNodeCount,
		&cfg.MaxExecCount, &cfg.MaxLoopTimes, &cfg.MaxRunRecords, &cfg.MaxSessionRecords,
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
func (r *PostgresRepository) Upsert(cfg *FlowConfig) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (
			tenant_id, max_timeout, max_recursion_depth, max_node_count,
			max_exec_count, max_loop_times, max_run_records, max_session_records, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			max_timeout = EXCLUDED.max_timeout,
			max_recursion_depth = EXCLUDED.max_recursion_depth,
			max_node_count = EXCLUDED.max_node_count,
			max_exec_count = EXCLUDED.max_exec_count,
			max_loop_times = EXCLUDED.max_loop_times,
			max_run_records = EXCLUDED.max_run_records,
			max_session_records = EXCLUDED.max_session_records,
			updated_at = NOW()`,
		cfg.TenantID, cfg.MaxTimeout, cfg.MaxRecursionDepth, cfg.MaxNodeCount,
		cfg.MaxExecCount, cfg.MaxLoopTimes, cfg.MaxRunRecords, cfg.MaxSessionRecords,
	)
	return err
}
