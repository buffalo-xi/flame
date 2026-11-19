package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ===== 节点工具（create_node / insert_node / update_node / delete_node）=====
//
// 边工具解决「谁连谁」，节点工具解决「有什么」。两者合起来才够建复杂流程：
// create_flow 的快路径负责主干，缺的节点用 create_node 补、用 insert_node 插进链里、
// 用 update_node 改 config、用 delete_node 删。
//
// 三条前端约束必须复刻，否则建出来的图要么一打开就被画布改坏，要么运行期静默空转：
//  1. loop / process / function 的子链从 entry 开始 —— 没有 entry，执行器记一行日志就直接成功返回；
//  2. function 只能建在顶层，return 只能建在 function 内部（含嵌套容器）；
//  3. 节点 id 与分支子分支 id、边 id 共用一个命名空间（画布 generateUniqueId 就是这么避让的）。

// creatableNodeTypes 工具能直接建的节点类型。
// 排除 start（全流程唯一，由 create_flow 生成）与 entry（容器自动带出，手建只会多出孤岛）。
// knowledge 也排除：后端没有它的执行器（executors.RegisterExecutor 里没有 knowledge），
// 前端 palette 早已撤掉入口、只保留渲染存量流程图；工具再允许建，等于批量生产
// 「连进链路就报 no executor registered」的废节点。存量图里的 knowledge 仍可
// read / update / delete / 连边（nodeHandleRules 保留它）。
var creatableNodeTypes = map[string]bool{
	"llm": true, "code": true, "http": true, "mcp": true, "rpc": true,
	"branch": true, "intent": true, "loop": true, "process": true, "function": true,
	"call": true, "assign": true, "memory": true, "wait": true, "fork": true,
	"result": true, "progress": true, "break": true, "continue": true, "return": true,
	"exit": true, "layout": true, "comment": true,
}

// containerTypes 能装子节点的类型（对应前端 nodeFactory 里会带 entry 的那三个）。
var containerTypes = map[string]bool{"loop": true, "process": true, "function": true}

// nodeJSON 新建节点的落库形态。
type nodeJSON struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Position   map[string]int `json:"position"`
	ParentNode string         `json:"parent_node,omitempty"`
	Extent     map[string]any `json:"extent,omitempty"`
	Data       map[string]any `json:"data"`
}

// childExtent 子节点限制在父节点范围内：数值照抄前端 nodeFactory（距四边 37/5/5/5）。
func childExtent() map[string]any {
	return map[string]any{"range": "parent", "padding": []int{37, 5, 5, 5}}
}

func buildNodeJSON(id, typ, label string, cfg map[string]any, parentID string, pos map[string]int) (json.RawMessage, error) {
	data := map[string]any{"label": label}
	if len(cfg) > 0 {
		data["config"] = cfg
	}
	n := nodeJSON{ID: id, Type: typ, Position: pos, Data: data}
	if parentID != "" {
		n.ParentNode = parentID
		n.Extent = childExtent()
	}
	return json.Marshal(n)
}

func defaultNodeLabel(typ string) string {
	switch typ {
	case "http":
		return "HTTP request"
	case "mcp":
		return "MCP"
	case "rpc":
		return "RPC"
	case "llm":
		return "LLM"
	case "code":
		return "Code"
	case "branch":
		return "Branch"
	case "intent":
		return "Intent"
	case "loop":
		return "Loop"
	case "process":
		return "Embedded flow"
	case "function":
		return "Function"
	case "call":
		return "Call"
	case "assign":
		return "Assign"
	case "memory":
		return "Memory"
	case "wait":
		return "Wait"
	case "fork":
		return "Fork"
	case "result":
		return "Result"
	case "progress":
		return "Progress"
	case "break":
		return "Break"
	case "continue":
		return "Continue"
	case "return":
		return "Return"
	case "exit":
		return "Terminate"
	case "layout":
		return "Layout"
	case "knowledge":
		return "Knowledge"
	case "comment":
		return "Comment"
	}
	return typ
}

// defaultNodeConfig 与前端「新建节点」写进去的模板对齐：空节点在画布上等于一堆必填项缺失。
func defaultNodeConfig(typ string, newID func() string) map[string]any {
	switch typ {
	case "http":
		return map[string]any{"outputs": map[string]any{
			"mode":            map[string]any{"type": "string", "description": "Response mode"},
			"status":          map[string]any{"type": "number", "description": "HTTP status code"},
			"headers":         map[string]any{"type": "string", "description": "Response headers"},
			"body":            map[string]any{"type": "string", "description": "Response body"},
			"sse_events":      map[string]any{"type": "array[string]", "description": "SSE event list"},
			"stream_finished": map[string]any{"type": "boolean", "description": "Whether streaming finished"},
		}}
	case "mcp", "rpc":
		return map[string]any{"outputs": map[string]any{
			"result":   map[string]any{"type": "string", "description": "Tool result"},
			"progress": map[string]any{"type": "array[string]", "description": "Progress notifications"},
			"error":    map[string]any{"type": "string", "description": "Call error message"},
		}}
	case "memory":
		return map[string]any{
			"outputs": map[string]any{"messages": map[string]any{"type": "array[object]"}},
			"limit":   10,
			"window":  map[string]any{"enabled": false, "unit": "hour", "value": 24},
		}
	case "assign":
		return map[string]any{"inputs": map[string]any{}, "outputs": map[string]any{}, "targets": map[string]any{}, "assignments": []any{}}
	case "loop":
		return map[string]any{
			"loop":      map[string]any{"type": "count", "total_times": 10},
			"loop_vars": map[string]any{"index": map[string]any{"type": "number", "description": "Loop index"}},
		}
	case "branch":
		return map[string]any{"children": []any{
			map[string]any{"id": newID(), "label": "Branch 1", "condition": "", "exclusive": true},
		}}
	case "intent":
		return map[string]any{
			"inputs": map[string]any{
				"messages": map[string]any{"type": "array[object]", "source_type": "argument", "source_ref": "", "source_key": ""},
				"input":    map[string]any{"type": "string", "source_type": "argument", "source_ref": "", "source_key": ""},
			},
			"llm": map[string]any{
				"gateway_model": "",
				"system_prompt": "You classify the user's intent from the conversation history and the user input.\n\nConversation history:\n{{json .input.messages}}\n\nUser input:\n{{.input.input}}",
				"params":        map[string]any{},
			},
			"children": []any{map[string]any{"id": newID(), "label": "Intent 1", "description": ""}},
			"outputs":  map[string]any{"intent": map[string]any{"type": "string"}, "index": map[string]any{"type": "number"}},
		}
	}
	return nil
}

