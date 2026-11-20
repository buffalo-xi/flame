package flow

import (
	"encoding/json"
	"errors"
	"flame/nexus/shared/globalvars"
	"flame/tools/xjson"
	"fmt"
	"strconv"
	"strings"
)

func getStringValue(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case json.Number:
		return string(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 32)
	default:
		return ""
	}
}

func unmarshalWithNumber(data string) (interface{}, error) {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	var result interface{}
	err := dec.Decode(&result)
	return result, err
}

func parsePathPart(part string) (string, int, bool) {
	startIdx := strings.Index(part, "[")
	endIdx := strings.LastIndex(part, "]")

	if startIdx > 0 && endIdx > startIdx {
		name := part[:startIdx]
		idxStr := part[startIdx+1 : endIdx]
		idx, err := strconv.Atoi(idxStr)
		if err == nil {
			return name, idx, true
		}
	}

	return part, 0, false
}

func getSliceElement(arr interface{}, index int, name, path string) (interface{}, error) {
	if index < 0 {
		return nil, fmt.Errorf("array index %d is negative for path %s", index, path)
	}

	switch a := arr.(type) {
	case []interface{}:
		if index >= len(a) {
			return nil, nil
		}
		return a[index], nil
	case []string:
		if index >= len(a) {
			return nil, nil
		}
		return a[index], nil
	case []bool:
		if index >= len(a) {
			return nil, nil
		}
		return a[index], nil
	case []map[string]interface{}:
		if index >= len(a) {
			return nil, nil
		}
		return a[index], nil
	default:
		return nil, fmt.Errorf("expected slice at '%s' but got %T", name, arr)
	}
}

func setSliceValue(arr interface{}, index int, value interface{}, name, path string) (interface{}, error) {
	if index < 0 {
		return nil, fmt.Errorf("array index %d is negative for path %s", index, path)
	}

	switch a := arr.(type) {
	case []interface{}:
		for len(a) <= index {
			a = append(a, nil)
		}
		switch v := value.(type) {
		case json.Number:
			a[index] = v
		case float64:
			a[index] = json.Number(strconv.FormatFloat(v, 'f', -1, 64))
		case int:
			a[index] = json.Number(strconv.Itoa(v))
		case int64:
			a[index] = json.Number(strconv.FormatInt(v, 10))
		case float32:
			a[index] = json.Number(strconv.FormatFloat(float64(v), 'f', -1, 32))
		default:
			a[index] = value
		}
		return a, nil
	case []string:
		strVal, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected string value for slice '%s' but got %T", name, value)
		}
		for len(a) <= index {
			a = append(a, "")
		}
		a[index] = strVal
		return a, nil
	case []bool:
		boolVal, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool value for slice '%s' but got %T", name, value)
		}
		for len(a) <= index {
			a = append(a, false)
		}
		a[index] = boolVal
		return a, nil
	case []map[string]interface{}:
		mapVal, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("expected map value for slice '%s' but got %T", name, value)
		}
		for len(a) <= index {
			a = append(a, nil)
		}
		a[index] = mapVal
		return a, nil
	default:
		return nil, fmt.Errorf("expected slice at '%s' but got %T", name, arr)
	}
}

// deprecatedEnvVarTypeGlobal is the retired env var type.
//
// "An env var pointing at a global variable" ({type: "global", value: <global var name>}) is gone:
// global variables became one source of node input variables (source_type: "global" inside
// config.inputs), on a par with env vars, and no longer travel through env_vars. The constant only
// exists to report legacy data with a readable sentence instead of falling into convertEnvVar's
// "unsupported envVar type".
const deprecatedEnvVarTypeGlobal = "global"

func GetEnvVar(envVars map[string]interface{}, path string) (interface{}, error) {
	envVar, err := parseEnvVar(envVars, path)
	if err != nil {
		return nil, err
	}
	if envVar.Type == deprecatedEnvVarTypeGlobal {
		return nil, fmt.Errorf("envVar %s uses the deprecated type global: pick the global source on the node input variable instead (source_type: \"global\" in config.inputs)", path)
	}
	return convertEnvVar(envVar, path)
}

