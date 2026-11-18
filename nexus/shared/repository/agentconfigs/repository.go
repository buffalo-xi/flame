package repository

import "database/sql"

// AgentConfig 对应 platform.agent_configs 一行（NULL 用 sql.NullInt32 表达）。
//
// 与 FlowConfig 的差别：这四列**不允许 -1**（都是安全阀，见 sql/agent_configs.sql）。
type AgentConfig struct {
	TenantID       string
	MaxIterations  sql.NullInt32
	MaxCallDepth   sql.NullInt32
	MaxTools       sql.NullInt32
	MaxOutputBytes sql.NullInt32
}

// Repository agent_configs 存储接口
type Repository interface {
	// Get 获取指定租户的配置行（tenantID 为空表示系统行），无该行时返回 nil
	Get(tenantID string) (*AgentConfig, error)
	// Upsert 插入或更新指定租户的配置行
	Upsert(cfg *AgentConfig) error
}
