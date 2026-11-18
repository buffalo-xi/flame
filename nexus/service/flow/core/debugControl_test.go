package flow

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 捕获挂起快照的 helper：在独立 goroutine 里驱动 SuspendIfNeeded（会阻塞直到放行），
// 真实的快照通过 eventHook 推入 snaps；done 在 SuspendIfNeeded 返回后关闭。
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
			t.Fatalf("期望挂起在节点 %s，实际在 %s", wantID, s.NodeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("超时：未在 2s 内于节点 %s 挂起", wantID)
	}
}

func noSnapshot(t *testing.T, snaps chan *DebugSnapshot, msg string) {
	t.Helper()
	select {
	case s := <-snaps:
		t.Fatalf("%s，但收到了挂起快照: %s", msg, s.NodeID)
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
	noSnapshot(t, snaps, "run 模式不应挂起")
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
	noSnapshot(t, snaps, "n1 无断点不应挂起")
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
	noSnapshot(t, snaps, "step over 放行后不应再挂起")
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
		t.Fatal("Stop 后挂起点未被唤醒")
	}
}

func TestDebugControl_BreakpointToggle(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.SetBreakpoint("x", true)
	if !ctrl.breakpoints["x"] {
		t.Fatal("设置断点后应为 true")
	}
	ctrl.SetBreakpoint("x", false)
	if ctrl.breakpoints["x"] {
		t.Fatal("取消断点后应为 false")
	}
}

func TestDebugControl_StackDepth(t *testing.T) {
	ctrl := NewDebugControl()
	ctrl.PushStack("p1")
	ctrl.PushStack("p2")
	if ctrl.CurrentDepth() != 2 {
		t.Fatalf("期望 depth=2，实际 %d", ctrl.CurrentDepth())
	}
	ctrl.PopStack()
	if ctrl.CurrentDepth() != 1 {
		t.Fatalf("期望 depth=1，实际 %d", ctrl.CurrentDepth())
	}
	_ = sync.Mutex{}
}

// ---------- 并发场景测试（Fix #10） ----------

// TestDebugControl_ConcurrentPushPopStack 并发 PushStack/PopStack 不应 panic 或死锁
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
		t.Fatalf("所有 push/pop 配对完成后 depth 应为 0，实际 %d", d)
	}
}

// TestDebugControl_StopWakesAllBlocked Stop 应唤醒所有挂起的 goroutine
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
		// 每个节点在不同的 process 中，模拟并发子流程
		processID := fmt.Sprintf("p%d", i)
		ctrl.PushStack(processID)
		driveNode(ctrl, nodeID, "code", processID, i, dones[i])
	}

	// 等待所有节点挂起（并发 goroutine 到达顺序不确定，仅等待收到 nodeCount 个快照）
	for i := 0; i < nodeCount; i++ {
		select {
		case <-snaps:
		case <-time.After(2 * time.Second):
			t.Fatalf("超时：仅收到 %d/%d 个挂起快照", i, nodeCount)
		}
	}

	// Stop 应唤醒所有挂起点
	ctrl.Stop()
	for i, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Stop 后节点 n%d 未被唤醒", i)
		}
	}
}

// TestDebugControl_ConcurrentSuspendAndResume 模拟多个并发节点同时到达 SuspendIfNeeded，
// Resume 逐个放行，验证不会死锁、不会丢失挂起。
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

	// 逐个 Resume：每收到一个快照就放行一个
	for i := 0; i < nodeCount; i++ {
		select {
		case <-snaps:
			ctrl.Resume()
		case <-time.After(3 * time.Second):
			t.Fatalf("等待第 %d 个挂起超时", i+1)
		}
	}

	// 所有节点应在 Resume 后完成
	for i, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("节点 cn%d 未在 Resume 后完成", i)
		}
	}
}

// TestDebugControl_RunModeNoRaceInConcurrent 并发节点在 run 模式下不应挂起，
// 且不应产生 data race（配合 go test -race 使用）。
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

	// run 模式下无断点，不应有任何挂起
	noSnapshot(t, snaps, "run 模式无断点不应挂起")
}
