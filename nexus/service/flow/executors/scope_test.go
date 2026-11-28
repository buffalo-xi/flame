package executors

import (
	"errors"
	"strings"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/flowconfig"
)

// scopeProbe is a member-friendly node: it sleeps and optionally fails, so a test controls timing and outcome without
// pulling in a sandbox.
type scopeProbe struct{}

func (p *scopeProbe) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if ms, ok := node.Data.Config["delay_ms"].(int); ok && ms > 0 {
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.StdCtx.Done():
			nr.Finish(flow.StatusCancelled, flow.ErrCodeCancel, ctx.StdCtx.Err().Error())
			return ctx.StdCtx.Err()
		}
	}
	if failed, _ := node.Data.Config["fail"].(bool); failed {
		code, _ := node.Data.Config["code"].(string)
		if code == "" {
			code = flow.ErrCodeOther
		}
		return failf(nr, code, "probe failure")
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (p *scopeProbe) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeOther, flow.ErrCodeTimeout, flow.ErrCodeScript},
	}
}

// registerProbe installs the test node type for one test only. It must not be registered at init: every registered
// type has to appear in flowcheck's capability table (capabilities_drift_test.go), and a test type has no business
// there.
func registerProbe(t *testing.T) {
	t.Helper()
	mu.Lock()
	executorMap["scope_probe"] = &scopeProbe{}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(executorMap, "scope_probe")
		mu.Unlock()
	})
}

// probeNode is a chain node of the test executor; a member is the same node with data.concurrent set.
func probeNode(id, parent string, cfg map[string]any) *flow.Node {
	n := newExecNode(id, "scope_probe", cfg)
	n.ParentNode = parent
	return n
}

func memberNode(id, parent string, cfg map[string]any) *flow.Node {
	n := probeNode(id, parent, cfg)
	n.Data.Concurrent = true
	return n
}

// entryTo builds the entry node of a container whose sub-chain starts at first, so a body can be executed the way the
// runner finds it (findEntryNode looks for the child with type entry).
func entryTo(parentID string, first *flow.Node) *flow.Node {
	id := "entry-" + parentID
	entry := &flow.Node{ID: id, Type: "entry", ParentNode: parentID, Data: &flow.NodeData{Label: "Entry"}}
	if first != nil {
		first.ParentNode = parentID
		entry.OutEdges = []*flow.Edge{{ID: "e-" + id, Source: id, Target: first.ID}}
	}
	return entry
}

// buildHost wires a process host whose body is exactly the given chain, and returns the host plus the context the
// runner would have built.
func buildHost(t *testing.T, hostID string, parallel map[string]any, chain []*flow.Node) (*flow.Node, *flow.ExecContext) {
	t.Helper()
	registerProbe(t)
	host := newExecNode(hostID, "process", map[string]any{})
	if parallel != nil {
		host.Data.Config["parallel"] = parallel
	}
	entry := entryTo(hostID, chain[0])
	all := append([]*flow.Node{host, entry}, chain...)
	for i := 0; i+1 < len(chain); i++ {
		chain[i].OutEdges = []*flow.Edge{{ID: "e-" + chain[i].ID, Source: chain[i].ID, Target: chain[i+1].ID}}
	}
	return host, newContainerContext(t, all)
}

// nestBody registers a nested container's body in the context built by buildHost: the walk reads ctx.Flow, so a nested
// host's entry and children have to be added there after buildHost wired the outer chain.
func nestBody(ctx *flow.ExecContext, nodes ...*flow.Node) {
	for _, n := range nodes {
		ctx.Flow.Nodes = append(ctx.Flow.Nodes, n)
		ctx.Flow.NodeMap[n.ID] = n
	}
}

func runHost(t *testing.T, host *flow.Node, ctx *flow.ExecContext) (*flow.NodeResult, error) {
	t.Helper()
	nr := newExecResult(host, ctx)
	err := (&ProcessExecutor{}).Execute(host, ctx, "1", nr)
	return nr, err
}

