package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"flame/nexus/shared/sandboxinfo"
)

// fakeFlows 记录调用、按需返回预置结果；不碰库。
type fakeFlows struct {
	exists    bool
	created   *FlowInput
	createdID string
	list      []FlowInfo
	detail    *FlowDetail
	deleted   [][2]string
	// deleteInfo 非 nil 表示「删到了」；nil 时工具层按「不存在」报错
	deleteInfo *FlowInfo

	// RunTest 的记录与预置结果
	run        *FlowRunResult
	runErr     error
	runRef     string
	runArgs    map[string]any
	runSession string
	runDepth   int
	runDrafts  bool // 本次试跑是否让子流程跟草稿

	// GetRun 的记录与预置结果
	runDetail *FlowRunDetail
	runGetErr error
	runGetID  string

	// UpdateDiagram / UpdateArguments 的记录与预置结果（边工具、入口参数）
	updateRef      [2]string
	updatedDiagram json.RawMessage
	updatedArgs    json.RawMessage
	updateInfo     *FlowInfo
}

func (f *fakeFlows) Exists(context.Context, string) (bool, error) { return f.exists, nil }

func (f *fakeFlows) Create(_ context.Context, in FlowInput) (string, error) {
	f.created = &in
	return f.createdID, nil
}

func (f *fakeFlows) List(context.Context) ([]FlowInfo, error) { return f.list, nil }

func (f *fakeFlows) Get(context.Context, string, string) (*FlowDetail, error) { return f.detail, nil }

func (f *fakeFlows) Delete(_ context.Context, id, name string) (*FlowInfo, error) {
	f.deleted = append(f.deleted, [2]string{id, name})
	return f.deleteInfo, nil
}

func (f *fakeFlows) RunTest(_ context.Context, ref string, args map[string]any, sessionID string, callDepth int, followDrafts bool) (*FlowRunResult, error) {
	f.runRef, f.runArgs, f.runSession, f.runDepth, f.runDrafts = ref, args, sessionID, callDepth, followDrafts
	return f.run, f.runErr
}

func (f *fakeFlows) GetRun(_ context.Context, runID string) (*FlowRunDetail, error) {
	f.runGetID = runID
	return f.runDetail, f.runGetErr
}

// UpdateDiagram 记录落库的 diagram，供边工具断言「真的写回了」。
func (f *fakeFlows) UpdateDiagram(_ context.Context, id, name string, diagram json.RawMessage) (*FlowInfo, error) {
	f.updateRef = [2]string{id, name}
	f.updatedDiagram = diagram
	// 落库后 detail 也要更新：连续调用要看到上一把改动（与真实仓储一致）
	if f.detail != nil {
		f.detail.Diagram = diagram
	}
	if f.updateInfo != nil {
		return f.updateInfo, nil
	}
	if f.detail != nil {
		info := f.detail.FlowInfo
		return &info, nil
	}
	return &FlowInfo{ID: id, Name: name}, nil
}

// UpdateArguments 记录落库的入口参数声明，供 update_flow_arguments 断言「真的写回了」。
func (f *fakeFlows) UpdateArguments(_ context.Context, id, name string, arguments json.RawMessage) (*FlowInfo, error) {
	f.updatedArgs = arguments
	// 落库后 detail 也要更新：连续调用要看到上一把改动（与真实仓储一致）
	if f.detail != nil {
		f.detail.Arguments = arguments
	}
	if f.updateInfo != nil {
		return f.updateInfo, nil
	}
	if f.detail != nil {
		info := f.detail.FlowInfo
		return &info, nil
	}
	return &FlowInfo{ID: id, Name: name}, nil
}

// 工作流元工具都必须落在 CategoryFlowAdmin：supervisor-only 的唯一判据是分类，
// 漏标就等于把「建/删工作流 + 对外开放端点」这种改对外能力面的动作开放给所有 agent。
func TestFlowMetaToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"create_flow", "list_flows", "read_flow", "delete_flow", "run_flow", "read_flow_run",
		"create_edge", "update_edge", "delete_edge",
		"create_node", "insert_node", "update_node", "delete_node",
		"update_flow_arguments"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("%s 未注册", name)
		}
		if got := tool.Meta().Category; got != CategoryFlowAdmin {
			t.Errorf("%s 分类 = %q, want %q", name, got, CategoryFlowAdmin)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%s 应判定为仅超级智能体可用", name)
		}
	}
}

