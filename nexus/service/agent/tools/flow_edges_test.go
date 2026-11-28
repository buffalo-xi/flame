package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// edgeTestDiagram is a graph that can be wired directly:
//
//	1 start ──> 2 code           (existing edge 10001)
//	3 branch (sub-branches c1 / c2)
//	4 result
//	7 process ── 8 code (parent_node=7)
//	9 function (no handle at all)
const edgeTestDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "计算"}},
    {"id": "3", "type": "branch", "data": {"label": "判断", "config": {"children": [{"id": "c1", "label": "是"}, {"id": "c2", "label": "否"}]}}},
    {"id": "4", "type": "result", "data": {"label": "输出"}},
    {"id": "7", "type": "process", "data": {"label": "子流程"}},
    {"id": "8", "type": "code", "parent_node": "7", "data": {"label": "容器内代码"}},
    {"id": "9", "type": "function", "data": {"label": "函数"}},
    {"id": "5", "type": "exit", "data": {"label": "结束"}}
  ],
  "edges": [
    {"id": "10001", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
  ]
}`

func edgeTestCtx(diagram string) (*Context, *fakeFlows) {
	fk := &fakeFlows{detail: &FlowDetail{
		FlowInfo: FlowInfo{ID: "flow-1", Name: "demo"},
		Diagram:  json.RawMessage(diagram),
	}}
	return &Context{Flows: fk}, fk
}

// storedEdges parses the stored edges, to assert that they really were written back.
func storedEdges(t *testing.T, fk *fakeFlows) []map[string]any {
	t.Helper()
	if len(fk.updatedDiagram) == 0 {
		t.Fatal("UpdateDiagram was not called: the graph was not stored")
	}
	var doc struct {
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(fk.updatedDiagram, &doc); err != nil {
		t.Fatalf("failed to parse the stored diagram: %v", err)
	}
	return doc.Edges
}

func edgeIDOf(m map[string]any) string {
	s, _ := m["id"].(string)
	return s
}

func TestCreateEdgeWritesDiagram(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow":          "demo",
		"source":        "2",
		"target":        "3",
		"source_handle": "source2right",
		"target_handle": "target3top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 2 {
		t.Errorf("edge_count = %v, want 2", got)
	}
	edges := storedEdges(t, fk)
	if len(edges) != 2 {
		t.Fatalf("stored edge count = %d, want 2", len(edges))
	}
	last := edges[len(edges)-1]
	if last["source"] != "2" || last["target"] != "3" || last["target_handle"] != "target3top" {
		t.Errorf("the last stored edge is wrong: %+v", last)
	}
}

// Both handles are required: a missing target_handle must not be quietly filled in.
func TestCreateEdgeRequiresHandles(t *testing.T) {
	cases := []struct {
		name  string
		args  map[string]any
		wantE string
	}{
		{"missing target_handle", map[string]any{"flow": "demo", "source": "2", "target": "3", "source_handle": "source2right"}, "target_handle is required"},
		{"missing source_handle", map[string]any{"flow": "demo", "source": "2", "target": "3", "target_handle": "target3top"}, "source_handle is required"},
		{"self edge", map[string]any{"flow": "demo", "source": "2", "target": "2", "source_handle": "source2right", "target_handle": "target2top"}, "to itself"},
		{"function node", map[string]any{"flow": "demo", "source": "2", "target": "9", "source_handle": "source2right", "target_handle": "target9left"}, "function"},
		{"across parents", map[string]any{"flow": "demo", "source": "8", "target": "4", "source_handle": "source8right", "target_handle": "target4left"}, "across parents"},
		{"a terminal node has no outgoing edge", map[string]any{"flow": "demo", "source": "5", "target": "3", "source_handle": "source5right", "target_handle": "target3top"}, "no outgoing handle"},
		{"an illegal sub-branch position", map[string]any{"flow": "demo", "source": "3", "target": "4", "source_handle": "sourcec1bottom", "target_handle": "target4left"}, "as an outgoing position"},
		{"an unknown owner", map[string]any{"flow": "demo", "source": "3", "target": "4", "source_handle": "sourcezzright", "target_handle": "target4left"}, "neither node"},
		{"an incoming position that is not this node's", map[string]any{"flow": "demo", "source": "2", "target": "4", "source_handle": "source2right", "target_handle": "target4bottom"}, "as an incoming position"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fk := edgeTestCtx(edgeTestDiagram)
			_, err := (&createEdgeTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantE) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantE)
			}
			if len(fk.updatedDiagram) != 0 {
				t.Error("nothing should be stored when validation fails")
			}
		})
	}
}

// At most one edge between two nodes: not even by changing the port.
func TestCreateEdgeRejectsDuplicatePair(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "1", "target": "2",
		"source_handle": "source1bottom", "target_handle": "target2top",
	})
	if err == nil || !strings.Contains(err.Error(), "at most one edge") {
		t.Fatalf("err = %v, want at most one edge", err)
	}
}

// Connecting a second time into the same inlet -> hard block.
func TestCreateEdgeRejectsSecondInboundOnSameHandle(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3right", "target_handle": "target2left",
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one incoming edge") {
		t.Fatalf("err = %v, want exactly one incoming edge", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when the same inlet is connected twice")
	}
}

// A different inlet -> the old incoming edge is displaced by the new one.
func TestCreateEdgeReplacesInboundOnDifferentHandle(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3right", "target_handle": "target2top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	rep, _ := m["replaced_edge"].(map[string]any)
	if edgeIDOf(rep) != "10001" {
		t.Errorf("replaced_edge = %v, want id 10001", m["replaced_edge"])
	}
	edges := storedEdges(t, fk)
	if len(edges) != 1 {
		t.Fatalf("stored edge count = %d, want 1 (the old incoming edge should be deleted)", len(edges))
	}
	if edges[0]["source"] != "3" || edges[0]["target_handle"] != "target2top" {
		t.Errorf("the stored edge is wrong: %+v", edges[0])
	}
}

// Duplicate outlet: one source_handle can carry only one edge.
func TestCreateEdgeRejectsDuplicateSourceHandle(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "1", "target": "3",
		"source_handle": "source1right", "target_handle": "target3top",
	})
	if err == nil || !strings.Contains(err.Error(), "one outlet takes exactly one edge") {
		t.Fatalf("err = %v, want a duplicate outlet error", err)
	}
}

func TestCreateEdgeRejectsCycle(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	// Build 2 -> 3 first, then try 3 -> 2
	if _, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
		"source_handle": "source2right", "target_handle": "target3top",
	}); err != nil {
		t.Fatalf("building 2->3 failed: %v", err)
	}
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3left", "target_handle": "target2top",
	})
	if err == nil || !strings.Contains(err.Error(), "creates a cycle") {
		t.Fatalf("err = %v, want a cycle error", err)
	}
}

func TestUpdateEdgeRepoints(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "source_handle": "source1bottom",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := out.(map[string]any)["edge_count"]; got != 1 {
		t.Errorf("edge_count = %v, want 1", got)
	}
	edges := storedEdges(t, fk)
	if len(edges) != 1 || edges[0]["source_handle"] != "source1bottom" {
		t.Errorf("the stored edge is wrong: %+v", edges)
	}
}

// Changing target must come with target_handle: the handle embeds the node id.
func TestUpdateEdgeRequiresHandleWhenTargetChanges(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "target": "3",
	})
	if err == nil || !strings.Contains(err.Error(), "target_handle") {
		t.Fatalf("err = %v, want a hint to supply target_handle", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when validation fails")
	}
}

func TestUpdateEdgeNotFound(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "99999", "source_handle": "source1right",
	})
	if err == nil || !strings.Contains(err.Error(), "no edge with edge_id") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestDeleteEdge(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&deleteEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 0 {
		t.Errorf("edge_count = %v, want 0", got)
	}
	if edges := storedEdges(t, fk); len(edges) != 0 {
		t.Errorf("stored edge count = %d, want 0", len(edges))
	}
}

func TestDeleteEdgeNotFound(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&deleteEdgeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "edge_id": "99999"})
	if err == nil || !strings.Contains(err.Error(), "no edge with edge_id") {
		t.Fatalf("err = %v, want not found", err)
	}
}

// The targeting argument uses the same rule as read_flow / delete_flow: flowRefHint is reused.
func TestEdgeToolsReuseFlowRefHint(t *testing.T) {
	for _, tl := range []Tool{&createEdgeTool{}, &updateEdgeTool{}, &deleteEdgeTool{}} {
		meta := tl.Meta()
		props, _ := meta.Parameters["properties"].(map[string]any)
		flow, _ := props["flow"].(map[string]any)
		if desc, _ := flow["description"].(string); desc != flowRefHint {
			t.Errorf("%s: the flow description does not reuse flowRefHint: %q", meta.Name, desc)
		}
	}
}

// create_edge / update_edge must share one copy of the rule text: writing it twice is exactly how "changed A, forgot B" starts.
func TestEdgeToolsShareRuleText(t *testing.T) {
	for _, tl := range []Tool{&createEdgeTool{}, &updateEdgeTool{}} {
		meta := tl.Meta()
		if !strings.Contains(meta.Description, edgeRuleText) {
			t.Errorf("%s's description does not reuse edgeRuleText (the wiring rules can only have one source)", meta.Name)
		}
		// The position advice is a shared constant too: rules without how to pick a position still leave the model guessing the
		// handle
		if !strings.Contains(meta.Description, handleLayoutAdvice) {
			t.Errorf("%s's description does not reuse handleLayoutAdvice", meta.Name)
		}
		// The assertions pin ASCII facts only (handle prefixes and the position table), never prose:
		// the description is translated with the product's primary language, and a Chinese heading must not become part of the
		// interface contract.
		for _, want := range []string{"source_handle", "target_handle", "left|top / right|bottom"} {
			if !strings.Contains(meta.Description, want) {
				t.Errorf("%s's description is missing %q", meta.Name, want)
			}
		}
	}
}

// Edges built by create_flow must carry target_handle too: both the edge tools and the canvas go by it.
func TestCreateFlowWritesTargetHandle(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-th"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "th", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "config": map[string]any{
				"code":    map[string]any{"lang": "lua", "script": "return {}"},
				"outputs": map[string]any{},
			}},
			map[string]any{"type": "branch", "config": map[string]any{"children": []any{
				map[string]any{"id": "c1", "condition": "return true"},
			}}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Edges []flowEdge `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("failed to parse the diagram: %v", err)
	}
	for _, e := range doc.Edges {
		if e.TargetHandle == "" {
			t.Errorf("edge %s->%s has no target_handle", e.Source, e.Target)
		}
		if e.SourceHandle == "" {
			t.Errorf("edge %s->%s has no source_handle (tool-generated edges must follow create_edge's rule too)", e.Source, e.Target)
		}
	}
	// Entering branch on the same row: left by position, no longer a fixed top
	for _, e := range doc.Edges {
		if e.Target == "3" && e.TargetHandle != "target3left" {
			t.Errorf("the edge entering branch has target_handle = %q, want target3left", e.TargetHandle)
		}
	}
}

