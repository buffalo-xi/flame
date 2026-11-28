package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/shared/flowcheck"
)

// ===== Edge tools (create_edge / update_edge / delete_edge) =====
//
// Why a separate set: create_flow can only build "the main chain + branch sub-chains" and never reaches container children or
// any rewiring; letting the model write the whole diagram again would make it trip over the config / coordinates / handles of
// 20-odd node types. So only the "edge" layer is exposed here.
//
// The rules are strictly the same as the frontend canvas (onConnect in Flow.vue plus the Handle declarations in each
// *Node.vue): loosen one rule and the next drag on the canvas corrupts the graph; tighten one and structures the canvas can
// draw become unreachable.

// nodeHandleRule is the set of handle positions a node type allows.
type nodeHandleRule struct {
	in  []string // incoming edges (target_handle)
	out []string // outgoing edges (source_handle)
}

var (
	// ioNode is a one-in one-out execution node: incoming left/top, outgoing right/bottom
	ioNode = nodeHandleRule{in: []string{"left", "top"}, out: []string{"right", "bottom"}}
	// containerNode is a container / branch type: one extra incoming on the right and one extra outgoing on the left (needed
	// when a sub-chain joins back from the left)
	containerNode = nodeHandleRule{in: []string{"left", "top", "right"}, out: []string{"left", "right", "bottom"}}
	// sinkNode is a terminal node: incoming edges only
	sinkNode = nodeHandleRule{in: []string{"left", "top", "right", "bottom"}}
	// sourceOnlyNode is a start / container entry: outgoing edges only
	sourceOnlyNode = nodeHandleRule{out: []string{"left", "right", "bottom"}}
)

var nodeHandleRules = map[string]nodeHandleRule{
	"start": sourceOnlyNode, "entry": sourceOnlyNode,
	"llm": ioNode, "code": ioNode, "http": ioNode, "mcp": ioNode, "rpc": ioNode,
	"assign": ioNode, "memory": ioNode, "result": ioNode, "progress": ioNode,
	"call": ioNode, "layout": ioNode, "knowledge": ioNode,
	"loop": containerNode, "process": containerNode, "branch": containerNode, "intent": containerNode,
	"exit": sinkNode, "break": sinkNode, "continue": sinkNode, "return": sinkNode,
	// raise never succeeds, so it only ever takes an incoming edge: a successor would be unreachable by construction.
	"raise": sinkNode,
	// function has no handle at all (blocked explicitly in validateEdge); comment is a pure annotation and is not wired either
	"function": {}, "comment": {},
}

// branchChildOut: the sub-branches of branch / intent (children[].id) only have the two horizontal outgoing handles
var branchChildOut = []string{"left", "right"}

// ===== Suggesting a handle by position =====
//
// The wiring direction follows where the source node sits relative to the target node: on the same row it uses the horizontal
// ends, and the larger the vertical offset the more it leans to the vertical ends. The rule is only a suggestion (any legal
// combination can be wired on the canvas), but following it gives the shortest lines with the fewest crossings; when the model
// picks a handle out of thin air, the tool echoes this result back as a hint.
//
// On the canvas y grows downward, and the thresholds are chosen for a node height of 32 and a main-chain row pitch of about 140.
const (
	// handleAlignTol is the tolerance for "the same row / the same column" (one node height plus a little slack)
	handleAlignTol = 40
	// handleFarTol: beyond this offset it counts as "much higher / much lower" and switches to the vertical ends
	handleFarTol = 200
)

// handleSuggest is a wiring suggested from the node positions (with a one-line explanation of the direction).
type handleSuggest struct {
	SourceHandle string
	TargetHandle string
	Reason       string
}

// handlePreferOrder is the fallback order when the wanted position is unavailable: e.g. start has no top outgoing handle, so a
// suggested top degrades naturally to left / bottom.
var handlePreferOrder = map[string][]string{
	"left":   {"left", "top", "right", "bottom"},
	"right":  {"right", "top", "bottom", "left"},
	"top":    {"top", "left", "right", "bottom"},
	"bottom": {"bottom", "right", "left", "top"},
}

