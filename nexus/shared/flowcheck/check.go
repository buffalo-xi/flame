// Package flowcheck statically checks a workflow definition.
//
// The same rule set is used by three call sites that used to disagree: the agent tools (create_flow / create_node warning
// about incomplete config), the save boundary (duplicate labels, node quota) and the validate_flow tool. Keeping one
// implementation is the whole point: a second opinion is how "the check passed but the run failed" becomes normal.
//
// Scope is static and side-effect free: no network, no model, no script execution. Anything that needs real data (a
// connection being enabled, a template rendering without <no value>, the JSON-Schema arguments) belongs to run_flow.
//
// Severity is deliberate: Error means the definition cannot run as written, Warning means it will run but probably not
// do what the author meant. Flagging something that actually works erodes trust faster than missing something, so a
// Warning is only used where the executor really does nothing (an empty children list, an unreachable node).
package flowcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"flame/nexus/shared/flowdoc"
)

// Severity levels of a Finding.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Finding is one problem found in a workflow.
//
// NodeID and Path make it actionable: the caller can go straight to update_node(config_patch=...) instead of guessing
// which node or which key is wrong.
type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	NodeID   string `json:"node_id,omitempty"`
	NodeType string `json:"node_type,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// Known finding codes. Stable strings, so a client can key behaviour on them.
const (
	CodeShape           = "shape"            // a config block has the wrong JSON type (refused at creation)
	CodeIncomplete      = "incomplete"       // a required key is missing (warning at creation)
	CodeUnsupportedLang = "unsupported_lang" // the sandbox does not pool that language
	CodeDuplicateLabel  = "duplicate_label"  // labels must be unique (labels are how refs are written)
	CodeUnknownType     = "unknown_type"     // no executor and not a visual node
	CodeEdgeEndpoint    = "edge_endpoint"    // an edge points at a node that does not exist
	CodeCrossParent     = "cross_parent"     // an edge crosses a container boundary
	CodeMultiOutgoing   = "multi_outgoing"   // more than one outgoing edge on a single-edge node
	CodeContainerEntry  = "container_entry"  // a container has no (or several) entry children
	CodeContainerOut    = "container_out"    // a container has more than one outgoing edge
	CodeBranchEdge      = "branch_edge"      // a branch child has no outgoing edge to route to
	CodeCallTarget      = "call_target"      // call.target does not name a function node
	CodeWaitFork        = "wait_fork"        // wait.fork_node_id does not name a fork node
	CodeDanglingRef     = "dangling_ref"     // source_ref points at a node that does not exist
	CodeLoopSource      = "loop_source"      // each-mode source_key is not declared in inputs
	CodeUnreachable     = "unreachable"      // no incoming edge and not the start node
	CodeOnErrorAction   = "on_error_action"  // the node does not declare that action, so the runner ignores the block
	CodeOnErrorCode     = "on_error_code"    // a code the node cannot produce, or one that does not exist at all
	CodeOnErrorPost     = "on_error_post"    // post configured on a node that does not support it
)

// containerTypes hold child nodes (and bring an entry child in the frontend).
var containerTypes = map[string]bool{"loop": true, "process": true, "function": true}

// multiOutgoingTypes may legitimately have several outgoing edges (they route by handle).
var multiOutgoingTypes = map[string]bool{"branch": true, "intent": true}

// knownTypes are every node type the runtime understands: an executor plus the canvas-only kinds. A type outside this
// set is a dead node (the runner reports "no executor registered" only when it reaches it).
var knownTypes = map[string]bool{
	"llm": true, "code": true, "http": true, "mcp": true, "rpc": true,
	"branch": true, "intent": true, "loop": true, "process": true, "function": true,
	"call": true, "assign": true, "memory": true, "wait": true, "fork": true,
	"result": true, "progress": true, "break": true, "continue": true, "return": true,
	"exit": true, "layout": true, "comment": true,
	"start": true, "entry": true, "knowledge": true,
}

// Step checks one node's config.
//
// The shape rules are refused outright (err != nil): a behaviour block that is not an object cannot be rescued on the
// canvas either. A missing required key only produces a Warning, because starting from a minimal config and finishing
// it on the canvas is allowed - but a Warning is a TODO, not advice.
//
// langs is the sandbox's workflow script language list; nil means "unknown" (the sandbox is down or too old to report
// it) and the language check is skipped rather than guessed.
func Step(typ string, cfg map[string]any, langs []string) ([]Finding, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	var out []Finding
	add := func(severity, code, path, msg string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeType: typ, Path: path, Message: msg})
	}

	// A node's behaviour block must be an object.
	for _, key := range []string{"code", "http", "llm", "loop", "mcp", "rpc", "branch"} {
		v, exists := cfg[key]
		if !exists {
			continue
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, fmt.Errorf("step %s: config.%s must be an object", typ, key)
		}
	}
	// children only appear on branch / intent.
	if v, exists := cfg["children"]; exists {
		if _, ok := v.([]any); !ok {
			return nil, fmt.Errorf("step %s: config.children must be an array", typ)
		}
	}
	// loop.type decides which field is read later, so a wrong value guarantees a run-time error.
	if lc, ok := cfg["loop"].(map[string]any); ok {
		if raw, exists := lc["type"]; exists {
			s, _ := raw.(string)
			switch s {
			case "count", "each", "while", "do_while":
			default:
				return nil, fmt.Errorf("step %s: config.loop.type must be count / each / while / do_while, got %v", typ, raw)
			}
		}
	}

	switch typ {
	case "code":
		code, _ := cfg["code"].(map[string]any)
		if script, ok := code["script"].(string); !ok || strings.TrimSpace(script) == "" {
			add(SeverityWarning, CodeIncomplete, "code.script", "code step has no code.script and fails with script not found at run time")
		}
		if _, ok := cfg["outputs"].(map[string]any); !ok {
			add(SeverityWarning, CodeIncomplete, "outputs", "code step has no config.outputs: variables returned by the script are rejected as undeclared")
		}
	case "http":
		httpCfg, _ := cfg["http"].(map[string]any)
		if url, _ := httpCfg["url"].(string); strings.TrimSpace(url) == "" {
			add(SeverityWarning, CodeIncomplete, "http.url", "http step has no http.url and cannot send the request at run time")
		}
	case "llm":
		if _, ok := cfg["llm"].(map[string]any); !ok {
			add(SeverityWarning, CodeIncomplete, "llm", "llm step has no config.llm (model and messages) and fails with a config error at run time")
		}
	case "loop":
		if _, ok := cfg["loop"].(map[string]any); !ok {
			add(SeverityWarning, CodeIncomplete, "loop", "loop step has no config.loop and fails with loop config not found at run time")
		}
	case "branch", "intent":
		if children, ok := cfg["children"].([]any); !ok || len(children) == 0 {
			add(SeverityWarning, CodeIncomplete, "children", typ+" step has empty config.children: there is no branch to take at run time")
		}
	case "call":
		if target, _ := cfg["target"].(string); strings.TrimSpace(target) == "" {
			add(SeverityWarning, CodeIncomplete, "target", "call step has no config.target (must be the id of a function node in the same flow)")
		}
	case "wait":
		if forkID, _ := cfg["fork_node_id"].(string); strings.TrimSpace(forkID) == "" {
			add(SeverityWarning, CodeIncomplete, "fork_node_id", "wait step has no config.fork_node_id (the concurrency group to wait on)")
		}
	}

	// Language fields are checked against the sandbox's "workflow script languages" list: a wrong one is invisible when
	// saving and on the canvas, and only fails at run time with unsupported_lang.
	if langs != nil {
		for _, lf := range langFields(typ, cfg) {
			// template is rendered in pure Go and does not use the sandbox's code pool, but it is legal for result /
			// progress.
			if lf.lang == "template" || contains(langs, lf.lang) {
				continue
			}
			add(SeverityWarning, CodeUnsupportedLang, lf.path, fmt.Sprintf(
				"step %s has %s=%s, which is not among the sandbox's current workflow script languages (%s); it will fail with unsupported_lang at run time",
				typ, lf.path, lf.lang, strings.Join(langs, ", ")))
		}
	}

	out = append(out, checkOnError(typ, cfg)...)
	return out, nil
}

// checkOnError validates the on_error block against the node's declared capabilities.
//
// Both failure modes are silent today: the runner drops an action the node does not declare (one log line, which nobody
// reads), and a code the node can never produce simply never matches - so the fallback the author thought they wrote
// does not exist. An unknown code is an Error because it can never match anything; a code that exists but is not
// declared by this type is a Warning, because it is legal config that just cannot fire here.
func checkOnError(typ string, cfg map[string]any) []Finding {
	caps, known := CapabilitiesOf(typ)
	if !known {
		return nil
	}
	oe := objectOf(cfg["on_error"])
	if oe == nil {
		return nil
	}
	var out []Finding
	add := func(severity, code, path, msg, hint string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeType: typ, Path: path, Message: msg, Hint: hint})
	}

	for _, action := range []string{"retry", "skip", "handler"} {
		block := objectOf(oe[action])
		if block == nil {
			continue
		}
		path := "on_error." + action
		if !caps.HasAction(action) {
			add(SeverityWarning, CodeOnErrorAction, path, fmt.Sprintf(
				"%s nodes do not declare the %s action, so the runner ignores this block at run time", typ, action),
				"remove it, or use an action this node declares")
			continue
		}
		if action == "handler" {
			if script, _ := block["script"].(string); strings.TrimSpace(script) == "" {
				add(SeverityWarning, CodeIncomplete, path+".script",
					fmt.Sprintf("%s node has an on_error.handler without a script, so it does nothing", typ),
					"write the handler script or drop the block")
			}
		}
		for _, c := range stringSlice(block["codes"]) {
			if !contains(AllErrorCodes, c) {
				add(SeverityError, CodeOnErrorCode, path+".codes", fmt.Sprintf(
					"%q is not an error code the runtime can produce", c),
					"use one of: "+strings.Join(AllErrorCodes, ", "))
				continue
			}
			if !caps.ProducesCode(c) {
				add(SeverityWarning, CodeOnErrorCode, path+".codes", fmt.Sprintf(
					"%s nodes never produce %q, so this rule can never fire", typ, c),
					"list only the codes this node type declares")
			}
		}
	}

	if post := objectOf(oe["post"]); post != nil && !caps.HasPost {
		add(SeverityWarning, CodeOnErrorPost, "on_error.post", fmt.Sprintf(
			"%s nodes do not support post, so the runner drops it at run time", typ),
			"remove the post block")
	}
	return out
}

// stringSlice reads a config list written either as []any (JSON) or []string (assembled by a tool).
func stringSlice(v any) []string {
	switch raw := v.(type) {
	case []any:
		out := make([]string, 0, len(raw))
		for _, it := range raw {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return raw
	}
	return nil
}

type langField struct {
	path string
	lang string
}

// langFields collects every "script language" field in a node's config.
//
// It covers the nodes a definition can produce: code.lang for code / result / progress, the condition script language of
// branch / loop, the handler of http / rpc / mcp / llm, and the two fallback handlers of on_error. One sweep instead of a
// copy per case: missing one means missing one "created fine, fails at run time" entry point.
func langFields(typ string, cfg map[string]any) []langField {
	var out []langField
	add := func(path string, obj map[string]any) {
		if s, _ := obj["lang"].(string); s != "" {
			out = append(out, langField{path: path, lang: s})
		}
	}
	switch typ {
	case "code", "result", "progress":
		add("code.lang", objectOf(cfg["code"]))
	case "branch":
		add("branch.lang", objectOf(cfg["branch"]))
	case "loop":
		add("loop.lang", objectOf(cfg["loop"]))
	case "http", "rpc", "mcp", "llm":
		add(typ+".handler.lang", objectOf(objectOf(cfg[typ])["handler"]))
	}
	if oe := objectOf(cfg["on_error"]); oe != nil {
		add("on_error.handler.lang", objectOf(oe["handler"]))
		add("on_error.post.handler.lang", objectOf(objectOf(oe["post"])["handler"]))
	}
	return out
}

func objectOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ===== whole diagram =====

type node struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ParentNode string `json:"parent_node"`
	Data       struct {
		Label  string         `json:"label"`
		Config map[string]any `json:"config"`
	} `json:"data"`
}

type edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"source_handle"`
	TargetHandle string `json:"target_handle"`
}

