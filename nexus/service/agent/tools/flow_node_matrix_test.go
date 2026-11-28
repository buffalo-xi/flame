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
		// The default code is filled in, the message is the part the author has to write.
		{typ: "raise", want: []string{"raise.message"}},
		{typ: "memory"},
		{typ: "result", want: []string{"config.code"}},
		{typ: "progress", want: []string{"config.code"}},
		{typ: "exit"},
		{typ: "layout"},
		{typ: "comment"},
	}

	// The three signal nodes that only exist inside a container: return needs a function, break / continue need a loop.
	// Their bare-creation rows live in the next two tests, where the container is created first.
	seen := map[string]bool{"return": true, "break": true, "continue": true}
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

// A break / continue node is only legal inside a loop: outside one its signal has no consumer, so the node itself fails at run
// time (control.go checkSignalScope). The write boundary refuses it up front instead of storing a step that can only fail.
func TestCreateNodeBareBreakContinueInsideLoop(t *testing.T) {
	for _, typ := range []string{"break", "continue"} {
		t.Run(typ, func(t *testing.T) {
			c, _ := edgeTestCtx(edgeTestDiagram)
			if _, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
				"flow": "demo", "type": typ,
			}); err == nil || !strings.Contains(err.Error(), "inside a loop") {
				t.Fatalf("a bare %s at the top level must be refused, got %v", typ, err)
			}

			loopOut, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
				"flow": "demo", "type": "loop",
			})
			if err != nil {
				t.Fatalf("create loop = %v", err)
			}
			node, _ := loopOut.(map[string]any)["node"].(map[string]any)
			loopID, _ := node["id"].(string)
			if loopID == "" {
				t.Fatalf("the loop node has no id: %v", loopOut)
			}

			out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
				"flow": "demo", "type": typ, "parent_node": loopID,
			})
			if err != nil {
				t.Fatalf("create %s inside a loop = %v", typ, err)
			}
			if warns := warningStrings(t, out); len(warns) != 0 {
				t.Fatalf("a %s node has nothing to configure, got %v", typ, warns)
			}
		})
	}
}
