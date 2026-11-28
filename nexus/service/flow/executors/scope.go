package executors

import (
	"errors"
	"fmt"
	"time"

	"flame/nexus/service/flow/core"
)

// parseParallelConfig reads the host block `config.parallel`. An absent (or empty) block is the default scope: no
// author limit (the system ceiling still applies, see applyConcurrencyCeiling), join=collect. Anything that cannot be
// understood is a config error - falling back to the default would run a different shape than the author configured
// while still reporting success, which is the class of behaviour this model exists to remove.
func parseParallelConfig(host *flow.Node) (flow.ParallelConfig, error) {
	cfg := flow.ParallelConfig{}
	if host == nil || host.Data == nil || host.Data.Config == nil {
		return cfg.Normalize(), nil
	}
	raw, exists := host.Data.Config["parallel"]
	if !exists || raw == nil {
		return cfg.Normalize(), nil
	}
	block, ok := raw.(map[string]any)
	if !ok {
		return cfg, newExecError(flow.ErrCodeConfig, fmt.Sprintf(
			"node %s: config.parallel must be an object ({max_concurrency, join, timeout}), got %T", host.ID, raw))
	}

	if v, ok := block["max_concurrency"]; ok && v != nil {
		switch n := v.(type) {
		case int:
			cfg.MaxConcurrency = n
		case float64:
			cfg.MaxConcurrency = int(n)
		default:
			return cfg, newExecError(flow.ErrCodeConfig, fmt.Sprintf(
				"node %s: config.parallel.max_concurrency must be a number, got %T", host.ID, v))
		}
	}

	if v, ok := block["join"]; ok && v != nil {
		join, _ := v.(string)
		switch join {
		case "", flow.JoinCollect:
			cfg.Join = flow.JoinCollect
		case flow.JoinDrain:
			cfg.Join = flow.JoinDrain
		case flow.JoinAbort:
			cfg.Join = flow.JoinAbort
		default:
			return cfg, newExecError(flow.ErrCodeConfig, fmt.Sprintf(
				"node %s: config.parallel.join %q is not a join mode (collect / drain / abort)", host.ID, join))
		}
	}

	// config.parallel.timeout uses the node-level {enabled, seconds} shape everyone else uses, so "switched off" means
	// the same thing here: no deadline. Only an explicitly enabled, positive number yields one.
	if v, ok := block["timeout"]; ok && v != nil {
		if _, isMap := v.(map[string]interface{}); !isMap {
			return cfg, newExecError(flow.ErrCodeConfig, fmt.Sprintf(
				"node %s: config.parallel.timeout must be an object {enabled, seconds}, got %T", host.ID, v))
		}
		if secs := parseTimeoutConfig(v); secs > 0 {
			cfg.Timeout = time.Duration(secs) * time.Second
		}
	}

	return cfg.Normalize(), nil
}

// applyConcurrencyCeiling bounds the scope by the tenant's ceiling. The author's number is a request, the ceiling is the
// answer: 0 means "no author limit" and therefore resolves to the ceiling, never to "unbounded". The clamp is logged
// because a scope that quietly runs 16 of the 40 members the author drew is a trap, and both numbers are kept in the
// snapshot so the record answers "why did only 16 run at once" without reading the config.
func applyConcurrencyCeiling(cfg flow.ParallelConfig, ctx *flow.ExecContext, hostID string) flow.ParallelConfig {
	ceiling := ctx.ResolveMaxConcurrency()
	if ceiling <= 0 { // defensive: the resolver never returns a non-positive number
		return cfg
	}
	cfg.Ceiling = ceiling
	switch {
	case cfg.MaxConcurrency <= 0:
		cfg.MaxConcurrency = ceiling
	case cfg.MaxConcurrency > ceiling:
		ctx.LogInfo(fmt.Sprintf("node %s: max_concurrency %d lowered to the system ceiling %d", hostID, cfg.MaxConcurrency, ceiling))
		cfg.MaxConcurrency = ceiling
	}
	return cfg
}

