package models

import "fmt"

// LimitError means a count quota (agents / skills / flows) is exhausted.
//
// It carries the numbers instead of shipping copy: the HTTP layer turns it into a localized code (quota.agent_limit_reached
// and friends) with {used} / {limit}, and the message below is only the diagnostic text for logs and curl.
type LimitError struct {
	Kind    string // agent | skill | flow
	Current int
	Max     int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s limit reached (%d/%d): delete an unused %s first", e.Kind, e.Current, e.Max, e.Kind)
}

// Params are the copy placeholders the bundle interpolates (used by ErrCode's params argument).
//
// The code itself stays a literal at each call site (quota.agent_limit_reached / quota.skill_limit_reached /
// quota.flow_limit_reached): scripts/check-locale.ps1 only sees literals, and a computed code would silently escape the
// "every code has a bundle entry" check.
func (e *LimitError) Params() map[string]any {
	return map[string]any{"used": e.Current, "limit": e.Max}
}

// CheckQuota checks whether a resource's usage has reached its ceiling (max < 0 means unlimited, i.e. -1).
// On failure it returns a *LimitError, so callers report a quota code rather than an internal error.
//
// ⚠️ Every new "create" entry point must call this function, or that entry point is not subject to the quota.
func CheckQuota(kind string, current, max int) error {
	if max < 0 {
		return nil // -1 = unlimited
	}
	if current >= max {
		return &LimitError{Kind: kind, Current: current, Max: max}
	}
	return nil
}
