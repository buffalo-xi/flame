package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// nodeTestDiagram 带引用的图：
//
//	1 start（孤岛，只为占位）
//	5 code ──> 2 code（2 通过 config.inputs.x.source_ref 引用 5）
//	7 process ── 9 entry ──> 8 code（容器内）
const nodeTestDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "中间", "config": {
      "code": {"lang": "lua", "script": "return {}"},
      "inputs": {"x": {"type": "string", "source_type": "node", "source_ref": "5", "source_key": "out"}}
    }}},
    {"id": "5", "type": "code", "data": {"label": "上游", "config": {"code": {"lang": "lua", "script": "return {}"}}}},
    {"id": "7", "type": "process", "data": {"label": "子流程"}},
    {"id": "9", "type": "entry", "parent_node": "7", "data": {"label": "入口"}},
    {"id": "8", "type": "code", "parent_node": "7", "data": {"label": "容器内"}}
  ],
  "edges": [
    {"id": "20001", "source": "5", "target": "2", "source_handle": "source5right", "target_handle": "target2left"},
    {"id": "20002", "source": "9", "target": "8", "source_handle": "source9right", "target_handle": "target8left"}
  ]
}`

// storedNodes 解析落库的 nodes，按 id 取。
func storedNodes(t *testing.T, fk *fakeFlows) map[string]map[string]any {
	t.Helper()
	if len(fk.updatedDiagram) == 0 {
		t.Fatal("UpdateDiagram 没被调用：图没落库")
	}
	var doc struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(fk.updatedDiagram, &doc); err != nil {
		t.Fatalf("落库 diagram 解析失败: %v", err)
	}
	out := make(map[string]map[string]any, len(doc.Nodes))
	for _, n := range doc.Nodes {
		id, _ := n["id"].(string)
		out[id] = n
	}
	return out
}

func TestCreateNodeTopLevel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "llm", "config": map[string]any{
			"llm": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	node, _ := m["node"].(map[string]any)
	id, _ := node["id"].(string)
	if id == "" || len(id) != 5 {
		t.Errorf("新节点 id = %q, want 5 位数字串（与画布一致）", id)
	}
	if _, ok := storedNodes(t, fk)[id]; !ok {
		t.Error("新节点没落库")
	}
	h, _ := m["handles"].(map[string]any)
	if ins, _ := h["in"].([]string); len(ins) == 0 {
		t.Errorf("handles.in 为空: %v", h)
	}
}

// 容器必须自动带 entry：没有 entry，执行器记一行日志就直接成功返回。
func TestCreateNodeInContainerAddsEntry(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "code", "parent_node": "7",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	entry, ok := m["parent_entry"].(map[string]any)
	if !ok {
		t.Fatal("容器里建节点没有给容器补 entry（容器没有 entry 会静默空转）")
	}
	entryID, _ := entry["id"].(string)
	nodes := storedNodes(t, fk)
	if got := nodes[entryID]["parent_node"]; got != "7" {
		t.Errorf("entry.parent_node = %v, want 7", got)
	}
	if got := nodes[entryID]["type"]; got != "entry" {
		t.Errorf("entry.type = %v, want entry", got)
	}
	node, _ := m["node"].(map[string]any)
	if got := nodes[node["id"].(string)]["parent_node"]; got != "7" {
		t.Errorf("新节点 parent_node = %v, want 7", got)
	}
}

func TestCreateNodeRejectsIllegalContainer(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		wantEr string
	}{
		{"function 进容器", map[string]any{"flow": "demo", "type": "function", "parent_node": "7"}, "只能建在顶层"},
		{"return 不在 function 里", map[string]any{"flow": "demo", "type": "return", "parent_node": "7"}, "function 容器内部"},
		{"父节点不是容器", map[string]any{"flow": "demo", "type": "code", "parent_node": "2"}, "只有 loop / process / function"},
		{"父节点不存在", map[string]any{"flow": "demo", "type": "code", "parent_node": "zz"}, "不存在"},
		{"start 不能建", map[string]any{"flow": "demo", "type": "start"}, "不能建"},
		{"entry 不能建", map[string]any{"flow": "demo", "type": "entry"}, "不能建"},
		{"knowledge 没有执行器", map[string]any{"flow": "demo", "type": "knowledge"}, "no executor registered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fk := edgeTestCtx(edgeTestDiagram)
			_, err := (&createNodeTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantEr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantEr)
			}
			if len(fk.updatedDiagram) != 0 {
				t.Error("校验失败时不应落库")
			}
		})
	}
}

// 能建的类型清单在这里钉死：增删类型时必须同时确认 executors 注册表、前端 palette 与技能文档，
// 否则就会出现「能渲染、一运行就 no executor registered」（knowledge 就是这么掉的）。
func TestCreatableNodeTypesFixed(t *testing.T) {
	want := []string{
		"llm", "code", "http", "mcp", "rpc", "branch", "intent", "loop", "process", "function",
		"call", "assign", "memory", "wait", "fork", "result", "progress", "break", "continue",
		"return", "exit", "layout", "comment",
	}
	got := make([]string, 0, len(creatableNodeTypes))
	for typ := range creatableNodeTypes {
		got = append(got, typ)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("creatableNodeTypes = %v，want %v（增删类型请同步 executors / 前端 palette / 技能文档）", got, want)
	}
	// 结构节点与无执行器的历史类型不能出现在可建清单里。
	for _, typ := range []string{"start", "entry", "knowledge"} {
		if creatableNodeTypes[typ] {
			t.Errorf("%q 不该允许创建", typ)
		}
	}
}

// branch 没给 children 时要自动带一条：执行器靠 children[].id 找出边。
func TestCreateNodeBranchDefaultsChildren(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "branch"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	node, _ := out.(map[string]any)["node"].(map[string]any)
	id, _ := node["id"].(string)
	raw, _ := json.Marshal(storedNodes(t, fk)[id])
	var n struct {
		Data struct {
			Config struct {
				Children []struct {
					ID string `json:"id"`
				} `json:"children"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("节点解析失败: %v", err)
	}
	if len(n.Data.Config.Children) != 1 || n.Data.Config.Children[0].ID == "" {
		t.Errorf("branch 的 children 没自动生成: %s", raw)
	}
}

