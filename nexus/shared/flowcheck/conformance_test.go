package flowcheck

import (
	"sort"
	"testing"
)

// TestEveryNodeTypeConformance is the per-type contract the create tools, the validate endpoint and the canvas badge all
// rely on, in one table: what a complete config looks like (no finding at all) and what an empty config costs.
//
// The table is checked against knownTypes in both directions, so a new node type - or a new required key on an existing
// one - cannot slip through without a row here. When a rule changes, this is the file that has to be updated with it.
func TestEveryNodeTypeConformance(t *testing.T) {
	cases := []struct {
		typ   string
		valid map[string]any
		// empty is what an absent config produces, severity included: "error:incomplete" means the step cannot run,
		// "warning:no_op" means it runs and does nothing.
		empty []string
	}{
		{
			typ: "code",
			valid: map[string]any{
				"code":    map[string]any{"lang": "template", "script": "return {}"},
				"outputs": map[string]any{"x": map[string]any{"type": "string"}},
			},
			empty: []string{"error:incomplete", "warning:outputs"},
		},
		{
			typ:   "http",
			valid: map[string]any{"http": map[string]any{"url": "https://example.com"}},
			empty: []string{"error:incomplete"},
		},
		{
			typ: "llm",
			valid: map[string]any{"llm": map[string]any{
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "mcp",
			valid: map[string]any{"mcp": map[string]any{"connection_id": "c", "tool_name": "t"}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "rpc",
			valid: map[string]any{"rpc": map[string]any{"target_type": "endpoint", "endpoint": "e"}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "branch",
			valid: map[string]any{"children": []any{map[string]any{"id": "c1", "condition": "return true"}}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "intent",
			valid: map[string]any{"children": []any{map[string]any{"id": "i1", "label": "refund", "description": "a refund"}}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "loop",
			valid: map[string]any{"loop": map[string]any{"type": "count", "total_times": 2}},
			empty: []string{"error:incomplete"},
		},
		// A container's entry child is a diagram question (it needs the children), not a per-node one.
		{typ: "process", empty: nil},
		{typ: "function", empty: nil},
		{
			typ:   "call",
			valid: map[string]any{"target": "2"},
			empty: []string{"error:incomplete"},
		},
		{
			typ: "assign",
			valid: map[string]any{"assignments": []any{
				map[string]any{"kind": "output", "target": "x", "value": "1"},
			}},
			empty: []string{"warning:no_op"},
		},
		{typ: "memory", empty: nil},
		{
			typ:   "wait",
			valid: map[string]any{"fork_node_id": "2"},
			empty: []string{"error:incomplete"},
		},
		{typ: "fork", empty: nil},
		{
			typ:   "result",
			valid: map[string]any{"code": map[string]any{"script": "x"}},
			empty: []string{"error:incomplete"},
		},
		{
			typ:   "progress",
			valid: map[string]any{"code": map[string]any{"script": "x"}},
			empty: []string{"error:incomplete"},
		},
		// Control signals have nothing to configure; where they may sit is a diagram question.
		{typ: "break", empty: nil},
		{typ: "continue", empty: nil},
		{typ: "return", empty: nil},
		{typ: "exit", empty: nil},
		// Canvas-only kinds: the runtime never reaches them, so no rule applies.
		{typ: "start", empty: nil},
		{typ: "entry", empty: nil},
		{typ: "comment", empty: nil},
		{typ: "layout", empty: nil},
		{typ: "knowledge", empty: nil},
	}

	seen := make(map[string]bool, len(cases))
	for _, tc := range cases {
		if seen[tc.typ] {
			t.Errorf("%s has more than one conformance row", tc.typ)
		}
		seen[tc.typ] = true

		t.Run(tc.typ+"/valid", func(t *testing.T) {
			fs, err := Step(tc.typ, tc.valid, nil)
			if err != nil {
				t.Fatalf("Step(%s, valid) = %v, want no shape error", tc.typ, err)
			}
			if len(fs) != 0 {
				t.Fatalf("a complete %s config must be silent, got %v", tc.typ, codes(fs))
			}
		})

		t.Run(tc.typ+"/empty", func(t *testing.T) {
			fs, err := Step(tc.typ, nil, nil)
			if err != nil {
				t.Fatalf("Step(%s, nil) = %v, want no shape error", tc.typ, err)
			}
			got := codes(fs)
			sort.Strings(got)
			want := append([]string(nil), tc.empty...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("an empty %s config: got %v, want %v", tc.typ, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("an empty %s config: got %v, want %v", tc.typ, got, want)
				}
			}
		})
	}

	for typ := range knownTypes {
		if !seen[typ] {
			t.Errorf("%s is in knownTypes but has no conformance row", typ)
		}
	}
}
