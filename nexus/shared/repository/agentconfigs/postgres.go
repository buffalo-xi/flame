package repository

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// PostgresRepository PostgreSQL 实现的 agent_configs 存储
type PostgresRepository struct{}

// NewPostgresRepository 创建 PostgreSQL 存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// tableName 返回 platform.agent_configs 全名
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".agent_configs"
}

// columns 查询列（不含 tenant_id）
const columns = "max_iterations, max_call_depth, max_tools, max_output_bytes"

// Get 获取指定租户的配置行，无该行时返回 nil
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

// Upsert 插入或更新指定租户的配置行
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