// isConcurrencyMember reports whether a node was marked as a member of its body's scope.
func isConcurrencyMember(node *flow.Node) bool {
	return node != nil && node.Data != nil && node.Data.Concurrent
}

// runBody executes a host's body and returns the scope it ran under. The caller joins the scope on every exit path;
// the scope is unregistered here because the walk is over by the time this returns, while the caller still holds the
// value it needs for the snapshot.
func runBody(host *flow.Node, ctx *flow.ExecContext, childPID string, entry *flow.Node) (*flow.BodyScope, error) {
	cfg, err := parseParallelConfig(host)
	if err != nil {
		return nil, err
	}
	cfg = applyConcurrencyCeiling(cfg, ctx, host.ID)
	scope := flow.NewBodyScope(host.ID, childPID, cfg)
	// The members get their own cancelable context, derived from the host's: `abort` cancels them without touching the
	// body's own chain (which stops at the next node boundary instead).
	scope.BindMembers(ctx.StdCtx)
	ctx.RegisterBodyScope(scope)
	defer ctx.RemoveBodyScope(childPID)
	return scope, execute(entry, ctx, childPID, 0)
}

// finishBody joins the scope, writes the snapshot into the host's NodeResult and turns a member failure into the
// host's error. The caller decides precedence when the body's own chain also failed (the chain wins).
func finishBody(nr *flow.NodeResult, scope *flow.BodyScope) error {
	if scope == nil {
		return nil
	}
	code, msg := scope.Join()
	if nr.ProcessResult == nil {
		nr.ProcessResult = &flow.ProcessResult{}
	}
	nr.ProcessResult.Parallel = scope.Snapshot()
	// The join is over, so "was this scope exited" is final: the host lifts the signal and finishes as success. This is
	// checked before the failure verdict on purpose - an exit is the author's decision to stop the whole run, and the
	// member or chain that failed at the same instant keeps its own failed record untouched.
	if exitedBy := scope.ExitedBy(); exitedBy != "" {
		nr.AddLogf("exit at node %s ended the run: the scope stopped, the members still running were cancelled", exitedBy)
		return SignalExit
	}
	if code == "" {
		return nil
	}
	if msg == "" {
		msg = "a concurrent member failed"
	}
	return newExecError(code, msg)
}

// finishLoopBody is finishBody for a loop: each iteration owns a fresh scope, so its snapshot is appended in round order
// instead of replacing the previous one.
func finishLoopBody(nr *flow.NodeResult, scope *flow.BodyScope) error {
	if scope == nil {
		return nil
	}
	code, msg := scope.Join()
	if nr.LoopResult == nil {
		nr.LoopResult = &flow.LoopResult{}
	}
	nr.LoopResult.AppendParallel(scope.Snapshot())
	// Same rule as finishBody: an exit carried out of a member ends the whole run, so this round hands the signal up
	// instead of a failure verdict.
	if exitedBy := scope.ExitedBy(); exitedBy != "" {
		nr.AddLogf("exit at node %s ended the run on this round: the members still running were cancelled", exitedBy)
		return SignalExit
	}
	if code == "" {
		return nil
	}
	if msg == "" {
		msg = "a concurrent member failed"
	}
	return newExecError(code, msg)
}