// ===== diagram 里节点相关的查询 / 改写 =====

// parentOf 取节点所属容器（"" = 顶层）。
func (d *flowDoc) parentOf(id string) string {
	if n, ok := d.node(id); ok {
		return n.ParentNode
	}
	return ""
}

// subtree 返回 id 及其全部后代（容器嵌套时是整棵子树）。
func (d *flowDoc) subtree(id string) []string {
	out := []string{id}
	for grew := true; grew; {
		grew = false
		for _, n := range d.nodes {
			if n.ParentNode == "" || containsStr(out, n.ID) {
				continue
			}
			if containsStr(out, n.ParentNode) {
				out = append(out, n.ID)
				grew = true
			}
		}
	}
	return out
}

// ancestorIs 从 id 往上找，是否存在类型为 typ 的祖先容器。
func (d *flowDoc) ancestorIs(id, typ string) bool {
	cur := id
	for i := 0; i < 32 && cur != ""; i++ {
		n, ok := d.node(cur)
		if !ok || n.ParentNode == "" {
			return false
		}
		p, ok := d.node(n.ParentNode)
		if !ok {
			return false
		}
		if p.Type == typ {
			return true
		}
		cur = p.ID
	}
	return false
}

// positionFor 给新节点算一个不与已有节点重叠的落点，口径与 create_flow 的排版一致：
// 顶层接在当前最右节点的右边（同 y），超过一行宽度就折到下一行；容器内子节点纵向排。
func (d *flowDoc) positionFor(parentID string) map[string]int {
	if parentID != "" {
		maxBottom := childBaseY
		for _, n := range d.nodes {
			if n.ParentNode != parentID {
				continue
			}
			h := 32
			if containerTypes[n.Type] {
				h = 100
			}
			if bottom := n.Position.Y + h; bottom > maxBottom {
				maxBottom = bottom
			}
		}
		return map[string]int{"x": childBaseX, "y": maxBottom + childGap}
	}

	rightX, rowY := 0, 0
	for _, n := range d.nodes {
		if n.ParentNode != "" {
			continue
		}
		if n.Position.X >= rightX {
			rightX = n.Position.X
			rowY = n.Position.Y
		}
	}
	// 步长与技能文档里的「相邻叶子 x 步长 200」对齐，追加节点不会比手写坐标排得更散
	if x := rightX + 200; x <= layoutRowMaxWidth {
		return map[string]int{"x": x, "y": rowY}
	}
	// 一行排不下：折到下一行，落到现有节点的最低点下方
	maxBottom := 0
	for _, n := range d.nodes {
		if n.ParentNode != "" {
			continue
		}
		if bottom := n.Position.Y + 32; bottom > maxBottom {
			maxBottom = bottom
		}
	}
	return map[string]int{"x": 0, "y": maxBottom + layoutRowGap}
}

// nodeConfigOf 把节点的 data.config 解成通用对象（引用扫描 / 配置变更用）。
func nodeConfigOf(raw json.RawMessage) map[string]any {
	var n struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &n)
	if n.Data.Config == nil {
		return map[string]any{}
	}
	return n.Data.Config
}

// patchNodeJSON 只改节点里被点名的键，其余（坐标、尺寸、extent、前端私有字段）原样保留。
func patchNodeJSON(raw json.RawMessage, apply func(node, data map[string]json.RawMessage) error) (json.RawMessage, error) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node JSON: %w", err)
	}
	data := map[string]json.RawMessage{}
	if d := node["data"]; len(d) > 0 {
		if err := json.Unmarshal(d, &data); err != nil {
			return nil, fmt.Errorf("failed to parse node data: %w", err)
		}
	}
	if err := apply(node, data); err != nil {
		return nil, err
	}
	db, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	node["data"] = db
	return json.Marshal(node)
}

