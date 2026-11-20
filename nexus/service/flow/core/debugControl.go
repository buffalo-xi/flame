package flow

import (
	"sync"
)

// DebugMode is the debug run mode.
type DebugMode string

const (
	// DebugRun runs straight through, suspending before no node.
	DebugRun DebugMode = "run"
	// DebugPause suspends before entering every node (waiting for resume).
	DebugPause DebugMode = "pause"
	// DebugStep suspends at the points selected by stepKind (over/in/out).
	DebugStep DebugMode = "step"
)

// DebugStepKind is the single-step type.
type DebugStepKind string

const (
	// StepOver suspends before the next sibling node, after the current node finished.
	StepOver DebugStepKind = "over"
	// StepIn suspends before the first node of a child process.
	StepIn DebugStepKind = "in"
	// StepOut suspends before the parent's next node, once the current child process ended.
	StepOut DebugStepKind = "out"
)

// DebugSnapshot is the snapshot pushed back on suspension, used by the frontend to highlight the
// node and show its variables.
type DebugSnapshot struct {
	NodeID   string                 `json:"node_id"`
	NodeType string                 `json:"node_type"`
	Process  string                 `json:"process"`
	Depth    int                    `json:"depth"`
	Vars     map[string]interface{} `json:"vars,omitempty"`
	Stack    []string               `json:"stack,omitempty"`
}

// DebugControl is the debug controller of a flow execution.
// execute() calls SuspendIfNeeded before descending into every node to decide whether to suspend;
// on suspension a snapshot is pushed back through eventHook and the run blocks on the resume
// condition until a control signal arrives.
type DebugControl struct {
	mu sync.Mutex

	// Enabled reports whether debugging is on (set to true once OnConnect has been validated).
	Enabled bool
	// mode is the current run mode.
	mode DebugMode
	// stepKind only applies when mode is step.
	stepKind DebugStepKind
	// cond is the suspend/resume condition variable (mu is its Locker).
	cond *sync.Cond
	// suspended reports whether the run currently waits for a release. A condition variable rather
	// than a one-shot channel, so signals cannot be lost.
	suspended bool
	// stopFlag is set when stop arrives; after the release the caller finishes via StdCtx cancellation.
	stopFlag bool
	// stepOutDepth is the depth step-out waits to fall back to (suspend only at or above it).
	stepOutDepth int
	// stepOverDepth is the depth recorded by step-over; it suspends once, only at that depth.
	// Deeper levels inside a process (child flows) do not suspend, so "step over" really runs the
	// whole node.
	stepOverDepth int
	// breakpoints is the set of node IDs whose breakpoint is hit.
	breakpoints map[string]bool
	// eventHook is called on suspension (injected by the WS debug source, pushing the snapshot).
	eventHook func(*DebugSnapshot)
	// execHook is called when a node starts (injected by the WS debug source, marking the node as
	// "already run" for the frontend). It is called whether or not the run suspends, so the frontend
	// can highlight every node that executed.
	execHook func(nodeID string, nodeName string)
	// failHook is called when a node fails (injected by the WS debug source, marking it as "failed").
	failHook func(nodeID string, nodeName string, errMsg string)
	// skipHook is called when a node is skipped (injected by the WS debug source, marking it "skipped").
	skipHook func(nodeID string, nodeName string)
	// doneHook is called when a node finishes (injected by the WS debug source, accumulating the
	// latest data of that node).
	doneHook func(nr *NodeResult)

	// stack is the current call stack (the chain of process IDs), maintained in debug mode only.
	stack []string
}

