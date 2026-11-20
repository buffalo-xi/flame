package flow

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Helper that captures a suspension snapshot: SuspendIfNeeded is driven from its own goroutine
// (it blocks until released), the actual snapshot arrives through eventHook into snaps, and done is
// closed once SuspendIfNeeded returns.
func driveNode(ctrl *DebugControl, nodeID, nodeType, processID string, depth int, done chan struct{}) {
	go func() {
		defer close(done)
		node := &Node{ID: nodeID, Type: nodeType}
		ctrl.SuspendIfNeeded(node, processID, depth)
	}()
}

func waitSnapshot(t *testing.T, snaps chan *DebugSnapshot, wantID string) {
	t.Helper()
	select {
	case s := <-snaps:
		if s.NodeID != wantID {
			t.Fatalf("expected a suspension at node %s, got %s", wantID, s.NodeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out: no suspension at node %s within 2s", wantID)
	}
}

func noSnapshot(t *testing.T, snaps chan *DebugSnapshot, msg string) {
	t.Helper()
	select {
	case s := <-snaps:
		t.Fatalf("%s, but a suspension snapshot arrived for %s", msg, s.NodeID)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDebugControl_PauseSuspendAndResume(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 8)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugPause, "")

	done := make(chan struct{})
	driveNode(ctrl, "n1", "code", "p1", 0, done)
	waitSnapshot(t, snaps, "n1")
	ctrl.Resume()

	done2 := make(chan struct{})
	driveNode(ctrl, "n2", "code", "p1", 0, done2)
	waitSnapshot(t, snaps, "n2")
	ctrl.Resume()
	<-done2
}

func TestDebugControl_RunNoSuspend(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 8)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugRun, "")

	done := make(chan struct{})
	driveNode(ctrl, "n1", "code", "p1", 0, done)
	noSnapshot(t, snaps, "run mode must not suspend")
	<-done
}

func TestDebugControl_RunBreakpointHits(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 8)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugRun, "")
	ctrl.SetBreakpoint("n2", true)

	done := make(chan struct{})
	driveNode(ctrl, "n1", "code", "p1", 0, done)
	noSnapshot(t, snaps, "n1 has no breakpoint and must not suspend")
	<-done

	done2 := make(chan struct{})
	driveNode(ctrl, "n2", "code", "p1", 0, done2)
	waitSnapshot(t, snaps, "n2")
	ctrl.Resume()
	<-done2
}

func TestDebugControl_StepOverOnce(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 8)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugStep, StepOver)

	done := make(chan struct{})
	driveNode(ctrl, "n1", "code", "p1", 0, done)
	waitSnapshot(t, snaps, "n1")
	ctrl.Resume()

	done2 := make(chan struct{})
	driveNode(ctrl, "n2", "code", "p1", 0, done2)
	noSnapshot(t, snaps, "step over must not suspend again after the release")
	<-done2
}

func TestDebugControl_StopWakesBlocked(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 8)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugPause, "")

	done := make(chan struct{})
	driveNode(ctrl, "n1", "code", "p1", 0, done)
	waitSnapshot(t, snaps, "n1")
	ctrl.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the suspension point was not woken after Stop")
	}
}

func TestDebugControl_BreakpointToggle(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.SetBreakpoint("x", true)
	if !ctrl.breakpoints["x"] {
		t.Fatal("setting a breakpoint should make it true")
	}
	ctrl.SetBreakpoint("x", false)
	if ctrl.breakpoints["x"] {
		t.Fatal("clearing a breakpoint should make it false")
	}
}

func TestDebugControl_StackDepth(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.PushStack("p1")
	ctrl.PushStack("p2")
	if ctrl.CurrentDepth() != 2 {
		t.Fatalf("expected depth=2, got %d", ctrl.CurrentDepth())
	}
	ctrl.PopStack()
	if ctrl.CurrentDepth() != 1 {
		t.Fatalf("expected depth=1, got %d", ctrl.CurrentDepth())
	}
	_ = sync.Mutex{}
}

