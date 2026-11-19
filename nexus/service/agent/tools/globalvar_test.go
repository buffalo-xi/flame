package tools

import (
	"context"
	"strings"
	"testing"
)

// The global variable list belongs to the management surface: the category must be flow-admin and the execution layer must only
// let the super agent through.
func TestListGlobalVarsToolIsSupervisorOnly(t *testing.T) {
	tool, ok := Get("list_global_vars")
	if !ok {
		t.Fatal("list_global_vars is not registered")
	}
	m := tool.Meta()
	if m.Category != CategoryFlowAdmin {
		t.Errorf("category = %q, want %q", m.Category, CategoryFlowAdmin)
	}
	if !IsSupervisorOnlyTool("list_global_vars") {
		t.Error("list_global_vars should be classified as super-agent only")
	}
	if strings.TrimSpace(m.Description) == "" {
		t.Error("the description must not be empty: the model relies on it to decide when to look things up")
	}
	if m.Parameters == nil {
		t.Error("the parameter schema must not be empty")
	}
}

// A non-super agent must be refused by the execution layer even if it writes the tool name by hand.
func TestListGlobalVarsRejectedForNonSupervisor(t *testing.T) {
	_, err := ExecuteTool(context.Background(), &Context{
		Schema: "t0", AgentName: "poet", IsSupervisor: false,
	}, "list_global_vars", map[string]any{})
	if err == nil {
		t.Fatal("a non-super agent must not be able to run list_global_vars")
	}
	if !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("the error should say it is a permission problem, got: %v", err)
	}
}
