package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
)

// ===== 边工具（create_edge / update_edge / delete_edge）=====
//
// 为什么单独一套：create_flow 只能建「主链 + 分支子链」，容器子节点、并发组、
// 以及任何重接都碰不到；让模型直接写整个 diagram 又会在 20 多种节点的 config /
// 坐标 / handle 上出错。这里只把「边」这一层暴露出来。
//
// 规则与前端画布严格同口径（Flow.vue 的 onConnect + 各 *Node.vue 的 Handle 声明）：
// 工具放宽一条，画布下一次拖动就会把图改坏；收紧一条，画布能拖出来的结构就建不出来。

// nodeHandleRule 某类节点允许的 handle 位置。
type nodeHandleRule struct {
	in  []string // 入边（target_handle）
	out []string // 出边（source_handle）
}

var (
	// ioNode 单入单出的执行节点：入 left/top，出 right/bottom
	ioNode = nodeHandleRule{in: []string{"left", "top"}, out: []string{"right", "bottom"}}
	// containerNode 容器 / 分支：多一个右侧入、一个左侧出（子链从左边接回去时要用）
	containerNode = nodeHandleRule{in: []string{"left", "top", "right"}, out: []string{"left", "right", "bottom"}}
	// sinkNode 终止节点：只有入边
	sinkNode = nodeHandleRule{in: []string{"left", "top", "right", "bottom"}}
	// sourceOnlyNode 起点 / 容器入口：只有出边
	sourceOnlyNode = nodeHandleRule{out: []string{"left", "right", "bottom"}}
)

var nodeHandleRules = map[string]nodeHandleRule{
	"start": sourceOnlyNode, "entry": sourceOnlyNode,
	"llm": ioNode, "code": ioNode, "http": ioNode, "mcp": ioNode, "rpc": ioNode,
	"assign": ioNode, "memory": ioNode, "result": ioNode, "progress": ioNode,
	"call": ioNode, "wait": ioNode, "fork": ioNode, "layout": ioNode, "knowledge": ioNode,
	"loop": containerNode, "process": containerNode, "branch": containerNode, "intent": containerNode,
	"exit": sinkNode, "break": sinkNode, "continue": sinkNode, "return": sinkNode,
	// function 没有任何 handle（validateEdge 里显式拦）；comment 是纯注释，同样不连线
	"function": {}, "comment": {},
}

// branchChildOut branch / intent 的子分支（children[].id）只有左右两个出边
var branchChildOut = []string{"left", "right"}

// ===== 按位置推荐 handle =====
//
// 连线方向由「出边节点（source）相对入边节点（target）」的位置决定：同一行走左右端点，
// 落差越大越偏向垂直端点。规则只做建议（画布上任何合法组合都能连），但照它连出来的
// 线最短、最少交叉；模型自己拍脑袋给 handle 时，工具会拿这套结果回显提示。
//
// 画布坐标 y 向下为正，阈值按节点高 32 / 主链行距约 140 取。
const (
	// handleAlignTol 视为「同一行 / 同一列」的容差（一个节点高再加一点余量）
	handleAlignTol = 40
	// handleFarTol 超过这个落差就算「高/低很多」，改走垂直端点
	handleFarTol = 200
)

// handleSuggest 按位置算出来的推荐连线（含一句方位说明）。
type handleSuggest struct {
	SourceHandle string
	TargetHandle string
	Reason       string
}

// handlePreferOrder 目标位置不可用时退而求其次的顺序：例如 start 没有 top 出边，
// 推荐 top 会自然退到 left / bottom。
var handlePreferOrder = map[string][]string{
	"left":   {"left", "top", "right", "bottom"},
	"right":  {"right", "top", "bottom", "left"},
	"top":    {"top", "left", "right", "bottom"},
	"bottom": {"bottom", "right", "left", "top"},
}

