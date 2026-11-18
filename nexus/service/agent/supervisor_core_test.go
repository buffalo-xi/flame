package agent

import (
	"testing"

	"flame/nexus/shared/models"
)

// core 必须给 supervisor 留下「管理入口」：建 / 改 / 删 agent + 技能绑定都在这一组里。
// 少了它，supervisor 连新 agent 都拉不起来，整个租户失去自举能力。
//
// 注意这条与曾经的「迁移标志」不是一回事：那个判的是「core 回填跑过没有」
// （已随存量迁移完成删除），这个判的是「自举能力还在不在」，长期有效。
func TestSupervisorCoreKeepsBootstrapAbility(t *testing.T) {
	const want = models.GrantRefBuiltinPrefix + "agent-admin"
	for _, ref := range models.SupervisorCoreGrantRefs {
		if ref == want {
			return
		}
	}
	t.Errorf("SupervisorCoreGrantRefs 必须含 %s（supervisor 的自举能力）", want)
}
