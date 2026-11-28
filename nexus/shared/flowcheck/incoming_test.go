package flowcheck

import "testing"

// A node is reached through one incoming edge: a second one is dead weight, and the runner never follows it.
func TestNodeTakesOneIncomingEdge(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			testNode("2", "code", ""),
			testNode("3", "code", ""),
			testNode("4", "code", ""),
		},
		"edges": []any{
			testEdge("e1", "1", "2"),
			testEdge("e2", "2", "4"),
			testEdge("e3", "3", "4"),
		},
	})
	if !has(fs, SeverityError, CodeMultiIncoming) {
		t.Fatalf("codes = %v, want multi_incoming on node 4", codes(fs))
	}
}

// start and entry are entered by the runner, never by an edge; function is covered by function_edge.
func TestStartAndEntryTakeNoIncomingEdge(t *testing.T) {
	cases := []struct {
		name  string
		nodes []any
		edges []any
	}{
		{
			name:  "edge into start",
			nodes: []any{testNode("1", "start", ""), testNode("2", "code", "")},
			edges: []any{testEdge("e1", "2", "1")},
		},
		{
			name: "edge into entry",
			nodes: []any{
				testNode("1", "start", ""),
				testNode("2", "process", ""),
				testNode("3", "entry", "2"),
				testNode("4", "code", "2"),
			},
			edges: []any{
				testEdge("e1", "1", "2"),
				testEdge("e2", "4", "3"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkDiagramMap(t, map[string]any{"nodes": tc.nodes, "edges": tc.edges})
			if !has(fs, SeverityError, CodeNoIncoming) {
				t.Fatalf("codes = %v, want no_incoming", codes(fs))
			}
		})
	}
}

// A terminal node has no outlet: an edge leaving exit / break / continue / return is never followed.
func TestTerminalNodeTakesNoOutgoingEdge(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			testNode("2", "exit", ""),
			testNode("3", "code", ""),
		},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "2", "3")},
	})
	if !has(fs, SeverityError, CodeNoOutgoing) {
		t.Fatalf("codes = %v, want no_outgoing on the exit node", codes(fs))
	}
}

// The normal shape stays quiet: one edge in, one edge out.
func TestSingleIncomingAndOutgoingIsClean(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{testNode("1", "start", ""), testNode("2", "code", ""), testNode("3", "exit", "")},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "2", "3")},
	})
	for _, code := range []string{CodeMultiIncoming, CodeNoIncoming, CodeNoOutgoing, CodeMultiOutgoing} {
		if has(fs, SeverityError, code) {
			t.Fatalf("codes = %v, want no %s", codes(fs), code)
		}
	}
}
