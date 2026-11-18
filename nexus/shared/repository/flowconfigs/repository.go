package repository

import "database/sql"

// flowconfig 对应 platform.flow_configs 一行（NULL 用 sql.NullInt32 表达）
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

// Repository flow_configs 存储接口
type Repository interface {
	// Get 获取指定租户的配置行，无该行时返回 nil
	Get(tenantID string) (*FlowConfig, error)
	// Upsert 插入或更新指定租户的配置行
	Upsert(cfg *FlowConfig) error
}
