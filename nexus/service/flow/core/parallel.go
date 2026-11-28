package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Join modes of a host's `config.parallel` block.
const (
	// JoinCollect is the default: every member runs to its own terminal state, and the join decides.
	JoinCollect = "collect"
	// JoinDrain stops starting new work on the first member failure: queued members and later chain nodes never
	// start, while members already running finish so their outcomes stay real.
	JoinDrain = "drain"
	// JoinAbort is drain plus cancelling the members still running: the first failure cancels the body's member context,
	// and everything already in flight is recorded as cancelled instead of being left to finish work nobody will read.
	// The body's own chain is not interrupted - it stops at the next node boundary - and neither is any sibling scope.
	JoinAbort = "abort"
)

// ParallelConfig is the host block `config.parallel` on a `process` / `loop` node. The zero value means "no author
// limit, collect", which is why the block is optional: a host without it is still a scope, just an unconfigured one.
//
// "No author limit" is not "unbounded": the executor lowers MaxConcurrency to Ceiling before the scope is built (see
// applyConcurrencyCeiling), so MaxConcurrency here is always the number that actually ran.
type ParallelConfig struct {
	MaxConcurrency int           `json:"max_concurrency"` // <= 0 means no author limit; the ceiling then applies
	Timeout        time.Duration `json:"timeout"`         // <= 0 means no scope deadline
	Join           string        `json:"join"`            // JoinCollect (default) | JoinDrain | JoinAbort
	// Ceiling is the system limit that bounded this scope (always positive, filled in by the executor). It is recorded so
	// "why did only 16 of my 40 members run at once" is answerable from the record alone.
	Ceiling int `json:"system_ceiling,omitempty"`
}

// abortReason is the message a member killed by `abort` carries. The executor only sees "context canceled", which says
// nothing about who cancelled it or why, and "was it cancelled or did it fail" is the first question on the run record.
const abortReason = "aborted by scope: the first member failure cancelled the members still running"

// timeoutReason is the same message one level up: the members that were still running when the scope deadline passed.
func (s *BodyScope) timeoutReason() string {
	return fmt.Sprintf("scope timed out after %s: the members still running were cancelled", s.Config.Timeout)
}

// Normalize fills the defaults, so callers never have to care whether the block existed.
func (c ParallelConfig) Normalize() ParallelConfig {
	if c.Join == "" {
		c.Join = JoinCollect
	}
	if c.Timeout < 0 {
		c.Timeout = 0
	}
	return c
}

// MemberRun is one member of a scope, as recorded in the host's snapshot.
type MemberRun struct {
	NodeID     string     `json:"node_id"`
	ProcessID  string     `json:"process_id"`
	Status     NodeStatus `json:"status"`
	ErrCode    string     `json:"err_code,omitempty"`
	ErrMsg     string     `json:"err_msg,omitempty"`
	StartAt    time.Time  `json:"start_at"`
	EndAt      time.Time  `json:"end_at"`
	DurationMs int64      `json:"duration_ms"`
}

// ParallelResult is the snapshot a host writes into its own NodeResult on every exit path. It is a hard requirement,
// not a nicety: without it "did it really run concurrently" can only be answered by an author-written timing harness.
type ParallelResult struct {
	MaxConcurrency int         `json:"max_concurrency"`
	SystemCeiling  int         `json:"system_ceiling,omitempty"` // the limit the scope was bounded by, see ParallelConfig.Ceiling
	Join           string      `json:"join"`
	Members        []MemberRun `json:"members"`
	Failed         []string    `json:"failed"`
	Abandoned      bool        `json:"abandoned"`
	// ExitedBy is the member node whose `exit` terminated the run. The host then finishes with success (the signal is
	// control flow), so without this field the run record would not say why the later chain nodes are missing.
	ExitedBy string `json:"exited_by,omitempty"`
}

// ScopeMember is the in-flight state of one member, owned by its BodyScope.
type ScopeMember struct {
	NodeID    string
	ProcessID string

	status   NodeStatus
	errCode  string
	errMsg   string
	startAt  time.Time
	endAt    time.Time
	started  bool
	finished bool
}

