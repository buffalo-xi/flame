package tools

import (
	"context"
	"strings"
	"testing"
)

// stubCatalog records what Execute forwards to the manager.
type stubCatalog struct {
	query, agentName, agentID string
}

func (s *stubCatalog) Search(_ context.Context, query, agentName, agentID string) (*ToolSearchResult, error) {
	s.query, s.agentName, s.agentID = query, agentName, agentID
	return &ToolSearchResult{Query: query}, nil
}

// Without a catalog manager injected the result must be an explicit error, not an empty result - an empty result would make the
// model believe "this tenant has nothing grantable", which misleads more than an error.
func TestSearchToolsWithoutCatalog(t *testing.T) {
	_, err := (&searchToolsTool{}).Execute(context.Background(), &Context{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

// Without an agent argument the caller's own identity is used (including backfilling its own grant state).
func TestSearchToolsDefaultsToSelf(t *testing.T) {
	stub := &stubCatalog{}
	c := &Context{AgentName: "main", AgentID: "uuid-main", ToolCatalog: stub}

	res, err := (&searchToolsTool{}).Execute(context.Background(), c, map[string]any{"query": "fs"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil {
		t.Fatal("result is nil")
	}
	if stub.query != "fs" || stub.agentName != "main" || stub.agentID != "uuid-main" {
		t.Fatalf("forwarded = (%q, %q, %q)", stub.query, stub.agentName, stub.agentID)
	}
}

// search_tools is a super category: the execution-layer hard gate only lets the super agent through.
func TestSearchToolsIsSupervisorOnly(t *testing.T) {
	if !IsSupervisorOnlyTool("search_tools") {
		t.Fatal("search_tools should be super-agent only")
	}
	_, err := ExecuteTool(context.Background(), &Context{
		AgentName:    "poet",
		ToolCatalog:  &stubCatalog{},
		AllowedTools: map[string]bool{"search_tools": true},
	}, "search_tools", nil)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("a non-super agent call should be refused, got %v", err)
	}
}
