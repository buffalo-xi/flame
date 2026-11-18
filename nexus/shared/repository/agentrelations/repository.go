// Package agentrelations 提供 agent 之间调用授权（agent_relations 表）的数据访问。
// 语义：无行 = caller 不可调用 callee（默认拒绝）；有行 = 允许。
package agentrelations

import "flame/nexus/shared/models"

// Repository agent 调用授权数据访问接口。
type Repository interface {
	// Allowed 判断 caller 是否有权调用 callee（无行 = 不允许）。
	Allowed(schema, callerID, calleeID string) (bool, error)
	// ListByCaller 列出 caller 可调用的目标（含 name），按目标名排序。
	ListByCaller(schema, callerID string) ([]models.AgentRelation, error)
	// ListCallableByCaller 列出 caller 可调用的目标摘要（id / name / description / status），
	// 按目标名排序。一次 JOIN 拿全，供 system prompt 的「可调用 agent 清单」。
	ListCallableByCaller(schema, callerID string) ([]models.CallableAgent, error)
	// ListByCallee 反查：哪些 agent 可以调用 callee（含 name），按来源名排序。
	ListByCallee(schema, calleeID string) ([]models.AgentRelation, error)
	// ReplaceByCaller 全量替换 caller 的出边（它能调用谁），事务内清空后重建。
	ReplaceByCaller(schema, callerID string, calleeIDs []string, operatorID string) error
	// ReplaceByCallee 全量替换 callee 的入边（谁能调用它），事务内清空后重建。
	// 注意：单条增删**不要**用它代劳 —— 它会清掉别人给该 callee 配的入边。
	ReplaceByCallee(schema, calleeID string, callerIDs []string, operatorID string) error
	// AddRelation 追加一条调用授权（幂等：同一 (caller, callee) 只保留一行）。
	AddRelation(schema, callerID, calleeID, operatorID string) error
	// RemoveRelation 移除一条调用授权，返回删除行数（幂等报告用）。
	RemoveRelation(schema, callerID, calleeID string) (int64, error)
}

// NewRepository 创建 Repository（PostgreSQL 实现）。
func NewRepository() Repository { return &PostgresRepository{} }
