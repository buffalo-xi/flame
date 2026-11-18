package daemon

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"flame/tools/sandbox"
)

// interactiveJanitorInterval 空闲回收巡检间隔。
const interactiveJanitorInterval = time.Minute

// errInteractiveEmptyArgv 长交互进程首次创建时缺少 argv。
var errInteractiveEmptyArgv = errors.New("interactive: empty argv")

// BrowserToolID 浏览器工具在长交互注册表里的 tool_id。
//
// 它与 "shell" 平级：同一个 session 的 tools map 里各占一个条目，
// 共享同一个隔离单元（docker）与同一套空闲回收 / kill / 粘性路由。
const BrowserToolID = "browser"

// writerSwitch 可切换目标的写入器。
//
// 长交互进程是持久的，而 stdout/stderr 都要跟随「当前这次调用」的连接回传，
// 所以每次调用开始时把目标切到当前连接的通道，调用结束复位为空。
//
// 两个设计点：
//   - 切换与进程写入之间的竞态，最坏是写到旧连接（已关闭）而丢一次输出，不杀进程；
//   - Write 永远返回成功：目标写失败（客户端断开）不该拆掉这条持久输出路径 ——
//     否则 stdout 广播 goroutine（io.Copy）会因一次写错而退出，进程就没输出了。
type writerSwitch struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *writerSwitch) set(w io.Writer) {
	s.mu.Lock()
	s.w = w
	s.mu.Unlock()
}

func (s *writerSwitch) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.w
	s.mu.Unlock()
	if w == nil {
		return len(p), nil
	}
	_, _ = w.Write(p) // 吞掉写错误：断开只丢这次输出，不杀输出路径
	return len(p), nil
}

// toolProc 一个长交互进程的句柄。
//
// mu 做并发串行化：同一进程只有一根 stdin，同一时刻只服务一次调用。
// 锁与进程生死解耦：unlock 只是「本次调用结束」，不杀进程。
type toolProc struct {
	proc       sandbox.Process
	stdin      io.WriteCloser
	stdoutOut  *writerSwitch // stdout 广播出口：目标 = 当前连接
	stderr     *writerSwitch // stderr 出口（spec.Stderr），目标 = 当前连接
	lastActive time.Time
	mu         sync.Mutex
}

// sessionSandbox 一个 session 的逻辑沙箱。
//
// 三种 mode 统一成同一结构，差异收敛到 unit：
//   - docker：unit 是容器（Acquire），所有 tool 进程 docker exec 进去，共享容器挂载；
//   - nsjail/none：unit 为 nil，每个 tool 进程独立 Start。
type sessionSandbox struct {
	mounts    []sandbox.Mount
	unit      sandbox.Unit
	tools     map[string]*toolProc
	dir       string // 进程工作目录（沙箱内路径，仅首次创建生效）
	ephemeral bool   // 无 session 的临时进程：空闲回收用短超时（run 结束即该销毁）
	mu        sync.Mutex
}

// interactiveRegistry 长交互注册表（非池，有归属）。
//
// 与 slotPool 的本质区别：对象「有归属、不可互换」，不能借还复用，
// 语义是「查得到就用、查不到就建、超时/结束就删」。
type interactiveRegistry struct {
	exec                 sandbox.Sandbox
	reusable             sandbox.Reusable // 非 nil 时走单元复用（docker）
	idleTimeout          time.Duration    // 持久进程（有 session）的空闲兜底
	ephemeralIdleTimeout time.Duration    // 临时进程（无 session）的短兜底
	janitorInterval      time.Duration    // 巡检间隔：决定空闲回收的延迟上界

	mu       sync.Mutex
	sessions map[string]*sessionSandbox

	closed    chan struct{}
	closeOnce sync.Once
}

// newInteractiveRegistry 创建注册表。ephemeralIdleTimeout 可选：不给则与 idleTimeout 相同
// （保持旧测试的调用方式不变）。
func newInteractiveRegistry(exec sandbox.Sandbox, idleTimeout time.Duration, ephemeralIdleTimeout ...time.Duration) *interactiveRegistry {
	eph := idleTimeout
	if len(ephemeralIdleTimeout) > 0 && ephemeralIdleTimeout[0] > 0 {
		eph = ephemeralIdleTimeout[0]
	}
	return newInteractiveRegistryWithJanitor(exec, idleTimeout, eph, 0)
}

// newInteractiveRegistryWithJanitor 同 newInteractiveRegistry，但显式指定巡检间隔
// （0 = 默认 1 分钟）。
//
// 为什么把巡检间隔也做成参数：空闲回收的真实时刻落在 (idle, idle+janitor] 之间，
// 而 gateway 的粘性 TTL 必须覆盖这个上界（sticky_ttl > idle + 2×janitor）。间隔
// 硬编码会让那个不等式在网关侧算不出来，所以它要能从配置进来、并随 capabilities 上报。
func newInteractiveRegistryWithJanitor(exec sandbox.Sandbox, idleTimeout, ephemeralIdleTimeout, janitorInterval time.Duration) *interactiveRegistry {
	eph := ephemeralIdleTimeout
	if eph <= 0 {
		eph = idleTimeout
	}
	if janitorInterval <= 0 {
		janitorInterval = interactiveJanitorInterval
	}
	r := &interactiveRegistry{
		exec:                 exec,
		idleTimeout:          idleTimeout,
		ephemeralIdleTimeout: eph,
		janitorInterval:      janitorInterval,
		sessions:             make(map[string]*sessionSandbox),
		closed:               make(chan struct{}),
	}
	if re, ok := exec.(sandbox.Reusable); ok {
		r.reusable = re
	}
	r.startJanitor()
	return r
}