// insert_node：删旧边 + 建 A→新→B 两条，原子完成。
func TestInsertNodeRewiresEdge(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&insertNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "type": "code",
		"config": map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	node, _ := m["node"].(map[string]any)
	id, _ := node["id"].(string)

	edges := storedEdges(t, fk)
	if len(edges) != 2 {
		t.Fatalf("落库边数 = %d, want 2（旧边删掉，插两条）", len(edges))
	}
	if edges[0]["source"] != "1" || edges[0]["target"] != id {
		t.Errorf("前一条边不对: %+v", edges[0])
	}
	if edges[1]["source"] != id || edges[1]["target"] != "2" {
		t.Errorf("后一条边不对: %+v", edges[1])
	}
	if edges[1]["target_handle"] != "target2left" {
		t.Errorf("后一条边应保留原入边 handle，实际 %v", edges[1]["target_handle"])
	}
}

func TestInsertNodeRejectsTerminalType(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&insertNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "type": "exit",
	})
	if err == nil || !strings.Contains(err.Error(), "出边") {
		t.Fatalf("err = %v, want 没有出边", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("校验失败时不应落库")
	}
}

func TestUpdateNodeConfigPatch(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "config_patch": map[string]any{"limit": 5},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cfg, _ := out.(map[string]any)["config"].(map[string]any)
	if cfg["limit"] == nil {
		t.Errorf("patch 没生效: %v", cfg)
	}
	if _, ok := cfg["code"]; !ok {
		t.Errorf("patch 把原有键弄丢了（应该是合并，不是替换）: %v", cfg)
	}
	_ = fk
}

// 移进容器：横跨新旧作用域的边会被删掉并回显（画布本来也不允许跨父节点连线）。
func TestUpdateNodeMoveDropsCrossScopeEdges(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "parent_node": "7",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	removed, _ := m["removed_edges"].([]any)
	if len(removed) != 1 {
		t.Fatalf("removed_edges = %v, want 1 条（5→2 跨容器）", m["removed_edges"])
	}
	if got := storedNodes(t, fk)["2"]["parent_node"]; got != "7" {
		t.Errorf("2.parent_node = %v, want 7", got)
	}
	// 容器内的 9→8 不受影响
	if edges := storedEdges(t, fk); len(edges) != 1 || edges[0]["id"] != "20002" {
		t.Errorf("剩下的边不对: %+v", edges)
	}
}