// suggestHandlePositions 按相对位置算出两个端点位置（已按节点类型可用位置退化）。
// create_edge 的建议与 create_flow 的 handle 回填共用这一份判定，避免两套规则。
//
//	基本同一行（|Δy| ≤ 40）   source 在左 → right→left；在右 → left→right
//	正上下堆叠（|Δx| ≤ 40）   target 在下 → bottom→top；在上 → top→bottom
//	高很多（Δy > 200）        bottom→top（垂直落下）
//	低很多（Δy < -200）       source 在左 → top→left；在右 → top→right
//	高得明显（Δy 40~200）     target 在右下方 → right→top；左下方 → left→top
//	低得明显（Δy -40~-200）   target 在右上方 → right→left；左上方 → left→right
func suggestHandlePositions(srcType string, srcX, srcY int, dstType string, dstX, dstY int) (srcPos, dstPos, reason string) {
	dx := dstX - srcX
	dy := dstY - srcY
	adx, ady := dx, dy
	if adx < 0 {
		adx = -adx
	}
	if ady < 0 {
		ady = -ady
	}

	switch {
	case ady <= handleAlignTol:
		if dx >= 0 {
			srcPos, dstPos, reason = "right", "left", "同一行、目标在右侧"
		} else {
			srcPos, dstPos, reason = "left", "right", "同一行、目标在左侧"
		}
	case adx <= handleAlignTol:
		if dy > 0 {
			srcPos, dstPos, reason = "bottom", "top", "正下方堆叠"
		} else {
			srcPos, dstPos, reason = "top", "bottom", "正上方堆叠"
		}
	case dy > handleFarTol:
		srcPos, dstPos, reason = "bottom", "top", "目标远在下方"
	case dy < -handleFarTol:
		if dx > 0 {
			srcPos, dstPos, reason = "top", "left", "目标远在上方"
		} else {
			srcPos, dstPos, reason = "top", "right", "目标远在上方、偏左"
		}
	case dy > 0:
		if dx > 0 {
			srcPos, dstPos, reason = "right", "top", "目标在右下方"
		} else {
			srcPos, dstPos, reason = "left", "top", "目标在左下方"
		}
	default:
		if dx > 0 {
			srcPos, dstPos, reason = "right", "left", "目标在右上方"
		} else {
			srcPos, dstPos, reason = "left", "right", "目标在左上方"
		}
	}

	wantSrc, wantDst := srcPos, dstPos
	srcPos = pickHandle(nodeHandleRules[srcType].out, handlePreferOrder[wantSrc])
	dstPos = pickHandle(nodeHandleRules[dstType].in, handlePreferOrder[wantDst])
	// 该类型没有这个位置（例如所有节点的出边都没有 top）：说明退到了哪，别让调用方以为按建议连上了。
	if srcPos != wantSrc {
		reason += fmt.Sprintf("；%s 没有 %s 出边，退到 %s", srcType, wantSrc, srcPos)
	}
	if dstPos != wantDst {
		reason += fmt.Sprintf("；%s 没有 %s 入边，退到 %s", dstType, wantDst, dstPos)
	}
	return srcPos, dstPos, reason
}

// suggestHandles 按两个节点的相对位置推荐一对 handle（供 create_edge 回显）。
func suggestHandles(src, dst *flowNodeMeta) handleSuggest {
	out, in, reason := suggestHandlePositions(
		src.Type, src.Position.X, src.Position.Y,
		dst.Type, dst.Position.X, dst.Position.Y)
	return handleSuggest{
		SourceHandle: "source" + src.ID + out,
		TargetHandle: "target" + dst.ID + in,
		Reason:       reason,
	}
}

// handleLayoutAdvice 给模型看的位置→handle 规则（create_edge 描述与报错共用）。
const handleLayoutAdvice = "**handle 按位置选**（Δ = 目标坐标 - 发出坐标，y 向下为正）：" +
	"基本同一行（|Δy| ≤ 40）→ 发出节点的 right 连目标的 left（目标在左就反过来：left→right）；" +
	"高得明显（Δy 40~200，目标在右下方）→ right→top（目标在左下方用 left→top）；" +
	"高很多（Δy > 200，目标在正下方远处）→ bottom→top；" +
	"低得明显（Δy -200~-40，目标在右上方）→ right→left（目标在左上方用 left→right）；" +
	"低很多（Δy < -200，目标在正上方远处）→ top→left（目标偏左用 top→right）；" +
	"正上下堆叠（|Δx| ≤ 40）→ 下→上用 bottom→top、上→下用 top→bottom。" +
	"注意：目前**所有节点都没有 top 出边**、bottom 入边只有终止节点（exit / break / continue / return）有，" +
	"所以「低很多 / 上→下」只能退到该类型支持的位置；工具会把实际退到的位置写进 suggested_edge.reason。"