// The edge direction is suggested from the two nodes' relative positions: on the same row the left and right ends are used, and the
// larger the vertical offset the more vertical the ends; when the type has no such position it falls back to the nearest available
// one (no node has a top outlet).
func TestSuggestHandlesByPosition(t *testing.T) {
	mk := func(id, typ string, x, y int) *flowNodeMeta {
		n := &flowNodeMeta{ID: id, Type: typ}
		n.Position.X, n.Position.Y = x, y
		return n
	}
	cases := []struct {
		name                   string
		src, dst               *flowNodeMeta
		wantSource, wantTarget string
	}{
		{"same row, target to the right", mk("1", "loop", 0, 0), mk("2", "loop", 300, 0), "source1right", "target2left"},
		{"same row, target to the left", mk("1", "loop", 300, 0), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"target far below", mk("1", "loop", 0, 0), mk("2", "loop", 20, 400), "source1bottom", "target2top"},
		{"target far above (no top outlet / bottom inlet, falls back to left->right)", mk("1", "loop", 0, 0), mk("2", "loop", 20, -400), "source1left", "target2right"},
		{"target below right", mk("1", "loop", 0, 0), mk("2", "loop", 300, 120), "source1right", "target2top"},
		{"target below left", mk("1", "loop", 300, 0), mk("2", "loop", 0, 120), "source1left", "target2top"},
		{"target above right", mk("1", "loop", 0, 200), mk("2", "loop", 300, 0), "source1right", "target2left"},
		{"target above left", mk("1", "loop", 300, 200), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"target far above right (no top outlet, falls back to left)", mk("1", "loop", 0, 500), mk("2", "loop", 300, 0), "source1left", "target2left"},
		{"target far above left (no top outlet, falls back to left)", mk("1", "loop", 300, 500), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"code on the same row but the target is left: no left outlet, no right inlet", mk("1", "code", 300, 0), mk("2", "code", 0, 0), "source1right", "target2top"},
		{"start with the target far above", mk("1", "start", 0, 400), mk("2", "loop", 300, 0), "source1left", "target2left"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestHandles(tc.src, tc.dst)
			if got.SourceHandle != tc.wantSource || got.TargetHandle != tc.wantTarget {
				t.Fatalf("suggestHandles = %s -> %s (%s), want %s -> %s",
					got.SourceHandle, got.TargetHandle, got.Reason, tc.wantSource, tc.wantTarget)
			}
		})
	}

	// The fallback has to be echoed in the reason, otherwise the caller believes it connected "as suggested".
	fallback := suggestHandles(mk("1", "loop", 0, 500), mk("2", "loop", 300, 0))
	if !strings.Contains(fallback.Reason, "falling back") {
		t.Errorf("on a fallback the reason should say where it fell back to: %q", fallback.Reason)
	}
}