// parseEnvVar parses one env_vars entry into a struct (shared by both read paths).
func parseEnvVar(envVars map[string]interface{}, path string) (EnvVar, error) {
	var envVar EnvVar
	if envVars == nil {
		return envVar, errors.New("envVars is nil")
	}

	value, ok := envVars[path]
	if !ok {
		return envVar, fmt.Errorf("envVar path %s not found", path)
	}

	switch v := value.(type) {
	case map[string]interface{}:
		envVar.Type = getStringValue(v, "type")
		envVar.Value = getStringValue(v, "value")
		envVar.Description = getStringValue(v, "description")
	default:
		return envVar, fmt.Errorf("envVar path %s has invalid format", path)
	}
	return envVar, nil
}

// convertEnvVar converts the inline value by type (existing behaviour, unchanged).
func convertEnvVar(envVar EnvVar, path string) (interface{}, error) {
	switch envVar.Type {
	case "number":
		if envVar.Value == "" {
			return 0, nil
		}
		if num, err := strconv.ParseInt(envVar.Value, 10, 64); err == nil {
			return num, nil
		}
		num, err := strconv.ParseFloat(envVar.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse number value for path %s: %w", path, err)
		}
		return num, nil
	case "string", "secret":
		return envVar.Value, nil
	default:
		return nil, fmt.Errorf("unsupported envVar type %s for path %s", envVar.Type, path)
	}
}

// ResolveGlobalSource reads one global variable for a node input variable with source_type: "global".
//
// This is the **only** entry point for referencing a global variable from a flow: it does not inject
// anything into the template context (there is no .global_vars) and does not go through env_vars —
// global variables sit on a par with argument / env / flow / node / builtin as one input source of a
// node, and templates always write {{ .input.xxx }}.
//
// Failure must be a hard error, never an empty string or nil: a silent downgrade lets the flow run on
// with empty values, and such a problem is nearly impossible to locate in the run log. All three errors
// carry enough context (variable name / tenant / input name).
func ResolveGlobalSource(schema, name string) (interface{}, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("input variable source is global but no global variable name was given")
	}
	if schema == "" {
		return nil, fmt.Errorf("input variable references global variable %s but the current context has no tenant schema", name)
	}
	vars, err := globalvars.Resolve(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to read global variable %s: %w", name, err)
	}
	value, ok := vars[name]
	if !ok {
		return nil, fmt.Errorf("global variable %q does not exist (create it under Settings → Global Variables first)", name)
	}
	return value, nil
}

func GetEnvVarSimple(envVars map[string]interface{}, path string) interface{} {
	result, _ := GetEnvVar(envVars, path)
	return result
}

func SetEnvVar(envVars map[string]interface{}, path string, value interface{}) error {
	if envVars == nil {
		return errors.New("envVars is nil")
	}

	var envVar EnvVar
	existing, ok := envVars[path]
	if ok {
		switch v := existing.(type) {
		case map[string]interface{}:
			envVar.Type = getStringValue(v, "type")
			envVar.Description = getStringValue(v, "description")
		}
	}

	switch envVar.Type {
	case "number":
		switch v := value.(type) {
		case int:
			envVar.Value = strconv.Itoa(v)
		case int64:
			envVar.Value = strconv.FormatInt(v, 10)
		case float64:
			envVar.Value = strconv.FormatFloat(v, 'f', -1, 64)
		case float32:
			envVar.Value = strconv.FormatFloat(float64(v), 'f', -1, 32)
		case json.Number:
			envVar.Value = string(v)
		case string:
			if v == "" {
				return fmt.Errorf("invalid number string value %q for envVar path %s", v, path)
			} else if _, err := strconv.ParseInt(v, 10, 64); err == nil {
				envVar.Value = v
			} else if _, err := strconv.ParseFloat(v, 64); err == nil {
				envVar.Value = v
			} else {
				return fmt.Errorf("invalid number string value %q for envVar path %s", v, path)
			}
		default:
			return fmt.Errorf("unsupported value type %T for number envVar path %s", value, path)
		}
	case deprecatedEnvVarTypeGlobal:
		// Retired: global variables no longer travel through env_vars, and at run time there is no
		// "name pointing elsewhere" to assign to.
		return fmt.Errorf("envVar path %s uses the deprecated type global: pick the global source on the node input variable instead (source_type: \"global\" in config.inputs)", path)
	case "string", "secret":
		switch v := value.(type) {
		case string:
			envVar.Value = v
		default:
			return fmt.Errorf("unsupported value type %T for %s envVar path %s", value, envVar.Type, path)
		}
	default:
		return fmt.Errorf("unsupported envVar type %s for path %s", envVar.Type, path)
	}

	envVars[path] = map[string]interface{}{
		"type":        envVar.Type,
		"value":       envVar.Value,
		"description": envVar.Description,
	}
	return nil
}

