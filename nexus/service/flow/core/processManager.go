package flow

import (
	"fmt"
	"sync"
)

type ProcessInfo struct {
	ID           string
	ParentID     string
	Data         map[string]interface{}
	ChildCounter int
}

type ProcessManager struct {
	processes map[string]*ProcessInfo
	children  map[string][]string
	mu        sync.Mutex
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*ProcessInfo),
		children:  make(map[string][]string),
	}
}

// NewProcess creates a process and returns its ID.
// An empty parentID creates the root process (ID "1"); otherwise a child process is created.
func (pm *ProcessManager) NewProcess(parentID string, data map[string]interface{}) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if parentID == "" {
		if _, exists := pm.processes["1"]; exists {
			return "", fmt.Errorf("root process already exists")
		}
		pm.processes["1"] = &ProcessInfo{
			ID:       "1",
			ParentID: "",
			Data:     data,
		}
		return "1", nil
	}

	parent, exists := pm.processes[parentID]
	if !exists {
		return "", fmt.Errorf("parent process %s not found", parentID)
	}

	parent.ChildCounter++
	id := fmt.Sprintf("%s_%d", parentID, parent.ChildCounter)

	pm.processes[id] = &ProcessInfo{
		ID:       id,
		ParentID: parentID,
		Data:     data,
	}
	pm.children[parentID] = append(pm.children[parentID], id)

	return id, nil
}

func (pm *ProcessManager) GetProcess(id string) (*ProcessInfo, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	info, ok := pm.processes[id]
	return info, ok
}

func (pm *ProcessManager) GetParentID(id string) (string, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	info, ok := pm.processes[id]
	if !ok {
		return "", false
	}
	return info.ParentID, info.ParentID != ""
}

func (pm *ProcessManager) GetParent(id string) (*ProcessInfo, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	info, ok := pm.processes[id]
	if !ok || info.ParentID == "" {
		return nil, false
	}

	parentInfo, ok := pm.processes[info.ParentID]
	return parentInfo, ok
}

func (pm *ProcessManager) GetChildren(id string) []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	children, ok := pm.children[id]
	if !ok {
		return []string{}
	}
	result := make([]string, len(children))
	copy(result, children)
	return result
}

func (pm *ProcessManager) GetChildrenInfo(id string) []*ProcessInfo {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	childIDs, ok := pm.children[id]
	if !ok {
		return []*ProcessInfo{}
	}

	result := make([]*ProcessInfo, 0, len(childIDs))
	for _, childID := range childIDs {
		if info, ok := pm.processes[childID]; ok {
			result = append(result, info)
		}
	}
	return result
}

func (pm *ProcessManager) RemoveProcess(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	info, ok := pm.processes[id]
	if !ok {
		return
	}

	if info.ParentID != "" {
		children := pm.children[info.ParentID]
		for i, childID := range children {
			if childID == id {
				pm.children[info.ParentID] = append(children[:i], children[i+1:]...)
				break
			}
		}
	}

	delete(pm.processes, id)

	if childIDs, ok := pm.children[id]; ok {
		for _, childID := range childIDs {
			if childInfo, ok := pm.processes[childID]; ok {
				childInfo.ParentID = ""
			}
		}
		delete(pm.children, id)
	}
}

func (pm *ProcessManager) GetAllProcessIDs() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	result := make([]string, 0, len(pm.processes))
	for id := range pm.processes {
		result = append(result, id)
	}
	return result
}

// GetCallDepth computes the call→function recursion depth: it walks up the parent chain and counts
// only the processes whose Data["call_func"] is true. A single locked pass, O(depth), in memory only.
func (pm *ProcessManager) GetCallDepth(id string) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	depth := 0
	current := id
	for {
		info, exists := pm.processes[current]
		if !exists {
			break
		}
		// Was the current process created by a call→function step?
		if info.Data != nil {
			if v, ok := info.Data["call_func"]; ok && v == true {
				depth++
			}
		}
		if info.ParentID == "" {
			break
		}
		current = info.ParentID
	}
	return depth
}