// ---------- Concurrency cases (Fix #10) ----------

// TestDebugControl_ConcurrentPushPopStack: concurrent PushStack/PopStack must neither panic nor deadlock.
func TestDebugControl_ConcurrentPushPopStack(t *testing.T) {
	ctrl := NewDebugControl()
	const goroutines = 20
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				pid := fmt.Sprintf("p%d_%d", id, i)
				ctrl.PushStack(pid)
				ctrl.CurrentDepth()
				ctrl.PopStack()
			}
		}(g)
	}
	wg.Wait()

	if d := ctrl.CurrentDepth(); d != 0 {
		t.Fatalf("depth should be 0 once every push/pop pair finished, got %d", d)
	}
}

// TestDebugControl_StopWakesAllBlocked: Stop must wake every suspended goroutine.
func TestDebugControl_StopWakesAllBlocked(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 16)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugPause, "")

	const nodeCount = 5
	dones := make([]chan struct{}, nodeCount)
	for i := 0; i < nodeCount; i++ {
		dones[i] = make(chan struct{})
		nodeID := fmt.Sprintf("n%d", i)
		// Each node sits in a different process, simulating concurrent child flows.
		processID := fmt.Sprintf("p%d", i)
		ctrl.PushStack(processID)
		driveNode(ctrl, nodeID, "code", processID, i, dones[i])
	}

	// Wait for every node to suspend (the arrival order of concurrent goroutines is unspecified, so
	// only wait for nodeCount snapshots).
	for i := 0; i < nodeCount; i++ {
		select {
		case <-snaps:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out: only %d/%d suspension snapshots arrived", i, nodeCount)
		}
	}

	// Stop should wake every suspension point.
	ctrl.Stop()
	for i, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("node n%d was not woken after Stop", i)
		}
	}
}

// TestDebugControl_ConcurrentSuspendAndResume: several concurrent nodes reach SuspendIfNeeded at the
// same time and Resume releases them one by one; this verifies there is no deadlock and no lost suspension.
func TestDebugControl_ConcurrentSuspendAndResume(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	snaps := make(chan *DebugSnapshot, 16)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })
	ctrl.Control(DebugPause, "")

	const nodeCount = 4
	dones := make([]chan struct{}, nodeCount)
	for i := 0; i < nodeCount; i++ {
		dones[i] = make(chan struct{})
		driveNode(ctrl, fmt.Sprintf("cn%d", i), "code", "p0", 0, dones[i])
	}

	// Resume one at a time: release one node per snapshot received.
	for i := 0; i < nodeCount; i++ {
		select {
		case <-snaps:
			ctrl.Resume()
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for suspension #%d", i+1)
		}
	}

	// Every node should finish after its Resume.
	for i, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("node cn%d did not finish after Resume", i)
		}
	}
}

// TestDebugControl_RunModeNoRaceInConcurrent: concurrent nodes must not suspend in run mode and must
// not produce a data race (run it together with go test -race).
func TestDebugControl_RunModeNoRaceInConcurrent(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.Enabled = true
	ctrl.Control(DebugRun, "")

	snaps := make(chan *DebugSnapshot, 16)
	ctrl.SetEventHook(func(s *DebugSnapshot) { snaps <- s })

	const nodeCount = 10
	var wg sync.WaitGroup
	wg.Add(nodeCount)
	for i := 0; i < nodeCount; i++ {
		go func(idx int) {
			defer wg.Done()
			node := &Node{ID: fmt.Sprintf("rn%d", idx), Type: "code"}
			ctrl.SuspendIfNeeded(node, "p0", 0)
		}(i)
	}
	wg.Wait()

	// No breakpoints in run mode, so nothing may suspend.
	noSnapshot(t, snaps, "run mode without breakpoints must not suspend")
}