func InitFlowVars(defVars map[string]interface{}) map[string]interface{} {
	if defVars == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{})
	for key, val := range defVars {
		switch v := val.(type) {
		case map[string]interface{}:
			varType := getStringValue(v, "type")
			defaultVal := getStringValue(v, "default")
			var value interface{}

			switch varType {
			case "number":
				if defaultVal == "" {
					defaultVal = "0"
				}
				value = json.Number(defaultVal)
			case "boolean":
				if defaultVal == "" {
					defaultVal = "false"
				}
				value = defaultVal == "true"
			case "object":
				properties := make(map[string]interface{})
				if props, ok := v["properties"].(map[string]interface{}); ok {
					for propKey, propVal := range props {
						if propMap, ok := propVal.(map[string]interface{}); ok {
							propType := getStringValue(propMap, "type")
							propDefault := getStringValue(propMap, "default")
							var propValue interface{}
							switch propType {
							case "number":
								if propDefault == "" {
									propDefault = "0"
								}
								propValue = json.Number(propDefault)
							case "boolean":
								if propDefault == "" {
									propDefault = "false"
								}
								propValue = propDefault == "true"
							default:
								propValue = propDefault
							}
							properties[propKey] = propValue
						}
					}
				}
				value = properties
			case "array[number]":
				if defaultVal == "" || defaultVal == "[]" {
					value = []interface{}{}
				} else {
					if arr, err := unmarshalWithNumber(defaultVal); err == nil {
						if arrSlice, ok := arr.([]interface{}); ok {
							value = arrSlice
						} else {
							value = []interface{}{}
						}
					} else {
						value = []interface{}{}
					}
				}
			case "array[boolean]":
				if defaultVal == "" || defaultVal == "[]" {
					value = []bool{}
				} else {
					if arr, err := unmarshalWithNumber(defaultVal); err == nil {
						if arrSlice, ok := arr.([]interface{}); ok {
							var boolArr []bool
							for _, item := range arrSlice {
								switch v := item.(type) {
								case bool:
									boolArr = append(boolArr, v)
								case string:
									boolArr = append(boolArr, v == "true")
								default:
									boolArr = append(boolArr, false)
								}
							}
							value = boolArr
						} else {
							value = []bool{}
						}
					} else {
						value = []bool{}
					}
				}
			case "array[string]":
				if defaultVal == "" || defaultVal == "[]" {
					value = []string{}
				} else {
					if arr, err := unmarshalWithNumber(defaultVal); err == nil {
						if arrSlice, ok := arr.([]interface{}); ok {
							var strArr []string
							for _, item := range arrSlice {
								if s, ok := item.(string); ok {
									strArr = append(strArr, s)
								} else {
									strArr = append(strArr, fmt.Sprintf("%v", item))
								}
							}
							value = strArr
						} else {
							value = []string{}
						}
					} else {
						value = []string{}
					}
				}
			case "array[object]":
				if defaultVal == "" || defaultVal == "[]" {
					value = []map[string]interface{}{}
				} else {
					if arr, err := unmarshalWithNumber(defaultVal); err == nil {
						if arrSlice, ok := arr.([]interface{}); ok {
							var objArr []map[string]interface{}
							for _, item := range arrSlice {
								if m, ok := item.(map[string]interface{}); ok {
									objArr = append(objArr, m)
								}
							}
							value = objArr
						} else {
							value = []map[string]interface{}{}
						}
					} else {
						value = []map[string]interface{}{}
					}
				}
			default:
				value = defaultVal
			}

			result[key] = value
		default:
			result[key] = val
		}
	}
	return result
}