// referencesTo 找出子树之外的节点对这批 id 的引用（删节点前必须拦：引用不会跟着消失）。
func (d *flowDoc) referencesTo(targets map[string]bool) []string {
	var refs []string
	for i := range d.nodes {
		n := &d.nodes[i]
		if targets[n.ID] {
			continue
		}
		cfg := nodeConfigOf(d.nodeRaw[i])
		if inputs, ok := cfg["inputs"].(map[string]any); ok {
			for key, v := range inputs {
				in, ok := v.(map[string]any)
				if !ok {
					continue
				}
				if ref, _ := in["source_ref"].(string); ref != "" && targets[ref] {
					refs = append(refs, fmt.Sprintf("node %s (%s) config.inputs.%s.source_ref", n.ID, n.Type, key))
				}
			}
		}
		if ref, _ := cfg["target"].(string); ref != "" && targets[ref] {
			refs = append(refs, fmt.Sprintf("node %s (%s) config.target (call target)", n.ID, n.Type))
		}
		if ref, _ := cfg["fork_node_id"].(string); ref != "" && targets[ref] {
			refs = append(refs, fmt.Sprintf("node %s (%s) config.fork_node_id (concurrency group)", n.ID, n.Type))
		}
	}
	return refs
}

// nodeView 回显用的节点视图（带坐标：调用方据此判断是否需要再调 position）。
func nodeView(n *flowNodeMeta) map[string]any {
	if n == nil {
		return map[string]any{}
	}
	v := map[string]any{
		"id":       n.ID,
		"type":     n.Type,
		"position": map[string]int{"x": n.Position.X, "y": n.Position.Y},
	}
	if n.ParentNode != "" {
		v["parent_node"] = n.ParentNode
	}
	return v
}

// argNodePosition 解析 update_node 的 position 参数（{x, y}，容器内节点是相对坐标）。
func argNodePosition(args map[string]any) (map[string]int, bool, error) {
	raw, exists := args["position"]
	if !exists || raw == nil {
		return nil, false, nil
	}
	pos, err := parseStepPosition(raw, "position")
	if err != nil {
		return nil, false, err
	}
	return pos, true, nil
}

// handlesView 该类型可用的 handle，直接喂给 create_edge，省掉模型自己猜位置。
func handlesView(typ string) map[string]any {
	rule := nodeHandleRules[typ]
	out := map[string]any{
		"in":  append([]string{}, rule.in...),
		"out": append([]string{}, rule.out...),
	}
	if typ == "branch" || typ == "intent" {
		out["children_out"] = append([]string{}, branchChildOut...)
		out["children_handle"] = "source<config.children[].id>left|right"
	}
	return out
}

func creatableTypeList() string {
	names := make([]string, 0, len(creatableNodeTypes))
	for _, t := range []string{
		"llm", "code", "http", "mcp", "rpc", "branch", "intent", "loop", "process", "function",
		"call", "assign", "memory", "wait", "fork", "result", "progress", "break", "continue",
		"return", "exit", "layout", "comment",
	} {
		if creatableNodeTypes[t] {
			names = append(names, t)
		}
	}
	return strings.Join(names, " / ")
}

// buildNodeForTool 节点工具共用的建节点前置校验 + 建节点（含容器自动带 entry）。
// 返回新节点 id、新容器的 entry id、给父容器补的 entry id、告警。
func buildNodeForTool(doc *flowDoc, typ, label, parentID string, userCfg map[string]any, concurrency string) (id, entryID, parentEntryID string, warnings []string, err error) {
	if typ == "" {
		return "", "", "", nil, errors.New("type must not be empty")
	}
	if !creatableNodeTypes[typ] {
		hint := "start is generated by create_flow, entry comes with the container"
		if typ == "knowledge" {
			hint = "knowledge has no executor on the backend (wiring it in only fails with no executor registered) and is kept for rendering existing diagrams; " +
				"for knowledge retrieval call the matching service from an mcp / rpc node, or let the llm node carry the context"
		}
		return "", "", "", nil, fmt.Errorf("cannot create a node of type %q. Creatable types: %s (%s)", typ, creatableTypeList(), hint)
	}
	if parentID != "" {
		p, ok := doc.node(parentID)
		if !ok {
			return "", "", "", nil, fmt.Errorf("parent_node %q does not exist: take the id from diagram.nodes[].id in read_flow", parentID)
		}
		if !containerTypes[p.Type] {
			return "", "", "", nil, fmt.Errorf("parent_node %q is %s and cannot hold children: only loop / process / function are containers", parentID, p.Type)
		}
	}
	if typ == "function" && parentID != "" {
		return "", "", "", nil, errors.New("a function node can only be created at the top level: call nodes invoke it through config.target and it takes no edges")
	}
	if typ == "return" && !doc.ancestorIs(parentID, "function") {
		return "", "", "", nil, errors.New("a return node can only be created inside a function container (a direct child or deeper)")
	}

	cfg := defaultNodeConfig(typ, doc.newNodeID)
	if cfg == nil {
		cfg = map[string]any{}
	}
	for k, v := range userCfg {
		cfg[k] = v
	}
	// 并发默认关闭：与 create_flow 同口径，按调用方声明的策略处理 concurrency，
	// 没声明就摘掉并回显；容器一律不带这一项。
	if w := applyConcurrencyPolicy(cfg, typ, concurrency, "new node"); w != "" {
		warnings = append(warnings, w)
	}
	// branch / intent 的 children 允许调用方自己给；给了就要保证每项都有 id。
	normalizeBranchChildren(doc, typ, cfg)

	label = strings.TrimSpace(label)
	if label != "" {
		if owner := doc.labelOwner(label); owner != "" {
			return "", "", "", nil, fmt.Errorf(
				"label %q is already used by node %s: node labels must be unique across the flow"+
					" (source_ref / call.target / fork_node_id can all be written as labels, and a duplicate points at the wrong node); pick another one, or leave it empty so the tool adds a suffix",
				label, owner)
		}
	} else {
		label = doc.uniqueLabel(defaultNodeLabel(typ))
	}

	ws, err := validateStepConfig(typ, cfg)
	if err != nil {
		return "", "", "", nil, err
	}
	warnings = append(warnings, ws...)

	id = doc.newNodeID()
	raw, err := buildNodeJSON(id, typ, label, cfg, parentID, doc.positionFor(parentID))
	if err != nil {
		return "", "", "", nil, err
	}
	if err := doc.addNode(raw); err != nil {
		return "", "", "", nil, err
	}

	// 父容器没有 entry 时顺手补一个：否则这个新子节点永远跑不到（执行器直接成功返回）。
	if parentID != "" && !doc.hasEntryOf(parentID) {
		pid := doc.newNodeID()
		praw, err := buildNodeJSON(pid, "entry", doc.uniqueLabel("Entry"), nil, parentID, map[string]int{"x": childBaseX, "y": childBaseY})
		if err != nil {
			return "", "", "", nil, err
		}
		if err := doc.addNode(praw); err != nil {
			return "", "", "", nil, err
		}
		parentEntryID = pid
		warnings = append(warnings, fmt.Sprintf("container %q had no entry, so one was added (id=%s): a container without an entry succeeds but runs nothing", parentID, pid))
	}

	if containerTypes[typ] {
		entryID = doc.newNodeID()
		eraw, err := buildNodeJSON(entryID, "entry", doc.uniqueLabel("Entry"), nil, id, map[string]int{"x": childBaseX, "y": childBaseY})
		if err != nil {
			return "", "", "", nil, err
		}
		if err := doc.addNode(eraw); err != nil {
			return "", "", "", nil, err
		}
	}
	return id, entryID, parentEntryID, warnings, nil
}

