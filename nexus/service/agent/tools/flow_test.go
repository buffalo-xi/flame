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

// fakeFlows records calls and returns preset results on demand; it never touches the database.
type fakeFlows struct {
	exists    bool
	created   *FlowInput
	createdID string
	list      []FlowInfo
	detail    *FlowDetail
	deleted   [][2]string
	// A non-nil deleteInfo means "it was deleted"; when nil the tool layer reports "does not exist".
	deleteInfo *FlowInfo

	// RunTest records and preset result
	run        *FlowRunResult
	runErr     error
	runRef     string
	runArgs    map[string]any
	runSession string
	runDepth   int
	runDrafts  bool // whether this test run lets sub-flows follow drafts

	// GetRun records and preset result
	runDetail *FlowRunDetail
	runGetErr error
	runGetID  string

	// UpdateDiagram / UpdateArguments records and preset results (edge tools, entry arguments)
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

// UpdateDiagram records the stored diagram so the edge tools can assert that it really was written back.
func (f *fakeFlows) UpdateDiagram(_ context.Context, id, name string, diagram json.RawMessage) (*FlowInfo, error) {
	f.updateRef = [2]string{id, name}
	f.updatedDiagram = diagram
	// detail is updated after storing too: consecutive calls must see the previous change (as the real repository does)
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

// UpdateArguments records the stored entry-argument declaration so update_flow_arguments can assert that it really was written
// back.
func (f *fakeFlows) UpdateArguments(_ context.Context, id, name string, arguments json.RawMessage) (*FlowInfo, error) {
	f.updatedArgs = arguments
	// detail is updated after storing too: consecutive calls must see the previous change (as the real repository does)
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

// The flow meta tools must all sit in CategoryFlowAdmin: the category is the only supervisor-only criterion, and an untagged tool
// opens "create / delete a flow + expose an endpoint" - actions that change the external capability surface - to every agent.
func TestFlowMetaToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{"create_flow", "list_flows", "read_flow", "delete_flow", "run_flow", "read_flow_run",
		"create_edge", "update_edge", "delete_edge",
		"create_node", "insert_node", "update_node", "delete_node",
		"update_flow_arguments"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := tool.Meta().Category; got != CategoryFlowAdmin {
			t.Errorf("%s category = %q, want %q", name, got, CategoryFlowAdmin)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%s should be classified as super-agent only", name)
		}
	}
}

// create_flow only exposes "steps"; the diagram is assembled by the tool layer: a start node + single-chain edges, coordinates
// shifting down node by node.
// This guards the design premise "the model never hand-writes the config / coordinates / handles of 26 node types".
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
		t.Fatal("FlowsManager.Create was not called")
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
		t.Fatalf("the diagram is not valid JSON: %v", err)
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("nodes/edges = %d/%d, want 3/2", len(g.Nodes), len(g.Edges))
	}
	if g.Nodes[0].Type != "start" {
		t.Errorf("the first node's type = %q, want start", g.Nodes[0].Type)
	}
	if g.Nodes[1].Type != "llm" || g.Nodes[2].Type != "http" {
		t.Errorf("step node types differ: %q / %q", g.Nodes[1].Type, g.Nodes[2].Type)
	}
	// The default label falls back to the node type, so the canvas does not show a row of blank blocks
	if g.Nodes[2].Data["label"] != "http" {
		t.Errorf("default label = %v, want http", g.Nodes[2].Data["label"])
	}
	for i, e := range g.Edges {
		if e.Source != g.Nodes[i].ID || e.Target != g.Nodes[i+1].ID {
			t.Errorf("edge %d = %s->%s, which does not match the node order", i, e.Source, e.Target)
		}
	}
	// The main chain runs horizontally: same y, increasing x (vertical space is left to branches and wrapping, rather than drawing
	// the main chain as a vertical line)
	if g.Nodes[1].Position["y"] != g.Nodes[0].Position["y"] || g.Nodes[2].Position["y"] != g.Nodes[1].Position["y"] {
		t.Errorf("the linear main chain is not on one row: %v", []map[string]int{g.Nodes[0].Position, g.Nodes[1].Position, g.Nodes[2].Position})
	}
	if g.Nodes[1].Position["x"] <= g.Nodes[0].Position["x"] || g.Nodes[2].Position["x"] <= g.Nodes[1].Position["x"] {
		t.Errorf("the nodes do not move right in order: %v", []map[string]int{g.Nodes[0].Position, g.Nodes[1].Position, g.Nodes[2].Position})
	}
	if len(out.(map[string]any)) == 0 {
		t.Error("the result must not be empty")
	}
}