// A missing handle is not just reported as "required": the value to fill in is given directly, from the positions.
func TestCreateEdgeMissingHandleSuggests(t *testing.T) {
	c, fk := edgeTestCtx(`{
  "nodes": [
    {"id": "1", "type": "start", "position": {"x": 0, "y": 0}, "data": {"label": "开始"}},
    {"id": "2", "type": "code", "position": {"x": 300, "y": 0}, "data": {"label": "甲"}},
    {"id": "3", "type": "code", "position": {"x": 600, "y": 0}, "data": {"label": "乙"}}
  ],
  "edges": []
}`)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
	})
	if err == nil {
		t.Fatal("a missing handle should be an error")
	}
	for _, want := range []string{"source2right", "target3left"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should suggest %q, got = %v", want, err)
		}
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when validation fails")
	}
}

// When the handles do not match the positions the edge is still stored (it is legal), but suggested_edge hints at a shorter wiring.
func TestCreateEdgeSuggestsWhenHandlesMismatch(t *testing.T) {
	c, _ := edgeTestCtx(`{
  "nodes": [
    {"id": "1", "type": "start", "position": {"x": 0, "y": 0}, "data": {"label": "开始"}},
    {"id": "2", "type": "code", "position": {"x": 0, "y": 0}, "data": {"label": "甲"}},
    {"id": "3", "type": "code", "position": {"x": 300, "y": 0}, "data": {"label": "乙"}}
  ],
  "edges": []
}`)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
		"source_handle": "source2bottom", "target_handle": "target3top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	sug, ok := m["suggested_edge"].(map[string]any)
	if !ok {
		t.Fatalf("same row but bottom->top was used, suggested_edge should be returned: %v", m)
	}
	if sug["source_handle"] != "source2right" || sug["target_handle"] != "target3left" {
		t.Errorf("suggested_edge = %v, want source2right / target3left", sug)
	}
}

