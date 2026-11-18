package handler

import (
	"testing"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
)

// 三态必须分得清：null（没设过 = 未开通）与 false（显式关闭）不是一个意思。
// 压成一个 bool 会让编辑框把「未设置」静默写成「关闭」，管理员再也分不清
// 「我关的」和「本来就没开」。
func TestBuildCapabilityViews(t *testing.T) {
	on := capabilitypolicy.Policy{Allowed: true}
	off := capabilitypolicy.Policy{Allowed: false}

	// 1) 都没配：未开通（缺省拒绝）
	got := buildCapabilityViews(nil, nil)
	if len(got) != len(capability.All()) {
		t.Fatalf("应列出全部已知能力，got %d", len(got))
	}
	for _, v := range got {
		if v.Allowed || v.Row != nil || v.Blocked {
			t.Errorf("%s 无任何配置时应为「未开通 + 无行 + 不拦」，got %+v", v.Key, v)
		}
	}

	// 2) 租户行 true：开通；Row 原样回填给编辑框
	got = buildCapabilityViews(map[string]capabilitypolicy.Policy{capability.Browser: on}, nil)
	for _, v := range got {
		switch v.Key {
		case capability.Browser:
			if !v.Allowed || v.Row == nil || !*v.Row || v.Blocked {
				t.Errorf("browser 有允许行应为「开通 + Row=true」，got %+v", v)
			}
		default:
			if v.Allowed {
				t.Errorf("%s 没配就不该开通，got %+v", v.Key, v)
			}
		}
	}

	// 3) 显式关闭：Allowed=false 但 Row 非 nil（编辑框要能回填「关」而不是「未设置」）
	got = buildCapabilityViews(map[string]capabilitypolicy.Policy{capability.Browser: off}, nil)
	for _, v := range got {
		if v.Key != capability.Browser {
			continue
		}
		if v.Allowed {
			t.Error("显式关闭不应生效为开通")
		}
		if v.Row == nil || *v.Row {
			t.Error("Row 应原样反映本租户的 false（不能被缺省值抹平）")
		}
	}

	// 4) 全局闸压过租户的显式开启，但不改写原始行 —— 否则界面会显示「租户关了」，
	//    管理员解闸后发现能力又回来了，排查方向会被带偏。
	got = buildCapabilityViews(
		map[string]capabilitypolicy.Policy{capability.Browser: on},
		map[string]bool{capability.Browser: true},
	)
	for _, v := range got {
		if v.Key != capability.Browser {
			continue
		}
		if v.Allowed {
			t.Error("全局闸 blocked 应压过租户行 true")
		}
		if !v.Blocked {
			t.Error("Blocked 应如实反映闸状态（界面要标出「被平台拦下」）")
		}
		if v.Row == nil || !*v.Row {
			t.Error("Row 必须仍是租户原始的 true（硬闸不修改原始配置）")
		}
	}
}

// 依赖只对 net.egress 生效，且是「exec 或 shell 任一」—— 两个通道都能执行任意
// 代码，只认 exec 会把「只开 shell」的租户挡在门外。其余键都是独立的，不许凭空长出依赖。
func TestCapabilityDeps(t *testing.T) {
	deps := capabilityDeps(capability.Egress)
	if len(deps) != 2 || deps[0] != capability.Exec || deps[1] != capability.Shell {
		t.Errorf("capabilityDeps(%q) = %v，want [%q %q]",
			capability.Egress, deps, capability.Exec, capability.Shell)
	}
	for _, key := range capability.All() {
		if key == capability.Egress {
			continue
		}
		if got := capabilityDeps(key); len(got) != 0 {
			t.Errorf("capabilityDeps(%q) = %v，want 无依赖", key, got)
		}
	}
}

// 新增键必须同时进 All / Valid / Label 三处 —— 漏一处就会出现「界面不列出」
// 或「接口拒收」这类只在一半地方生效的偏差，而且都不会报错。
func TestEgressKeyRegistered(t *testing.T) {
	if !capability.Valid(capability.Egress) {
		t.Error("net.egress 必须是合法键（否则 PUT 直接拒绝）")
	}
	if capability.Label(capability.Egress) == capability.Egress {
		t.Error("net.egress 缺 Label（界面会显示裸键名）")
	}
	var listed bool
	for _, key := range capability.All() {
		if key == capability.Egress {
			listed = true
		}
	}
	if !listed {
		t.Error("net.egress 不在 All 里（能力授权卡片不会列出这一行）")
	}
}
