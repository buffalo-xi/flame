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

// These tests target the long-lived interactive registry (interactiveRegistry) itself and never touch a real
// isolation implementation: fakeExec / fakeProc provide controllable processes and verify the semantics of "lazy
// creation / reuse / recreation after a self-exit / removal / idle reclamation / unit lifecycle / destroy on close".
// The two resident goroutines — stdout broadcast and the Wait reaper — live in spawn, and the fakeProc echo and exit
// drive them directly here.

// fakeExec counts Start calls and returns a controllable fakeProc (stdin passthrough to stdout).
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

// fakeProc is a controllable fake process: stdin passthrough to stdout (echo), Wait blocks until exit/Kill.
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

// exit simulates the process exiting on its own (killed is not set, so the "self-exit → reaper removes it" path is
// taken).
func (p *fakeProc) exit() {
	p.exitOnce.Do(func() {
		close(p.exited)
		_ = p.stdinW.Close()
	})
}

// fakeReusable satisfies Sandbox and Reusable at once, simulating docker: Acquire creates the unit, Exec starts
// processes.
type fakeReusable struct {
	mu       sync.Mutex
	acquired int
	units    []*fakeUnit
}

func (f *fakeReusable) Name() string { return "fake-reusable" }

func (f *fakeReusable) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	return nil, errors.New("fake-reusable: Start must not be called (a reusable sandbox goes through unit.Exec)")
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

// testToolCount reads a session's tool count under the lock (avoiding a data race on the map with
// spawn/removeTool).
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

// waitFor polls until the condition holds (reaping / idle reclamation are asynchronous).
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

// The first acquire creates lazily; acquiring the same session+tool again reuses the same process and does not Start
// again.
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
		t.Errorf("reusing a session should return the same process handle")
	}
	if got := ex.startCount(); got != 1 {
		t.Errorf("two acquires should Start only once, got %d", got)
	}
}

// Creating without argv must report an error and produce no process at all.
func TestInteractiveAcquireEmptyArgv(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	if _, err := r.acquire("s1", nil, "shell", nil, sandbox.Limits{}); !errors.Is(err, errInteractiveEmptyArgv) {
		t.Errorf("want errInteractiveEmptyArgv, got %v", err)
	}
	if got := ex.startCount(); got != 0 {
		t.Errorf("an empty argv must not Start, got %d", got)
	}
}

// After the process exits on its own, the reaper goroutine removes it and the next acquire recreates it (a second
// Start).
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
	waitFor(t, "the reaper to remove the tool", func() bool { return testToolCount(r, "s1") == 0 })

	tp2, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	tp2.unlock()

	if tp1 == tp2 {
		t.Errorf("after a self-exit the process should be recreated, not the old handle reused")
	}
	if got := ex.startCount(); got != 2 {
		t.Errorf("recreating after a self-exit should Start a second time, got %d", got)
	}
}

// removeTool (FrameKill semantics): kill the process, and release the session when it becomes empty.
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
		t.Errorf("removeTool should kill the process")
	}
	if r.sessions["s1"] != nil {
		t.Errorf("a session that became empty should be released")
	}
}

// A process past its idle timeout is reclaimed, and the session is released with it.
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
		t.Errorf("an idle timeout should kill the process")
	}
	if r.sessions["s1"] != nil {
		t.Errorf("the session should be released after idle reclamation")
	}
}

// A process currently serving a call (tp.mu held) is not reclaimed — a failing TryLock means skip.
func TestInteractiveReapSkipsActive(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Millisecond)
	defer r.Close()

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Deliberately not unlocking: simulating a process exclusively held by one call
	time.Sleep(20 * time.Millisecond)
	r.reapIdle()

	if ex.proc(0).killed.Load() {
		t.Errorf("a process currently serving a call must not be reclaimed")
	}
	tp.unlock()
}

// Close destroys every session and kills all processes.
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
		t.Errorf("Close should kill the process")
	}
	if len(r.sessions) != 0 {
		t.Errorf("sessions should be empty after Close")
	}
}

