package svcauthbindings

import "flame/nexus/shared/models"

// BindingIdentity is the identity a service credential resolves to.
// A binding only provides the identity a service call needs (for auditing) and does not restrict roles, so only the
// account status is carried out here.
type BindingIdentity struct {
	UserID string
	Status string // users.status (an empty string when the user row is missing)

	// The endpoint access scope of this credential (svc_auth_bindings.restrict_targets).
	// A new binding defaults to true (the safe default), which allows only the targets in TargetAllow, an empty set
	// allowing none; false = unrestricted, and it has to be turned off explicitly (existing bindings stay false after the
	// migration).
	// Elements look like "agent:<uuid>" / "flow:<uuid>".
	RestrictTargets bool
	TargetAllow     []string

	// ExcludeSupervisor only takes effect when RestrictTargets=false: allow every endpoint but exclude the super agent.
	// It is ignored when RestrictTargets=true (the allowlist already says it precisely).
	ExcludeSupervisor bool
}

// Repository is the service-side authentication identity binding storage interface (shared by MCP / RPC).
type Repository interface {
	// ResolveBindingIdentity resolves a binding identity by (authType, issuer, subject);
	// only bindings with is_active = true match, and a miss returns (nil, nil).
	ResolveBindingIdentity(schema, authType, issuer, subject string) (*BindingIdentity, error)
	// List lists bindings (filtered by type when authType is non-empty), newest created_at first.
	List(schema, authType string) ([]models.SvcAuthBinding, error)
	// GetByID fetches a binding by id; missing returns (nil, nil).
	GetByID(schema, id string) (*models.SvcAuthBinding, error)
	// Create creates a binding (the id is generated when empty).
	Create(schema string, b *models.SvcAuthBinding) error
	// Update updates a binding (external_subject / user_id / is_active).
	Update(schema string, b *models.SvcAuthBinding) error
	// Delete deletes a binding.
	Delete(schema, id string) error
	// GetTargets reads a binding's endpoint access scope (the restrict switch + exclude-supervisor + the allowlist;
	// missing returns sql.ErrNoRows).
	GetTargets(schema, bindingID string) (restrict, excludeSupervisor bool, targets []BindingTarget, err error)
	// SetTargets replaces a binding's endpoint access scope wholesale (restrict=true with an empty list = allow none).
	// When restrict=true, excludeSupervisor is forced to false — the two settings are mutually exclusive, and the
	// allowlist already says it precisely.
	SetTargets(schema, bindingID string, restrict, excludeSupervisor bool, targets []BindingTarget) error
	// ListTargetCandidates lists the grantable candidate targets: agents / flows that have an exposed endpoint.
	ListTargetCandidates(schema string) ([]BindingTarget, error)
}