// edgeRuleText 连边规则的唯一出处（create_edge 与 update_edge 共用同一份文案）。
// 技能文档 references/edges.md 不再重复枚举这些规则，只放形状 / 示例 / 排查，
// 避免「文档说一套、工具做一套」把模型带偏。
const edgeRuleText = "**handle 形状**：source_handle = source + handle owner + 位置，target_handle = target + 到达节点 id + 位置。" +
	"owner 一般是发出节点自己的 id；branch / intent 的**子分支出边**用 config.children[].id（位置只有 left / right），" +
	"默认分支才用节点自己的 id。**两个 handle 都必填、没有缺省值**：位置写错不会报「渲染不出来」，" +
	"而是运行期按 handle 找路时静默走不通，所以这里宁可硬拦。" +
	"**各类型可用位置**（入边 / 出边）：" +
	"llm / code / http / mcp / rpc / assign / memory / result / progress / call / wait / fork / layout / knowledge → left|top / right|bottom；" +
	"start / entry → 无 / left|right|bottom；" +
	"loop / process / branch / intent → left|top|right / left|right|bottom；" +
	"exit / break / continue / return → left|top|right|bottom / 无；" +
	"function / comment → 不能连线（出现在任一端直接拒绝）。" +
	"**硬拦项**：不能自连；两个节点必须同一个 parent（不同容器的子节点不能互连）；同一对节点最多一条边；" +
	"一个出口（source_handle）只能一条边；一个节点只能一条入边（同一入口重复连报错，换入口则旧入边被顶掉并回 replaced_edge）；不能成环。" +
	handleLayoutAdvice

// flowEdge 落库形态的一条边（前端保存时也只写这五个键）。
type flowEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"source_handle"`
	TargetHandle string `json:"target_handle"`
}

// flowNodeMeta 校验只需要节点的这几个字段；坐标 / 尺寸 / extent 等原样保留在 diagram 里。
type flowNodeMeta struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ParentNode string `json:"parent_node"`
	// Position 只用来给新节点算一个不重叠的落点（子节点是相对父节点的坐标）。
	Position struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"position"`
	Data struct {
		Label  string `json:"label"`
		Config struct {
			Children []struct {
				ID string `json:"id"`
			} `json:"children"`
		} `json:"config"`
	} `json:"data"`
}

func (n *flowNodeMeta) hasChild(id string) bool {
	if n.Type != "branch" && n.Type != "intent" {
		return false
	}
	for _, c := range n.Data.Config.Children {
		if c.ID == id {
			return true
		}
	}
	return false
}

// flowDoc 一份 diagram：nodes / edges 逐个原样保留，只重写真正改到的那几条。
//
// nodeRaw 是节点的原始 JSON —— 坐标、extent、前端私有字段都不解不重排，
// 免得「工具只改了一条边，却把整张图的节点样式全洗了一遍」。
type flowDoc struct {
	top     map[string]json.RawMessage
	nodeRaw []json.RawMessage
	nodes   []flowNodeMeta
	edges   []flowEdge
}

func parseFlowDoc(raw []byte) (*flowDoc, error) {
	if len(raw) == 0 {
		return nil, errors.New("这个工作流还没有 diagram（没在画布保存过）：先用 create_flow 建，或在画布上保存一次")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("diagram 不是合法的 JSON 对象: %w", err)
	}
	d := &flowDoc{top: top}
	if nraw := top["nodes"]; len(nraw) > 0 {
		if err := json.Unmarshal(nraw, &d.nodeRaw); err != nil {
			return nil, fmt.Errorf("diagram.nodes 解析失败: %w", err)
		}
	}
	if err := d.reloadNodes(); err != nil {
		return nil, err
	}
	if eraw := top["edges"]; len(eraw) > 0 {
		if err := json.Unmarshal(eraw, &d.edges); err != nil {
			return nil, fmt.Errorf("diagram.edges 解析失败: %w", err)
		}
	}
	return d, nil
}

func (d *flowDoc) marshal() (json.RawMessage, error) {
	if d.nodeRaw != nil {
		nb, err := json.Marshal(d.nodeRaw)
		if err != nil {
			return nil, err
		}
		d.top["nodes"] = nb
	}
	edges := d.edges
	if edges == nil {
		edges = []flowEdge{}
	}
	eb, err := json.Marshal(edges)
	if err != nil {
		return nil, err
	}
	d.top["edges"] = eb
	return json.Marshal(d.top)
}

