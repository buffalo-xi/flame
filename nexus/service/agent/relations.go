package agent

import (
	"context"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/repository/agentrelations"
)

// agentRelationsManager implements tools.AgentRelationsManager: it manages the call grants between agents
// (the "out-edges" of agent_relations = who may call whom).
//
// The semantics match caller.go: no row for Allowed means refused, and every agent is treated alike (the super agent gets no exemption);
// so "let the super agent call poet" must really write a (super agent -> poet) row instead of special-casing the identity.
//
// The shape mirrors grant/revoke_agent_tool: paired single rows, never a full replacement - with a full replacement in an LLM's hands,
// leaving one entry out silently revokes a grant.
type agentRelationsManager struct {
	schema       string
	agentID      string // current agent uuid (written to created_by)
	agentName    string // current agent name
	isSupervisor bool   // identity criterion (agents.is_supervisor), frozen at construction time
	userID       string
	rel          agentrelations.Repository
}

// NewAgentRelationsManager creates tools.AgentRelationsManager (with the tenant and the current agent identity frozen in).
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

// AllowCall appends a "caller may call callee" grant (idempotent).
// The checks on the target's existence / non-super-agent / non-deprecated status happen in the tool layer (which has c.Agents to consult).
func (m *agentRelationsManager) AllowCall(_ context.Context, callerID, calleeID string) error {
	if !m.isSupervisor {
		return errors.New("only the super agent may modify an agent's call grants")
	}
	if callerID == "" || calleeID == "" {
		return errors.New("caller / callee id is empty")
	}
	// Relation to D5: the super agent may add out-edges for itself. A call grant widens "whom the super agent may call", not the super
	// agent's own tool surface (fully open by default), so it is not self-escalation; forbidding it would only turn "the super agent built
	// a child but cannot call it" into "go find an administrator". The reverse direction (target = super agent) is refused in the tool
	// layer - caller.go hard-refuses calls to the super agent.
	return m.rel.AddRelation(m.schema, callerID, calleeID, m.userID)
}

// RevokeCall removes a call grant (idempotent) and returns the number of deleted rows.
func (m *agentRelationsManager) RevokeCall(_ context.Context, callerID, calleeID string) (int64, error) {
	if !m.isSupervisor {
		return 0, errors.New("only the super agent may modify an agent's call grants")
	}
	if callerID == "" || calleeID == "" {
		return 0, errors.New("caller / callee id is empty")
	}
	return m.rel.RemoveRelation(m.schema, callerID, calleeID)
}

// ListRelations lists an agent's out-edges (whom it may call) and in-edges (who may call it), returning agent names.
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
