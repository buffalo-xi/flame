package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// 这些测试只针对长交互注册表（interactiveRegistry）本身，不碰真实隔离实现：
// fakeExec / fakeProc 提供可控的进程，验证「懒建/复用 / 自退重建 / 移除 / 空闲回收 /
// 单元生命周期 / 关闭销毁」这几条语义。stdout 广播与 Wait 收尸两个常驻 goroutine
// 在 spawn 里，这里通过 fakeProc 的 echo 与 exit 直接驱动它们。

// fakeExec 记录 Start 次数，返回可控的 fakeProc（stdin→stdout 直通）。
type fakeExec struct {
	mu       sync.Mutex
	started  int
	procs    []*fakeProc
	startErr error
}

func (f *fakeExec) Name() string { return "fake" }

func (f *fakeExec) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started++
	p := newFakeProc()
	f.procs = append(f.procs, p)
	return p, nil
}

func (f *fakeExec) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeExec) proc(i int) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[i]
}

// fakeProc 可控假进程：stdin → stdout 直通（echo），Wait 阻塞到 exit/Kill。
type fakeProc struct {
	stdinW   *io.PipeWriter
	stdoutR  *io.PipeReader
	exited   chan struct{}
	exitOnce sync.Once
	killed   atomic.Bool
}

func newFakeProc() *fakeProc {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &fakeProc{stdinW: inW, stdoutR: outR, exited: make(chan struct{})}
	go func() {
		defer outW.Close()
		_, _ = io.Copy(outW, inR) // echo stdin → stdout
	}()
	return p
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProc) Stdout() io.ReadCloser { return p.stdoutR }

func (p *fakeProc) Wait() error {
	<-p.exited
	return nil
}

func (p *fakeProc) Kill() error {
	p.killed.Store(true)
	p.exitOnce.Do(func() {
		close(p.exited)
		_ = p.stdinW.Close()
	})
	return nil
}

// exit 模拟进程自己退出（不标 killed，走「自退 → 收尸移除」路径）。
func (p *fakeProc) exit() {
	p.exitOnce.Do(func() {
		close(p.exited)
		_ = p.stdinW.Close()
	})
}

// fakeReusable 同时满足 Sandbox 与 Reusable，模拟 docker：Acquire 建单元、Exec 起进程。
type fakeReusable struct {
	mu       sync.Mutex
	acquired int
	units    []*fakeUnit
}

func (f *fakeReusable) Name() string { return "fake-reusable" }

func (f *fakeReusable) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	return nil, errors.New("fake-reusable: Start must not be called (reusable 走 unit.Exec)")
}

func (f *fakeReusable) Acquire(ctx context.Context, spec *sandbox.Spec) (sandbox.Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired++
	u := &fakeUnit{}
	f.units = append(f.units, u)
	return u, nil
}

func (f *fakeReusable) acquireCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired
}

func (f *fakeReusable) unit(i int) *fakeUnit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.units[i]
}

type fakeUnit struct {
	mu        sync.Mutex
	execCount int
	closed    atomic.Bool
}

func (u *fakeUnit) Exec(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.execCount++
	return newFakeProc(), nil
}

func (u *fakeUnit) Close() error { u.closed.Store(true); return nil }
func (u *fakeUnit) Name() string { return "fake-unit" }

func (u *fakeUnit) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.execCount
}

