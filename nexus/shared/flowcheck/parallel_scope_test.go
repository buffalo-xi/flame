package flowcheck

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// psNode builds a node of the scope model: a member is the same node with data.concurrent set.
func psNode(id, typ, parent string, concurrent bool, cfg map[string]any) map[string]any {
	data := map[string]any{}
	if cfg != nil {
		data["config"] = cfg
	}
	if concurrent {
		data["concurrent"] = true
	}
	n := map[string]any{"id": id, "type": typ, "data": data}
	if parent != "" {
		n["parent_node"] = parent
	}
	return n
}

// psDiagram wires a process body entry -> first -> second, with the process itself hanging off the start node.
func psDiagram(process map[string]any, first, second map[string]any) map[string]any {
	return map[string]any{
		"nodes": []any{
			psNode("1", "start", "", false, nil),
			process,
			psNode("entry-2", "entry", "2", false, nil),
			first,
			second,
		},
		"edges": []any{
			testEdge("e1", "1", "2"),
			testEdge("e2", "entry-2", "3"),
			testEdge("e3", "3", "4"),
		},
	}
}

// Two members on the body's entry chain are the shape the model exists for: validation must stay quiet.
func TestParallelScopeTwoMembersIsClean(t *testing.T) {
	fs := checkDiagramMap(t, psDiagram(
		psNode("2", "process", "", false, map[string]any{"parallel": map[string]any{"max_concurrency": 2}}),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", true, nil),
	))
	for _, code := range []string{CodeConcurrentMemberParent, CodeConcurrentMemberOnChain, CodeParallelVisibility, CodeConcurrentMemberWrite} {
		if has(fs, SeverityError, code) {
			t.Fatalf("codes = %v, want no %s", codes(fs), code)
		}
	}
}

// The top level is not a host: a member there has no scope to belong to.
func TestParallelScopeTopLevelMember(t *testing.T) {
	diagram := map[string]any{
		"nodes": []any{psNode("1", "start", "", false, nil), psNode("3", "code", "", true, nil)},
		"edges": []any{testEdge("e1", "1", "3")},
	}
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeConcurrentMemberParent) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberParent)
	}
}

// A function body is not a host either, even though it is a container.
func TestParallelScopeMemberInsideFunction(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "function", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", false, nil),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeConcurrentMemberParent) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberParent)
	}
}

// A member whose parent is the host but which hangs outside the entry chain is never dispatched: the flag would read as
// "parallel" while the node runs nowhere.
func TestParallelScopeMemberOffChain(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("5", "code", "2", true, nil), // no edge reaches it
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeConcurrentMemberOnChain) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberOnChain)
	}
}

// A chain node reading a member reads a value the join has not produced yet.
func TestParallelScopeChainNodeReadsMember(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", false, map[string]any{
			"inputs": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "3"}},
		}),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeParallelVisibility) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelVisibility)
	}
}

// Two members cannot read each other either.
func TestParallelScopeMemberReadsSibling(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", true, map[string]any{
			"inputs": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "3"}},
		}),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeParallelVisibility) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelVisibility)
	}
}

// A member writing a flow variable is the unsynchronised, last-write-wins channel the model replaces.
func TestParallelScopeMemberWritesFlowVar(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, map[string]any{
			"targets": map[string]any{"x": map[string]any{"source_type": "flow"}},
		}),
		psNode("4", "code", "2", false, nil),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeConcurrentMemberWrite) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberWrite)
	}
}

// Two writers to one host output, at least one of them a member: only one value survives the join, so the other is lost
// silently.
func TestParallelScopeOutputConflict(t *testing.T) {
	process := psNode("2", "process", "", false, map[string]any{
		"outputs": map[string]any{"x": map[string]any{}},
	})
	first := psNode("3", "code", "2", true, map[string]any{
		"targets": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "2"}},
	})
	second := psNode("4", "code", "2", false, map[string]any{
		"targets": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "2"}},
	})
	fs := checkDiagramMap(t, psDiagram(process, first, second))
	if !has(fs, SeverityError, CodeParallelOutputConflict) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelOutputConflict)
	}
}

