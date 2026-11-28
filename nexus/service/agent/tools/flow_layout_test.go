package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Layout rule: the main chain is one horizontal row, and the branches of a branch / intent card open to the right of the card -
// the first branch row on the card's own row (left inlet, straight out of the sub-branch's right outlet), the rest stacked
// below it (top inlet). On a wrap the branch's first node takes its incoming edge at the top.
// These are the baseline for "a graph the tool built is readable"; make them pass first when changing the layout.
func TestLayoutDiagramMainChainAndBranches(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	llm := b.addNode("llm", "总结", nil, 0)
	b.addEdge("1", llm, "")
	br := b.addNode("branch", "分流", nil, 0)
	b.addEdge(llm, br, "")

	a1 := b.addNode("code", "甲", nil, 1)
	b.addEdge(br, a1, "sourcec1right")
	a2 := b.addNode("code", "甲二", nil, 1)
	b.addEdge(a1, a2, "")
	d1 := b.addNode("code", "乙", nil, 1)
	b.addEdge(br, d1, "sourcec2right")

	b.layoutDiagram()
	pos := func(id string) (int, int) {
		for i := range b.nodes {
			if b.nodes[i].ID == id {
				return b.nodes[i].Position["x"], b.nodes[i].Position["y"]
			}
		}
		t.Fatalf("node %s does not exist", id)
		return 0, 0
	}

	sx, sy := pos("1")
	lx, ly := pos(llm)
	bx, by := pos(br)
	if sy != ly || ly != by {
		t.Errorf("the main chain is not on one row: start=(%d,%d) llm=(%d,%d) branch=(%d,%d)", sx, sy, lx, ly, bx, by)
	}
	if !(sx < lx && lx < bx) {
		t.Errorf("the main chain does not run left to right: x = %d / %d / %d", sx, lx, bx)
	}

	ax, ay := pos(a1)
	a2x, a2y := pos(a2)
	dx, dy := pos(d1)
	if ax < bx+300+layoutColGap {
		t.Errorf("the branch row should open to the right of the card: branch head x=%d, card x=%d", ax, bx)
	}
	if ay != by {
		t.Errorf("the first branch row should share the card's row: head y=%d, card y=%d", ay, by)
	}
	if a2y != ay || a2x <= ax {
		t.Errorf("nodes in one branch should continue to the right: a1=(%d,%d) a2=(%d,%d)", ax, ay, a2x, a2y)
	}
	if dx != ax {
		t.Errorf("branch rows should start at the same column: d1 x=%d, a1 x=%d", dx, ax)
	}
	if dy <= ay {
		t.Errorf("the second branch is not below the first: d1=(%d,%d) a1=(%d,%d)", dx, dy, ax, ay)
	}

	want := map[string]string{a1: "left", d1: "top"}
	for _, e := range b.edges {
		inlet, ok := want[e.Target]
		if !ok {
			continue
		}
		if e.TargetHandle == nil || *e.TargetHandle != "target"+e.Target+inlet {
			t.Errorf("branch head %s should enter via %s, got = %v", e.Target, inlet, e.TargetHandle)
		}
	}
}

// When the main chain does not fit on one row it wraps, clearing the height the previous row's branches occupy.
func TestLayoutDiagramWrapsLongChain(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	prev := "1"
	ids := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		id := b.addNode("code", "步骤长名字用来撑宽度", nil, 0)
		b.addEdge(prev, id, "")
		prev = id
		ids = append(ids, id)
	}
	b.layoutDiagram()

	rows := 1
	lastY := b.nodes[0].Position["y"]
	for _, id := range ids {
		for i := range b.nodes {
			if b.nodes[i].ID != id {
				continue
			}
			y := b.nodes[i].Position["y"]
			if y != lastY {
				if y <= lastY {
					t.Fatalf("after a wrap y did not move down: %d -> %d", lastY, y)
				}
				rows++
				lastY = y
			}
			if x := b.nodes[i].Position["x"]; x > layoutMarginX+layoutRowMaxWidth {
				t.Errorf("node %s has x=%d, beyond the single-row budget %d measured from the row start",
					id, x, layoutRowMaxWidth)
			}
		}
	}
	if rows < 2 {
		t.Errorf("12 wide nodes should wrap, got only %d row(s)", rows)
	}
}

