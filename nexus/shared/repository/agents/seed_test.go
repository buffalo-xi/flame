package repository

import (
	"testing"

	"flame/nexus/shared/models"
)

// 「未定制」的判据必须是严格相等。
//
// 授予 supervisor 时会用它决定「要不要把现有 required 行整批删掉换成 core 组」。
// 宽松成子集会把管理员手工删减过的授权也一并覆盖 —— 那是丢配置，不是升级。
func TestIsDefaultAgentSeedOnly(t *testing.T) {
	seed := models.DefaultAgentToolGrants()
	refs := make([]string, 0, len(seed))
	for _, it := range seed {
		refs = append(refs, it.Ref)
	}

	if !isDefaultAgentSeedOnly(refs) {
		t.Fatal("恰好等于默认种子时应判定为未定制")
	}

	if isDefaultAgentSeedOnly(refs[:len(refs)-1]) {
		t.Error("少一条不该判为未定制（那可能是管理员手工删减）")
	}

	more := append(append([]string{}, refs...), models.GrantRefBuiltinPrefix+"exec")
	if isDefaultAgentSeedOnly(more) {
		t.Error("多一条不该判为未定制")
	}

	if isDefaultAgentSeedOnly(nil) {
		t.Error("空授权不是「等于种子」，是「尚未配置」")
	}

	// core 组（6 条）与默认种子（12 条单工具）形状完全不同，不能被误判。
	core := make([]string, 0, len(models.SupervisorCoreGrantRefs))
	core = append(core, models.SupervisorCoreGrantRefs...)
	if isDefaultAgentSeedOnly(core) {
		t.Error("core 组不该判为默认种子")
	}
}
