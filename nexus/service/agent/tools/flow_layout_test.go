package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 排版口径：主链横向一行、分支各占一行纵向下排、折行与分支首节点的入边走 top。
// 这几条是「工具建的图能看」的底线，改 layout 时先让它们过。
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
		t.Fatalf("节点 %s 不存在", id)
		return 0, 0
	}

	sx, sy := pos("1")
	lx, ly := pos(llm)
	bx, by := pos(br)
	if sy != ly || ly != by {
		t.Errorf("主链没有排在同一行：start=(%d,%d) llm=(%d,%d) branch=(%d,%d)", sx, sy, lx, ly, bx, by)
	}
	if !(sx < lx && lx < bx) {
		t.Errorf("主链没有从左往右：x = %d / %d / %d", sx, lx, bx)
	}

	ax, ay := pos(a1)
	a2x, a2y := pos(a2)
	dx, dy := pos(d1)
	if ax <= bx {
		t.Errorf("分支首节点 x=%d 未让开分支节点 x=%d", ax, bx)
	}
	if ay <= by {
		t.Errorf("分支首节点 y=%d 未排到分支节点下方（y=%d）", ay, by)
	}
	if a2y != ay || a2x <= ax {
		t.Errorf("同一分支内的节点应继续向右：a1=(%d,%d) a2=(%d,%d)", ax, ay, a2x, a2y)
	}
	if dy <= a2y {
		t.Errorf("第二条分支没有排到第一条分支下方：乙=(%d,%d) 甲二=(%d,%d)", dx, dy, a2x, a2y)
	}

	for _, e := range b.edges {
		if e.Target != a1 && e.Target != d1 {
			continue
		}
		if e.TargetHandle == nil || *e.TargetHandle != "target"+e.Target+"top" {
			t.Errorf("分支首节点的入边应走 top，实际 = %v", e.TargetHandle)
		}
	}
}

// 主链一行放不下时要折行，并让开上一行分支占用的高度。
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
					t.Fatalf("折行后 y 没有下移：%d → %d", lastY, y)
				}
				rows++
				lastY = y
			}
			if x := b.nodes[i].Position["x"]; x > layoutRowMaxWidth {
				t.Errorf("节点 %s 的 x=%d 超出单行宽度 %d", id, x, layoutRowMaxWidth)
			}
		}
	}
	if rows < 2 {
		t.Errorf("12 个宽节点应当折行，实际只有 %d 行", rows)
	}
}

// 容器：子节点纵向排列，容器尺寸按包围盒撑开，写进 config.size（前端直接读它设宽高）。
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
		t.Fatalf("节点 %s 不存在", id)
		return nil
	}

	entryNode := nodeByID(entry)
	innerNode := nodeByID(inner)
	if entryNode.Position["x"] != childBaseX || entryNode.Position["y"] != childBaseY {
		t.Errorf("entry 落点 = %v，want (%d,%d)", entryNode.Position, childBaseX, childBaseY)
	}
	wantY := childBaseY + 32 + childGap
	if innerNode.Position["x"] != childBaseX || innerNode.Position["y"] != wantY {
		t.Errorf("子节点落点 = %v，want (%d,%d)", innerNode.Position, childBaseX, wantY)
	}

	cfg, _ := nodeByID(loop).Data["config"].(map[string]any)
	size, _ := cfg["size"].(map[string]any)
	if size["width"] != "300px" {
		t.Errorf("容器宽度 = %v，want 300px（叶子宽 120 + 左右各 20 时仍是下限）", size["width"])
	}
	if size["height"] != "145px" {
		t.Errorf("容器高度 = %v，want 145px（子节点底 125 + 下边距 20）", size["height"])
	}
}

// ===== 模型给坐标（step.position）=====