// hasEntryOf 容器是否已经有 entry 子节点。
func (d *flowDoc) hasEntryOf(parentID string) bool {
	for i := range d.nodes {
		if d.nodes[i].ParentNode == parentID && d.nodes[i].Type == "entry" {
			return true
		}
	}
	return false
}

// labelOwner 返回正在使用该 label 的节点 id（"" = 没被占用）。
//
// label 是 source_ref / call.target / fork_node_id 的可写形式，重名会让这些引用指错节点，
// 所以全流程图内 label 必须唯一。
func (d *flowDoc) labelOwner(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	for i := range d.nodes {
		if strings.TrimSpace(d.nodes[i].Data.Label) == label {
			return d.nodes[i].ID
		}
	}
	return ""
}

// uniqueLabel 给**自动生成**的 label 加 -2 / -3 … 直到全图唯一。
// 用户显式写的 label 撞名走报错，不静默改名。
func (d *flowDoc) uniqueLabel(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Node"
	}
	if d.labelOwner(base) == "" {
		return base
	}
	for i := 2; i <= 999; i++ {
		if candidate := fmt.Sprintf("%s-%d", base, i); d.labelOwner(candidate) == "" {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, len(d.nodes)+1)
}

// normalizeBranchChildren branch / intent 的 children 必须带 id（执行器靠它找出边）。
func normalizeBranchChildren(doc *flowDoc, typ string, cfg map[string]any) {
	if typ != "branch" && typ != "intent" {
		return
	}
	kids, _ := cfg["children"].([]any)
	if len(kids) == 0 {
		cfg["children"] = []any{map[string]any{"id": doc.newNodeID(), "label": "Branch 1", "condition": "", "exclusive": true}}
		return
	}
	for _, k := range kids {
		m, ok := k.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["id"].(string); strings.TrimSpace(id) == "" {
			m["id"] = doc.newNodeID()
		}
	}
	cfg["children"] = kids
}

// ===== create_node =====

type createNodeTool struct{}

func (t *createNodeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_node",
		Category: CategoryFlowAdmin,
		Description: "Add **one node** to an existing flow (no wiring — use create_edge / insert_node for that). " +
			"loop / process / function automatically carry an entry child node — a container sub-chain must start from entry, otherwise it silently spins empty at run time. " +
			"branch / intent automatically carry one config.children entry (the executor finds outgoing edges via children[].id). " +
			"Pass a container id as parent_node to create a child node; only loop / process / function can hold children, " +
			"function can only be created at the top level, and return can only be created inside a function. " +
			"**Concurrency is off by default**: config.concurrency is stripped and reported in a warning; only when the user explicitly asks for concurrency, " +
			"declare it with the concurrency parameter (mono / group). " +
			"The result returns node / entry / handles (the in/out handle positions available for that type) / warnings.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":   map[string]any{"type": "string", "description": flowRefHint},
				"type":   map[string]any{"type": "string", "description": "Node type: " + creatableTypeList()},
				"label":  map[string]any{"type": "string", "description": "Node title; defaults to the type's default name"},
				"config": map[string]any{"type": "object", "description": "Node config (same shape as the front-end form for that type); defaults to that type's template"},
				"parent_node": map[string]any{
					"type":        "string",
					"description": "Optional: container node id (loop / process / function). Omit to create at the top level",
				},
				"concurrency": concurrencyParamSchema,
			},
			"required": []any{"flow", "type"},
		},
	}
}