// testToolCount 加锁读某个 session 的 tool 数（避免与 spawn/removeTool 并发访问 map 竞态）。
func testToolCount(r *interactiveRegistry, sid string) int {
	r.mu.Lock()
	sb := r.sessions[sid]
	r.mu.Unlock()
	if sb == nil {
		return 0
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return len(sb.tools)
}

// waitFor 轮询直到条件成立（收尸 / 空闲回收是异步的）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// 首次 acquire 懒建；同一 session+tool 再次 acquire 复用同一进程、不再 Start。
func TestInteractiveAcquireReusesProcess(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	tp1, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp1.unlock()

	tp2, err := r.acquire("s1", nil, "shell", nil, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	tp2.unlock()

	if tp1 != tp2 {
		t.Errorf("复用会话应返回同一进程句柄")
	}
	if got := ex.startCount(); got != 1 {
		t.Errorf("两次 acquire 只该 Start 一次，got %d", got)
	}
}

// 首次创建缺 argv 必须报错，且不产生任何进程。
func TestInteractiveAcquireEmptyArgv(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	if _, err := r.acquire("s1", nil, "shell", nil, sandbox.Limits{}); !errors.Is(err, errInteractiveEmptyArgv) {
		t.Errorf("want errInteractiveEmptyArgv, got %v", err)
	}
	if got := ex.startCount(); got != 0 {
		t.Errorf("空 argv 不该 Start，got %d", got)
	}
}

// 进程自退后，收尸 goroutine 把它移除，下次 acquire 重建（Start 第二次）。
func TestInteractiveRecreatesAfterExit(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	tp1, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp1.unlock()

	ex.proc(0).exit()
	waitFor(t, "收尸移除 tool", func() bool { return testToolCount(r, "s1") == 0 })

	tp2, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	tp2.unlock()

	if tp1 == tp2 {
		t.Errorf("进程自退后应重建，而不是复用旧句柄")
	}
	if got := ex.startCount(); got != 2 {
		t.Errorf("自退后重建应 Start 第二次，got %d", got)
	}
}

// removeTool（FrameKill 语义）：kill 进程，session 变空则一并释放。
func TestInteractiveRemoveToolKills(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp.unlock()

	r.removeTool("s1", "shell")

	if !ex.proc(0).killed.Load() {
		t.Errorf("removeTool 应 kill 进程")
	}
	if r.sessions["s1"] != nil {
		t.Errorf("变空的 session 应被释放")
	}
}

// 空闲超时的进程被回收，session 随之释放。
func TestInteractiveReapIdle(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, 20*time.Millisecond)
	defer r.Close()

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp.unlock()

	time.Sleep(40 * time.Millisecond)
	r.reapIdle()

	if !ex.proc(0).killed.Load() {
		t.Errorf("空闲超时应 kill 进程")
	}
	if r.sessions["s1"] != nil {
		t.Errorf("空闲回收后 session 应被释放")
	}
}

// 正在服务调用的进程（tp.mu 被持有）不被空闲回收 —— TryLock 失败即跳过。
func TestInteractiveReapSkipsActive(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Millisecond)
	defer r.Close()

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// 故意不 unlock：模拟进程正被一次调用独占
	time.Sleep(20 * time.Millisecond)
	r.reapIdle()

	if ex.proc(0).killed.Load() {
		t.Errorf("正在服务的进程不该被空闲回收")
	}
	tp.unlock()
}

// Close 销毁全部 session 并 kill 所有进程。
func TestInteractiveCloseDestroys(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp.unlock()

	r.Close()

	if !ex.proc(0).killed.Load() {
		t.Errorf("Close 应 kill 进程")
	}
	if len(r.sessions) != 0 {
		t.Errorf("Close 后 sessions 应为空")
	}
}

