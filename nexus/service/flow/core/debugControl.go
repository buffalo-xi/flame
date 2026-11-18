package flow

import (
	"sync"
)

// DebugMode 调试运行模式
type DebugMode string

const (
	// DebugRun 一路跑完，节点前不挂起
	DebugRun DebugMode = "run"
	// DebugPause 每个节点进入前都挂起（等待 resume）
	DebugPause DebugMode = "pause"
	// DebugStep 按 stepKind 决定挂起点（over/in/out）
	DebugStep DebugMode = "step"
)

// DebugStepKind 单步类型
type DebugStepKind string

const (
	// StepOver 执行完当前节点后，在同级下一节点前挂起
	StepOver DebugStepKind = "over"
	// StepIn 进入子 process 的第一个节点前挂起
	StepIn DebugStepKind = "in"
	// StepOut 跑到当前子 process 结束回到父 process 后，在父下一节点前挂起
	StepOut DebugStepKind = "out"
)

// DebugSnapshot 挂起时回推的快照，供前端高亮节点 + 展示变量
type DebugSnapshot struct {
	NodeID   string                 `json:"node_id"`
	NodeType string                 `json:"node_type"`
	Process  string                 `json:"process"`
	Depth    int                    `json:"depth"`
	Vars     map[string]interface{} `json:"vars,omitempty"`
	Stack    []string               `json:"stack,omitempty"`
}

// DebugControl 工作流执行调试控制器。
// 在 execute() 递归进入每个节点前调用 SuspendIfNeeded 决定是否挂起；
// 挂起时通过 eventHook 回推快照，并阻塞在 resume channel 直到收到控制信号。
type DebugControl struct {
	mu sync.Mutex

	// Enabled 是否启用调试模式（OnConnect 校验通过后置 true）
	Enabled bool
	// mode 当前运行模式
	mode DebugMode
	// stepKind 当 mode=step 时生效
	stepKind DebugStepKind
	// cond 挂起/唤醒的条件变量（mu 为其 Locker）
	cond *sync.Cond
	// suspended 当前是否处于挂起（等待放行）。替代一次性 chan，避免信号丢失。
	suspended bool
	// stopFlag 收到 stop，放行后由调用方靠 StdCtx 取消收尾
	stopFlag bool
	// stepOutDepth step out 时等待回退到的深度（>=该深度才允许再次挂起）
	stepOutDepth int
	// stepOverDepth step over 时记录的当前深度，仅在 == 该深度时挂起一次；
	// process 内部更深层（子流程）不挂起，从而“步过”直接跑完整节点
	stepOverDepth int
	// breakpoints 命中的断点 nodeID 集合
	breakpoints map[string]bool
	// eventHook 挂起时回调（由 WS 调试源注入，向客户端推送快照）
	eventHook func(*DebugSnapshot)
	// execHook 节点开始执行时回调（由 WS 调试源注入，向前端标记「已运行」节点）。
	// 无论是否挂起都会调用，用于让前端高亮所有已执行过的节点。
	execHook func(nodeID string, nodeName string)
	// failHook 节点执行失败时回调（由 WS 调试源注入，向前端标记「出错」节点）。
	failHook func(nodeID string, nodeName string, errMsg string)
	// skipHook 节点被跳过时回调（由 WS 调试源注入，向前端标记「跳过」节点）。
	skipHook func(nodeID string, nodeName string)
	// doneHook 节点执行完成时回调（由 WS 调试源注入，用于累积该节点最新数据）。
	doneHook func(nr *NodeResult)

	// stack 当前调用栈（processID 链），仅调试模式下维护
	stack []string
}