func InitLocalVars(defVars map[string]interface{}) map[string]interface{} {
	if defVars == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{})
	for key, val := range defVars {
		switch v := val.(type) {
		case map[string]interface{}:
			varType := getStringValue(v, "type")

			var value interface{}
			switch varType {
			case "number":
				value = json.Number("0")
			case "boolean":
				value = false
			case "object":
				value = make(map[string]interface{})
			case "array[number]":
				value = []interface{}{}
			case "array[boolean]":
				value = []bool{}
			case "array[string]":
				value = []string{}
			case "array[object]":
				value = []map[string]interface{}{}
			default:
				value = ""
			}
			result[key] = value
		}
	}
	return result
}

func InitLoopVars(defVars map[string]interface{}) map[string]interface{} {
	if defVars == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{})
	for key, val := range defVars {
		switch v := val.(type) {
		case map[string]interface{}:
			varType := getStringValue(v, "type")

			var value interface{}
			switch varType {
			case "number":
				value = json.Number("0")
			case "boolean":
				value = false
			case "object":
				value = make(map[string]interface{})
			default:
				value = ""
			}
			result[key] = value
		}
	}
	return result
}

func InitOutputVars(defVars map[string]interface{}) map[string]interface{} {
	if defVars == nil {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{})
	for key, val := range defVars {
		switch v := val.(type) {
		case map[string]interface{}:
			varType := getStringValue(v, "type")
			var value interface{}
			switch varType {
			case "string":
				value = ""
			case "number":
				value = json.Number("0")
			case "boolean":
				value = false
			case "object":
				value = make(map[string]interface{})
			case "array[number]":
				value = []interface{}{}
			case "array[boolean]":
				value = []bool{}
			case "array[string]":
				value = []string{}
			case "array[object]":
				value = []map[string]interface{}{}
			}
			result[key] = value
		}
	}
	return result
}

func InitArguments(args interface{}) map[string]interface{} {
	if args == nil {
		return make(map[string]interface{})
	}

	data, err := json.Marshal(args)
	if err != nil {
		return make(map[string]interface{})
	}

	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()

	var result interface{}
	if err := dec.Decode(&result); err != nil {
		return make(map[string]interface{})
	}

	if m, ok := result.(map[string]interface{}); ok {
		return m
	}

	return make(map[string]interface{})
}

func GetVarByPath(vars map[string]interface{}, path string) (interface{}, error) {
	if vars == nil {
		return nil, errors.New("vars is nil")
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil, errors.New("path is empty")
	}

	baseKey, baseIndex, hasBaseIndex := parsePathPart(parts[0])

	value, ok := vars[baseKey]
	if !ok {
		return nil, fmt.Errorf("var path %s not found", path)
	}

	result := value

	if hasBaseIndex {
		switch r := result.(type) {
		case []bool:
			if baseIndex < 0 || baseIndex >= len(r) {
				return nil, fmt.Errorf("array index %d out of bounds for path %s", baseIndex, path)
			}
			result = r[baseIndex]
		case []string:
			if baseIndex < 0 || baseIndex >= len(r) {
				return nil, fmt.Errorf("array index %d out of bounds for path %s", baseIndex, path)
			}
			result = r[baseIndex]
		case []map[string]interface{}:
			if baseIndex < 0 || baseIndex >= len(r) {
				return nil, fmt.Errorf("array index %d out of bounds for path %s", baseIndex, path)
			}
			result = r[baseIndex]
		case []interface{}:
			if baseIndex < 0 || baseIndex >= len(r) {
				return nil, fmt.Errorf("array index %d out of bounds for path %s", baseIndex, path)
			}
			result = r[baseIndex]
		default:
			return nil, fmt.Errorf("cannot apply index %d to non-array type %T for path %s", baseIndex, result, path)
		}
	}

	for i := 1; i < len(parts); i++ {
		part := parts[i]
		name, index, isIndex := parsePathPart(part)

		if isIndex {
			switch r := result.(type) {
			case []bool:
				if index < 0 || index >= len(r) {
					return nil, fmt.Errorf("array index %d out of bounds for path %s", index, path)
				}
				result = r[index]
			case []string:
				if index < 0 || index >= len(r) {
					return nil, fmt.Errorf("array index %d out of bounds for path %s", index, path)
				}
				result = r[index]
			case []map[string]interface{}:
				if index < 0 || index >= len(r) {
					return nil, fmt.Errorf("array index %d out of bounds for path %s", index, path)
				}
				result = r[index]
			case []interface{}:
				if index < 0 || index >= len(r) {
					return nil, fmt.Errorf("array index %d out of bounds for path %s", index, path)
				}
				result = r[index]
			default:
				return nil, fmt.Errorf("cannot apply index %d to non-array type %T for path %s", index, result, path)
			}
		} else {
			switch r := result.(type) {
			case map[string]interface{}:
				val, ok := r[name]
				if !ok {
					return nil, fmt.Errorf("property %s not found in object for path %s", name, path)
				}
				result = val
			default:
				return nil, fmt.Errorf("cannot access property %s on non-object type %T for path %s", name, result, path)
			}
		}
	}

	return result, nil
}

