package daemon

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// TestMain 初始化日志：xlog 未 Init 时 Info/Error 会 panic（池的 refiller 会打日志）。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sandboxd-test-log")
	if err != nil {
		panic(err)
	}
	if err := xlog.Init(dir, "sandboxd"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// 这些测试不碰真实 codehost / 解释器：用一个假隔离实现（只实现 codehost 的 ping/pong
// 协议），验证池的归还/补量策略本身 —— 干净 worker 归还复用，脏 worker 销毁 + 补量。
//
// lifecycle（reuse / restart）分档已删除：解释器进程每次都新建，没有跨请求残留，
// 归还策略只剩一档（见 slotPool.Release）。

// fakeIsolate 记录启动次数，并按帧协议回应 ping。
type fakeIsolate struct {
	mu      sync.Mutex
	started int
}

func (f *fakeIsolate) Name() string { return "fake" }

func (f *fakeIsolate) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeIsolate) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	f.mu.Lock()
	f.started++
	f.mu.Unlock()

	reqR, reqW := io.Pipe()  // 池写 reqW（= 进程 stdin），我们读 reqR
	outR, outW := io.Pipe()  // 池读 outR（= 进程 stdout），我们写 outW

	p := &fakeProcess{stdin: reqW, stdout: outR, exited: make(chan struct{})}
	go func() {
		defer func() { _ = outW.Close() }()
		for {
			msg, err := recvMsg(reqR)
			if err != nil {
				return
			}
			if msg["action"] == "ping" {
				if err := sendMsg(outW, map[string]any{"action": "pong"}); err != nil {
					return
				}
			}
		}
	}()
	return p, nil
}

type fakeProcess struct {
	stdin   *io.PipeWriter
	stdout  *io.PipeReader
	exitOne sync.Once
	exited  chan struct{}
	killed  atomic.Bool
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeProcess) Stdout() io.ReadCloser { return p.stdout }

func (p *fakeProcess) Wait() error {
	<-p.exited
	return nil
}

func (p *fakeProcess) Kill() error {
	p.killed.Store(true)
	p.exitOne.Do(func() {
		close(p.exited)
		_ = p.stdin.Close()
		_ = p.stdout.Close()
	})
	return nil
}

func newTestPool(t *testing.T, initial int) (*slotPool, *fakeIsolate) {
	t.Helper()
	iso := &fakeIsolate{}
	p := newSlotPool("lua", LangConfig{
		Command:           "fake",
		MaxConcurrent:     4,
		AcquireTimeoutSec: 2,
		Idle:              PoolConfig{Initial: initial, Max: 8},
	}, iso, "codehost-fake", t.TempDir(), sandbox.Limits{})
	t.Cleanup(p.Close)
	return p, iso
}

// waitIdle 等空闲池补到 want（refiller 是异步的）。
func waitIdle(t *testing.T, p *slotPool, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.idle) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("idle pool did not reach %d (got %d)", want, len(p.idle))
}

// 干净 worker（协议层正常走完）归还后复用同一实例，且不触发补量；脏 worker（执行中断 /
// 超时 / 协议错误）一律销毁，由 refiller 补回 idle.initial。
func TestPoolReturnsCleanWorker(t *testing.T) {
	p, iso := newTestPool(t, 1)
	waitIdle(t, p, 1)

	w, reused, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !reused {
		t.Fatalf("预热过的池应当复用空闲 worker")
	}
	id := w.ID
	before := iso.startCount()

	p.Release(w, true)
	waitIdle(t, p, 1)

	w2, _, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if w2.ID != id {
		t.Errorf("干净 worker 应归还同一个实例：got %s want %s", w2.ID, id)
	}
	if got := iso.startCount(); got != before {
		t.Errorf("归还干净 worker 不应有额外启动：%d → %d", before, got)
	}

	// 脏 worker 一律销毁
	tmpDir := w2.tmpDir
	p.Release(w2, false)
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("脏 worker 的 tmp 目录应被删除，实际 stat err=%v", err)
	}
	if fp, ok := w2.proc.(*fakeProcess); !ok || !fp.killed.Load() {
		t.Errorf("脏 worker 应杀进程")
	}
	waitIdle(t, p, 1) // 销毁后补回，避免池被抽干
}

// idle = {0, 0}（不预热、不留驻）：借出时现场起，归还即销毁 ——
// 池只在请求路径上按需起进程，且 refiller 不该每秒补量（那会是"起一个杀一个"的回归）。
func TestPoolIdleZeroDiscardsWorker(t *testing.T) {
	iso := &fakeIsolate{}
	p := newSlotPool("lua", LangConfig{
		Command:           "fake",
		MaxConcurrent:     4,
		AcquireTimeoutSec: 2,
		Idle:              PoolConfig{Initial: 0, Max: 0},
	}, iso, "codehost-fake", t.TempDir(), sandbox.Limits{})
	t.Cleanup(p.Close)

	w, reused, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if reused {
		t.Errorf("initial=0 时首次 borrow 不该命中空闲池")
	}
	if n := len(p.idle); n != 0 {
		t.Errorf("容量为 0 的池里不该有对象，got %d", n)
	}

	tmpDir := w.tmpDir
	p.Release(w, true) // worker 干净 —— 但容量 0，仍必须销毁
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("max=0 时归还应销毁 worker（tmp 目录该消失），实际 stat err=%v", err)
	}
	if fp, ok := w.proc.(*fakeProcess); !ok || !fp.killed.Load() {
		t.Errorf("max=0 时归还应杀进程")
	}
	if n := len(p.idle); n != 0 {
		t.Errorf("归还后池里仍不该有对象，got %d", n)
	}

	// refiller 在 initial=0 时第一件事就是 return：不该有任何补量启动。
	time.Sleep(50 * time.Millisecond)
	if got := iso.startCount(); got != 1 {
		t.Errorf("initial=0 不该触发补量：启动次数 = %d, want 1", got)
	}
}