// reloadNodes 重新解析节点元信息；节点被增删改之后必须调它，
// 否则后续校验（parent 作用域、分支子分支）还在看旧快照。
func (d *flowDoc) reloadNodes() error {
	d.nodes = make([]flowNodeMeta, len(d.nodeRaw))
	for i := range d.nodeRaw {
		if err := json.Unmarshal(d.nodeRaw[i], &d.nodes[i]); err != nil {
			return fmt.Errorf("diagram.nodes[%d] 解析失败: %w", i, err)
		}
	}
	return nil
}

func (d *flowDoc) nodeIndex(id string) int {
	for i := range d.nodes {
		if d.nodes[i].ID == id {
			return i
		}
	}
	return -1
}

func (d *flowDoc) addNode(raw json.RawMessage) error {
	d.nodeRaw = append(d.nodeRaw, raw)
	return d.reloadNodes()
}

func (d *flowDoc) setNodeRaw(i int, raw json.RawMessage) error {
	if i < 0 || i >= len(d.nodeRaw) {
		return fmt.Errorf("节点下标越界: %d", i)
	}
	d.nodeRaw[i] = raw
	return d.reloadNodes()
}

func (d *flowDoc) removeNodeAt(i int) error {
	if i < 0 || i >= len(d.nodeRaw) {
		return fmt.Errorf("节点下标越界: %d", i)
	}
	d.nodeRaw = append(d.nodeRaw[:i], d.nodeRaw[i+1:]...)
	return d.reloadNodes()
}

// newNodeID 与画布一致：5 位数字串，且避开节点 id / 分支子分支 id / 边 id。
func (d *flowDoc) newNodeID() string {
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%05d", rand.IntN(100000))
		if !d.idTaken(id) {
			return id
		}
	}
	return fmt.Sprintf("node-%d", len(d.nodes)+1)
}

func (d *flowDoc) node(id string) (*flowNodeMeta, bool) {
	for i := range d.nodes {
		if d.nodes[i].ID == id {
			return &d.nodes[i], true
		}
	}
	return nil, false
}

func (d *flowDoc) edgeIndex(id string) int {
	for i := range d.edges {
		if d.edges[i].ID == id {
			return i
		}
	}
	return -1
}

func (d *flowDoc) removeEdge(id string) bool {
	i := d.edgeIndex(id)
	if i < 0 {
		return false
	}
	d.edges = append(d.edges[:i], d.edges[i+1:]...)
	return true
}

// idTaken 新边 id 不能与节点 id、分支子分支 id、边 id 撞车（与画布 generateUniqueId 同口径）。
func (d *flowDoc) idTaken(id string) bool {
	for i := range d.nodes {
		if d.nodes[i].ID == id {
			return true
		}
		for _, c := range d.nodes[i].Data.Config.Children {
			if c.ID == id {
				return true
			}
		}
	}
	return d.edgeIndex(id) >= 0
}

// newEdgeID 画布生成的是 5 位数字串，这里保持一致，撞车就重摇。
func (d *flowDoc) newEdgeID() string {
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%05d", rand.IntN(100000))
		if !d.idTaken(id) {
			return id
		}
	}
	return fmt.Sprintf("edge-%d", len(d.edges)+1)
}

