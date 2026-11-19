package agents

import (
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// Visibility for "read history / delete session" is scoped by user, matching ListSessions.
func TestSessionVisibleTo(t *testing.T) {
	ai := &auth.AuthInfo{ID: "user-a"}

	cases := []struct {
		name  string
		sess  *models.Session
		ai    *auth.AuthInfo
		allow bool
	}{
		{"nil session", nil, ai, false},
		{"nil identity", &models.Session{UserID: "user-a"}, nil, false},
		{"own session", &models.Session{UserID: "user-a"}, ai, true},
		{"someone else's session", &models.Session{UserID: "user-b"}, ai, false},
		// A tenant-wide session has no owner, so there is no "someone else" - allow.
		{"tenant-wide session", &models.Session{UserID: ""}, ai, true},
		// Empty identity (cannot happen in theory, denied defensively - an empty user would otherwise match an empty session
		{"empty identity", &models.Session{UserID: "user-a"}, &auth.AuthInfo{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionVisibleTo(c.sess, c.ai); got != c.allow {
				t.Errorf("sessionVisibleTo = %v, want %v", got, c.allow)
			}
		})
	}
}