// suggestHandlePositions computes the two end positions from the relative position (already degraded to the positions the node
// types actually offer).
// The suggestion in create_edge and the handle backfill in create_flow share this single decision, so there are not two rule
// sets.
//
//	roughly the same row (|Δy| ≤ 40)   source on the left -> right->left; on the right -> left->right
//	stacked vertically (|Δx| ≤ 40)     target below -> bottom->top; above -> top->bottom
//	much higher (Δy > 200)             bottom->top (dropping vertically)
//	much lower (Δy < -200)             source on the left -> top->left; on the right -> top->right
//	notably higher (Δy 40~200)         target at the lower right -> right->top; lower left -> left->top
//	notably lower (Δy -40~-200)        target at the upper right -> right->left; upper left -> left->right
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
			srcPos, dstPos, reason = "right", "left", "same row, target on the right"
		} else {
			srcPos, dstPos, reason = "left", "right", "same row, target on the left"
		}
	case adx <= handleAlignTol:
		if dy > 0 {
			srcPos, dstPos, reason = "bottom", "top", "stacked right below"
		} else {
			srcPos, dstPos, reason = "top", "bottom", "stacked right above"
		}
	case dy > handleFarTol:
		srcPos, dstPos, reason = "bottom", "top", "target far below"
	case dy < -handleFarTol:
		if dx > 0 {
			srcPos, dstPos, reason = "top", "left", "target far above"
		} else {
			srcPos, dstPos, reason = "top", "right", "target far above and to the left"
		}
	case dy > 0:
		if dx > 0 {
			srcPos, dstPos, reason = "right", "top", "target at the lower right"
		} else {
			srcPos, dstPos, reason = "left", "top", "target at the lower left"
		}
	default:
		if dx > 0 {
			srcPos, dstPos, reason = "right", "left", "target at the upper right"
		} else {
			srcPos, dstPos, reason = "left", "right", "target at the upper left"
		}
	}

	wantSrc, wantDst := srcPos, dstPos
	srcPos = pickHandle(nodeHandleRules[srcType].out, handlePreferOrder[wantSrc])
	dstPos = pickHandle(nodeHandleRules[dstType].in, handlePreferOrder[wantDst])
	// The type does not offer that position (e.g. no node has a top outgoing handle): say where it fell back to, so the caller
	// does not believe the suggestion was applied as is.
	if srcPos != wantSrc {
		reason += fmt.Sprintf("; %s has no %s outgoing handle, falling back to %s", srcType, wantSrc, srcPos)
	}
	if dstPos != wantDst {
		reason += fmt.Sprintf("; %s has no %s incoming handle, falling back to %s", dstType, wantDst, dstPos)
	}
	return srcPos, dstPos, reason
}

// suggestHandles suggests a pair of handles from the two nodes' relative positions (echoed back by create_edge).
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

// handleLayoutAdvice is the position -> handle rule shown to the model (shared by the create_edge description and its errors).
const handleLayoutAdvice = "**Pick the handle by position** (Δ = target coords - source coords, y growing downward): " +
	"roughly the same row (|Δy| ≤ 40) → the source's right to the target's left (reverse when the target is to the left: left→right); " +
	"notably higher (Δy 40~200, target at the lower right) → right→top (lower left uses left→top); " +
	"much higher (Δy > 200, target far below) → bottom→top; " +
	"notably lower (Δy -200~-40, target at the upper right) → right→left (upper left uses left→right); " +
	"much lower (Δy < -200, target far above) → top→left (target to the left uses top→right); " +
	"stacked directly (|Δx| ≤ 40) → bottom→top downward, top→bottom upward. " +
	"Note: **no node currently has a top outgoing edge**, and bottom incoming edges exist only on terminal nodes (exit / break / continue / return), " +
	"so \"much lower / top→bottom\" can only fall back to a position the type supports; the tool writes the position it actually fell back to into suggested_edge.reason."