func (t *createNodeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	doc, detail, err := loadFlowDoc(ctx, c, argString(args, "flow"))
	if err != nil {
		return nil, err
	}
	userCfg, err := argObject(args, "config")
	if err != nil {
		return nil, err
	}
	typ := strings.ToLower(strings.TrimSpace(argString(args, "type")))
	parentID := strings.TrimSpace(argString(args, "parent_node"))
	concurrency, err := parseConcurrencyPolicy(args)
	if err != nil {
		return nil, err
	}
	id, entryID, parentEntryID, warnings, err := buildNodeForTool(
		doc, typ, strings.TrimSpace(argString(args, "label")), parentID, userCfg, concurrency,
	)
	if err != nil {
		return nil, err
	}
	info, err := saveDiagram(ctx, c, detail, doc)
	if err != nil {
		return nil, err
	}

	n, _ := doc.node(id)
	out := map[string]any{
		"flow_id":    info.ID,
		"flow_name":  info.Name,
		"node":       nodeView(n),
		"handles":    handlesView(typ),
		"node_count": len(doc.nodes),
		"edge_count": len(doc.edges),
		"next_step":  "wire it with create_edge: " + edgeHint(id, typ),
	}
	if entryID != "" {
		out["entry"] = map[string]any{"id": entryID, "parent_node": id}
		out["container_note"] = "a container sub-chain starts from entry (id=" + entryID + "): connect the first child with create_edge(source=" + entryID + ")"
	}
	if parentEntryID != "" {
		out["parent_entry"] = map[string]any{"id": parentEntryID, "parent_node": parentID}
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

// edgeHint 按类型给一句「下一步怎么连」的提示。
func edgeHint(id, typ string) string {
	if typ == "branch" || typ == "intent" {
		return "sub-branches use source<children[].id>right, the default branch uses source" + id + "right"
	}
	rule := nodeHandleRules[typ]
	if len(rule.in) == 0 || len(rule.out) == 0 {
		return "this type has no incoming or no outgoing handle and cannot be wired (function is invoked by call)"
	}
	return "source_handle=source" + id + rule.out[0] + ", target_handle=target<target id>" + rule.in[0]
}

// ===== insert_node =====

type insertNodeTool struct{}

func (t *insertNodeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "insert_node",
		Category: CategoryFlowAdmin,
		Description: "Insert a node in the middle of an **existing edge**: delete the old edge and wire A→new→B automatically (atomic — it never leaves a half-wired graph). " +
			"The new node lands under the same parent as that edge (it cannot cross containers). " +
			"Only types that take both an incoming and an outgoing edge can be inserted (function / exit / break and the like cannot). " +
			"**Concurrency is off by default**: config.concurrency is stripped and reported in a warning; only when the user explicitly asks for concurrency, declare it with the concurrency parameter. " +
			"The result returns node, the two edges, handles and warnings.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":        map[string]any{"type": "string", "description": flowRefHint},
				"edge_id":     map[string]any{"type": "string", "description": "Id of the edge being split (diagram.edges[].id from read_flow)"},
				"type":        map[string]any{"type": "string", "description": "Type of the new node: " + creatableTypeList()},
				"label":       map[string]any{"type": "string", "description": "Node title; defaults to the type's default name"},
				"config":      map[string]any{"type": "object", "description": "Node config; defaults to that type's template"},
				"concurrency": concurrencyParamSchema,
			},
			"required": []any{"flow", "edge_id", "type"},
		},
	}
}

