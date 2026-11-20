package auth

import "testing"

// The three states of an endpoint access scope: unrestricted / allowlist hit / everything denied.
//
// "restrict=true with an empty set" must mean **deny everything** and must never degrade into "unrestricted" — otherwise an
// administrator trying to shut a key down would instead grant it full access, which is the most dangerous possible
// inversion.
func TestAuthInfoTargetAllowed(t *testing.T) {
	t.Run("a nil receiver counts as unrestricted", func(t *testing.T) {
		var a *AuthInfo
		if !a.TargetAllowed("agent", "x") {
			t.Error("a nil AuthInfo should let the call through (so callers need no nil checks everywhere)")
		}
	})

	t.Run("with no restriction any target is allowed", func(t *testing.T) {
		a := &AuthInfo{ID: "u1"}
		if !a.TargetAllowed("agent", "x") || !a.TargetAllowed("flow", "y") {
			t.Error("RestrictTargets=false should allow every target")
		}
	})

	t.Run("restricted with an empty allowlist = deny everything", func(t *testing.T) {
		a := &AuthInfo{RestrictTargets: true, TargetAllow: map[string]struct{}{}}
		if a.TargetAllowed("agent", "x") {
			t.Error("an empty allowlist must not allow any target")
		}
	})

	// The "everyone except the super agent" setting: with RestrictTargets=false it only blocks a supervisor's agent
	// endpoints, while ordinary agents / flows are all allowed; with RestrictTargets=true the field takes no part at all.
	t.Run("exclude supervisor: off by default", func(t *testing.T) {
		a := &AuthInfo{ID: "u1"}
		if !a.TargetAllowedWith("agent", "a1", true) {
			t.Error("with the exclusion off a supervisor endpoint should be allowed")
		}
	})

	t.Run("exclude supervisor: blocks the supervisor, allows the rest", func(t *testing.T) {
		a := &AuthInfo{ExcludeSupervisor: true}
		if a.TargetAllowedWith("agent", "a1", true) {
			t.Error("a supervisor's agent endpoint should be refused")
		}
		if !a.TargetAllowedWith("agent", "a2", false) {
			t.Error("an ordinary agent should be allowed")
		}
		if !a.TargetAllowedWith("flow", "f1", false) {
			t.Error("a flow should be allowed (a supervisor is only meaningful for agents)")
		}
	})

	t.Run("in allowlist mode the exclusion switch takes no part", func(t *testing.T) {
		a := &AuthInfo{
			RestrictTargets:   true,
			ExcludeSupervisor: true,
			TargetAllow:       map[string]struct{}{"agent:sup": {}, "agent:plain": {}},
		}
		// A supervisor explicitly listed in the allowlist must be allowed: the two settings are mutually exclusive, and a
		// leftover field must not subtract from an explicit grant
		if !a.TargetAllowedWith("agent", "sup", true) {
			t.Error("a supervisor explicitly listed in the allowlist should be allowed")
		}
		if !a.TargetAllowedWith("agent", "plain", false) {
			t.Error("an ordinary agent listed in the allowlist should be allowed")
		}
		if a.TargetAllowedWith("agent", "other", false) {
			t.Error("a target outside the allowlist should still be refused")
		}
	})

	t.Run("the allowlist allows only the listed targets", func(t *testing.T) {
		a := &AuthInfo{
			RestrictTargets: true,
			TargetAllow:     map[string]struct{}{"agent:a1": {}, "flow:f1": {}},
		}
		if !a.TargetAllowed("agent", "a1") {
			t.Error("a listed agent should be allowed")
		}
		if !a.TargetAllowed("flow", "f1") {
			t.Error("a listed flow should be allowed")
		}
		if a.TargetAllowed("agent", "a2") {
			t.Error("an unlisted target should be refused")
		}
		// The same id under a different type must not leak: agent:a1 must not allow flow:a1
		if a.TargetAllowed("flow", "a1") {
			t.Error("target_type must take part in matching, so an id cannot leak across types")
		}
	})
}