// docker (Reusable): Acquire the container when the first process is created, Close it when the last is removed;
// several tools in between share the container.
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
		t.Errorf("two tools should share one container, Acquire called %d times, want 1", got)
	}
	if got := fr.unit(0).count(); got != 2 {
		t.Errorf("two tools should each Exec once, Exec called %d times, want 2", got)
	}

	r.removeTool("s1", "a")
	if fr.unit(0).closed.Load() {
		t.Errorf("tool b is still there, the container must not be Closed")
	}
	r.removeTool("s1", "b")
	if !fr.unit(0).closed.Load() {
		t.Errorf("removing the last tool should Close the container")
	}
}

// stats: the session count / tool process count are correct (the interactive part of the /metrics load snapshot).
func TestInteractiveStats(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	if s, p, a := r.stats(); s != 0 || p != 0 || a != 0 {
		t.Fatalf("initial stats = %d/%d/%d, want 0/0/0", s, p, a)
	}

	// s1 has two tools, s2 has one
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
		t.Fatalf("stats = %d sessions / %d processes / %d active, want 2/3/0", s, p, a)
	}
}

// The active criterion of stats: tp.mu being held (currently serving one call) counts as active, idle does not. It
// comes from the same TryLock probe as reapIdle — the active count returns to zero once the lock is released,
// guaranteeing that /metrics' interactive_active only reflects "calls that are really burning CPU".
func TestInteractiveStatsCountsActive(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistry(ex, time.Hour)
	defer r.Close()

	// s1: a is active (deliberately not unlocked, simulating a call in flight), b is idle; s2: c is idle.
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
		t.Fatalf("stats = %d sessions / %d processes / %d active, want 2/3/1", s, p, a)
	}

	// Once a's lock is released (the call is over), the active count drops to zero.
	tpA.unlock()
	if s, p, a := r.stats(); s != 2 || p != 3 || a != 0 {
		t.Fatalf("stats after a released = %d sessions / %d processes / %d active, want 2/3/0", s, p, a)
	}
}

// writerSwitch: a nil target drops the data, a failing target swallows the error (the persistent output path must not
// be torn down), a normal target passes through.
func TestWriterSwitch(t *testing.T) {
	var sw writerSwitch

	if n, err := sw.Write([]byte("x")); n != 1 || err != nil {
		t.Errorf("a nil target should drop and report success: n=%d err=%v", n, err)
	}

	sw.set(errWriter{})
	if n, err := sw.Write([]byte("x")); n != 1 || err != nil {
		t.Errorf("a failing target should swallow the error: n=%d err=%v", n, err)
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

// The sweep interval is configurable and really takes effect: the real reclaim time falls in (idle, idle+janitor],
// and that upper bound is exactly what the gateway's sticky TTL must cover, so it cannot be a hardcoded constant.
func TestInteractiveJanitorIntervalHonored(t *testing.T) {
	ex := &fakeExec{}
	r := newInteractiveRegistryWithJanitor(ex, 20*time.Millisecond, 0, 10*time.Millisecond)
	defer r.Close()

	if r.janitorInterval != 10*time.Millisecond {
		t.Fatalf("janitorInterval = %v, want 10ms", r.janitorInterval)
	}
	// When ephemeral is not given it falls back to idle (keeping the old semantics)
	if r.ephemeralIdleTimeout != 20*time.Millisecond {
		t.Fatalf("ephemeralIdleTimeout = %v, want the 20ms fallback", r.ephemeralIdleTimeout)
	}

	tp, err := r.acquire("s1", nil, "shell", []string{"sh"}, sandbox.Limits{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tp.unlock() // the call is over; from here only the janitor reclaims it

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessions, _, _ := r.stats(); sessions == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the session should be reclaimed after a 10ms sweep and 20ms idle")
}

// The sweep interval default: when not passed, the package default (1 minute) is used and it must not silently
// become 0 (the ticker would panic).
func TestInteractiveJanitorIntervalDefault(t *testing.T) {
	r := newInteractiveRegistry(&fakeExec{}, time.Hour)
	defer r.Close()
	if r.janitorInterval != interactiveJanitorInterval {
		t.Errorf("janitorInterval = %v, want %v", r.janitorInterval, interactiveJanitorInterval)
	}
}
