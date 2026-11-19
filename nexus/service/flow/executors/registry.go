package executors

import (
	"flame/nexus/service/flow/core"
	"sync"
)

type NodeCapabilities struct {
	ErrorCodes []string           `json:"error_codes"` // the error codes this node can produce
	Actions    []flow.ErrorAction `json:"actions"`     // additional actions supported besides fail
	HasPost    bool               `json:"has_post"`    // whether it supports post-processing (failure cleanup)
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

// GetNodeCapabilities returns the capability declarations of every registered node type plus the global
// error codes
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
