package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/core"
	"flame/nexus/service/flow/core"
	"flame/nexus/service/flow/executors"
	"flame/nexus/service/mcp/mcpspec"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sessions"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xlog"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer loads this tenant's svc_endpoints (protocol=mcp AND exposed=true) and builds a Server
// instance dynamically. Every endpoint is registered as one MCP tool, with input_schema used directly
// as the tool InputSchema.
func NewMCPServer(authInfo *auth.AuthInfo) (*mcp.Server, error) {
	listReq := &svcendpoints.ListSvcEndpointsRequest{
		Protocol: "mcp",
		Exposed:  boolPtr(true),
	}
	// ListAll is required: List defaults to limit=10 and would silently cut off the 11th endpoint onwards.
	endpoints, err := svcendpoints.NewRepository().ListAll(authInfo, listReq)
	if err != nil {
		return nil, fmt.Errorf("list mcp endpoints: %w", err)
	}

	impl := &mcp.Implementation{
		Name:    "flame-mcp-server",
		Version: "2.0",
	}
	// Only one protocol version is accepted: 2026-07-28 (no initialize handshake, every request carries
	// protocolVersion in its _meta).
	// A new client reporting another version in _meta gets -32022 UnsupportedProtocolVersion from the SDK;
	// an old client still goes through initialize, is negotiated down to 2025-11-25, and the following
	// request carrying the MCP-Protocol-Version header is rejected by the transport with
	// 400 Unsupported protocol version.
	s := mcp.NewServer(impl, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{mcpspec.ProtocolVersion},
	})

	for i := range endpoints {
		ep := endpoints[i]
		// Endpoint scope of the credential: unauthorized targets are not registered.
		// MCP needs exactly this one place — tools/list is generated from the registry, and a call can
		// only hit a registered handler, so there is no bypass like the hand-written request addressed
		// by name that RPC has.
		if !authInfo.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			continue
		}
		tool := buildTool(ep)
		s.AddTool(tool, makeToolHandler(authInfo, ep))
	}
	return s, nil
}

// buildTool builds an mcp.Tool from a SvcEndpoint.
func buildTool(ep models.SvcEndpoint) *mcp.Tool {
	t := &mcp.Tool{
		Name:        ep.Name,
		Description: ep.Description,
	}
	// input_schema is used directly as the tool InputSchema (a JSON Schema object).
	// It is only adopted when the user schema is valid (it parses and its top-level type is object),
	// otherwise it falls back to an open object.
	if !ep.InputSchema.IsNil() {
		var raw json.RawMessage
		if err := ep.InputSchema.Unmarshal(&raw); err == nil && len(raw) > 0 {
			if isObjectSchema(raw) {
				t.InputSchema = raw
			}
		}
	}
	// Fallback: with no input_schema, or an invalid one, use an open object schema (any input is valid).
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t
}

// isObjectSchema checks that the top-level JSON Schema type is object (an SDK requirement).
func isObjectSchema(raw json.RawMessage) bool {
	var s struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s.Type == "object"
}

// makeToolHandler returns the call handler of a tool.
func makeToolHandler(authInfo *auth.AuthInfo, ep models.SvcEndpoint) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input map[string]interface{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
				return nil, fmt.Errorf("invalid tool arguments: %w", err)
			}
		}
		if input == nil {
			input = map[string]interface{}{}
		}
		// The deprecated arguments.stream is stripped (neither a control parameter nor a business
		// variable), with a log line to keep it diagnosable.
		if core.StripLegacyStream(input) {
			xlog.Info("ignoring the deprecated arguments.stream (subscribe to progress through params._meta.progressToken instead) endpoint=%s", ep.Name)
		}

		// Call chain: reject re-entry (f → a → f), the same semantics as /service/rpc (see shared/callchain).
		// The chain is only read from params._meta and never from arguments — the latter is the
		// model-visible business input.
		// Detection is cooperative too: without a chain nothing is detected, so the depth cap must stay.
		chain, chainTooLong := callChain(req)
		if chainTooLong {
			return nil, fmt.Errorf("call_chain exceeds the limit of %d entries", callchain.MaxLen)
		}
		self := callchain.Key(ep.TargetType, ep.TargetID)
		if callchain.Contains(chain, self) {
			xlog.Info("detected a call loop schema=%s endpoint=%s chain=%s", authInfo.Schema, ep.Name, callchain.Format(chain))
			return nil, fmt.Errorf("detected a call loop: %s is already on this call chain (%s)", self, callchain.Format(chain))
		}
		chain = callchain.Append(chain, self)

		switch ep.TargetType {
		case "agent":
			return runAgentTool(authInfo, ep.TargetID, input, req, ctx, chain)
		case "flow":
			return runFlowTool(authInfo, ep.TargetID, input, req, ctx, chain)
		default:
			return nil, fmt.Errorf("unsupported target_type %q for endpoint %s", ep.TargetType, ep.Name)
		}
	}
}