// splitHandle 拆 source<owner><pos> / target<owner><pos>：位置固定在末尾，
// 先剥位置、剩下的是 owner —— 这样 owner 里含 left/right 之类的词也不会拆错。
func splitHandle(raw, prefix string) (owner, pos string, err error) {
	if !strings.HasPrefix(raw, prefix) {
		return "", "", fmt.Errorf("handle %q 必须以 %q 开头（形如 %s<节点id><left|right|top|bottom>）", raw, prefix, prefix)
	}
	body := strings.TrimPrefix(raw, prefix)
	for _, p := range []string{"left", "right", "top", "bottom"} {
		if strings.HasSuffix(body, p) {
			if head := strings.TrimSuffix(body, p); head != "" {
				return head, p, nil
			}
			break
		}
	}
	return "", "", fmt.Errorf("handle %q 缺少位置后缀（%s<节点id>left|right|top|bottom）", raw, prefix)
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// checkConnectable 节点这一侧能不能连线：function 一个 handle 都没有，
// 终止节点没有出边、起点没有入边，其余按 nodeHandleRules 判断。
func checkConnectable(n *flowNodeMeta, side string) error {
	if n.Type == "function" {
		return fmt.Errorf("节点 %q 是 function（函数）节点：没有任何入边也没有出边，不能参与连线", n.ID)
	}
	rule, ok := nodeHandleRules[n.Type]
	if !ok {
		return fmt.Errorf("节点 %q 的类型 %q 不认识（能连线的类型见节点总览），不能连线", n.ID, n.Type)
	}
	if side == "source" && len(rule.out) == 0 {
		return fmt.Errorf("节点 %q（%s）没有出边，不能作为边的起点", n.ID, n.Type)
	}
	if side == "target" && len(rule.in) == 0 {
		return fmt.Errorf("节点 %q（%s）没有入边，不能作为边的终点", n.ID, n.Type)
	}
	return nil
}

// sourcePositions 解析 source_handle 的 owner：可能是节点自己（默认分支口），
// 也可能是 branch / intent 的 children[].id（子分支出边）。
func (d *flowDoc) sourcePositions(n *flowNodeMeta, owner string) ([]string, error) {
	if owner == n.ID {
		return nodeHandleRules[n.Type].out, nil
	}
	if n.hasChild(owner) {
		return branchChildOut, nil
	}
	return nil, fmt.Errorf("source_handle 里的 owner %q 既不是节点 %q 自己，也不是它的分支子节点 id；"+
		"分支子分支的写法是 source<config.children[].id>left|right，默认分支才是 source<节点自己的id>left|right|bottom",
		owner, n.ID)
}

// edgeValidation 校验通过后的附加动作。
type edgeValidation struct {
	// replaced 目标节点上「入口位置不同」的旧入边：按画布语义，新边顶掉它
	replaced *flowEdge
}

// validateEdge 按画布规则校验一条待落库的边；skipEdgeID 是 update 时排除的自身。
func (d *flowDoc) validateEdge(e flowEdge, skipEdgeID string) (*edgeValidation, error) {
	src, ok := d.node(e.Source)
	if !ok {
		return nil, fmt.Errorf("source 节点 %q 不存在：id 从 read_flow 的 diagram.nodes[].id 里取，不要自己编", e.Source)
	}
	dst, ok := d.node(e.Target)
	if !ok {
		return nil, fmt.Errorf("target 节点 %q 不存在：id 从 read_flow 的 diagram.nodes[].id 里取", e.Target)
	}
	if e.Source == e.Target {
		return nil, fmt.Errorf("不能把节点 %q 连到它自己", e.Source)
	}
	if err := checkConnectable(src, "source"); err != nil {
		return nil, err
	}
	if err := checkConnectable(dst, "target"); err != nil {
		return nil, err
	}
	if src.ParentNode != dst.ParentNode {
		return nil, fmt.Errorf("不能跨父节点连接：%q 的 parent_node=%q，%q 的是 %q（子节点只能连同一个容器内的节点）",
			src.ID, src.ParentNode, dst.ID, dst.ParentNode)
	}

	// ---- 出边 handle ----
	if e.SourceHandle == "" {
		sug := suggestHandles(src, dst)
		return nil, fmt.Errorf("source_handle 必填：形如 source<发出节点id><left|right|bottom>（例 source46004right）；"+
			"branch / intent 的子分支是 source<children[].id>left|right。"+
			"按两个节点的位置（%s）建议 source_handle=%s、target_handle=%s",
			sug.Reason, sug.SourceHandle, sug.TargetHandle)
	}
	owner, pos, err := splitHandle(e.SourceHandle, "source")
	if err != nil {
		return nil, err
	}
	allowed, err := d.sourcePositions(src, owner)
	if err != nil {
		return nil, err
	}
	if !containsStr(allowed, pos) {
		return nil, fmt.Errorf("节点 %q（%s，owner=%q）的出边位置只能是 %s，收到 %q",
			src.ID, src.Type, owner, strings.Join(allowed, "/"), pos)
	}

	// ---- 入边 handle ----
	if e.TargetHandle == "" {
		sug := suggestHandles(src, dst)
		return nil, fmt.Errorf("target_handle 必填：形如 target<目标节点id><left|top|right|bottom>（例 target45063top），不能省。"+
			"按两个节点的位置（%s）建议 source_handle=%s、target_handle=%s",
			sug.Reason, sug.SourceHandle, sug.TargetHandle)
	}
	tOwner, tPos, err := splitHandle(e.TargetHandle, "target")
	if err != nil {
		return nil, err
	}
	if tOwner != dst.ID {
		return nil, fmt.Errorf("target_handle 的 owner 必须是目标节点 %q；收到 %q（分支子分支只有出边，没有入边）", dst.ID, tOwner)
	}
	if in := nodeHandleRules[dst.Type].in; !containsStr(in, tPos) {
		return nil, fmt.Errorf("节点 %q（%s）的入边位置只能是 %s，收到 %q", dst.ID, dst.Type, strings.Join(in, "/"), tPos)
	}

	// ---- 同一对节点最多一条边 ----
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID {
			continue
		}
		if old.Source == e.Source && old.Target == e.Target {
			return nil, fmt.Errorf("节点 %q → %q 之间已经有边（id=%s）：两个节点之间最多一条边", e.Source, e.Target, old.ID)
		}
	}

	// ---- 一个出口只能一条边 ----
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID {
			continue
		}
		if old.Source == e.Source && old.SourceHandle == e.SourceHandle {
			return nil, fmt.Errorf("节点 %q 的出口 %s 已经有边（id=%s）：一个出口只能连一条边", e.Source, e.SourceHandle, old.ID)
		}
	}

	// ---- 一个节点只能一条入边 ----
	// 同一入口位置 → 硬拦；换成另一个入口位置 → 顶掉旧边（画布 onConnect 就是这个语义）。
	var replaced *flowEdge
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID || old.Target != e.Target {
			continue
		}
		if old.TargetHandle == e.TargetHandle {
			return nil, fmt.Errorf("节点 %q 的入口 %s 已经有边（id=%s）：一个节点只能有一条入边，"+
				"同一入口不能重复连；要换来源请用 update_edge，或换成另一个入口位置", e.Target, e.TargetHandle, old.ID)
		}
		oldCopy := old
		replaced = &oldCopy
	}

	// ---- 不能成环（将要被删掉的边不参与判断）----
	replacedID := ""
	if replaced != nil {
		replacedID = replaced.ID
	}
	if d.wouldCycle(e.Source, e.Target, skipEdgeID, replacedID) {
		return nil, fmt.Errorf("会形成环（%q → %q 之后能绕回 %q）：流程图必须是有向无环图；"+
			"要重复执行请用 loop 节点，不要用回边", e.Source, e.Target, e.Source)
	}
	return &edgeValidation{replaced: replaced}, nil
}