// createdPositions 解析 create_flow 落库的 diagram，取 id → (x, y)。
func createdPositions(t *testing.T, fk *fakeFlows) map[string][2]int {
	t.Helper()
	if fk.created == nil {
		t.Fatal("未调用 FlowsManager.Create")
	}
	var doc struct {
		Nodes []struct {
			ID       string         `json:"id"`
			Position map[string]int `json:"position"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
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

// 顶层节点全给坐标：完全按模型摆，start 自动挪到首个节点左侧同行。
func TestCreateFlowHonorsModelPositions(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, _ := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code,
			"position": map[string]any{"x": 400.0, "y": 200.0}},
		map[string]any{"type": "code", "label": "乙", "config": code,
			"position": map[string]any{"x": 800.0, "y": 200.0}},
	})
	if pos["1"] != [2]int{200, 200} {
		t.Errorf("start 应摆在首个节点左侧同行 (200,200)，实际 %v", pos["1"])
	}
	if pos["2"] != [2]int{400, 200} || pos["3"] != [2]int{800, 200} {
		t.Errorf("模型给的坐标被改写了：%v", pos)
	}
}

// 只给一半坐标：整体退回自动排版，并点名还差哪些节点。
func TestCreateFlowPartialPositionsFallBack(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code,
			"position": map[string]any{"x": 400.0, "y": 200.0}},
		map[string]any{"type": "code", "label": "乙", "config": code},
	})
	// 自动排版：start(100,50) → 甲 x=100+120+80=300、同 y=50
	if pos["2"] != [2]int{300, 50} {
		t.Errorf("应退回自动排版，甲落点 = %v，want (300,50)", pos["2"])
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "没给 position") {
		t.Errorf("缺坐标要回 warning，实际 warnings = %v", warnings)
	}
}

// 坐标重叠只提示不挪动：挪了会破坏模型自己的构图意图。
func TestCreateFlowWarnsOverlap(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	same := map[string]any{"x": 400.0, "y": 200.0}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code, "position": same},
		map[string]any{"type": "code", "label": "乙", "config": code, "position": same},
	})
	if pos["2"] != [2]int{400, 200} || pos["3"] != [2]int{400, 200} {
		t.Errorf("重叠的坐标也不该被偷偷改掉：%v", pos)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "重叠") {
		t.Errorf("重叠要回 warning，实际 warnings = %v", warnings)
	}
}

// layout 是视觉拐点：允许出现在 steps 里，链条照常穿过（runner 会跳过它并直通）。
func TestCreateFlowAllowsLayoutNode(t *testing.T) {
	code := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	pos, warnings := createPosFlow(t, []any{
		map[string]any{"type": "code", "label": "甲", "config": code},
		map[string]any{"type": "layout", "label": "折行", "config": map[string]any{}},
		map[string]any{"type": "code", "label": "乙", "config": code},
	})
	if len(pos) != 4 {
		t.Fatalf("nodes = %v，want start + 3 个节点", pos)
	}
	// 自动排版：start(100,50) → 甲(300,50) → layout(500,50) → 乙(640,50)
	want := map[string][2]int{"1": {100, 50}, "2": {300, 50}, "3": {500, 50}, "4": {640, 50}}
	for id, p := range want {
		if pos[id] != p {
			t.Errorf("节点 %s 落点 = %v，want %v（layout 宽度按 60 计）", id, pos[id], p)
		}
	}
	for _, w := range warnings {
		if strings.Contains(w, "layout") || strings.Contains(w, "不支持") {
			t.Errorf("layout 节点不该产生结构告警：%s", w)
		}
	}
}

// 并发节点会多出一行「并发控制 + 下拉」，宽度顶到 280、高度约 64 —— 排版要按这个留间距。
func TestNodeSizeAccountsForConcurrency(t *testing.T) {
	plain := diagramNode{Type: "code", Data: map[string]any{"label": "代码"}}
	if w, h := nodeSize(plain); w != 120 || h != 32 {
		t.Errorf("普通叶子 = %d×%d，want 120×32", w, h)
	}
	withConc := diagramNode{Type: "code", Data: map[string]any{
		"label":  "代码",
		"config": map[string]any{"concurrency": map[string]any{"mode": "mono"}},
	}}
	if w, h := nodeSize(withConc); w != 280 || h != 64 {
		t.Errorf("开了并发的叶子 = %d×%d，want 280×64", w, h)
	}
	// 空对象等于没开（前端不会渲染并发行）
	emptyConc := diagramNode{Type: "code", Data: map[string]any{
		"label":  "代码",
		"config": map[string]any{"concurrency": map[string]any{}},
	}}
	if w, h := nodeSize(emptyConc); w != 120 || h != 32 {
		t.Errorf("空 concurrency 不该撑大节点：%d×%d", w, h)
	}
	if w, h := nodeSize(diagramNode{Type: "layout", Data: map[string]any{"label": "折行"}}); w != 60 || h != 32 {
		t.Errorf("layout = %d×%d，want 60×32", w, h)
	}
}

// 循环 / 流程容器默认串行：模型抄示例带上来的 config.concurrency 要被摘掉并回显。
func TestCreateFlowStripsContainerConcurrency(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-cc"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "cc-demo", "description": "d",
		"steps": []any{
			map[string]any{
				"type": "loop", "label": "循环",
				"config": map[string]any{
					"loop":        map[string]any{"type": "count", "total_times": 2},
					"concurrency": map[string]any{"mode": "mono"},
				},
				"children": []any{map[string]any{"type": "result", "config": map[string]any{}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
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
		t.Fatalf("diagram 解析失败: %v", err)
	}
	for _, n := range doc.Nodes {
		if n.Type != "loop" {
			continue
		}
		if _, ok := n.Data.Config["concurrency"]; ok {
			t.Errorf("loop 节点的 config 不该带 concurrency：%v", n.Data.Config)
		}
	}
	joined := strings.Join(warningsOf(out), "\n")
	if !strings.Contains(joined, "concurrency 已忽略") {
		t.Errorf("摘掉 concurrency 要回 warning，实际 = %v", joined)
	}
}

// create_node 同口径：容器不落 concurrency。
func TestCreateNodeStripsContainerConcurrency(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "process", "label": "并发子流程",
		"config": map[string]any{"concurrency": map[string]any{"mode": "mono"}},
	})
	if err != nil {
		t.Fatalf("create_node = %v", err)
	}
	nodeID, _ := out.(map[string]any)["node"].(map[string]any)["id"].(string)
	data, _ := storedNodes(t, fk)[nodeID]["data"].(map[string]any)
	cfg, _ := data["config"].(map[string]any)
	if _, ok := cfg["concurrency"]; ok {
		t.Errorf("process 节点的 config 不该带 concurrency：%v", cfg)
	}
	if !strings.Contains(strings.Join(warningsOf(out), "\n"), "concurrency") {
		t.Errorf("应回显「已忽略 concurrency」：%v", warningsOf(out))
	}
}

// 并发默认关闭：不给 concurrency 参数时，叶子节点上的 concurrency 也要摘掉并回显。
func TestCreateFlowConcurrencyDefaultOff(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-off"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "off-demo", "description": "d",
		"steps": []any{map[string]any{
			"type": "code", "label": "并发代码",
			"config": map[string]any{
				"code":        map[string]any{"lang": "lua", "script": "return {}"},
				"concurrency": map[string]any{"mode": "mono"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if _, ok := createdConfig(t, fk, "code")["concurrency"]; ok {
		t.Errorf("默认应关闭并发，config 不该带 concurrency：%v", createdConfig(t, fk, "code"))
	}
	if !strings.Contains(strings.Join(warningsOf(out), "\n"), "并发默认关闭") {
		t.Errorf("摘掉 concurrency 要回显「并发默认关闭」：%v", warningsOf(out))
	}
}

// 显式 mono：只保留单体并发，fork 组形态被摘掉并回显。
func TestCreateFlowConcurrencyMonoKeepsOnlyMono(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-mono"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "mono-demo", "description": "d", "concurrency": "mono",
		"steps": []any{
			map[string]any{
				"type": "code", "label": "单体",
				"config": map[string]any{
					"code":        map[string]any{"lang": "lua", "script": "return {}"},
					"concurrency": map[string]any{"mode": "mono"},
				},
			},
			map[string]any{
				"type": "http", "label": "组员",
				"config": map[string]any{
					"url":         "https://example.com",
					"concurrency": map[string]any{"mode": "fork", "fork_node_id": "9"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if got := createdConfig(t, fk, "http")["concurrency"]; got != nil {
		t.Errorf("mono 模式下 fork 形态应被摘掉，实际 %v", got)
	}
	cc, _ := createdConfig(t, fk, "code")["concurrency"].(map[string]any)
	if cc["mode"] != "mono" {
		t.Errorf("mono 模式下单体并发应保留，实际 %v", createdConfig(t, fk, "code"))
	}
	if !strings.Contains(strings.Join(warningsOf(out), "\n"), "只开单体并发") {
		t.Errorf("应回显「只开单体并发」：%v", warningsOf(out))
	}
}

// 显式 group：只保留并发组成员，单体并发形态被摘掉并回显。
func TestCreateFlowConcurrencyGroupKeepsOnlyFork(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-group"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "group-demo", "description": "d", "concurrency": "group",
		"steps": []any{
			map[string]any{
				"type": "code", "label": "单体",
				"config": map[string]any{
					"code":        map[string]any{"lang": "lua", "script": "return {}"},
					"concurrency": map[string]any{"mode": "mono"},
				},
			},
			map[string]any{
				"type": "http", "label": "组员",
				"config": map[string]any{
					"url":         "https://example.com",
					"concurrency": map[string]any{"mode": "fork", "fork_node_id": "9"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if got := createdConfig(t, fk, "code")["concurrency"]; got != nil {
		t.Errorf("group 模式下单体并发应被摘掉，实际 %v", got)
	}
	cc, _ := createdConfig(t, fk, "http")["concurrency"].(map[string]any)
	if cc["mode"] != "fork" || cc["fork_node_id"] != "9" {
		t.Errorf("group 模式下并发组成员应保留，实际 %v", createdConfig(t, fk, "http"))
	}
	if !strings.Contains(strings.Join(warningsOf(out), "\n"), "并发组") {
		t.Errorf("应回显「并发组」：%v", warningsOf(out))
	}
}

// 存量流程不受影响：update_node 不传 concurrency 时，已写死的并发配置原样保留。
func TestUpdateNodeKeepsExistingConcurrency(t *testing.T) {
	c, fk := edgeTestCtx(concurrencyNodeDiagram)
	if _, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "label": "改个名",
	}); err != nil {
		t.Fatalf("update_node = %v", err)
	}
	cfg, _ := storedNodes(t, fk)["2"]["data"].(map[string]any)["config"].(map[string]any)
	cc, _ := cfg["concurrency"].(map[string]any)
	if cc["mode"] != "mono" {
		t.Errorf("没传 concurrency 时不该动存量并发配置：%v", cfg)
	}
}

// 显式 off：只有调用方要求关并发时，才把该节点的 concurrency 摘掉。
func TestUpdateNodeConcurrencyOffStrips(t *testing.T) {
	c, fk := edgeTestCtx(concurrencyNodeDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "concurrency": "off",
	})
	if err != nil {
		t.Fatalf("update_node = %v", err)
	}
	cfg, _ := storedNodes(t, fk)["2"]["data"].(map[string]any)["config"].(map[string]any)
	if _, ok := cfg["concurrency"]; ok {
		t.Errorf("concurrency=off 应摘掉并发：%v", cfg)
	}
	if !strings.Contains(strings.Join(warningsOf(out), "\n"), "并发默认关闭") {
		t.Errorf("应回显摘除原因：%v", warningsOf(out))
	}
}

// concurrencyNodeDiagram 带单体并发的存量图（update_node 的「不动存量」用例）。
const concurrencyNodeDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "并发代码", "config": {"code": {"lang": "lua", "script": "return {}"}, "concurrency": {"mode": "mono"}}}}
  ],
  "edges": [
    {"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
  ]
}`

// createdConfig 从 create_flow 落库的 diagram 里取指定类型的第一个节点 config。
func createdConfig(t *testing.T, fk *fakeFlows, typ string) map[string]any {
	t.Helper()
	if fk.created == nil {
		t.Fatal("Create 没被调用")
	}
	var doc struct {
		Nodes []struct {
			Type string `json:"type"`
			Data struct {
				Config map[string]any `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
	}
	for _, n := range doc.Nodes {
		if n.Type == typ {
			return n.Data.Config
		}
	}
	t.Fatalf("diagram 里没有 %s 节点", typ)
	return nil
}

// warningsOf 取工具返回值里的 warnings 字段。
func warningsOf(out any) []string {
	m, _ := out.(map[string]any)
	raw, _ := m["warnings"]
	list, _ := raw.([]string)
	return list
}

// update_node 支持单独挪一个节点（模型据此微调自动排版的结果）。
func TestUpdateNodePosition(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	if _, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "position": map[string]any{"x": 520.0, "y": 640.0},
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	got, _ := storedNodes(t, fk)["2"]["position"].(map[string]any)
	if got["x"] != float64(520) || got["y"] != float64(640) {
		t.Errorf("position = %v，want {520 640}", got)
	}
}