func SetVarByPath(vars map[string]interface{}, path string, value interface{}) error {
	if vars == nil {
		return errors.New("vars is nil")
	}

	convertedValue, err := xjson.ConvertToJSONNumber(value)
	if err != nil {
		return fmt.Errorf("failed to convert value to JSON number: %v", err)
	}
	value = convertedValue

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return errors.New("path is empty")
	}

	var current interface{} = vars

	baseName, baseIndex, hasBaseIndex := parsePathPart(parts[0])

	if hasBaseIndex {
		arr, ok := vars[baseName]
		if !ok {
			arr = []interface{}{}
		}

		if len(parts) == 1 {
			newArr, err := setSliceValue(arr, baseIndex, value, baseName, path)
			if err != nil {
				return err
			}
			vars[baseName] = newArr
			return nil
		}

		elem, err := getSliceElement(arr, baseIndex, baseName, path)
		if err != nil {
			return err
		}
		if elem == nil {
			elem = make(map[string]interface{})
			newArr, err := setSliceValue(arr, baseIndex, elem, baseName, path)
			if err != nil {
				return err
			}
			vars[baseName] = newArr
		}
		current = elem
	} else {
		if len(parts) == 1 {
			vars[parts[0]] = value
			return nil
		}
		next, ok := vars[parts[0]]
		if !ok || next == nil {
			next = make(map[string]interface{})
			vars[parts[0]] = next
		}
		current = next
	}

	for i := 1; i < len(parts); i++ {
		part := parts[i]
		name, index, isIndex := parsePathPart(part)

		if i == len(parts)-1 {
			if isIndex {
				c, ok := current.(map[string]interface{})
				if !ok {
					return fmt.Errorf("expected map at path but got %T", current)
				}
				arr, ok := c[name]
				if !ok {
					arr = []interface{}{}
				}
				newArr, err := setSliceValue(arr, index, value, name, path)
				if err != nil {
					return err
				}
				c[name] = newArr
			} else {
				c, ok := current.(map[string]interface{})
				if !ok {
					return fmt.Errorf("expected map at path but got %T", current)
				}
				c[name] = value
			}
			return nil
		}

		if isIndex {
			c, ok := current.(map[string]interface{})
			if !ok {
				return fmt.Errorf("expected map at path but got %T", current)
			}
			arr, ok := c[name]
			if !ok {
				arr = []interface{}{}
			}

			elem, err := getSliceElement(arr, index, name, path)
			if err != nil {
				return err
			}
			if elem == nil {
				elem = make(map[string]interface{})
				newArr, err := setSliceValue(arr, index, elem, name, path)
				if err != nil {
					return err
				}
				c[name] = newArr
			}
			current = elem
		} else {
			c, ok := current.(map[string]interface{})
			if !ok {
				return fmt.Errorf("expected map at path but got %T", current)
			}
			next, ok := c[name]
			if !ok || next == nil {
				next = make(map[string]interface{})
				c[name] = next
			}
			current = next
		}
	}
	return nil
}
