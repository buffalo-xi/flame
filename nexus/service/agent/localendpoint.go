package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/agentconfig"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xlog"
)

// RunLocalEndpointFunc executes a local endpoint in-process; flow/executors registers it.
//
// Why a registration point instead of a direct import: executors imports this package in reverse (the rpc node calls agent endpoints
// directly, see executors/rpc_target.go), so importing it back would create a cycle. A registration function is equivalent to inverting
// "run a flow" outward, keeping the dependency direction one-way: executors -> service/agent -> tools.
//
// The signature matches executors.runEndpoint without onProgress: a tool call only has the final result as its channel, and subscribing
// to progress would just collect all the tiny flow events again (the same choice as the rpc node's progress, off by default).
type RunLocalEndpointFunc func(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	sessionID string,
	callDepth int,
	endpointID string,
	args map[string]any,
	wantProgress bool,
	followDrafts bool,
) (content string, isError bool, err error)

var runLocalEndpoint RunLocalEndpointFunc

// SetLocalEndpointRunner is called by flow/executors from init and takes effect process-wide.
func SetLocalEndpointRunner(fn RunLocalEndpointFunc) { runLocalEndpoint = fn }

// localEndpointRunner translates local__<endpoint name> into one in-process endpoint execution.
//
// It only covers local endpoints (local__); mcp__ / rpc__ belong to externalToolRunner, and the prefixes do not overlap.
//
// The endpoint catalog is cached per run instance (eps): one agent loop may call local__ several times in a row, and listing
// svc_endpoints every time is pure waste; the endpoint table does not change within a round, so the snapshot semantics match
// "this round's tool catalog is frozen when the run starts".
type localEndpointRunner struct {
	mu  sync.Mutex
	eps []models.SvcEndpoint
}

var _ tools.EndpointRunner = (*localEndpointRunner)(nil)

func (*localEndpointRunner) Has(name string) bool {
	return strings.HasPrefix(name, tools.ExternalPrefixLocal)
}

func (r *localEndpointRunner) Invoke(ctx context.Context, in tools.LocalCall) (any, error) {
	if in.Schema == "" {
		return nil, fmt.Errorf("tool %s is a local endpoint, but the tenant context is missing", in.ToolName)
	}
	epName := strings.TrimPrefix(in.ToolName, tools.ExternalPrefixLocal)
	if epName == "" || epName == in.ToolName {
		return nil, fmt.Errorf("%w: %s", tools.ErrToolNotFound, in.ToolName)
	}

	// A chain that involves an agent uses the agent depth cap (platform.agent_configs.max_call_depth, default 3, the same source as
	// call_agent): every return into an agent costs a whole extra LLM round, far more than pure flow orchestration, so it cannot share the
	// flow-side max_recursion_depth (default 10). Checked before the endpoint lookup: when over the cap, not even the database is touched.
	if maxDepth := agentconfig.Resolve(in.Schema).MaxCallDepth; maxDepth > 0 && in.CallDepth >= maxDepth {
		return nil, fmt.Errorf("agent call depth exceeded (%d levels, cap %d): refusing to call %s",
			in.CallDepth, maxDepth, in.ToolName)
	}

	// The execution layer narrows once more: only protocol=rpc with target_type=flow is accepted.
	// The catalog side already narrows it (Catalog.localRPCConns); this is defence in depth - otherwise one hand-written
	// local:rpc:<agent name> grant row could call an agent endpoint directly, bypassing both agent_relations and the
	// "main cannot be called by other agents" gate (see the notes in toolgrant/ref.go).
	ep, err := r.findEndpoint(in.Schema, epName)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, fmt.Errorf("%w: %s (the local endpoint does not exist, or it is not an rpc-protocol workflow endpoint)",
			tools.ErrToolNotFound, in.ToolName)
	}
	if runLocalEndpoint == nil {
		return nil, fmt.Errorf("tool %s cannot run: the local endpoint execution channel is not wired (this process did not load flow/executors)", in.ToolName)
	}

	// Session pass-through: it puts the called flow and the caller in the same users/{uid}/sessions/{sid}/ workspace.
	// session_id is stripped by NewFlow's extractSessionID, so it does not pollute the flow's business arguments.
	args := in.Args
	if in.SessionID != "" {
		args = make(map[string]any, len(in.Args)+1)
		for k, v := range in.Args {
			args[k] = v
		}
		args["session_id"] = in.SessionID
	}

	authInfo := &auth.AuthInfo{Schema: in.Schema, ID: in.UserID}
	// The ancestor chain is handed to the execution layer through the ctx: the endpoint call does cycle detection there and passes it on
	// as "chain + target". Not a function parameter: RunLocalEndpointFunc already has 8 positional arguments, and the chain is pure control
	// information.
	ctx = callchain.With(ctx, in.CallChain)
	// runLocalEndpoint resolves by endpoint name (names are unique and renamed together, while ids change after export/import): ep was
	// just looked up by name
	content, isError, err := runLocalEndpoint(ctx, authInfo, in.SessionID, in.CallDepth, ep.Name, args, false,
		!in.DefinitionMode.IsCurrent())
	if err != nil {
		return nil, fmt.Errorf("local endpoint %s failed: %w", ep.Name, err)
	}
	xlog.Info("local endpoint tool call schema=%s agent=%s endpoint=%s depth=%d is_error=%v",
		in.Schema, in.AgentName, ep.Name, in.CallDepth, isError)
	// Business errors use the same convention as external tools: is_error=true still hands the body to the model instead of raising a Go
	// error (only by seeing the error body can the model correct itself; connection / wiring failures go through err).
	return externalResult(content, nil, isError, in.MaxOutput), nil
}

// findEndpoint looks up, by exact name, an endpoint of this tenant with protocol=rpc that targets a workflow.
//
// The Name filter of ListAll cannot be used: endpointWhere matches name with ILIKE
// (e.name ILIKE '%name%'), so "poet" also hits "poet-2" and a name-based call may reach a different endpoint.
func (r *localEndpointRunner) findEndpoint(schema, name string) (*models.SvcEndpoint, error) {
	list, err := r.endpoints(schema)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// endpoints returns this run's endpoint catalog snapshot (a database read on the first call, reused afterwards).
//
// Only rows with protocol=rpc and target=flow are taken: that is the only target shape local__ may call
// (see the defence in depth in Invoke).
func (r *localEndpointRunner) endpoints(schema string) ([]models.SvcEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eps != nil {
		return r.eps, nil
	}
	list, err := svcendpoints.NewRepository().ListAll(
		&auth.AuthInfo{Schema: schema},
		&svcendpoints.ListSvcEndpointsRequest{
			Protocol:   toolgrant.LocalProtocolRPC,
			TargetType: toolgrant.LocalTargetFlow,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to query local endpoints: %w", err)
	}
	r.eps = list
	return list, nil
}
