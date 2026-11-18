package agent

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// local__ 只归本地端点执行器管，与 mcp__ / rpc__ 前缀不重叠。
func TestLocalEndpointRunnerHas(t *testing.T) {
	r := &localEndpointRunner{}
	for _, name := range []string{"local__daily-report", "local__x"} {
		if !r.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"mcp__fs__read_file", "rpc__erp__create_order", "read_file", "custom__x"} {
		if r.Has(name) {
			t.Errorf("Has(%q) = true, want false", name)
		}
	}
}

// 缺租户上下文必须直接报错：没有 schema 就无法定位端点所在的租户表。
func TestLocalEndpointRunnerRequiresSchema(t *testing.T) {
	_, err := (&localEndpointRunner{}).Invoke(context.Background(), tools.LocalCall{ToolName: "local__daily-report"})
	if err == nil || !strings.Contains(err.Error(), "租户上下文") {
		t.Fatalf("期望租户上下文错误，实际 %v", err)
	}
}
