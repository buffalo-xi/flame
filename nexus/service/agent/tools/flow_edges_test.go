package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// edgeTestDiagram 一份可直接连线的图：
//
//	1 start ──> 2 code           （已有边 10001）
//	3 branch（子分支 c1 / c2）
//	4 result
//	7 process ── 8 code（parent_node=7）
//	9 function（没有任何 handle）
const edgeTestDiagram = `{
  "nodes": [
    {"id": "1", "type": "start", "data": {"label": "开始"}},
    {"id": "2", "type": "code", "data": {"label": "计算"}},
    {"id": "3", "type": "branch", "data": {"label": "判断", "config": {"children": [{"id": "c1", "label": "是"}, {"id": "c2", "label": "否"}]}}},
    {"id": "4", "type": "result", "data": {"label": "输出"}},
    {"id": "7", "type": "process", "data": {"label": "子流程"}},
    {"id": "8", "type": "code", "parent_node": "7", "data": {"label": "容器内代码"}},
    {"id": "9", "type": "function", "data": {"label": "函数"}},
    {"id": "5", "type": "exit", "data": {"label": "结束"}}
  ],
  "edges": [
    {"id": "10001", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
  ]
}`

func edgeTestCtx(diagram string) (*Context, *fakeFlows) {
	fk := &fakeFlows{detail: &FlowDetail{
		FlowInfo: FlowInfo{ID: "flow-1", Name: "demo"},
		Diagram:  json.RawMessage(diagram),
	}}
	return &Context{Flows: fk}, fk
}

// storedEdges 解析落库的 edges，用来断言「真的写回去了」。
func storedEdges(t *testing.T, fk *fakeFlows) []map[string]any {
	t.Helper()
	if len(fk.updatedDiagram) == 0 {
		t.Fatal("UpdateDiagram 没被调用：图没落库")
	}
	var doc struct {
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(fk.updatedDiagram, &doc); err != nil {
		t.Fatalf("落库 diagram 解析失败: %v", err)
	}
	return doc.Edges
}

func edgeIDOf(m map[string]any) string {
	s, _ := m["id"].(string)
	return s
}

func TestCreateEdgeWritesDiagram(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow":          "demo",
		"source":        "2",
		"target":        "3",
		"source_handle": "source2right",
		"target_handle": "target3top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 2 {
		t.Errorf("edge_count = %v, want 2", got)
	}
	edges := storedEdges(t, fk)
	if len(edges) != 2 {
		t.Fatalf("落库边数 = %d, want 2", len(edges))
	}
	last := edges[len(edges)-1]
	if last["source"] != "2" || last["target"] != "3" || last["target_handle"] != "target3top" {
		t.Errorf("落库的最后一条边不对: %+v", last)
	}
}

// 两个 handle 都必填：target_handle 缺省不能被悄悄补出来。
func TestCreateEdgeRequiresHandles(t *testing.T) {
	cases := []struct {
		name  string
		args  map[string]any
		wantE string
	}{
		{"缺 target_handle", map[string]any{"flow": "demo", "source": "2", "target": "3", "source_handle": "source2right"}, "target_handle is required"},
		{"缺 source_handle", map[string]any{"flow": "demo", "source": "2", "target": "3", "target_handle": "target3top"}, "source_handle is required"},
		{"自连", map[string]any{"flow": "demo", "source": "2", "target": "2", "source_handle": "source2right", "target_handle": "target2top"}, "to itself"},
		{"function 节点", map[string]any{"flow": "demo", "source": "2", "target": "9", "source_handle": "source2right", "target_handle": "target9left"}, "function"},
		{"跨父节点", map[string]any{"flow": "demo", "source": "8", "target": "4", "source_handle": "source8right", "target_handle": "target4left"}, "across parents"},
		{"终止节点没有出边", map[string]any{"flow": "demo", "source": "5", "target": "3", "source_handle": "source5right", "target_handle": "target3top"}, "no outgoing handle"},
		{"子分支位置非法", map[string]any{"flow": "demo", "source": "3", "target": "4", "source_handle": "sourcec1bottom", "target_handle": "target4left"}, "as an outgoing position"},
		{"owner 不认识", map[string]any{"flow": "demo", "source": "3", "target": "4", "source_handle": "sourcezzright", "target_handle": "target4left"}, "neither node"},
		{"入边位置不是该节点的", map[string]any{"flow": "demo", "source": "2", "target": "4", "source_handle": "source2right", "target_handle": "target4bottom"}, "as an incoming position"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fk := edgeTestCtx(edgeTestDiagram)
			_, err := (&createEdgeTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantE) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantE)
			}
			if len(fk.updatedDiagram) != 0 {
				t.Error("校验失败时不应落库")
			}
		})
	}
}