// NewDebugControl 创建调试控制器（默认 run 模式，不挂起）
func NewDebugControl() *DebugControl {
	d := &DebugControl{
		mode:        DebugRun,
		breakpoints: make(map[string]bool),
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// SetEventHook 注入快照回推回调
func (d *DebugControl) SetEventHook(hook func(*DebugSnapshot)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventHook = hook
}

// SetExecHook 注入节点开始执行的回调（用于标记「已运行」节点）
func (d *DebugControl) SetExecHook(hook func(nodeID string, nodeName string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execHook = hook
}

// SetFailHook 注入节点执行失败的回调（用于标记「出错」节点）
func (d *DebugControl) SetFailHook(hook func(nodeID string, nodeName string, errMsg string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failHook = hook
}

// SetSkipHook 注入节点被跳过的回调（用于标记「跳过」节点）
func (d *DebugControl) SetSkipHook(hook func(nodeID string, nodeName string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.skipHook = hook
}

// SetDoneHook 注入节点执行完成的回调（用于累积节点最新数据）
func (d *DebugControl) SetDoneHook(hook func(nr *NodeResult)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.doneHook = hook
}

// SetBreakpoint 增删断点
func (d *DebugControl) SetBreakpoint(nodeID string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if on {
		d.breakpoints[nodeID] = true
	} else {
		delete(d.breakpoints, nodeID)
	}
}

// pushStack / popStack 维护调用栈（调试模式）
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

// shouldSuspend 纯逻辑判断：当前节点是否应在执行前挂起。
// 不修改状态、不加锁（调用方持锁）。
func (d *DebugControl) shouldSuspend(nodeID string, depth int) bool {
	// 断点优先：任何模式下命中断点都挂起（与主流调试器一致）。
	// 步出期间遇到断点 → 停在断点；resume 后 mode 已被切回 run，继续普通运行。
	if d.breakpoints[nodeID] {
		return true
	}

	switch d.mode {
	case DebugPause:
		return true
	case DebugStep:
		switch d.stepKind {
		case StepOver:
			// 仅在当前深度挂起一次（process 内部更深层不挂，直接跑完本节点）
			return depth == d.stepOverDepth
		case StepIn:
			// 进入任意深度（含 process 内部第一个节点）都挂起一次
			return true
		case StepOut:
			// 仅在深度回到 stepOutDepth（或更浅）时才挂起
			return depth <= d.stepOutDepth
		}
	case DebugRun:
		return false
	}
	return false
}

// SuspendIfNeeded 在节点执行前调用。返回时表示已获得继续执行许可。
// node: 即将执行的节点；processID: 当前 process；depth: 当前递归深度。
func (d *DebugControl) SuspendIfNeeded(node *Node, processID string, depth int) {
	d.mu.Lock()
	if !d.Enabled {
		d.mu.Unlock()
		return
	}
	// start（流程根入口）、entry（容器内入口）与 layout（纯占位）都不真正执行：
	// 跳过（不标记已运行、不挂起、不命中断点）。
	if node != nil && (node.Type == string(NodeTypeStart) ||
		node.Type == string(NodeTypeEntry) ||
		node.Type == string(NodeTypeLayout)) {
		d.mu.Unlock()
		return
	}
	// 取出节点开始执行的回调，先 unlock 再调用（hook 会写 WS，不宜持锁）。
	execHook := d.execHook
	d.mu.Unlock()

	// 无论是否挂起，都标记该节点已开始执行（前端据此高亮「已运行」节点）
	if execHook != nil {
		if node != nil {
			execHook(node.ID, node.Label())
		}
	}

	d.mu.Lock()
	// run 模式的断点命中
	if d.shouldSuspend(node.ID, depth) {
		// 准备快照
		snap := &DebugSnapshot{
			NodeID:   node.ID,
			NodeType: node.Type,
			Process:  processID,
			Depth:    depth,
			Stack:    append([]string{}, d.stack...),
		}
		hook := d.eventHook
		// step 模式下挂起一次后恢复 run（over/in）：下一次进入直接放行
		if d.mode == DebugStep {
			if d.stepKind == StepOver || d.stepKind == StepIn {
				d.mode = DebugRun
			}
			// step out 在挂起后被 resume，恢复 run
			if d.stepKind == StepOut {
				d.mode = DebugRun
			}
		}
		// 标记挂起，交给下面的 cond 等待循环统一处理（信号不会丢失）
		d.suspended = true
		d.mu.Unlock()

		if hook != nil {
			hook(snap)
		}
		// 阻塞等待放行：用 cond 循环等待，唤醒后重新检查状态位，
		// 避免「unlock 后、<-chan 前」Resume/Stop 到达导致信号丢失的竞态。
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

// WaitIfGloballySuspended 全停屏障：若有节点已触发挂起（suspended=true），
// 当前节点（无论自身是否命中断点）在此阻塞，直到 Resume/Stop 广播唤醒。
// 用于实现 all-stop：断点命中后冻结所有并发分支，避免多挂起点同时等待的竞态。
// 检查与阻塞在同一把锁下原子完成，避免「同时检查、同时通过」的竞态。
func (d *DebugControl) WaitIfGloballySuspended() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.suspended && !d.stopFlag {
		d.cond.Wait()
	}
}

// Resume 唤醒挂起（run/pause 模式下持续放行；step 模式下仅放行一次）
func (d *DebugControl) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.suspended = false
	d.cond.Broadcast()
}

// Control 接收控制指令（来自 WS）
func (d *DebugControl) Control(mode DebugMode, stepKind DebugStepKind) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode = mode
	d.stepKind = stepKind
	if mode == DebugStep {
		switch stepKind {
		case StepOut:
			// step out：记录当前深度 -1（父 process 深度），待回退到父层级后才挂起。
			// 注意：在并发子流程场景下，同深度的兄弟 process 中的节点也可能触发挂起，
			// 因为判断条件仅基于 depth 而非具体 processID。这是可接受的折中：
			// 调试器在并发场景下 step-out 会停在同层级的最近一个节点。
			d.stepOutDepth = len(d.stack) - 1
		case StepOver:
			// step over：记录当前深度，仅在该深度挂起一次；
			// process 内部更深层直接放行，实现“步过=跑完整节点”。
			d.stepOverDepth = len(d.stack)
		}
	}
}

// Stop 停止执行（置 stopFlag 并广播，唤醒阻塞中的挂起点）
func (d *DebugControl) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopFlag = true
	d.suspended = false
	d.cond.Broadcast()
}
