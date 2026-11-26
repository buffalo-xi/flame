package executors

import (
	"encoding/json"
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*AssignExecutor)(nil)

type AssignExecutor struct{}

// Assignment is one assignment declaration
type Assignment struct {
	Kind   string `json:"kind"`   // "output" | "target"
	Target string `json:"target"` // variable name; for kind=target it is a key of targets
	Path   string `json:"path"`   // sub-path, empty = assign directly
	Value  string `json:"value"`  // Go template string
}

func (e *AssignExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. Initialise the output variables (from config.outputs)
	if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// 2. Parse the assignments
	rawAssignments, ok := node.Data.Config["assignments"].([]interface{})
	if !ok {
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	assignments, err := parseAssignments(rawAssignments)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, err.Error())
	}

	if len(assignments) == 0 {
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	// 3. Resolve the input variables (from config.inputs)
	var inputArgs map[string]interface{}
	inputs, inputsExist := node.Data.Config["inputs"].(map[string]interface{})
	if inputsExist {
		var err error
		inputArgs, err = ctx.GetVariables(inputs, processID)
		if err != nil {
			return failf(nr, flow.ErrCodeConfig, err.Error())
		}
	} else {
		inputArgs = make(map[string]interface{})
	}
	nr.SetInputVars(inputArgs)

	// 4. Build the template context (global variables are already merged into the inputs, there is no
	// separate .global_vars)
	ctxData := BuildScriptTemplateContext(nr, inputArgs)

	// 5. Run them one by one
	for _, a := range assignments {
		rendered, err := RenderTemplate(a.Value, ctxData)
		if err != nil {
			return failf(nr, flow.ErrCodeScript, err.Error())
		}

		// Convert the value according to the variable type
		converted := convertByType(rendered, a, node)

		// Build the full path
		fullPath := buildFullPath(a, node)

		switch a.Kind {
		case "output":
			if err := nr.SetOutputVarWithFire(fullPath, converted); err != nil {
				return failf(nr, flow.ErrCodeVar, err.Error())
			}
		case "target":
			targetDef, err := nr.GetTargetDef(a.Target)
			if err != nil {
				return failf(nr, flow.ErrCodeConfig, fmt.Errorf("target %s: %w", a.Target, err).Error())
			}
			target, ok := targetDef.(map[string]interface{})
			if !ok {
				return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("target %s: invalid definition", a.Target))
			}

			// source_key names the variable to write; when it is empty the targets key is used. A sub-path is appended to
			// that name.
			target = copyMap(target)
			sourceKey := targetSourceKey(target, a.Target)
			if a.Path != "" {
				targetType, _ := target["type"].(string)
				if strings.HasPrefix(targetType, "array") {
					sourceKey += a.Path
				} else {
					sourceKey += "." + a.Path
				}
			}
			target["source_key"] = sourceKey

			if err := ctx.SetVariableWithFire(target, converted, processID); err != nil {
				return failf(nr, flow.ErrCodeVar, err.Error())
			}
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *AssignExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		// var: a failed variable write (see SetVariableWithFire above) produces it.
		// Leaving it out makes the runner degrade the code to other, and an on_error keyed on var would
		// never match.
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeScript, flow.ErrCodeVar},
		HasPost:    false,
	}
}

func NewAssignExecutor() *AssignExecutor {
	return &AssignExecutor{}
}

func init() {
	RegisterExecutor("assign", NewAssignExecutor())
}

// parseAssignments parses the assignments config
func parseAssignments(raw []interface{}) ([]Assignment, error) {
	result := make([]Assignment, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("assignment[%d]: not a map", i)
		}
		kind, _ := m["kind"].(string)
		target, _ := m["target"].(string)
		path, _ := m["path"].(string)
		value, _ := m["value"].(string)

		if kind == "" || target == "" {
			return nil, fmt.Errorf("assignment[%d]: kind and target are required", i)
		}
		if kind != "output" && kind != "target" {
			return nil, fmt.Errorf("assignment[%d]: invalid kind %q, must be output or target", i, kind)
		}

		result = append(result, Assignment{
			Kind:   kind,
			Target: target,
			Path:   path,
			Value:  value,
		})
	}
	return result, nil
}

// buildFullPath builds the complete variable path
func buildFullPath(a Assignment, node *flow.Node) string {
	if a.Path == "" {
		return a.Target
	}

	var varType string
	if a.Kind == "output" {
		if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
			if def, exists := outputs[a.Target]; exists {
				if defMap, ok := def.(map[string]interface{}); ok {
					varType, _ = defMap["type"].(string)
				}
			}
		}
	} else {
		// target: the type was already read from targets, so it is not looked up again here.
		// The path format decides: a path starting with [ means an array
		if strings.HasPrefix(a.Path, "[") {
			varType = "array"
		}
	}

	if strings.HasPrefix(varType, "array") || strings.HasPrefix(a.Path, "[") {
		return a.Target + a.Path // items[0], items[0].name
	}
	return a.Target + "." + a.Path // response.meta.title
}

// convertByType converts the rendered template string according to the variable type
func convertByType(rendered string, a Assignment, node *flow.Node) interface{} {
	var varType string

	if a.Kind == "output" {
		if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
			if def, exists := outputs[a.Target]; exists {
				if defMap, ok := def.(map[string]interface{}); ok {
					varType, _ = defMap["type"].(string)
				}
			}
		}
	}

	switch varType {
	case "number":
		// Try json.Number first
		var num json.Number
		if err := json.Unmarshal([]byte(rendered), &num); err == nil {
			return num
		}
		return json.Number(rendered)
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(rendered)) {
		case "true", "1":
			return true
		case "false", "0", "":
			return false
		default:
			return rendered
		}
	default:
		return rendered
	}
}

// copyMap shallow-copies a map
func copyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
