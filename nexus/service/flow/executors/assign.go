package executors

import (
	"encoding/json"
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*AssignExecutor)(nil)

type AssignExecutor struct{}

// Assignment 单条赋值声明
type Assignment struct {
	Kind   string `json:"kind"`   // "output" | "target"
	Target string `json:"target"` // 变量名；kind=target 时是 targets 里的键名
	Path   string `json:"path"`   // 子路径，空=直接赋值
	Value  string `json:"value"`  // Go template 模板字符串
}

func (e *AssignExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. 初始化输出变量（从 config.outputs）
	if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// 2. 解析 assignments
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

	// 3. 解析输入变量（从 config.inputs）
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

	// 4. 构建模板上下文（全局变量已并入输入变量，没有单独的 .global_vars）
	ctxData := BuildScriptTemplateContext(nr, inputArgs)

	// 5. 逐条执行
	for _, a := range assignments {
		rendered, err := RenderTemplate(a.Value, ctxData)
		if err != nil {
			return failf(nr, flow.ErrCodeScript, err.Error())
		}

		// 按变量类型转换值
		converted := convertByType(rendered, a, node)

		// 构建完整路径
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

			// 有子路径时，修正 source_key
			if a.Path != "" {
				target = copyMap(target)
				sourceKey, _ := target["source_key"].(string)
				targetType, _ := target["type"].(string)
				if strings.HasPrefix(targetType, "array") {
					target["source_key"] = sourceKey + a.Path
				} else {
					target["source_key"] = sourceKey + "." + a.Path
				}
			}

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
		// var：写变量失败（见上面 SetVariableWithFire 那条）会产出它。
		// 少声明会被 runner 降级成 other，用户按 var 配的 on_error 就永远匹配不上。
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

// parseAssignments 解析 assignments 配置
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

// buildFullPath 构建完整变量路径
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
		// target: type 已从 targets 中读取，但这里先不重复查
		// 用 path 格式判断：路径以 [ 开头的是 array
		if strings.HasPrefix(a.Path, "[") {
			varType = "array"
		}
	}

	if strings.HasPrefix(varType, "array") || strings.HasPrefix(a.Path, "[") {
		return a.Target + a.Path // items[0], items[0].name
	}
	return a.Target + "." + a.Path // response.meta.title
}

// convertByType 根据变量类型转换模板渲染后的字符串值
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
		// 尝试 json.Number
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

// copyMap 浅拷贝 map
func copyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
