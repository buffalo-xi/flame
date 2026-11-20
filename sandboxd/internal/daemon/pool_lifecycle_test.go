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

// TestMain initialises logging: without xlog.Init, Info/Error panic (the pool's refiller logs).
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

// These tests touch neither a real codehost nor a real interpreter: a fake isolation implementation (which only speaks
// codehost's ping/pong protocol) pins down the return/refill policy itself — a clean worker is returned and reused, a
// dirty worker is destroyed and refilled.
//
// The lifecycle tiers (reuse / restart) have been removed: the interpreter process is created fresh every time, so
// nothing survives across requests and the return policy has a single tier left (see slotPool.Release).

// fakeIsolate counts starts and answers pings according to the frame protocol.
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

	reqR, reqW := io.Pipe() // the pool writes to reqW (= process stdin), we read reqR
	outR, outW := io.Pipe() // the pool reads outR (= process stdout), we write outW

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

// waitIdle waits for the idle pool to be refilled to want (the refiller is asynchronous).
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

// A clean worker (the protocol exchange completed normally) is returned and the same instance is reused, with no
// refill triggered; a dirty worker (interrupted execution / timeout / protocol error) is always destroyed and the
// refiller restores idle.initial.
func TestPoolReturnsCleanWorker(t *testing.T) {
	p, iso := newTestPool(t, 1)
	waitIdle(t, p, 1)

	w, reused, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !reused {
		t.Fatalf("a prewarmed pool should reuse an idle worker")
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
		t.Errorf("a clean worker should be returned as the same instance: got %s want %s", w2.ID, id)
	}
	if got := iso.startCount(); got != before {
		t.Errorf("returning a clean worker must not start anything extra: %d → %d", before, got)
	}

	// A dirty worker is always destroyed
	tmpDir := w2.tmpDir
	p.Release(w2, false)
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("a dirty worker's tmp dir should be removed, actual stat err=%v", err)
	}
	if fp, ok := w2.proc.(*fakeProcess); !ok || !fp.killed.Load() {
		t.Errorf("a dirty worker should have its process killed")
	}
	waitIdle(t, p, 1) // refilled after destruction, so the pool is not drained
}

// idle = {0, 0} (no prewarming, nothing resident): created on lend, destroyed on return — the pool only starts
// processes on demand along the request path, and the refiller must not top it up every second (that would be a
// regression to "start one, kill one").
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
		t.Errorf("with initial=0 the first borrow must not hit the idle pool")
	}
	if n := len(p.idle); n != 0 {
		t.Errorf("a pool with capacity 0 must not hold objects, got %d", n)
	}

	tmpDir := w.tmpDir
	p.Release(w, true) // the worker is clean — but capacity is 0, so it must still be destroyed
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("with max=0 a return must destroy the worker (its tmp dir should be gone), actual stat err=%v", err)
	}
	if fp, ok := w.proc.(*fakeProcess); !ok || !fp.killed.Load() {
		t.Errorf("with max=0 a return should kill the process")
	}
	if n := len(p.idle); n != 0 {
		t.Errorf("the pool must still hold nothing after the return, got %d", n)
	}

	// With initial=0 the refiller returns immediately: it must not start anything at all.
	time.Sleep(50 * time.Millisecond)
	if got := iso.startCount(); got != 1 {
		t.Errorf("initial=0 must not trigger a refill: starts = %d, want 1", got)
	}
}