// 两个节点之间最多一条边：换入口位置也不行。
func TestCreateEdgeRejectsDuplicatePair(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "1", "target": "2",
		"source_handle": "source1bottom", "target_handle": "target2top",
	})
	if err == nil || !strings.Contains(err.Error(), "at most one edge") {
		t.Fatalf("err = %v, want 最多一条边", err)
	}
}

// 同一入口重复连入 → 硬拦。
func TestCreateEdgeRejectsSecondInboundOnSameHandle(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3right", "target_handle": "target2left",
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one incoming edge") {
		t.Fatalf("err = %v, want 只能有一条入边", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("同入口重复连时不应落库")
	}
}

// 入口位置不同 → 旧入边被顶掉，以新边为准。
func TestCreateEdgeReplacesInboundOnDifferentHandle(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3right", "target_handle": "target2top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	rep, _ := m["replaced_edge"].(map[string]any)
	if edgeIDOf(rep) != "10001" {
		t.Errorf("replaced_edge = %v, want id 10001", m["replaced_edge"])
	}
	edges := storedEdges(t, fk)
	if len(edges) != 1 {
		t.Fatalf("落库边数 = %d, want 1（旧入边应被删）", len(edges))
	}
	if edges[0]["source"] != "3" || edges[0]["target_handle"] != "target2top" {
		t.Errorf("落库的边不对: %+v", edges[0])
	}
}

// 出口重复：同一个 source_handle 只能有一条边。
func TestCreateEdgeRejectsDuplicateSourceHandle(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "1", "target": "3",
		"source_handle": "source1right", "target_handle": "target3top",
	})
	if err == nil || !strings.Contains(err.Error(), "one outlet takes exactly one edge") {
		t.Fatalf("err = %v, want 出口重复", err)
	}
}

func TestCreateEdgeRejectsCycle(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	// 先建 2 → 3，再试 3 → 2
	if _, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
		"source_handle": "source2right", "target_handle": "target3top",
	}); err != nil {
		t.Fatalf("建 2→3 失败: %v", err)
	}
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "3", "target": "2",
		"source_handle": "source3left", "target_handle": "target2top",
	})
	if err == nil || !strings.Contains(err.Error(), "creates a cycle") {
		t.Fatalf("err = %v, want 成环", err)
	}
}

func TestUpdateEdgeRepoints(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "source_handle": "source1bottom",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := out.(map[string]any)["edge_count"]; got != 1 {
		t.Errorf("edge_count = %v, want 1", got)
	}
	edges := storedEdges(t, fk)
	if len(edges) != 1 || edges[0]["source_handle"] != "source1bottom" {
		t.Errorf("落库的边不对: %+v", edges)
	}
}

// 改 target 必须一起给 target_handle：handle 里嵌的是节点 id。
func TestUpdateEdgeRequiresHandleWhenTargetChanges(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	_, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001", "target": "3",
	})
	if err == nil || !strings.Contains(err.Error(), "target_handle") {
		t.Fatalf("err = %v, want 提示补 target_handle", err)
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("校验失败时不应落库")
	}
}

func TestUpdateEdgeNotFound(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&updateEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "99999", "source_handle": "source1right",
	})
	if err == nil || !strings.Contains(err.Error(), "no edge with edge_id") {
		t.Fatalf("err = %v, want 找不到", err)
	}
}