// findExecuted locates a node result by node id, wherever its process sits.
func findExecuted(ctx *flow.ExecContext, nodeID string) *flow.NodeResult {
	var found *flow.NodeResult
	ctx.ExecutedNodeMap.Range(func(_ string, nr *flow.NodeResult) bool {
		if nr != nil && nr.NodeID == nodeID {
			found = nr
			return false
		}
		return true
	})
	return found
}

// Two members are dispatched and joined by the body's end: the host succeeds, and the snapshot carries both members with
// their measured durations.
func TestProcessScopeDispatchesAndJoins(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 60})
	m2 := memberNode("m2", "p1", map[string]any{"delay_ms": 60})
	c1 := probeNode("c1", "p1", map[string]any{})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, m2, c1})

	nr, err := runHost(t, host, ctx)
	if err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Fatalf("status = %q, want success", nr.Status)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || len(snap.Members) != 2 {
		t.Fatalf("snapshot = %+v, want two members", snap)
	}
	for _, m := range snap.Members {
		if m.Status != flow.StatusSuccess || m.DurationMs <= 0 {
			t.Errorf("member %+v, want success with a measured duration", m)
		}
	}
	a, b := snap.Members[0], snap.Members[1]
	if !a.StartAt.Before(b.EndAt) || !b.StartAt.Before(a.EndAt) {
		t.Error("the two members did not overlap: they ran one after the other")
	}
}

// A member failure fails the run on its own - no wait node is needed for it to be visible.
func TestProcessScopeMemberFailureFailsTheHost(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout, "delay_ms": 20})
	c1 := probeNode("c1", "p1", map[string]any{})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, c1})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want the member failure to fail the host")
	}
	if nr.Status != flow.StatusFailed || nr.ErrCode != flow.ErrCodeTimeout {
		t.Fatalf("status/code = %q/%q, want failed/timeout", nr.Status, nr.ErrCode)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || len(snap.Failed) != 1 || snap.Failed[0] != "m1" {
		t.Fatalf("Failed = %+v, want [m1]", snap)
	}
}

// `exit` is never a member itself - it is control flow on a chain - but a member that carries one out of its own body ends
// the run all the same: the member succeeds, the outer scope stops starting work, and the sibling still running is
// cancelled rather than failed.
func TestProcessScopeMemberCarryingExitEndsRun(t *testing.T) {
	// The member is a nested host whose body fires the exit, so the exit node lives inside that body.
	inner := newExecNode("p2", "process", map[string]any{})
	inner.ParentNode = "p1"
	inner.Data.Concurrent = true
	innerExit := newExecNode("x1", "exit", map[string]any{})
	innerEntry := entryTo("p2", innerExit)

	slow := memberNode("m2", "p1", map[string]any{"delay_ms": 300})
	// The ordinary node sits between the sibling and the nested host: by the time the inner body fires its exit the
	// sibling is already running, so the record asserted below is the cancelled one rather than a member that never
	// started. A node placed *after* the exit would be skipped by the drain and buy no time.
	c0 := probeNode("c0", "p1", map[string]any{"delay_ms": 100})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{slow, c0, inner})
	nestBody(ctx, innerEntry, innerExit)

	nr, err := runHost(t, host, ctx)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal lifted to the host", err)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %q, want success: exit is control flow, not a failure", nr.Status)
	}
	if x := findExecuted(ctx, "x1"); x == nil || x.Status != flow.StatusSuccess {
		t.Errorf("x1 = %+v, want success: the exit node inside the member succeeded", x)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || snap.ExitedBy != "p2" {
		t.Fatalf("snapshot = %+v, want exited_by p2: the member that carried the signal out", snap)
	}
	if len(snap.Failed) != 0 {
		t.Errorf("failed = %v, want none: a cancelled sibling is not a failure", snap.Failed)
	}
	for _, m := range snap.Members {
		if m.NodeID == "m2" && m.Status != flow.StatusCancelled && m.Status != flow.StatusSkipped {
			t.Errorf("m2 status = %q, want cancelled or skipped", m.Status)
		}
	}
	// The cancelled sibling's own record has to say who stopped it: "context canceled" explains nothing.
	found := findExecuted(ctx, "m2")
	if found == nil || found.Status != flow.StatusCancelled {
		t.Fatalf("m2 record = %+v, want it cancelled", found)
	}
	if !strings.Contains(found.ErrMsg, "exit at node p2") {
		t.Errorf("m2 err_msg = %q, want the scope's exit reason", found.ErrMsg)
	}
}