// The input boundary: whatever the model supplies must not produce a flow that cannot run or cannot be changed.
func TestCreateFlowValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			"missing description",
			map[string]any{"name": "f", "steps": []any{map[string]any{"type": "llm"}}},
			"description must not be empty",
		},
		{
			"empty steps",
			map[string]any{"name": "f", "description": "d", "steps": []any{}},
			"steps must be a non-empty array",
		},
		{
			// start is generated by the tool; writing it into steps creates a start node with id != 1, which has no executor at run
			// time
			"structural node",
			map[string]any{"name": "f", "description": "d", "steps": []any{map[string]any{"type": "start"}}},
			"unsupported step type",
		},
		{
			"a node with no executor",
			map[string]any{"name": "f", "description": "d", "steps": []any{map[string]any{"type": "knowledge"}}},
			"unsupported step type",
		},
		{
			"illegal endpoint protocol",
			map[string]any{
				"name": "f", "description": "d",
				"steps":              []any{map[string]any{"type": "llm"}},
				"external_protocols": []any{"http"},
			},
			"supports mcp / rpc only",
		},
		{
			// The name is the endpoint name / model-side tool name local__<name>: Chinese can be created, but never exposed or
			// granted
			"name contains Chinese",
			map[string]any{"name": "每日简报", "description": "d", "steps": []any{map[string]any{"type": "llm"}}},
			"letters, digits and hyphens only",
		},
		{
			"name too long",
			map[string]any{"name": strings.Repeat("a", 65), "description": "d", "steps": []any{map[string]any{"type": "llm"}}},
			"letters, digits and hyphens only",
		},
		{
			// A broken schema does not fail at creation, it only turns into "the parameter has no description" on the caller's side
			"unsupported parameter type",
			map[string]any{"name": "f", "description": "d",
				"steps":     []any{map[string]any{"type": "http"}},
				"arguments": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "date"}}}},
			`type="date"`,
		},
		{
			"array without items",
			map[string]any{"name": "f", "description": "d",
				"steps":     []any{map[string]any{"type": "http"}},
				"arguments": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "array"}}}},
			"needs items",
		},
		{
			"required points at a missing parameter",
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
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if fk.created != nil {
				t.Error("nothing should be stored when validation fails")
			}
		})
	}
}

// Control-flow / container nodes must be creatable (early on only 5 execution nodes were allowed), but structural gaps must come back
// as warnings: "created" and "runnable" are two different things, and neither may be silent.
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
		t.Fatal("FlowsManager.Create was not called")
	}

	var g struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fk.created.Diagram, &g); err != nil {
		t.Fatalf("invalid diagram: %v", err)
	}
	if len(g.Nodes) != 5 || g.Nodes[2].Type != "branch" || g.Nodes[3].Type != "loop" || g.Nodes[4].Type != "result" {
		t.Fatalf("the nodes were not generated from the steps: %+v", g.Nodes)
	}

	m := out.(map[string]any)
	warns, _ := m["warnings"].([]string)
	// branch: config.children is empty + no outgoing edge; loop: the container has no children
	if len(warns) != 3 {
		t.Fatalf("warnings = %v, want 3 (branch config + branch structure + loop structure)", warns)
	}
	if !strings.Contains(warns[0], "children") {
		t.Errorf("the first should be branch's empty children: %v", warns)
	}
	if !strings.Contains(warns[1], "branch") || !strings.Contains(warns[2], "loop") {
		t.Errorf("unexpected warning copy: %v", warns)
	}
}

