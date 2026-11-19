package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/models"
)

// fakeScriptSource implements only the three methods the file gate uses.
type fakeScriptSource struct {
	status int
	found  bool
}

func (f fakeScriptSource) List(context.Context) ([]CustomToolSpec, error) { return nil, nil }

func (f fakeScriptSource) Get(context.Context, string) (*CustomToolSpec, error) { return nil, nil }

func (f fakeScriptSource) Status(context.Context, string) (int, bool, error) {
	return f.status, f.found, nil
}

// The four criteria of tools/{name}/** once it is under the file gate.
//
// The difference from skills: a custom tool's code **is** the tool itself, so "does not exist" must refuse the write - otherwise
// one could just mkdir a directory and create an orphan that exists on disk but not in the custom_tools table.
func TestCheckWriteCustomTool(t *testing.T) {
	// wantErr pins only the identifier or keyword needed to tell which criterion blocked it (error copy is translated with the
	// product's primary language, and a whole Chinese sentence must not become the interface contract); "" = this case should
	// pass.
	cases := []struct {
		name    string
		agent   string
		src     CustomToolSource
		path    string
		wantErr string
	}{
		{"super agent writes a normal tool", "main", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum/main.py", ""},
		{"tool does not exist", "main", fakeScriptSource{}, "tools/sum/main.py", "create_custom_tool"},
		{"tool is locked", "main", fakeScriptSource{status: models.StatusLocked, found: true}, "tools/sum/main.py", "locked"},
		{"the entity root itself", "main", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum", "entity directory itself"},
		{"non-supervisor is always refused", "poet", fakeScriptSource{status: models.StatusNormal, found: true}, "tools/sum/main.py", "supervisor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The identity criterion is an attribute: in tests "main" means the super agent, which must be set explicitly
			c := &Context{AgentName: tc.agent, IsSupervisor: tc.agent == "main", CustomTools: tc.src}
			err := checkWrite(context.Background(), c, tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("should pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error matching %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Without CustomTools injected (unit tests / callers with no tenant semantics) it must not panic but report a readable error.
func TestCheckWriteCustomToolWithoutSource(t *testing.T) {
	c := &Context{AgentName: "main", IsSupervisor: true}
	err := checkWrite(context.Background(), c, "tools/sum/main.py")
	if err == nil || !strings.Contains(err.Error(), "script tool source") {
		t.Fatalf("expected an error about the source being unavailable, got %v", err)
	}
}