// One outlet is source<owner> without its position (the canvas' getHandleBase), so a second edge on another position of the
// same node is refused by create_edge - the canvas cannot draw it either.
const loopOutletDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "loop", "data": {"label": "循环", "config": {"loop": {"type": "count", "total_times": 3}}}},
    {"id": "3", "type": "entry", "parent_node": "2", "data": {"label": "入口"}},
    {"id": "4", "type": "code", "data": {"label": "甲"}},
    {"id": "5", "type": "code", "data": {"label": "乙"}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"},
    {"id": "e2", "source": "2", "target": "4", "source_handle": "source2right", "target_handle": "target4left"},
    {"id": "e3", "source": "4", "target": "5", "source_handle": "source4right", "target_handle": "target5left"}
  ]
}`

// A branch child is its own outlet owner: left and right of one child are one outlet, another child is another outlet.
const branchOwnerDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "branch", "data": {"label": "判断", "config": {"children": [{"id": "c1", "label": "是"}, {"id": "c2", "label": "否"}]}}},
    {"id": "3", "type": "code", "data": {"label": "甲"}},
    {"id": "4", "type": "code", "data": {"label": "乙"}},
    {"id": "5", "type": "code", "data": {"label": "丙"}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"},
    {"id": "e2", "source": "2", "target": "3", "source_handle": "sourcec1left", "target_handle": "target3left"},
    {"id": "e3", "source": "2", "target": "4", "source_handle": "sourcec2right", "target_handle": "target4left"},
    {"id": "e4", "source": "3", "target": "5", "source_handle": "source3right", "target_handle": "target5left"}
  ]
}`