func TestDeleteNodeCascadesEdges(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "2"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 0 {
		t.Errorf("edge_count = %v, want 0", got)
	}
	if _, ok := storedNodes(t, fk)["2"]; ok {
		t.Error("节点没删掉")
	}
}

// 被引用时拒绝：source_ref 指向的节点删了，运行期一定报取值失败。
func TestDeleteNodeRefusesReferenced(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	_, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "5"})
	if err == nil || !strings.Contains(err.Error(), "引用") {
		t.Fatalf("err = %v, want 拒绝并列出引用方", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("拒绝时不应落库")
	}
}

func TestDeleteNodeRefusesEntryAndStart(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"9", "entry"},
		{"1", "start"},
	} {
		c, _ := edgeTestCtx(nodeTestDiagram)
		_, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": tc.id})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("删 %s: err = %v, want 含 %q", tc.id, err, tc.want)
		}
	}
}

// 删容器：整棵子树（含 entry）一起走。
func TestDeleteContainerRemovesSubtree(t *testing.T) {
	c, fk := edgeTestCtx(nodeTestDiagram)
	out, err := (&deleteNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "node_id": "7"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	deleted, _ := m["deleted_nodes"].([]any)
	if len(deleted) != 3 {
		t.Fatalf("deleted_nodes = %v, want 3（7 + 9 + 8）", m["deleted_nodes"])
	}
	nodes := storedNodes(t, fk)
	for _, id := range []string{"7", "8", "9"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("节点 %s 没删掉", id)
		}
	}
}

