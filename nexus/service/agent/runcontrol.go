package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/shared/runledger"
)

// 终止原因：用 context.Cause 传递，让上层能给出人话，而不是一句 "context canceled"。
var (
	// ErrRunStopped 用户 / 管理端主动终止。
	ErrRunStopped = errors.New("run stopped by user")
	// ErrClientGone 客户端断开（SSE 连接关闭 / 页面关闭）。
	ErrClientGone = errors.New("client disconnected")
)

// runHandle 一次在跑的运行的取消句柄。
type runHandle struct {
	cancel    context.CancelCauseFunc
	sessionID string
	// cancelled 已触发取消。用来让「停止」幂等：重复调用返回 0，
	// 而不是每次都把同一批运行再数一遍。
	cancelled atomic.Bool
}

// runRegistry 进程内台账：run_id → 句柄；session_id → run_id 集合（副索引）。
//
// 为什么需要这张表：Stream 以前从 context.Background() 起步，整条链路
// （LLM 流、工具执行）没有任何取消点。用户关页面或想强行停下时只能等模型
// 自己跑完 —— 期间 exec / browser 还在继续占用沙箱资源。
//
// 主键取 run_id（每次调用唯一）；副索引取 session_id，因为 UI 只知道会话。
var runRegistry = struct {
	mu     sync.Mutex
	byRun  map[string]*runHandle
	bySess map[string]map[string]struct{}
}{
	byRun:  make(map[string]*runHandle),
	bySess: make(map[string]map[string]struct{}),
}

// registerRun 登记一次运行，返回幂等的注销函数（Stream 结束时 defer 调用）。
func registerRun(entry runledger.Entry, cancel context.CancelCauseFunc) func() {
	if entry.RunID == "" || cancel == nil {
		return func() {}
	}
	runID, sessionID := entry.RunID, entry.SessionID
	h := &runHandle{cancel: cancel, sessionID: sessionID}

	runRegistry.mu.Lock()
	runRegistry.byRun[runID] = h
	if sessionID != "" {
		set := runRegistry.bySess[sessionID]
		if set == nil {
			set = make(map[string]struct{})
			runRegistry.bySess[sessionID] = set
		}
		set[runID] = struct{}{}
	}
	runRegistry.mu.Unlock()

	// 跨实例台账：写一条「在跑」并纳入心跳（runledger 内部按租户合并）。
	// best-effort：Redis 不可用时只记内存，不影响运行本身。
	unregisterLedger := runledger.Register(entry)

	// 停止可能早于登记：客户端断开与 Stream 启动之间有一个窗口，那时的 StopRunCause
	// 找不到句柄。命中待取消就立刻取消，并标成已取消（后续重复停止照旧返回 false）。
	if cause := takePendingStop(runID, sessionID); cause != nil {
		h.cancelled.Store(true)
		cancel(cause)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			unregisterRun(runID, sessionID)
			unregisterLedger()
		})
	}
}

func unregisterRun(runID, sessionID string) {
	runRegistry.mu.Lock()
	defer runRegistry.mu.Unlock()
	delete(runRegistry.byRun, runID)
	if sessionID == "" {
		return
	}
	if set := runRegistry.bySess[sessionID]; set != nil {
		delete(set, runID)
		if len(set) == 0 {
			delete(runRegistry.bySess, sessionID)
		}
	}
}

// pendingStopTTL 待取消登记的最长存活时间。
//
// 停止可能早于登记（客户端断开与 Stream 启动之间有一个窗口），所以要能「先记下来，
// 等它登记时立刻取消」。但必须过期：否则很久以后的一次停止会把同一会话里新起的
// 那一轮当成立即要停的那一轮杀掉。
const pendingStopTTL = 10 * time.Second

// pendingStop 一条尚未命中的停止请求。runID 为空 = 会话级（对该会话之后登记的那条生效）。
type pendingStop struct {
	runID     string
	sessionID string
	cause     error
	at        time.Time
}

var pendingStops = struct {
	mu  sync.Mutex
	all []pendingStop
}{}

// rememberPendingStop 记下一条暂时没命中的停止请求，交给 registerRun 消费。
//
// 只存内存、只活一个 TTL：它补的是「登记与停止的先后顺序」这个窗口，不是跨进程通知
// —— 多实例下另一台机器上的运行，这里永远等不到它登记。
func rememberPendingStop(runID, sessionID string, cause error) {
	if sessionID == "" {
		return
	}
	now := time.Now()
	pendingStops.mu.Lock()
	defer pendingStops.mu.Unlock()
	pendingStops.all = prunePendingStops(pendingStops.all, now)
	pendingStops.all = append(pendingStops.all,
		pendingStop{runID: runID, sessionID: sessionID, cause: cause, at: now})
}

// takePendingStop 取走与这次登记匹配的待取消请求（消费即删：一次停止只生效一次）。
func takePendingStop(runID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	now := time.Now()
	pendingStops.mu.Lock()
	defer pendingStops.mu.Unlock()
	pendingStops.all = prunePendingStops(pendingStops.all, now)
	for i, p := range pendingStops.all {
		if p.sessionID != sessionID {
			continue
		}
		// 按 run 停的必须精确对上；按会话停的（runID 为空）对新登记的这条就生效。
		if p.runID != "" && p.runID != runID {
			continue
		}
		pendingStops.all = append(pendingStops.all[:i], pendingStops.all[i+1:]...)
		return p.cause
	}
	return nil
}

