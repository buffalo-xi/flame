package executors

import (
	"sort"
	"sync"

	"flame/nexus/service/flow/core"
)

type NodeCapabilities struct {
	ErrorCodes []string           `json:"error_codes"` // the error codes this node can produce
	Actions    []flow.ErrorAction `json:"actions"`     // additional actions supported besides fail
	HasPost    bool               `json:"has_post"`    // whether it supports post-processing (failure cleanup)
	// FixedOutputs are the output variables the executor writes itself, on top of whatever config.outputs declares.
	// The static checks need them to answer "does this node really produce that source_key" without running it.
	FixedOutputs []string `json:"fixed_outputs"`
}

// outputNames returns the sorted names of a fixed-output declaration, so a capability list does not depend on map order.
func outputNames(decl map[string]string) []string {
	out := make([]string, 0, len(decl))
	for name := range decl {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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

// RegisteredTypes returns every node type that has an executor, sorted. It lets callers enumerate the palette without
// reaching into the map (the capability drift test uses it to notice a new executor that has no check entry).
func RegisteredTypes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(executorMap))
	for t := range executorMap {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// GetNodeCapabilities returns the capability declarations of every registered node type plus the global
// error codes and the raise node's raisable subset.
func GetNodeCapabilities() map[string]interface{} {
	mu.RLock()
	defer mu.RUnlock()
	result := make(map[string]interface{})
	for nodeType, executor := range executorMap {
		result[nodeType] = executor.Capabilities()
	}
	result["all_error_codes"] = flow.AllErrorCodes()
	// The raise node's picker reads this instead of keeping a fourth copy of the code table on the client: a code the
	// runner refuses must not be offerable in the editor.
	result["raisable_codes"] = RaisableCodeList()
	return result
}