func (t *insertNodeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	doc, detail, err := loadFlowDoc(ctx, c, argString(args, "flow"))
	if err != nil {
		return nil, err
	}
	edgeID := strings.TrimSpace(argString(args, "edge_id"))
	if edgeID == "" {
		return nil, errors.New("edge_id must not be empty (diagram.edges[].id from read_flow)")
	}
	idx := doc.edgeIndex(edgeID)
	if idx < 0 {
		return nil, fmt.Errorf("no edge with edge_id=%q: check diagram.edges with read_flow first", edgeID)
	}
	old := doc.edges[idx]
	if old.SourceHandle == "" || old.TargetHandle == "" {
		return nil, fmt.Errorf("edge %s has no source_handle / target_handle and cannot be split: fill them in with update_edge first", edgeID)
	}
	src, ok := doc.node(old.Source)
	if !ok {
		return nil, fmt.Errorf("edge %s has a source node %q that does not exist", edgeID, old.Source)
	}

	typ := strings.ToLower(strings.TrimSpace(argString(args, "type")))
	userCfg, err := argObject(args, "config")
	if err != nil {
		return nil, err
	}
	if creatableNodeTypes[typ] {
		if err := checkInsertableType(typ); err != nil {
			return nil, err
		}
	}
	concurrency, err := parseConcurrencyPolicy(args)
	if err != nil {
		return nil, err
	}
	nodeID, entryID, parentEntryID, warnings, err := buildNodeForTool(
		doc, typ, strings.TrimSpace(argString(args, "label")), src.ParentNode, userCfg, concurrency,
	)
	if err != nil {
		return nil, err
	}

	inPos := pickHandle(nodeHandleRules[typ].in, []string{"left", "top", "right", "bottom"})
	outPos := pickHandle(nodeHandleRules[typ].out, []string{"right", "bottom", "left"})

	head := flowEdge{
		Source:       old.Source,
		Target:       nodeID,
		SourceHandle: old.SourceHandle,
		TargetHandle: "target" + nodeID + inPos,
	}
	tail := flowEdge{
		Source:       nodeID,
		Target:       old.Target,
		SourceHandle: "source" + nodeID + outPos,
		TargetHandle: old.TargetHandle,
	}
	// 先删旧边再校验：否则会撞「同一对节点最多一条边」/「一个出口一条边」。
	doc.removeEdge(edgeID)
	if _, err := doc.validateEdge(head, ""); err != nil {
		return nil, fmt.Errorf("the leading edge after the insert is invalid: %w", err)
	}
	if _, err := doc.validateEdge(tail, ""); err != nil {
		return nil, fmt.Errorf("the trailing edge after the insert is invalid: %w", err)
	}
	head.ID = doc.newEdgeID()
	tail.ID = doc.newEdgeID()
	doc.edges = append(doc.edges, head, tail)

	info, err := saveDiagram(ctx, c, detail, doc)
	if err != nil {
		return nil, err
	}
	n, _ := doc.node(nodeID)
	out := map[string]any{
		"flow_id":    info.ID,
		"flow_name":  info.Name,
		"node":       nodeView(n),
		"edges":      []any{edgeView(head), edgeView(tail)},
		"handles":    handlesView(typ),
		"edge_count": len(doc.edges),
	}
	if entryID != "" {
		out["entry"] = map[string]any{"id": entryID, "parent_node": nodeID}
	}
	if parentEntryID != "" {
		out["parent_entry"] = map[string]any{"id": parentEntryID}
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

// checkInsertableType 插进链中间的节点必须既有入边又有出边。
func checkInsertableType(typ string) error {
	rule := nodeHandleRules[typ]
	if len(rule.in) == 0 {
		return fmt.Errorf("a %s node has no incoming handle and cannot be inserted mid-chain (create top-level nodes with create_node)", typ)
	}
	if len(rule.out) == 0 {
		return fmt.Errorf("a %s node has no outgoing handle and cannot be inserted mid-chain (wire terminal nodes to the chain tail with create_edge)", typ)
	}
	return nil
}

func pickHandle(allowed, prefer []string) string {
	for _, p := range prefer {
		if containsStr(allowed, p) {
			return p
		}
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return ""
}

// ===== update_node =====

type updateNodeTool struct{}

func (t *updateNodeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_node",
		Category: CategoryFlowAdmin,
		Description: "Change one node: label / config / parent_node (move it into a container or back to the top level) / position (canvas coordinates). " +
			"config is a **wholesale replacement** while config_patch is a **top-level merge** (only the keys you mention change; a null value deletes that key) — pick one of the two. " +
			"When parent_node changes, edges that span the old and new container are deleted and listed in removed_edges (cross-parent edges are invalid and the canvas rejects them too), " +
			"so remember to rewire with create_edge after moving a node into a container. position is the canvas-absolute coordinate for top-level nodes and the parent-relative coordinate for nodes inside a container. " +
			"**Existing concurrency config is left alone by default** (existing flows stay as they are): only an explicit concurrency argument rewrites this node's " +
			"config.concurrency (off = strip it, mono / group = switch to that shape). " +
			"The result returns node / config / removed_edges.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"position": map[string]any{
					"type": "object",
					"description": "Optional: move the node to {x, y} (px). Top-level nodes use canvas-absolute coordinates; nodes inside a container use parent-relative ones. " +
						"Layout advice: use multiples of 20 for x / y, keep the main chain on one row (same y) with an x step of 200 between neighbouring leaves (380 after branch / container), " +
						"place branches at the lower right of the branch node (≥ 72 between rows), and never overlap existing nodes",
					"properties": map[string]any{
						"x": map[string]any{"type": "number"},
						"y": map[string]any{"type": "number"},
					},
					"required": []any{"x", "y"},
				},
				"flow":         map[string]any{"type": "string", "description": flowRefHint},
				"node_id":      map[string]any{"type": "string", "description": "Id of the node to change (diagram.nodes[].id from read_flow)"},
				"label":        map[string]any{"type": "string", "description": "Optional: new title"},
				"config":       map[string]any{"type": "object", "description": "Optional: replace data.config wholesale (mutually exclusive with config_patch)"},
				"config_patch": map[string]any{"type": "object", "description": "Optional: merge into data.config at the top level; give a key the value null to delete it"},
				"parent_node":  map[string]any{"type": "string", "description": "Optional: move the node into this container (loop / process / function); pass an empty string to move it back to the top level"},
				"concurrency": map[string]any{
					"type": "string",
					"enum": []any{"off", "mono", "group"},
					"description": "Optional: rewrite this node's concurrency shape; **omitted = leave the existing config.concurrency exactly as it is** " +
						"(existing flows stay unchanged). Pass it only when the user explicitly asks for concurrency or to turn it off: " +
						"off strips concurrency, mono switches to single-node concurrency, group switches to a concurrency group (needs fork_node_id)",
				},
			},
			"required": []any{"flow", "node_id"},
		},
	}
}