// acquire 懒建并返回 tool 进程，返回时进程已被锁住（tp.mu），
// 调用方在本次调用结束后必须 unlock 释放。
// acquireOpts acquire 的可选参数（首次创建 session 时生效）。
type acquireOpts struct {
	dir       string // 进程工作目录（沙箱内路径）
	ephemeral bool   // 无 session 的临时进程：用短空闲超时兜底
}

func (r *interactiveRegistry) acquire(sessionID string, mounts []sandbox.Mount, toolID string, argv []string, lim sandbox.Limits, opt ...acquireOpts) (*toolProc, error) {
	o := acquireOpts{}
	if len(opt) > 0 {
		o = opt[0]
	}
	r.mu.Lock()
	sb := r.sessions[sessionID]
	if sb == nil {
		sb = &sessionSandbox{mounts: mounts, tools: make(map[string]*toolProc), dir: o.dir, ephemeral: o.ephemeral}
		r.sessions[sessionID] = sb
	}
	r.mu.Unlock()

	sb.mu.Lock()
	tp := sb.tools[toolID]
	if tp == nil {
		var err error
		tp, err = sb.spawn(r, sessionID, toolID, argv, lim)
		if err != nil {
			sb.mu.Unlock()
			return nil, err
		}
		sb.tools[toolID] = tp
	}
	// lastActive 必须在 sb.mu 内刷新：janitor 的 reapIdleTools 也持 sb.mu，
	// 否则存在「刷新前被 janitor TryLock 抢到并按旧值回收」的窗口，把已 kill 的进程交给调用方。
	tp.lastActive = time.Now()
	sb.mu.Unlock()

	tp.mu.Lock()
	return tp, nil
}

// spawn 在 sessionSandbox 内懒建一个 tool 进程，并起两个常驻 goroutine：
//   - stdout 广播：唯一读 stdout 的读端，把输出写到 stdoutOut（目标 = 当前连接）；
//   - 收尸：Wait() 会启动 stderr 复制 goroutine（spec.Stderr 非 *os.File 时）、
//     reap 进程、释放 cancel，并在进程自退时把它从注册表移除（下次调用重建）。
func (sb *sessionSandbox) spawn(r *interactiveRegistry, sessionID, toolID string, argv []string, lim sandbox.Limits) (*toolProc, error) {
	if len(argv) == 0 {
		return nil, errInteractiveEmptyArgv
	}

	// docker：懒建容器（unit）。建第一个进程时 Acquire，删最后一个时 Close。
	if r.reusable != nil && sb.unit == nil {
		unit, err := r.reusable.Acquire(context.Background(), &sandbox.Spec{Mounts: sb.mounts, Limits: lim})
		if err != nil {
			return nil, err
		}
		sb.unit = unit
	}

	spec := &sandbox.Spec{
		Argv:   argv,
		Dir:    sb.dir,
		Mounts: sb.mounts,
		Limits: lim,
	}
	stderrOut := &writerSwitch{}
	spec.Stderr = stderrOut

	var (
		proc sandbox.Process
		err  error
	)
	if sb.unit != nil {
		proc, err = sb.unit.Exec(context.Background(), spec)
	} else {
		proc, err = r.exec.Start(context.Background(), spec)
	}
	if err != nil {
		return nil, err
	}

	tp := &toolProc{
		proc:       proc,
		stdin:      proc.Stdin(),
		stdoutOut:  &writerSwitch{},
		stderr:     stderrOut,
		lastActive: time.Now(),
	}

	// stdout 广播：每个进程只有一个读 stdout 的 goroutine。不能每个连接各起一个
	// io.Copy 直接读 proc.Stdout() —— 持久进程的 stdout 一直开着，断连后旧读端卡死，
	// 下个连接会并发读同一根管道，把输出撕裂。这里统一读、按当前连接分流。
	go func() {
		_, _ = io.Copy(tp.stdoutOut, proc.Stdout())
	}()

	// 收尸：Wait 做三件事——启动 stderr 复制（不 Wait 的话 stderr 永远到不了客户端）、
	// reap 进程、释放 cancel。返回即进程退出，从注册表移除。
	go func() {
		_ = proc.Wait()
		r.removeTool(sessionID, toolID)
	}()

	return tp, nil
}

// unlock 释放进程锁（本次调用结束，进程保留）。
func (tp *toolProc) unlock() {
	tp.mu.Unlock()
}

