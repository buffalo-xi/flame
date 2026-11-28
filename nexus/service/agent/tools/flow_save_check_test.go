package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A node reading its own outputs can never be satisfied: config.inputs is resolved before the node runs, so the value does
// not exist yet. The write boundary refuses it instead of storing a diagram that fails on every run.
//
// A while / do_while loop is the one exception (next test): its condition re-resolves config.inputs every round, so a loop
// reading what its body wrote back is a legal accumulator. Refusing it refused a runnable definition.
func TestSaveDiagramRefusesSelfReference(t *testing.T) {
	const diagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "累计", "config": {
      "inputs": {"acc": {"type": "number", "source_type": "node", "source_ref": "累计", "source_key": "sum"}},
      "outputs": {"sum": {"type": "number"}},
      "code": {"script": "return { sum = 1 }"}
    }}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
  ]
}`
	c, fk := edgeTestCtx(diagram)
	doc, err := parseFlowDoc(json.RawMessage(diagram))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	if _, err := saveDiagram(context.Background(), c, fk.detail, doc); err == nil {
		t.Fatal("a self reference must be refused")
	} else if !strings.Contains(err.Error(), "own outputs") {
		t.Errorf("err = %q, want it to name the self reference", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("a refused diagram must not be written")
	}
}

// The accumulator shape is storable: the loop's condition reads the loop's own output through config.inputs, which the
// runtime re-resolves before every check (loop.go conditionInputs). It used to be refused as a self reference while the
// engine ran it fine.
func TestSaveDiagramAllowsWhileAccumulator(t *testing.T) {
	const diagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "loop", "data": {"label": "累计", "config": {
      "inputs": {"acc": {"type": "number", "source_type": "node", "source_ref": "累计", "source_key": "sum"}},
      "outputs": {"sum": {"type": "number"}},
      "loop": {"type": "while", "max_times": 3, "condition": "return input.acc < 3"}
    }}},
    {"id": "3", "type": "entry", "parent_node": "2"},
    {"id": "4", "type": "code", "parent_node": "2", "data": {"label": "加", "config": {
      "code": {"script": "return { v = 1 }"},
      "outputs": {"v": {"type": "number"}},
      "targets": {"sum": {"source_type": "node", "source_ref": "累计", "source_key": "sum"}}
    }}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"},
    {"id": "e2", "source": "3", "target": "4", "source_handle": "source3right", "target_handle": "target4left"}
  ]
}`
	c, fk := edgeTestCtx(diagram)
	doc, err := parseFlowDoc(json.RawMessage(diagram))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	if _, err := saveDiagram(context.Background(), c, fk.detail, doc); err != nil {
		t.Fatalf("a while accumulator must be storable: %v", err)
	}
	if len(fk.updatedDiagram) == 0 {
		t.Error("the diagram should have been written")
	}
}

// The normal state between two tool calls is an unfinished graph: unconnected nodes, a missing entry child, a config still
// being filled in. Those findings must not block a write, or the step-by-step creation tools could never build anything.
func TestSaveDiagramAllowsAnUnfinishedDiagram(t *testing.T) {
	const diagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "计算"}}
  ],
  "edges": []
}`
	c, fk := edgeTestCtx(diagram)
	doc, err := parseFlowDoc(json.RawMessage(diagram))
	if err != nil {
		t.Fatalf("parseFlowDoc = %v", err)
	}
	if _, err := saveDiagram(context.Background(), c, fk.detail, doc); err != nil {
		t.Fatalf("an unfinished diagram must still be stored: %v", err)
	}
	if len(fk.updatedDiagram) == 0 {
		t.Error("the diagram was not written")
	}
}

// update_node refuses an input that reads the node itself before the diagram is written: the node's own outputs and
// locals are empty until it runs.
func TestCheckSelfInputReference(t *testing.T) {
	cases := []struct {
		name    string
		nodeID  string
		label   string
		cfg     map[string]any
		wantErr bool
	}{
		{"own id", "2", "累计", map[string]any{"inputs": map[string]any{
			"acc": map[string]any{"source_type": "node", "source_ref": "2", "source_key": "sum"},
		}}, true},
		{"own label", "2", "累计", map[string]any{"inputs": map[string]any{
			"acc": map[string]any{"source_type": "node", "source_ref": "累计", "source_key": "sum"},
		}}, true},
		{"upstream node", "2", "累计", map[string]any{"inputs": map[string]any{
			"x": map[string]any{"source_type": "node", "source_ref": "1", "source_key": "y"},
		}}, false},
		{"not a node source", "2", "累计", map[string]any{"inputs": map[string]any{
			"x": map[string]any{"source_type": "flow", "source_key": "y"},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSelfInputReference(tc.nodeID, tc.label, tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