// A wrong language is invisible when saving and on the canvas, and only fails at run time with unsupported_lang, so creation has to
// warn - the model writing js from memory is the most typical source (the "executable languages" in the system prompt include js,
// but that belongs to the exec channel, not to what a code node may use).
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
		t.Fatalf("warnings = %v, want exactly one, for the js step", warns)
	}
	for _, want := range []string{"js", "code.lang", "unsupported_lang", "lua, python"} {
		if !strings.Contains(warns[0], want) {
			t.Errorf("the warning is missing %q: %s", want, warns[0])
		}
	}
}

// When the sandbox list is unavailable (the sandbox is down / an old sandboxd does not report the field) nothing may be guessed,
// or every flow would carry a warning.
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
		t.Errorf("there should be no language warning without the list: %v", warns)
	}
}

// A malformed config shape must be refused outright: such a problem cannot be fixed on the canvas either, and storing it only leaves
// behind a flow that does not run.
func TestCreateFlowRejectsMalformedStepConfig(t *testing.T) {
	cases := []struct {
		name    string
		step    map[string]any
		wantErr string
	}{
		{"the code block is not an object", map[string]any{"type": "code", "config": map[string]any{"code": "return 1"}}, "config.code must be an object"},
		{"children is not an array", map[string]any{"type": "branch", "config": map[string]any{"children": "a"}}, "config.children must be an array"},
		{"illegal loop.type", map[string]any{"type": "loop", "config": map[string]any{"loop": map[string]any{"type": "nope"}}}, "loop.type"},
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
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if fk.created != nil {
				t.Error("nothing should be stored when validation fails")
			}
		})
	}
}

// When it already exists, Create must not be called (otherwise it hits the database's unique constraint, and the error message is
// harder to understand).
func TestCreateFlowRejectsDuplicateName(t *testing.T) {
	fk := &fakeFlows{exists: true}
	_, err := (&createFlowTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{
		"name": "dup", "description": "d",
		"steps": []any{map[string]any{"type": "llm"}},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
	if fk.created != nil {
		t.Error("nothing should be stored on a duplicate name")
	}
}

// read_flow must error when it finds nothing: returning null makes the model believe "this flow is empty".
func TestReadFlowNotFound(t *testing.T) {
	_, err := (&readFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}
}

// delete_flow passes ref as both id and name, and the manager decides which one hits; the result must echo the real id and name -
// a name may point at another definition, and which one was deleted has to be visible.
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
		t.Errorf("the real id and name should be echoed: %v", res)
	}
}

func TestDeleteFlowNotFound(t *testing.T) {
	_, err := (&deleteFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}
}

// The targeting argument's description must steer the model towards "the name", and must never contain a uuid example: given one,
// the model writes one of its own, and the invented id is well-formed enough that only "not found" catches it.
func TestFlowToolsSteerToNameNotUUID(t *testing.T) {
	for _, tl := range []Tool{&readFlowTool{}, &deleteFlowTool{}, &runFlowTool{}} {
		meta := tl.Meta()
		props, _ := meta.Parameters["properties"].(map[string]any)
		flow, _ := props["flow"].(map[string]any)
		desc, _ := flow["description"].(string)
		if desc != flowRefHint {
			t.Errorf("%s: the flow description does not reuse flowRefHint: %q", meta.Name, desc)
		}
		// Pin ASCII facts rather than prose: the example name plus "the id can only come from list_flows" says enough about "steer
		// to the name, do not invent one", and the test survives a language change in the description.
		for _, want := range []string{"daily-sales-report", "list_flows"} {
			if !strings.Contains(desc, want) {
				t.Errorf("%s: the description is missing %q: %s", meta.Name, want, desc)
			}
		}
		if strings.Contains(strings.ToLower(desc), "0190") {
			t.Errorf("%s: the description must not contain a uuid example: %s", meta.Name, desc)
		}
	}
}

