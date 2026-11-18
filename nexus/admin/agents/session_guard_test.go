package agents

import (
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// 「读历史消息 / 删会话」的可见性口径：按 user 收口，与 ListSessions 对齐。
func TestSessionVisibleTo(t *testing.T) {
	ai := &auth.AuthInfo{ID: "user-a"}

	cases := []struct {
		name  string
		sess  *models.Session
		ai    *auth.AuthInfo
		allow bool
	}{
		{"nil 会话", nil, ai, false},
		{"nil 身份", &models.Session{UserID: "user-a"}, nil, false},
		{"自己的会话", &models.Session{UserID: "user-a"}, ai, true},
		{"别人的会话", &models.Session{UserID: "user-b"}, ai, false},
		// 无归属用户的租户级会话没有「别人」可言，放行
		{"租户级会话", &models.Session{UserID: ""}, ai, true},
		// 身份为空（理论上进不来，防御性判否 —— 否则空 user 会匹配空会话
		{"身份为空", &models.Session{UserID: "user-a"}, &auth.AuthInfo{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionVisibleTo(c.sess, c.ai); got != c.allow {
				t.Errorf("sessionVisibleTo = %v, want %v", got, c.allow)
			}
		})
	}
}
