package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
)

// The capability mapping is graded by **category**, one category per tier: exec / shell / script / browser.
// What it separates is the resource shape (one-shot / resident / hosted snippet execution), not the permission surface - all four
// can execute arbitrary code.
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

// Every sandbox tool in the registry must map to some capability tier.
// This guards against "an execution tool was added but never classified" - with no category or a new one, the gate is a no-op for
// it while the catalog still shows it as available.
func TestEverySandboxToolMapsToCapability(t *testing.T) {
	// The exemption list: these categories never touch the sandbox and need no capability tier.
	// Anything not exempt here that maps to no capability is reported.
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
			t.Errorf("tool %s is in category %s but maps to no capability (a new sandbox tool needs CapabilityOf and capability.All)",
				m.Name, m.Category)
		}
	}
}

// The execution-layer hard gate: Caps non-nil and missing the key -> refused, naming the tier;
// nil -> unrestricted (only for callers with no tenant semantics, such as unit tests).
//
// Why it is needed: capabilities are platform policy and can be revoked between two calls; greying out the catalog alone cannot
// stop an agent that already has the tool bound (the model still calls it from its old tool list).
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
		t.Fatalf("without exec granted it should refuse and name the capability, got %v", err)
	}

	c.Caps = map[string]bool{capability.Exec: true}
	if _, err := ExecuteTool(context.Background(), c, "stub_capped", nil); err != nil {
		t.Fatalf("it should be allowed once granted: %v", err)
	}

	c.Caps = nil
	if _, err := ExecuteTool(context.Background(), c, "stub_capped", nil); err != nil {
		t.Fatalf("a nil Caps must not block: %v", err)
	}
}
