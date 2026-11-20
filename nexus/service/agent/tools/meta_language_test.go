package tools

import (
	"context"
	"strings"
	"testing"
)

// langStubAgents records the SetOutputLanguage call; the rest of AgentManager falls back to the embedded nil interface
// (a call panics), so a case cannot quietly rely on unstubbed behaviour.
type langStubAgents struct {
	AgentManager
	called bool
	gotID  string
	gotTag string
}

func (s *langStubAgents) Update(_ context.Context, name, newName, _ string) (string, error) {
	if newName != "" {
		return newName, nil
	}
	return name, nil
}

func (s *langStubAgents) Get(_ context.Context, name string) (*AgentInfo, error) {
	return &AgentInfo{ID: "aid", Name: name, Status: 1}, nil
}

func (s *langStubAgents) SetOutputLanguage(_ context.Context, agentID, tag string) error {
	s.called, s.gotID, s.gotTag = true, agentID, tag
	return nil
}

// TestUpdateAgentDefaultLanguage: the value is validated and stored in agents.default_language, and the echo is the tag
// that was actually stored (trimmed), not the raw input.
func TestUpdateAgentDefaultLanguage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zh-CN", "zh-CN"},
		{"  zh-CN  ", "zh-CN"}, // trimmed
		{"ja", "ja"},
		{"", ""}, // clears
	}
	for _, tc := range cases {
		stub := &langStubAgents{}
		c := &Context{Agents: stub, AgentName: "poet"}
		out, err := (&updateAgentTool{}).Execute(context.Background(), c,
			map[string]any{"name": "poet", "default_language": tc.in})
		if err != nil {
			t.Fatalf("default_language=%q: %v", tc.in, err)
		}
		if !stub.called || stub.gotID != "aid" {
			t.Fatalf("default_language=%q: the setter was not called with the agent id: %+v", tc.in, stub)
		}
		if stub.gotTag != tc.want {
			t.Errorf("default_language=%q stored %q, want %q", tc.in, stub.gotTag, tc.want)
		}
		if got, _ := out.(map[string]any)["default_language"]; got != tc.want {
			t.Errorf("default_language=%q echoed %v, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUpdateAgentDefaultLanguageRejected: a malformed tag is an error, not a silent no-op. The runtime reads an
// unrecognized value as "not configured", so accepting it would look like the setting had been applied and done nothing.
func TestUpdateAgentDefaultLanguageRejected(t *testing.T) {
	stub := &langStubAgents{}
	c := &Context{Agents: stub, AgentName: "poet"}
	_, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "default_language": "answer in Chinese"})
	if err == nil || !strings.Contains(err.Error(), "not a valid BCP-47") {
		t.Fatalf("err = %v, want one naming the invalid tag", err)
	}
	if stub.called {
		t.Fatal("a rejected tag must not reach the setter")
	}
}

// TestUpdateAgentWithoutLanguageLeavesItAlone: omitting the argument must not trigger a write (otherwise every rename
// would clear the configured language).
func TestUpdateAgentWithoutLanguageLeavesItAlone(t *testing.T) {
	stub := &langStubAgents{}
	c := &Context{Agents: stub, AgentName: "poet"}
	if _, err := (&updateAgentTool{}).Execute(context.Background(), c,
		map[string]any{"name": "poet", "description": "new"}); err != nil {
		t.Fatal(err)
	}
	if stub.called {
		t.Fatal("omitting default_language must not write anything")
	}
}