// An `exit` on the body's own chain is as global as a member's: the members still running are cancelled instead of waited
// for, because a run that has ended can never read their results.
func TestProcessScopeChainExitCancelsRunningMembers(t *testing.T) {
	slow := memberNode("m1", "p1", map[string]any{"delay_ms": 3000})
	// A chain node between the member and the exit: the member is certainly running when the exit fires.
	c0 := probeNode("c0", "p1", map[string]any{"delay_ms": 100})
	ex := newExecNode("x1", "exit", map[string]any{})
	ex.ParentNode = "p1"
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{slow, c0, ex})

	started := time.Now()
	nr, err := runHost(t, host, ctx)
	elapsed := time.Since(started)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal lifted to the host", err)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed = %v, want the 3s member cancelled instead of waited for", elapsed)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %q, want success: exit is control flow, not a failure", nr.Status)
	}
	if snap := nr.ProcessResult.Parallel; snap == nil || snap.ExitedBy != "x1" {
		t.Fatalf("snapshot = %+v, want exited_by x1", nr.ProcessResult.Parallel)
	}
	m1 := findExecuted(ctx, "m1")
	if m1 == nil || m1.Status != flow.StatusCancelled {
		t.Fatalf("m1 = %+v, want it cancelled", m1)
	}
	if !strings.Contains(m1.ErrMsg, "exit at node x1") {
		t.Errorf("m1 err_msg = %q, want the exit reason", m1.ErrMsg)
	}
	// The persisted record is built from the same list, so the reason has to reach it: the run record is what a reader
	// actually sees, and "context canceled" there would leave the cause unexplained.
	stored := ""
	for _, rec := range buildNodeRecords(ctx) {
		if rec.NodeID == "m1" {
			stored = rec.ErrMsg
		}
	}
	if !strings.Contains(stored, "exit at node x1") {
		t.Errorf("stored m1 err_msg = %q, want the exit reason", stored)
	}
}

// Nothing starts after an exit, not even a member that was already queued for a slot: the scope drains as it stops.
func TestProcessScopeChainExitStopsQueuedMembers(t *testing.T) {
	// Both members are as long as each other, so whichever gets the single slot is still running when the exit fires and
	// the other stays queued - the outcome does not depend on which goroutine won the race.
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 300})
	m2 := memberNode("m2", "p1", map[string]any{"delay_ms": 300})
	c0 := probeNode("c0", "p1", map[string]any{"delay_ms": 100})
	ex := newExecNode("x1", "exit", map[string]any{})
	ex.ParentNode = "p1"
	host, ctx := buildHost(t, "p1", map[string]any{"max_concurrency": 1}, []*flow.Node{m1, m2, c0, ex})

	started := time.Now()
	nr, err := runHost(t, host, ctx)
	elapsed := time.Since(started)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal", err)
	}
	// Without draining the queued member would take the slot when the first one ends (300ms) and run its own 300ms.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want the queued member abandoned instead of run", elapsed)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || snap.ExitedBy != "x1" {
		t.Fatalf("snapshot = %+v, want exited_by x1", snap)
	}
	skipped, cancelled := 0, 0
	for _, m := range snap.Members {
		switch m.Status {
		case flow.StatusSkipped:
			skipped++
			if !strings.Contains(m.ErrMsg, "exit at node x1") {
				t.Errorf("queued member %s err_msg = %q, want the exit reason", m.NodeID, m.ErrMsg)
			}
		case flow.StatusCancelled:
			cancelled++
		case flow.StatusSuccess:
			t.Errorf("member %s ran to completion after the exit", m.NodeID)
		}
	}
	if skipped != 1 || cancelled != 1 {
		t.Errorf("skipped/cancelled = %d/%d, want 1/1: one member ran, one never started", skipped, cancelled)
	}
}