// docker（Reusable）：建第一个进程时 Acquire 容器、删最后一个时 Close；中间多工具共享容器。
func TestInteractiveDockerUnitLifecycle(t *testing.T) {
	fr := &fakeReusable{}
	r := newInteractiveRegistry(fr, time.Hour)
	defer r.Close()

	t1, err := r.acquire("s1", nil, "a", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	t1.unlock()

	t2, err := r.acquire("s1", nil, "b", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	t2.unlock()

	if got := fr.acquireCount(); got != 1 {
		t.Errorf("两个工具应共用一个容器，Acquire %d 次，want 1", got)
	}
	if got := fr.unit(0).count(); got != 2 {
		t.Errorf("两个工具应各 Exec 一次，Exec %d 次，want 2", got)
	}

	r.removeTool("s1", "a")
	if fr.unit(0).closed.Load() {
		t.Errorf("还剩工具 b，容器不该被 Close")
	}
	r.removeTool("s1", "b")
	if !fr.unit(0).closed.Load() {
		t.Errorf("最后一个工具移除后应 Close 容器")
	}
}

// stats：session 数 / tool 进程数计数正确（/metrics 负载快照的 interactive 部分）。
func TestInteractiveStats(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	if s, p, a := r.stats(); s != 0 || p != 0 || a != 0 {
		t.Fatalf("初始 stats = %d/%d/%d，want 0/0/0", s, p, a)
	}

	// s1 两个 tool、s2 一个 tool
	for _, tc := range []struct{ sid, tool string }{
		{"s1", "a"}, {"s1", "b"}, {"s2", "c"},
	} {
		tp, err := r.acquire(tc.sid, nil, tc.tool, []string{"sh"}, sandbox.Limits{})
		if err != nil {
			t.Fatalf("acquire %s/%s: %v", tc.sid, tc.tool, err)
		}
		tp.unlock()
	}

	if s, p, a := r.stats(); s != 2 || p != 3 || a != 0 {
		t.Fatalf("stats = %d sessions / %d processes / %d active，want 2/3/0", s, p, a)
	}
}

// stats 的 active 口径：tp.mu 被持有（正服务一次调用）计活跃，空闲不计。
// 与 reapIdle 的 TryLock 探针同源 —— 释放锁后活跃数归零，保证 /metrics 的
// interactive_active 只反映「真正在烧 CPU 的调用」。
func TestInteractiveStatsCountsActive(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	// s1: a 活跃（故意不 unlock，模拟正服务调用）、b 空闲；s2: c 空闲。
	tpA, err := r.acquire("s1", nil, "a", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}

	tpB, err := r.acquire("s1", nil, "b", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	tpB.unlock()

	tpC, err := r.acquire("s2", nil, "c", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire c: %v", err)
	}
	tpC.unlock()

	if s, p, a := r.stats(); s != 2 || p != 3 || a != 1 {
		t.Fatalf("stats = %d sessions / %d processes / %d active，want 2/3/1", s, p, a)
	}

	// 释放 a 的锁（本次调用结束）后，活跃数归零。
	tpA.unlock()
	if s, p, a := r.stats(); s != 2 || p != 3 || a != 0 {
		t.Fatalf("a 释放后 stats = %d sessions / %d processes / %d active，want 2/3/0", s, p, a)
	}
}

// writerSwitch：nil 目标丢弃、目标写失败吞错（不能拆掉持久输出路径）、正常目标透传。
func TestWriterSwitch(t *testing.T) {
	var sw writerSwitch

	if n, err := sw.Write([]byte("x")); n != 1 || err != nil {
		t.Errorf("nil 目标应丢弃并返回成功：n=%d err=%v", n, err)
	}

	sw.set(errWriter{})
	if n, err := sw.Write([]byte("x")); n != 1 || err != nil {
		t.Errorf("目标写失败应吞错：n=%d err=%v", n, err)
	}

	var buf bytes.Buffer
	sw.set(&buf)
	if _, err := sw.Write([]byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.String() != "hi" {
		t.Errorf("buf = %q, want hi", buf.String())
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("boom") }

// 巡检间隔可配且真的生效：空闲回收的真实时刻落在 (idle, idle+janitor]，
// 这个上界正是 gateway 粘性 TTL 要覆盖的东西，所以它不能是硬编码的常数。
func TestInteractiveJanitorIntervalHonored(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistryWithJanitor(ex, 20*time.Millisecond, 0, 10*time.Millisecond)
	defer r.Close()

	if r.janitorInterval != 10*time.Millisecond {
		t.Fatalf("janitorInterval = %v, want 10ms", r.janitorInterval)
	}
	// ephemeral 未指定时回落到 idle（保持旧语义）
	if r.ephemeralIdleTimeout != 20*time.Millisecond {
		t.Fatalf("ephemeralIdleTimeout = %v, want 回落 20ms", r.ephemeralIdleTimeout)
	}

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp.unlock() // 本次调用结束，之后只靠 janitor 回收

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessions, _, _ := r.stats(); sessions == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("10ms 巡检 + 20ms 空闲后 session 应被回收")
}

// 巡检间隔缺省值：不传时用包内默认（1 分钟），别静默变成 0（ticker 会 panic）。
func TestInteractiveJanitorIntervalDefault(t *testing.T) {
	r := newInteractiveRegistry(&fakeExec{}, time.Hour)
	defer r.Close()
	if r.janitorInterval != interactiveJanitorInterval {
		t.Errorf("janitorInterval = %v, want %v", r.janitorInterval, interactiveJanitorInterval)
	}
}