// Two serial steps in one body writing the same output is the pre-existing behaviour, not this model's business.
func TestParallelScopeSerialWritersAreLeftAlone(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, map[string]any{"outputs": map[string]any{"x": map[string]any{}}}),
		psNode("3", "code", "2", false, map[string]any{
			"targets": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "2"}},
		}),
		psNode("4", "code", "2", false, map[string]any{
			"targets": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "2"}},
		}),
	)
	fs := checkDiagramMap(t, diagram)
	if has(fs, SeverityError, CodeParallelOutputConflict) {
		t.Fatalf("codes = %v, want no conflict without a member", codes(fs))
	}
}

// One member is a scope in name only.
func TestParallelScopeSingleMemberIsDegenerate(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", false, nil),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityWarning, CodeParallelDegenerate) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelDegenerate)
	}
}

// A limit of 1 with several members runs strictly serially while the diagram claims concurrency.
func TestParallelScopeLimitOneIsDegenerate(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, map[string]any{"parallel": map[string]any{"max_concurrency": 1}}),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", true, nil),
	)
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityWarning, CodeParallelDegenerate) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelDegenerate)
	}
}

// Writing into the host's own outputs is the sanctioned channel and must not be flagged.
func TestParallelScopeMemberWritesHostOutputs(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, map[string]any{"outputs": map[string]any{"x": map[string]any{}}}),
		psNode("3", "code", "2", true, map[string]any{
			"targets": map[string]any{"x": map[string]any{"source_type": "node", "source_ref": "2"}},
		}),
		psNode("4", "code", "2", false, nil),
	)
	fs := checkDiagramMap(t, diagram)
	if has(fs, SeverityError, CodeConcurrentMemberWrite) {
		t.Fatalf("codes = %v, want the host's outputs to be allowed", codes(fs))
	}
}

// pfNested wires hosts nested one inside the next: start -> h1 -> (entry-h1) h2 -> ... -> leaf, each hN a process with
// the given max_concurrency (0 = unlimited).
func pfNested(limits ...int) map[string]any {
	nodes := []any{psNode("1", "start", "", false, nil)}
	edges := []any{}
	from := "1"
	for i, limit := range limits {
		hostID := fmt.Sprintf("h%d", i+1)
		cfg := map[string]any{}
		if limit > 0 {
			cfg["parallel"] = map[string]any{"max_concurrency": limit}
		}
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("h%d", i)
		}
		nodes = append(nodes, psNode(hostID, "process", parent, false, cfg))
		nodes = append(nodes, psNode("entry-"+hostID, "entry", hostID, false, nil))
		edges = append(edges, testEdge(fmt.Sprintf("e-host-%d", i+1), from, hostID))
		from = "entry-" + hostID
	}
	nodes = append(nodes, psNode("leaf", "code", fmt.Sprintf("h%d", len(limits)), false, nil))
	edges = append(edges, testEdge("e-leaf", from, "leaf"))
	return map[string]any{"nodes": nodes, "edges": edges}
}

// fanoutMessages keeps the fan-out messages so a test can tell the depth rule from the product rule.
func fanoutMessages(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		if f.Code == CodeParallelFanout {
			out = append(out, f.Message)
		}
	}
	return out
}

// Two nested hosts are a normal shape; at three the number of nodes in flight stops being visible on the canvas, so it is
// a warning that names the ancestor chain.
func TestParallelScopeFanoutNestingDepth(t *testing.T) {
	if fs := checkDiagramMap(t, pfNested(2, 2)); has(fs, SeverityWarning, CodeParallelFanout) {
		t.Fatalf("codes = %v, want no %s for two levels", codes(fs), CodeParallelFanout)
	}

	fs := checkDiagramMap(t, pfNested(2, 2, 2))
	msgs := fanoutMessages(fs)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "nesting depth 3") {
		t.Fatalf("fanout = %v, want one depth-3 warning (the limits multiply to 8, which is allowed)", msgs)
	}
	if !strings.Contains(msgs[0], "h1 > h2") {
		t.Errorf("msg = %q, want the ancestors named outermost first", msgs[0])
	}
}

