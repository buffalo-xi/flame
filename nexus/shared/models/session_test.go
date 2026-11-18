package models

import "testing"

// 创建者判据：只回答「这个会话是谁建的」（记录归属 / 展示 / 审计），
// 不再承担访问控制 —— 那是 VisibleToUser 的职责。
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
		{"同一 agent", agentSess, SessionOwnerTypeAgent, "agent-1", true},
		{"别的 agent", agentSess, SessionOwnerTypeAgent, "agent-2", false},
		{"类型不符（flow 会话被 agent 端点拿到）", agentSess, SessionOwnerTypeFlow, "agent-1", false},
		{
			"会话不存在",
			nil, SessionOwnerTypeAgent, "agent-1", false,
		},
		{
			// 空 ownerID 绝不能当通配符：会话行里 owner_id 非空，
			// 但调用方缺 id 时（装配错误）必须判 false，而不是"恰好匹配"
			"调用方没给 id",
			&Session{OwnerType: SessionOwnerTypeAgent, OwnerID: ""},
			SessionOwnerTypeAgent, "", false,
		},
	}
	for _, c := range cases {
		if got := c.sess.OwnedBy(c.otype, c.oid); got != c.want {
			t.Errorf("%s: OwnedBy(%q,%q) = %v, 期望 %v", c.name, c.otype, c.oid, got, c.want)
		}
	}
}

// 访问控制判据：三个入口（HTTP chat / MCP / RPC）共用它，错一次的后果是
// 「凭一个 sid 就能读写别人会话的工作区」或「父子 agent 共用会话被整体拒掉」。
func TestSessionVisibleToUser(t *testing.T) {
	userSess := &Session{ID: "s1", OwnerType: SessionOwnerTypeAgent, OwnerID: "agent-1", UserID: "user-1"}
	cases := []struct {
		name   string
		sess   *Session
		userID string
		want   bool
	}{
		{"同一个用户", userSess, "user-1", true},
		{"别的用户", userSess, "user-2", false},
		// 父子 agent 共用会话：owner 不同但 user 相同，必须放行
		{"同一用户的另一个 agent 创建的会话", &Session{UserID: "user-1", OwnerID: "agent-2"}, "user-1", true},
		{"租户级会话（user_id 为空）", &Session{ID: "s1", OwnerID: "agent-1"}, "user-1", true},
		{"租户级会话但身份缺失", &Session{ID: "s1"}, "", false},
		{"会话不存在", nil, "user-1", false},
		{"身份缺失", userSess, "", false},
	}
	for _, c := range cases {
		if got := c.sess.VisibleToUser(c.userID); got != c.want {
			t.Errorf("%s: VisibleToUser(%q) = %v, 期望 %v", c.name, c.userID, got, c.want)
		}
	}
}
