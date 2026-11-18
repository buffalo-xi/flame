package auth

import "testing"

// 端点访问范围的三态：不限制 / 白名单命中 / 全禁。
//
// 「restrict=true + 空集合」必须是**全禁**，不能退化成不限制 —— 否则管理员
// 想关死某把 key 时反而得到全部权限，这是最危险的一种反转。
func TestAuthInfoTargetAllowed(t *testing.T) {
	t.Run("nil 接收者视为不限制", func(t *testing.T) {
		var a *AuthInfo
		if !a.TargetAllowed("agent", "x") {
			t.Error("nil AuthInfo 应放行（避免调用方到处判空）")
		}
	})

	t.Run("未启用限制时放行任意目标", func(t *testing.T) {
		a := &AuthInfo{ID: "u1"}
		if !a.TargetAllowed("agent", "x") || !a.TargetAllowed("flow", "y") {
			t.Error("RestrictTargets=false 应放行全部目标")
		}
	})

	t.Run("启用限制但白名单为空 = 全禁", func(t *testing.T) {
		a := &AuthInfo{RestrictTargets: true, TargetAllow: map[string]struct{}{}}
		if a.TargetAllowed("agent", "x") {
			t.Error("空白名单不应放行任何目标")
		}
	})

	// 「除超级智能体外」这一档：RestrictTargets=false 时只挡 supervisor 的 agent 端点，
	// 普通 agent / flow 一律放行；RestrictTargets=true 时该字段完全不参与判定。
	t.Run("排除超级智能体：默认不生效", func(t *testing.T) {
		a := &AuthInfo{ID: "u1"}
		if !a.TargetAllowedWith("agent", "a1", true) {
			t.Error("未开启排除时应放行 supervisor 端点")
		}
	})

	t.Run("排除超级智能体：挡 supervisor、放行其它", func(t *testing.T) {
		a := &AuthInfo{ExcludeSupervisor: true}
		if a.TargetAllowedWith("agent", "a1", true) {
			t.Error("supervisor 的 agent 端点应被拒绝")
		}
		if !a.TargetAllowedWith("agent", "a2", false) {
			t.Error("普通 agent 应放行")
		}
		if !a.TargetAllowedWith("flow", "f1", false) {
			t.Error("flow 应放行（supervisor 只对 agent 有意义）")
		}
	})

	t.Run("白名单档下排除开关不参与判定", func(t *testing.T) {
		a := &AuthInfo{
			RestrictTargets:   true,
			ExcludeSupervisor: true,
			TargetAllow:       map[string]struct{}{"agent:sup": {}, "agent:plain": {}},
		}
		// 白名单里显式列了 supervisor 就该放行：两档互斥，不能让残留字段把显式授权减掉
		if !a.TargetAllowedWith("agent", "sup", true) {
			t.Error("白名单显式列出的 supervisor 应放行")
		}
		if !a.TargetAllowedWith("agent", "plain", false) {
			t.Error("白名单列出的普通 agent 应放行")
		}
		if a.TargetAllowedWith("agent", "other", false) {
			t.Error("白名单外的目标仍应拒绝")
		}
	})

	t.Run("白名单只放行列出的目标", func(t *testing.T) {
		a := &AuthInfo{
			RestrictTargets: true,
			TargetAllow:     map[string]struct{}{"agent:a1": {}, "flow:f1": {}},
		}
		if !a.TargetAllowed("agent", "a1") {
			t.Error("列出的 agent 应放行")
		}
		if !a.TargetAllowed("flow", "f1") {
			t.Error("列出的 flow 应放行")
		}
		if a.TargetAllowed("agent", "a2") {
			t.Error("未列出的目标应拒绝")
		}
		// 同 id 不同类型不能串：agent:a1 不应放行 flow:a1
		if a.TargetAllowed("flow", "a1") {
			t.Error("target_type 必须参与匹配，不能让同名 id 串类型")
		}
	})
}
