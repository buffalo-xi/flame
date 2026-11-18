package toolgrant

import (
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/capability"
)

// 能力闸必须落在叶子清单上（builtinTools）：超级智能体的合并、Resolve 的可用性过滤、
// 目录接口都从这里取工具。漏在这一层，就会出现「组显示未开通、生效工具说可用」。
func TestBuiltinToolsCapabilityGate(t *testing.T) {
	c := &Catalog{caps: map[string]bool{capability.Browser: true}}
	var blocked, allowed int
	for _, ti := range c.builtinTools() {
		switch tools.CapabilityOf(builtinCategory(ti)) {
		case capability.Browser:
			allowed++
			if !ti.Available {
				t.Errorf("%s 的能力已开通，却标为不可用", ti.Ref)
			}
			if ti.UnavailableKind != "" || ti.UnavailableReason != "" {
				t.Errorf("%s 可用时不该带归因：kind=%q reason=%q", ti.Ref, ti.UnavailableKind, ti.UnavailableReason)
			}
		case capability.Exec:
			blocked++
			if ti.Available {
				t.Errorf("%s 的能力未开通，却仍是可用", ti.Ref)
			}
			if ti.UnavailableKind != UnavailableCapability {
				t.Errorf("%s 的归因 = %q, want %q", ti.Ref, ti.UnavailableKind, UnavailableCapability)
			}
			if ti.UnavailableReason == "" {
				t.Errorf("%s 缺少不可用原因", ti.Ref)
			}
		}
	}
	if blocked == 0 || allowed == 0 {
		t.Fatalf("测试前提不成立：注册表里没有 exec/browser 工具（exec=%d browser=%d）", blocked, allowed)
	}
}

// nil 视图 = 不拦（仅无租户语义的调用方；运行时恒为非 nil）。
func TestBuiltinToolsWithoutCapabilityView(t *testing.T) {
	c := &Catalog{}
	for _, ti := range c.builtinTools() {
		if !ti.Available {
			t.Errorf("%s 不该被拦（未注入能力视图）", ti.Ref)
		}
	}
}

// 每个内置工具的分类都必须在 BuiltinCategories 里，否则 Groups() 不产出该组、
// 工具在授权目录里直接消失（shell / script 拆档时最容易漏这一步）。
func TestBuiltinCategoriesCoverAllTools(t *testing.T) {
	cats := BuiltinCategories()
	for _, tl := range tools.List() {
		m := tl.Meta()
		if !cats[m.Category] {
			t.Errorf("工具 %s 的分类 %q 不在 BuiltinCategories 里，授权目录里会看不见它", m.Name, m.Category)
		}
	}
}

// 视角过滤与能力闸同时生效：非超级智能体看不到 super 类，能力仍照标。
func TestBuiltinToolsSupervisorOnlyFilter(t *testing.T) {
	c := &Catalog{caps: map[string]bool{}}
	for _, ti := range c.builtinTools() {
		if ti.SupervisorOnly {
			t.Errorf("非超级智能体视角不该出现 %s", ti.Ref)
		}
	}

	sup := &Catalog{isSupervisor: true, caps: map[string]bool{}}
	var superSeen bool
	for _, ti := range sup.builtinTools() {
		if ti.SupervisorOnly {
			superSeen = true
		}
	}
	if !superSeen {
		t.Fatal("supervisor 视角应能看到 super 类工具")
	}
}
