// Package flowdoc reads the stored workflow diagram.
//
// A diagram reaches the database in two shapes: the canvas submits a JSON string (the front
// end stringifies once before saving) and tools write a JSON object. Every check that looks
// inside a diagram has to accept both, so the unwrapping lives here instead of being copied
// into each caller.
package flowdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CommentNodeType is the node type of a canvas comment.
//
// Comments are annotations for the editor: they never reach the executor, so they must not
// consume a node budget.
const CommentNodeType = "comment"

// Node is one entry of the diagram's nodes array (only the fields the checks need).
type Node struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Label string `json:"label"`
	} `json:"data"`
}

// Doc is the parsed diagram.
type Doc struct {
	Nodes []Node `json:"nodes"`
}

// Parse unwraps the two accepted shapes (a JSON string or a JSON object).
//
// ok is false when the document is empty or does not parse into an object with a nodes array:
// those are draft shapes, not write-boundary errors, so callers let them through and the run
// path reports them.
func Parse(raw []byte) (*Doc, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, false
		}
		raw = []byte(s)
	}

	var doc *Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	if doc == nil { // a literal null: not a diagram either
		return nil, false
	}
	return doc, true
}

// CountNodes returns the number of nodes, skipping the given node types.
func (d *Doc) CountNodes(skipTypes ...string) int {
	if d == nil {
		return 0
	}
	skip := make(map[string]bool, len(skipTypes))
	for _, t := range skipTypes {
		skip[t] = true
	}
	n := 0
	for _, node := range d.Nodes {
		if skip[node.Type] {
			continue
		}
		n++
	}
	return n
}

// DuplicateLabels returns one "<label> (nodes A and B)" entry per repeated label.
//
// An empty label takes no part: it is not a reference target, and the editor leaves it empty
// while a node is being created.
func (d *Doc) DuplicateLabels() []string {
	if d == nil {
		return nil
	}
	seen := make(map[string]string, len(d.Nodes))
	var dups []string
	for _, n := range d.Nodes {
		label := strings.TrimSpace(n.Data.Label)
		if label == "" {
			continue
		}
		if first, ok := seen[label]; ok {
			dups = append(dups, fmt.Sprintf("%s (nodes %s and %s)", label, first, n.ID))
			continue
		}
		seen[label] = n.ID
	}
	return dups
}
