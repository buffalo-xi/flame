package agent

import (
	"context"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/repository/agentrelations"
)

// agentRelationsManager 实现 tools.AgentRelationsManager：管理 agent 间的调用授权
// （agent_relations 的「出边」= 谁能调用谁）。
//
// 语义对齐 caller.go：Allowed 无行即拒绝、所有 agent 一视同仁（超级智能体也没有豁免）；
// 所以「让超级智能体能调用 poet」必须真的写一行 (超级智能体 → poet)，而不是靠身份特判。
//
// 形态与 grant/revoke_agent_tool 同构：成对单条，不做全量替换 —— 全量替换在 LLM 手里
// 少写一项就等于静默撤销一条授权。
type agentRelationsManager struct {
	schema       string
	agentID      string // 当前 agent uuid（写 created_by）
	agentName    string // 当前 agent 名
	isSupervisor bool   // 身份判据（agents.is_supervisor），由构造时固化
	userID       string
	rel          agentrelations.Repository
}

// NewAgentRelationsManager 创建 tools.AgentRelationsManager（租户与当前 agent 身份固化）。
func NewAgentRelationsManager(schema, agentID, agentName, userID string, isSupervisor bool) tools.AgentRelationsManager {
	return &agentRelationsManager{
		schema:       schema,
		agentID:      agentID,
		agentName:    agentName,
		isSupervisor: isSupervisor,
		userID:       userID,
		rel:          agentrelations.NewRepository(),
	}
}

// AllowCall 追加一条「caller 可调用 callee」的授权（幂等）。
// 目标的存在性 / 非超级智能体 / 非废弃等校验在工具层完成（那里有 c.Agents 可查）。
func (m *agentRelationsManager) AllowCall(_ context.Context, callerID, calleeID string) error {
	if !m.isSupervisor {
		return errors.New("仅超级智能体可修改 agent 的调用授权")
	}
	if callerID == "" || calleeID == "" {
		return errors.New("caller / callee id is empty")
	}
	// 与 D5 的关系：允许超级智能体给自己加出边。调用授权扩的是「超级智能体能调谁」，不是超级智能体自身的
	// 工具面（超级智能体缺省已全开），所以不构成自我扩权；禁掉它只会让「超级智能体造完子 agent 却调用不了」
	// 变成必须找管理员。反向（target = 超级智能体）在工具层被拒 —— caller.go 会硬拒 超级智能体被调用。
	return m.rel.AddRelation(m.schema, callerID, calleeID, m.userID)
}

// RevokeCall 移除一条调用授权（幂等），返回删除行数。
func (m *agentRelationsManager) RevokeCall(_ context.Context, callerID, calleeID string) (int64, error) {
	if !m.isSupervisor {
		return 0, errors.New("仅超级智能体可修改 agent 的调用授权")
	}
	if callerID == "" || calleeID == "" {
		return 0, errors.New("caller / callee id is empty")
	}
	return m.rel.RemoveRelation(m.schema, callerID, calleeID)
}

// ListRelations 列出某 agent 的出边（能调用谁）与入边（谁能调用它），返回 agent 名。
func (m *agentRelationsManager) ListRelations(_ context.Context, agentID string) ([]string, []string, error) {
	if agentID == "" {
		return nil, nil, nil
	}
	out, err := m.rel.ListByCaller(m.schema, agentID)
	if err != nil {
		return nil, nil, err
	}
	in, err := m.rel.ListByCallee(m.schema, agentID)
	if err != nil {
		return nil, nil, err
	}
	canCall := make([]string, 0, len(out))
	for _, r := range out {
		canCall = append(canCall, string(r.CalleeName))
	}
	calledBy := make([]string, 0, len(in))
	for _, r := range in {
		calledBy = append(calledBy, string(r.CallerName))
	}
	return canCall, calledBy, nil
}
