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
		return "HTTP 请求"
	case "mcp":
		return "MCP"
	case "rpc":
		return "RPC"
	case "llm":
		return "LLM"
	case "code":
		return "代码"
	case "branch":
		return "条件分支"
	case "intent":
		return "意图"
	case "loop":
		return "循环"
	case "process":
		return "嵌入流程"
	case "function":
		return "函数"
	case "call":
		return "调用"
	case "assign":
		return "赋值"
	case "memory":
		return "记忆"
	case "wait":
		return "并发同步"
	case "fork":
		return "并发组"
	case "result":
		return "结果输出"
	case "progress":
		return "进度输出"
	case "break":
		return "跳出"
	case "continue":
		return "跳过"
	case "return":
		return "返回"
	case "exit":
		return "终止"
	case "layout":
		return "布局"
	case "knowledge":
		return "知识"
	case "comment":
		return "注释"
	}
	return typ
}

// defaultNodeConfig 与前端「新建节点」写进去的模板对齐：空节点在画布上等于一堆必填项缺失。
func defaultNodeConfig(typ string, newID func() string) map[string]any {
	switch typ {
	case "http":
		return map[string]any{"outputs": map[string]any{
			"mode":            map[string]any{"type": "string", "description": "响应模式"},
			"status":          map[string]any{"type": "number", "description": "HTTP 状态码"},
			"headers":         map[string]any{"type": "string", "description": "响应头"},
			"body":            map[string]any{"type": "string", "description": "响应体"},
			"sse_events":      map[string]any{"type": "array[string]", "description": "SSE 事件列表"},
			"stream_finished": map[string]any{"type": "boolean", "description": "流式传输是否完成"},
		}}
	case "mcp", "rpc":
		return map[string]any{"outputs": map[string]any{
			"result":   map[string]any{"type": "string", "description": "工具返回结果"},
			"progress": map[string]any{"type": "array[string]", "description": "进度通知数组"},
			"error":    map[string]any{"type": "string", "description": "调用错误信息"},
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
			"loop_vars": map[string]any{"index": map[string]any{"type": "number", "description": "循环下标"}},
		}
	case "branch":
		return map[string]any{"children": []any{
			map[string]any{"id": newID(), "label": "分支 1", "condition": "", "exclusive": true},
		}}
	case "intent":
		return map[string]any{
			"inputs": map[string]any{
				"messages": map[string]any{"type": "array[object]", "source_type": "argument", "source_ref": "", "source_key": ""},
				"input":    map[string]any{"type": "string", "source_type": "argument", "source_ref": "", "source_key": ""},
			},
			"llm": map[string]any{
				"gateway_model": "",
				"system_prompt": "你是意图分类器，请根据对话历史和用户输入判断用户意图。\n\n对话历史：\n{{json .input.messages}}\n\n用户输入：\n{{.input.input}}",
				"params":        map[string]any{},
			},
			"children": []any{map[string]any{"id": newID(), "label": "意图 1", "description": ""}},
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
		return nil, fmt.Errorf("节点 JSON 解析失败: %w", err)
	}
	data := map[string]json.RawMessage{}
	if d := node["data"]; len(d) > 0 {
		if err := json.Unmarshal(d, &data); err != nil {
			return nil, fmt.Errorf("节点 data 解析失败: %w", err)
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
					refs = append(refs, fmt.Sprintf("节点 %s（%s）的 config.inputs.%s.source_ref", n.ID, n.Type, key))
				}
			}
		}
		if ref, _ := cfg["target"].(string); ref != "" && targets[ref] {
			refs = append(refs, fmt.Sprintf("节点 %s（%s）的 config.target（call 调用目标）", n.ID, n.Type))
		}
		if ref, _ := cfg["fork_node_id"].(string); ref != "" && targets[ref] {
			refs = append(refs, fmt.Sprintf("节点 %s（%s）的 config.fork_node_id（并发组）", n.ID, n.Type))
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
		return "", "", "", nil, errors.New("type 不能为空")
	}
	if !creatableNodeTypes[typ] {
		hint := "start 由 create_flow 自动生成，entry 由容器自动带出"
		if typ == "knowledge" {
			hint = "knowledge 后端没有执行器（连进链路只会报 no executor registered），只为渲染存量流程图保留；" +
				"需要知识检索请用 mcp / rpc 节点调对应服务，或让 llm 节点自己带上下文"
		}
		return "", "", "", nil, fmt.Errorf("不能建 %q 类型的节点。可以建：%s（%s）", typ, creatableTypeList(), hint)
	}
	if parentID != "" {
		p, ok := doc.node(parentID)
		if !ok {
			return "", "", "", nil, fmt.Errorf("parent_node %q 不存在：id 从 read_flow 的 diagram.nodes[].id 里取", parentID)
		}
		if !containerTypes[p.Type] {
			return "", "", "", nil, fmt.Errorf("parent_node %q 是 %s，不能装子节点：只有 loop / process / function 是容器", parentID, p.Type)
		}
	}
	if typ == "function" && parentID != "" {
		return "", "", "", nil, errors.New("function 节点只能建在顶层：它靠 call 节点按 config.target 调用，不参与连线")
	}
	if typ == "return" && !doc.ancestorIs(parentID, "function") {
		return "", "", "", nil, errors.New("return 节点只能建在 function 容器内部（直接子节点或更深）")
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
	if w := applyConcurrencyPolicy(cfg, typ, concurrency, "新节点"); w != "" {
		warnings = append(warnings, w)
	}
	// branch / intent 的 children 允许调用方自己给；给了就要保证每项都有 id。
	normalizeBranchChildren(doc, typ, cfg)

	label = strings.TrimSpace(label)
	if label != "" {
		if owner := doc.labelOwner(label); owner != "" {
			return "", "", "", nil, fmt.Errorf(
				"label %q 已被节点 %s 使用：全流程节点 label 必须唯一"+
					"（source_ref / call.target / fork_node_id 都能按 label 写，重名会指错节点）；换一个，或留空让工具自动加后缀",
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
		praw, err := buildNodeJSON(pid, "entry", doc.uniqueLabel("入口"), nil, parentID, map[string]int{"x": childBaseX, "y": childBaseY})
		if err != nil {
			return "", "", "", nil, err
		}
		if err := doc.addNode(praw); err != nil {
			return "", "", "", nil, err
		}
		parentEntryID = pid
		warnings = append(warnings, fmt.Sprintf("容器 %q 之前没有 entry，已补一个（id=%s）：容器没有 entry 会「成功但什么都不跑」", parentID, pid))
	}

	if containerTypes[typ] {
		entryID = doc.newNodeID()
		eraw, err := buildNodeJSON(entryID, "entry", doc.uniqueLabel("入口"), nil, id, map[string]int{"x": childBaseX, "y": childBaseY})
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
		base = "节点"
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
		cfg["children"] = []any{map[string]any{"id": doc.newNodeID(), "label": "分支 1", "condition": "", "exclusive": true}}
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
		Description: "给已有流程**加一个节点**（不连线，连线用 create_edge / insert_node）。" +
			"loop / process / function 会自动带一个 entry 子节点 —— 容器子链必须从 entry 开始，否则运行期静默空转。" +
			"branch / intent 会自动带一条 config.children（执行器靠 children[].id 找出边）。" +
			"parent_node 指定容器 id 即可建子节点；只有 loop / process / function 能装子节点，" +
			"function 只能建在顶层，return 只能建在 function 内部。" +
			"**并发默认关闭**：config.concurrency 会被摘掉并回 warning，只有用户明确要求并发时才用 " +
			"concurrency 参数声明（mono / group）。" +
			"返回 node / entry / handles（该类型可用的出入 handle 位置）/ warnings。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":   map[string]any{"type": "string", "description": flowRefHint},
				"type":   map[string]any{"type": "string", "description": "节点类型：" + creatableTypeList()},
				"label":  map[string]any{"type": "string", "description": "节点标题；不给就用类型的中文默认名"},
				"config": map[string]any{"type": "object", "description": "节点 config（与前端该类型表单同形状）；不给就用该类型的默认模板"},
				"parent_node": map[string]any{
					"type":        "string",
					"description": "可选：容器节点 id（loop / process / function）。不传 = 建在顶层",
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
		"next_step":  "用 create_edge 连它：" + edgeHint(id, typ),
	}
	if entryID != "" {
		out["entry"] = map[string]any{"id": entryID, "parent_node": id}
		out["container_note"] = "容器子链从 entry（id=" + entryID + "）开始：先 create_edge(source=" + entryID + ") 把第一个子节点接上"
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
		return "子分支用 source<children[].id>right，默认分支用 source" + id + "right"
	}
	rule := nodeHandleRules[typ]
	if len(rule.in) == 0 || len(rule.out) == 0 {
		return "该类型没有入边或没有出边，不能连（function 靠 call 调用）"
	}
	return "source_handle=source" + id + rule.out[0] + "，target_handle=target<目标id>" + rule.in[0]
}

// ===== insert_node =====

type insertNodeTool struct{}

func (t *insertNodeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "insert_node",
		Category: CategoryFlowAdmin,
		Description: "在一条**已存在的边**中间插入一个节点：删掉旧边，自动连 A→新→B（原子操作，不会留半截图）。" +
			"新节点落在与这条边相同的 parent 下（跨容器插不进去）。" +
			"需要入边和出边的节点才能插（function / exit / break 这类不能插）。" +
			"**并发默认关闭**：config.concurrency 会被摘掉并回 warning，只有用户明确要求并发时才用 concurrency 参数声明。" +
			"返回 node、两条 edge、handles 与 warnings。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":        map[string]any{"type": "string", "description": flowRefHint},
				"edge_id":     map[string]any{"type": "string", "description": "被插入的那条边 id（read_flow 的 diagram.edges[].id）"},
				"type":        map[string]any{"type": "string", "description": "新节点的类型：" + creatableTypeList()},
				"label":       map[string]any{"type": "string", "description": "节点标题；不给就用类型的中文默认名"},
				"config":      map[string]any{"type": "object", "description": "节点 config；不给就用该类型的默认模板"},
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
		return nil, errors.New("edge_id 不能为空（read_flow 的 diagram.edges[].id）")
	}
	idx := doc.edgeIndex(edgeID)
	if idx < 0 {
		return nil, fmt.Errorf("找不到 edge_id=%q 的边；先 read_flow 核对 diagram.edges", edgeID)
	}
	old := doc.edges[idx]
	if old.SourceHandle == "" || old.TargetHandle == "" {
		return nil, fmt.Errorf("边 %s 缺 source_handle / target_handle，插不了；先 update_edge 把它补齐", edgeID)
	}
	src, ok := doc.node(old.Source)
	if !ok {
		return nil, fmt.Errorf("边 %s 的 source 节点 %q 不存在", edgeID, old.Source)
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
		return nil, fmt.Errorf("插入后的前一条边不合法: %w", err)
	}
	if _, err := doc.validateEdge(tail, ""); err != nil {
		return nil, fmt.Errorf("插入后的后一条边不合法: %w", err)
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
		return fmt.Errorf("%s 节点没有入边，不能插在链中间（顶部节点请用 create_node 建在顶层）", typ)
	}
	if len(rule.out) == 0 {
		return fmt.Errorf("%s 节点没有出边，不能插在链中间（终止节点请用 create_edge 连到链尾）", typ)
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
		Description: "改一个节点：label / config / parent_node（移到容器里或移回顶层）/ position（画布坐标）。" +
			"config 是**整体替换**，config_patch 是**顶层合并**（只改提到的键，值为 null 表示删除该键）—— 二选一。" +
			"改 parent_node 时，横跨新旧容器的边会被删除并在 removed_edges 里列出（跨父节点的边不合法，画布也不允许），" +
			"移进容器后记得用 create_edge 重新连。position 是顶层节点的画布绝对坐标；容器内节点是相对父容器的坐标。" +
			"**并发的既有配置默认不动**（存量流程保持原样）：只有显式传 concurrency 才改写这个节点的 " +
			"config.concurrency（off = 摘掉，mono / group = 改成对应形态）。" +
			"返回 node / config / removed_edges。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"position": map[string]any{
					"type": "object",
					"description": "可选：把节点挪到 {x, y}（px）。顶层节点=画布绝对坐标；容器内节点=相对父容器坐标。" +
						"排版建议：x / y 取 20 的倍数，主链同 y 同行、相邻叶子 x 步长 200（branch / 容器之后 380），" +
						"分支放分支节点右下方（行间 ≥ 72），别和已有节点重叠",
					"properties": map[string]any{
						"x": map[string]any{"type": "number"},
						"y": map[string]any{"type": "number"},
					},
					"required": []any{"x", "y"},
				},
				"flow":         map[string]any{"type": "string", "description": flowRefHint},
				"node_id":      map[string]any{"type": "string", "description": "要改的节点 id（read_flow 的 diagram.nodes[].id）"},
				"label":        map[string]any{"type": "string", "description": "可选：新标题"},
				"config":       map[string]any{"type": "object", "description": "可选：整体替换 data.config（与 config_patch 二选一）"},
				"config_patch": map[string]any{"type": "object", "description": "可选：顶层合并到 data.config；某个键的值给 null 表示删掉它"},
				"parent_node":  map[string]any{"type": "string", "description": "可选：移到这个容器（loop / process / function）里；传空串 = 移回顶层"},
				"concurrency": map[string]any{
					"type": "string",
					"enum": []any{"off", "mono", "group"},
					"description": "可选：改写该节点的并发形态，**默认不传 = 完全不动已有的 config.concurrency**" +
						"（存量流程保持原样）。只有用户明确要求并发/关闭并发时才传：" +
						"off 摘掉并发、mono 改成单体并发、group 改成并发组（需要 fork_node_id）",
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
		return nil, errors.New("node_id 不能为空（read_flow 的 diagram.nodes[].id）")
	}
	idx := doc.nodeIndex(nodeID)
	if idx < 0 {
		return nil, fmt.Errorf("找不到 node_id=%q 的节点；先 read_flow 核对 diagram.nodes", nodeID)
	}
	cur := doc.nodes[idx]

	_, hasReplace := args["config"]
	_, hasPatch := args["config_patch"]
	if hasReplace && hasPatch {
		return nil, errors.New("config 与 config_patch 只能给一个：整体替换用 config，只改几项用 config_patch")
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
				"label %q 已被节点 %s 使用：全流程节点 label 必须唯一"+
					"（source_ref / call.target / fork_node_id 都能按 label 写，重名会指错节点）",
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
		return nil, errors.New("没有要改的字段：label / config / config_patch / parent_node / position / concurrency 至少给一个")
	}
	if moved {
		if newParent != "" {
			p, ok := doc.node(newParent)
			if !ok {
				return nil, fmt.Errorf("parent_node %q 不存在", newParent)
			}
			if !containerTypes[p.Type] {
				return nil, fmt.Errorf("parent_node %q 是 %s，不能装子节点：只有 loop / process / function 是容器", newParent, p.Type)
			}
			if containsStr(doc.subtree(nodeID), newParent) {
				return nil, errors.New("不能把节点移进它自己的子树")
			}
		}
		if cur.Type == "function" && newParent != "" {
			return nil, errors.New("function 节点只能待在顶层")
		}
		if cur.Type == "start" {
			return nil, errors.New("start 节点是流程的固定入口，不能移动")
		}
		if cur.Type == "entry" {
			return nil, errors.New("entry 是容器的入口节点，跟着容器走；要换容器请移动整个容器")
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
		if w := applyConcurrencyPolicy(cfg, cur.Type, concurrencyPolicy, "节点 "+nodeID); w != "" {
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
		out["note"] = "跨容器的边已被删除：跨父节点的边不合法，请在新容器内重新 create_edge"
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
		Description: "删一个节点，连带删掉它的全部边；容器会连同整棵子树（含 entry）一起删。" +
			"被别的节点引用时**拒绝**并列出引用方（config.inputs.*.source_ref / config.target / config.fork_node_id）——" +
			"这些引用不会跟着节点消失，删了会在运行期报错。start / entry 不能单独删。" +
			"返回 deleted_nodes 与 removed_edges（删错了照着它重建）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":    map[string]any{"type": "string", "description": flowRefHint},
				"node_id": map[string]any{"type": "string", "description": "要删的节点 id（read_flow 的 diagram.nodes[].id）"},
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
		return nil, errors.New("node_id 不能为空（read_flow 的 diagram.nodes[].id）")
	}
	idx := doc.nodeIndex(nodeID)
	if idx < 0 {
		return nil, fmt.Errorf("找不到 node_id=%q 的节点；先 read_flow 核对 diagram.nodes", nodeID)
	}
	n := doc.nodes[idx]
	switch n.Type {
	case "start":
		return nil, errors.New("start 节点是流程的固定入口，删完整张图就跑不起来；要清空请 delete_flow")
	case "entry":
		return nil, fmt.Errorf("entry（id=%s）是容器 %q 的入口节点，随容器管理：要删请删容器本身", nodeID, n.ParentNode)
	}

	sub := doc.subtree(nodeID)
	subSet := make(map[string]bool, len(sub))
	for _, id := range sub {
		subSet[id] = true
	}
	if refs := doc.referencesTo(subSet); len(refs) > 0 {
		return nil, fmt.Errorf("不能删：还有节点引用它 —— 先 update_node 改掉这些引用，再删\n - %s", strings.Join(refs, "\n - "))
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