// wouldCycle 从 target 顺着出边能不能回到 source。
func (d *flowDoc) wouldCycle(source, target, skipEdgeID, replacedID string) bool {
	seen := make(map[string]bool, len(d.nodes))
	var walk func(string) bool
	walk = func(id string) bool {
		if id == source {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		for i := range d.edges {
			e := d.edges[i]
			if e.ID == skipEdgeID || (replacedID != "" && e.ID == replacedID) {
				continue
			}
			if e.Source == id && walk(e.Target) {
				return true
			}
		}
		return false
	}
	return walk(target)
}

// ===== 工具的公共部分 =====

func edgeView(e flowEdge) map[string]any {
	return map[string]any{
		"id":            e.ID,
		"source":        e.Source,
		"target":        e.Target,
		"source_handle": e.SourceHandle,
		"target_handle": e.TargetHandle,
	}
}

// loadFlowDoc 定位工作流并解析 diagram（ref 同时当 id 与名称，由 manager 决定命中哪个）。
func loadFlowDoc(ctx context.Context, c *Context, ref string) (*flowDoc, *FlowDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, errors.New("flow 不能为空（工作流 id 或名称）")
	}
	if c.Flows == nil {
		return nil, nil, errors.New("flow manager not available")
	}
	detail, err := c.Flows.Get(ctx, ref, ref)
	if err != nil {
		return nil, nil, err
	}
	if detail == nil {
		return nil, nil, fmt.Errorf("工作流 %q 不存在；先用 list_flows 确认名字", ref)
	}
	doc, err := parseFlowDoc(detail.Diagram)
	if err != nil {
		return nil, nil, err
	}
	return doc, detail, nil
}

// saveDiagram 把整份 diagram 写回（只走 diagram 一列，不动名称/限额/断点）。
func saveDiagram(ctx context.Context, c *Context, detail *FlowDetail, doc *flowDoc) (*FlowInfo, error) {
	raw, err := doc.marshal()
	if err != nil {
		return nil, err
	}
	return c.Flows.UpdateDiagram(ctx, detail.ID, detail.Name, raw)
}