// BodyScope is one execution of a host's body: every member dispatched by this body is registered here, and the
// body's end joins them. A `loop` creates one per iteration, so two iterations never share members.
type BodyScope struct {
	HostID    string
	ProcessID string
	Config    ParallelConfig

	mu       sync.Mutex
	sem      chan struct{} // nil when MaxConcurrency <= 0 (unlimited)
	members  []*ScopeMember
	draining bool
	aborted  bool
	timedOut bool
	// exited marks a scope terminated by a member's `exit`. It is not a failure: the member succeeded, the verdict is
	// "the run stops here", and the host lifts the signal to its own caller.
	exited     bool
	exitNodeID string
	wg         sync.WaitGroup

	// timer fires the scope deadline. One timer per body, stopped by the join: a goroutine per member would have to be
	// stopped too, and the deadline belongs to the scope, not to whichever member happens to be running.
	timer *time.Timer

	// The context every member of this body runs under (see ExecContext.WithContext). It is derived once, when the body
	// starts, so cancelling it on `abort` stops the members in flight while the body's own chain keeps its parent
	// context. Nil when nobody bound one (a scope built by hand in a unit test).
	memberCtx     context.Context
	cancelMembers context.CancelFunc
}

// NewBodyScope creates the scope of one body execution. processID is the body's (child) process id, which is also
// the key the walk uses to find "which scope am I in".
func NewBodyScope(hostID, processID string, cfg ParallelConfig) *BodyScope {
	cfg = cfg.Normalize()
	s := &BodyScope{HostID: hostID, ProcessID: processID, Config: cfg}
	if cfg.MaxConcurrency > 0 {
		s.sem = make(chan struct{}, cfg.MaxConcurrency)
	}
	return s
}

// BindMembers derives the context every member of this body runs under from parent. Deriving it once per body is what
// makes `abort` a per-scope decision: cancelling it never touches a sibling scope or the body's own chain.
func (s *BodyScope) BindMembers(parent context.Context) {
	memberCtx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.memberCtx, s.cancelMembers = memberCtx, cancel
	timeout := s.Config.Timeout
	if timeout > 0 {
		s.timer = time.AfterFunc(timeout, s.fireTimeout)
	}
	s.mu.Unlock()
}

// fireTimeout is the scope deadline: cancel the members in flight and stop the walk at the next node boundary, the same
// way `abort` does. It does nothing when every member has already finished - a scope whose work is done has not timed out,
// and reporting one would turn a successful body into a failure because a timer happened to fire a moment late.
func (s *BodyScope) fireTimeout() {
	s.mu.Lock()
	// An exited run ended for a reason the author chose, so a deadline firing afterwards must not relabel its members as
	// "timed out" - the exit already stopped them.
	if s.exited || s.allMembersFinishedLocked() {
		s.mu.Unlock()
		return
	}
	s.timedOut = true
	s.draining = true
	cancel := s.cancelMembers
	s.mu.Unlock()
	// Outside the lock: the members it wakes run their own Finish, which needs the lock this call just released.
	if cancel != nil {
		cancel()
	}
}

func (s *BodyScope) allMembersFinishedLocked() bool {
	for _, m := range s.members {
		if !m.finished {
			return false
		}
	}
	return true
}

// TimedOut reports whether the scope deadline passed. The host uses it to decide the verdict: a timeout outranks the
// member failures it caused, because "one member failed" is not what happened.
func (s *BodyScope) TimedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timedOut
}

// SkipReason is what a node skipped by the stopped walk records: the walk has to say why the rest of the body is missing,
// and "timed out" is a different answer from "draining".
func (s *BodyScope) SkipReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipReasonLocked()
}

// stopTimer disarms the deadline. Called by the join on every exit path so a finished body leaves no timer behind.
func (s *BodyScope) stopTimer() {
	s.mu.Lock()
	timer := s.timer
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// MemberContext is the context members run under. A scope whose members were never bound (a unit test) answers with the
// background context, so dispatch does not have to special-case it.
func (s *BodyScope) MemberContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memberCtx == nil {
		return context.Background()
	}
	return s.memberCtx
}

