package executors

import (
	"flame/nexus/service/flow/core"
	"sync"
)

type NodeCapabilities struct {
	ErrorCodes []string           `json:"error_codes"` // 该节点可能产生的错误码
	Actions    []flow.ErrorAction `json:"actions"`     // 除 fail 以外，额外支持的动作
	HasPost    bool               `json:"has_post"`    // 是否支持后处理（失败收尾）
}

type Executor interface {
	Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error
	Capabilities() NodeCapabilities
}

var (
	executorMap = make(map[string]Executor)
	mu          sync.RWMutex
)

func RegisterExecutor(nodeType string, executor Executor) {
	mu.Lock()
	defer mu.Unlock()
	executorMap[nodeType] = executor
}

func GetExecutor(nodeType string) (Executor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	executor, exists := executorMap[nodeType]
	return executor, exists
}

// GetNodeCapabilities 返回所有已注册节点类型的能力声明 + 全局错误编码
func GetNodeCapabilities() map[string]interface{} {
	mu.RLock()
	defer mu.RUnlock()
	result := make(map[string]interface{})
	for nodeType, executor := range executorMap {
		result[nodeType] = executor.Capabilities()
	}
	result["all_error_codes"] = flow.AllErrorCodes()
	return result
}
