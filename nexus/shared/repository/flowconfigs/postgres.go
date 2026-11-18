package repository

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// PostgresRepository PostgreSQL 实现的 flow_configs 存储
type PostgresRepository struct{}

// NewPostgresRepository 创建 PostgreSQL 存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// tableName 返回 platform.flow_configs 全名
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".flow_configs"
}

// columns 查询列（不含 tenant_id）
const columns = "max_timeout, max_recursion_depth, max_node_count, max_exec_count, max_loop_times, max_run_records, max_session_records"

// Get 获取指定租户的配置行，无该行时返回 nil
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

// Upsert 插入或更新指定租户的配置行
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