// dispatchMember starts one member: its own child process, registered in the scope, executed in a goroutine that does
// not follow the member's outgoing edge. The walk continues immediately - that is what makes it concurrent.
func dispatchMember(node *flow.Node, ctx *flow.ExecContext, parentProcessID string, scope *flow.BodyScope) error {
	childProcessID, err := ctx.ProcessManager.NewProcess(parentProcessID, nil)
	if err != nil {
		return newExecError(flow.ErrCodeInternal,
			fmt.Sprintf("create child process for member %s: %v", node.ID, err))
	}

	member := scope.Add(node.ID, childProcessID)
	// The member gets its own context - same run, same variables, its own cancellation tree - so `abort` cancels this
	// member alone instead of the whole run. Contexts are cheap: this is a shallow copy sharing every store by pointer.
	memberCtx := ctx.WithContext(scope.MemberContext())

	go func(n *flow.Node, parent, c *flow.ExecContext, pid string, m *flow.ScopeMember, s *flow.BodyScope) {
		defer func() {
			if r := recover(); r != nil {
				c.LogError(fmt.Sprintf("concurrent member %s panic: %v", n.ID, r))
				s.Finish(m, flow.StatusFailed, flow.ErrCodeInternal, fmt.Sprintf("member panic: %v", r))
			}
		}()

		// The slot is taken here, inside the goroutine: a limit must never block the walk dispatching the next node.
		if !s.Acquire(c.StdCtx, m) {
			s.Abandon(m, c.StdCtx)
			return
		}

		// Same debug treatment as a serial node: breakpoint / step suspension plus the all-stop barrier.
		if c.Debug != nil && c.Debug.Enabled {
			c.Debug.PushStack(pid)
			c.Debug.SuspendIfNeeded(n, pid, c.Debug.CurrentDepth())
			c.Debug.WaitIfGloballySuspended()
			c.Debug.PopStack()
		}

		err := executeNode(n, c, pid)
		status, code, msg := memberOutcome(c, pid, n.ID)
		// A member stopped by the scope recorded "context canceled", which names neither the cause nor the node
		// responsible. The scope knows, so its reason replaces that message on the member's own record too - the run
		// record is what the author reads, and the snapshot alone does not explain a cancelled node.
		if status == flow.StatusCancelled {
			if reason := s.StopReason(); reason != "" {
				msg = reason
				if mr, ok := c.ExecutedNodeMap.Get(pid + "_" + n.ID); ok && mr != nil {
					mr.ErrMsg = reason
				}
			}
		}
		// Fold the member's own progress / result text back before finishing: wg.Done in Finish is the barrier the host's
		// join waits on, so everything above has to be visible by then.
		parent.MergeFrom(c)
		if err != nil && isControlSignal(err) {
			if errors.Is(err, SignalExit) {
				// `exit` is global by contract, and a member that carries one out of its own body is no exception (an exit
				// node is never a member itself - flowcheck refuses the flag on control nodes). The member succeeded, the
				// scope stops starting work, and the members still running are cancelled (nothing they compute can be read
				// once the run stops). s.Exit finishes the member; the host lifts the signal at the join.
				s.Exit(m, n.ID)
				return
			}
			// break / continue / return belong to a loop or a function of the body's own chain. From a member there is
			// nobody to consume them, so they are reported instead of being dropped.
			status, code, msg = flow.StatusFailed, flow.ErrCodeConfig,
				fmt.Sprintf("member %s cannot leave its body: %v", n.ID, err)
		}
		s.Finish(m, status, code, msg)
	}(node, ctx, memberCtx, childProcessID, member, scope)

	return nil
}

// memberOutcome reads the terminal state a member's own executor recorded. The member runs only itself, so its
// NodeResult is keyed by its child process id.
func memberOutcome(ctx *flow.ExecContext, processID, nodeID string) (flow.NodeStatus, string, string) {
	nr, ok := ctx.ExecutedNodeMap.Get(processID + "_" + nodeID)
	if !ok || nr == nil {
		return flow.StatusFailed, flow.ErrCodeInternal, "member produced no node result"
	}
	return nr.Status, nr.ErrCode, nr.ErrMsg
}

// markScopeDraining records the chain node where a stopped scope stopped the walk. Later nodes never start, so this
// is the one place that shows why the rest of the body is missing from the run - including whether the cause was a
// member failure (`drain` / `abort`) or the scope deadline.
func markScopeDraining(node *flow.Node, ctx *flow.ExecContext, processID string, scope *flow.BodyScope) {
	nr := flow.NewNodeResult(node, processID, ctx)
	if err := ctx.RecordNodeExecution(processID, node.ID, nr); err != nil {
		nr.Finish(flow.StatusFailed, flow.ErrCodeInternal, "failed to record the node run: "+err.Error())
		return
	}
	reason := "scope draining: the scope stopped starting new work"
	if scope != nil {
		reason = scope.SkipReason()
	}
	nr.Finish(flow.StatusSkipped, "", reason)
}