// runFlowTool executes a flow-typed target.
// When the client did not subscribe to progress (no progressToken): run synchronously and return the
// final result. When it did: run in the background, consume the progress/result channels and push them
// as notifications/progress (both channels are pushed with category=text; the final result is only the
// tools/call return value).
func runFlowTool(authInfo *auth.AuthInfo, flowId string, input map[string]interface{}, req *mcp.CallToolRequest, ctx context.Context, chain []string) (*mcp.CallToolResult, error) {
	repo := flows.NewRepository()
	// Production entry point: only the released version runs. Without one it fails loudly and never
	// falls back to the draft — drafts only take effect in the editor debugger / run_flow trial runs.
	Instance, err := repo.GetCurrentFlowVersion(authInfo, flowId)
	if err != nil {
		if errors.Is(err, flows.ErrNoReleasedVersion) {
			return nil, fmt.Errorf("flow %s has no released version yet; release it from the editor before calling it", flowId)
		}
		return nil, fmt.Errorf("get flow instance %s: %w", flowId, err)
	}
	// A deprecated flow cannot run: status lives on the entity row and not in the version snapshot, so
	// an external entry point has to check it itself; it cannot rely on the best-effort act of
	// retracting the endpoint when deprecating (a failure there is only logged).
	if !models.IsRunnable(int(Instance.Status)) {
		return nil, fmt.Errorf("flow %s is deprecated and cannot be called", Instance.Name)
	}

	// Whether progress is pushed is decided by the client alone: params._meta.progressToken present means
	// subscribed. The endpoint has no switch of its own (flows.stream is gone) — the server may not decide
	// on the client's behalf whether notifications are wanted.
	pump := core.NewProgressPump(progressToken(req))

	// The session id is only recognized in params._meta (the same rule as agent endpoints).
	// The ownership check lives here rather than in NewFlow — that one also serves the internal path
	// where an agent calls a flow and shares the caller's session, and there the owner is the parent agent.
	sid := metaSessionID(req)
	if sid != "" {
		// A session belongs to a user (owner only records the creator): a parent and child agent sharing
		// a session would disagree on owner, and closing on owner would reject every legitimate cascade,
		// so this closes on the user like HTTP chat. A missing session is created with that id — protocol
		// endpoints express "first creation" by the caller supplying a UUID.
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeFlow, Instance.ID, authInfo.ID, "",
		); serr != nil {
			return nil, fmt.Errorf("session %s: %w", sid, serr)
		}
		input = core.WithSessionID(input, sid) // NewFlow strips it out of the business input again
	}
	// The output language is supplied the same way and takes the same route: params._meta.language is a pin, so the flow
	// run carries it as its builtin `language` and forwards it to whatever it calls next. A malformed tag is "not
	// supplied" (_meta is advisory).
	if lang := metaLanguage(req); lang != "" {
		input = core.WithOutputLanguage(input, lang)
	}
	// The call chain travels through the reserved key and is stripped by NewFlow (the same mechanism as
	// _call_depth), so it never becomes a business variable.
	input = callchain.WithArguments(input, chain)

	execCtx, err := executors.NewFlow(Instance, input, uuid.NewString(), uuid.Must(uuid.NewV7()).String(), authInfo.Schema, authInfo.ID, ctx)
	if err != nil {
		return nil, fmt.Errorf("init flow exec: %w", err)
	}

	if !pump.Enabled() {
		// The error is already mirrored into execCtx.Error and handled uniformly by finalFlowResult.
		_ = executors.Start(execCtx)
		return finalFlowResult(execCtx)
	}

	// Progress subscribed: run in the background and let the main goroutine consume the channels and
	// push notifications.
	ss := req.Session

	errCh := make(chan error, 1)
	go func() {
		errCh <- executors.Start(execCtx)
		execCtx.CloseChans()
	}()

	progressCh := execCtx.GetProgressChan()
	resultCh := execCtx.GetResultChan()

