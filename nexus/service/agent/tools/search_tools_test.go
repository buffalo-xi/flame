package tools

import (
	"context"
	"strings"
	"testing"
)

// stubCatalog 记录 Execute 透传给管理器的东西。
type stubCatalog struct {
	query, agentName, agentID string
}

func (s *stubCatalog) Search(_ context.Context, query, agentName, agentID string) (*ToolSearchResult, error) {
	s.query, s.agentName, s.agentID = query, agentName, agentID
	return &ToolSearchResult{Query: query}, nil
}

// 没有注入目录管理器时必须是明确错误，而不是空结果 —— 空结果会让模型以为
// 「本租户没有可授权的东西」，比报错更误导。
func TestSearchToolsWithoutCatalog(t *testing.T) {
	_, err := (&searchToolsTool{}).Execute(context.Background(), &Context{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

// 不传 agent 时按调用者自己的身份查（含回填自己的授权状态）。
func TestSearchToolsDefaultsToSelf(t *testing.T) {
	stub := &stubCatalog{}
	c := &Context{AgentName: "main", AgentID: "uuid-main", ToolCatalog: stub}

	res, err := (&searchToolsTool{}).Execute(context.Background(), c, map[string]any{"query": "fs"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil {
		t.Fatal("结果为空")
	}
	if stub.query != "fs" || stub.agentName != "main" || stub.agentID != "uuid-main" {
		t.Fatalf("透传 = (%q, %q, %q)", stub.query, stub.agentName, stub.agentID)
	}
}

// search_tools 属于 super 分类：执行层硬闸只放行超级智能体。
func TestSearchToolsIsSupervisorOnly(t *testing.T) {
	if !IsSupervisorOnlyTool("search_tools") {
		t.Fatal("search_tools 应为超级智能体专属")
	}
	_, err := ExecuteTool(context.Background(), &Context{
		AgentName:    "poet",
		ToolCatalog:  &stubCatalog{},
		AllowedTools: map[string]bool{"search_tools": true},
	}, "search_tools", nil)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("非超级智能体调用应被拒，实际 %v", err)
	}
}
