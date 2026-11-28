package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scopeDiagram is a process host with two members and a trailing serial node - the shape read_flow has to describe.
const scopeDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "Start"}},
    {"id": "2", "type": "process", "data": {"label": "Fan out", "config": {"parallel": {"max_concurrency": 2, "join": "drain"}}}},
    {"id": "20", "type": "entry", "parent_node": "2", "data": {"label": "Entry"}},
    {"id": "3", "type": "code", "parent_node": "2", "data": {"label": "A", "concurrent": true}},
    {"id": "4", "type": "code", "parent_node": "2", "data": {"label": "B", "concurrent": true}},
    {"id": "5", "type": "code", "parent_node": "2", "data": {"label": "C"}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2"},
    {"id": "e2", "source": "20", "target": "3"},
    {"id": "e3", "source": "3", "target": "4"},
    {"id": "e4", "source": "4", "target": "5"}
  ]
}`

// read_flow's blocks answer "which scope am I in" from the tool output instead of leaving it to be inferred from
// parent_node plus a boolean.
func TestConcurrencyBlocksSummary(t *testing.T) {
	doc, err := parseFlowDoc([]byte(scopeDiagram))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	blocks := concurrencyBlocks(doc)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b["host"] != "2" {
		t.Errorf("host = %v, want 2", b["host"])
	}
	members, _ := b["members"].([]string)
	if len(members) != 2 || members[0] != "3" || members[1] != "4" {
		t.Errorf("members = %v, want [3 4]", b["members"])
	}
	if b["max_concurrency"] != 2 {
		t.Errorf("max_concurrency = %v, want 2", b["max_concurrency"])
	}
	if b["join"] != "drain" {
		t.Errorf("join = %v, want drain", b["join"])
	}
}

// A host with no config.parallel still reports a block: it is a scope, just an unconfigured one.
func TestConcurrencyBlocksDefaults(t *testing.T) {
	doc, err := parseFlowDoc([]byte(`{
		"nodes": [
			{"id": "2", "type": "loop", "data": {"label": "Loop"}},
			{"id": "20", "type": "entry", "parent_node": "2"},
			{"id": "3", "type": "code", "parent_node": "2", "data": {"concurrent": true}}
		],
		"edges": [{"id": "e2", "source": "20", "target": "3"}]
	}`))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	blocks := concurrencyBlocks(doc)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0]["join"] != "collect" || blocks[0]["max_concurrency"] != 0 {
		t.Errorf("block = %v, want the collect / unlimited defaults", blocks[0])
	}
}

// The flag is a structure attribute: it lands in data, never in config (which update_node replaces wholesale).
func TestBuildNodeJSONConcurrentFlag(t *testing.T) {
	raw, err := buildNodeJSON("9", "code", "A", map[string]any{"code": map[string]any{}}, "2", map[string]int{"x": 0, "y": 0}, true)
	if err != nil {
		t.Fatalf("buildNodeJSON = %v", err)
	}
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	data, _ := node["data"].(map[string]any)
	if flag, _ := data["concurrent"].(bool); !flag {
		t.Errorf("data.concurrent = %v, want true", data["concurrent"])
	}
	if cfg, _ := data["config"].(map[string]any); cfg != nil {
		if _, ok := cfg["concurrent"]; ok {
			t.Error("concurrent landed in config: update_node would drop it on the next config write")
		}
	}
}

// Membership only exists inside a host body: outside one the runtime rejects the flag, so the write refuses it too.
func TestBuildMemberRequiresHostParent(t *testing.T) {
	doc, err := parseFlowDoc([]byte(scopeDiagram))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	if _, _, _, _, err := buildNodeForTool(doc, "code", "", "", nil, true); err == nil {
		t.Fatal("buildNodeForTool = nil, want a refusal: the top level is not a scope")
	} else if !strings.Contains(err.Error(), "process or loop") {
		t.Errorf("err = %v, want it to name the host types", err)
	}

	id, _, _, _, err := buildNodeForTool(doc, "code", "", "2", nil, true)
	if err != nil {
		t.Fatalf("buildNodeForTool inside a process = %v", err)
	}
	n, ok := doc.node(id)
	if !ok || !n.Data.Concurrent {
		t.Errorf("node %s = %+v, want data.concurrent stored", id, n)
	}
}

// Membership belongs to steps that can wait: a control node expresses control flow on the body's chain, an annotation
// never executes, result / progress append to the run's own output in call order, and assign / memory finish inline. flowcheck
// reports the flag as an error - the write refuses the same types instead of storing the finding.
func TestBuildMemberRefusesControlAndOutputTypes(t *testing.T) {
	for _, typ := range []string{"break", "continue", "return", "exit", "comment", "layout", "result", "progress", "assign", "memory"} {
		doc, err := parseFlowDoc([]byte(scopeDiagram))
		if err != nil {
			t.Fatalf("parseFlowDoc = %v", err)
		}
		_, _, _, _, err = buildNodeForTool(doc, typ, "", "2", nil, true)
		if err == nil {
			t.Errorf("%s as a concurrent member = nil, want a refusal", typ)
		} else if !strings.Contains(err.Error(), "concurrent member") {
			t.Errorf("%s as a concurrent member: err = %v, want it to name the membership rule", typ, err)
		}
	}
}

// The update path carries the same refusal: without it a node created correctly could be flagged afterwards, and the tool
// would store a diagram the validator reports as an error.
func TestUpdateNodeMemberRefusesOutputNode(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "result", "parent_node": "7",
	})
	if err != nil {
		t.Fatalf("create result inside a process = %v", err)
	}
	node, _ := out.(map[string]any)["node"].(map[string]any)
	id, _ := node["id"].(string)
	if id == "" {
		t.Fatalf("created node = %v, want an id", out)
	}
	_, err = (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": id, "concurrent": true,
	})
	if err == nil || !strings.Contains(err.Error(), "concurrent member") {
		t.Fatalf("update_node on a result node = %v, want a refusal naming the membership rule", err)
	}
}