func TestDeleteEdge(t *testing.T) {
	c, fk := edgeTestCtx(edgeTestDiagram)
	out, err := (&deleteEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "edge_id": "10001",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	if got := m["edge_count"]; got != 0 {
		t.Errorf("edge_count = %v, want 0", got)
	}
	if edges := storedEdges(t, fk); len(edges) != 0 {
		t.Errorf("落库边数 = %d, want 0", len(edges))
	}
}

func TestDeleteEdgeNotFound(t *testing.T) {
	c, _ := edgeTestCtx(edgeTestDiagram)
	_, err := (&deleteEdgeTool{}).Execute(context.Background(), c, map[string]any{"flow": "demo", "edge_id": "99999"})
	if err == nil || !strings.Contains(err.Error(), "no edge with edge_id") {
		t.Fatalf("err = %v, want 找不到", err)
	}
}

// 定位参数与 read_flow / delete_flow 同口径：复用 flowRefHint。
func TestEdgeToolsReuseFlowRefHint(t *testing.T) {
	for _, tl := range []Tool{&createEdgeTool{}, &updateEdgeTool{}, &deleteEdgeTool{}} {
		meta := tl.Meta()
		props, _ := meta.Parameters["properties"].(map[string]any)
		flow, _ := props["flow"].(map[string]any)
		if desc, _ := flow["description"].(string); desc != flowRefHint {
			t.Errorf("%s: flow 描述未复用 flowRefHint: %q", meta.Name, desc)
		}
	}
}

// create_edge / update_edge 必须共用同一份规则文案：两处各写一套正是「改了 A 忘了 B」的源头。
func TestEdgeToolsShareRuleText(t *testing.T) {
	for _, tl := range []Tool{&createEdgeTool{}, &updateEdgeTool{}} {
		meta := tl.Meta()
		if !strings.Contains(meta.Description, edgeRuleText) {
			t.Errorf("%s 的描述没有复用 edgeRuleText（连边规则只能有一个出处）", meta.Name)
		}
		// 位置建议也是共享常量：只给规则不给怎么选位置，模型照样得猜 handle
		if !strings.Contains(meta.Description, handleLayoutAdvice) {
			t.Errorf("%s 的描述没有复用 handleLayoutAdvice", meta.Name)
		}
		// 断言只钉 ASCII 事实（handle 前缀与位置表），不钉散文：
		// 描述会随产品主语言翻译，中文小标题不该成为接口契约。
		for _, want := range []string{"source_handle", "target_handle", "left|top / right|bottom"} {
			if !strings.Contains(meta.Description, want) {
				t.Errorf("%s 描述缺 %q", meta.Name, want)
			}
		}
	}
}

// create_flow 建的边也要带 target_handle：边工具与画布都以它为准。
func TestCreateFlowWritesTargetHandle(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-th"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "th", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "config": map[string]any{
				"code":    map[string]any{"lang": "lua", "script": "return {}"},
				"outputs": map[string]any{},
			}},
			map[string]any{"type": "branch", "config": map[string]any{"children": []any{
				map[string]any{"id": "c1", "condition": "return true"},
			}}},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Edges []flowEdge `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
	}
	for _, e := range doc.Edges {
		if e.TargetHandle == "" {
			t.Errorf("边 %s→%s 没有 target_handle", e.Source, e.Target)
		}
		if e.SourceHandle == "" {
			t.Errorf("边 %s→%s 没有 source_handle（工具生成的边也要与 create_edge 同口径）", e.Source, e.Target)
		}
	}
	// 同一行进入 branch：按位置走 left，不再固定 top
	for _, e := range doc.Edges {
		if e.Target == "3" && e.TargetHandle != "target3left" {
			t.Errorf("进入 branch 的边 target_handle = %q, want target3left", e.TargetHandle)
		}
	}
}

// 连线方向按两个节点的相对位置推荐：同一行走左右端点，落差越大越偏垂直端点；
// 该类型没有这个位置时退到最近的可选位置（所有节点都没有 top 出边）。
func TestSuggestHandlesByPosition(t *testing.T) {
	mk := func(id, typ string, x, y int) *flowNodeMeta {
		n := &flowNodeMeta{ID: id, Type: typ}
		n.Position.X, n.Position.Y = x, y
		return n
	}
	cases := []struct {
		name                   string
		src, dst               *flowNodeMeta
		wantSource, wantTarget string
	}{
		{"同一行目标在右", mk("1", "loop", 0, 0), mk("2", "loop", 300, 0), "source1right", "target2left"},
		{"同一行目标在左", mk("1", "loop", 300, 0), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"目标远在正下方", mk("1", "loop", 0, 0), mk("2", "loop", 20, 400), "source1bottom", "target2top"},
		{"目标远在正上方（无 top 出边 / bottom 入边，退到 left→right）", mk("1", "loop", 0, 0), mk("2", "loop", 20, -400), "source1left", "target2right"},
		{"目标在右下方", mk("1", "loop", 0, 0), mk("2", "loop", 300, 120), "source1right", "target2top"},
		{"目标在左下方", mk("1", "loop", 300, 0), mk("2", "loop", 0, 120), "source1left", "target2top"},
		{"目标在右上方", mk("1", "loop", 0, 200), mk("2", "loop", 300, 0), "source1right", "target2left"},
		{"目标在左上方", mk("1", "loop", 300, 200), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"目标远在右上方（无 top 出边，退到 left）", mk("1", "loop", 0, 500), mk("2", "loop", 300, 0), "source1left", "target2left"},
		{"目标远在左上方（无 top 出边，退到 left）", mk("1", "loop", 300, 500), mk("2", "loop", 0, 0), "source1left", "target2right"},
		{"code 同一行但目标在左：没有 left 出边、right 入边", mk("1", "code", 300, 0), mk("2", "code", 0, 0), "source1right", "target2top"},
		{"start 目标远在上方", mk("1", "start", 0, 400), mk("2", "loop", 300, 0), "source1left", "target2left"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestHandles(tc.src, tc.dst)
			if got.SourceHandle != tc.wantSource || got.TargetHandle != tc.wantTarget {
				t.Fatalf("suggestHandles = %s → %s（%s），want %s → %s",
					got.SourceHandle, got.TargetHandle, got.Reason, tc.wantSource, tc.wantTarget)
			}
		})
	}

	// 退位置的说法要回显出来，否则调用方会以为「按建议连上了」。
	fallback := suggestHandles(mk("1", "loop", 0, 500), mk("2", "loop", 300, 0))
	if !strings.Contains(fallback.Reason, "falling back") {
		t.Errorf("退位置时 reason 应说明退到哪：%q", fallback.Reason)
	}
}

// 缺 handle 时不只报「必填」，还要按位置把该填的值直接给出来。
func TestCreateEdgeMissingHandleSuggests(t *testing.T) {
	c, fk := edgeTestCtx(`{
  "nodes": [
    {"id": "1", "type": "start", "position": {"x": 0, "y": 0}, "data": {"label": "开始"}},
    {"id": "2", "type": "code", "position": {"x": 300, "y": 0}, "data": {"label": "甲"}},
    {"id": "3", "type": "code", "position": {"x": 600, "y": 0}, "data": {"label": "乙"}}
  ],
  "edges": []
}`)
	_, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
	})
	if err == nil {
		t.Fatal("缺 handle 应该报错")
	}
	for _, want := range []string{"source2right", "target3left"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错里应给出建议 %q，实际 = %v", want, err)
		}
	}
	if len(fk.updatedDiagram) != 0 {
		t.Error("校验失败时不应落库")
	}
}

// handle 与位置不匹配时边照样落库（合法），但回 suggested_edge 提示更短的连法。
func TestCreateEdgeSuggestsWhenHandlesMismatch(t *testing.T) {
	c, _ := edgeTestCtx(`{
  "nodes": [
    {"id": "1", "type": "start", "position": {"x": 0, "y": 0}, "data": {"label": "开始"}},
    {"id": "2", "type": "code", "position": {"x": 0, "y": 0}, "data": {"label": "甲"}},
    {"id": "3", "type": "code", "position": {"x": 300, "y": 0}, "data": {"label": "乙"}}
  ],
  "edges": []
}`)
	out, err := (&createEdgeTool{}).Execute(context.Background(), c, map[string]any{
		"flow": "demo", "source": "2", "target": "3",
		"source_handle": "source2bottom", "target_handle": "target3top",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	m := out.(map[string]any)
	sug, ok := m["suggested_edge"].(map[string]any)
	if !ok {
		t.Fatalf("同一行却用了 bottom→top，应回 suggested_edge：%v", m)
	}
	if sug["source_handle"] != "source2right" || sug["target_handle"] != "target3left" {
		t.Errorf("suggested_edge = %v，want source2right / target3left", sug)
	}
}