// NewDebugControl creates a debug controller (run mode by default, never suspending).
func NewDebugControl() *DebugControl {
	d := &DebugControl{
		mode:        DebugRun,
		breakpoints: make(map[string]bool),
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// SetEventHook injects the snapshot push callback.
func (d *DebugControl) SetEventHook(hook func(*DebugSnapshot)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventHook = hook
}

// SetExecHook injects the node-start callback (marks "already run" nodes).
func (d *DebugControl) SetExecHook(hook func(nodeID string, nodeName string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execHook = hook
}

// SetFailHook injects the node-failure callback (marks "failed" nodes).
func (d *DebugControl) SetFailHook(hook func(nodeID string, nodeName string, errMsg string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failHook = hook
}

// SetSkipHook injects the node-skipped callback (marks "skipped" nodes).
func (d *DebugControl) SetSkipHook(hook func(nodeID string, nodeName string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.skipHook = hook
}

// SetDoneHook injects the node-finished callback (accumulates the latest node data).
func (d *DebugControl) SetDoneHook(hook func(nr *NodeResult)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.doneHook = hook
}

// SetBreakpoint adds or removes a breakpoint.
func (d *DebugControl) SetBreakpoint(nodeID string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if on {
		d.breakpoints[nodeID] = true
	} else {
		delete(d.breakpoints, nodeID)
	}
}

// PushStack / PopStack maintain the call stack (debug mode).
func (d *DebugControl) PushStack(processID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stack = append(d.stack, processID)
}

func (d *DebugControl) PopStack() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.stack) > 0 {
		d.stack = d.stack[:len(d.stack)-1]
	}
}

func (d *DebugControl) CurrentDepth() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.stack)
}

// shouldSuspend decides whether the current node must suspend before executing.
// It only evaluates: no state change, no locking (the caller holds the lock).
func (d *DebugControl) shouldSuspend(nodeID string, depth int) bool {
	// Breakpoints win: in any mode a hit suspends (as in mainstream debuggers).
	// Hitting a breakpoint during step-out stops at the breakpoint; after resume the mode is back to
	// run and execution continues normally.
	if d.breakpoints[nodeID] {
		return true
	}

	switch d.mode {
	case DebugPause:
		return true
	case DebugStep:
		switch d.stepKind {
		case StepOver:
			// Suspend once, at the current depth only (deeper levels inside a process run through).
			return depth == d.stepOverDepth
		case StepIn:
			// Suspend once at any depth, including the first node inside a process.
			return true
		case StepOut:
			// Suspend only once the depth is back at stepOutDepth or shallower.
			return depth <= d.stepOutDepth
		}
	case DebugRun:
		return false
	}
	return false
}

// SuspendIfNeeded is called before a node executes. Returning means the permission to continue was granted.
// node: the node about to execute; processID: the current process; depth: the current recursion depth.
func (d *DebugControl) SuspendIfNeeded(node *Node, processID string, depth int) {
	d.mu.Lock()
	if !d.Enabled {
		d.mu.Unlock()
		return
	}
	// start (flow root entry), entry (container entry) and layout (pure placeholder) never really
	// execute: skip them (no "already run" mark, no suspension, no breakpoint hit).
	if node != nil && (node.Type == string(NodeTypeStart) ||
		node.Type == string(NodeTypeEntry) ||
		node.Type == string(NodeTypeLayout)) {
		d.mu.Unlock()
		return
	}
	// Grab the start callback and unlock before calling it (the hook writes to the WS, no lock wanted).
	execHook := d.execHook
	d.mu.Unlock()

	// Regardless of suspension, mark the node as started (the frontend highlights "already run" nodes).
	if execHook != nil {
		if node != nil {
			execHook(node.ID, node.Label())
		}
	}

	d.mu.Lock()
	// A breakpoint hit in run mode.
	if d.shouldSuspend(node.ID, depth) {
		// Prepare the snapshot.
		snap := &DebugSnapshot{
			NodeID:   node.ID,
			NodeType: node.Type,
			Process:  processID,
			Depth:    depth,
			Stack:    append([]string{}, d.stack...),
		}
		hook := d.eventHook
		// In step mode, suspend once and return to run (over/in): the next entry passes straight through.
		if d.mode == DebugStep {
			if d.stepKind == StepOver || d.stepKind == StepIn {
				d.mode = DebugRun
			}
			// step out is resumed after the suspension and returns to run as well.
			if d.stepKind == StepOut {
				d.mode = DebugRun
			}
		}
		// Mark the suspension and let the condition-variable loop below handle it (no lost signal).
		d.suspended = true
		d.mu.Unlock()

		if hook != nil {
			hook(snap)
		}
		// Block until released: wait on the condition variable in a loop and re-check the state bits
		// after waking, so a Resume/Stop arriving between "unlock" and "wait" cannot lose the signal.
		d.mu.Lock()
		for d.suspended && !d.stopFlag {
			d.cond.Wait()
		}
		d.suspended = false
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
}

// WaitIfGloballySuspended is the all-stop barrier: once any node has suspended (suspended == true),
// the current node blocks here — whether or not it hit a breakpoint — until Resume/Stop broadcasts.
// It implements all-stop: a breakpoint hit freezes every concurrent branch, avoiding the race of
// several suspension points waiting at once. The check and the block happen atomically under the
// same lock, so "everyone checks, everyone passes" cannot happen.
func (d *DebugControl) WaitIfGloballySuspended() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.suspended && !d.stopFlag {
		d.cond.Wait()
	}
}

// Resume wakes a suspension (keeps releasing in run/pause mode; releases once in step mode).
func (d *DebugControl) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.suspended = false
	d.cond.Broadcast()
}

// Control applies a control signal (coming from the WS).
func (d *DebugControl) Control(mode DebugMode, stepKind DebugStepKind) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode = mode
	d.stepKind = stepKind
	if mode == DebugStep {
		switch stepKind {
		case StepOut:
			// step out: record the current depth - 1 (the parent process depth) and suspend only once
			// execution fell back to the parent level.
			// Note: with concurrent child flows, a node of a sibling process at the same depth can
			// suspend too, because the check looks at depth only, not at the concrete process ID.
			// That is an acceptable trade-off: in concurrent scenarios step-out stops at the nearest
			// node of the same level.
			d.stepOutDepth = len(d.stack) - 1
		case StepOver:
			// step over: record the current depth and suspend once at that depth only; deeper levels
			// inside a process pass straight through, so "step over" means "run the whole node".
			d.stepOverDepth = len(d.stack)
		}
	}
}

// Stop stops the execution (sets stopFlag and broadcasts, waking every blocked suspension point).
func (d *DebugControl) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopFlag = true
	d.suspended = false
	d.cond.Broadcast()
}