// removeTool 移除并回收一个 tool 进程（进程退出或显式删除时），
// 返回该 session 是否已无任何 tool。
//
// 返回值是「粘性绑定该不该解」的判据：一个 session 可以有多个长交互工具，
// 杀掉其中一个不代表会话作废。session 不存在也算空（幂等 kill 的语义）。
func (r *interactiveRegistry) removeTool(sessionID, toolID string) bool {
	r.mu.Lock()
	sb := r.sessions[sessionID]
	r.mu.Unlock()
	if sb == nil {
		return true
	}

	sb.mu.Lock()
	tp := sb.tools[toolID]
	if tp != nil {
		delete(sb.tools, toolID)
	}
	empty := len(sb.tools) == 0
	sb.mu.Unlock()

	if tp != nil {
		_ = tp.proc.Kill()
	}
	if empty {
		r.release(sessionID)
	}
	return empty
}

// release 销毁整个 session 的沙箱。
func (r *interactiveRegistry) release(sessionID string) {
	r.mu.Lock()
	sb := r.sessions[sessionID]
	if sb != nil {
		delete(r.sessions, sessionID)
	}
	r.mu.Unlock()
	if sb != nil {
		sb.destroy()
	}
}

// destroy 销毁 sessionSandbox：kill 所有进程，docker 下 Close 单元。
func (sb *sessionSandbox) destroy() {
	sb.mu.Lock()
	tools := sb.tools
	sb.tools = make(map[string]*toolProc)
	unit := sb.unit
	sb.unit = nil
	sb.mu.Unlock()

	for _, tp := range tools {
		_ = tp.proc.Kill()
	}
	if unit != nil {
		_ = unit.Close()
	}
}

// startJanitor 空闲回收巡检（间隔见 janitorInterval，缺省 1 分钟）。
func (r *interactiveRegistry) startJanitor() {
	go func() {
		t := time.NewTicker(r.janitorInterval)
		defer t.Stop()
		for {
			select {
			case <-r.closed:
				return
			case <-t.C:
				r.reapIdle()
			}
		}
	}()
}

// reapIdle 回收空闲超时的 tool 进程，以及因此变空的 session。
func (r *interactiveRegistry) reapIdle() {
	now := time.Now()

	r.mu.Lock()
	sessions := make(map[string]*sessionSandbox, len(r.sessions))
	for sid, sb := range r.sessions {
		sessions[sid] = sb
	}
	r.mu.Unlock()

	for sid, sb := range sessions {
		// 临时进程（无 session）用短超时：run 结束就尽快回收；持久进程用长超时。
		timeout := r.idleTimeout
		if sb.ephemeral {
			timeout = r.ephemeralIdleTimeout
		}
		if sb.reapIdleTools(now, timeout) {
			r.mu.Lock()
			if r.sessions[sid] == sb {
				delete(r.sessions, sid)
			}
			r.mu.Unlock()
			sb.destroy()
		}
	}
}

// reapIdleTools 回收空闲超时的 tool 进程，返回 session 是否已空。
// 正在服务调用的进程（TryLock 失败）跳过，不回收。
func (sb *sessionSandbox) reapIdleTools(now time.Time, timeout time.Duration) bool {
	var idle []*toolProc
	sb.mu.Lock()
	for id, tp := range sb.tools {
		if !tp.mu.TryLock() {
			continue // 正在服务调用，跳过
		}
		if now.Sub(tp.lastActive) > timeout {
			delete(sb.tools, id)
			idle = append(idle, tp)
		}
		tp.mu.Unlock()
	}
	empty := len(sb.tools) == 0
	sb.mu.Unlock()

	for _, tp := range idle {
		_ = tp.proc.Kill()
	}
	return empty
}

// Close 关闭注册表：停止 janitor 并销毁所有 session。
func (r *interactiveRegistry) Close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		r.mu.Lock()
		sessions := make([]*sessionSandbox, 0, len(r.sessions))
		for sid, sb := range r.sessions {
			delete(r.sessions, sid)
			sessions = append(sessions, sb)
		}
		r.mu.Unlock()
		for _, sb := range sessions {
			sb.destroy()
		}
	})
}

// stats 返回当前 session 数、tool 进程总数、活跃进程数（/metrics 负载快照用）。
//
// 只读快照：锁顺序与 reapIdle 一致 —— r.mu 只用来拷贝 session 引用、随即释放，
// 再逐个 sb.mu 读 tools，避免 r.mu 与 sb.mu 嵌套持有。
//
// 「活跃」= tp.mu 被持有（正服务一次调用，TryLock 探测）。空闲长交互进程不烧
// CPU，gateway 应只用活跃数计重、把空闲进程排除出负载。
func (r *interactiveRegistry) stats() (sessions, processes, active int) {
	r.mu.Lock()
	sbs := make([]*sessionSandbox, 0, len(r.sessions))
	for _, sb := range r.sessions {
		sbs = append(sbs, sb)
	}
	sessions = len(sbs)
	r.mu.Unlock()

	for _, sb := range sbs {
		sb.mu.Lock()
		for _, tp := range sb.tools {
			processes++
			if !tp.mu.TryLock() {
				active++ // TryLock 失败 = tp.mu 被持有 = 正在服务调用
			} else {
				tp.mu.Unlock()
			}
		}
		sb.mu.Unlock()
	}
	return
}
