package daemon

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"flame/tools/sandbox"
)

// interactiveJanitorInterval is the sweep interval of idle reclamation.
const interactiveJanitorInterval = time.Minute

// errInteractiveEmptyArgv is returned when a long-lived interactive process is created without argv.
var errInteractiveEmptyArgv = errors.New("interactive: empty argv")

// BrowserToolID is the tool_id of the browser tool in the long-lived interactive registry.
//
// It is a peer of "shell": each occupies one entry in a session's tools map, and they share the same isolation unit
// (docker) and the same idle reclamation / kill / sticky routing.
const BrowserToolID = "browser"

// writerSwitch is a writer whose target can be switched.
//
// A long-lived interactive process is persistent, while stdout/stderr must be sent back over the connection of "the
// current call", so at the start of every call the target is switched to that connection's channel and reset to nil
// when the call ends.
//
// Two design points:
//   - a race between switching and the process writing at worst writes to the old (closed) connection and loses one
//     batch of output; it does not kill the process;
//   - Write always reports success: a failed write to the target (the client disconnected) must not tear down this
//     persistent output path — otherwise the stdout broadcast goroutine (io.Copy) would exit on one write error and
//     the process would have no output at all.
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
	_, _ = w.Write(p) // the write error is swallowed: a disconnect only loses this batch, it does not kill the output path
	return len(p), nil
}

// toolProc is the handle of one long-lived interactive process.
//
// mu serialises concurrency: one process has a single stdin and serves one call at a time. The lock is decoupled from
// the process lifetime: unlocking only means "this call is over", it does not kill the process.
type toolProc struct {
	proc       sandbox.Process
	stdin      io.WriteCloser
	stdoutOut  *writerSwitch // stdout broadcast sink: target = the current connection
	stderr     *writerSwitch // stderr sink (spec.Stderr), target = the current connection
	lastActive time.Time
	mu         sync.Mutex
}

// sessionSandbox is the logical sandbox of one session.
//
// The three modes are unified into this one structure and the differences collapse into unit:
//   - docker: unit is the container (Acquire), every tool process is docker exec'd into it and shares the container
//     mounts;
//   - nsjail/none: unit is nil and every tool process is Started independently.
type sessionSandbox struct {
	mounts    []sandbox.Mount
	unit      sandbox.Unit
	tools     map[string]*toolProc
	dir       string // process working directory (an in-sandbox path, only effective on first creation)
	ephemeral bool   // a temporary process without a session: reclaimed with the short timeout (it should die when the run ends)
	mu        sync.Mutex
}

// interactiveRegistry is the long-lived interactive registry (not a pool; entries have an owner).
//
// The essential difference from slotPool: objects "have an owner and are not interchangeable", so they cannot be lent
// out and reused; the semantics are "use it if it is there, create it if it is not, delete it on timeout / end".
type interactiveRegistry struct {
	exec                 sandbox.Sandbox
	reusable             sandbox.Reusable // non-nil enables unit reuse (docker)
	idleTimeout          time.Duration    // idle backstop of persistent processes (with a session)
	ephemeralIdleTimeout time.Duration    // short backstop of temporary processes (without a session)
	janitorInterval      time.Duration    // sweep interval: decides the upper bound of the reclaim delay

	mu       sync.Mutex
	sessions map[string]*sessionSandbox

	closed    chan struct{}
	closeOnce sync.Once
}

// newInteractiveRegistry creates the registry. ephemeralIdleTimeout is optional: when omitted it equals idleTimeout
// (which keeps the call style of the older tests unchanged).
func newInteractiveRegistry(exec sandbox.Sandbox, idleTimeout time.Duration, ephemeralIdleTimeout ...time.Duration) *interactiveRegistry {
	eph := idleTimeout
	if len(ephemeralIdleTimeout) > 0 && ephemeralIdleTimeout[0] > 0 {
		eph = ephemeralIdleTimeout[0]
	}
	return newInteractiveRegistryWithJanitor(exec, idleTimeout, eph, 0)
}

// newInteractiveRegistryWithJanitor is newInteractiveRegistry with an explicit sweep interval (0 = the default 1
// minute).
//
// Why the sweep interval is a parameter as well: the real reclaim time falls in (idle, idle+janitor], and the gateway's
// sticky TTL must cover that upper bound (sticky_ttl > idle + 2×janitor). A hardcoded interval would make that
// inequality impossible to compute on the gateway side, so it must come from the config and be reported through
// capabilities.
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

