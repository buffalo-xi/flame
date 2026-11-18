package executors

// ensureFixedOutputs 把固定输出变量补进 outputs 定义，返回新 map（不改调用方数据）。
//
// 执行器写固定变量前必须先保证变量已声明：SetOutputVar 对未声明的变量直接报错，
// 而前端配置可能落后于后端（老节点没有新增的固定变量、某个响应模式没生成某个变量）。
// 依赖配置正确会让执行器要么写失败、要么节点直接判失败，所以统一在这里补齐。
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