// A scope is never unbounded. An explicit number above the tenant ceiling is lowered before the body starts, and the
// snapshot keeps both numbers so "why did only 4 of my 40 members run at once" is answerable from the record alone.
func TestProcessScopeConcurrencyCeilingClamps(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 0})
	host, ctx := buildHost(t, "p1", map[string]any{"max_concurrency": 40}, []*flow.Node{m1})
	limits := flowconfig.FlowLimits{MaxConcurrency: 4}
	ctx.Limits = &limits

	nr, err := runHost(t, host, ctx)
	if err != nil {
		t.Fatalf("Execute = %v, want success", err)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil {
		t.Fatal("snapshot = nil, want one on every exit path")
	}
	if snap.MaxConcurrency != 4 {
		t.Errorf("max_concurrency = %d, want the ceiling 4", snap.MaxConcurrency)
	}
	if snap.SystemCeiling != 4 {
		t.Errorf("system_ceiling = %d, want 4: the record has to name the limit that bounded the scope", snap.SystemCeiling)
	}
}

// "No author limit" resolves to the ceiling rather than to "unbounded": a body without config.parallel is still bounded.
func TestProcessScopeDefaultLimitIsTheCeiling(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 0})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1})
	limits := flowconfig.FlowLimits{MaxConcurrency: 3}
	ctx.Limits = &limits

	nr, err := runHost(t, host, ctx)
	if err != nil {
		t.Fatalf("Execute = %v, want success", err)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || snap.MaxConcurrency != 3 || snap.SystemCeiling != 3 {
		t.Fatalf("snapshot = %+v, want max_concurrency 3 bounded by ceiling 3", nr.ProcessResult.Parallel)
	}
}

// break / continue / return keep failing from a member: only `exit` is global, because only `exit` has no consumer that
// a member could bypass.
func TestProcessScopeMemberBreakStillFails(t *testing.T) {
	br := newExecNode("b1", "break", map[string]any{})
	br.ParentNode = "p1"
	br.Data.Concurrent = true
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{br})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want the break to be refused")
	}
	if nr.Status != flow.StatusFailed || nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("status/code = %q/%q, want failed/config", nr.Status, nr.ErrCode)
	}
}

// Mixed member codes collapse into other, with one line per member: the join cannot pick a single member's code.
func TestProcessScopeMixedCodesCollapseToOther(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout})
	m2 := memberNode("m2", "p1", map[string]any{"fail": true, "code": flow.ErrCodeScript})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, m2})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want a failure")
	}
	if nr.ErrCode != flow.ErrCodeOther {
		t.Fatalf("code = %q, want %q", nr.ErrCode, flow.ErrCodeOther)
	}
	if !strings.Contains(nr.ErrMsg, "m1") || !strings.Contains(nr.ErrMsg, "m2") {
		t.Errorf("msg = %q, want one line per member", nr.ErrMsg)
	}
}

// A scope failure is an ordinary node failure, so the host's own on_error applies: `skip` lets the main chain continue past
// an optional concurrent section, and the member states stay on the skipped record so the run still shows what happened.
func TestProcessScopeFailureCanBeSkipped(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout})
	c1 := probeNode("c1", "p1", map[string]any{})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, c1})
	host.Data.Config["on_error"] = map[string]any{
		"skip": map[string]any{"codes": []any{flow.ErrCodeTimeout}},
	}

	if err := executeNode(host, ctx, "1"); err != nil {
		t.Fatalf("executeNode = %v, want the skip to swallow the scope failure", err)
	}
	nr := findExecuted(ctx, "p1")
	if nr == nil {
		t.Fatal("the host has no node result")
	}
	if nr.Status != flow.StatusSkipped || nr.ErrorAction != flow.ErrorActionSkip {
		t.Fatalf("status/action = %q/%q, want skipped/skip", nr.Status, nr.ErrorAction)
	}
	if nr.ProcessResult == nil || nr.ProcessResult.Parallel == nil || len(nr.ProcessResult.Parallel.Failed) != 1 {
		t.Errorf("the skipped record lost the member snapshot: %+v", nr.ProcessResult)
	}
}

