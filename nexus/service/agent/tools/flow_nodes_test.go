package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// nodeTestDiagram is a graph with references:
//
//	1 start (an island, only to take up space)
//	5 code ──> 2 code (2 references 5 through config.inputs.x.source_ref)
//	7 process ── 9 entry ──> 8 code (inside the container)
const nodeTestDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "中间", "config": {
      "code": {"lang": "lua", "script": "return {}"},
      "inputs": {"x": {"type": "string", "source_type": "node", "source_ref": "5", "source_key": "out"}}
    }}},
    {"id": "5", "type": "code", "data": {"label": "上游", "config": {"code": {"lang": "lua", "script": "return {}"}}}},
    {"id": "7", "type": "process", "data": {"label": "子流程"}},
    {"id": "9", "type": "entry", "parent_node": "7", "data": {"label": "入口"}},
    {"id": "8", "type": "code", "parent_node": "7", "data": {"label": "容器内"}}
  ],
  "edges": [
    {"id": "20001", "source": "5", "target": "2", "source_handle": "source5right", "target_handle": "target2left"},
    {"id": "20002", "source": "9", "target": "8", "source_handle": "source9right", "target_handle": "target8left"}
  ]
}`

// storedNodes parses the stored nodes and indexes them by id.
func storedNodes(t *testing.T, fk *fakeFlows) map[string]map[string]any {
	t.Helper()
	if len(fk.updatedDiagram) == 0 {
		t.Fatal("UpdateDiagram was not called: the graph was not stored")
	}
	var doc struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(fk.updatedDiagram, &doc); err != nil {
		t.Fatalf("failed to parse the stored diagram: %v", err)
	}
	out := make(map[string]map[string]any, len(doc.Nodes))
	for _, n := range doc.Nodes {
		id, _ := n["id"].(string)
		out[id] = n
	}
	return out
}

func TestCreateNodeTopLevel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "llm", "config": map[string]any{
			"llm": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	node, _ := m["node"].(map[string]any)
	id, _ := node["id"].(string)
	if id == "" || len(id) != 5 {
		t.Errorf("new node id = %q, want a 5-digit string (the same as the canvas)", id)
	}
	if _, ok := storedNodes(t, fk)[id]; !ok {
		t.Error("the new node was not stored")
	}
	h, _ := m["handles"].(map[string]any)
	if ins, _ := h["in"].([]string); len(ins) == 0 {
		t.Errorf("handles.in is empty: %v", h)
	}
}

// A container must bring an entry automatically: without one the executor logs a line and returns success straight away.
func TestCreateNodeInContainerAddsEntry(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "code", "parent_node": "7",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	entry, ok := m["parent_entry"].(map[string]any)
	if !ok {
		t.Fatal("creating a node inside a container did not add an entry (a container without one spins empty silently)")
	}
	entryID, _ := entry["id"].(string)
	nodes := storedNodes(t, fk)
	if got := nodes[entryID]["parent_node"]; got != "7" {
		t.Errorf("entry.parent_node = %v, want 7", got)
	}
	if got := nodes[entryID]["type"]; got != "entry" {
		t.Errorf("entry.type = %v, want entry", got)
	}
	node, _ := m["node"].(map[string]any)
	if got := nodes[node["id"].(string)]["parent_node"]; got != "7" {
		t.Errorf("the new node's parent_node = %v, want 7", got)
	}
}

func TestCreateNodeRejectsIllegalContainer(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		wantEr string
	}{
		{"function inside a container", map[string]any{"flow": "demo", "type": "function", "parent_node": "7"}, "only be created at the top level"},
		{"return outside a function", map[string]any{"flow": "demo", "type": "return", "parent_node": "7"}, "inside a function container"},
		{"the parent is not a container", map[string]any{"flow": "demo", "type": "code", "parent_node": "2"}, "only loop / process / function are containers"},
		{"the parent does not exist", map[string]any{"flow": "demo", "type": "code", "parent_node": "zz"}, "does not exist"},
		{"start cannot be created", map[string]any{"flow": "demo", "type": "start"}, "cannot create a node of type"},
		{"entry cannot be created", map[string]any{"flow": "demo", "type": "entry"}, "cannot create a node of type"},
		{"knowledge has no executor", map[string]any{"flow": "demo", "type": "knowledge"}, "no executor registered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fk := edgeTestCtx(edgeTestDiagram)
			_, err := (&createNodeTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantEr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantEr)
			}
			if len(fk.updatedDiagram) != 0 {
				t.Error("nothing should be stored when validation fails")
			}
		})
	}
}

// The list of creatable types is pinned here: adding or removing a type requires confirming the executors registry, the frontend
// palette and the skill docs at the same time, otherwise you get "renders fine, fails at run time with no executor registered"
// (that is how knowledge was lost).
func TestCreatableNodeTypesFixed(t *testing.T) {
	want := []string{
		"llm", "code", "http", "mcp", "rpc", "branch", "intent", "loop", "process", "function",
		"call", "assign", "memory", "wait", "fork", "result", "progress", "break", "continue",
		"return", "exit", "layout", "comment",
	}
	got := make([]string, 0, len(creatableNodeTypes))
	for typ := range creatableNodeTypes {
		got = append(got, typ)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("creatableNodeTypes = %v, want %v (sync executors / the frontend palette / the skill docs when adding or removing a type)", got, want)
	}
	// Structural nodes and legacy types without an executor must not appear in the creatable list.
	for _, typ := range []string{"start", "entry", "knowledge"} {
		if creatableNodeTypes[typ] {
			t.Errorf("%q must not be creatable", typ)
		}
	}
}

// When branch gets no children one is added automatically: the executor finds outgoing edges by children[].id.
func TestCreateNodeBranchDefaultsChildren(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "branch"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	node, _ := out.(map[string]any)["node"].(map[string]any)
	id, _ := node["id"].(string)
	raw, _ := json.Marshal(storedNodes(t, fk)[id])
	var n struct {
		Data struct {
			Config struct {
				Children []struct {
					ID string `json:"id"`
				} `json:"children"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("failed to parse the node: %v", err)
	}
	if len(n.Data.Config.Children) != 1 || n.Data.Config.Children[0].ID == "" {
		t.Errorf("branch's children were not generated automatically: %s", raw)
	}
}