loop:
	for progressCh != nil || resultCh != nil {
		select {
		case p, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			notifyCategory(ctx, ss, pump, p.Category, core.CleanProgressMarkers(p.Message), nil)
		case r, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			notifyCategory(ctx, ss, pump, core.CategoryText, r, nil)
		case <-errCh:
			// The background execution ended: the business error is already mirrored into execCtx.Error
			// and wrapped uniformly by finalFlowResult (an isError result versus a text result).
			break loop
		case <-ctx.Done():
			execCtx.CloseChans()
			return nil, ctx.Err()
		}
	}

	return finalFlowResult(execCtx)
}

// finalFlowResult checks execCtx.Error when a flow execution ends: an error becomes a result flagged
// isError, otherwise the final result is returned.
func finalFlowResult(execCtx *flow.ExecContext) (*mcp.CallToolResult, error) {
	if e := execCtx.GetError(); e != "" {
		return errorResult(e), nil
	}
	return textResult(execCtx.GetResult()), nil
}

// runAgentTool executes an agent-typed target.
// When the client did not subscribe to progress (no progressToken): run synchronously and return the
// final result. When it did: run in the background, consume the structured event channel and push it as
// notifications/progress (category = thinking / text / tool_call / tool_result).
// An agent has no final frame: the final answer is returned by tools/call, and the stream is split on
// tool_call boundaries.
func runAgentTool(authInfo *auth.AuthInfo, agentID string, input map[string]interface{}, req *mcp.CallToolRequest, ctx context.Context, chain []string) (*mcp.CallToolResult, error) {
	repo := agents.NewRepository()
	agentInst, err := repo.GetAgent(authInfo, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent instance %s: %w", agentID, err)
	}

	// Same as runFlowTool: the progress subscription is expressed by progressToken only. Agent endpoints
	// used to be non-streaming without exception, which was a by-product of "there was no switch to fall
	// back to"; they now follow the same rule as flow endpoints.
	pump := core.NewProgressPump(progressToken(req))

	// The session id is only recognized in params._meta (the same place as progressToken).
	sid := metaSessionID(req)
	// A key of the same name in the business input is stripped: it would become the model-visible
	// {{args.session_id}}. It is no longer a session switch here, only protection against the control
	// field leaking into the prompt by mistake.
	input = core.StripSessionID(input)
	// The same protection for the language hint: supplied through _meta (see metaLanguage), so a business key of that
	// name is only a mistake and must not reach the model as {{args._output_language}}.
	input = core.StripOutputLanguage(input)

	// Session ownership: when a session_id is given, the session must belong to this user (the same rule
	// as HTTP chat). owner only records the creator: a parent and child agent sharing a session would
	// disagree on owner, and closing on owner would reject every legitimate call_agent cascade, so the
	// check closes on the user; a missing session is created with that id (a caller-supplied UUID is the
	// intent of a first creation).
	if sid != "" {
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeAgent, agentInst.ID, authInfo.ID, "",
		); serr != nil {
			return nil, fmt.Errorf("session %s: %w", sid, serr)
		}
	}

	// user_id is always injected: verifyAPIKey / verifyOAuth only accept credentials bound to a user
	// (see auth.go), and MCP has no tenant-level or anonymous identity — run-domain isolation, session
	// ownership and session memory all key off it.
	// session_id comes from the caller (already stripped from the business input): when given it is a
	// session call, otherwise a single turn that records no memory.
	agentArguments := map[string]interface{}{
		"args":      input,
		"user_id":   authInfo.ID,
		"_run_type": "mcp",
	}
	if sid != "" {
		agentArguments["session_id"] = sid
	}
	// The chain goes into a top-level reserved key, not inside args: args are the model-visible business
	// input, and a control field there would become {{args._call_chain}}.
	if len(chain) > 0 {
		agentArguments[callchain.ArgumentKey] = chain
	}
	// Same shape for the caller's language (params._meta.language): a top-level reserved key, never inside args.
	// Without it a programmatic call whose arguments are pure JSON leaves the model no language signal at all, and it
	// answers in the prompt's language.
	if lang := metaLanguage(req); lang != "" {
		agentArguments[core.OutputLanguageKey] = lang
	}
	// Production entry point: read the released current version (refuse when there is none, never fall
	// back to the draft).
	agent.SetReleaseMode(agentArguments)
	inst, err := agent.NewAgent(agentInst, authInfo.Schema, agentArguments)
	if err != nil {
		// Missing / unusable configuration (no default model etc.): MCP has no application error code, so report it as
		// a tool-level error - the caller, or the model behind it, reads the sentence instead of a protocol-level
		// server fault that says nothing about where to look.
		if ce, isCfg := agent.AsConfigError(err); isCfg {
			return errorResult(ce.Msg), nil
		}
		return nil, fmt.Errorf("init agent: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("agent %s not supported or not found", agentID)
	}
	defer inst.Close()

	if !pump.Enabled() {
		// Use the caller's ctx: when the peer closes the connection or cancels the request, this run is
		// cancelled on the same ctx without waiting for its LLM turns and sandbox calls to finish (the
		// same rule as HTTP chat).
		// The error is already mirrored into inst.Error and handled uniformly by finalAgentResult.
		_ = inst.Stream(ctx)
		inst.CloseChans()
		if ctx.Err() != nil {
			return nil, ctx.Err() // the caller disconnected: no result to return
		}
		return finalAgentResult(inst)
	}

	// Progress subscribed: run in the background and let the main goroutine consume the "structured
	// event" channel and push notifications.
	// Thinking (EventThinking) and tool calls only appear here; the earlier plain-text progress channel
	// has been removed.
	ss := req.Session

	errCh := make(chan error, 1)
	go func() {
		errCh <- inst.Stream(ctx)
		inst.CloseChans()
	}()

	eventCh := inst.GetEventChan()
	// After the caller disconnects, stop pushing notifications but keep draining the event channel: the
	// run shares this call's ctx and has already been cancelled by the disconnect — wait for it to wind
	// down (persistence, killing resident processes) before returning. Returning directly would let
	// defer inst.Close() run while Stream is still active, fighting over the same one-shot working directory.
	gone := ctx.Done()

loop:
	for eventCh != nil {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			if gone == nil {
				continue // already disconnected: read only, never push
			}
			if item, ok := ev.ToProgress(); ok {
				notifyCategory(ctx, ss, pump, item.Category, item.Message, item.Meta)
			}
		case <-errCh:
			// The background execution ended: the business error is already mirrored into inst.Error and
			// wrapped uniformly by finalAgentResult.
			break loop
		case <-gone:
			gone = nil // nil blocks that branch for good, avoiding a busy loop
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err() // the caller disconnected: nobody receives the result
	}
	return finalAgentResult(inst)
}