// Both hosts declare skip and handler and nothing else: retrying a body would repeat every member's side effects.
func TestScopeHostsDeclareSkipAndHandlerOnly(t *testing.T) {
	want := map[flow.ErrorAction]bool{flow.ErrorActionSkip: true, flow.ErrorActionHandler: true}
	for _, typ := range []string{"process", "loop"} {
		ex, ok := GetExecutor(typ)
		if !ok {
			t.Fatalf("%s has no executor", typ)
		}
		got := ex.Capabilities().Actions
		if len(got) != len(want) {
			t.Errorf("%s actions = %v, want skip + handler only", typ, got)
		}
		for _, a := range got {
			if !want[a] {
				t.Errorf("%s declares %q, want skip + handler only", typ, a)
			}
		}
	}
}

// The walk is not blocked by a limit or by a member: the serial node between two members executes while the first
// member is still running.
func TestProcessScopeWalkIsNotBlocked(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 200})
	c1 := probeNode("c1", "p1", map[string]any{})
	m2 := memberNode("m2", "p1", map[string]any{"delay_ms": 20})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, c1, m2})

	if _, err := runHost(t, host, ctx); err != nil {
		t.Fatalf("Execute = %v", err)
	}
	first := findExecuted(ctx, "m1")
	serial := findExecuted(ctx, "c1")
	if first == nil || serial == nil {
		t.Fatal("missing node results")
	}
	if !serial.StartTime.Before(first.EndTime) {
		t.Error("the serial chain node waited for the running member: the walk was blocked")
	}
}

// max_concurrency serialises members; a limit of 1 must still let the walk reach the join.
func TestProcessScopeLimitSerialisesMembers(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"delay_ms": 80})
	m2 := memberNode("m2", "p1", map[string]any{"delay_ms": 80})
	host, ctx := buildHost(t, "p1", map[string]any{"max_concurrency": 1}, []*flow.Node{m1, m2})

	started := time.Now()
	nr, err := runHost(t, host, ctx)
	if err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Errorf("elapsed = %v, want the limit of 1 to serialise the two 80ms members", elapsed)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || snap.MaxConcurrency != 1 || len(snap.Members) != 2 {
		t.Fatalf("snapshot = %+v, want two members under a limit of 1", snap)
	}
	// Which member wins the single slot is a scheduling detail; the invariant is that the two runs never overlap.
	a, b := snap.Members[0], snap.Members[1]
	if a.StartAt.After(b.StartAt) {
		a, b = b, a
	}
	if b.StartAt.Before(a.EndAt) {
		t.Error("two members overlapped under max_concurrency 1, so the limit was ignored")
	}
}

// drain: after the first member failure no new work starts, so a later member never runs and the node where the walk
// stopped is recorded as skipped.
func TestProcessScopeDrainStopsLaterChainNodes(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout, "delay_ms": 20})
	c1 := probeNode("c1", "p1", map[string]any{"delay_ms": 150})
	m3 := memberNode("m3", "p1", map[string]any{"delay_ms": 10})
	host, ctx := buildHost(t, "p1", map[string]any{"join": flow.JoinDrain}, []*flow.Node{m1, c1, m3})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want the member failure")
	}
	if nr.ErrCode != flow.ErrCodeTimeout {
		t.Fatalf("code = %q, want timeout", nr.ErrCode)
	}
	if snap := nr.ProcessResult.Parallel; snap == nil || len(snap.Members) != 1 {
		t.Fatalf("snapshot = %+v, want only the dispatched member", snap)
	}
	m3nr := findExecuted(ctx, "m3")
	if m3nr == nil || m3nr.Status != flow.StatusSkipped {
		t.Fatalf("m3 = %+v, want a skipped record: the walk must stop where the scope drained", m3nr)
	}
}

// The body's own chain failure wins over a member failure, which is the one precedence rule the join cannot decide.
func TestProcessScopeChainFailureWins(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout})
	c1 := probeNode("c1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeScript})
	host, ctx := buildHost(t, "p1", nil, []*flow.Node{m1, c1})

	nr, err := runHost(t, host, ctx)
	if err == nil {
		t.Fatal("Execute = nil, want a failure")
	}
	if nr.ErrCode != flow.ErrCodeScript {
		t.Fatalf("code = %q, want the chain's script code", nr.ErrCode)
	}
	if nr.ProcessResult == nil || nr.ProcessResult.Parallel == nil {
		t.Fatal("the snapshot is missing although the host failed through its chain")
	}
}