type diagram struct {
	Nodes []node `json:"nodes"`
	Edges []edge `json:"edges"`
}

// Diagram checks a whole stored diagram: structure (edges, containers, branch routing, references) plus every node's
// config through Step.
//
// raw accepts both shapes a diagram has in the database: a JSON object (tools write it) and a JSON string (the canvas
// stringifies once).
func Diagram(raw json.RawMessage, langs []string) ([]Finding, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}
	var out []Finding
	add := func(severity, code, nodeID, nodeType, path, msg, hint string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeID: nodeID, NodeType: nodeType, Path: path, Message: msg, Hint: hint})
	}

	// id -> node, label -> node (labels are the writable form of source_ref / call.target / fork_node_id).
	byID := make(map[string]*node, len(doc.Nodes))
	byLabel := make(map[string]*node, len(doc.Nodes))
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.ID == "" {
			add(SeverityError, CodeShape, "", n.Type, "id", "a node has no id and cannot be referenced", "")
			continue
		}
		byID[n.ID] = n
		if l := strings.TrimSpace(n.Data.Label); l != "" {
			byLabel[l] = n
		}
	}
	resolve := func(ref string) *node {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil
		}
		if n, ok := byID[ref]; ok {
			return n
		}
		return byLabel[ref]
	}

	// Duplicate labels: labels are how references are written, so a duplicate points at the wrong node.
	if d, ok := flowdoc.Parse(raw); ok {
		for _, dup := range d.DuplicateLabels() {
			add(SeverityError, CodeDuplicateLabel, "", "", "label", "duplicate node label: "+dup,
				"labels must be unique across the flow; pick another one or leave it empty so the tool adds a suffix")
		}
	}

	// Per-node config: unknown type + Step (shape / completeness / language).
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "" && !knownTypes[n.Type] {
			add(SeverityError, CodeUnknownType, n.ID, n.Type, "type",
				fmt.Sprintf("node type %q has no executor", n.Type), "delete the node or change its type")
		}
		fs, err := Step(n.Type, n.Data.Config, langs)
		if err != nil {
			add(SeverityError, CodeShape, n.ID, n.Type, "", err.Error(), "")
			continue
		}
		for _, f := range fs {
			f.NodeID = n.ID
			f.NodeType = n.Type
			out = append(out, f)
		}
	}

	// Edges: both ends must exist.
	valid := make([]edge, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		src, dst := byID[e.Source], byID[e.Target]
		if src == nil || dst == nil {
			add(SeverityError, CodeEdgeEndpoint, e.Source, "", "edges",
				fmt.Sprintf("edge %s connects %q to %q, and at least one of them does not exist", e.ID, e.Source, e.Target),
				"delete the edge and wire it again with create_edge")
			continue
		}
		valid = append(valid, e)
	}

	// Cross-parent edges are invalid on the canvas and at run time.
	for _, e := range valid {
		src, dst := byID[e.Source], byID[e.Target]
		if src.ParentNode != dst.ParentNode {
			add(SeverityError, CodeCrossParent, e.Source, src.Type, "edges",
				fmt.Sprintf("edge %s crosses a container boundary (%q is inside %q, %q is not)", e.ID, src.ID, src.ParentNode, dst.ID),
				"wire the edge inside one container, or move the node into it with update_node(parent_node=...)")
		}
	}

	// Outgoing edges: one at most, except branch / intent which route by handle. Containers are checked the same way.
	outCount := make(map[string]int, len(doc.Nodes))
	inCount := make(map[string]int, len(doc.Nodes))
	for _, e := range valid {
		outCount[e.Source]++
		inCount[e.Target]++
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if c := outCount[n.ID]; c > 1 && !multiOutgoingTypes[n.Type] {
			add(SeverityError, CodeMultiOutgoing, n.ID, n.Type, "edges",
				fmt.Sprintf("node %s (%s) has %d outgoing edges; only branch / intent may have more than one", n.ID, n.Type, c),
				"delete the extra edges: after the node finishes it follows the first outgoing edge")
		}
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if !containerTypes[n.Type] {
			continue
		}
		entries := 0
		for j := range doc.Nodes {
			if doc.Nodes[j].ParentNode == n.ID && doc.Nodes[j].Type == "entry" {
				entries++
			}
		}
		if entries != 1 {
			add(SeverityError, CodeContainerEntry, n.ID, n.Type, "children",
				fmt.Sprintf("container %s (%s) has %d entry children; it needs exactly one", n.ID, n.Type, entries),
				"create the entry child (parent_node=<container id>, type=entry); without it the container fails with 'body entry node not found'")
		}
	}

	// Branch routing: every declared child must have an outgoing edge with the source<childId> handle prefix.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "branch" && n.Type != "intent" {
			continue
		}
		children, _ := n.Data.Config["children"].([]any)
		if len(children) == 0 {
			continue // already reported as incomplete by Step
		}
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			id, _ := child["id"].(string)
			if strings.TrimSpace(id) == "" {
				add(SeverityError, CodeBranchEdge, n.ID, n.Type, "children",
					fmt.Sprintf("%s node %s has a child without an id, so no edge can route to it", n.Type, n.ID),
					"give every child an id")
				continue
			}
			prefix := "source" + id
			routed := false
			for _, e := range valid {
				if e.Source == n.ID && strings.HasPrefix(e.SourceHandle, prefix) {
					routed = true
					break
				}
			}
			if !routed {
				add(SeverityWarning, CodeBranchEdge, n.ID, n.Type, "edges",
					fmt.Sprintf("%s node %s declares child %q but no outgoing edge starts at source%s", n.Type, n.ID, id, id),
					"create the sub-chain edge for that child, or remove the child")
			}
		}
	}

	// Reference checks: a dangling source_ref silently yields an empty value at run time.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		cfg := n.Data.Config
		for path, obj := range namedObjects(cfg, "inputs", "targets") {
			ref, _ := obj["source_ref"].(string)
			sourceType, _ := obj["source_type"].(string)
			if sourceType != "node" || strings.TrimSpace(ref) == "" {
				continue
			}
			if resolve(ref) == nil {
				add(SeverityError, CodeDanglingRef, n.ID, n.Type, path,
					fmt.Sprintf("%s refers to node %q, which does not exist", path, ref),
					"point source_ref at an existing node id or label")
			}
		}
	}

	// call.target must be a function node in this flow.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "call" {
			continue
		}
		target, _ := n.Data.Config["target"].(string)
		if strings.TrimSpace(target) == "" {
			continue // already reported as incomplete
		}
		t := resolve(target)
		if t == nil {
			add(SeverityError, CodeCallTarget, n.ID, n.Type, "target",
				fmt.Sprintf("call node %s targets %q, which does not exist", n.ID, target),
				"use the id of a function node from read_flow")
			continue
		}
		if t.Type != "function" {
			add(SeverityError, CodeCallTarget, n.ID, n.Type, "target",
				fmt.Sprintf("call node %s targets %q, which is a %s node, not a function", n.ID, target, t.Type),
				"call can only invoke a function node")
		}
	}

	// wait.fork_node_id must name a fork node.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "wait" {
			continue
		}
		ref, _ := n.Data.Config["fork_node_id"].(string)
		if strings.TrimSpace(ref) == "" {
			continue // already reported as incomplete
		}
		t := resolve(ref)
		if t == nil || t.Type != "fork" {
			add(SeverityError, CodeWaitFork, n.ID, n.Type, "fork_node_id",
				fmt.Sprintf("wait node %s refers to %q, which is not a fork node in this flow", n.ID, ref),
				"point fork_node_id at the id of a fork node on the same chain")
		}
	}

	// each-mode loops read their array from config.inputs.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "loop" {
			continue
		}
		lc := objectOf(n.Data.Config["loop"])
		if t, _ := lc["type"].(string); t != "each" {
			continue
		}
		key, _ := lc["source_key"].(string)
		if strings.TrimSpace(key) == "" {
			add(SeverityError, CodeLoopSource, n.ID, n.Type, "loop.source_key",
				fmt.Sprintf("loop node %s iterates in each mode but declares no source_key", n.ID),
				"set loop.source_key to the name of an array declared in config.inputs")
			continue
		}
		if _, ok := objectOf(n.Data.Config["inputs"])[key]; !ok {
			add(SeverityError, CodeLoopSource, n.ID, n.Type, "loop.source_key",
				fmt.Sprintf("loop node %s iterates source_key %q, which is not declared in config.inputs", n.ID, key),
				"declare it in config.inputs (an array) or fix the name")
		}
	}

	// Unreachable nodes: no incoming edge and not the start node. A warning, because a node inside a container is
	// entered from its parent rather than by an edge.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type == "start" || n.Type == "entry" || n.Type == "comment" || n.ParentNode != "" {
			continue
		}
		if inCount[n.ID] == 0 {
			add(SeverityWarning, CodeUnreachable, n.ID, n.Type, "",
				fmt.Sprintf("node %s (%s) has no incoming edge and never runs", n.ID, n.Type),
				"wire it into the chain, or delete it")
		}
	}

	sortFindings(out)
	return out, nil
}

// namedObjects returns "<section>.<name>" -> object for the given config sections, so a path in a Finding matches what the
// caller has to edit.
func namedObjects(cfg map[string]any, sections ...string) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for _, s := range sections {
		for name, raw := range objectOf(cfg[s]) {
			if obj := objectOf(raw); obj != nil {
				out[s+"."+name] = obj
			}
		}
	}
	return out
}

func parse(raw json.RawMessage) (*diagram, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("the diagram is empty")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("the diagram string does not parse: %w", err)
		}
		raw = []byte(s)
	}
	var doc diagram
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the diagram is not a JSON object with a nodes array: %w", err)
	}
	return &doc, nil
}

// sortFindings puts errors first, then orders by node id so a report of the same diagram is stable.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity == SeverityError
		}
		if fs[i].NodeID != fs[j].NodeID {
			return fs[i].NodeID < fs[j].NodeID
		}
		return fs[i].Path < fs[j].Path
	})
}

// Summarize counts findings by severity.
func Summarize(fs []Finding) (errors, warnings int) {
	for _, f := range fs {
		if f.Severity == SeverityError {
			errors++
			continue
		}
		warnings++
	}
	return errors, warnings
}