// finalAgentResult checks inst.GetError() when an agent execution ends: an error becomes a result
// flagged isError, otherwise the final result is returned.
func finalAgentResult(inst core.Agent) (*mcp.CallToolResult, error) {
	if e := inst.GetError(); e != "" {
		return errorResult(e), nil
	}
	return textResult(inst.GetResult()), nil
}

// textResult builds a plain-text tool result.
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: s},
		},
	}
}

// errorResult builds a plain-text tool result flagged isError (the tool ran but the business result failed).
func errorResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: s},
		},
		IsError: true,
	}
}

// callChain reads the ancestor chain the client supplied in params._meta["call_chain"].
//
// tooLong means it exceeds MaxLen: it must be rejected outright, never truncated — the dropped entries
// may be exactly the re-entry being looked for.
func callChain(req *mcp.CallToolRequest) (chain []string, tooLong bool) {
	if req == nil || req.Params == nil {
		return nil, false
	}
	raw := req.Params.Meta[callchain.MetaKey]
	if callchain.RawLen(raw) > callchain.MaxLen {
		return nil, true
	}
	return callchain.Normalize(raw), false
}

// metaSessionID reads the session id the client supplied in params._meta["session_id"].
//
// Symmetric to progressToken: both are metadata that "affects how this call is handled", neither belongs
// to the endpoint's business parameters. An empty return means "not supplied" (a stateless run, falling
// back to the runs/ scratch area).
func metaSessionID(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	s, _ := req.Params.Meta[core.MetaSessionIDKey].(string)
	return strings.TrimSpace(s)
}

