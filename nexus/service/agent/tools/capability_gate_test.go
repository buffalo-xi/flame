package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// 能力映射按**分类**划档，一档一个分类：exec / shell / script / browser。
// 拆开的是资源形态（一次性 / 常驻 / 代码片段托管执行），不是权限面 —— 四档都能执行任意代码。
func TestCapabilityOf(t *testing.T) {
	cases := map[string]string{
		CategoryExec:       capability.Exec,
		CategoryShell:      capability.Shell,
		CategoryScript:     capability.Code,
		CategoryBrowser:    capability.Browser,
		CategoryFile:       "",
		CategoryAgent:      "",
		CategorySkill:      "",
		CategorySkillOwn:   "",
		CategoryAgentAdmin: "",
		CategorySkillAdmin: "",
		CategoryToolGrant:  "",
		CategoryCustomTool: "",
		CategoryFlowAdmin:  "",
		CategorySuper:      "",
	}
	for cat, want := range cases {
		if got := CapabilityOf(cat); got != want {
			t.Errorf("CapabilityOf(%q) = %q, want %q", cat, got, want)
		}
	}
}

// 注册表里每个沙箱类工具都必须落到某档能力上。
// 这条防的是「新增一个执行类工具但忘了归类」—— 分类没写或者写了新分类，
// 闸门对它就形同虚设，而目录里照样显示为可用。
func TestEverySandboxToolMapsToCapability(t *testing.T) {
	// 豁免清单：这些分类不碰沙箱，不需要能力档。
	// 凡是没被豁免、又映射不到能力的分类，都会在这里报出来。
	nonSandbox := map[string]bool{
		CategoryFile:       true,
		CategoryAgent:      true,
		CategorySkill:      true,
		CategorySkillOwn:   true,
		CategoryAgentAdmin: true,
		CategorySkillAdmin: true,
		CategoryToolGrant:  true,
		CategoryCustomTool: true,
		CategoryFlowAdmin:  true,
		CategorySuper:      true,
	}
	for _, tool := range List() {
		m := tool.Meta()
		if m.Category == "" || nonSandbox[m.Category] {
			continue
		}
		if CapabilityOf(m.Category) == "" {
			t.Errorf("工具 %s 属于 %s 类却映射不到能力（新增沙箱类工具需补 CapabilityOf 与 capability.All）",
				m.Name, m.Category)
		}
	}
}

// 执行层硬闸：Caps 非 nil 且缺键 → 拒绝并说明是哪档能力；
// nil → 不限制（仅限无租户语义的调用方，如单测）。
//
// 必要性：能力是平台策略，可能在两次调用之间被收回；只靠目录置灰挡不住
// 已经绑定该工具的 agent（模型仍按旧工具清单发起调用）。
func TestExecuteToolHonoursCapabilities(t *testing.T) {
	Register(&stubSchemaTool{meta: ToolMeta{
		Name:       "stub_capped",
		Category:   CategoryExec,
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}})

	c := &Context{AgentName: "joker", AllowedTools: map[string]bool{"stub_capped": true}}

	c.Caps = map[string]bool{capability.Browser: true}
	_, err := ExecuteTool(context.Background(), c, "stub_capped", nil)
	if err == nil || !strings.Contains(err.Error(), capability.Exec) {
		t.Fatalf("未开通 exec 时应拒绝并指出能力名，got %v", err)
	}

	c.Caps = map[string]bool{capability.Exec: true}
	if _, err := ExecuteTool(context.Background(), c, "stub_capped", nil); err != nil {
		t.Fatalf("开通后应放行: %v", err)
	}

	c.Caps = nil
	if _, err := ExecuteTool(context.Background(), c, "stub_capped", nil); err != nil {
		t.Fatalf("Caps 为 nil 时不该拦: %v", err)
	}
}
