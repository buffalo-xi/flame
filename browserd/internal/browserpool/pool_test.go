package browserpool

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// Fakes: the pool logic must be testable on a machine without Chrome, otherwise the tests fail for the wrong reason.
// ============================================================================

// fakeProc is an in-memory stand-in for a browserhost process.
type fakeProc struct {
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	killed atomic.Bool
	ops    atomic.Int32
}

// newFakeProc builds the pipes; echo makes it answer every stdin line by echoing it on stdout (one line per op, like the
// real driver).
func newFakeProc(echo bool) *fakeProc {
	p := &fakeProc{}
	p.stdinR, p.stdinW = io.Pipe()
	p.stdoutR, p.stdoutW = io.Pipe()
	p.stderrR, p.stderrW = io.Pipe()
	if echo {
		go func() {
			sc := bufio.NewScanner(p.stdinR)
			for sc.Scan() {
				p.ops.Add(1)
				line := append(append([]byte{}, sc.Bytes()...), '\n')
				_, _ = p.stdoutW.Write(line)
			}
		}()
	}
	return p
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProc) Stdout() io.ReadCloser { return p.stdoutR }
func (p *fakeProc) Stderr() io.ReadCloser { return p.stderrR }

func (p *fakeProc) Kill() error {
	p.killed.Store(true)
	_ = p.stdoutW.Close() // end the pump, exactly like a real exit
	return nil
}

func (p *fakeProc) Wait() error { return nil }

// fakeFactory hands out fake processes and remembers them.
type fakeFactory struct {
	mu        sync.Mutex
	procs     []*fakeProc
	echo      bool
	createErr error
}

func (f *fakeFactory) create(_ context.Context, spec workerSpec) (*worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	proc := newFakeProc(f.echo)
	f.procs = append(f.procs, proc)
	return newWorker(spec, proc), nil
}

func (f *fakeFactory) proc(i int) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[i]
}