// saveFlowDoc 落库并拼回执：回显边本身 + 剩余条数 + 被顶掉的旧边（如果有）。
func saveFlowDoc(ctx context.Context, c *Context, detail *FlowDetail, doc *flowDoc, e flowEdge, v *edgeValidation) (any, error) {
	info, err := saveDiagram(ctx, c, detail, doc)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"flow_id":    info.ID,
		"flow_name":  info.Name,
		"edge":       edgeView(e),
		"edge_count": len(doc.edges),
	}
	if v != nil && v.replaced != nil {
		out["replaced_edge"] = edgeView(*v.replaced)
		out["note"] = "目标节点的入口位置不同：旧入边已被新边顶掉（一个节点只能有一条入边）"
	}
	return out, nil
}

// argStringSet 取字符串参数并区分「没传」与「传了空串」—— update 要靠它判断改了哪个字段。
func argStringSet(args map[string]any, key string) (string, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", false
	}
	s, _ := raw.(string)
	return strings.TrimSpace(s), true
}

// ===== create_edge =====

type createEdgeTool struct{}

func (t *createEdgeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_edge",
		Category: CategoryFlowAdmin,
		Description: "在一个**已有**的工作流上连一条边（只改 edges，不建节点、不改 config）。" +
			"source_handle 形如 source<发出节点id><位置>（例 source46004right），" +
			"target_handle 形如 target<目标节点id><位置>（例 target45063top）。" +
			edgeRuleText +
			"返回落库后的 edge 与剩余 edge_count；发生顶替时回 replaced_edge。" +
			"缺 handle 时按位置把该填的值直接写进报错；handle 与位置明显不匹配时额外回 suggested_edge（reason + 建议的两个 handle），照它改能少绕线。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":          map[string]any{"type": "string", "description": flowRefHint},
				"source":        map[string]any{"type": "string", "description": "边的发出节点 id（read_flow 的 diagram.nodes[].id）"},
				"target":        map[string]any{"type": "string", "description": "边的到达节点 id"},
				"source_handle": map[string]any{"type": "string", "description": "发出节点的出口，形如 source<节点id><left|right|bottom>，必填"},
				"target_handle": map[string]any{"type": "string", "description": "到达节点的入口，形如 target<节点id><left|top|right|bottom>，必填"},
			},
			"required": []any{"flow", "source", "target", "source_handle", "target_handle"},
		},
	}
}

func (t *createEdgeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	doc, detail, err := loadFlowDoc(ctx, c, argString(args, "flow"))
	if err != nil {
		return nil, err
	}
	e := flowEdge{
		Source:       strings.TrimSpace(argString(args, "source")),
		Target:       strings.TrimSpace(argString(args, "target")),
		SourceHandle: strings.TrimSpace(argString(args, "source_handle")),
		TargetHandle: strings.TrimSpace(argString(args, "target_handle")),
	}
	if e.Source == "" || e.Target == "" {
		return nil, errors.New("source / target 都不能为空（节点 id，从 read_flow 的 diagram.nodes[].id 里取）")
	}
	v, err := doc.validateEdge(e, "")
	if err != nil {
		return nil, err
	}
	if v.replaced != nil {
		doc.removeEdge(v.replaced.ID)
	}
	e.ID = doc.newEdgeID()
	doc.edges = append(doc.edges, e)
	res, err := saveFlowDoc(ctx, c, detail, doc, e, v)
	if err != nil {
		return nil, err
	}
	// handle 与两个节点的相对位置不匹配：边已经落库（合法），但线会绕，回一条建议。
	if src, ok := doc.node(e.Source); ok {
		if dst, ok := doc.node(e.Target); ok {
			if sug := suggestHandles(src, dst); sug.SourceHandle != e.SourceHandle || sug.TargetHandle != e.TargetHandle {
				if m, ok := res.(map[string]any); ok {
					m["suggested_edge"] = map[string]any{
						"reason":        sug.Reason,
						"source_handle": sug.SourceHandle,
						"target_handle": sug.TargetHandle,
					}
					m["handle_hint"] = "当前 handle 与两个节点的位置不太匹配（线会绕）：" + sug.Reason +
						"，建议 source_handle=" + sug.SourceHandle + "、target_handle=" + sug.TargetHandle +
						"（可用 update_edge 改）"
				}
			}
		}
	}
	return res, nil
}

// ===== update_edge =====

type updateEdgeTool struct{}

