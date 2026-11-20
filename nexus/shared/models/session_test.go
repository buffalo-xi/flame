package models

import "testing"

// The creator criterion only answers "who created this session" (record ownership / display / auditing) and no longer
// carries access control — that is VisibleToUser's job.
func TestSessionOwnedBy(t *testing.T) {
	agentSess := &Session{
		ID:        "s1",
		OwnerType: SessionOwnerTypeAgent,
		OwnerID:   "agent-1",
		UserID:    "user-1",
	}
	cases := []struct {
		name  string
		sess  *Session
		otype string
		oid   string
		want  bool
	}{
		{"the same agent", agentSess, SessionOwnerTypeAgent, "agent-1", true},
		{"another agent", agentSess, SessionOwnerTypeAgent, "agent-2", false},
		{"type mismatch (a flow session reached through an agent endpoint)", agentSess, SessionOwnerTypeFlow, "agent-1", false},
		{
			"the session does not exist",
			nil, SessionOwnerTypeAgent, "agent-1", false,
		},
		{
			// An empty ownerID must never act as a wildcard: owner_id in a session row is non-empty, but when the caller
			// is missing the id (a wiring bug) it has to answer false rather than "happens to match"
			"the caller gave no id",
			&Session{OwnerType: SessionOwnerTypeAgent, OwnerID: ""},
			SessionOwnerTypeAgent, "", false,
		},
	}
	for _, c := range cases {
		if got := c.sess.OwnedBy(c.otype, c.oid); got != c.want {
			t.Errorf("%s: OwnedBy(%q,%q) = %v, want %v", c.name, c.otype, c.oid, got, c.want)
		}
	}
}

// The access control criterion is shared by the three entries (HTTP chat / MCP / RPC); getting it wrong once means
// "one sid is enough to read and write someone else's session workspace" or "a parent and child agent sharing a session
// are rejected wholesale".
func TestSessionVisibleToUser(t *testing.T) {
	userSess := &Session{ID: "s1", OwnerType: SessionOwnerTypeAgent, OwnerID: "agent-1", UserID: "user-1"}
	cases := []struct {
		name   string
		sess   *Session
		userID string
		want   bool
	}{
		{"the same user", userSess, "user-1", true},
		{"another user", userSess, "user-2", false},
		// A parent and child agent share a session: different owners, same user, must be allowed
		{"a session created by another agent of the same user", &Session{UserID: "user-1", OwnerID: "agent-2"}, "user-1", true},
		{"a tenant-level session (empty user_id)", &Session{ID: "s1", OwnerID: "agent-1"}, "user-1", true},
		{"a tenant-level session but no identity", &Session{ID: "s1"}, "", false},
		{"the session does not exist", nil, "user-1", false},
		{"no identity", userSess, "", false},
	}
	for _, c := range cases {
		if got := c.sess.VisibleToUser(c.userID); got != c.want {
			t.Errorf("%s: VisibleToUser(%q) = %v, want %v", c.name, c.userID, got, c.want)
		}
	}
}