// A branch row begins at the branch card's right edge, but its width budget is measured from that start: a chain that would
// not fit under an absolute cap still stays on one row instead of wrapping early.
func TestLayoutDiagramBranchRowKeepsFullWidth(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	br := b.addNode("branch", "分流", nil, 0)
	b.addEdge("1", br, "")
	prev, handle := br, "sourcec1right"
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		id := b.addNode("code", "步骤长名字用来撑宽度", nil, 1)
		b.addEdge(prev, id, handle)
		prev, handle = id, ""
		ids = append(ids, id)
	}
	b.layoutDiagram()

	wantY := 0
	for n, id := range ids {
		for i := range b.nodes {
			if b.nodes[i].ID != id {
				continue
			}
			y := b.nodes[i].Position["y"]
			if n == 0 {
				wantY = y
			} else if y != wantY {
				t.Fatalf("the branch row wrapped even though its budget starts at the row start: %s y=%d, want %d", id, y, wantY)
			}
		}
	}
}

// A branch chain may open its own fan: the nested rows push the outer fan's next row below them instead of overlapping it.
func TestLayoutDiagramNestedBranchRowsDoNotOverlap(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	outer := b.addNode("branch", "外层", nil, 0)
	b.addEdge("1", outer, "")
	inner := b.addNode("intent", "内层", nil, 1)
	b.addEdge(outer, inner, "sourcec1right")
	innerA := b.addNode("code", "内一", nil, 2)
	b.addEdge(inner, innerA, "sourcec2right")
	innerB := b.addNode("code", "内二", nil, 2)
	b.addEdge(inner, innerB, "sourcec3right")
	sibling := b.addNode("code", "外层二", nil, 1)
	b.addEdge(outer, sibling, "sourcec4right")
	b.layoutDiagram()

	pos := func(id string) (int, int) {
		for i := range b.nodes {
			if b.nodes[i].ID == id {
				return b.nodes[i].Position["x"], b.nodes[i].Position["y"]
			}
		}
		t.Fatalf("node %s does not exist", id)
		return 0, 0
	}

	_, iy := pos(inner)
	ax, ay := pos(innerA)
	bx, by := pos(innerB)
	sx, sy := pos(sibling)
	if ax != bx {
		t.Errorf("the nested branch rows should start at one column: %d / %d", ax, bx)
	}
	if ay != iy || by <= ay {
		t.Errorf("the nested fan should start on the inner card's row and stack downward: inner=%d a=%d b=%d", iy, ay, by)
	}
	if sy < by+32 {
		t.Errorf("the outer fan's next row overlaps the nested one: sibling=(%d,%d) innerB=(%d,%d)", sx, sy, bx, by)
	}
	if sx >= ax {
		t.Errorf("the outer fan's next row should start left of the nested fan: sibling x=%d, nested x=%d", sx, ax)
	}
}

// Containers: children are stacked vertically, the container is expanded to its bounding box, and that is written into config.size
// (the frontend reads it directly for width and height).
func TestLayoutDiagramContainer(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	loop := b.addNode("loop", "循环", nil, 0)
	b.addEdge("1", loop, "")
	entry := b.addNodeIn("entry", "入口", nil, 0, loop)
	inner := b.addNodeIn("llm", "处理", nil, 0, loop)
	b.addEdge(entry, inner, "")

	b.layoutDiagram()

	nodeByID := func(id string) *diagramNode {
		for i := range b.nodes {
			if b.nodes[i].ID == id {
				return &b.nodes[i]
			}
		}
		t.Fatalf("node %s does not exist", id)
		return nil
	}

	entryNode := nodeByID(entry)
	innerNode := nodeByID(inner)
	if entryNode.Position["x"] != childBaseX || entryNode.Position["y"] != childBaseY {
		t.Errorf("entry position = %v, want (%d,%d)", entryNode.Position, childBaseX, childBaseY)
	}
	wantY := childBaseY + 32 + childGap
	if innerNode.Position["x"] != childBaseX || innerNode.Position["y"] != wantY {
		t.Errorf("child position = %v, want (%d,%d)", innerNode.Position, childBaseX, wantY)
	}

	cfg, _ := nodeByID(loop).Data["config"].(map[string]any)
	size, _ := cfg["size"].(map[string]any)
	if size["width"] != "300px" {
		t.Errorf("container width = %v, want 300px (leaf width 120 plus 20 on each side is still the floor)", size["width"])
	}
	if size["height"] != "145px" {
		t.Errorf("container height = %v, want 145px (child bottom 125 plus a 20 bottom margin)", size["height"])
	}
}

