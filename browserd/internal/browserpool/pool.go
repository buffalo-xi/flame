package browserpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flame/tools/xlog"
)

// Errors returned to the caller (they travel to the model as a tool error, so each one says what to do).
var (
	ErrClosed = errors.New("browser pool is closed")
	ErrBusy   = errors.New("no free browser worker: every session is busy and the queue timed out (raise workers)")
	// ErrTenantBusy is deliberately distinct from ErrBusy: "you are at your own limit" is actionable by the tenant,
	// "the pool is full" is actionable by the operator.
	ErrTenantBusy = errors.New("this tenant already holds the maximum number of browser sessions (max_per_tenant)")
)

// selftestTimeout caps the version probe: it only runs `<browserhost> --selftest`, which never launches Chromium.
const selftestTimeout = 30 * time.Second

// SelfTest is the browser capability probe result.
//
// It is a probe, not a config statement: whether Chromium is actually installed (and which version) is only known by
// running the driver's self-test. On Windows the version is deliberately "unknown" (see browserhost.SelfTest), so a
// missing version must not be read as "unavailable".
type SelfTest struct {
	Engine  string `json:"engine,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Lease is one session's claim on one worker.
type Lease struct {
	Session string
	Tenant  string
	Worker  *worker

	// mu is held for the duration of one call: a browserhost serves one JSON op at a time, and serialising here keeps
	// two concurrent calls from interleaving their requests and responses.
	mu         sync.Mutex
	busy       atomic.Bool
	lastActive atomic.Int64 // unix nanos
}

// Begin takes the call lock (one call at a time per session).
func (l *Lease) Begin() {
	l.mu.Lock()
	l.busy.Store(true)
	l.touch()
}

// End releases the call lock and refreshes the idle clock.
func (l *Lease) End() {
	l.touch()
	l.busy.Store(false)
	l.mu.Unlock()
}

func (l *Lease) touch() { l.lastActive.Store(time.Now().UnixNano()) }

func (l *Lease) idleFor(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, l.lastActive.Load()))
}

// Pool owns the workers and the leases.
type Pool struct {
	cfg *Config
	fac factory

	slots    chan struct{} // capacity = Workers
	closedCh chan struct{}
	queue    atomic.Int32
	selftest atomic.Pointer[SelfTest]

	// queueTimeout mirrors cfg.QueueTimeout() and is a field of its own so tests can use milliseconds instead of
	// waiting a whole configured second.
	queueTimeout time.Duration

	mu        sync.Mutex
	leases    map[string]*Lease
	perTenant map[string]int
	nextID    int
	closed    bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a pool that launches real browsers.
func New(cfg *Config) *Pool {
	p := newPool(cfg, &execFactory{cfg: cfg})
	go p.probe()
	return p
}

func newPool(cfg *Config, fac factory) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		cfg:          cfg,
		fac:          fac,
		slots:        make(chan struct{}, cfg.Workers),
		closedCh:     make(chan struct{}),
		leases:       make(map[string]*Lease),
		perTenant:    make(map[string]int),
		queueTimeout: cfg.QueueTimeout(),
		ctx:          ctx,
		cancel:       cancel,
	}
	p.wg.Add(1)
	go p.janitor()
	return p
}

// Has reports whether the session currently owns a browser. It exists so the caller can answer "kill this" without
// creating one: acquiring to probe would spawn a Chromium (hundreds of megabytes, seconds of latency) just to kill it.
func (p *Pool) Has(session string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases[session] != nil
}

// Acquire returns the lease of this session, creating a worker if there is capacity.
//
// workDir is the session's workspace **relative to WorkspaceRoot** (the caller's storage key); it becomes the driver's
// cwd, so screenshots and downloads land in the tenant's directory instead of this service's. An empty value means "the
// caller did not say": the session then gets a scratch directory under the workspace root, never this process's cwd.
//
// Contract: the lease is returned unlocked; the caller must Begin/End around each call. The lease survives a
// disconnected call — that is the point of session affinity — and is reclaimed by the idle janitor.
func (p *Pool) Acquire(ctx context.Context, session, tenant, workDir string) (*Lease, error) {
	if session == "" {
		return nil, errors.New("session_id is required")
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if l := p.leases[session]; l != nil {
		l.touch()
		p.mu.Unlock()
		return l, nil
	}
	if p.cfg.MaxPerTenant > 0 && p.perTenant[tenant] >= p.cfg.MaxPerTenant {
		n := p.perTenant[tenant]
		p.mu.Unlock()
		return nil, fmt.Errorf("%w (%d/%d)", ErrTenantBusy, n, p.cfg.MaxPerTenant)
	}
	p.mu.Unlock()

	// Capacity: queue instead of failing fast (short bursts are normal), but never wait forever.
	p.queue.Add(1)
	defer p.queue.Add(-1)
	timer := time.NewTimer(p.queueTimeout)
	defer timer.Stop()
	select {
	case p.slots <- struct{}{}:
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closedCh:
		return nil, ErrClosed
	}

	// Re-check after the wait: another call for the same session may have created the lease meanwhile, and then this
	// slot must go back rather than be silently held.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.releaseSlot()
		return nil, ErrClosed
	}
	if l := p.leases[session]; l != nil {
		l.touch()
		p.mu.Unlock()
		p.releaseSlot()
		return l, nil
	}
	if p.cfg.MaxPerTenant > 0 && p.perTenant[tenant] >= p.cfg.MaxPerTenant {
		p.mu.Unlock()
		p.releaseSlot()
		return nil, fmt.Errorf("%w (%d/%d)", ErrTenantBusy, p.perTenant[tenant], p.cfg.MaxPerTenant)
	}
	id := p.nextID
	p.nextID++
	p.mu.Unlock()

	wsDir, err := workspaceDir(p.cfg, id, workDir)
	if err != nil {
		p.releaseSlot()
		return nil, err
	}
	spec := workerSpec{
		ID:           id,
		Session:      session,
		Tenant:       tenant,
		WorkspaceDir: wsDir,
		DownloadsDir: filepath.Join(wsDir, "downloads"),
		ProfileDir:   sessionProfileDir(p.cfg, id),
	}
	// The worker belongs to the pool, not to this request: creating it with the request context would kill the
	// browser the moment the HTTP call returns.
	w, err := p.fac.create(p.ctx, spec)
	if err != nil {
		p.releaseSlot()
		return nil, err
	}

	l := &Lease{Session: session, Tenant: tenant, Worker: w}
	l.touch()
	w.onExit = func(reason string) { p.dropDead(session, l, reason) }
	w.start()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.releaseSlot()
		w.kill()
		return nil, ErrClosed
	}
	p.leases[session] = l
	p.perTenant[tenant]++
	sessions := len(p.leases)
	p.mu.Unlock()

	xlog.Info("browserd: session %s (tenant %q) leased worker %d [%d/%d]", session, tenant, id, sessions, p.cfg.Workers)
	return l, nil
}

// Release destroys the session's browser and returns its capacity.
//
// Idempotent: releasing a session that is not there is not an error (the fast path at run end races with the janitor).
func (p *Pool) Release(session string) bool {
	p.mu.Lock()
	l := p.leases[session]
	if l == nil {
		p.mu.Unlock()
		return false
	}
	delete(p.leases, session)
	if n := p.perTenant[l.Tenant]; n > 1 {
		p.perTenant[l.Tenant] = n - 1
	} else {
		delete(p.perTenant, l.Tenant)
	}
	p.mu.Unlock()

	l.Worker.kill()
	p.releaseSlot()
	p.cleanupProfile(l)
	xlog.Info("browserd: session %s released worker %d", session, l.Worker.id)
	return true
}

// dropDead removes a lease whose worker ended on its own.
func (p *Pool) dropDead(session string, l *Lease, reason string) {
	p.mu.Lock()
	if p.leases[session] != l {
		p.mu.Unlock()
		return // already released: Release owns the cleanup
	}
	delete(p.leases, session)
	if n := p.perTenant[l.Tenant]; n > 1 {
		p.perTenant[l.Tenant] = n - 1
	} else {
		delete(p.perTenant, l.Tenant)
	}
	p.mu.Unlock()

	p.releaseSlot()
	p.cleanupProfile(l)
	xlog.Error("browserd: worker %d (session %s) ended: %s", l.Worker.id, session, reason)
}

func (p *Pool) releaseSlot() {
	select {
	case <-p.slots:
	default:
	}
}

// cleanupProfile removes the session's user-data-dir.
//
// Chromium profiles are tens to hundreds of megabytes, and they must not outlive the session: the next session gets a
// fresh directory by design (cookies must not leak across sessions). Removal is asynchronous because a profile has many
// small files and this must not delay the caller.
//
// The retries are not defensive noise: on Windows the process tree is killed with taskkill, and Chromium keeps handles on
// files inside the profile for a moment afterwards (observed: CrashpadMetrics-active.pma). A single RemoveAll therefore
// loses that race and would report a 200 MB directory as leaked on every release.
func (p *Pool) cleanupProfile(l *Lease) {
	dir := sessionProfileDir(p.cfg, l.Worker.id)
	go func() {
		delay := 100 * time.Millisecond
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(delay)
			delay *= 2
		}
		xlog.Error("browserd: remove profile %s: %v (left for manual cleanup)", dir, err)
	}()
}

// janitor reclaims idle sessions.
func (p *Pool) janitor() {
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.JanitorInterval())
	defer t.Stop()
	for {
		select {
		case <-p.closedCh:
			return
		case <-t.C:
			p.reapIdle(time.Now())
		}
	}
}

// reapIdle is one sweep (split out of the loop so tests can drive it without waiting for a ticker).
func (p *Pool) reapIdle(now time.Time) {
	p.mu.Lock()
	var victims []*Lease
	for _, l := range p.leases {
		if l.busy.Load() {
			continue
		}
		if l.idleFor(now) > p.cfg.IdleTimeout() {
			victims = append(victims, l)
		}
	}
	p.mu.Unlock()

	for _, l := range victims {
		// TryLock loses the race against a call that just started: that call must not have its browser yanked out from
		// under it.
		if !l.mu.TryLock() {
			continue
		}
		l.mu.Unlock()
		xlog.Info("browserd: session %s idle for %s, reclaiming", l.Session, l.idleFor(now).Round(time.Second))
		p.Release(l.Session)
	}
}

// Close tears everything down: janitor first, then every live browser.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	leases := make([]*Lease, 0, len(p.leases))
	for _, l := range p.leases {
		leases = append(leases, l)
	}
	p.leases = make(map[string]*Lease)
	p.mu.Unlock()

	close(p.closedCh)
	p.cancel() // also kills the contexts of every child process

	for _, l := range leases {
		l.Worker.kill()
	}
	p.wg.Wait()
}

// Stats is the load snapshot (metrics endpoint).
type Stats struct {
	Workers  int `json:"workers"`
	Sessions int `json:"sessions"`
	Busy     int `json:"busy"`
	Queue    int `json:"queue"`
}

// Stats returns the current occupancy.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	leases := make([]*Lease, 0, len(p.leases))
	for _, l := range p.leases {
		leases = append(leases, l)
	}
	p.mu.Unlock()

	busy := 0
	for _, l := range leases {
		if l.busy.Load() {
			busy++
		}
	}
	return Stats{
		Workers:  p.cfg.Workers,
		Sessions: len(leases),
		Busy:     busy,
		Queue:    int(p.queue.Load()),
	}
}

// Leases returns a snapshot for the debug endpoint.
func (p *Pool) Leases() []LeaseInfo {
	now := time.Now()
	p.mu.Lock()
	infos := make([]LeaseInfo, 0, len(p.leases))
	for _, l := range p.leases {
		infos = append(infos, LeaseInfo{
			Session:      l.Session,
			Tenant:       l.Tenant,
			Worker:       l.Worker.id,
			IdleSec:      int(l.idleFor(now).Seconds()),
			Busy:         l.busy.Load(),
			WorkspaceDir: l.Worker.workspace,
			DownloadsDir: l.Worker.downloads,
		})
	}
	p.mu.Unlock()
	return infos
}

// LeaseInfo is one row of the debug snapshot.
type LeaseInfo struct {
	Session      string `json:"session"`
	Tenant       string `json:"tenant"`
	Worker       int    `json:"worker"`
	IdleSec      int    `json:"idle_sec"`
	Busy         bool   `json:"busy"`
	WorkspaceDir string `json:"workspace_dir"`
	DownloadsDir string `json:"downloads_dir"`
}

// SelfTest returns the cached probe result (nil while probing).
func (p *Pool) SelfTest() *SelfTest {
	return p.selftest.Load()
}

// probe runs `<browserhost> --selftest` once and caches the result.
//
// A failure does not stop the service: the pool still serves sessions, and the honest answer "the browser is not
// installed" is more useful than refusing to start.
func (p *Pool) probe() {
	args := []string{"--selftest"}
	if p.cfg.Browser.ExecPath != "" {
		args = append([]string{"--exec-path", p.cfg.Browser.ExecPath}, args...)
	}

	ctx, cancel := context.WithTimeout(p.ctx, selftestTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.cfg.Browser.Binary, args...).Output()
	if err != nil {
		st := &SelfTest{Error: selftestError(err)}
		p.selftest.Store(st)
		xlog.Error("browserd: browser self-test failed: %s", st.Error)
		return
	}

	// The driver prints one JSON line; take the last non-empty one so a stray log line cannot break the parse.
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
		}
	}

	st := &SelfTest{}
	if uerr := json.Unmarshal([]byte(line), st); uerr != nil {
		st = &SelfTest{Error: "unexpected self-test output: " + line}
		p.selftest.Store(st)
		xlog.Error("browserd: browser self-test: %s", st.Error)
		return
	}
	if st.Version == "" {
		st.Version = "unknown"
	}
	p.selftest.Store(st)
	xlog.Info("browserd: browser ready (%s, %s)", st.Engine, st.Version)
}

func selftestError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return msg
		}
	}
	return err.Error()
}