// A loop body is a scope owner too, and every round gets its own scope: three iterations produce three snapshots that
// never share a member.
func TestLoopScopePerIteration(t *testing.T) {
	registerProbe(t)
	loop := newExecNode("l1", "loop", map[string]any{"loop": map[string]any{"type": "count", "total_times": 3}})
	m1 := memberNode("m1", "l1", map[string]any{"delay_ms": 5})
	m2 := memberNode("m2", "l1", map[string]any{"delay_ms": 5})
	m1.OutEdges = []*flow.Edge{{ID: "e1", Source: "m1", Target: "m2"}}
	entry := entryTo("l1", m1)
	ctx := newContainerContext(t, []*flow.Node{loop, entry, m1, m2})

	nr := newExecResult(loop, ctx)
	if err := (&LoopExecutor{}).Execute(loop, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult == nil || len(nr.LoopResult.Parallel) != 3 {
		t.Fatalf("snapshots = %+v, want one per round", nr.LoopResult)
	}
	seen := map[string]bool{}
	for i, snap := range nr.LoopResult.Parallel {
		if len(snap.Members) != 2 {
			t.Fatalf("round %d members = %d, want 2", i, len(snap.Members))
		}
		for _, m := range snap.Members {
			if seen[m.ProcessID] {
				t.Errorf("process %s appears in more than one round: the scopes mixed", m.ProcessID)
			}
			seen[m.ProcessID] = true
		}
	}
}

// A member outside a host body (the flow root, a function, a branch lane) has no scope to join: it is a config error,
// never a silently serial execution.
func TestConcurrentOutsideHostIsConfigError(t *testing.T) {
	m1 := memberNode("m1", "", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{m1})

	err := execute(m1, ctx, "1", 0)
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
		t.Fatalf("execute = %v, want a config error", err)
	}
	if !strings.Contains(ee.Msg, "no concurrency scope") {
		t.Errorf("msg = %q, want it to say the body has no scope", ee.Msg)
	}
}

// Shapes this version cannot read are refused, never downgraded: a block that quietly behaved as the default would run a
// different shape than the author configured while still reporting success.
func TestProcessScopeRejectsUnsupportedParallelConfig(t *testing.T) {
	cases := []struct {
		name     string
		parallel map[string]any
		want     string
	}{
		{"timeout shape", map[string]any{"timeout": "5s"}, "must be an object"},
		{"unknown join", map[string]any{"join": "collect-all"}, "not a join mode"},
		{"max_concurrency type", map[string]any{"max_concurrency": "4"}, "must be a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m1 := memberNode("m1", "p1", map[string]any{})
			host, ctx := buildHost(t, "p1", tc.parallel, []*flow.Node{m1})
			nr, err := runHost(t, host, ctx)
			if err == nil {
				t.Fatal("Execute = nil, want a config error")
			}
			if nr.Status != flow.StatusFailed || nr.ErrCode != flow.ErrCodeConfig {
				t.Fatalf("status/code = %q/%q, want failed/config", nr.Status, nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.want) {
				t.Errorf("msg = %q, want it to contain %q", nr.ErrMsg, tc.want)
			}
		})
	}
}