// insert_node: delete the old edge and create A->new->B, atomically.
func TestInsertNodeRewiresEdge(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&insertNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "type": "code",
		"config": map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	node, _ := m["node"].(map[string]any)
	id, _ := node["id"].(string)

	edges := storedEdges(t, fk)
	if len(edges) != 2 {
		t.Fatalf("stored edge count = %d, want 2 (the old edge deleted, two inserted)", len(edges))
	}
	if edges[0]["source"] != "1" || edges[0]["target"] != id {
		t.Errorf("the first edge is wrong: %+v", edges[0])
	}
	if edges[1]["source"] != id || edges[1]["target"] != "2" {
		t.Errorf("the second edge is wrong: %+v", edges[1])
	}
	if edges[1]["target_handle"] != "target2left" {
		t.Errorf("the second edge should keep the original incoming handle, got %v", edges[1]["target_handle"])
	}
}

func TestInsertNodeRejectsTerminalType(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&insertNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "type": "exit",
	})
	if err == nil || !strings.Contains(err.Error(), "no outgoing handle") {
		t.Fatalf("err = %v, want no outgoing edge", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored when validation fails")
	}
}

func TestUpdateNodeConfigPatch(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "config_patch": map[string]any{"limit": 5},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cfg, _ := out.(map[string]any)["config"].(map[string]any)
	if cfg["limit"] == nil {
		t.Errorf("the patch did not take effect: %v", cfg)
	}
	if _, ok := cfg["code"]; !ok {
		t.Errorf("the patch lost existing keys (it should merge, not replace): %v", cfg)
	}
	_ = fk
}

// Moving into a container: edges spanning the old and new scope are deleted and echoed back (the canvas does not allow crossing
// parents either).
func TestUpdateNodeMoveDropsCrossScopeEdges(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "parent_node": "7",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	removed, _ := m["removed_edges"].([]any)
	if len(removed) != 1 {
		t.Fatalf("removed_edges = %v, want 1 (5->2 crosses the container)", m["removed_edges"])
	}
	if got := storedNodes(t, fk)["2"]["parent_node"]; got != "7" {
		t.Errorf("2.parent_node = %v, want 7", got)
	}
	// The in-container edge 9->8 is unaffected
	if edges := storedEdges(t, fk); len(edges) != 1 || edges[0]["id"] != "20002" {
		t.Errorf("the remaining edge is wrong: %+v", edges)
	}
}

func TestDeleteNodeCascadesEdges(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "2"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 0 {
		t.Errorf("edge_count = %v, want 0", got)
	}
	if _, ok := storedNodes(t, fk)["2"]; ok {
		t.Error("the node was not deleted")
	}
}

