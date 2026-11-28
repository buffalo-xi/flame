package tools

import (
	"context"
	"strings"
	"testing"
)

// A function with a return inside it, plus an empty process to move it into.
const returnInFunctionDiagram = `{"nodes":[
 {"id":"1","type":"start","data":{}},
 {"id":"f1","type":"function","data":{}},
 {"id":"fe","type":"entry","parent_node":"f1","data":{}},
 {"id":"r1","type":"return","parent_node":"f1","data":{}},
 {"id":"p1","type":"process","data":{}},
 {"id":"pe","type":"entry","parent_node":"p1","data":{}}],
 "edges":[{"id":"e1","source":"1","target":"f1"}]}`

// A return belongs to a function body: outside one it ends the whole flow instead of the container, so the move is refused at
// the write rather than left for validate_flow to report afterwards (return_scope).
func TestUpdateNodeRefusesMovingReturnOutOfFunction(t *testing.T) {
	cases := []struct {
		name   string
		parent any
	}{
		{"into a process", "p1"},
		{"back to the top level", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fk := edgeTestCtx(returnInFunctionDiagram)
			_, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
				"flow": "demo", "node_id": "r1", "parent_node": tc.parent,
			})
			if err == nil || !strings.Contains(err.Error(), "only live inside a function") {
				t.Fatalf("err = %v, want the refusal", err)
			}
			if len(fk.updatedDiagram) != 0 {
				t.Error("nothing should be stored when the move is refused")
			}
		})
	}
}