// ===== run_flow: run it once after creating it, rather than discovering it does not work only after exposing an endpoint =====

// A test run must pass the caller's session and stack depth down: the agent nodes in a flow converge by depth, and the artefacts have
// to land in the same session venue (the same rule as a local__ endpoint).
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
		t.Errorf("inputs were not forwarded: %v", fk.runArgs)
	}
	if fk.runSession != sess || fk.runDepth != 2 {
		t.Errorf("session/depth = %q/%d, want %s/2", fk.runSession, fk.runDepth, sess)
	}
	res, _ := out.(*FlowRunResult)
	if res == nil || res.Status != "success" || res.Result != "ok" {
		t.Errorf("unexpected result: %+v", out)
	}
}

// An invalid session_id is not passed down: otherwise NewFlow reports a uuid error, which is read as "the flow will not run".
func TestRunFlowToolDropsInvalidSession(t *testing.T) {
	fk := &fakeFlows{run: &FlowRunResult{Status: "success"}}
	if _, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: fk, SessionID: "not-a-uuid"},
		map[string]any{"flow": "f"}); err != nil {
		t.Fatalf("run_flow = %v", err)
	}
	if fk.runSession != "" {
		t.Errorf("an invalid session should be dropped, got %q", fk.runSession)
	}
}

// A flow that does not exist gives an explicit error rather than an empty result - an empty result is read as "it ran but produced
// nothing".
func TestRunFlowToolNotFound(t *testing.T) {
	_, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"flow": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}
}

// Writing inputs as a JSON string is a common slip: refuse it outright, rather than letting it degrade into "all inputs empty" and
// running a flow with side effects again.
func TestRunFlowToolRejectsNonObjectInputs(t *testing.T) {
	_, err := (&runFlowTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}},
		map[string]any{"flow": "f", "inputs": `{"q":"x"}`})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("err = %v, want inputs to be an object", err)
	}
}

// Output must be bounded: the final output is cut at MaxOutput, a failed node's logs keep the tail up to a fixed limit (the traceback
// is at the end).
// Truncation must land on a rune boundary, or multi-byte characters are split into U+FFFD.
func TestRunFlowToolClipsOversizedOutput(t *testing.T) {
	bigResult := strings.Repeat("中", 20000) // about 60KB
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
		t.Error("Truncated should be set after clipping")
	}
	if len(res.Result) > 1024+len("\n… (truncated)") {
		t.Errorf("Result was not clipped to the limit: %d bytes", len(res.Result))
	}
	if !utf8.ValidString(res.Result) {
		t.Error("clipping cut a multi-byte character in half (invalid UTF-8)")
	}
	if !strings.HasSuffix(res.Nodes[0].Logs, "末尾报错") {
		t.Error("the node logs should keep the tail (that is where the traceback is)")
	}
	if len(res.Nodes[0].Logs) > flowRunNodeLogLimit+len("… (truncated, tail kept)\n") {
		t.Errorf("the logs were not clipped to the limit: %d bytes", len(res.Nodes[0].Logs))
	}
	if !utf8.ValidString(res.Nodes[0].Logs) {
		t.Error("log clipping cut a multi-byte character in half")
	}
}

// ===== read_flow_run: run_flow says "where it got stuck", this says "why" =====

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

// By default only failed nodes come back: this tool exists to locate failures, and successful / skipped nodes only waste context;
// NodeTotal keeps the count from before filtering, or it reads as "the whole flow ran only 1 node".
func TestReadFlowRunDefaultsToFailedNodes(t *testing.T) {
	fk := &fakeFlows{runDetail: sampleRunDetail()}
	out, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk}, map[string]any{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("read_flow_run = %v", err)
	}
	if fk.runGetID != "run-1" {
		t.Errorf("run_id was not forwarded: %q", fk.runGetID)
	}
	res := out.(*FlowRunDetail)
	if len(res.Nodes) != 1 || res.Nodes[0].NodeID != "3" {
		t.Fatalf("nodes = %+v, want only the failed node 3", res.Nodes)
	}
	if res.NodeTotal != 3 {
		t.Errorf("NodeTotal = %d, want 3 (before filtering)", res.NodeTotal)
	}
	if res.Nodes[0].ErrStack == "" {
		t.Error("a failed node must carry its error stack")
	}
}

