package flowcheck

import "testing"

func branchDiagram(edges []any) map[string]any {
	return map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			map[string]any{"id": "2", "type": "branch", "data": map[string]any{"config": map[string]any{
				"children": []any{
					map[string]any{"id": "c1", "condition": map[string]any{"lang": "lua", "script": "return true"}},
					map[string]any{"id": "c2", "condition": map[string]any{"lang": "lua", "script": "return true"}},
				},
			}}},
			testNode("3", "code", ""),
			testNode("4", "code", ""),
			testNode("5", "code", ""),
		},
		"edges": edges,
	}
}

func handleEdge(id, src, dst, handle string) map[string]any {
	e := testEdge(id, src, dst)
	e["source_handle"] = handle
	return e
}

// A branch child owns one outlet: left and right are the same outlet, so two edges would route one child to two next nodes.
func TestBranchChildRoutesThroughOneEdge(t *testing.T) {
	fs := checkDiagramMap(t, branchDiagram([]any{
		testEdge("e1", "1", "2"),
		handleEdge("e2", "2", "3", "sourcec1left"),
		handleEdge("e3", "2", "4", "sourcec1right"),
		handleEdge("e4", "2", "5", "sourcec2left"),
	}))
	if !has(fs, SeverityError, CodeBranchOutlet) {
		t.Fatalf("codes = %v, want branch_outlet for the doubly routed child", codes(fs))
	}
}

// One edge per child is the normal shape and must stay clean.
func TestBranchOneEdgePerChildIsClean(t *testing.T) {
	fs := checkDiagramMap(t, branchDiagram([]any{
		testEdge("e1", "1", "2"),
		handleEdge("e2", "2", "3", "sourcec1left"),
		handleEdge("e3", "2", "4", "sourcec2left"),
	}))
	if has(fs, SeverityError, CodeBranchOutlet) {
		t.Fatalf("codes = %v, want no branch_outlet", codes(fs))
	}
}

// A handle naming neither the node itself nor a declared child routes nowhere at run time.
func TestBranchHandleMustNameADeclaredOutlet(t *testing.T) {
	fs := checkDiagramMap(t, branchDiagram([]any{
		testEdge("e1", "1", "2"),
		handleEdge("e2", "2", "3", "sourceGhostLeft"),
		handleEdge("e3", "2", "4", "sourcec2left"),
	}))
	if !has(fs, SeverityError, CodeBranchOutlet) {
		t.Fatalf("codes = %v, want branch_outlet for the unknown handle", codes(fs))
	}
}

// A child id that is a prefix of another must not claim the longer child's edge: with cA / cA2, sourcecA2left also starts
// with "sourcecA", so a correctly wired diagram was reported as "cA routes through 2 edges".
func TestBranchPrefixedChildIDsKeepTheirOwnOutlets(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			map[string]any{"id": "2", "type": "branch", "data": map[string]any{"config": map[string]any{
				"children": []any{
					map[string]any{"id": "cA", "condition": map[string]any{"lang": "lua", "script": "return true"}},
					map[string]any{"id": "cA2", "condition": map[string]any{"lang": "lua", "script": "return true"}},
				},
			}}},
			testNode("3", "code", ""),
			testNode("4", "code", ""),
		},
		"edges": []any{
			testEdge("e1", "1", "2"),
			handleEdge("e2", "2", "3", "sourcecA2left"),
			handleEdge("e3", "2", "4", "sourcecAleft"),
		},
	})
	if has(fs, SeverityError, CodeBranchOutlet) || has(fs, SeverityError, CodeBranchEdge) {
		t.Fatalf("codes = %v, want prefixed child ids to keep their own outlets", codes(fs))
	}
}