// create_flow 的容器子链：自动建 entry、子节点带 parent_node、从 entry 接出。
func TestCreateFlowContainerChildren(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-c"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "loop-with-body", "description": "循环里跑一步",
		"steps": []any{
			map[string]any{
				"type": "loop", "label": "循环",
				"config":   map[string]any{"loop": map[string]any{"type": "count", "total_times": 3}},
				"children": []any{map[string]any{"type": "code", "label": "循环体", "config": map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Nodes []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			ParentNode string `json:"parent_node"`
		} `json:"nodes"`
		Edges []flowEdge `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
	}

	var entryID, bodyID string
	for _, n := range doc.Nodes {
		switch {
		case n.Type == "entry":
			entryID = n.ID
			if n.ParentNode != "2" {
				t.Errorf("entry.parent_node = %q, want 2（循环节点）", n.ParentNode)
			}
		case n.Type == "code":
			bodyID = n.ID
			if n.ParentNode != "2" {
				t.Errorf("循环体 parent_node = %q, want 2", n.ParentNode)
			}
		}
	}
	if entryID == "" || bodyID == "" {
		t.Fatalf("没建出 entry 或循环体: %+v", doc.Nodes)
	}
	found := false
	for _, e := range doc.Edges {
		if e.Source == entryID && e.Target == bodyID {
			found = true
		}
	}
	if !found {
		t.Errorf("入口没有接到循环体：%+v", doc.Edges)
	}
}

// create_flow 里 call.target / wait.fork_node_id 也支持写 label（与 source_ref 同口径）。
func TestCreateFlowResolvesTargetAndForkIDByLabel(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-r"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "call-by-label", "description": "按 label 解析 target",
		"steps": []any{
			map[string]any{
				"type": "function", "label": "我的函数",
				"config": map[string]any{},
			},
			map[string]any{
				"type": "call", "label": "调它",
				"config": map[string]any{"target": "我的函数"},
			},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
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
	var fnID string
	for _, n := range doc.Nodes {
		if n.Type == "function" {
			fnID = n.ID
		}
	}
	for _, n := range doc.Nodes {
		if n.Type != "call" {
			continue
		}
		if got, _ := n.Data.Config["target"].(string); got != fnID {
			t.Errorf("call.target = %q, want 函数节点 id %q", got, fnID)
		}
	}
}

// ===== label 唯一性 =====

func nodeLabel(t *testing.T, fk *fakeFlows, id string) string {
	t.Helper()
	data, _ := storedNodes(t, fk)[id]["data"].(map[string]any)
	label, _ := data["label"].(string)
	return label
}

// 显式写了重名 label → 拒绝，不落库。
func TestCreateNodeRejectsDuplicateLabel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram) // 节点 2 的 label 是「计算」
	_, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "type": "code", "label": "计算",
	})
	if err == nil || !strings.Contains(err.Error(), "必须唯一") {
		t.Fatalf("err = %v, want label 重名被拒", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("重名时不应落库")
	}
}

// 不写 label（自动命名）撞名 → 自动加后缀，不打断建图。
func TestCreateNodeAutoSuffixForDefaultLabel(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	labels := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		out, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "code"})
		if err != nil {
			t.Fatalf("第 %d 个 code 建失败: %v", i+1, err)
		}
		node, _ := out.(map[string]any)["node"].(map[string]any)
		labels = append(labels, nodeLabel(t, fk, node["id"].(string)))
	}
	if labels[0] != "代码" {
		t.Errorf("第一个默认 label = %q, want 代码", labels[0])
	}
	if labels[0] == labels[1] {
		t.Errorf("两个默认 label 都是 %q：应该自动加后缀", labels[0])
	}
}

// 容器 entry 也参与唯一性：第二个容器的 entry 不能还叫「入口」。
func TestCreateNodeContainerEntryLabelUnique(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	first, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "process"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	second, err := (&createNodeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "type": "process"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	e1 := first.(map[string]any)["entry"].(map[string]any)["id"].(string)
	e2 := second.(map[string]any)["entry"].(map[string]any)["id"].(string)
	if l1, l2 := nodeLabel(t, fk, e1), nodeLabel(t, fk, e2); l1 == l2 {
		t.Errorf("两个 entry 的 label 都是 %q：应该加后缀", l1)
	}
}

func TestUpdateNodeRenameLabelRules(t *testing.T) {
	// 改成别人的 label → 拒绝
	c, fk := edgeTestCtx(nodeTestDiagram) // 2=中间，5=上游
	_, err := (&updateNodeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "node_id": "2", "label": "上游",
	})
	if err == nil || !strings.Contains(err.Error(), "必须唯一") {
		t.Fatalf("err = %v, want 改名重名被拒", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("重名时不应落库")
	}

	// 改成自己原来的 label → 允许
	c2, fk2 := edgeTestCtx(nodeTestDiagram)
	if _, err := (&updateNodeTool{}).Execute(context.Background(), c2, map[string]any{
		"flow": "demo", "node_id": "2", "label": "中间",
	}); err != nil {
		t.Fatalf("改成自己原来的 label 不应报错: %v", err)
	}
	if got := nodeLabel(t, fk2, "2"); got != "中间" {
		t.Errorf("label = %q, want 中间", got)
	}
}

// create_flow 的 steps 里显式重名 → 报第几步。
func TestCreateFlowRejectsDuplicateLabels(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-dup"}
	codeCfg := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "dup", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "label": "同名", "config": codeCfg},
			map[string]any{"type": "code", "label": "同名", "config": codeCfg},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "重名") {
		t.Fatalf("err = %v, want 第 2 步 label 重名被拒", err)
	}
}

// create_flow 里不写 label（自动命名）撞名 → 自动加后缀。
func TestCreateFlowAutoSuffixForDefaultLabels(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-sfx"}
	codeCfg := map[string]any{"code": map[string]any{"lang": "lua", "script": "return {}"}}
	if _, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "sfx", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "config": codeCfg},
			map[string]any{"type": "code", "config": codeCfg},
		},
	}); err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Nodes []struct {
			Type string `json:"type"`
			Data struct {
				Label string `json:"label"`
			} `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range doc.Nodes {
		if n.Data.Label == "" {
			t.Errorf("节点 %s 没有 label", n.Type)
			continue
		}
		if seen[n.Data.Label] {
			t.Errorf("label %q 重复", n.Data.Label)
		}
		seen[n.Data.Label] = true
	}
}