// A function body is laid out like the main flow - entry at the left, the chain to the right - instead of the vertical stack
// loop / process use, and the card grows to that row (wide and short) rather than to a column (narrow and tall).
func TestLayoutDiagramFunctionBodyFollowsMainFlow(t *testing.T) {
	b := &diagramBuilder{}
	b.addStart()
	fn := b.addNode("function", "求和", nil, 0)
	entry := b.addNodeIn("entry", "入口", nil, 0, fn)
	first := b.addNodeIn("code", "取值", nil, 0, fn)
	second := b.addNodeIn("code", "求和", nil, 0, fn)
	b.addEdge(entry, first, "")
	b.addEdge(first, second, "")

	b.layoutDiagram()

	pos := func(id string) map[string]int {
		for i := range b.nodes {
			if b.nodes[i].ID == id {
				return b.nodes[i].Position
			}
		}
		t.Fatalf("node %s does not exist", id)
		return nil
	}

	entryPos, firstPos, secondPos := pos(entry), pos(first), pos(second)
	if entryPos["x"] != childBaseX || entryPos["y"] != childBaseY {
		t.Errorf("function entry = %v, want (%d,%d)", entryPos, childBaseX, childBaseY)
	}
	if firstPos["y"] != childBaseY || secondPos["y"] != childBaseY {
		t.Errorf("the body must stay on one row, got %v and %v", firstPos, secondPos)
	}
	if firstPos["x"] <= entryPos["x"] || secondPos["x"] <= firstPos["x"] {
		t.Errorf("the body must run left to right: entry %d, first %d, second %d", entryPos["x"], firstPos["x"], secondPos["x"])
	}

	var fnCfg map[string]any
	for i := range b.nodes {
		if b.nodes[i].ID == fn {
			fnCfg, _ = b.nodes[i].Data["config"].(map[string]any)
		}
	}
	size, _ := fnCfg["size"].(map[string]any)
	if size["width"] == nil || size["height"] == nil {
		t.Fatalf("the function card has no size: %v", fnCfg)
	}
	var w, h int
	fmt.Sscanf(size["width"].(string), "%dpx", &w)
	fmt.Sscanf(size["height"].(string), "%dpx", &h)
	if w < 400 || h > 100 {
		t.Errorf("card size = %dx%d, want a wide, short card (the body is one row)", w, h)
	}
	if pos(fn)["y"] <= pos("1")["y"] {
		t.Errorf("function node = %v, want it below the flow, not in the main row", pos(fn))
	}
}

// ===== Model-supplied coordinates (step.position) =====

