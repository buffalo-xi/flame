package tools

import (
	"context"
	"strings"
	"testing"
)

// warningStrings pulls the warnings a tool returned, or nil when the tool reported none.
func warningStrings(t *testing.T, out any) []string {
	t.Helper()
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("tool result = %T, want map[string]any", out)
	}
	warns, _ := m["warnings"].([]string)
	return warns
}

// TestCreateNodeBareTypeWarnings is the creation-side half of the per-type contract (the other half is
// flowcheck/conformance_test.go): dropping a node of each creatable type with no config must produce exactly the findings
// Step produces on the default config the tool fills in.
//
// The table is the answer to "what do I get when I drop this node on the canvas", and it is checked against
// creatableNodeTypes in both directions, so a new creatable type cannot ship without saying what it warns about.
func TestCreateNodeBareTypeWarnings(t *testing.T) {
	cases := []struct {
		typ  string
		want []string // one substring per expected warning
	}{
		{typ: "llm", want: []string{"config.llm"}},
		{typ: "code", want: []string{"code.script", "config.outputs"}},
		{typ: "http", want: []string{"http.url"}},
		{typ: "mcp", want: []string{"config.mcp"}},
		{typ: "rpc", want: []string{"config.rpc"}},
		{typ: "branch", want: []string{"no condition script"}},
		// intent children are classified by label / description; the default one carries both, so nothing is missing.
		{typ: "intent"},
		{typ: "loop"},    // the default is count=10
		{typ: "process"}, // the entry child is added for it
		{typ: "function"},
		{typ: "call", want: []string{"config.target"}},
		{typ: "assign", want: []string{"config.assignments"}},
		{typ: "memory"},
		{typ: "wait", want: []string{"fork_node_id"}},
		{typ: "fork"},
		{typ: "result", want: []string{"config.code"}},
		{typ: "progress", want: []string{"config.code"}},
		{typ: "break"},
		{typ: "continue"},
		{typ: "exit"},
		{typ: "layout"},
		{typ: "comment"},
	}

	seen := map[string]bool{"return": true} // created inside a function, see the next test
	for _, tc := range cases {
		seen[tc.typ] = true
		t.Run(tc.typ, func(t *testing.T) {
			c, _ := edgeTestCtx(edgeTestDiagram)
			out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
				"flow": "demo", "type": tc.typ,
			})
			if err != nil {
				t.Fatalf("create %s = %v", tc.typ, err)
			}
			warns := warningStrings(t, out)
			if len(warns) != len(tc.want) {
				t.Fatalf("bare %s creation warnings = %v, want %d (%v)", tc.typ, warns, len(tc.want), tc.want)
			}
			used := make([]bool, len(warns))
			for _, want := range tc.want {
				found := false
				for i, w := range warns {
					if !used[i] && strings.Contains(w, want) {
						used[i] = true
						found = true
						break
					}
				}
				if !found {
					t.Errorf("bare %s creation: no warning mentions %q (got %v)", tc.typ, want, warns)
				}
			}
		})
	}

	for typ := range creatableNodeTypes {
		if !seen[typ] {
			t.Errorf("%s is creatable but has no bare-creation row", typ)
		}
	}
}

// A return node is only legal inside a function, and it has nothing to configure: creating it there must be silent. The
// function is created first so it already has an entry - a hand-written function without one legitimately warns, which is
// a different finding and not what this test is about.
func TestCreateNodeBareReturnInsideFunction(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	fnOut, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "function",
	})
	if err != nil {
		t.Fatalf("create function = %v", err)
	}
	node, _ := fnOut.(map[string]any)["node"].(map[string]any)
	fnID, _ := node["id"].(string)
	if fnID == "" {
		t.Fatalf("the function node has no id: %v", fnOut)
	}

	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "return", "parent_node": fnID,
	})
	if err != nil {
		t.Fatalf("create return = %v", err)
	}
	if warns := warningStrings(t, out); len(warns) != 0 {
		t.Fatalf("a return node has nothing to configure, got %v", warns)
	}
}
