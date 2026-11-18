package tools

import (
	"context"
	"strings"
	"testing"
)

// 全局变量清单属于管理面：分类必须落在 flow-admin，且执行层只放行超级智能体。
func TestListGlobalVarsToolIsSupervisorOnly(t *testing.T) {
	tool, ok := Get("list_global_vars")
	if !ok {
		t.Fatal("list_global_vars 未注册")
	}
	m := tool.Meta()
	if m.Category != CategoryFlowAdmin {
		t.Errorf("分类 = %q, want %q", m.Category, CategoryFlowAdmin)
	}
	if !IsSupervisorOnlyTool("list_global_vars") {
		t.Error("list_global_vars 应判定为仅超级智能体可用")
	}
	if strings.TrimSpace(m.Description) == "" {
		t.Error("描述不能为空：模型是靠它决定什么时候查的")
	}
	if m.Parameters == nil {
		t.Error("参数 schema 不能为空")
	}
}

// 非超级智能体即便硬写出工具名，也必须在执行层被拒。
func TestListGlobalVarsRejectedForNonSupervisor(t *testing.T) {
	_, err := ExecuteTool(context.Background(), &Context{
		Schema: "t0", AgentName: "poet", IsSupervisor: false,
	}, "list_global_vars", map[string]any{})
	if err == nil {
		t.Fatal("非超级智能体不该能执行 list_global_vars")
	}
	if !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("错误信息应说明是权限问题，实际: %v", err)
	}
}
