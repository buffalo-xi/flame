package executors

// ensureFixedOutputs adds the fixed output variables to the outputs declaration, returning a new map
// (the caller's data is not modified).
//
// An executor must declare a fixed variable before writing it: SetOutputVar fails outright on an
// undeclared variable, while the frontend config may lag behind the backend (an old node without the
// newly added fixed variable, or one response mode that does not generate a given variable). Relying on
// the config being right would make the executor either fail to write or fail the node outright, so
// everything is topped up here.
func ensureFixedOutputs(outputs map[string]interface{}, fixed map[string]string) map[string]interface{} {
	merged := make(map[string]interface{}, len(outputs)+len(fixed))
	for k, v := range outputs {
		merged[k] = v
	}
	for name, typ := range fixed {
		if _, exists := merged[name]; !exists {
			merged[name] = map[string]interface{}{"type": typ}
		}
	}
	return merged
}
