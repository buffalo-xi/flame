package flowcheck

import (
	"encoding/json"
	"testing"
)

func checkDiagramMap(t *testing.T, v any) []Finding {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fs, err := Diagram(raw, nil)
	if err != nil {
		t.Fatalf("Diagram: %v", err)
	}
	return fs
}

func testNode(id, typ, parent string) map[string]any {
	n := map[string]any{"id": id, "type": typ, "data": map[string]any{}}
	if parent != "" {
		n["parent_node"] = parent
	}
	return n
}

func testEdge(id, src, dst string) map[string]any {
	return map[string]any{"id": id, "source": src, "target": dst}
}

// A return in a process body ends the whole flow, not the container: the signal has no consumer between it and the root, and the
// run still reports success. A shape that reads as "leave this container" and does something else is refused, not explained away.
func TestReturnScopeInProcessBody(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			testNode("2", "process", ""),
			testNode("4", "entry", "2"),
			testNode("5", "return", "2"),
		},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "4", "5")},
	})
	if !has(fs, SeverityError, CodeReturnScope) {
		t.Fatalf("codes = %v, want the return_scope error", codes(fs))
	}
}

// The same in a loop body: break / continue are the loop's signals, a return is not.
func TestReturnScopeInLoopBody(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			testNode("2", "loop", ""),
			testNode("4", "entry", "2"),
			testNode("5", "return", "2"),
		},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "4", "5")},
	})
	if !has(fs, SeverityError, CodeReturnScope) {
		t.Fatalf("codes = %v, want the return_scope error", codes(fs))
	}
}

// A function anywhere above consumes the signal, so a return nested in a process inside a function body really does leave the
// function - reporting it would be a false positive on the one layout where a return is legal.
func TestReturnScopeAllowsFunctionAncestor(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{
			testNode("1", "start", ""),
			testNode("f1", "function", ""),
			testNode("fe", "entry", "f1"),
			testNode("p1", "process", "f1"),
			testNode("pe", "entry", "p1"),
			testNode("r1", "return", "p1"),
		},
		"edges": []any{
			testEdge("e1", "1", "f1"),
			testEdge("e2", "fe", "p1"),
			testEdge("e3", "pe", "r1"),
		},
	})
	if has(fs, SeverityError, CodeReturnScope) || has(fs, SeverityWarning, CodeReturnScope) {
		t.Fatalf("codes = %v, want no return_scope under a function", codes(fs))
	}
}

// The rule is "a function ancestor", not "not in a process / loop": a top-level return is the same effect as exit, so it is
// reported too instead of leaving a second spelling of "end the whole flow" around.
func TestReturnScopeTopLevelIsError(t *testing.T) {
	fs := checkDiagramMap(t, map[string]any{
		"nodes": []any{testNode("1", "start", ""), testNode("2", "return", "")},
		"edges": []any{testEdge("e1", "1", "2")},
	})
	if !has(fs, SeverityError, CodeReturnScope) {
		t.Fatalf("codes = %v, want the return_scope error at the top level", codes(fs))
	}
}