// Only an explicit only_failed=false returns the whole chain; the nodes filter stacks with it.
func TestReadFlowRunFilterOptions(t *testing.T) {
	fk := &fakeFlows{runDetail: sampleRunDetail()}
	out, _ := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk},
		map[string]any{"run_id": "run-1", "only_failed": false})
	if res := out.(*FlowRunDetail); len(res.Nodes) != 3 {
		t.Fatalf("only_failed=false should return all 3, got %d", len(res.Nodes))
	}

	fk = &fakeFlows{runDetail: sampleRunDetail()}
	out, _ = (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: fk},
		map[string]any{"run_id": "run-1", "only_failed": false, "nodes": []any{"2", "4"}})
	res := out.(*FlowRunDetail)
	if len(res.Nodes) != 2 || res.Nodes[0].NodeID != "2" || res.Nodes[1].NodeID != "4" {
		t.Fatalf("the nodes filter did not take effect: %+v", res.Nodes)
	}
}

func TestReadFlowRunNotFound(t *testing.T) {
	_, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{"run_id": "run-x"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}
}

func TestReadFlowRunRequiresRunID(t *testing.T) {
	_, err := (&readFlowRunTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("err = %v, want run_id", err)
	}
}

// An oversized variable must degrade to a summary rather than being cut: half of it is not valid JSON, and the whole tool result
// would fail to serialise.
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
		t.Error("Truncated should be set once over the limit")
	}
	summary, ok := res.Nodes[0].InputVars.(map[string]any)
	if !ok || summary["_truncated"] != true {
		t.Fatalf("an oversized variable should degrade to a summary: %#v", res.Nodes[0].InputVars)
	}
	keys, _ := summary["keys"].([]string)
	if len(keys) != 2 || keys[0] != "body" || keys[1] != "q" {
		t.Errorf("the summary should keep the sorted top-level keys: %v", keys)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Errorf("the result must still be valid JSON: %v", err)
	}
}

// ===== Branch structure: an edge with a handle is pulled from each child =====
//
// The executor finds its way by the handle prefix (executors.findBranchEdge matches "source"+childID), so what is asserted here
// cannot merely be "an edge exists" but how the handle is built and which node it targets.

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
		t.Fatalf("invalid diagram: %v", err)
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
	t.Fatalf("no edge %s->%s, actual edges %+v", source, target, d.Edges)
	return ""
}

// The edges create_flow stores must match create_edge's suggestion (both share suggestHandlePositions): anyone who pins a container
// back to "top", or changes only one side, fails here.
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
		t.Fatalf("failed to parse the diagram: %v", err)
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
			t.Errorf("edge %s->%s target_handle = %q, want %q (%s)",
				e.Source, e.Target, e.TargetHandle, wantTarget, reason)
		}
		// The first edge of a branch sub-chain is owned by children[].id (a semantic key) and is not bound by the positional
		// suggestion.
		if !strings.HasPrefix(e.SourceHandle, "source"+e.Source) {
			continue
		}
		wantSource := "source" + e.Source + srcPos
		if e.SourceHandle != wantSource {
			t.Errorf("edge %s->%s source_handle = %q, want %q (%s)",
				e.Source, e.Target, e.SourceHandle, wantSource, reason)
		}
	}
}