// edgeRuleText is the single source of the wiring rules (create_edge and update_edge share the same copy).
// The skill doc references/edges.md no longer enumerates these rules - it only carries shapes / examples / troubleshooting - so
// that "the doc says one thing, the tool does another" cannot mislead the model.
const edgeRuleText = "**Handle shape**: source_handle = source + handle owner + position, target_handle = target + target node id + position. " +
	"The owner is normally the source node's own id; **sub-branch outgoing edges** of branch / intent use config.children[].id (positions left / right only), " +
	"and only the default branch uses the node's own id. **Both handles are required and have no default**: a wrong position does not report \"cannot render\" — " +
	"it silently fails to route at run time when following handles, so it is blocked hard here." +
	"**Positions available per type** (incoming / outgoing): " +
	"llm / code / http / mcp / rpc / assign / memory / result / progress / call / layout / knowledge → left|top / right|bottom; " +
	"start / entry → none / left|right|bottom; " +
	"loop / process / branch / intent → left|top|right / left|right|bottom; " +
	"exit / break / continue / return → left|top|right|bottom / none; " +
	"function / comment → cannot be connected (rejected as soon as either end is one of them). " +
	"**Hard rules**: no self-loops; both nodes must share the same parent (children of different containers cannot be connected); at most one edge per node pair; " +
	"one outlet allows one edge only — an outlet is source<owner> **without** its position, so left / right / bottom of one node, and left / right of one branch child, are the same outlet " +
	"(create_edge refuses a second edge there; update_edge moving an edge onto an occupied outlet removes the old edge and returns replaced_out_edge); " +
	"a node allows one incoming edge only (connecting the same inlet twice is an error; connecting a different inlet displaces the old incoming edge and returns replaced_edge); no cycles." +
	handleLayoutAdvice

