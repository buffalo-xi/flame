package auth

import "flame/nexus/shared/account"

// User roles (the <schema>.users.role column):
// they only decide "may it log into the console / call the admin API", and have nothing to do with service credentials
// (a credential does not restrict roles).
const (
	RoleManage  = "manage"  // management user: may log in
	RoleService = "service" // service user: may not log in (an API call identity only)
)

// User statuses (the <schema>.users.status column):
// whether the account is usable — both login and API calls look at it.
// The vocabulary's source of truth is the neutral package shared/account (platform.users.status uses the same set); this
// only re-exports it.
const (
	UserStatusActive   = account.StatusActive
	UserStatusDisabled = account.StatusDisabled
	UserStatusLocked   = account.StatusLocked
)

// AuthInfo is the authentication information.
//
// Note: it deliberately carries no role — a role is judged only at the "login" gate (login.go), and the only token
// issuer right now is /login, so an admin API needs no second role check.
// On the service side (MCP/RPC) AuthInfo comes from a resolved binding, whose ID is the bound user, and no role is needed
// there either.
type AuthInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Schema string `json:"schema"`

	// RestrictTargets / TargetAllow only mean something for a service-side (MCP / RPC) binding identity:
	// they restrict which **endpoint targets** (agent / flow) this credential may call.
	//
	//   RestrictTargets=false          -> unrestricted (must be turned off explicitly; existing bindings stay false after
	//                                     the migration)
	//   RestrictTargets=true, empty    -> nothing is allowed (the initial state of a new binding)
	//   RestrictTargets=true, non-empty-> only the listed targets are allowed
	//
	// The grant is per target rather than per endpoint: one agent usually has both an rpc and an mcp endpoint, and
	// granting per endpoint would produce "only rpc was ticked, so the MCP connection is empty" and "the grant is void
	// after the endpoint is retracted and reopened".
	//
	// json:"-": these two fields are runtime policy and must not appear in an API response or be serialised into a token.
	RestrictTargets bool                `json:"-"`
	TargetAllow     map[string]struct{} `json:"-"`

	// ExcludeSupervisor is the middle setting of "unrestricted": with RestrictTargets=false, every endpoint is allowed but
	// the super agent's endpoints are excluded (a supervisor's endpoint equals opening the tenant's management capability
	// to the outside).
	// It is ignored when RestrictTargets=true — the allowlist already states the scope precisely.
	ExcludeSupervisor bool `json:"-"`
}

// TargetAllowed reports whether an endpoint target is inside the allowed scope.
//
// A nil receiver / a disabled restriction is always true — the caller (the rpc / mcp wiring layer) need not nil-check.
func (a *AuthInfo) TargetAllowed(targetType, targetID string) bool {
	if a == nil || !a.RestrictTargets {
		return true
	}
	_, ok := a.TargetAllow[targetType+":"+targetID]
	return ok
}

// TargetAllowedWith layers "exclude the super agent" on top of TargetAllowed.
//
// supervisor is carried out from the endpoint row by the caller (models.SvcEndpoint.TargetSupervisor); it is not looked up
// here: rpc's findEndpoint runs on every tool call, and querying agents again would double the hot path.
func (a *AuthInfo) TargetAllowedWith(targetType, targetID string, supervisor bool) bool {
	if a == nil {
		return true
	}
	if !a.TargetAllowed(targetType, targetID) {
		return false
	}
	// The allowlist already states the scope precisely, so the exclusion setting takes no part (even if someone wrote both
	// columns as true by hand in the database)
	if a.RestrictTargets {
		return true
	}
	return !(a.ExcludeSupervisor && targetType == "agent" && supervisor)
}
