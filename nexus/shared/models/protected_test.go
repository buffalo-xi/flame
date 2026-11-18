package models

import "testing"

// 身份判据（is_supervisor 属性）与保护名单（predefined 资产）正交，
// 但两者在「main 能不能降级」这一格上叠加：main 是预置入口，属性必须恒定。
func TestCanDemoteSupervisor(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"main", false}, // 预置 supervisor：不可降级（否则留一个占着入口却没有权限的死角）
		{"poet", true},  // 普通 supervisor：可降级，只受「最后一个」约束
		{"", true},      // 空名不属名单，交给上层校验
		{"Main", true},  // 名单大小写敏感，与 IsProtectedAgent 保持一致
	}
	for _, tc := range cases {
		if got := CanDemoteSupervisor(tc.name); got != tc.want {
			t.Errorf("CanDemoteSupervisor(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 保护名单与身份判据必须各管各的：给名单加名字不会让它变成 supervisor，
// 给 agent 打开 is_supervisor 也不会让它不可删。
func TestProtectedIsNotSupervisor(t *testing.T) {
	if !IsProtectedAgent("main") {
		t.Fatal("main 应在保护名单里（不可删除 / 不可改名）")
	}
	if IsProtectedAgent("poet") {
		t.Fatal("普通 agent 不该进保护名单")
	}
	// supervisor 判据只看实体字段
	if IsSupervisor(&Agent{Name: "poet", IsSupervisor: true}) != true {
		t.Error("带属性的 agent 应判定为 supervisor")
	}
	if IsSupervisor(&Agent{Name: "main"}) {
		t.Error("没有属性的实体不是 supervisor —— 判据不按名字回退")
	}
	if IsSupervisor(nil) {
		t.Error("nil 应安全返回 false")
	}
}

// supervisor 起步技能必须都是「不许删 / 不许改名」的基石技能：
// 授予时自动绑定的那几份说明书，如果本身可被删掉，新 supervisor 的自举路径就断在
// 「谁都能删掉它赖以自举的技能」上。
func TestSupervisorSeedSkillsAreProtected(t *testing.T) {
	if len(SupervisorSeedSkills) == 0 {
		t.Fatal("SupervisorSeedSkills 为空")
	}
	for _, name := range SupervisorSeedSkills {
		if !IsProtectedSkill(name) {
			t.Errorf("%q 是 supervisor 起步技能，却不在受保护名单里（可被删除 / 改名）", name)
		}
	}
}
