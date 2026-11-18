package pluginpolicy

import "testing"

// 缺省必须是开放（2026-11-02 产品决定）：插件本体是平台放进磁盘的可信代码，
// 凭证与成本都在租户侧（{schema}.plugins.configs 里是租户自己的 key），
// 逐租户开通只是运营负担。要收紧就写一行 is_allowed=false。
//
// 注意：Policy 的**零值**仍然是 Allowed=false —— 缺省语义只由 Default() 表达，
// 读表拿不到行时必须显式调 Default()，不能依赖零值。这条也是防止将来有人
// 把 `map[name]` 查不到时的零值直接当策略用、悄悄把缺省变回拒绝（或反过来）。
func TestDefaultAllows(t *testing.T) {
	p := Default()
	if !p.Allowed {
		t.Fatalf("缺省策略应为「开放」，实际 Allowed=false")
	}
	if p.Blocked {
		t.Fatalf("缺省不应被全局闸拦下")
	}

	var zero Policy
	if zero.Allowed {
		t.Fatalf("Policy 零值应保持 false（缺省语义只由 Default() 表达）")
	}
}