func TestCreateEdgeRefusesSecondOutgoingFromOneNode(t *testing.T) {
	c, fk := edgeTestCtx(loopOutletDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "5",
		"source_handle": "source2bottom", "target_handle": "target5left",
	})
	if err == nil || !strings.Contains(err.Error(), "one outlet takes exactly one edge") {
		t.Fatalf("err = %v, want the second outgoing edge to be refused", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when the edge is refused")
	}
}

// update_edge moves an edge: landing on another position of an occupied outlet displaces the old edge and says so.
func TestUpdateEdgeDisplacesTheOldOutgoingEdge(t *testing.T) {
	c, fk := edgeTestCtx(loopOutletDiagram)
	out, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "e3",
		"source": "2", "source_handle": "source2bottom",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	rep, ok := m["replaced_out_edge"].(map[string]any)
	if !ok || edgeIDOf(rep) != "e2" {
		t.Fatalf("replaced_out_edge = %v, want e2", m["replaced_out_edge"])
	}
	edges := storedEdges(t, fk)
	if len(edges) != 2 {
		t.Fatalf("stored edge count = %d, want 2 (e1 and the moved e3)", len(edges))
	}
	for _, e := range edges {
		switch edgeIDOf(e) {
		case "e2":
			t.Error("the displaced edge e2 is still stored")
		case "e3":
			if e["source"] != "2" || e["source_handle"] != "source2bottom" {
				t.Errorf("e3 was not moved: %+v", e)
			}
		}
	}
}

// The very same handle twice stays a hard error: displacement only covers another position of the same outlet.
func TestUpdateEdgeRefusesTheSameOutletTwice(t *testing.T) {
	c, fk := edgeTestCtx(loopOutletDiagram)
	_, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "e3",
		"source": "2", "source_handle": "source2right",
	})
	if err == nil || !strings.Contains(err.Error(), "one outlet takes exactly one edge") {
		t.Fatalf("err = %v, want the same handle to stay refused", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when validation fails")
	}
}

// A branch child behaves the same: moving an edge onto sourcec1 displaces the route the child already had.
func TestUpdateEdgeDisplacesTheOldChildRoute(t *testing.T) {
	c, fk := edgeTestCtx(branchOwnerDiagram)
	out, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "e4",
		"source": "2", "source_handle": "sourcec1right",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if rep, ok := m["replaced_out_edge"].(map[string]any); !ok || edgeIDOf(rep) != "e2" {
		t.Fatalf("replaced_out_edge = %v, want e2", m["replaced_out_edge"])
	}
	edges := storedEdges(t, fk)
	if len(edges) != 3 {
		t.Fatalf("stored edge count = %d, want 3", len(edges))
	}
	for _, e := range edges {
		if edgeIDOf(e) == "e2" {
			t.Error("the displaced child route e2 is still stored")
		}
	}
}

// Child ids are matched exactly: c1 and c11 are two outlets, so the sibling must stay wireable (the old prefix match refused it).
func TestCreateEdgeAllowsSiblingChildWithPrefixID(t *testing.T) {
	c, fk := edgeTestCtx(`{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "branch", "data": {"label": "判断", "config": {"children": [{"id": "c1", "label": "是"}, {"id": "c11", "label": "否"}]}}},
    {"id": "3", "type": "code", "data": {"label": "甲"}},
    {"id": "5", "type": "code", "data": {"label": "乙"}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"},
    {"id": "e2", "source": "2", "target": "3", "source_handle": "sourcec1left", "target_handle": "target3left"}
  ]
}`)
	if _, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "5",
		"source_handle": "sourcec11right", "target_handle": "target5left",
	}); err != nil {
		t.Fatalf("err = %v, want the sibling child to be wireable", err)
	}
	if edges := storedEdges(t, fk); len(edges) != 3 {
		t.Fatalf("stored edge count = %d, want 3 (e1, e2 and the new child edge)", len(edges))
	}
}
