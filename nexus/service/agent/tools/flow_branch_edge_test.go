package tools

import (
	"context"
	"strings"
	"testing"
)

// One branch, one declared child (already routed to node 3), and a spare node 4 to point it at again.
const branchOneChildDiagram = `{"nodes":[
 {"id":"1","type":"start","data":{}},
 {"id":"2","type":"branch","data":{"label":"分支","config":{"children":[{"id":"c1","label":"是","condition":{"lang":"lua","script":"return true"}}]}}},
 {"id":"3","type":"code","data":{"label":"甲","config":{"code":{"lang":"lua","script":"return {}"}}}},
 {"id":"4","type":"code","data":{"label":"乙","config":{"code":{"lang":"lua","script":"return {}"}}}}
],"edges":[
 {"id":"e1","source":"1","target":"2","source_handle":"source1right","target_handle":"target2left"},
 {"id":"e2","source":"2","target":"3","source_handle":"sourcec1left","target_handle":"target3top"}
]}`

// left and right of one child are the same outlet: a second edge would route that child to two different next nodes, so the
// "one outlet per handle" rule alone does not cover it.
func TestCreateEdgeRefusesSecondRouteForSameChild(t *testing.T) {
	c, fk := edgeTestCtx(branchOneChildDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "4",
		"source_handle": "sourcec1right", "target_handle": "target4top",
	})
	if err == nil || !strings.Contains(err.Error(), "one child routes to exactly one next node") {
		t.Fatalf("err = %v, want the per-child refusal", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when the edge is refused")
	}
}