// The real shape seen in production: code -> branch(c1/c2) -> two results, with no default branch on the branch node.
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
					// source_ref deliberately holds a label: the model is bad at computing ids by hand, and the tool should rewrite
					// it to the real id
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
			t.Fatalf("node %d = %s(id=%s), want %s(id=%d)", i, d.Nodes[i].Type, d.Nodes[i].ID, typ, i+1)
		}
	}
	// Main-chain edges must carry handles too: at run time only OutEdges[0] is walked, but the edge tools (insert_node /
	// update_edge) go by the handle, and an empty handle means "runs, but cannot be changed".
	if h := edgeHandle(t, d, "1", "2"); h != "source1right" {
		t.Errorf("main-chain source handle = %q, want source1right", h)
	}
	// The first edge of a branch sub-chain is owned by children[].id (a semantic key), and must not be rewritten by position when
	// handles are backfilled after layout
	if h := edgeHandle(t, d, "3", "4"); h != "sourcec1right" {
		t.Errorf("branch sub-chain source handle = %q, want sourcec1right", h)
	}
	if h := edgeHandle(t, d, "3", "5"); h != "sourcec2right" {
		t.Errorf("branch sub-chain source handle = %q, want sourcec2right", h)
	}
	for _, e := range d.Edges {
		if e.SourceHandle == nil || *e.SourceHandle == "" {
			t.Errorf("edge %s->%s has an empty source_handle: tool-generated edges carry handles too", e.Source, e.Target)
		}
		if e.TargetHandle == nil || *e.TargetHandle == "" {
			t.Errorf("edge %s->%s has an empty target_handle", e.Source, e.Target)
		}
	}
	if h := edgeHandle(t, d, "3", "4"); h != "sourcec1right" {
		t.Errorf("c1 branch edge handle = %q, want sourcec1right", h)
	}
	if h := edgeHandle(t, d, "3", "5"); h != "sourcec2right" {
		t.Errorf("c2 branch edge handle = %q, want sourcec2right", h)
	}

	// A source_ref written as a label should be rewritten to the real id (2 = the code node carrying that fixture label)
	ref := objectOf(objectOf(d.Nodes[2].Data.Config["inputs"])["result"])["source_ref"]
	if ref != "2" {
		t.Errorf("source_ref = %v, want \"2\"", ref)
	}

	m := out.(map[string]any)
	if nodes, _ := m["nodes"].([]map[string]any); len(nodes) != 5 || nodes[2]["type"] != "branch" {
		t.Errorf("the result should carry the node list: %v", m["nodes"])
	}
	if warns, _ := m["warnings"].([]string); len(warns) != 0 {
		t.Errorf("a structurally complete flow should not warn: %v", warns)
	}
}

// The default branch's handle uses the node's own id (that is exactly how the executor's findBranchEdge(node, node.ID) looks for it).
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
		t.Fatalf("nodes = %d, want 4 (start + branch + two results)", len(d.Nodes))
	}
	if h := edgeHandle(t, d, "2", "4"); h != "source2right" {
		t.Errorf("default edge handle = %q, want source2right", h)
	}
}

// A branch nested inside another branch's sub-chain: ids stay consecutive and each handle belongs to its own case.
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
		t.Fatalf("nodes = %d, want 4 (start + outer + inner + result)", len(d.Nodes))
	}
	if h := edgeHandle(t, d, "2", "3"); h != "sourcearight" {
		t.Errorf("outer handle = %q", h)
	}
	if h := edgeHandle(t, d, "3", "4"); h != "sourcebright" {
		t.Errorf("inner handle = %q", h)
	}
}

// Declaring branches makes the step the last in its chain: after a branch finishes only the default edge is taken, so continuing the
// main chain behind it has no meaning.
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
		t.Fatalf("err = %v, want a hint that branches must be on the last step", err)
	}
	if fk.created != nil {
		t.Error("nothing should be stored when validation fails")
	}
}

// A wrong key is the most common slip: the available ids must be listed, rather than silently building a path that can never be
// taken.
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
		t.Fatalf("err = %v, want c9 echoed back together with the available ids", err)
	}
}

// A missing sub-branch declaration: not blocked (building the structure first and the chains later is allowed), but warn that a hit
// would be skipped.
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
		t.Fatalf("warnings = %v, want one saying c2 has no outgoing edge", warns)
	}
}