func (t *updateNodeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	doc, detail, err := loadFlowDoc(ctx, c, argString(args, "flow"))
	if err != nil {
		return nil, err
	}
	nodeID := strings.TrimSpace(argString(args, "node_id"))
	if nodeID == "" {
		return nil, errors.New("node_id must not be empty (diagram.nodes[].id from read_flow)")
	}
	idx := doc.nodeIndex(nodeID)
	if idx < 0 {
		return nil, fmt.Errorf("no node with node_id=%q: check diagram.nodes with read_flow first", nodeID)
	}
	cur := doc.nodes[idx]

	_, hasReplace := args["config"]
	_, hasPatch := args["config_patch"]
	if hasReplace && hasPatch {
		return nil, errors.New("give either config or config_patch: config replaces wholesale, config_patch changes a few keys")
	}
	var replaceCfg map[string]any
	if hasReplace {
		if replaceCfg, err = argObject(args, "config"); err != nil {
			return nil, err
		}
	}
	var patchCfg map[string]any
	if hasPatch {
		if patchCfg, err = argObject(args, "config_patch"); err != nil {
			return nil, err
		}
	}
	label := strings.TrimSpace(argString(args, "label"))
	if label != "" {
		if owner := doc.labelOwner(label); owner != "" && owner != nodeID {
			return nil, fmt.Errorf(
				"label %q is already used by node %s: node labels must be unique across the flow"+
					" (source_ref / call.target / fork_node_id can all be written as labels, and a duplicate points at the wrong node)",
				label, owner)
		}
	}

	newParent, moved := argStringSet(args, "parent_node")
	pos, hasPos, posErr := argNodePosition(args)
	if posErr != nil {
		return nil, posErr
	}
	// 并发默认不改存量：不传 concurrency 就完全不动节点已有的 config.concurrency。
	_, hasConcurrencyArg := args["concurrency"]
	concurrencyPolicy, err := parseConcurrencyPolicy(args)
	if err != nil {
		return nil, err
	}
	if !moved && label == "" && !hasReplace && !hasPatch && !hasPos && !hasConcurrencyArg {
		return nil, errors.New("nothing to change: give at least one of label / config / config_patch / parent_node / position / concurrency")
	}
	if moved {
		if newParent != "" {
			p, ok := doc.node(newParent)
			if !ok {
				return nil, fmt.Errorf("parent_node %q does not exist", newParent)
			}
			if !containerTypes[p.Type] {
				return nil, fmt.Errorf("parent_node %q is %s and cannot hold children: only loop / process / function are containers", newParent, p.Type)
			}
			if containsStr(doc.subtree(nodeID), newParent) {
				return nil, errors.New("cannot move a node into its own subtree")
			}
		}
		if cur.Type == "function" && newParent != "" {
			return nil, errors.New("a function node has to stay at the top level")
		}
		if cur.Type == "start" {
			return nil, errors.New("the start node is the fixed entry of the flow and cannot be moved")
		}
		if cur.Type == "entry" {
			return nil, errors.New("entry is the entry node of a container and follows it: move the whole container instead")
		}
	}

	// 显式传了 concurrency：先算出目标 config（整体替换 / 合并 / 只改这一项），
	// 按策略改写 concurrency，再统一按「整体替换」写回。
	var concurrencyWarnings []string
	if hasConcurrencyArg {
		var cfg map[string]any
		switch {
		case hasReplace:
			cfg = replaceCfg
		case hasPatch:
			cfg = nodeConfigOf(doc.nodeRaw[idx])
			for k, v := range patchCfg {
				if v == nil {
					delete(cfg, k)
					continue
				}
				cfg[k] = v
			}
		default:
			cfg = nodeConfigOf(doc.nodeRaw[idx])
		}
		if w := applyConcurrencyPolicy(cfg, cur.Type, concurrencyPolicy, "node "+nodeID); w != "" {
			concurrencyWarnings = append(concurrencyWarnings, w)
		}
		replaceCfg, hasReplace, hasPatch = cfg, true, false
	}

	newRaw, err := patchNodeJSON(doc.nodeRaw[idx], func(node, data map[string]json.RawMessage) error {
		if label != "" {
			b, err := json.Marshal(label)
			if err != nil {
				return err
			}
			data["label"] = b
		}
		if hasReplace {
			b, err := json.Marshal(replaceCfg)
			if err != nil {
				return err
			}
			data["config"] = b
		}
		if hasPatch {
			merged := nodeConfigOf(doc.nodeRaw[idx])
			for k, v := range patchCfg {
				if v == nil {
					delete(merged, k)
					continue
				}
				merged[k] = v
			}
			b, err := json.Marshal(merged)
			if err != nil {
				return err
			}
			data["config"] = b
		}
		if moved {
			if newParent == "" {
				delete(node, "parent_node")
				delete(node, "extent")
			} else {
				pb, err := json.Marshal(newParent)
				if err != nil {
					return err
				}
				node["parent_node"] = pb
				eb, err := json.Marshal(childExtent())
				if err != nil {
					return err
				}
				node["extent"] = eb
			}
			posb, err := json.Marshal(doc.positionFor(newParent))
			if err != nil {
				return err
			}
			node["position"] = posb
		}
		// 显式坐标优先于「换容器时的自动落点」：调用方给了就不该被覆盖
		if hasPos {
			posb, err := json.Marshal(pos)
			if err != nil {
				return err
			}
			node["position"] = posb
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := doc.setNodeRaw(idx, newRaw); err != nil {
		return nil, err
	}

	// 换了容器：横跨新旧作用域的边不再合法（画布 onConnect 也不允许），直接删掉并回显。
	var removed []flowEdge
	if moved {
		keep := make([]flowEdge, 0, len(doc.edges))
		for _, e := range doc.edges {
			if doc.parentOf(e.Source) != doc.parentOf(e.Target) {
				removed = append(removed, e)
				continue
			}
			keep = append(keep, e)
		}
		doc.edges = keep
	}

	info, err := saveDiagram(ctx, c, detail, doc)
	if err != nil {
		return nil, err
	}
	n, _ := doc.node(nodeID)
	out := map[string]any{
		"flow_id":    info.ID,
		"flow_name":  info.Name,
		"node":       nodeView(n),
		"config":     nodeConfigOf(doc.nodeRaw[doc.nodeIndex(nodeID)]),
		"edge_count": len(doc.edges),
	}
	if moved {
		out["parent_node"] = newParent
		out["note"] = "edges crossing containers were removed: a cross-parent edge is invalid, so re-create it inside the new container with create_edge"
	}
	if len(removed) > 0 {
		views := make([]any, 0, len(removed))
		for _, e := range removed {
			views = append(views, edgeView(e))
		}
		out["removed_edges"] = views
	}
	if len(concurrencyWarnings) > 0 {
		out["warnings"] = concurrencyWarnings
	}
	return out, nil
}

// ===== delete_node =====

type deleteNodeTool struct{}

func (t *deleteNodeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_node",
		Category: CategoryFlowAdmin,
		Description: "Delete a node together with all of its edges; a container is deleted along with its entire subtree (entry included). " +
			"It is **refused** while other nodes still reference it, and the referrers are listed (config.inputs.*.source_ref / config.target / config.fork_node_id) — " +
			"those references do not disappear with the node, so deleting it would fail at run time. start / entry cannot be deleted on their own. " +
			"The result returns deleted_nodes and removed_edges (use them to rebuild if you deleted the wrong thing).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":    map[string]any{"type": "string", "description": flowRefHint},
				"node_id": map[string]any{"type": "string", "description": "Id of the node to delete (diagram.nodes[].id from read_flow)"},
			},
			"required": []any{"flow", "node_id"},
		},
	}
}

