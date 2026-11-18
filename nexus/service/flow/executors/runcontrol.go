package executors

import (
	"sync"

	flow "flame/nexus/service/flow/core"
	"flame/nexus/shared/runledger"
)

// flowRuns 本进程在跑的 flow 运行（run_id → 执行上下文）。
//
// 台账（runledger）负责「在哪台」，这里负责「这台怎么停」：ExecContext.Cancel 会把
// StdCtx 置为已取消，节点循环、wait、沙箱脚本、以及端点请求断开后传进来的 ctx
// 都挂在同一条链上，所以一 cancel 整条运行就开始收尾（会话记录记成 stopped）。
var flowRuns = struct {
	mu    sync.Mutex
	byRun map[string]*flow.ExecContext
}{byRun: make(map[string]*flow.ExecContext)}

// registerLocalRun 把这次运行登记进本进程的停止表；返回幂等注销函数。
func registerLocalRun(ec *flow.ExecContext) func() {
	if ec == nil || ec.GetRunID() == "" {
		return func() {}
	}
	runID := ec.GetRunID()
	flowRuns.mu.Lock()
	flowRuns.byRun[runID] = ec
	flowRuns.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			flowRuns.mu.Lock()
			delete(flowRuns.byRun, runID)
			flowRuns.mu.Unlock()
		})
	}
}

// StopRun 取消本进程上的一条 flow 运行。返回 false 表示不在这台（或已结束）。
func StopRun(runID string) bool {
	if runID == "" {
		return false
	}
	flowRuns.mu.Lock()
	ec := flowRuns.byRun[runID]
	flowRuns.mu.Unlock()
	if ec == nil {
		return false
	}
	ec.Cancel()
	return true
}

// handleRunStopSignal 处理来自 Redis 的跨实例停止信号。
// 每台实例都会收到同一条信号，没跑这条运行的实例直接忽略。
func handleRunStopSignal(sig runledger.StopSignal) {
	if sig.Kind != "" && sig.Kind != runledger.KindFlow {
		return
	}
	StopRun(sig.RunID)
}

func init() {
	runledger.SubscribeStop(handleRunStopSignal)
}