// branches only mean something on branch / intent; passing them on another type must be refused (otherwise it silently generates
// edges nobody can make sense of).
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
		t.Fatalf("err = %v, want only branch / intent to have sub-branches", err)
	}
}

// Entry arguments used to be fillable only in the frontend: a tool-built flow was exposed with no input_schema, leaving the client
// with an endpoint that documents no parameters. This guards "create_flow can write arguments".
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
		t.Fatal("the entry arguments were not written into FlowInput.Arguments")
	}
	var got struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(fk.created.Arguments, &got); err != nil {
		t.Fatalf("arguments is not valid JSON: %v", err)
	}
	if got.Type != "object" || len(got.Properties) != 4 || len(got.Required) != 1 || got.Required[0] != "city" {
		t.Fatalf("the normalised arguments differ: %+v", got)
	}
	if names, _ := out.(map[string]any)["arguments"].([]string); strings.Join(names, ",") != "city,days,filter,tags" {
		t.Errorf("returned parameter names = %v, want them sorted by name", names)
	}
}

// Entry arguments must be changeable in place: rebuilding via delete_flow + create_flow changes flows.id, and service-credential
// allowlists are granted by target_id, so the old rows would silently stop working.
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
		t.Fatalf("the stored arguments are invalid: %v", err)
	}
	if len(got.Properties) != 2 {
		t.Fatalf("properties = %v, want city + days (debug should be removed)", got.Properties)
	}
	if _, ok := got.Properties["debug"]; ok {
		t.Error("remove did not take effect")
	}
	if len(got.Required) != 1 || got.Required[0] != "city" {
		t.Errorf("required = %v, want [city] (existing required entries must not be cleared when required is not passed)", got.Required)
	}
	if !strings.Contains(string(fk.updatedArgs), `"description":"城市"`) {
		t.Error("a parameter that was not named (city's description) was stripped: merging should only rewrite the ones in set")
	}
	if names, _ := out.(map[string]any)["arguments"].([]string); strings.Join(names, ",") != "city,days" {
		t.Errorf("returned parameter names = %v, want city,days", names)
	}
}

// When required is passed it replaces the whole list: promote days to required, and allow demoting city.
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
		t.Fatalf("the stored arguments are invalid: %v", err)
	}
	if len(got.Required) != 1 || got.Required[0] != "days" {
		t.Errorf("required = %v, want [days]", got.Required)
	}
}

// Validation must happen before storing: one bad input must not corrupt the existing declaration.
func TestUpdateFlowArgumentsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			"bad type",
			map[string]any{"flow": "demo", "set": map[string]any{"t": map[string]any{"type": "date"}}},
			`type="date"`,
		},
		{
			"array without items",
			map[string]any{"flow": "demo", "set": map[string]any{"ids": map[string]any{"type": "array"}}},
			"needs items",
		},
		{
			"required points at a missing parameter",
			map[string]any{"flow": "demo", "set": map[string]any{"b": map[string]any{"type": "string"}}, "required": []any{"zzz"}},
			"properties has no such parameter",
		},
		{
			"the same name in set and remove",
			map[string]any{"flow": "demo", "set": map[string]any{"a": map[string]any{"type": "string"}}, "remove": []any{"a"}},
			"is in both set and remove",
		},
		{
			"nothing given",
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
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if len(fk.updatedArgs) != 0 {
				t.Error("nothing should be stored when validation fails")
			}
		})
	}
}

// A flow that does not exist must error explicitly, rather than quietly creating an empty declaration.
func TestUpdateFlowArgumentsMissingFlow(t *testing.T) {
	_, err := (&updateFlowArgumentsTool{}).Execute(context.Background(), &Context{Flows: &fakeFlows{}}, map[string]any{
		"flow": "nope",
		"set":  map[string]any{"a": map[string]any{"type": "string"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want does not exist", err)
	}
}
