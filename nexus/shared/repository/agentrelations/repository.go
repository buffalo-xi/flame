// Package agentrelations provides data access to the call grants between agents (the agent_relations table).
// Semantics: no row = the caller may not call the callee (deny by default); a row = allowed.
package agentrelations

import "flame/nexus/shared/models"

// Repository is the agent call grant data access interface.
type Repository interface {
	// Allowed reports whether the caller may call the callee (no row = not allowed).
	Allowed(schema, callerID, calleeID string) (bool, error)
	// ListByCaller lists the targets the caller may call (name included), sorted by target name.
	ListByCaller(schema, callerID string) ([]models.AgentRelation, error)
	// ListCallableByCaller lists a summary of the targets the caller may call (id / name / description / status), sorted by
	// target name. One JOIN fetches it all, for the system prompt's "callable agents" list.
	ListCallableByCaller(schema, callerID string) ([]models.CallableAgent, error)
	// ListByCallee is the reverse lookup: which agents may call the callee (name included), sorted by source name.
	ListByCallee(schema, calleeID string) ([]models.AgentRelation, error)
	// ReplaceByCaller replaces the caller's out-edges wholesale (whom it may call), clearing and rebuilding inside a
	// transaction.
	ReplaceByCaller(schema, callerID string, calleeIDs []string, operatorID string) error
	// ReplaceByCallee replaces the callee's in-edges wholesale (who may call it), clearing and rebuilding inside a
	// transaction.
	// Note: **do not** use it for a single add / remove — it wipes the in-edges others configured for that callee.
	ReplaceByCallee(schema, calleeID string, callerIDs []string, operatorID string) error
	// AddRelation appends one call grant (idempotent: one row per (caller, callee)).
	AddRelation(schema, callerID, calleeID, operatorID string) error
	// RemoveRelation removes one call grant, returning the number of rows deleted (for an idempotent report).
	RemoveRelation(schema, callerID, calleeID string) (int64, error)
}

// NewRepository creates the Repository (PostgreSQL implementation).
func NewRepository() Repository { return &PostgresRepository{} }