func (t *updateEdgeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_edge",
		Category: CategoryFlowAdmin,
		Description: "改一条已存在的边（按 edge_id，从 read_flow 的 diagram.edges[].id 取）：" +
			"可改 source / target / source_handle / target_handle，至少给一个。" +
			"改了 source 必须同时给 source_handle，改了 target 必须同时给 target_handle —— handle 里嵌的是节点 id。" +
			"校验规则与 create_edge 完全相同：" + edgeRuleText +
			"返回更新后的 edge 与 edge_count；换入口顶掉旧入边时回 replaced_edge。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":          map[string]any{"type": "string", "description": flowRefHint},
				"edge_id":       map[string]any{"type": "string", "description": "要改的边 id（read_flow 的 diagram.edges[].id）"},
				"source":        map[string]any{"type": "string", "description": "可选：新的发出节点 id"},
				"target":        map[string]any{"type": "string", "description": "可选：新的到达节点 id"},
				"source_handle": map[string]any{"type": "string", "description": "可选：新的出口，形如 source<节点id><left|right|bottom>"},
				"target_handle": map[string]any{"type": "string", "description": "可选：新的入口，形如 target<节点id><left|top|right|bottom>"},
			},
			"required": []any{"flow", "edge_id"},
		},
	}
}

func (t *updateEdgeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
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
		return nil, fmt.Errorf("找不到 edge_id=%q 的边；先 read_flow 核对 diagram.edges，不要拿节点 id 当边 id", edgeID)
	}
	cur := doc.edges[idx]

	changed := false
	if s, ok := argStringSet(args, "source"); ok {
		if s == "" {
			return nil, errors.New("source 不能是空串")
		}
		cur.Source, changed = s, true
	}
	if s, ok := argStringSet(args, "target"); ok {
		if s == "" {
			return nil, errors.New("target 不能是空串")
		}
		cur.Target, changed = s, true
	}
	if s, ok := argStringSet(args, "source_handle"); ok {
		if s == "" {
			return nil, errors.New("source_handle 不能是空串（它必填）")
		}
		cur.SourceHandle, changed = s, true
	}
	if s, ok := argStringSet(args, "target_handle"); ok {
		if s == "" {
			return nil, errors.New("target_handle 不能是空串（它必填）")
		}
		cur.TargetHandle, changed = s, true
	}
	if !changed {
		return nil, errors.New("没有要改的字段：source / target / source_handle / target_handle 至少给一个")
	}
	if _, ok := argStringSet(args, "source"); ok {
		if _, hasHandle := argStringSet(args, "source_handle"); !hasHandle {
			return nil, errors.New("改了 source 就要一起给 source_handle：handle 里嵌的是发出节点的 id")
		}
	}
	if _, ok := argStringSet(args, "target"); ok {
		if _, hasHandle := argStringSet(args, "target_handle"); !hasHandle {
			return nil, errors.New("改了 target 就要一起给 target_handle：handle 里嵌的是到达节点的 id")
		}
	}

	v, err := doc.validateEdge(cur, edgeID)
	if err != nil {
		return nil, err
	}
	if v.replaced != nil {
		doc.removeEdge(v.replaced.ID)
	}
	// 删过边之后下标会变，按 id 重新定位
	doc.edges[doc.edgeIndex(edgeID)] = cur
	return saveFlowDoc(ctx, c, detail, doc, cur, v)
}

// ===== delete_edge =====

type deleteEdgeTool struct{}

func (t *deleteEdgeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_edge",
		Category: CategoryFlowAdmin,
		Description: "删除一条已存在的边（按 edge_id，从 read_flow 的 diagram.edges[].id 取），" +
			"返回被删的 edge 与剩余 edge_count；删错时照着返回体重建即可。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":    map[string]any{"type": "string", "description": flowRefHint},
				"edge_id": map[string]any{"type": "string", "description": "要删的边 id（read_flow 的 diagram.edges[].id）"},
			},
			"required": []any{"flow", "edge_id"},
		},
	}
}

func (t *deleteEdgeTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
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
	deleted := doc.edges[idx]
	doc.removeEdge(edgeID)

	raw, err := doc.marshal()
	if err != nil {
		return nil, err
	}
	info, err := c.Flows.UpdateDiagram(ctx, detail.ID, detail.Name, raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"flow_id":    info.ID,
		"flow_name":  info.Name,
		"deleted":    edgeView(deleted),
		"edge_count": len(doc.edges),
	}, nil
}