// createdPositions parses the diagram stored by create_flow into id -> (x, y).
func createdPositions(t *testing.T, fk *fakeFlows) map[string][2]int {
	t.Helper()
	if fk.created == nil {
		t.Fatal("FlowsManager.Create was not called")
	}
	var doc struct {
		Nodes []struct {
			ID       string         `json:"id"`
			Position map[string]int `json:"position"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("failed to parse the diagram: %v", err)
	}
	out := make(map[string][2]int, len(doc.Nodes))
	for _, n := range doc.Nodes {
		out[n.ID] = [2]int{n.Position["x"], n.Position["y"]}
	}
	return out
}

func createPosFlow(t *testing.T, steps []any) (map[string][2]int, []string) {
	t.Helper()
	fk := &fakeFlows{createdID: "flow-pos"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "pos-demo", "description": "d", "steps": steps,
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	var warnings []string
	if raw, ok := out.(map[string]any)["warnings"]; ok {
		warnings, _ = raw.([]string)
	}
	return createdPositions(t, fk), warnings
}

// All top-level positions supplied: laid out exactly as the model asked, with start moved to the same row left of the first node.
func TestCreateFlowHonorsModelPositions(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, _ := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code,
			"position": map[string]any{"x": 400.0, "y": 200.0}},
		map[string]any{"type": "code", "label": "乙", "config": code,
			"position": map[string]any{"x": 800.0, "y": 200.0}},
	})
	if pos["1"] != [2]int{200, 200} {
		t.Errorf("start should sit to the left of the first node on the same row (200,200), got %v", pos["1"])
	}
	if pos["2"] != [2]int{400, 200} || pos["3"] != [2]int{800, 200} {
		t.Errorf("the model's coordinates were rewritten: %v", pos)
	}
}

// Only half the positions given: everything falls back to automatic layout, and the missing nodes are named.
func TestCreateFlowPartialPositionsFallBack(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code,
			"position": map[string]any{"x": 400.0, "y": 200.0}},
		map[string]any{"type": "code", "label": "乙", "config": code},
	})
	// Automatic layout: start(100,50) -> a x=100+120+80=300, same y=50
	if pos["2"] != [2]int{300, 50} {
		t.Errorf("it should fall back to automatic layout, a is at %v, want (300,50)", pos["2"])
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "have no position") {
		t.Errorf("missing coordinates should produce a warning, got warnings = %v", warnings)
	}
}

// Overlapping coordinates are reported but not moved: moving them would break the model's own composition.
func TestCreateFlowWarnsOverlap(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	same := map[string]any{"x": 400.0, "y": 200.0}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code, "position": same},
		map[string]any{"type": "code", "label": "乙", "config": code, "position": same},
	})
	if pos["2"] != [2]int{400, 200} || pos["3"] != [2]int{400, 200} {
		t.Errorf("overlapping coordinates must not be quietly changed either: %v", pos)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "overlap") {
		t.Errorf("an overlap should produce a warning, got warnings = %v", warnings)
	}
}

// layout is a visual waypoint: it may appear in steps and the chain runs straight through it (the runner skips it and continues).
func TestCreateFlowAllowsLayoutNode(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code},
		map[string]any{"type": "layout", "label": "折行", "config": map[string]any{}},
		map[string]any{"type": "code", "label": "乙", "config": code},
	})
	if len(pos) != 4 {
		t.Fatalf("nodes = %v, want start plus 3 nodes", pos)
	}
	// Automatic layout: start(100,50) -> a(300,50) -> layout(500,50) -> b(640,50)
	want := map[string][2]int{"1": {100, 50}, "2": {300, 50}, "3": {500, 50}, "4": {640, 50}}
	for id, p := range want {
		if pos[id] != p {
			t.Errorf("node %s is at %v, want %v (layout counts as 60 wide)", id, pos[id], p)
		}
	}
	for _, w := range warnings {
		if strings.Contains(w, "layout") || strings.Contains(w, "unsupported") {
			t.Errorf("a layout node should not produce a structural warning: %s", w)
		}
	}
}

// A node is a leaf whatever its config says: a config block the canvas does not render must not inflate the estimate
// (an inflated box overlaps its neighbours).
func TestNodeSizeIgnoresUnrenderedConfig(t *testing.T) {
	plain := diagramNode{Type: "code", Data: map[string]any{"label": "代码"}}
	if w, h := nodeSize(plain); w != 120 || h != 32 {
		t.Errorf("a plain leaf = %dx%d, want 120x32", w, h)
	}
	withExtra := diagramNode{Type: "code", Data: map[string]any{
		"label":  "代码",
		"config": map[string]any{"unknown_block": map[string]any{"mode": "mono"}},
	}}
	if w, h := nodeSize(withExtra); w != 120 || h != 32 {
		t.Errorf("an unrendered config block must not enlarge the node: %dx%d, want 120x32", w, h)
	}
	if w, h := nodeSize(diagramNode{Type: "layout", Data: map[string]any{"label": "折行"}}); w != 60 || h != 32 {
		t.Errorf("layout = %dx%d, want 60x32", w, h)
	}
}

func TestUpdateNodePosition(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	if _, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "position": map[string]any{"x": 520.0, "y": 640.0},
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	got, _ := storedNodes(t, fk)["2"]["position"].(map[string]any)
	if got["x"] != float64(520) || got["y"] != float64(640) {
		t.Errorf("position = %v, want {520 640}", got)
	}
}