func (t *deleteNodeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	doc, detail, err := loadFlowDoc(ctx, c, argString(args, "flow"))
	if err != nil {
		return nil, err
	}
	nodeID := strings.TrimSpace(argString(args, "node_id"))
	if nodeID == "" {
		return nil, errors.New("node_id must not be empty (diagram.nodes[].id from read_flow)")
	}
	idx := doc.nodeIndex(nodeID)
	if idx < 0 {
		return nil, fmt.Errorf("no node with node_id=%q: check diagram.nodes with read_flow first", nodeID)
	}
	n := doc.nodes[idx]
	switch n.Type {
	case "start":
		return nil, errors.New("the start node is the fixed entry of the flow and the graph cannot run without it: use delete_flow to clear the flow")
	case "entry":
		return nil, fmt.Errorf("entry (id=%s) is the entry node of container %q and is managed with it: delete the container itself", nodeID, n.ParentNode)
	}

	sub := doc.subtree(nodeID)
	subSet := make(map[string]bool, len(sub))
	for _, id := range sub {
		subSet[id] = true
	}
	if refs := doc.referencesTo(subSet); len(refs) > 0 {
		return nil, fmt.Errorf("cannot delete: other nodes still reference it — fix those references with update_node first, then delete\n - %s", strings.Join(refs, "\n - "))
	}

	var removedEdges []flowEdge
	keep := make([]flowEdge, 0, len(doc.edges))
	for _, e := range doc.edges {
		if subSet[e.Source] || subSet[e.Target] {
			removedEdges = append(removedEdges, e)
			continue
		}
		keep = append(keep, e)
	}
	doc.edges = keep

	deleted := make([]any, 0, len(sub))
	for i := len(doc.nodeRaw) - 1; i >= 0; i-- {
		var meta flowNodeMeta
		if err := json.Unmarshal(doc.nodeRaw[i], &meta); err != nil {
			continue
		}
		if !subSet[meta.ID] {
			continue
		}
		deleted = append(deleted, map[string]any{"id": meta.ID, "type": meta.Type})
		if err := doc.removeNodeAt(i); err != nil {
			return nil, err
		}
	}

	info, err := saveDiagram(ctx, c, detail, doc)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"flow_id":       info.ID,
		"flow_name":     info.Name,
		"deleted_nodes": deleted,
		"node_count":    len(doc.nodes),
		"edge_count":    len(doc.edges),
	}
	if len(removedEdges) > 0 {
		views := make([]any, 0, len(removedEdges))
		for _, e := range removedEdges {
			views = append(views, edgeView(e))
		}
		out["removed_edges"] = views
	}
	return out, nil
}

// resolveNodeLabels 把 config.target（call）/ config.fork_node_id（wait）里写成 label 的值换成节点 id。
// 与 resolveSourceRefs 同一套口径：id 优先，只在值不匹配任何节点 id 时才按 label 替换。
func (b *diagramBuilder) resolveNodeLabels() {
	byLabel := make(map[string]string, len(b.nodes))
	ids := make(map[string]bool, len(b.nodes))
	for _, n := range b.nodes {
		ids[n.ID] = true
		label, _ := n.Data["label"].(string)
		if _, dup := byLabel[label]; label == "" || dup {
			continue
		}
		byLabel[label] = n.ID
	}
	resolve := func(v any) any {
		s, _ := v.(string)
		if s == "" || ids[s] {
			return v
		}
		if id, ok := byLabel[s]; ok {
			return id
		}
		return v
	}
	for _, n := range b.nodes {
		cfg := objectOf(n.Data["config"])
		for _, key := range []string{"target", "fork_node_id"} {
			if v, ok := cfg[key]; ok {
				cfg[key] = resolve(v)
			}
		}
	}
}
