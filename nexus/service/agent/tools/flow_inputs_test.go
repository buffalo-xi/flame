package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A process node reads no config.inputs: the body chain reaches the outer nodes directly, so a copied inputs block is dropped
// instead of stored. It is dropped with a warning rather than silently, otherwise the author never learns the values are gone.
func TestCreateNodeStripsInputsOnProcess(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "process",
		"config": map[string]any{
			"inputs": map[string]any{"x": map[string]any{
				"type": "string", "source_type": "node", "source_ref": "5", "source_key": "out",
			}},
			"outputs": map[string]any{"summary": map[string]any{"type": "string"}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	node, _ := m["node"].(map[string]any)
	id, _ := node["id"].(string)

	data, _ := storedNodes(t, fk)[id]["data"].(map[string]any)
	cfg, _ := data["config"].(map[string]any)
	if _, ok := cfg["inputs"]; ok {
		t.Errorf("config.inputs was stored on a process node: %v", cfg)
	}
	if _, ok := cfg["outputs"]; !ok {
		t.Errorf("the rest of the config must survive: %v", cfg)
	}
	if w := fmt.Sprint(m["warnings"]); !strings.Contains(w, "config.inputs removed") {
		t.Errorf("warnings = %v, want the removal echoed", m["warnings"])
	}
}

// The same rule on the update path: a patch that adds inputs to an existing process node loses the key.
func TestUpdateNodeStripsInputsOnProcess(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "7",
		"config_patch": map[string]any{
			"inputs":  map[string]any{"x": map[string]any{"type": "string", "source_type": "flow", "source_key": "k"}},
			"outputs": map[string]any{"summary": map[string]any{"type": "string"}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cfg, _ := out.(map[string]any)["config"].(map[string]any)
	if _, ok := cfg["inputs"]; ok {
		t.Errorf("config.inputs was stored on a process node: %v", cfg)
	}
	if _, ok := cfg["outputs"]; !ok {
		t.Errorf("the patch itself must still apply: %v", cfg)
	}
	if _, ok := storedNodes(t, fk)["7"]; !ok {
		t.Error("the diagram was not written back")
	}
	if w := fmt.Sprint(out.(map[string]any)["warnings"]); !strings.Contains(w, "config.inputs removed") {
		t.Errorf("warnings = %v, want the removal echoed", out.(map[string]any)["warnings"])
	}
}

// A code node really reads inputs, so the same key must be left alone there - the rule is about the type, not about the key.
func TestCreateNodeKeepsInputsOnCode(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "code",
		"config": map[string]any{
			"code": map[string]any{"lang": "lua", "script": "return { out = input.x }"},
			"inputs": map[string]any{"x": map[string]any{
				"type": "string", "source_type": "node", "source_ref": "5", "source_key": "out",
			}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	node, _ := out.(map[string]any)["node"].(map[string]any)
	id, _ := node["id"].(string)
	data, _ := storedNodes(t, fk)[id]["data"].(map[string]any)
	cfg, _ := data["config"].(map[string]any)
	if _, ok := cfg["inputs"]; !ok {
		t.Errorf("config.inputs was dropped from a code node: %v", cfg)
	}
}
