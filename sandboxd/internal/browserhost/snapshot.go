package browserhost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
)

// interactiveRoles are the roles that get a ref assigned.
//
// Only "things you can operate" deserve a ref: numbering every text node too would bloat the snapshot with a pile
// of meaningless e17/e18 and make targets harder for the model to find. Pure text nodes are still emitted, just
// without a ref.
var interactiveRoles = map[string]bool{
	"button":           true,
	"link":             true,
	"textbox":          true,
	"searchbox":        true,
	"checkbox":         true,
	"radio":            true,
	"combobox":         true,
	"listbox":          true,
	"option":           true,
	"menuitem":         true,
	"menuitemcheckbox": true,
	"menuitemradio":    true,
	"tab":              true,
	"switch":           true,
	"slider":           true,
	"spinbutton":       true,
	"treeitem":         true,
}

// skipRoles are roles that carry no information themselves and are only passed through as structural containers.
// Keys are always lowercase — role is already lowercased before rendering.
var skipRoles = map[string]bool{
	"none":          true,
	"generic":       true,
	"inlinetextbox": true,
	"linebreak":     true,
	"presentation":  true,
}

// emptyNameRoles are structural roles that need no line of their own when nameless (they are still emitted when
// named).
var emptyNameRoles = map[string]bool{
	"paragraph":   true,
	"group":       true,
	"section":     true,
	"rootwebarea": true,
	"webarea":     true,
}

// renderAX renders the accessibility tree into compact "one node per line" text.
//
// Returns: the text, the ref -> backendNodeId mapping, the number of emitted nodes, and whether a budget truncated
// it.
//
// Why the accessibility tree instead of the DOM: half of the DOM is layout / framework output (div inside div),
// and showing that to the model means letting it hunt for operable elements in the noise; the accessibility tree
// keeps only the semantic nodes — "button / textbox / link / text" — and already carries the accessible name.
func renderAX(nodes []*accessibility.Node, maxNodes, maxBytes int) (string, map[string]cdp.BackendNodeID, int, bool) {
	byID := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}

	refs := make(map[string]cdp.BackendNodeID)
	var (
		buf     strings.Builder
		emitted int
		trunc   bool
		seq     int
	)

	var walk func(n *accessibility.Node, depth int, parentName string)
	walk = func(n *accessibility.Node, depth int, parentName string) {
		if n == nil || trunc {
			return
		}
		role := strings.ToLower(axString(n.Role))
		name := strings.TrimSpace(axString(n.Name))
		value := strings.TrimSpace(axString(n.Value))

		// Deep nodes are not expanded: anything further down is mostly table cell / list item detail, and
		// rendering it on would spend the budget on things the model will never click.
		if depth > 12 {
			return
		}

		emit := !n.Ignored && !skipRoles[role] && role != ""
		// Nameless text / nameless structural containers (paragraph, group and the like) are only branching points
		// of the tree; writing them out would bloat the snapshot into a wall of brackets.
		if name == "" && emptyNameRoles[role] {
			emit = false
		}
		// A text node with the same name as its parent is a duplicate projection of the same content
		// (heading -> statictext), so only the parent line is kept.
		if role == "statictext" && name == parentName {
			emit = false
		}

		childDepth := depth
		if emit {
			if emitted >= maxNodes {
				trunc = true
				return
			}
			line := strings.Repeat("  ", depth) + "- "
			if interactiveRoles[role] && n.BackendDOMNodeID != 0 {
				seq++
				ref := fmt.Sprintf("e%d", seq)
				refs[ref] = n.BackendDOMNodeID
				line += ref + " "
			}
			line += "[" + role + "]"
			if name != "" {
				line += " " + strconv.Quote(name)
			}
			if value != "" && value != name {
				line += " value=" + strconv.Quote(value)
			}
			line += renderStates(n)
			line += "\n"

			if buf.Len()+len(line) > maxBytes {
				trunc = true
				return
			}
			buf.WriteString(line)
			emitted++
			childDepth = depth + 1
		}

		nextParent := name
		if nextParent == "" {
			nextParent = parentName
		}
		for _, id := range n.ChildIDs {
			walk(byID[id], childDepth, nextParent)
			if trunc {
				return
			}
		}
	}

	// Roots: the nodes whose parent is not in this result (usually Document, and an iframe document gets its own
	// root as well).
	for _, n := range nodes {
		if n.ParentID != "" {
			if _, ok := byID[n.ParentID]; ok {
				continue
			}
		}
		walk(n, 0, "")
	}

	text := buf.String()
	if trunc {
		text += "\n... (snapshot truncated; narrow it with max_nodes / max_bytes, or use text)\n"
	}
	return text, refs, emitted, trunc
}

// renderStates joins the boolean / tri-state properties into a tail like (checked, disabled).
// Only true states and the tri-state mixed are emitted — writing out "not checked" is just noise.
func renderStates(n *accessibility.Node) string {
	var out []string
	for _, p := range n.Properties {
		if p == nil || p.Value == nil {
			continue
		}
		name := string(p.Name)
		switch name {
		case "checked", "selected", "expanded", "disabled", "required", "readonly", "invalid":
			v := axString(p.Value)
			switch v {
			case "true":
				out = append(out, name)
			case "mixed":
				out = append(out, name+"=mixed")
			}
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, ", ") + ")"
}

// axString takes the string form of an AXValue. Underneath it is raw JSON (jsontext.Value), and the type may be a
// string, a number, a boolean or the tri-state "mixed"/"false".
func axString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(v.Value, &n); err == nil {
		return n.String()
	}
	var b bool
	if err := json.Unmarshal(v.Value, &b); err == nil {
		return strconv.FormatBool(b)
	}
	return ""
}