// abort is drain plus cancelling the members in flight: the failure must not wait for a member that can no longer
// contribute. The node already executing on the body's own chain is left to finish - only the walk stops, at the next
// boundary - so "unknown outcome" stays confined to the member side.
func TestProcessScopeAbortCancelsRunningMembers(t *testing.T) {
	m1 := memberNode("m1", "p1", map[string]any{"fail": true, "code": flow.ErrCodeTimeout, "delay_ms": 20})
	slow := memberNode("slow", "p1", map[string]any{"delay_ms": 3000})
	c1 := probeNode("c1", "p1", map[string]any{"delay_ms": 100})
	c2 := probeNode("c2", "p1", map[string]any{})
	host, ctx := buildHost(t, "p1", map[string]any{"join": flow.JoinAbort}, []*flow.Node{m1, slow, c1, c2})

	started := time.Now()
	nr, err := runHost(t, host, ctx)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Execute = nil, want the member failure")
	}
	if nr.ErrCode != flow.ErrCodeTimeout {
		t.Fatalf("code = %q, want the first member's timeout", nr.ErrCode)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed = %v, want the 3s member cancelled instead of waited for", elapsed)
	}

	slowNR := findExecuted(ctx, "slow")
	if slowNR == nil || slowNR.Status != flow.StatusCancelled || slowNR.ErrCode != flow.ErrCodeCancel {
		t.Fatalf("slow member = %+v, want it cancelled", slowNR)
	}
	// The cancelled member's own record has to name the cause: "context canceled" alone cannot be acted on.
	if !strings.Contains(slowNR.ErrMsg, "aborted by scope") {
		t.Errorf("slow member err_msg = %q, want the scope's abort reason", slowNR.ErrMsg)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || len(snap.Failed) != 1 || snap.Failed[0] != "m1" {
		t.Fatalf("snapshot = %+v, want only m1 recorded as failed", snap)
	}
	for _, m := range snap.Members {
		if m.NodeID == "slow" && !strings.Contains(m.ErrMsg, "aborted by scope") {
			t.Errorf("slow member msg = %q, want it to say the scope aborted it", m.ErrMsg)
		}
	}

	if c1NR := findExecuted(ctx, "c1"); c1NR == nil || c1NR.Status != flow.StatusSuccess {
		t.Errorf("c1 = %+v, want the node already running to finish", c1NR)
	}
	if c2NR := findExecuted(ctx, "c2"); c2NR == nil || c2NR.Status != flow.StatusSkipped {
		t.Errorf("c2 = %+v, want the walk to stop at the next node boundary", c2NR)
	}
}

// The scope deadline cancels the members in flight and outranks the member failures it caused: "the body timed out" is
// what happened, and the snapshot records that the host stopped waiting instead of a member failing.
func TestProcessScopeTimeoutCancelsRunningMembers(t *testing.T) {
	slow := memberNode("slow", "p1", map[string]any{"delay_ms": 5000})
	c1 := probeNode("c1", "p1", map[string]any{})
	host, ctx := buildHost(t, "p1",
		map[string]any{"timeout": map[string]any{"enabled": true, "seconds": 1}}, []*flow.Node{slow, c1})

	started := time.Now()
	nr, err := runHost(t, host, ctx)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Execute = nil, want the scope timeout")
	}
	if nr.ErrCode != flow.ErrCodeTimeout {
		t.Fatalf("code = %q, want %q", nr.ErrCode, flow.ErrCodeTimeout)
	}
	if elapsed > 3*time.Second {
		t.Errorf("elapsed = %v, want the 5s member cancelled at the 1s deadline", elapsed)
	}
	if !strings.Contains(nr.ErrMsg, "scope timed out after 1s") {
		t.Errorf("msg = %q, want it to name the deadline", nr.ErrMsg)
	}

	slowNR := findExecuted(ctx, "slow")
	if slowNR == nil || slowNR.Status != flow.StatusCancelled {
		t.Fatalf("slow member = %+v, want it cancelled by the deadline", slowNR)
	}
	// Same rule as abort: the record names the deadline instead of the bare context error.
	if !strings.Contains(slowNR.ErrMsg, "scope timed out after 1s") {
		t.Errorf("slow member err_msg = %q, want the deadline named", slowNR.ErrMsg)
	}
	snap := nr.ProcessResult.Parallel
	if snap == nil || !snap.Abandoned {
		t.Fatalf("snapshot = %+v, want the scope marked abandoned", snap)
	}
	// The chain node ran before the deadline, so it is a real success rather than a skipped one.
	if c1NR := findExecuted(ctx, "c1"); c1NR == nil || c1NR.Status != flow.StatusSuccess {
		t.Errorf("c1 = %+v, want it untouched by the member deadline", c1NR)
	}
}