// Refused when referenced: deleting a node some source_ref points at guarantees a failed lookup at run time.
func TestDeleteNodeRefusesReferenced(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	_, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "5"})
	if err == nil || !strings.Contains(err.Error(), "still reference it") {
		t.Fatalf("err = %v, want a refusal listing the referencing nodes", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored on a refusal")
	}
}

func TestDeleteNodeRefusesEntryAndStart(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"9", "entry"},
		{"1", "start"},
	} {
		c, _ := edgeTestCtx(nodeTestDiagram)
		_, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": tc.id})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("deleting %s: err = %v, want it to contain %q", tc.id, err, tc.want)
		}
	}
}

// Deleting a container: the whole subtree (entry included) goes with it.
func TestDeleteContainerRemovesSubtree(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "7"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	deleted, _ := m["deleted_nodes"].([]any)
	if len(deleted) != 3 {
		t.Fatalf("deleted_nodes = %v, want 3 (7 + 9 + 8)", m["deleted_nodes"])
	}
	nodes := storedNodes(t, fk)
	for _, id := range []string{"7", "8", "9"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("node %s was not deleted", id)
		}
	}
}

// create_flow's container sub-chains: the entry is created automatically, children carry parent_node, and the chain starts from the
// entry.
func TestCreateFlowContainerChildren(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-c"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "loop-with-body", "description": "循环里跑一步",
		"steps": []any{
			map[string]any{
				"type": "loop", "label": "循环",
				"config":   map[string]any{"loop": map[string]any{"type": "count", "total_times": 3}},
				"children": []any{map[string]any{"type": "code", "label": "循环体", "config": map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Nodes []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			ParentNode string `json:"parent_node"`
		} `json:"nodes"`
		Edges []flowEdge `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("failed to parse the diagram: %v", err)
	}

	var entryID, bodyID string
	for _, n := range doc.Nodes {
		switch {
		case n.Type == "entry":
			entryID = n.ID
			if n.ParentNode != "2" {
				t.Errorf("entry.parent_node = %q, want 2 (the loop node)", n.ParentNode)
			}
		case n.Type == "code":
			bodyID = n.ID
			if n.ParentNode != "2" {
				t.Errorf("the loop body's parent_node = %q, want 2", n.ParentNode)
			}
		}
	}
	if entryID == "" || bodyID == "" {
		t.Fatalf("no entry or loop body was created: %+v", doc.Nodes)
	}
	found := false
	for _, e := range doc.Edges {
		if e.Source == entryID && e.Target == bodyID {
			found = true
		}
	}
	if !found {
		t.Errorf("the entry is not connected to the loop body: %+v", doc.Edges)
	}
}

// In create_flow, call.target / wait.fork_node_id also accept a label (the same rule as source_ref).
func TestCreateFlowResolvesTargetAndForkIDByLabel(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-r"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "call-by-label", "description": "按 label 解析 target",
		"steps": []any{
			map[string]any{
				"type": "function", "label": "我的函数",
				"config": map[string]any{},
			},
			map[string]any{
				"type": "call", "label": "调它",
				"config": map[string]any{"target": "我的函数"},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Data struct {
				Config map[string]any `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("failed to parse the diagram: %v", err)
	}
	var fnID string
	for _, n := range doc.Nodes {
		if n.Type == "function" {
			fnID = n.ID
		}
	}
	for _, n := range doc.Nodes {
		if n.Type != "call" {
			continue
		}
		if got, _ := n.Data.Config["target"].(string); got != fnID {
			t.Errorf("call.target = %q, want the function node id %q", got, fnID)
		}
	}
}

// ===== Label uniqueness =====

func nodeLabel(t *testing.T, fk *fakeFlows, id string) string {
	t.Helper()
	data, _ := storedNodes(t, fk)[id]["data"].(map[string]any)
	label, _ := data["label"].(string)
	return label
}

// An explicitly written duplicate label -> refused, nothing stored.
func TestCreateNodeRejectsDuplicateLabel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram) // node 2 already carries a fixture label
	_, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "code", "label": "计算",
	})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("err = %v, want the duplicate label refused", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored on a duplicate")
	}
}

