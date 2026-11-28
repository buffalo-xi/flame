package flowcheck

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The entry-argument declaration (flows.arguments) is what the caller sees: it becomes the endpoint's input_schema, and it is
// the only place a parameter can be marked required. What a step actually reads is config.inputs[].source_key with
// source_type "argument", and nothing reconciles the two at run time - which is why a mismatch is invisible until a call:
//
//   - a name the declaration does not have fails the node with "var path X not found" the moment it is reached;
//   - a name declared but left out of `required` fails the node only when a caller omits the value, which the declaration
//     just told that caller was fine.

// Arguments reconciles the declaration with the diagram. A flow with no declaration at all is judged as "nothing is declared" -
// a row reading source_type "argument" then has nowhere to come from - while unreadable stored text is left alone: that is a
// data problem, not a declaration problem. Callers holding only an inline diagram should not call this at all.
func Arguments(diagram, arguments json.RawMessage) []Finding {
	declared, required, ok := parseArguments(arguments)
	if !ok {
		return nil
	}
	doc, err := parse(diagram)
	if err != nil {
		return nil
	}
	var out []Finding
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if IgnoresInputs(n.Type) {
			continue
		}
		inputs := objectOf(n.Data.Config["inputs"])
		names := make([]string, 0, len(inputs))
		for name := range inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			row := objectOf(inputs[name])
			if row == nil {
				continue
			}
			if sourceType, _ := row["source_type"].(string); sourceType != "argument" {
				continue
			}
			key, _ := row["source_key"].(string)
			key = strings.TrimSpace(key)
			if key == "" {
				continue // a row without a source key is input_source's finding, not this one
			}
			switch {
			case !declared[key]:
				out = append(out, Finding{
					Severity: SeverityError, Code: CodeArgumentUndeclared, NodeID: n.ID, NodeType: n.Type,
					Path: "inputs." + name,
					Message: fmt.Sprintf("node %s reads entry argument %q, which this flow does not declare: the run fails with \"var path %s not found\"",
						n.ID, key, key),
					Hint: "declare it in arguments (create_flow's arguments / update_flow_arguments set) or point the row at another source",
				})
			case !required[key]:
				out = append(out, Finding{
					Severity: SeverityWarning, Code: CodeArgumentOptional, NodeID: n.ID, NodeType: n.Type,
					Path: "inputs." + name,
					Message: fmt.Sprintf("node %s reads entry argument %q, which is declared but not in arguments.required: a caller may omit it, and the node then fails with \"var path %s not found\"",
						n.ID, key, key),
					Hint: "add it to required (update_flow_arguments required) or give the node a source that always has a value",
				})
			}
		}
	}
	return out
}

// parseArguments reads {"properties":{...},"required":[...]}. A declaration without required marks nothing as required - that
// is exactly the trap, so it must not be read as "everything is required".
func parseArguments(raw json.RawMessage) (declared, required map[string]bool, ok bool) {
	declared = make(map[string]bool)
	required = make(map[string]bool)
	if len(raw) == 0 {
		return declared, required, true
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, false
	}
	for name := range doc.Properties {
		declared[name] = true
	}
	for _, name := range doc.Required {
		required[name] = true
	}
	return declared, required, true
}