// CancelMembers cancels the members in flight without recording a failure of its own. The join calls it on every exit
// path once every member has been waited for, so a cancelled tail does not keep a context alive.
func (s *BodyScope) CancelMembers() {
	s.mu.Lock()
	cancel := s.cancelMembers
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Add registers a member before its goroutine starts. The join counts it from this moment, so a member that never
// starts is still recorded (skipped or cancelled) instead of silently missing from the snapshot.
func (s *BodyScope) Add(nodeID, processID string) *ScopeMember {
	m := &ScopeMember{NodeID: nodeID, ProcessID: processID}
	s.mu.Lock()
	s.members = append(s.members, m)
	s.mu.Unlock()
	s.wg.Add(1)
	return m
}

// Acquire takes a concurrency slot, then refuses to start when the scope has begun draining. The semaphore is taken
// inside the member's goroutine on purpose: a limit must never block the walk that dispatches the next member.
func (s *BodyScope) Acquire(ctx context.Context, m *ScopeMember) bool {
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	s.mu.Lock()
	draining := s.draining
	s.mu.Unlock()
	if draining {
		if s.sem != nil {
			<-s.sem
		}
		return false
	}
	s.mu.Lock()
	m.started = true
	m.startAt = time.Now()
	s.mu.Unlock()
	return true
}

// Finish records a member's terminal state and releases its slot. It is idempotent: a panic path that runs after a
// normal finish must not double-release.
func (s *BodyScope) Finish(m *ScopeMember, status NodeStatus, code, msg string) {
	s.mu.Lock()
	if m.finished {
		s.mu.Unlock()
		return
	}
	m.finished = true
	m.status = status
	m.errCode = code
	m.errMsg = msg
	m.endAt = time.Now()
	started := m.started
	// `abort` / the scope deadline / a member's `exit` rewrite the message of the members they killed: "context
	// canceled" is what the executor saw, not why it was cancelled.
	if status == StatusCancelled {
		switch {
		case s.exited:
			m.errMsg = fmt.Sprintf("stopped by exit at node %s: the run ends here, so this result can never be read", s.exitNodeID)
		case s.timedOut:
			m.errMsg = s.timeoutReason()
		case s.aborted:
			m.errMsg = abortReason
		}
	}
	var cancel context.CancelFunc
	if status == StatusFailed && s.Config.Join != JoinCollect {
		// The first failure is chosen under the scope lock: deterministic within one run. Which member wins when two
		// fail in the same instant is not reproducible across runs, and that is not a bug.
		s.draining = true
		if s.Config.Join == JoinAbort && !s.aborted {
			s.aborted = true
			cancel = s.cancelMembers
		}
	}
	s.mu.Unlock()
	// Outside the lock: the members it wakes run their own Finish, which needs the lock this call just released.
	if cancel != nil {
		cancel()
	}
	// Only a member that actually took a slot owns one; a panic before Acquire would otherwise block here forever.
	if s.sem != nil && started {
		<-s.sem
	}
	s.wg.Done()
}

// Abandon records a member that never started: skipped when the scope stopped starting work (`drain` / `abort` / the
// deadline), cancelled when the run itself was cancelled. It is not a failure - reporting a stopped member as a failure
// turns one fault into many.
func (s *BodyScope) Abandon(m *ScopeMember, ctx context.Context) {
	s.mu.Lock()
	if m.finished {
		s.mu.Unlock()
		return
	}
	status := StatusSkipped
	code := ""
	msg := s.skipReasonLocked()
	switch {
	case s.draining:
		// The scope stopped starting work. Checked before ctx.Err(): the cancel this scope issued is also visible on the
		// context, and "the scope stopped" is the reason the operator can act on.
	case ctx.Err() != nil:
		status, code, msg = StatusCancelled, ErrCodeCancel, ctx.Err().Error()
	}
	m.finished = true
	m.status = status
	m.errCode = code
	m.errMsg = msg
	m.endAt = time.Now()
	s.mu.Unlock()
	s.wg.Done()
}

// StopOnExit records that an `exit` node ended the run and stops everything this scope already started. `exit` is global
// and immediate: no queued member starts afterwards (it is abandoned as skipped), the members in flight are cancelled, and
// the join reports ExitedBy so the host lifts the signal. The first exit wins - the node that fired it is the one the run
// record should name, and a later one can only be a nested host unwinding the same signal.
//
// Both paths call it: a member's own `exit` (see Exit) and an `exit` on the body's chain (the walk calls it before it
// returns the signal). Leaving the chain case out used to let queued members start after the run had already ended.
func (s *BodyScope) StopOnExit(nodeID string) {
	s.mu.Lock()
	first := !s.exited
	if first {
		s.exited = true
		s.exitNodeID = nodeID
		// Drain rather than abort: abort's message blames the first member failure, and there is none here.
		s.draining = true
	}
	s.mu.Unlock()
	// Outside the lock: the members it wakes run their own Finish, which needs the lock this call just released.
	s.CancelMembers()
}

// Exit records a member whose `exit` ended the run. Unlike Abandon this is a positive outcome: the member itself
// succeeded (the signal is control flow, not a failure), the scope stops starting work, and the members still running are
// cancelled - whatever they compute can never be read once the run stops. The host learns about it at the join and lifts
// the signal, so `exit` is global from a concurrent member exactly as it is from the body's own chain.
func (s *BodyScope) Exit(m *ScopeMember, nodeID string) {
	s.StopOnExit(nodeID)
	s.Finish(m, StatusSuccess, "", "")
}

// ExitedBy reports the member node whose `exit` ended the run, or "" when the scope was not exited. The host turns that
// into the signal it propagates; the run itself keeps closing as success.
func (s *BodyScope) ExitedBy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitNodeID
}