// acquire lazily creates and returns the tool process; on return the process is locked (tp.mu) and the caller must
// unlock it when this call ends.
// acquireOpts holds the optional arguments of acquire (effective when the session is first created).
type acquireOpts struct {
	dir       string // process working directory (an in-sandbox path)
	ephemeral bool   // a temporary process without a session: backstopped by the short idle timeout
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
	// lastActive must be refreshed inside sb.mu: the janitor's reapIdleTools holds sb.mu too, otherwise there is a
	// window where "the janitor wins the TryLock before the refresh and reaps by the stale value", handing an
	// already killed process to the caller.
	tp.lastActive = time.Now()
	sb.mu.Unlock()

	tp.mu.Lock()
	return tp, nil
}

// spawn lazily creates one tool process inside the sessionSandbox and starts two resident goroutines:
//   - stdout broadcast: the single reader of stdout, writing the output to stdoutOut (target = the current
//     connection);
//   - reaping: Wait() starts the stderr copy goroutine (when spec.Stderr is not *os.File), reaps the process,
//     releases cancel, and removes it from the registry when the process exits on its own (recreated on the next
//     call).
func (sb *sessionSandbox) spawn(r *interactiveRegistry, sessionID, toolID string, argv []string, lim sandbox.Limits) (*toolProc, error) {
	if len(argv) == 0 {
		return nil, errInteractiveEmptyArgv
	}

	// docker: the container (unit) is created lazily. Acquire on the first process, Close on the last.
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

	// stdout broadcast: every process gets exactly one goroutine reading stdout. Starting one io.Copy per connection
	// reading proc.Stdout() directly is not an option — a persistent process keeps its stdout open, the old reader
	// blocks after a disconnect, and the next connection would read the same pipe concurrently and tear the output
	// apart. Here it is read once and dispatched to the current connection.
	go func() {
		_, _ = io.Copy(tp.stdoutOut, proc.Stdout())
	}()

	// Reaping: Wait does three things — starts the stderr copy (without Wait, stderr would never reach the client),
	// reaps the process, and releases cancel. Returning means the process exited, so it is removed from the registry.
	go func() {
		_ = proc.Wait()
		r.removeTool(sessionID, toolID)
	}()

	return tp, nil
}

// unlock releases the process lock (this call is over, the process stays).
func (tp *toolProc) unlock() {
	tp.mu.Unlock()
}

// removeTool removes and reclaims a tool process (when it exits or is explicitly deleted) and reports whether the
// session no longer has any tool.
//
// The return value is the criterion for "should the sticky binding be dropped": a session can have several
// long-lived interactive tools, and killing one of them does not invalidate the session. A missing session also
// counts as empty (the semantics of an idempotent kill).
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

// release destroys the whole session's sandbox.
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

// destroy tears down the sessionSandbox: kill every process and, under docker, Close the unit.
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

// startJanitor runs the idle reclamation sweep (the interval is janitorInterval, 1 minute by default).
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

// reapIdle reclaims tool processes that exceeded their idle timeout, along with the sessions that become empty
// because of it.
func (r *interactiveRegistry) reapIdle() {
	now := time.Now()

	r.mu.Lock()
	sessions := make(map[string]*sessionSandbox, len(r.sessions))
	for sid, sb := range r.sessions {
		sessions[sid] = sb
	}
	r.mu.Unlock()

	for sid, sb := range sessions {
		// A temporary process (without a session) uses the short timeout: reclaim it as soon as the run ends; a
		// persistent process uses the long one.
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

// reapIdleTools reclaims tool processes that exceeded their idle timeout and reports whether the session is now empty.
// A process currently serving a call (TryLock fails) is skipped and not reclaimed.
func (sb *sessionSandbox) reapIdleTools(now time.Time, timeout time.Duration) bool {
	var idle []*toolProc
	sb.mu.Lock()
	for id, tp := range sb.tools {
		if !tp.mu.TryLock() {
			continue // currently serving a call, skip
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

// Close shuts the registry down: it stops the janitor and destroys every session.
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

// stats returns the number of sessions, the total number of tool processes and the number of active processes (used
// for the /metrics load snapshot).
//
// A read-only snapshot: the lock order matches reapIdle — r.mu is only used to copy the session references and
// released immediately, then each sb.mu is taken to read tools, so r.mu and sb.mu are never held nested.
//
// "Active" = tp.mu is held (currently serving one call, probed with TryLock). An idle long-lived interactive process
// burns no CPU, so the gateway should weight load by the active count only and keep idle processes out of it.
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
				active++ // TryLock failing = tp.mu is held = serving a call
			} else {
				tp.mu.Unlock()
			}
		}
		sb.mu.Unlock()
	}
	return
}
