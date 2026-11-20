package flow

import (
	"sync"
)

type ProcessExecMap struct {
	nodes      map[string][]*NodeResult
	totalCount int
	mu         sync.Mutex
}

func NewProcessExecMap() *ProcessExecMap {
	return &ProcessExecMap{
		nodes: make(map[string][]*NodeResult),
	}
}

func (m *ProcessExecMap) Append(processID string, node *NodeResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[processID] = append(m.nodes[processID], node)
	m.totalCount++
}

// TryAppend checks and appends atomically: when max > 0 and the total already reached max it returns false,
// otherwise it appends and returns true.
func (m *ProcessExecMap) TryAppend(processID string, node *NodeResult, max int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if max > 0 && m.totalCount >= max {
		return false
	}
	m.nodes[processID] = append(m.nodes[processID], node)
	m.totalCount++
	return true
}

func (m *ProcessExecMap) Get(processID string) []*NodeResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*NodeResult, len(m.nodes[processID]))
	copy(result, m.nodes[processID])
	return result
}

func (m *ProcessExecMap) GetAll() []*NodeResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*NodeResult
	for _, nodes := range m.nodes {
		result = append(result, nodes...)
	}
	return result
}

func (m *ProcessExecMap) Len(processID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.nodes[processID])
}

type NodeExecMap struct {
	nodes map[string]*NodeResult
	mu    sync.Mutex
}

func NewNodeExecMap() *NodeExecMap {
	return &NodeExecMap{
		nodes: make(map[string]*NodeResult),
	}
}

func (m *NodeExecMap) Get(key string) (*NodeResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[key]
	return node, ok
}

func (m *NodeExecMap) Set(key string, value *NodeResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[key] = value
}

func (m *NodeExecMap) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, key)
}

func (m *NodeExecMap) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.nodes)
}

func (m *NodeExecMap) Range(fn func(key string, value *NodeResult) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.nodes {
		if !fn(k, v) {
			break
		}
	}
}