// prunePendingStops 丢掉过期的登记（调用方持锁）。
func prunePendingStops(all []pendingStop, now time.Time) []pendingStop {
	kept := all[:0]
	for _, p := range all {
		if now.Sub(p.at) <= pendingStopTTL {
			kept = append(kept, p)
		}
	}
	return kept
}

// StopRunCause 终止一次运行并指定原因（如客户端断开）。
//
// sessionID 必填：它既是「停哪批运行」的锚点，也是归属校验的唯一依据。
// 空 sessionID 一律拒绝 —— 台账是进程级、跨 agent 的，只凭 run_id 就能停掉
// 别人的运行；这个口子不在单个调用方那里守，而是守在这里，任何入口都绕不过去。
// 返回 false 表示没有匹配的在跑实例（已结束 / id 不对 / 会话不匹配 / 没给会话）。
func StopRunCause(runID, sessionID string, cause error) bool {
	if runID == "" || sessionID == "" {
		return false
	}
	if cause == nil {
		cause = ErrRunStopped
	}
	runRegistry.mu.Lock()
	h := runRegistry.byRun[runID]
	ok := h != nil && h.sessionID == sessionID
	if ok && h.cancelled.Swap(true) {
		ok = false // 已经停过：幂等
	}
	missing := h == nil
	runRegistry.mu.Unlock()
	if !ok {
		// 这条 run 还没登记（Stream 刚起步）就记下来，等它登记时立刻取消。
		// 只记「查不到这条 run」：会话不匹配是明确的越权请求，不该留下待取消；
		// 已结束的 run 也不会复用同一个 run_id，记下来只会随 TTL 自然过期。
		if missing {
			rememberPendingStop(runID, sessionID, cause)
		}
		return false
	}
	h.cancel(cause)
	return true
}

// StopRunByID 按 run_id 精确终止，不再要求会话。
//
// 只给管理端的「运行列表 → 终止」用：那条 run 是先从本租户台账里查出来的，归属已经由
// 台账把关。无会话的运行（RPC / MCP 单轮）没有 session_id 可传，用 StopRunCause 会永远
// 停不掉，所以单开这一个入口，而不是把「会话必填」的校验放宽给所有人。
func StopRunByID(runID string) bool {
	if runID == "" {
		return false
	}
	runRegistry.mu.Lock()
	h := runRegistry.byRun[runID]
	ok := h != nil
	if ok && h.cancelled.Swap(true) {
		ok = false // 已经停过：幂等
	}
	runRegistry.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel(ErrRunStopped)
	return true
}

// StopSession 终止某会话下所有在跑的运行，返回终止数量。
// 同一会话可能同时有多条运行（子 agent、并发请求），逐个取消。
func StopSession(sessionID string) int {
	if sessionID == "" {
		return 0
	}
	runRegistry.mu.Lock()
	handles := make([]*runHandle, 0, len(runRegistry.bySess[sessionID]))
	for id := range runRegistry.bySess[sessionID] {
		h := runRegistry.byRun[id]
		if h == nil || h.cancelled.Swap(true) {
			continue // 已停过的跳过，保证重复调用返回 0
		}
		handles = append(handles, h)
	}
	runRegistry.mu.Unlock()

	for _, h := range handles {
		h.cancel(ErrRunStopped)
	}
	return len(handles)
}

// RunningCount 当前在跑的数量（观测与测试用）。
func RunningCount() int {
	runRegistry.mu.Lock()
	defer runRegistry.mu.Unlock()
	return len(runRegistry.byRun)
}

// handleRunStopSignal 处理来自 Redis 的跨实例停止信号。
//
// 每个实例都会收到同一条信号，只有持有这条 run 的实例能命中；其余实例的台账里
// 没有它（StopRunCause 返回 false），直接忽略即可 —— 不需要「谁在哪台」的路由。
func handleRunStopSignal(sig runledger.StopSignal) {
	if sig.Kind != "" && sig.Kind != runledger.KindAgent {
		return
	}
	if sig.RunID != "" {
		StopRunCause(sig.RunID, sig.SessionID, ErrRunStopped)
		return
	}
	// 会话级停止：run_id 为空时按会话停（多实例下各实例各停自己那部分）
	if sig.SessionID != "" {
		StopSession(sig.SessionID)
	}
}

func init() {
	runledger.SubscribeStop(handleRunStopSignal)
}

// cancelMessage 把终止原因翻成给用户看的一句话。
func cancelMessage(cause error) string {
	switch {
	case errors.Is(cause, ErrRunStopped):
		return "运行已终止（用户请求）"
	case errors.Is(cause, ErrClientGone):
		return "运行已终止（客户端断开）"
	case cause != nil:
		return "运行已终止：" + cause.Error()
	}
	return "运行已终止"
}
