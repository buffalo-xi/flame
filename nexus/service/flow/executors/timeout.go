package executors

// parseTimeoutConfig 解析统一的节点级超时配置 {enabled, seconds}。
//
// 语义统一为：未配置 / enabled=false / seconds<=0 都表示「不设节点级超时」，
// 由运行的全局上限（ResolveTimeout / ResolveScriptTimeout）兜底；只有显式启用且
// 秒数合法时才返回正整数。
//
// 目前 branch / loop / code / result / progress / wait / http 共用这一份解析，
// 避免同一种配置形状在不同节点里出现「关闭了还生效」的差异。
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