// StopReason spells out why this scope cancelled the members it cancelled, or "" when nothing was stopped. A member's
// own node record otherwise says "context canceled", which names neither the cause nor the node responsible.
func (s *BodyScope) StopReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.exited:
		return fmt.Sprintf("stopped by exit at node %s: the run ends here, so this result can never be read", s.exitNodeID)
	case s.timedOut:
		return s.timeoutReason()
	case s.aborted:
		return abortReason
	}
	return ""
}

func (s *BodyScope) skipReasonLocked() string {
	switch {
	case s.exited:
		return fmt.Sprintf("scope terminated by exit at node %s: no further work starts", s.exitNodeID)
	case s.timedOut:
		return s.timeoutReason()
	}
	return "scope draining: the scope stopped starting new work"
}

// Draining reports whether the scope has stopped starting new work. The walk asks before every node, so a drain
// also stops later chain nodes.
func (s *BodyScope) Draining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// Join waits for every member and summarises the outcome: an empty code means no member failed. The aggregation
// follows the design table: one shared code propagates as is, mixed codes collapse into `other` with one line per
// member.
func (s *BodyScope) Join() (string, string) {
	// The deadline has to stay armed while waiting: that wait is exactly what it bounds. It is disarmed afterwards, and a
	// timer that fires as the last member finishes is ignored by fireTimeout's "every member is done" check.
	s.wg.Wait()
	s.stopTimer()
	// Every member has finished, so the derived context has no reason to stay alive. On the `abort` path this is already
	// done; everywhere else it releases the context the body created.
	defer s.CancelMembers()
	s.mu.Lock()
	defer s.mu.Unlock()
	// The deadline outranks everything else: the members it cancelled are not failures, and reporting the first one of
	// them as the block's error would name a casualty as the cause.
	if s.timedOut {
		return ErrCodeTimeout, s.timeoutMessageLocked()
	}
	var failed []*ScopeMember
	for _, m := range s.members {
		if m.finished && m.status == StatusFailed {
			failed = append(failed, m)
		}
	}
	if len(failed) == 0 {
		return "", ""
	}
	code := failed[0].errCode
	if code == "" {
		code = ErrCodeOther
	}
	same := true
	for _, m := range failed {
		c := m.errCode
		if c == "" {
			c = ErrCodeOther
		}
		if c != code {
			same = false
			break
		}
	}
	if same {
		msg := ""
		if failed[0].errMsg != "" {
			msg = fmt.Sprintf("member %s: %s", failed[0].NodeID, failed[0].errMsg)
		}
		return code, msg
	}
	var b strings.Builder
	b.WriteString("members failed with different codes:")
	for _, m := range failed {
		fmt.Fprintf(&b, "\nnode %s (process %s): %s %s", m.NodeID, m.ProcessID, m.errCode, m.errMsg)
	}
	return ErrCodeOther, b.String()
}

