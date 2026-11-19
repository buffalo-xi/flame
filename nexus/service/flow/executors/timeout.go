package executors

// parseTimeoutConfig parses the shared node-level timeout config {enabled, seconds}.
//
// The semantics are uniform: unconfigured / enabled=false / seconds<=0 all mean "no node-level
// timeout", leaving the run's global ceiling (ResolveTimeout / ResolveScriptTimeout) as the fallback;
// only an explicitly enabled timeout with a valid number of seconds yields a positive integer.
//
// branch / loop / code / result / progress / wait / http share this parser today, so one config shape
// cannot behave differently between nodes ("switched off but still in effect").
func parseTimeoutConfig(raw interface{}) int {
	cfg, ok := raw.(map[string]interface{})
	if !ok {
		return -1
	}
	if enabled, _ := cfg["enabled"].(bool); !enabled {
		return -1
	}
	var seconds float64
	switch v := cfg["seconds"].(type) {
	case float64:
		seconds = v
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	default:
		return -1
	}
	if seconds <= 0 {
		return -1
	}
	return int(seconds)
}
