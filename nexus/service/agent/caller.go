package agent

import (
	"context"
	"fmt"
	"strings"

	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/agentrelations"
	agents "flame/nexus/shared/repository/agents"
)

// The cascade depth cap is no longer a constant: it comes from platform.agent_configs (see shared/agentconfig), snapshotted by
// newAgentImpl and injected through NewAgentCaller. A child agent resolves it once when created (same tenant, same config), so the whole
// call tree reaches the same conclusion.

// agentCaller implements tools.AgentCaller: it calls another agent synchronously and returns its final result.
// Each call spins up a temporary instance from its definition and reclaims it when done (not resident, so it needs no lifecycle
// management).
type agentCaller struct {
	schema     string
	agentsRepo agents.Repository
	rel        agentrelations.Repository
	emit       func(core.Event) // forwards the callee's events into the parent event stream (may be nil)
	maxDepth   int              // maximum cascade depth (agentconfig; falls back to 3 when <= 0)
}

// NewAgentCaller creates an AgentCaller (with the tenant schema fixed).
// emit forwards the callee's thinking / tool calls / content events to the parent so its process is visible in the main conversation (may
// be nil).
// maxDepth comes from agentconfig (tenant / system levels); passing <= 0 falls back to 3 - it must never become "unlimited depth".
func NewAgentCaller(schema string, emit func(core.Event), maxDepth int) tools.AgentCaller {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	return &agentCaller{
		schema:     schema,
		agentsRepo: agents.NewRepository(),
		rel:        agentrelations.NewRepository(),
		emit:       emit,
		maxDepth:   maxDepth,
	}
}

// Call: depth check -> load definition -> grant check -> start a temporary instance -> run synchronously -> return the result.
func (c *agentCaller) Call(ctx context.Context, req tools.CallRequest) (string, error) {
	if req.Depth >= c.maxDepth {
		return "", fmt.Errorf("agent call depth exceeded (limit %d)", c.maxDepth)
	}

	inst, err := c.getByName(req.CalleeName)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", fmt.Errorf("agent %q not found", req.CalleeName)
	}
	// A deprecated agent cannot be called
	if !models.IsRunnable(int(inst.Status)) {
		return "", fmt.Errorf("agent %q is deprecated", req.CalleeName)
	}
	// The supervisor can be called by other agents: its capabilities must not be discounted by the caller, and it always reads the draft
	// (resolveDefinitionMode), so it is still the full management surface when invoked. The risk is covered by grants - the check below
	// applies to the supervisor too, and a missing grant row is always refused.

	// Grant check: every agent is treated alike (no row = refused; no privileged account)
	allowed, aerr := c.rel.Allowed(c.schema, req.CallerID, inst.ID)
	if aerr != nil {
		return "", aerr
	}
	if !allowed {
		return "", fmt.Errorf("agent %q is not allowed to call %q", req.CallerName, req.CalleeName)
	}

	// Cycle detection: stop when the target is already in its own ancestor chain (f -> a -> f) instead of waiting for the depth cap to be
	// reached. The same test as RPC inbound (see shared/callchain): repeated sibling calls are not in the chain and are not affected.
	self := callchain.Key(callchain.KindAgent, inst.ID)
	if callchain.Contains(req.Chain, self) {
		return "", fmt.Errorf("cycle detected: %s is already in this call chain (%s)", self, callchain.Format(req.Chain))
	}

	// Arguments for the callee: args is its input, and the call depth, userID and session are passed through
	// (session pass-through puts the child agent and the parent in the same workspace)
	sub := map[string]any{
		"args":        req.Args,
		"_call_depth": req.Depth + 1,
		"_run_type":   "agent",
	}
	// Chain: append the callee itself before passing it down (when the child continues deeper, this level becomes an ancestor)
	if chain := callchain.Append(req.Chain, self); len(chain) > 0 {
		sub[callchain.ArgumentKey] = chain
	}
	if req.UserID != "" {
		sub["user_id"] = req.UserID
	}
	if req.SessionID != "" {
		sub["session_id"] = req.SessionID
	}
	// The definition mode follows the parent: a production parent -> the child reads the released version; a debugging parent -> the child
	// reads the draft. Without passing it through, a child agent runs the live version while debugging, which easily misleads during triage.
	if req.DefinitionMode.IsCurrent() {
		SetReleaseMode(sub)
	} else {
		SetDraftMode(sub)
	}

	a, err := NewAgent(inst, c.schema, sub)
	if err != nil {
		return "", err
	}
	// A session-less child agent lands in a one-shot workspace under runs/{run_id}/ and is reclaimed by Close()
	// (cleanupEphemeralWorkspace: KillInteractiveRun first, then DeletePrefix; with a session it is skipped and the session directory is left
	// alone). The rpc / mcp / flow entries all defer Close; only this one was missing - every call_agent left a directory behind until the
	// next process restart swept it away.
	defer a.Close()

	// Forward the callee's events to the parent while it runs (tagged with the source agent and depth)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range a.GetEventChan() {
			if c.emit == nil {
				continue
			}
			ev.Agent = inst.Name
			ev.Depth = req.Depth + 1
			c.emit(ev)
		}
	}()

	// Parent ctx pass-through: when the parent is stopped (user stop / disconnect) the child instance exits immediately instead of
	// finishing the run and discarding the result.
	streamErr := a.Stream(ctx)
	a.CloseChans() // close eventCh -> the goroutine above exits
	<-done
	if streamErr != nil {
		return "", streamErr
	}
	return a.GetResult(), nil
}

// Callable lists the agents the caller may call (an out-edge row ∩ runnable ∩ not the super agent), the same source as Call.
// All of it comes from one JOIN query (name / description / status), with no second query per target.
func (c *agentCaller) Callable(ctx context.Context, callerID string) ([]tools.AgentInfo, error) {
	if callerID == "" {
		return nil, nil
	}
	list, err := c.rel.ListCallableByCaller(c.schema, callerID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	out := make([]tools.AgentInfo, 0, len(list))
	for _, m := range list {
		// Only deprecated agents cannot be called (see IsRunnable); the supervisor is listed as usual - whether it can be called is decided
		// by the grant relation, not by a hard identity refusal.
		if !models.IsRunnable(m.Status) {
			continue
		}
		out = append(out, tools.AgentInfo{
			ID: m.ID, Name: m.Name, Description: m.Description, Status: m.Status,
		})
	}
	return out, nil
}

// getByName looks a definition up by exact name (ListAgents matches Name fuzzily, so an exact comparison is needed).
//
// Limit must be -1 (all): the repository returns rows sorted by name ascending, and with a LIMIT the exact match may be pushed out of the
// window. Example: with writer / writer-1 ... writer-9, looking up "writer-5" returns writer, writer-1..4 first and the exact match is cut -
// an existing callee is reported as "not found".
func (c *agentCaller) getByName(name string) (*models.Agent, error) {
	instances, _, err := c.agentsRepo.ListAgents(&auth.AuthInfo{Schema: c.schema},
		&agents.ListAgentsRequest{Name: name, Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if strings.EqualFold(instances[i].Name, name) {
			return &instances[i], nil
		}
	}
	return nil, nil
}