// timeoutMessageLocked spells out each member's state at the deadline. The code says "timed out"; which member was still
// running is what tells the author whether to raise the limit, shorten the members' work, or move the deadline.
func (s *BodyScope) timeoutMessageLocked() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scope timed out after %s", s.Config.Timeout)
	if len(s.members) > 0 {
		b.WriteString("; members at that point:")
		for _, m := range s.members {
			status := m.status
			if !m.finished {
				status = StatusRunning
			}
			fmt.Fprintf(&b, "\nnode %s (process %s): %s", m.NodeID, m.ProcessID, status)
		}
	}
	return b.String()
}

// Snapshot builds the record written into the host's NodeResult. `Abandoned` marks a scope that hit its deadline: the
// join verdict is already in the host's err_code, but "the run stopped waiting" is a different fact from "a member
// failed", and the run record has to carry both. A member still running keeps StatusRunning, which is only reachable when
// the deadline fired between the cancel and the join.
func (s *BodyScope) Snapshot() *ParallelResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &ParallelResult{
		MaxConcurrency: s.Config.MaxConcurrency,
		SystemCeiling:  s.Config.Ceiling,
		Join:           s.Config.Join,
		Abandoned:      s.timedOut,
		ExitedBy:       s.exitNodeID,
		Members:        make([]MemberRun, 0, len(s.members)),
	}
	for _, m := range s.members {
		status := m.status
		if !m.finished {
			status = StatusRunning
		}
		run := MemberRun{
			NodeID:    m.NodeID,
			ProcessID: m.ProcessID,
			Status:    status,
			ErrCode:   m.errCode,
			ErrMsg:    m.errMsg,
			StartAt:   m.startAt,
			EndAt:     m.endAt,
		}
		if !m.startAt.IsZero() && !m.endAt.IsZero() {
			run.DurationMs = m.endAt.Sub(m.startAt).Milliseconds()
		}
		res.Members = append(res.Members, run)
		if status == StatusFailed {
			res.Failed = append(res.Failed, m.NodeID)
		}
	}
	return res
}

// MemberCount reports how many members the scope holds (used by tests and diagnostics).
func (s *BodyScope) MemberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members)
}

// RegisterBodyScope publishes a scope under its body process id, so the walk can answer "which scope am I in" for
// every node it executes.
func (ctx *ExecContext) RegisterBodyScope(s *BodyScope) {
	ctx.bodyMu.Lock()
	defer ctx.bodyMu.Unlock()
	if ctx.bodyScopes == nil {
		ctx.bodyScopes = make(map[string]*BodyScope)
	}
	ctx.bodyScopes[s.ProcessID] = s
}

// BodyScope returns the scope whose body is processID, or nil when that process is not a host's body (the flow root,
// a function body, a branch lane). A nil scope is how the runtime rejects a member outside a host.
func (ctx *ExecContext) BodyScope(processID string) *BodyScope {
	ctx.bodyMu.RLock()
	defer ctx.bodyMu.RUnlock()
	return ctx.bodyScopes[processID]
}

// RemoveBodyScope drops a finished body's scope; the snapshot already lives in the host's NodeResult.
func (ctx *ExecContext) RemoveBodyScope(processID string) {
	ctx.bodyMu.Lock()
	defer ctx.bodyMu.Unlock()
	delete(ctx.bodyScopes, processID)
}