// flowEdge is one edge in its stored form (the frontend also writes only these five keys when saving).
type flowEdge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"source_handle"`
	TargetHandle string `json:"target_handle"`
}

// flowNodeMeta: validation only needs these node fields; coordinates / sizes / extent stay untouched in the diagram.
type flowNodeMeta struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ParentNode string `json:"parent_node"`
	// Position is only used to place a new node somewhere non-overlapping (a child's coordinates are relative to its parent).
	Position struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"position"`
	Data struct {
		Label string `json:"label"`
		// Concurrent is the scope membership flag: a direct child of a process / loop that the body dispatches instead
		// of running inline.
		Concurrent bool `json:"concurrent"`
		Config     struct {
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

// flowDoc is one diagram: nodes / edges are kept as they are, one by one, and only the entries actually changed are rewritten.
//
// nodeRaw is a node's original JSON - coordinates, extent and frontend-private fields are never decoded or reordered, so that
// "the tool changed one edge and washed away every node's styling in the whole graph" cannot happen.
type flowDoc struct {
	top     map[string]json.RawMessage
	nodeRaw []json.RawMessage
	nodes   []flowNodeMeta
	edges   []flowEdge
	// idReserved holds ids handed out by nextID that are not in nodes / edges yet: one caller can allocate several at
	// once (a container plus its entry, branch children inside a pending config, an inserted edge pair).
	idReserved map[string]bool
}

func parseFlowDoc(raw []byte) (*flowDoc, error) {
	if len(raw) == 0 {
		return nil, errors.New("this workflow has no diagram yet (never saved from the canvas): create it with create_flow, or save once on the canvas")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("diagram is not a valid JSON object: %w", err)
	}
	d := &flowDoc{top: top}
	if nraw := top["nodes"]; len(nraw) > 0 {
		if err := json.Unmarshal(nraw, &d.nodeRaw); err != nil {
			return nil, fmt.Errorf("failed to parse diagram.nodes: %w", err)
		}
	}
	if err := d.reloadNodes(); err != nil {
		return nil, err
	}
	if eraw := top["edges"]; len(eraw) > 0 {
		if err := json.Unmarshal(eraw, &d.edges); err != nil {
			return nil, fmt.Errorf("failed to parse diagram.edges: %w", err)
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

// reloadNodes re-parses the node metadata; it must be called after nodes are added, removed or changed, otherwise later checks
// (parent scope, branch sub-branches) still look at the stale snapshot.
func (d *flowDoc) reloadNodes() error {
	d.nodes = make([]flowNodeMeta, len(d.nodeRaw))
	for i := range d.nodeRaw {
		if err := json.Unmarshal(d.nodeRaw[i], &d.nodes[i]); err != nil {
			return fmt.Errorf("failed to parse diagram.nodes[%d]: %w", i, err)
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
		return fmt.Errorf("node index out of range: %d", i)
	}
	d.nodeRaw[i] = raw
	return d.reloadNodes()
}

func (d *flowDoc) removeNodeAt(i int) error {
	if i < 0 || i >= len(d.nodeRaw) {
		return fmt.Errorf("node index out of range: %d", i)
	}
	d.nodeRaw = append(d.nodeRaw[:i], d.nodeRaw[i+1:]...)
	return d.reloadNodes()
}

// newNodeID matches the canvas: an increasing sequence written as a plain number, so a new node id is the largest id
// already in the document plus one (nodes, branch sub-branch ids and edge ids share the one sequence).
func (d *flowDoc) newNodeID() string {
	return d.nextID()
}

// nextID hands out the next free id of the shared sequence. Ids are reserved as they are handed out because a caller can
// allocate several before writing any of them into the document.
func (d *flowDoc) nextID() string {
	if d.idReserved == nil {
		d.idReserved = make(map[string]bool)
	}
	for next := d.highestID() + 1; ; next++ {
		id := strconv.Itoa(next)
		if !d.idTaken(id) && !d.idReserved[id] {
			d.idReserved[id] = true
			return id
		}
	}
}

// highestID is the largest numeric id in use: node ids, branch sub-branch ids, edge ids and the ids already handed out in
// this call. Anything that is not a plain non-negative integer (the canvas's default start node is "1", older flows may
// carry other shapes) takes no part in the sequence.
func (d *flowDoc) highestID() int {
	highest := 0
	bump := func(id string) {
		if n, err := strconv.Atoi(strings.TrimSpace(id)); err == nil && n > highest {
			highest = n
		}
	}
	for i := range d.nodes {
		bump(d.nodes[i].ID)
		for _, c := range d.nodes[i].Data.Config.Children {
			bump(c.ID)
		}
	}
	for _, e := range d.edges {
		bump(e.ID)
	}
	for id := range d.idReserved {
		bump(id)
	}
	return highest
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

// idTaken: a new edge id must not collide with a node id, a branch sub-branch id or an edge id (the same rule as the canvas's
// generateUniqueId).
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

// newEdgeID: edge ids share the node sequence, exactly like the canvas.
func (d *flowDoc) newEdgeID() string {
	return d.nextID()
}

// splitHandle splits source<owner><pos> / target<owner><pos>: the position is fixed at the end, so strip the position first and
// what remains is the owner - that way an owner containing words like left/right is not split wrongly.
func splitHandle(raw, prefix string) (owner, pos string, err error) {
	if !strings.HasPrefix(raw, prefix) {
		return "", "", fmt.Errorf("handle %q must start with %q (shape: %s<node id><left|right|top|bottom>)", raw, prefix, prefix)
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
	return "", "", fmt.Errorf("handle %q has no position suffix (%s<node id>left|right|top|bottom)", raw, prefix)
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// checkConnectable decides whether one side of a node can be wired: function has no handle at all, terminal nodes have no
// outgoing handle and start nodes no incoming one, and the rest follows nodeHandleRules.
func checkConnectable(n *flowNodeMeta, side string) error {
	if n.Type == "function" {
		return fmt.Errorf("node %q is a function node: it has no incoming and no outgoing handle and takes no edges", n.ID)
	}
	rule, ok := nodeHandleRules[n.Type]
	if !ok {
		return fmt.Errorf("node %q has unknown type %q (see the node overview for wireable types) and cannot be wired", n.ID, n.Type)
	}
	if side == "source" && len(rule.out) == 0 {
		return fmt.Errorf("node %q (%s) has no outgoing handle and cannot be the source of an edge", n.ID, n.Type)
	}
	if side == "target" && len(rule.in) == 0 {
		return fmt.Errorf("node %q (%s) has no incoming handle and cannot be the target of an edge", n.ID, n.Type)
	}
	return nil
}

// sourcePositions resolves the owner in source_handle: it may be the node itself (the default branch outlet) or a
// children[].id of branch / intent (a sub-branch outlet).
func (d *flowDoc) sourcePositions(n *flowNodeMeta, owner string) ([]string, error) {
	if owner == n.ID {
		return nodeHandleRules[n.Type].out, nil
	}
	if n.hasChild(owner) {
		return branchChildOut, nil
	}
	return nil, fmt.Errorf("the owner %q in source_handle is neither node %q itself nor one of its sub-branch child ids; "+
		"a sub-branch is written source<config.children[].id>left|right, while the default branch is source<the node own id>left|right|bottom",
		owner, n.ID)
}

// edgeValidation is the extra action to take once validation passes.
type edgeValidation struct {
	// replaced is an old incoming edge of the target node on a "different inlet position": per the canvas semantics the new
	// edge displaces it
	replaced *flowEdge
	// replacedOut is an old outgoing edge of the source node on another position of the same outlet. Only update_edge hits
	// this: create_edge refuses a second edge, while moving an edge onto an occupied outlet displaces the old one - the same
	// way a different inlet displaces an incoming edge.
	replacedOut *flowEdge
}

// validateEdge validates one edge against the canvas rules; skipEdgeID is the edge itself, excluded on update.
func (d *flowDoc) validateEdge(e flowEdge, skipEdgeID string) (*edgeValidation, error) {
	src, ok := d.node(e.Source)
	if !ok {
		return nil, fmt.Errorf("source node %q does not exist: take the id from diagram.nodes[].id in read_flow and never invent one", e.Source)
	}
	dst, ok := d.node(e.Target)
	if !ok {
		return nil, fmt.Errorf("target node %q does not exist: take the id from diagram.nodes[].id in read_flow", e.Target)
	}
	if e.Source == e.Target {
		return nil, fmt.Errorf("cannot connect node %q to itself", e.Source)
	}
	if err := checkConnectable(src, "source"); err != nil {
		return nil, err
	}
	if err := checkConnectable(dst, "target"); err != nil {
		return nil, err
	}
	if src.ParentNode != dst.ParentNode {
		return nil, fmt.Errorf("cannot connect across parents: parent_node of %q is %q, of %q is %q (a child node can only connect to nodes in the same container)",
			src.ID, src.ParentNode, dst.ID, dst.ParentNode)
	}

	// ---- outgoing handle ----
	if e.SourceHandle == "" {
		sug := suggestHandles(src, dst)
		return nil, fmt.Errorf("source_handle is required: shape source<source node id><left|right|bottom> (e.g. source46004right); "+
			"sub-branches of branch / intent use source<children[].id>left|right. "+
			"based on the positions of the two nodes (%s), source_handle=%s and target_handle=%s are suggested",
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
		return nil, fmt.Errorf("node %q (%s, owner=%q) can only use %s as an outgoing position, got %q",
			src.ID, src.Type, owner, strings.Join(allowed, "/"), pos)
	}

	// ---- incoming handle ----
	if e.TargetHandle == "" {
		sug := suggestHandles(src, dst)
		return nil, fmt.Errorf("target_handle is required: shape target<target node id><left|top|right|bottom> (e.g. target45063top) and cannot be omitted. "+
			"based on the positions of the two nodes (%s), source_handle=%s and target_handle=%s are suggested",
			sug.Reason, sug.SourceHandle, sug.TargetHandle)
	}
	tOwner, tPos, err := splitHandle(e.TargetHandle, "target")
	if err != nil {
		return nil, err
	}
	if tOwner != dst.ID {
		return nil, fmt.Errorf("the owner in target_handle must be the target node %q, got %q (a sub-branch has only an outgoing handle, no incoming one)", dst.ID, tOwner)
	}
	if in := nodeHandleRules[dst.Type].in; !containsStr(in, tPos) {
		return nil, fmt.Errorf("node %q (%s) can only use %s as an incoming position, got %q", dst.ID, dst.Type, strings.Join(in, "/"), tPos)
	}

	// ---- at most one edge per node pair ----
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID {
			continue
		}
		if old.Source == e.Source && old.Target == e.Target {
			return nil, fmt.Errorf("nodes %q → %q already have an edge (id=%s): at most one edge between two nodes", e.Source, e.Target, old.ID)
		}
	}

	// ---- one outlet takes exactly one edge ----
	// An outlet is source<owner> without its position: onConnect's getHandleBase strips it, so left / right / bottom of one
	// node - and left / right of one branch child - are the same outlet. The exact same handle twice is always an error.
	// create_edge refuses a second edge on an occupied outlet; update_edge moves an edge onto it and displaces the old one,
	// exactly like a different inlet displaces an incoming edge.
	var replacedOut *flowEdge
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID || old.Source != e.Source {
			continue
		}
		sameHandle := old.SourceHandle == e.SourceHandle
		if !sameHandle {
			oldOwner, _, errOld := splitHandle(old.SourceHandle, "source")
			if errOld != nil || oldOwner != owner {
				continue
			}
		}
		if sameHandle || skipEdgeID == "" {
			if owner != src.ID {
				return nil, fmt.Errorf("child %q of %s node %q already routes to an edge (id=%s, %s): one child routes to exactly one next node, "+
					"left and right of the same child are the same outlet", owner, src.Type, src.ID, old.ID, old.SourceHandle)
			}
			return nil, fmt.Errorf("outlet %s of node %q already has an edge (id=%s): one outlet takes exactly one edge "+
				"(an outlet is source<owner> without its position, so left / right / bottom of the same node are one outlet); "+
				"delete edge %s first, or rewire this one with update_edge", e.SourceHandle, e.Source, old.ID, old.ID)
		}
		oldCopy := old
		replacedOut = &oldCopy
	}

	// ---- one node takes exactly one incoming edge ----
	// Same inlet position -> hard block; a different inlet position -> displace the old edge (exactly what the canvas onConnect does).
	var replaced *flowEdge
	for i := range d.edges {
		old := d.edges[i]
		if old.ID == skipEdgeID || old.Target != e.Target {
			continue
		}
		if old.TargetHandle == e.TargetHandle {
			return nil, fmt.Errorf("inlet %s of node %q already has an edge (id=%s): a node takes exactly one incoming edge, "+
				"an inlet cannot be wired twice; use update_edge to change the source, or pick another inlet position", e.Target, e.TargetHandle, old.ID)
		}
		oldCopy := old
		replaced = &oldCopy
	}

	// ---- no cycles (edges about to be removed do not take part) ----
	removed := []string{skipEdgeID}
	if replaced != nil {
		removed = append(removed, replaced.ID)
	}
	if replacedOut != nil {
		removed = append(removed, replacedOut.ID)
	}
	if d.wouldCycle(e.Source, e.Target, removed...) {
		return nil, fmt.Errorf("this creates a cycle (%q → %q can still reach back to %q): a flow graph must be a DAG; "+
			"use a loop node for repetition instead of a back edge", e.Source, e.Target, e.Source)
	}
	return &edgeValidation{replaced: replaced, replacedOut: replacedOut}, nil
}

// wouldCycle reports whether following outgoing edges from target can get back to source; removed holds the ids that are
// about to be deleted and therefore do not take part.
func (d *flowDoc) wouldCycle(source, target string, removed ...string) bool {
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
			if containsStr(removed, e.ID) {
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

// ===== Shared tool helpers =====

func edgeView(e flowEdge) map[string]any {
	return map[string]any{
		"id":            e.ID,
		"source":        e.Source,
		"target":        e.Target,
		"source_handle": e.SourceHandle,
		"target_handle": e.TargetHandle,
	}
}

// loadFlowDoc locates a workflow and parses its diagram (ref serves as both id and name; the manager decides which one hits).
func loadFlowDoc(ctx context.Context, c *Context, ref string) (*flowDoc, *FlowDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, errors.New("flow must not be empty (workflow id or name)")
	}
	if c.Flows == nil {
		return nil, nil, errors.New("flow manager not available")
	}
	detail, err := c.Flows.Get(ctx, ref, ref)
	if err != nil {
		return nil, nil, err
	}
	if detail == nil {
		return nil, nil, fmt.Errorf("workflow %q does not exist: confirm the name with list_flows first", ref)
	}
	doc, err := parseFlowDoc(detail.Diagram)
	if err != nil {
		return nil, nil, err
	}
	return doc, detail, nil
}

// saveDiagram writes the whole diagram back (it touches the diagram column only, never the name / limits / checkpoints).
//
// A node that reads its own outputs is refused rather than warned about: config.inputs / config.args are resolved before
// the node runs, so such a reference can never be satisfied - and the model has no other feedback channel for it (the
// editor's red badge and the pre-save dialog are for humans, and validate_flow is a tool it may simply not call). Every
// other finding is left alone: a diagram under construction legitimately has unconnected nodes, missing entry edges and
// half-written configs, and refusing those would make the step-by-step node tools impossible to use.
//
// The refusal follows flowcheck's self_ref finding instead of re-deciding the rule: that finding already exempts a
// while / do_while loop condition, whose inputs are re-resolved every round (the documented accumulator, which runs).
func saveDiagram(ctx context.Context, c *Context, detail *FlowDetail, doc *flowDoc) (*FlowInfo, error) {
	raw, err := doc.marshal()
	if err != nil {
		return nil, err
	}
	if findings, cerr := flowcheck.Diagram(raw, sandboxScriptLangs()); cerr == nil {
		for _, f := range findings {
			if f.Severity != flowcheck.SeverityError || f.Code != flowcheck.CodeSelfRef {
				continue
			}
			return nil, fmt.Errorf(
				"refused: %s on node %s reads that same node's own outputs or locals, which do not exist before the node runs; %s",
				f.Path, f.NodeID, f.Hint)
		}
	}
	return c.Flows.UpdateDiagram(ctx, detail.ID, detail.Name, raw)
}

// saveFlowDoc persists and assembles the receipt: the edge itself + the remaining count + the displaced old edge, if any.
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
	var notes []string
	if v != nil && v.replaced != nil {
		out["replaced_edge"] = edgeView(*v.replaced)
		notes = append(notes, "the target node has a different inlet position: the old incoming edge was replaced (a node takes exactly one incoming edge)")
	}
	if v != nil && v.replacedOut != nil {
		out["replaced_out_edge"] = edgeView(*v.replacedOut)
		notes = append(notes, "the outlet is one outlet regardless of position: the old outgoing edge was replaced (one outlet takes exactly one edge)")
	}
	if len(notes) > 0 {
		out["note"] = strings.Join(notes, "; ")
	}
	return out, nil
}

// argStringSet takes a string argument while distinguishing "not passed" from "passed empty" - update relies on it to decide
// which field changed.
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
		Description: "Connect an edge on an **existing** workflow (only touches edges — creates no node and changes no config). " +
			"source_handle looks like source<source node id><position> (e.g. source46004right), " +
			"target_handle looks like target<target node id><position> (e.g. target45063top). " +
			edgeRuleText +
			"The result returns the stored edge and the remaining edge_count; when an edge was displaced it returns replaced_edge. " +
			"When a handle is missing, the error states exactly which value that position needs; when the handle obviously mismatches the position it also returns suggested_edge (reason + the two suggested handles) — following it avoids routing detours.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":          map[string]any{"type": "string", "description": flowRefHint},
				"source":        map[string]any{"type": "string", "description": "Edge source node id (diagram.nodes[].id from read_flow)"},
				"target":        map[string]any{"type": "string", "description": "Edge target node id"},
				"source_handle": map[string]any{"type": "string", "description": "Outlet of the source node, shaped source<node id><left|right|bottom>; required"},
				"target_handle": map[string]any{"type": "string", "description": "Inlet of the target node, shaped target<node id><left|top|right|bottom>; required"},
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
		return nil, errors.New("source / target must not be empty (node ids, taken from diagram.nodes[].id in read_flow)")
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
	// The handles do not match the two nodes' relative positions: the edge is already stored (it is legal), but the line will
	// loop, so return a suggestion.
	if src, ok := doc.node(e.Source); ok {
		if dst, ok := doc.node(e.Target); ok {
			if sug := suggestHandles(src, dst); sug.SourceHandle != e.SourceHandle || sug.TargetHandle != e.TargetHandle {
				if m, ok := res.(map[string]any); ok {
					m["suggested_edge"] = map[string]any{
						"reason":        sug.Reason,
						"source_handle": sug.SourceHandle,
						"target_handle": sug.TargetHandle,
					}
					m["handle_hint"] = "the current handles do not match the positions of the two nodes (the line will loop): " + sug.Reason +
						"; suggested source_handle=" + sug.SourceHandle + ", target_handle=" + sug.TargetHandle +
						" (change them with update_edge)"
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
		Description: "Change an existing edge (by edge_id, from diagram.edges[].id in read_flow): " +
			"you may change source / target / source_handle / target_handle, at least one of them. " +
			"Changing source requires source_handle as well, and changing target requires target_handle as well — the handle embeds the node id. " +
			"The validation rules are exactly those of create_edge: " + edgeRuleText +
			"The result returns the updated edge and edge_count; when a new inlet displaces an old incoming edge it returns replaced_edge.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":          map[string]any{"type": "string", "description": flowRefHint},
				"edge_id":       map[string]any{"type": "string", "description": "Edge id to change (diagram.edges[].id from read_flow)"},
				"source":        map[string]any{"type": "string", "description": "Optional: new source node id"},
				"target":        map[string]any{"type": "string", "description": "Optional: new target node id"},
				"source_handle": map[string]any{"type": "string", "description": "Optional: new outlet, shaped source<node id><left|right|bottom>"},
				"target_handle": map[string]any{"type": "string", "description": "Optional: new inlet, shaped target<node id><left|top|right|bottom>"},
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
		return nil, errors.New("edge_id must not be empty (diagram.edges[].id from read_flow)")
	}
	idx := doc.edgeIndex(edgeID)
	if idx < 0 {
		return nil, fmt.Errorf("no edge with edge_id=%q: check diagram.edges with read_flow first, and do not pass a node id as an edge id", edgeID)
	}
	cur := doc.edges[idx]

	changed := false
	if s, ok := argStringSet(args, "source"); ok {
		if s == "" {
			return nil, errors.New("source must not be an empty string")
		}
		cur.Source, changed = s, true
	}
	if s, ok := argStringSet(args, "target"); ok {
		if s == "" {
			return nil, errors.New("target must not be an empty string")
		}
		cur.Target, changed = s, true
	}
	if s, ok := argStringSet(args, "source_handle"); ok {
		if s == "" {
			return nil, errors.New("source_handle must not be an empty string (it is required)")
		}
		cur.SourceHandle, changed = s, true
	}
	if s, ok := argStringSet(args, "target_handle"); ok {
		if s == "" {
			return nil, errors.New("target_handle must not be an empty string (it is required)")
		}
		cur.TargetHandle, changed = s, true
	}
	if !changed {
		return nil, errors.New("nothing to change: give at least one of source / target / source_handle / target_handle")
	}
	if _, ok := argStringSet(args, "source"); ok {
		if _, hasHandle := argStringSet(args, "source_handle"); !hasHandle {
			return nil, errors.New("changing source requires source_handle too: the handle embeds the source node id")
		}
	}
	if _, ok := argStringSet(args, "target"); ok {
		if _, hasHandle := argStringSet(args, "target_handle"); !hasHandle {
			return nil, errors.New("changing target requires target_handle too: the handle embeds the target node id")
		}
	}

	v, err := doc.validateEdge(cur, edgeID)
	if err != nil {
		return nil, err
	}
	if v.replaced != nil {
		doc.removeEdge(v.replaced.ID)
	}
	if v.replacedOut != nil {
		doc.removeEdge(v.replacedOut.ID)
	}
	// Indices shift after an edge is removed, so locate it again by id
	doc.edges[doc.edgeIndex(edgeID)] = cur
	return saveFlowDoc(ctx, c, detail, doc, cur, v)
}

// ===== delete_edge =====

type deleteEdgeTool struct{}

func (t *deleteEdgeTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_edge",
		Category: CategoryFlowAdmin,
		Description: "Delete an existing edge (by edge_id, from diagram.edges[].id in read_flow); " +
			"returns the deleted edge and the remaining edge_count — if you deleted the wrong one, rebuild it from the returned body.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":    map[string]any{"type": "string", "description": flowRefHint},
				"edge_id": map[string]any{"type": "string", "description": "Edge id to delete (diagram.edges[].id from read_flow)"},
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
		return nil, errors.New("edge_id must not be empty (diagram.edges[].id from read_flow)")
	}
	idx := doc.edgeIndex(edgeID)
	if idx < 0 {
		return nil, fmt.Errorf("no edge with edge_id=%q: check diagram.edges with read_flow first", edgeID)
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