// metaLanguage reads the output language the caller supplied in params._meta["language"].
//
// The same family as metaSessionID: it describes how the call should be answered, not what the business input is. An
// unrecognised tag counts as "not supplied" rather than an error - _meta is advisory, and failing a whole call over a
// mistyped tag would be worse than ignoring the hint.
func metaLanguage(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	s, _ := req.Params.Meta[core.MetaLanguageKey].(string)
	tag, _ := core.NormalizeOutputLanguage(s)
	return tag
}

// progressToken reads the subscription the client expressed in params._meta.progressToken.
//
// nil means "not subscribed" — and then not a single progress frame may be sent: MCP 2026-07-28 requires
// notifications/progress to reference only a token supplied by an active request. The earlier approach
// generated a UUID when nothing was supplied, which pushed onto a token the client never subscribed to.
func progressToken(req *mcp.CallToolRequest) any {
	if req == nil || req.Params == nil {
		return nil
	}
	t, ok := req.Params.Meta["progressToken"]
	if !ok || !core.ValidProgressToken(t) {
		return nil
	}
	return t
}

// notifyCategory pushes one increment of the given category through a standard notifications/progress.
// The category values and the event mapping live in agent/core (the Category* constants /
// Event.ToProgress). Nothing is sent when both message and extra are empty, avoiding empty frames — and
// therefore avoiding burning a sequence number.
func notifyCategory(ctx context.Context, ss *mcp.ServerSession, pump *core.ProgressPump, category, message string, extra map[string]any) {
	if !pump.Enabled() {
		return
	}
	if message == "" && len(extra) == 0 {
		return
	}
	meta := map[string]any{"category": category}
	for k, v := range extra {
		meta[k] = v
	}
	_ = ss.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
		Meta:          meta,
		ProgressToken: pump.Token,
		// progress MUST increase: the value here is "which notification this is", not real completion,
		// and the total is unknown, so Total is left out (the spec allows omitting it).
		Progress: pump.Next(),
		Message:  message,
	})
}

// boolPtr is a helper.
func boolPtr(b bool) *bool {
	return &b
}

// NewMCPHandler returns the gin route handler: it resolves the tenant and authenticates first (Bearer
// dispatch), then delegates to the SDK StreamableHTTPHandler (stateless mode).
func NewMCPHandler() http.HandlerFunc {
	streamable := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			ai, ok := AuthInfoFromContext(r.Context())
			if !ok {
				return nil
			}
			s, err := NewMCPServer(ai)
			if err != nil {
				return nil
			}
			return s
		},
		&mcp.StreamableHTTPOptions{
			Stateless: true,
			// Session headers may be absent (stateless mode).
			JSONResponse: false,
			// The SDK handles the protocol details; authentication failures are already intercepted above.
		},
	)

	return func(w http.ResponseWriter, r *http.Request) {
		authInfo, err := resolveTenant(r)
		if err != nil {
			xhttpErr(w, http.StatusBadRequest, "tenant resolution failed: "+err.Error())
			return
		}
		ai, err := authenticate(authInfo, r)
		if err != nil {
			if err == errNoAuth {
				writeWWWAuthenticate(w, authInfo)
				return
			}
			xhttpErr(w, http.StatusUnauthorized, "authentication failed: "+err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), authInfoKey, ai)
		streamable.ServeHTTP(w, r.WithContext(ctx))
	}
}

// xhttpErr writes a plain JSON error (an authentication failure never logs the credential in clear).
func xhttpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	})
}