// create_flow 只暴露「步骤」，diagram 由工具层组装：开始节点 + 单链边，坐标逐节点下移。
// 这条守住的是「模型不用手写 26 种节点的 config / 坐标 / 句柄」这个设计前提。
func TestCreateFlowBuildsLinearDiagram(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-1"}
	c := &Context{Flows: fk}

	out, err := (&createFlowTool{}).Execute(context.Background(), c, map[string]any{
		"name":        "weekly-report",
		"description": "每周汇总一次销售数据",
		"steps": []any{
			map[string]any{"type": "llm", "label": "总结", "config": map[string]any{"prompt": "汇总"}},
			map[string]any{"type": "http", "config": map[string]any{"url": "https://example.com"}},
		},
		"external_protocols": []any{"mcp"},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if fk.created == nil {
		t.Fatal("未调用 FlowsManager.Create")
	}
	if got := strings.Join(fk.created.Endpoints, ","); got != "mcp" {
		t.Errorf("endpoints = %q, want mcp", got)
	}

	var g struct {
		Nodes []struct {
			ID       string         `json:"id"`
			Type     string         `json:"type"`
			Position map[string]int `json:"position"`
			Data     map[string]any `json:"data"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &g); err != nil {
		t.Fatalf("diagram 不是合法 JSON: %v", err)
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("nodes/edges = %d/%d, want 3/2", len(g.Nodes), len(g.Edges))
	}
	if g.Nodes[0].Type != "start" {
		t.Errorf("首节点 type = %q, want start", g.Nodes[0].Type)
	}
	if g.Nodes[1].Type != "llm" || g.Nodes[2].Type != "http" {
		t.Errorf("步骤节点类型不符：%q / %q", g.Nodes[1].Type, g.Nodes[2].Type)
	}
	// 默认 label 回落成节点类型，免得画布上一排空白块
	if g.Nodes[2].Data["label"] != "http" {
		t.Errorf("缺省 label = %v, want http", g.Nodes[2].Data["label"])
	}
	for i, e := range g.Edges {
		if e.Source != g.Nodes[i].ID || e.Target != g.Nodes[i+1].ID {
			t.Errorf("边 %d = %s→%s，与节点顺序不一致", i, e.Source, e.Target)
		}
	}
	// 主链横向：同一行 y 相同、x 递增（竖向空间留给分支与折行，不是把主链拉成一条竖线）
	if g.Nodes[1].Position["y"] != g.Nodes[0].Position["y"] || g.Nodes[2].Position["y"] != g.Nodes[1].Position["y"] {
		t.Errorf("线性主链没有排在同一行：%v", []map[string]int{g.Nodes[0].Position, g.Nodes[1].Position, g.Nodes[2].Position})
	}
	if g.Nodes[1].Position["x"] <= g.Nodes[0].Position["x"] || g.Nodes[2].Position["x"] <= g.Nodes[1].Position["x"] {
		t.Errorf("节点未按顺序右移：%v", []map[string]int{g.Nodes[0].Position, g.Nodes[1].Position, g.Nodes[2].Position})
	}
	if len(out.(map[string]any)) == 0 {
		t.Error("返回值不应为空")
	}
}

// 输入边界：模型给什么都不能生成一张跑不通或改不动的流程。
func TestCreateFlowValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			"缺描述",
			map[string]any{"name": "f", "steps": []any{map[string]any{"type": "llm"}}},
			"description must not be empty",
		},
		{
			"空步骤",
			map[string]any{"name": "f", "description": "d", "steps": []any{}},
			"steps must be a non-empty array",
		},
		{
			// start 由工具自动生成，写进 steps 会变成 id!=1 的 start 节点，运行时无执行器
			"结构节点",
			map[string]any{"name": "f", "description": "d", "steps": []any{map[string]any{"type": "start"}}},
			"unsupported step type",
		},
		{
			"无执行器的节点",
			map[string]any{"name": "f", "description": "d", "steps": []any{map[string]any{"type": "knowledge"}}},
			"unsupported step type",
		},
		{
			"非法端点协议",
			map[string]any{
				"name": "f", "description": "d",
				"steps":              []any{map[string]any{"type": "llm"}},
				"external_protocols": []any{"http"},
			},
			"supports mcp / rpc only",
		},
		{
			// 名字就是端点名 / 模型侧工具名 local__<name>：中文能建出来，却对外开放不了、授权不上
			"名字含中文",
			map[string]any{"name": "每日简报", "description": "d", "steps": []any{map[string]any{"type": "llm"}}},
			"letters, digits and hyphens only",
		},
		{
			"名字过长",
			map[string]any{"name": strings.Repeat("a", 65), "description": "d", "steps": []any{map[string]any{"type": "llm"}}},
			"letters, digits and hyphens only",
		},
		{
			// 写坏的 schema 在创建期不报错，到调用方那里才变成「参数没有说明」
			"参数类型不支持",
			map[string]any{"name": "f", "description": "d",
				"steps":     []any{map[string]any{"type": "http"}},
				"arguments": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "date"}}}},
			`type="date"`,
		},
		{
			"数组缺 items",
			map[string]any{"name": "f", "description": "d",
				"steps":     []any{map[string]any{"type": "http"}},
				"arguments": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "array"}}}},
			"needs items",
		},
		{
			"required 指向不存在的参数",
			map[string]any{"name": "f", "description": "d",
				"steps": []any{map[string]any{"type": "http"}},
				"arguments": map[string]any{
					"properties": map[string]any{"x": map[string]any{"type": "string"}},
					"required":   []any{"y"},
				}},
			"properties has no such parameter",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &fakeFlows{}
			_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantErr)
			}
			if fk.created != nil {
				t.Error("校验失败时不应落库")
			}
		})
	}
}

// 控制流 / 容器节点必须能建（早期只放行 5 种执行节点），但要带结构缺口 warnings：
// 「建成了」和「能跑」是两件事，不能静默。
func TestCreateFlowAllowsControlNodesWithWarnings(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-2"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name":        "routed",
		"description": "带分支与循环的流程",
		"steps": []any{
			map[string]any{"type": "http", "config": map[string]any{"http": map[string]any{"url": "https://example.com"}}},
			map[string]any{"type": "branch", "config": map[string]any{"children": []any{}}},
			map[string]any{"type": "loop", "config": map[string]any{"loop": map[string]any{"type": "count", "total_times": 2}}},
			map[string]any{"type": "result", "config": map[string]any{"code": map[string]any{"lang": "template", "script": "done"}}},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if fk.created == nil {
		t.Fatal("未调用 FlowsManager.Create")
	}

	var g struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &g); err != nil {
		t.Fatalf("diagram 非法: %v", err)
	}
	if len(g.Nodes) != 5 || g.Nodes[2].Type != "branch" || g.Nodes[3].Type != "loop" || g.Nodes[4].Type != "result" {
		t.Fatalf("节点未按步骤生成: %+v", g.Nodes)
	}

	m := out.(map[string]any)
	warns, _ := m["warnings"].([]string)
	// branch：config.children 为空 + 没有出边；loop：容器没有子节点
	if len(warns) != 3 {
		t.Fatalf("warnings = %v, want 3 条（branch config + branch 结构 + loop 结构）", warns)
	}
	if !strings.Contains(warns[0], "children") {
		t.Errorf("第 1 条应是 branch 的 children 为空: %v", warns)
	}
	if !strings.Contains(warns[1], "branch") || !strings.Contains(warns[2], "loop") {
		t.Errorf("warnings 文案不符: %v", warns)
	}
}

// 语言写错在保存和画布上都看不出来，要到运行期才以 unsupported_lang 失败，
// 所以创建期就该给 warning —— 模型凭印象写 js 是最典型的来源（系统提示里的
// 「可执行语言」含 js，但那是 exec 通道的，不是 code 节点的可选项）。
func TestCreateFlowWarnsLangNotInWorkflowPool(t *testing.T) {
	old := sandboxinfo.Get()
	t.Cleanup(func() { sandboxinfo.Set(old) })
	sandboxinfo.Set(sandboxinfo.Info{
		Mode:      "docker",
		CodeLangs: []string{"lua", "python"},
		FetchedAt: time.Now(),
	})

	fk := &fakeFlows{createdID: "flow-lang"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "lang-check", "description": "语言校验",
		"steps": []any{
			map[string]any{"type": "code", "config": map[string]any{
				"code":    map[string]any{"lang": "js", "script": "return {}"},
				"outputs": map[string]any{"total": map[string]any{"type": "number"}},
			}},
			map[string]any{"type": "code", "config": map[string]any{
				"code":    map[string]any{"lang": "lua", "script": "return {}"},
				"outputs": map[string]any{"total": map[string]any{"type": "number"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	warns, _ := out.(map[string]any)["warnings"].([]string)
	if len(warns) != 1 {
		t.Fatalf("warnings = %v, want 仅 js 那一步一条", warns)
	}
	for _, want := range []string{"js", "code.lang", "unsupported_lang", "lua, python"} {
		if !strings.Contains(warns[0], want) {
			t.Errorf("warning 缺少 %q: %s", want, warns[0])
		}
	}
}

// 拿不到沙箱清单（没起来 / 旧版 sandboxd 没报该字段）时不能瞎猜，否则每条都报警告。
func TestCreateFlowSkipsLangWarningWithoutSnapshot(t *testing.T) {
	old := sandboxinfo.Get()
	t.Cleanup(func() { sandboxinfo.Set(old) })
	sandboxinfo.Set(sandboxinfo.Info{Err: "dial tcp 127.0.0.1:27081: connect: connection refused"})

	fk := &fakeFlows{createdID: "flow-lang-unknown"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "lang-unknown", "description": "无清单",
		"steps": []any{
			map[string]any{"type": "code", "config": map[string]any{
				"code":    map[string]any{"lang": "js", "script": "return {}"},
				"outputs": map[string]any{"total": map[string]any{"type": "number"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if warns, _ := out.(map[string]any)["warnings"].([]string); len(warns) != 0 {
		t.Errorf("无清单时不该给语言警告: %v", warns)
	}
}

// config 形状写错要直接拒绝：这类问题到画布也修不了，落库只会留一个跑不起来的流程。
func TestCreateFlowRejectsMalformedStepConfig(t *testing.T) {
	cases := []struct {
		name    string
		step    map[string]any
		wantErr string
	}{
		{"code 块不是对象", map[string]any{"type": "code", "config": map[string]any{"code": "return 1"}}, "config.code must be an object"},
		{"children 不是数组", map[string]any{"type": "branch", "config": map[string]any{"children": "a"}}, "config.children must be an array"},
		{"loop.type 非法", map[string]any{"type": "loop", "config": map[string]any{"loop": map[string]any{"type": "nope"}}}, "loop.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &fakeFlows{}
			_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
				"name":        "bad",
				"description": "d",
				"steps":       []any{tc.step},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantErr)
			}
			if fk.created != nil {
				t.Error("校验失败时不应落库")
			}
		})
	}
}

// 已存在时不该走到 Create（否则会撞库里的唯一约束，错误信息还更难懂）。
func TestCreateFlowRejectsDuplicateName(t *testing.T) {
	fk := &fakeFlows{exists: true}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "dup", "description": "d",
		"steps": []any{map[string]any{"type": "llm"}},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want 已存在", err)
	}
	if fk.created != nil {
		t.Error("重名时不应落库")
	}
}

// read_flow 取不到时必须报错：返回 null 会让模型以为「这个流程是空的」。
func TestReadFlowNotFound(t *testing.T) {
	_, err := (&readFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want 不存在", err)
	}
}

// delete_flow 把 ref 同时当 id 与名称传下去，由 manager 决定按哪个命中；
// 返回体要回显真实 id 与名称 —— 名称可能指向另一份定义，删掉的到底是哪一个必须可见。
func TestDeleteFlowPassesRef(t *testing.T) {
	fk := &fakeFlows{deleteInfo: &FlowInfo{ID: "f-1", Name: "weekly-report"}}
	out, err := (&deleteFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{"flow": "weekly-report"})
	if err != nil {
		t.Fatalf("delete_flow = %v", err)
	}
	if len(fk.deleted) != 1 || fk.deleted[0] != [2]string{"weekly-report", "weekly-report"} {
		t.Fatalf("deleted = %v", fk.deleted)
	}
	res, _ := out.(map[string]any)
	if res["flow_id"] != "f-1" || res["flow_name"] != "weekly-report" {
		t.Errorf("应回显真实 id 与名称: %v", res)
	}
}

func TestDeleteFlowNotFound(t *testing.T) {
	_, err := (&deleteFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want 不存在", err)
	}
}

// 定位参数的说明必须把模型往「名字」上引，并且绝不出现 uuid 示例 ——
// 示例里给了 uuid，模型会照着编，编出来的 id 形态合法、只能靠「查不到」兜底。
func TestFlowToolsSteerToNameNotUUID(t *testing.T) {
	for _, tl := range []Tool{&readFlowTool{}, &deleteFlowTool{}, &runFlowTool{}} {
		meta := tl.Meta()
		props, _ := meta.Parameters["properties"].(map[string]any)
		flow, _ := props["flow"].(map[string]any)
		desc, _ := flow["description"].(string)
		if desc != flowRefHint {
			t.Errorf("%s: flow 描述未复用 flowRefHint: %q", meta.Name, desc)
		}
		// 钉 ASCII 事实而不是散文：示例名 + 「id 只能来自 list_flows」这两件事
		// 足以表达「往名字上引、别自己编」，描述的语言变了也不用改测试。
		for _, want := range []string{"daily-sales-report", "list_flows"} {
			if !strings.Contains(desc, want) {
				t.Errorf("%s: 描述缺少 %q: %s", meta.Name, want, desc)
			}
		}
		if strings.Contains(strings.ToLower(desc), "0190") {
			t.Errorf("%s: 描述里不要出现 uuid 示例: %s", meta.Name, desc)
		}
	}
}

// ===== run_flow：建完先跑一次，别等到对外开放端点后才发现跑不通 =====

// 试运行要把调用方的会话与栈深度一并下传：流程里的 agent 节点靠深度收敛，
// 产物要落在同一个会话场地（与 local__ 端点同口径）。
func TestRunFlowToolPassesContext(t *testing.T) {
	const sess = "11111111-1111-1111-1111-111111111111"
	fk := &fakeFlows{run: &FlowRunResult{FlowID: "f1", Status: "success", Result: "ok"}}
	c := &Context{Flows: fk, SessionID: sess, CallDepth: 2}

	out, err := (&runFlowTool{}).Execute(context.Background(), c, map[string]any{
		"flow":   "daily-report",
		"inputs": map[string]any{"q": "x"},
	})
	if err != nil {
		t.Fatalf("run_flow = %v", err)
	}
	if fk.runRef != "daily-report" {
		t.Errorf("ref = %q", fk.runRef)
	}
	if fk.runArgs["q"] != "x" {
		t.Errorf("inputs 没透传: %v", fk.runArgs)
	}
	if fk.runSession != sess || fk.runDepth != 2 {
		t.Errorf("session/depth = %q/%d, want %s/2", fk.runSession, fk.runDepth, sess)
	}
	res, _ := out.(*FlowRunResult)
	if res == nil || res.Status != "success" || res.Result != "ok" {
		t.Errorf("返回体不对: %+v", out)
	}
}

// 非法 session_id 不下传：否则 NewFlow 会报 uuid 错误，被读成「流程跑不起来」。
func TestRunFlowToolDropsInvalidSession(t *testing.T) {
	fk := &fakeFlows{run: &FlowRunResult{Status: "success"}}
	if _, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: fk, SessionID: "not-a-uuid"},
		map[string]any{"flow": "f"}); err != nil {
		t.Fatalf("run_flow = %v", err)
	}
	if fk.runSession != "" {
		t.Errorf("非法 session 应被丢弃，实际 %q", fk.runSession)
	}
}

// 不存在的流程回明确错误，而不是空结果 —— 空结果会被模型读成「跑通了但没输出」。
func TestRunFlowToolNotFound(t *testing.T) {
	_, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want 不存在", err)
	}
}

// inputs 写成 JSON 字符串是常见手滑：直接拒，别让它退化成「入参全空」再跑一遍有副作用的流程。
func TestRunFlowToolRejectsNonObjectInputs(t *testing.T) {
	_, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}},
		map[string]any{"flow": "f", "inputs": `{"q":"x"}`})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("err = %v, want inputs 必须是对象", err)
	}
}

// 输出要有界：最终输出按 MaxOutput 截断，失败节点日志按固定上限留尾部（traceback 在最后）。
// 截断必须落在 rune 边界，否则中文会被劈成 U+FFFD。
func TestRunFlowToolClipsOversizedOutput(t *testing.T) {
	bigResult := strings.Repeat("中", 20000) // 约 60KB
	bigLogs := "前缀噪音" + strings.Repeat("x", 5000) + "末尾报错"
	fk := &fakeFlows{run: &FlowRunResult{
		Status: "failed",
		Result: bigResult,
		Nodes:  []FlowNodeTrace{{NodeID: "3", Status: "failed", Logs: bigLogs}},
	}}
	c := &Context{Flows: fk, MaxOutput: 1024}

	out, err := (&runFlowTool{}).Execute(context.Background(), c, map[string]any{"flow": "f"})
	if err != nil {
		t.Fatalf("run_flow = %v", err)
	}
	res := out.(*FlowRunResult)
	if !res.Truncated {
		t.Error("截断后应置 Truncated")
	}
	if len(res.Result) > 1024+len("\n…（已截断）") {
		t.Errorf("Result 未按上限截断: %d 字节", len(res.Result))
	}
	if !utf8.ValidString(res.Result) {
		t.Error("截断切出了半个汉字（非法 UTF-8）")
	}
	if !strings.HasSuffix(res.Nodes[0].Logs, "末尾报错") {
		t.Error("节点日志应保留末尾（traceback 在那里）")
	}
	if len(res.Nodes[0].Logs) > flowRunNodeLogLimit+len("…（已截断，仅保留末尾）\n") {
		t.Errorf("日志未按上限截断: %d 字节", len(res.Nodes[0].Logs))
	}
	if !utf8.ValidString(res.Nodes[0].Logs) {
		t.Error("日志截断切出了半个汉字")
	}
}

// ===== read_flow_run：run_flow 回「卡在哪」，它回「为什么」 =====

func sampleRunDetail() *FlowRunDetail {
	return &FlowRunDetail{
		FlowID: "f1", RunID: "run-1", Status: "failed", RunType: "debug",
		NodeTotal: 3,
		Nodes: []FlowRunNode{
			{NodeID: "2", Type: "code", Status: "success", OutputVars: map[string]any{"total": 3}},
			{NodeID: "3", Type: "llm", Status: "failed", ErrCode: "timeout", ErrStack: "traceback…"},
			{NodeID: "4", Type: "result", Status: "skipped"},
		},
	}
}

// 缺省只回失败节点：这个工具的用途是定位失败，成功/跳过节点只会白占上下文；
// NodeTotal 要保留过滤前的总数，否则会被读成「整条流程只跑了 1 个节点」。
func TestReadFlowRunDefaultsToFailedNodes(t *testing.T) {
	fk := &fakeFlows{runDetail: sampleRunDetail()}
	out, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("read_flow_run = %v", err)
	}
	if fk.runGetID != "run-1" {
		t.Errorf("run_id 没透传: %q", fk.runGetID)
	}
	res := out.(*FlowRunDetail)
	if len(res.Nodes) != 1 || res.Nodes[0].NodeID != "3" {
		t.Fatalf("nodes = %+v, want 只有失败的 3", res.Nodes)
	}
	if res.NodeTotal != 3 {
		t.Errorf("NodeTotal = %d, want 过滤前的 3", res.NodeTotal)
	}
	if res.Nodes[0].ErrStack == "" {
		t.Error("失败节点要带错误栈")
	}
}

// 显式 only_failed=false 才回全链路；nodes 过滤器可与它叠加。
func TestReadFlowRunFilterOptions(t *testing.T) {
	fk := &fakeFlows{runDetail: sampleRunDetail()}
	out, _ := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk},
		map[string]any{"run_id": "run-1", "only_failed": false})
	if res := out.(*FlowRunDetail); len(res.Nodes) != 3 {
		t.Fatalf("only_failed=false 应回全部 3 个，实际 %d", len(res.Nodes))
	}

	fk = &fakeFlows{runDetail: sampleRunDetail()}
	out, _ = (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk},
		map[string]any{"run_id": "run-1", "only_failed": false, "nodes": []any{"2", "4"}})
	res := out.(*FlowRunDetail)
	if len(res.Nodes) != 2 || res.Nodes[0].NodeID != "2" || res.Nodes[1].NodeID != "4" {
		t.Fatalf("nodes 过滤不生效: %+v", res.Nodes)
	}
}

func TestReadFlowRunNotFound(t *testing.T) {
	_, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"run_id": "run-x"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want 不存在", err)
	}
}

func TestReadFlowRunRequiresRunID(t *testing.T) {
	_, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("err = %v, want run_id", err)
	}
}

// 超大变量必须降级成摘要，而不是切字节：切一半就不是合法 JSON，整个工具结果会序列化失败。
func TestReadFlowRunClipsOversizedVars(t *testing.T) {
	big := strings.Repeat("x", flowRunVarLimit*2)
	detail := &FlowRunDetail{
		RunID: "run-2", NodeTotal: 1,
		Nodes: []FlowRunNode{{
			NodeID: "2", Status: "failed",
			InputVars: map[string]any{"body": big, "q": "x"},
		}},
	}
	fk := &fakeFlows{runDetail: detail}
	out, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{"run_id": "run-2"})
	if err != nil {
		t.Fatalf("read_flow_run = %v", err)
	}
	res := out.(*FlowRunDetail)
	if !res.Truncated {
		t.Error("超限后应置 Truncated")
	}
	summary, ok := res.Nodes[0].InputVars.(map[string]any)
	if !ok || summary["_truncated"] != true {
		t.Fatalf("超大变量应降级为摘要: %#v", res.Nodes[0].InputVars)
	}
	keys, _ := summary["keys"].([]string)
	if len(keys) != 2 || keys[0] != "body" || keys[1] != "q" {
		t.Errorf("摘要应保留排序后的顶层 key: %v", keys)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Errorf("结果必须仍是合法 JSON: %v", err)
	}
}

// ===== 分支结构：从每个 child 拉出带 handle 的边 =====
//
// 执行器是靠 handle 前缀找路的（executors.findBranchEdge 匹配 "source"+childID），
// 所以这里断言的不能只是「有边」，而是 handle 的拼法与目标节点。

type testDiagram struct {
	Nodes []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Label  string         `json:"label"`
			Config map[string]any `json:"config"`
		} `json:"data"`
	} `json:"nodes"`
	Edges []struct {
		ID           string  `json:"id"`
		Source       string  `json:"source"`
		Target       string  `json:"target"`
		SourceHandle *string `json:"source_handle"`
		TargetHandle *string `json:"target_handle"`
	} `json:"edges"`
}

func parseTestDiagram(t *testing.T, raw []byte) testDiagram {
	t.Helper()
	var d testDiagram
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("diagram 非法: %v", err)
	}
	return d
}

func edgeHandle(t *testing.T, d testDiagram, source, target string) string {
	t.Helper()
	for _, e := range d.Edges {
		if e.Source == source && e.Target == target {
			if e.SourceHandle == nil {
				return ""
			}
			return *e.SourceHandle
		}
	}
	t.Fatalf("找不到边 %s→%s，实际 %+v", source, target, d.Edges)
	return ""
}

// create_flow 落库的边必须与 create_edge 的建议一致（两者共用 suggestHandlePositions）：
// 谁要是又给容器固定 top、或者只改一边，这里就会失败。
func TestCreateFlowHandlesMatchEdgeSuggestion(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-sug"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "sug-check", "description": "d",
		"steps": []any{
			map[string]any{"type": "code", "label": "甲", "config": map[string]any{
				"code": map[string]any{"lang": "lua", "script": "return {}"},
			}},
			map[string]any{"type": "loop", "label": "循环", "config": map[string]any{
				"mode":     "count",
				"times":    2,
				"children": []any{map[string]any{"id": "l1"}},
			}},
			map[string]any{"type": "branch", "label": "判断", "config": map[string]any{
				"children": []any{map[string]any{"id": "c1", "condition": "return true"}},
			}, "branches": []any{map[string]any{"key": "c1", "steps": []any{map[string]any{
				"type": "result", "label": "出", "config": map[string]any{
					"code": map[string]any{"lang": "template", "script": "ok"},
				},
			}}}}},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}

	var doc struct {
		Nodes []struct {
			ID       string         `json:"id"`
			Type     string         `json:"type"`
			Position map[string]int `json:"position"`
		} `json:"nodes"`
		Edges []struct {
			Source       string `json:"source"`
			Target       string `json:"target"`
			SourceHandle string `json:"source_handle"`
			TargetHandle string `json:"target_handle"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &doc); err != nil {
		t.Fatalf("diagram 解析失败: %v", err)
	}
	type nodeInfo struct {
		typ  string
		x, y int
	}
	byID := make(map[string]nodeInfo, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.ID] = nodeInfo{typ: n.Type, x: n.Position["x"], y: n.Position["y"]}
	}

	for _, e := range doc.Edges {
		s, d := byID[e.Source], byID[e.Target]
		srcPos, dstPos, reason := suggestHandlePositions(s.typ, s.x, s.y, d.typ, d.x, d.y)
		wantTarget := "target" + e.Target + dstPos
		if e.TargetHandle != wantTarget {
			t.Errorf("边 %s→%s 的 target_handle = %q，want %q（%s）",
				e.Source, e.Target, e.TargetHandle, wantTarget, reason)
		}
		// 分支子链首边的 owner 是 children[].id（语义 key），不受位置建议约束。
		if !strings.HasPrefix(e.SourceHandle, "source"+e.Source) {
			continue
		}
		wantSource := "source" + e.Source + srcPos
		if e.SourceHandle != wantSource {
			t.Errorf("边 %s→%s 的 source_handle = %q，want %q（%s）",
				e.Source, e.Target, e.SourceHandle, wantSource, reason)
		}
	}
}

// 线上真实形态：code → branch(c1/c2) → 两条 result，且 branch 没有 default 分支。
func TestCreateFlowBranchChildrenEdges(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-b"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "calc-branch", "description": "算 5*9 并判断",
		"steps": []any{
			map[string]any{"type": "code", "label": "计算5*9", "config": map[string]any{
				"code":    map[string]any{"lang": "lua", "script": "return { result = 45 }"},
				"outputs": map[string]any{"result": map[string]any{"type": "number"}},
			}},
			map[string]any{
				"type": "branch", "label": "判断是否等于45",
				"config": map[string]any{
					// source_ref 故意写 label：模型很难心算 id，工具应改成真实 id
					"inputs": map[string]any{"result": map[string]any{
						"type": "number", "source_key": "result", "source_type": "node", "source_ref": "计算5*9",
					}},
					"children": []any{
						map[string]any{"id": "c1", "label": "等于45", "condition": "return input.result == 45", "exclusive": true},
						map[string]any{"id": "c2", "label": "不等于45", "condition": "return input.result ~= 45", "exclusive": true},
					},
				},
				"branches": []any{
					map[string]any{"key": "c1", "steps": []any{map[string]any{"type": "result", "label": "输出正确",
						"config": map[string]any{"code": map[string]any{"lang": "template", "script": "正确"}}}}},
					map[string]any{"key": "c2", "steps": []any{map[string]any{"type": "result", "label": "输出错误",
						"config": map[string]any{"code": map[string]any{"lang": "template", "script": "错误"}}}}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	d := parseTestDiagram(t, fk.created.Diagram)

	if len(d.Nodes) != 5 || len(d.Edges) != 4 {
		t.Fatalf("nodes/edges = %d/%d, want 5/4", len(d.Nodes), len(d.Edges))
	}
	for i, typ := range []string{"start", "code", "branch", "result", "result"} {
		if d.Nodes[i].Type != typ || d.Nodes[i].ID != strconv.Itoa(i+1) {
			t.Fatalf("节点 %d = %s(id=%s)，want %s(id=%d)", i, d.Nodes[i].Type, d.Nodes[i].ID, typ, i+1)
		}
	}
	// 主链边也要带 handle：运行期虽然只走 OutEdges[0]，但边工具（insert_node / update_edge）
	// 都以 handle 为准，空 handle 就是「能跑、改不动」的边。
	if h := edgeHandle(t, d, "1", "2"); h != "source1right" {
		t.Errorf("主链边 source handle = %q, want source1right", h)
	}
	// 分支子链首边的 owner 是 children[].id（语义 key），排版后回填 handle 时不能被位置改写
	if h := edgeHandle(t, d, "3", "4"); h != "sourcec1right" {
		t.Errorf("分支子链首边 source handle = %q, want sourcec1right", h)
	}
	if h := edgeHandle(t, d, "3", "5"); h != "sourcec2right" {
		t.Errorf("分支子链首边 source handle = %q, want sourcec2right", h)
	}
	for _, e := range d.Edges {
		if e.SourceHandle == nil || *e.SourceHandle == "" {
			t.Errorf("边 %s→%s 的 source_handle 为空：工具生成的边也要带 handle", e.Source, e.Target)
		}
		if e.TargetHandle == nil || *e.TargetHandle == "" {
			t.Errorf("边 %s→%s 的 target_handle 为空", e.Source, e.Target)
		}
	}
	if h := edgeHandle(t, d, "3", "4"); h != "sourcec1right" {
		t.Errorf("c1 分支边 handle = %q, want sourcec1right", h)
	}
	if h := edgeHandle(t, d, "3", "5"); h != "sourcec2right" {
		t.Errorf("c2 分支边 handle = %q, want sourcec2right", h)
	}

	// source_ref 写 label 应被改写成真实 id（2 = 计算5*9 那个节点）
	ref := objectOf(objectOf(d.Nodes[2].Data.Config["inputs"])["result"])["source_ref"]
	if ref != "2" {
		t.Errorf("source_ref = %v, want \"2\"", ref)
	}

	m := out.(map[string]any)
	if nodes, _ := m["nodes"].([]map[string]any); len(nodes) != 5 || nodes[2]["type"] != "branch" {
		t.Errorf("返回体应带节点清单: %v", m["nodes"])
	}
	if warns, _ := m["warnings"].([]string); len(warns) != 0 {
		t.Errorf("结构完整时不该有 warning: %v", warns)
	}
}

// 默认分支的 handle 用节点自身 id（执行器 findBranchEdge(node, node.ID) 就是这么找的）。
func TestCreateFlowBranchDefaultHandle(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-d"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "default-branch", "description": "带兜底分支",
		"steps": []any{
			map[string]any{
				"type": "branch", "label": "分流",
				"config": map[string]any{"children": []any{
					map[string]any{"id": "c1", "condition": "return true"},
				}},
				"branches": []any{
					map[string]any{"key": "c1", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
					map[string]any{"key": "default", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	d := parseTestDiagram(t, fk.created.Diagram)
	if len(d.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4（start + branch + 两个 result）", len(d.Nodes))
	}
	if h := edgeHandle(t, d, "2", "4"); h != "source2right" {
		t.Errorf("default 边 handle = %q, want source2right", h)
	}
}

// 分支子链里再嵌分支：id 连续、两条 handle 各归各的。
func TestCreateFlowNestedBranch(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-n"}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "nested", "description": "两层分支",
		"steps": []any{
			map[string]any{
				"type": "branch", "label": "外层",
				"config": map[string]any{"children": []any{map[string]any{"id": "a", "condition": "return true"}}},
				"branches": []any{
					map[string]any{"key": "a", "steps": []any{
						map[string]any{
							"type": "branch", "label": "内层",
							"config": map[string]any{"children": []any{map[string]any{"id": "b", "condition": "return true"}}},
							"branches": []any{
								map[string]any{"key": "b", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
							},
						},
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	d := parseTestDiagram(t, fk.created.Diagram)
	if len(d.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4（start + 外 + 内 + result）", len(d.Nodes))
	}
	if h := edgeHandle(t, d, "2", "3"); h != "sourcearight" {
		t.Errorf("外层 handle = %q", h)
	}
	if h := edgeHandle(t, d, "3", "4"); h != "sourcebright" {
		t.Errorf("内层 handle = %q", h)
	}
}

// 声明了 branches 就必须是所在链的最后一步：branch 执行完只走 default 边，后面接主链没有语义。
func TestCreateFlowBranchMustBeTerminal(t *testing.T) {
	fk := &fakeFlows{}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "bad", "description": "d",
		"steps": []any{
			map[string]any{
				"type":   "branch",
				"config": map[string]any{"children": []any{map[string]any{"id": "c1"}}},
				"branches": []any{
					map[string]any{"key": "c1", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
				},
			},
			map[string]any{"type": "result", "config": map[string]any{}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must be the last step") {
		t.Fatalf("err = %v, want 提示 branches 必须在最后一步", err)
	}
	if fk.created != nil {
		t.Error("校验失败时不应落库")
	}
}

// key 写错是最容易发生的一类手滑：必须列出可用 id，而不是静默建出一条永远走不到的路。
func TestCreateFlowBranchUnknownKey(t *testing.T) {
	fk := &fakeFlows{}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "bad-key", "description": "d",
		"steps": []any{
			map[string]any{
				"type":   "branch",
				"config": map[string]any{"children": []any{map[string]any{"id": "c1", "condition": "return true"}}},
				"branches": []any{
					map[string]any{"key": "c9", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "c9") || !strings.Contains(err.Error(), "default") {
		t.Fatalf("err = %v, want 回显 c9 与可用 id", err)
	}
}

// 漏声明某个子分支：不拦（允许先建结构后补链），但要提示「命中后会被跳过」。
func TestCreateFlowBranchMissingCaseWarns(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-w"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "partial", "description": "d",
		"steps": []any{
			map[string]any{
				"type": "branch",
				"config": map[string]any{"children": []any{
					map[string]any{"id": "c1", "condition": "return true"},
					map[string]any{"id": "c2", "condition": "return false"},
				}},
				"branches": []any{
					map[string]any{"key": "c1", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	warns, _ := out.(map[string]any)["warnings"].([]string)
	if len(warns) != 1 || !strings.Contains(warns[0], `"c2"`) || !strings.Contains(warns[0], "no outgoing edge") {
		t.Fatalf("warnings = %v, want 一条 c2 没有出边的提示", warns)
	}
}

// branches 只对 branch / intent 有意义，别的类型传了要拒绝（否则会静默生成看不懂的边）。
func TestCreateFlowBranchesRejectedOnNonBranch(t *testing.T) {
	fk := &fakeFlows{}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "bad-type", "description": "d",
		"steps": []any{
			map[string]any{
				"type": "code", "config": map[string]any{},
				"branches": []any{
					map[string]any{"key": "c1", "steps": []any{map[string]any{"type": "result", "config": map[string]any{}}}},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support branches") {
		t.Fatalf("err = %v, want 只有 branch / intent 有子分支", err)
	}
}

// 入口参数过去只能在前端补：工具建的流程对外开放后没有 input_schema，
// 客户端拿到的是一个没有参数说明的端点。这条守住「create_flow 能写 arguments」。
func TestCreateFlowWritesArguments(t *testing.T) {
	fk := &fakeFlows{createdID: "flow-args"}
	out, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name":        "weather-brief",
		"description": "按城市出天气简报",
		"steps": []any{
			map[string]any{"type": "http", "config": map[string]any{"url": "https://example.com"}},
		},
		"arguments": map[string]any{
			"properties": map[string]any{
				"city": map[string]any{"type": "string", "description": "城市名"},
				"days": map[string]any{"type": "number"},
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"filter": map[string]any{
					"type":       "object",
					"properties": map[string]any{"keyword": map[string]any{"type": "string"}},
					"required":   []any{"keyword"},
				},
			},
			"required": []any{"city"},
		},
	})
	if err != nil {
		t.Fatalf("create_flow = %v", err)
	}
	if fk.created == nil || len(fk.created.Arguments) == 0 {
		t.Fatal("入口参数没有写进 FlowInput.Arguments")
	}
	var got struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(fk.created.Arguments, &got); err != nil {
		t.Fatalf("arguments 不是合法 JSON: %v", err)
	}
	if got.Type != "object" || len(got.Properties) != 4 || len(got.Required) != 1 || got.Required[0] != "city" {
		t.Fatalf("arguments 规整结果不符: %+v", got)
	}
	if names, _ := out.(map[string]any)["arguments"].([]string); strings.Join(names, ",") != "city,days,filter,tags" {
		t.Errorf("返回的参数名 = %v，应按名字排序", names)
	}
}

// 入口参数必须能原地改：靠 delete_flow + create_flow 重建会换掉 flows.id，
// 服务凭证的白名单按 target_id 授权，旧行会静默失效。
func TestUpdateFlowArgumentsMerges(t *testing.T) {
	fk := &fakeFlows{detail: &FlowDetail{
		FlowInfo: FlowInfo{ID: "f1", Name: "demo"},
		Arguments: json.RawMessage(
			`{"type":"object","properties":{"city":{"type":"string","description":"城市"},"debug":{"type":"boolean"}},"required":["city"]}`),
	}}
	out, err := (&updateFlowArgumentsTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"flow":   "demo",
		"set":    map[string]any{"days": map[string]any{"type": "number"}},
		"remove": []any{"debug"},
	})
	if err != nil {
		t.Fatalf("update_flow_arguments = %v", err)
	}

	var got struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(fk.updatedArgs, &got); err != nil {
		t.Fatalf("落库的 arguments 非法: %v", err)
	}
	if len(got.Properties) != 2 {
		t.Fatalf("properties = %v, want city + days（debug 应被删掉）", got.Properties)
	}
	if _, ok := got.Properties["debug"]; ok {
		t.Error("remove 没生效")
	}
	if len(got.Required) != 1 || got.Required[0] != "city" {
		t.Errorf("required = %v, want [city]（没传 required 时原有必填不能被清掉）", got.Required)
	}
	if !strings.Contains(string(fk.updatedArgs), `"description":"城市"`) {
		t.Error("未被点名的参数（city 的 description）被清洗掉了：合并只该重写 set 里的参数")
	}
	if names, _ := out.(map[string]any)["arguments"].([]string); strings.Join(names, ",") != "city,days" {
		t.Errorf("返回的参数名 = %v, want city,days", names)
	}
}

// required 传了就整体替换：把 days 提成必填，并允许把 city 降级。
func TestUpdateFlowArgumentsReplacesRequired(t *testing.T) {
	fk := &fakeFlows{detail: &FlowDetail{
		FlowInfo:  FlowInfo{ID: "f1", Name: "demo"},
		Arguments: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"number"}},"required":["city"]}`),
	}}
	if _, err := (&updateFlowArgumentsTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"flow":     "demo",
		"required": []any{"days"},
	}); err != nil {
		t.Fatalf("update_flow_arguments = %v", err)
	}
	var got struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(fk.updatedArgs, &got); err != nil {
		t.Fatalf("落库的 arguments 非法: %v", err)
	}
	if len(got.Required) != 1 || got.Required[0] != "days" {
		t.Errorf("required = %v, want [days]", got.Required)
	}
}

// 校验必须发生在落库之前：一次坏输入不能把现有声明写坏。
func TestUpdateFlowArgumentsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			"坏 type",
			map[string]any{"flow": "demo", "set": map[string]any{"t": map[string]any{"type": "date"}}},
			`type="date"`,
		},
		{
			"数组缺 items",
			map[string]any{"flow": "demo", "set": map[string]any{"ids": map[string]any{"type": "array"}}},
			"needs items",
		},
		{
			"required 指向不存在的参数",
			map[string]any{"flow": "demo", "set": map[string]any{"b": map[string]any{"type": "string"}}, "required": []any{"zzz"}},
			"properties has no such parameter",
		},
		{
			"set 与 remove 同名",
			map[string]any{"flow": "demo", "set": map[string]any{"a": map[string]any{"type": "string"}}, "remove": []any{"a"}},
			"is in both set and remove",
		},
		{
			"什么都没给",
			map[string]any{"flow": "demo"},
			"give at least one of",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &fakeFlows{detail: &FlowDetail{
				FlowInfo:  FlowInfo{ID: "f1", Name: "demo"},
				Arguments: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`),
			}}
			_, err := (&updateFlowArgumentsTool{}).Execute(context.Background(), &Context{Flows: fk}, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantErr)
			}
			if len(fk.updatedArgs) != 0 {
				t.Error("校验失败时不应落库")
			}
		})
	}
}

// 不存在的流程要明确报错，而不是悄悄建一份空声明。
func TestUpdateFlowArgumentsMissingFlow(t *testing.T) {
	_, err := (&updateFlowArgumentsTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{
		"flow": "nope",
		"set":  map[string]any{"a": map[string]any{"type": "string"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want 不存在", err)
	}
}