func testConfig(t *testing.T, workers int) *Config {
	t.Helper()
	return &Config{
		Mode:            ModeProcess,
		Workers:         workers,
		QueueTimeoutSec: 1,
		IdleTimeoutSec:  300,
		// A long janitor interval keeps the ticker out of the way: the tests drive reclamation by hand.
		JanitorSec:    3600,
		WorkspaceRoot: t.TempDir(),
		Browser: BrowserConfig{
			Binary:      "browserhost",
			ProfileRoot: t.TempDir(),
		},
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

// ============================================================================
// Pool
// ============================================================================

// One session keeps one worker: a second call must not spawn a second Chromium (that is the whole point of a session).
func TestAcquireIsSticky(t *testing.T) {
	f := &fakeFactory{echo: true}
	p := newPool(testConfig(t, 2), f)
	defer p.Close()

	l1, err := p.Acquire(context.Background(), "s1", "t0", "")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l2, err := p.Acquire(context.Background(), "s1", "t0", "")
	if err != nil {
		t.Fatalf("acquire again: %v", err)
	}
	if l1.Worker.id != l2.Worker.id {
		t.Errorf("worker changed between calls: %d then %d", l1.Worker.id, l2.Worker.id)
	}
	if got := len(f.procs); got != 1 {
		t.Errorf("spawned %d processes, want 1", got)
	}
}

// A full pool queues, then rejects with a busy error — it must not silently start a third Chromium.
func TestQueueTimeoutWhenFull(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()
	p.queueTimeout = 50 * time.Millisecond

	if _, err := p.Acquire(context.Background(), "s1", "t0", ""); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	_, err := p.Acquire(context.Background(), "s2", "t1", "")
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second acquire = %v, want ErrBusy", err)
	}
	if got := len(f.procs); got != 1 {
		t.Errorf("spawned %d processes, want 1 (the pool is full)", got)
	}
}

// The per-tenant cap is separate from the global one, and it is what keeps one tenant from owning the pool.
func TestPerTenantLimit(t *testing.T) {
	cfg := testConfig(t, 4)
	cfg.MaxPerTenant = 1
	f := &fakeFactory{}
	p := newPool(cfg, f)
	defer p.Close()

	if _, err := p.Acquire(context.Background(), "s1", "t0", ""); err != nil {
		t.Fatalf("first tenant acquire: %v", err)
	}
	_, err := p.Acquire(context.Background(), "s2", "t0", "")
	if !errors.Is(err, ErrTenantBusy) {
		t.Fatalf("same tenant second session = %v, want ErrTenantBusy", err)
	}
	if _, err := p.Acquire(context.Background(), "s3", "t1", ""); err != nil {
		t.Errorf("other tenant acquire: %v", err)
	}
}

// Releasing a session kills its browser and returns the capacity.
func TestReleaseKillsWorkerAndFreesSlot(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()

	if _, err := p.Acquire(context.Background(), "s1", "", ""); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !p.Release("s1") {
		t.Fatal("Release reported nothing to release")
	}
	if !f.proc(0).killed.Load() {
		t.Error("worker process was not killed")
	}
	if got := p.Stats().Sessions; got != 0 {
		t.Errorf("sessions = %d, want 0", got)
	}
	// The profile must not outlive the session, otherwise every release leaks a few hundred megabytes.
	profile := sessionProfileDir(p.cfg, 0)
	waitFor(t, 2*time.Second, func() bool {
		_, statErr := os.Stat(profile)
		return os.IsNotExist(statErr)
	})

	// Capacity must be back: a new session fits in a pool of one.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Acquire(ctx, "s2", "", ""); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// Releasing twice is not an error (the run-end fast path races with the janitor).
func TestReleaseIsIdempotent(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()

	if _, err := p.Acquire(context.Background(), "s1", "", ""); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !p.Release("s1") {
		t.Fatal("first release should report true")
	}
	if p.Release("s1") {
		t.Error("second release should report false")
	}
}

// An idle session is reclaimed; a session in the middle of a call is not.
func TestReapIdle(t *testing.T) {
	cfg := testConfig(t, 2)
	cfg.IdleTimeoutSec = 1
	f := &fakeFactory{}
	p := newPool(cfg, f)
	defer p.Close()

	idle, err := p.Acquire(context.Background(), "idle", "", "")
	if err != nil {
		t.Fatalf("acquire idle: %v", err)
	}
	busy, err := p.Acquire(context.Background(), "busy", "", "")
	if err != nil {
		t.Fatalf("acquire busy: %v", err)
	}
	busy.Begin() // a call is in flight

	p.reapIdle(time.Now().Add(2 * time.Second))

	if _, err := p.Acquire(context.Background(), "idle", "", ""); err != nil {
		t.Fatalf("re-acquire after reap: %v", err)
	}
	if got := p.Stats().Sessions; got != 2 {
		t.Fatalf("sessions = %d, want 2 (idle reclaimed and recreated, busy kept)", got)
	}
	if !f.proc(0).killed.Load() {
		t.Error("the idle worker should have been killed")
	}
	if f.proc(1).killed.Load() {
		t.Error("the busy worker must not be killed while a call is in flight")
	}
	busy.End()
	_ = idle
}

// A worker that dies on its own drops its lease and returns the capacity, so the next call gets a fresh browser.
func TestWorkerDeathReturnsCapacity(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()

	if _, err := p.Acquire(context.Background(), "s1", "", ""); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_ = f.proc(0).Kill()

	waitFor(t, 2*time.Second, func() bool { return p.Stats().Sessions == 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Acquire(ctx, "s1", "", ""); err != nil {
		t.Fatalf("acquire after worker death: %v", err)
	}
}

// A create failure must not leak a slot: without this, one broken spawn shrinks the pool forever.
func TestCreateFailureReturnsSlot(t *testing.T) {
	f := &fakeFactory{createErr: errors.New("cannot start browserhost")}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()
	p.queueTimeout = 100 * time.Millisecond

	if _, err := p.Acquire(context.Background(), "s1", "", ""); err == nil {
		t.Fatal("want an error when the worker cannot start")
	}
	// The slot is back: an intact factory would be able to serve this call, and here the second attempt must reach the
	// factory again instead of timing out on a leaked slot.
	_, err := p.Acquire(context.Background(), "s2", "", "")
	if err == nil || !strings.Contains(err.Error(), "cannot start browserhost") {
		t.Fatalf("second acquire = %v, want the factory error (a leaked slot would give ErrBusy)", err)
	}
}

// A probe or a kill opens the channel without a spec; that must never create a browser. The HTTP handler asks Has
// first — this pins the "does not create" half of that contract.
func TestHasDoesNotCreate(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 2), f)
	defer p.Close()

	if p.Has("s1") {
		t.Fatal("Has reported a session that was never acquired")
	}
	if got := len(f.procs); got != 0 {
		t.Fatalf("Has spawned %d processes, want 0", got)
	}
	if _, err := p.Acquire(context.Background(), "s1", "t0", ""); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !p.Has("s1") {
		t.Fatal("Has missed a live session")
	}
}

// The workspace path comes from the caller, so it must not be able to point the browser outside the shared root.
func TestWorkspacePathMustStayInsideRoot(t *testing.T) {
	cfg := testConfig(t, 1)
	for _, bad := range []string{"../etc", "..", "/etc/passwd", `..\windows`, `C:\Windows`} {
		if _, err := workspaceDir(cfg, 0, bad); err == nil {
			t.Errorf("workspaceDir(%q) = ok, want an error", bad)
		}
	}
	got, err := workspaceDir(cfg, 0, "tenants/t0/users/u1")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}
	if want := filepath.Join(cfg.WorkspaceRoot, "tenants", "t0", "users", "u1"); got != want {
		t.Errorf("workspaceDir = %q, want %q", got, want)
	}
	// No caller-supplied path: a scratch directory under the private profile root — never this process's cwd, and never
	// inside the workspace root, which belongs to nexus.
	scratch, err := workspaceDir(cfg, 7, "")
	if err != nil {
		t.Fatalf("workspaceDir(empty): %v", err)
	}
	if !strings.HasPrefix(scratch, cfg.Browser.ProfileRoot) {
		t.Errorf("scratch dir %q is outside the profile root", scratch)
	}
	if strings.HasPrefix(scratch, cfg.WorkspaceRoot) {
		t.Errorf("scratch dir %q must not live inside the workspace root", scratch)
	}
}

// Downloads follow the session workspace: a downloaded file the model cannot open is a lost file.
func TestDownloadsFollowWorkspace(t *testing.T) {
	f := &fakeFactory{}
	p := newPool(testConfig(t, 1), f)
	defer p.Close()

	l, err := p.Acquire(context.Background(), "s1", "t0", "tenants/t0/ws")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	want := filepath.Join(p.cfg.WorkspaceRoot, "tenants", "t0", "ws", "downloads")
	if l.Worker.downloads != want {
		t.Errorf("downloads dir = %q, want %q", l.Worker.downloads, want)
	}
}

// ============================================================================
// Worker
// ============================================================================

// lockedBuf is a concurrency-safe sink (the pump writes from its own goroutine).
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// stdout is dispatched to the attached connection, and dropped (not written anywhere) while detached.
func TestWorkerStdoutDispatch(t *testing.T) {
	proc := newFakeProc(true)
	w := newWorker(workerSpec{ID: 1, Session: "s1"}, proc)
	w.start()
	defer w.kill()

	buf := &lockedBuf{}
	w.attach(buf)
	defer w.detach()

	if _, err := w.Write([]byte("{\"id\":1,\"op\":\"open\"}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return strings.Contains(buf.String(), `"id":1`) })

	w.detach()
	// Between calls the driver writes nothing, but if it did, the line must be discarded rather than panic on a closed
	// connection.
	_, _ = proc.stdoutW.Write([]byte("{\"id\":2}\n"))
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(buf.String(), `"id":2`) {
		t.Error("a line written while detached must not reach the previous connection")
	}
}

// Writing to a dead worker reports an error instead of hanging or panicking: the caller turns it into a tool error.
func TestWorkerWriteAfterDeath(t *testing.T) {
	proc := newFakeProc(false)
	w := newWorker(workerSpec{ID: 2, Session: "s1"}, proc)
	w.start()
	_ = proc.Kill()

	waitFor(t, 2*time.Second, func() bool { return !w.alive() })
	if _, err := w.Write([]byte("{}")); err == nil {
		t.Fatal("write to a dead worker must fail")
	}
}

// ============================================================================
// Config
// ============================================================================

func TestConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("workers: 3\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != defaultListen {
		t.Errorf("listen = %q, want %q", cfg.Listen, defaultListen)
	}
	if cfg.Mode != ModeProcess {
		t.Errorf("mode = %q, want %q", cfg.Mode, ModeProcess)
	}
	if cfg.Workers != 3 {
		t.Errorf("workers = %d, want 3", cfg.Workers)
	}
	if !cfg.HeadlessDefault() {
		t.Error("headless should default to true")
	}
	if cfg.HeadedAllowed() {
		t.Error("headed should default to false")
	}
	if !filepath.IsAbs(cfg.Browser.ProfileRoot) || !filepath.IsAbs(cfg.WorkspaceRoot) {
		t.Errorf("directory roots must be absolute: %q / %q", cfg.Browser.ProfileRoot, cfg.WorkspaceRoot)
	}
}

// A headed browser without permission to be headed is a configuration mistake: it would fail on the first call instead.
func TestConfigRejectsHeadedWithoutPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "browser:\n  headless: false\n  allow_headed: false\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// LoadConfig accepts it (it is a runtime launch constraint); the factory is what refuses.
	f := &execFactory{cfg: cfg}
	if _, cerr := f.create(context.Background(), workerSpec{ID: 0, ProfileDir: t.TempDir(), DownloadsDir: t.TempDir()}); cerr == nil {
		t.Fatal("creating a headed-by-default worker without allow_headed must fail")
	}
}

func TestConfigRejectsDockerMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: docker\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("mode: docker must be refused until it is implemented")
	}
}