// Nested limits multiply: 4 x 4 means up to 16 members in flight at the innermost level, not 4.
func TestParallelScopeFanoutMultipliesLimits(t *testing.T) {
	fs := checkDiagramMap(t, pfNested(4, 4))
	msgs := fanoutMessages(fs)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "multiply to 16") {
		t.Fatalf("fanout = %v, want the multiplied limit", msgs)
	}

	// 2 x 3 stays under the threshold. An unlimited level has no ceiling to multiply, so a product through it says
	// nothing - warning there would fire on the default (unlimited) of every nested host.
	if fs := checkDiagramMap(t, pfNested(2, 3)); has(fs, SeverityWarning, CodeParallelFanout) {
		t.Errorf("codes = %v, want no %s for 2 x 3", codes(fs), CodeParallelFanout)
	}
	if fs := checkDiagramMap(t, pfNested(0, 4)); has(fs, SeverityWarning, CodeParallelFanout) {
		t.Errorf("codes = %v, want no %s when a level is unlimited", codes(fs), CodeParallelFanout)
	}
}

// Control, output and instant nodes cannot be members: `exit` / `break` / `continue` / `return` express control flow on the
// chain, an annotation never executes, `result` / `progress` append to the run's own output in call order, and
// `assign` / `memory` finish inline. Membership belongs to steps that can wait - an `exit` inside a member's own body still
// ends the run through the nested host.
func TestParallelScopeControlNodeCannotBeMember(t *testing.T) {
	for _, typ := range []string{"exit", "break", "continue", "return", "comment", "result", "progress", "assign", "memory"} {
		t.Run(typ, func(t *testing.T) {
			diagram := psDiagram(
				psNode("2", "process", "", false, nil),
				psNode("3", typ, "2", true, nil),
				psNode("4", "code", "2", false, map[string]any{"code": map[string]any{"script": "return {}"}}),
			)
			fs := checkDiagramMap(t, diagram)
			if !has(fs, SeverityError, CodeConcurrentMemberType) {
				t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberType)
			}
		})
	}
}

// The body's entry is the chain itself: flagging it is the same mistake, and it would leave the body without a head.
func TestParallelScopeEntryCannotBeMember(t *testing.T) {
	diagram := map[string]any{
		"nodes": []any{
			psNode("1", "start", "", false, nil),
			psNode("2", "process", "", false, nil),
			psNode("entry-2", "entry", "2", true, nil),
			psNode("3", "code", "2", false, map[string]any{"code": map[string]any{"script": "return {}"}}),
		},
		"edges": []any{
			testEdge("e1", "1", "2"),
			testEdge("e2", "entry-2", "3"),
		},
	}
	fs := checkDiagramMap(t, diagram)
	if !has(fs, SeverityError, CodeConcurrentMemberType) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeConcurrentMemberType)
	}
}

// The one parallel rule that needs the tenant's configuration rather than the diagram alone: more members than the
// ceiling still runs, but the extras queue - which the canvas cannot show.
func TestScopeOverCeilingMembersQueue(t *testing.T) {
	diagram := psDiagram(
		psNode("2", "process", "", false, nil),
		psNode("3", "code", "2", true, nil),
		psNode("4", "code", "2", true, nil),
	)
	raw, err := json.Marshal(diagram)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if fs := ScopeOverCeiling(raw, 0); len(fs) != 0 {
		t.Fatalf("codes = %v, want no findings without a ceiling", codes(fs))
	}
	if fs := ScopeOverCeiling(raw, 2); len(fs) != 0 {
		t.Fatalf("codes = %v, want no findings when both members fit", codes(fs))
	}
	fs := ScopeOverCeiling(raw, 1)
	if !has(fs, SeverityWarning, CodeParallelMembersQueued) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeParallelMembersQueued)
	}
	if !strings.Contains(fs[0].Message, "2 concurrent members") {
		t.Errorf("msg = %q, want the member count", fs[0].Message)
	}
	if fs[0].NodeID != "2" {
		t.Errorf("node = %q, want the host", fs[0].NodeID)
	}
}