// No label given (auto naming) but the name collides -> a suffix is appended, without interrupting the build.
func TestCreateNodeAutoSuffixForDefaultLabel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	labels := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "code"})
		if err != nil {
			t.Fatalf("creating code node %d failed: %v", i+1, err)
		}
		node, _ := out.(map[string]any)["node"].(map[string]any)
		labels = append(labels, nodeLabel(t, fk, node["id"].(string)))
	}
	if labels[0] != "Code" {
		t.Errorf("the first default label = %q, want Code", labels[0])
	}
	if labels[0] == labels[1] {
		t.Errorf("both default labels are %q: a suffix should have been appended", labels[0])
	}
}

// A container's entry takes part in uniqueness too: the second container's entry must not reuse the first entry's label.
func TestCreateNodeContainerEntryLabelUnique(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	first, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "process"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	second, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "process"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	e1 := first.(map[string]any)["entry"].(map[string]any)["id"].(string)
	e2 := second.(map[string]any)["entry"].(map[string]any)["id"].(string)
	if l1, l2 := nodeLabel(t, fk, e1), nodeLabel(t, fk, e2); l1 == l2 {
		t.Errorf("both entries have the label %q: a suffix should have been appended", l1)
	}
}

func TestUpdateNodeRenameLabelRules(t *testing.T) {
	// Renaming to someone else's label -> refused
	c, fk := edgeTestCtx(nodeTestDiagram) // nodes 2 and 5 both carry fixture labels
	_, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "label": "上游",
	})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("err = %v, want the rename onto a duplicate refused", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("nothing should be stored on a duplicate")
	}

	// Renaming to its own current label -> allowed
	c2, fk2 := edgeTestCtx(nodeTestDiagram)
	if _, err := (&updateNodeTool{}).Execute(context.Background(), c2, map[string]any{
		"flow": "demo", "node_id": "2", "label": "中间",
	}); err != nil {
		t.Fatalf("renaming to its own current label should not fail: %v", err)
	}
	if got := nodeLabel(t, fk2, "2"); got != "中间" {
		t.Errorf("label = %q, want 中间", got)
	}
}

// An explicit duplicate inside create_flow's steps -> the error names the step number.
func TestCreateFlowRejectsDuplicateLabels(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-dup"}
	codeCfg := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "dup", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "label": "同名", "config": codeCfg},
			map[string]any{"type": "code", "label": "同名", "config": codeCfg},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "collides with") {
		t.Fatalf("err = %v, want step 2's duplicate label refused", err)
	}
}

// No label given in create_flow (auto naming) but the name collides -> a suffix is appended automatically.
func TestCreateFlowAutoSuffixForDefaultLabels(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-sfx"}
	codeCfg := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	if _, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "sfx", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "config": codeCfg},
			map[string]any{"type": "code", "config": codeCfg},
		},
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Nodes []struct {
			Type string `json:"type"`
			Data struct {
				Label string `json:"label"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("failed to parse the diagram: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range doc.Nodes {
		if n.Data.Label == "" {
			t.Errorf("node %s has no label", n.Type)
			continue
		}
		if seen[n.Data.Label] {
			t.Errorf("label %q is duplicated", n.Data.Label)
		}
		seen[n.Data.Label] = true
	}
}

// update_node re-checks the config it just wrote: a wrong shape is refused before anything is stored (the same stance as
// create_node), and a missing required key comes back as a warning instead of being saved silently.
func TestUpdateNodeRechecksConfig(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	_, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2",
		"config_patch": map[string]any{"code": "not an object"},
	})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("err = %v, want a non-object code block refused", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("a refused config must not be stored")
	}

	// A complete config produces no warning: a checker that always speaks is a checker nobody reads.
	c2, _ := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c2, map[string]any{
		"flow": "demo", "node_id": "2",
		"config_patch": map[string]any{
			"code":    map[string]any{"lang": "lua", "script": "return {}"},
			"outputs": map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if w, ok := out.(map[string]any)["warnings"]; ok {
		t.Errorf("warnings = %v, want none for a complete config", w)
	}

	// Dropping the script: the node is still saved, but the caller is told it will fail at run time.
	c3, fk3 := edgeTestCtx(nodeTestDiagram)
	out, err = (&updateNodeTool{}).Execute(context.Background(), c3, map[string]any{
		"flow": "demo", "node_id": "2",
		"config_patch": map[string]any{"code": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	warns, _ := out.(map[string]any)["warnings"].([]string)
	found := false
	for _, w := range warns {
		if strings.Contains(w, "code.script") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want the missing code.script reported", warns)
	}
	if _, ok := storedNodes(t, fk3)["2"]; !ok {
		t.Error("a warning must not block the write")
	}
}
