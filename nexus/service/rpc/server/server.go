package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/core"
	"flame/nexus/service/flow/core"
	"flame/nexus/service/flow/executors"
	"flame/nexus/service/rpc/protocol"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/repository/sessions"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/conv"
	"flame/tools/xjson"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// JSON-RPC 2.0 error codes.
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeInvalidParams  = -32602
	errCodeInternalError  = -32603
	// errCodeQuotaExceeded means the quota is exhausted (session count limit etc.): JSON-RPC reserves
	// -32000..-32099 for implementation-defined errors, matching console 507, so the client can say
	// "go clean up" instead of reading it as a server fault.
	errCodeQuotaExceeded = -32000
	// errCodeCallLoop means a call loop was detected (f → a → f): the chain itself is cyclic, retrying
	// cannot succeed, and the client should report "misconfigured cycle" rather than a server fault.
	errCodeCallLoop = -32001
)

// rpcRequest is the JSON-RPC request sent by the client.
type rpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version"` // protocol major version; absent means MinSupportedVersion
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      interface{} `json:"id"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// rpcResponse is the JSON-RPC response envelope; result and error are mutually exclusive.
type rpcResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

// rpcToolContent is one text segment of a tool result (aligned with MCP TextContent).
type rpcToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// rpcToolResultMsg is a tool call result: content is always an array of text segments
// (aligned with MCP CallToolResult).
type rpcToolResultMsg struct {
	Content []rpcToolContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// rpcProgressMsg holds the parameters of a progress notification (field layout aligned with
// notifications/progress of MCP 2026-07-28).
//
// progressToken sits at the top level of params (it identifies which subscription of this call the
// frame belongs to); _meta is a key inside params. Progress is "which notification this is" and is
// only guaranteed to increase monotonically — it does not express completion, and Total is omitted
// when the total is unknown.
type rpcProgressMsg struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total,omitempty"`
	Message       string  `json:"message"`
	// Meta is a map[string]any: agent events (tool_call / tool_result) carry non-string fields such as
	// call_id / ok / duration_ms inside _meta.
	Meta map[string]any `json:"_meta,omitempty"`
}

// rpcProgress is a progress notification (notifications/progress).
type rpcProgress struct {
	Jsonrpc string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  rpcProgressMsg `json:"params"`
}

// rpcTool is a tool definition (aligned with the Tool structure returned by MCP tools/list).
type rpcTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// rpcListToolsResult is the result structure of tools/list (aligned with MCP ListToolsResult).
type rpcListToolsResult struct {
	Tools         []rpcTool `json:"tools"`
	ServerVersion int       `json:"serverVersion"` // current protocol version of the server, for client alignment
}

// NewRPCHandler returns the RPC handler: it resolves the tenant and authenticates first (Bearer
// dispatch), then parses the JSON-RPC request and dispatches by svc_endpoints name (protocol=rpc)
// to the agent or flow domain.
func NewRPCHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authInfo, err := resolveTenant(r)
		if err != nil {
			xhttpErr(w, http.StatusBadRequest, "tenant resolution failed: %s", err.Error())
			return
		}
		ai, err := authenticate(authInfo, r)
		if err != nil {
			if err == errNoAuth {
				writeWWWAuthenticate(w, authInfo)
				return
			}
			xhttpErr(w, http.StatusUnauthorized, "authentication failed: %s", err.Error())
			return
		}

		// Parse the JSON-RPC request body.
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			xhttpErr(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
			return
		}
		if req.Jsonrpc != "2.0" || req.Method == "" {
			xhttpErr(w, http.StatusBadRequest, "invalid jsonrpc request")
			return
		}

		// Normalize and validate the version (through the shared package, so nothing is hardcoded here).
		req.Version = protocol.NormalizeVersion(req.Version)
		if !protocol.IsVersionSupported(req.Version) {
			writeVersionError(w, req.Version)
			return
		}

		// tools/list: return this tenant's protocol=rpc and exposed tools (aligned with MCP).
		if req.Method == "tools/list" {
			writeToolsList(w, req.ID, ai)
			return
		}

		// Extract name / arguments from params (a bad argument shape: MCP InvalidParams(-32602), HTTP stays 200).
		name, err := extractName(req.Params)
		if err != nil {
			writeRPCError(w, req.ID, errCodeInvalidParams, "failed to read the service name: %s", err.Error())
			return
		}
		arguments, err := extractArguments(req.Params)
		if err != nil {
			writeRPCError(w, req.ID, errCodeInvalidParams, "failed to read the arguments: %s", err.Error())
			return
		}

		// Strip the deprecated arguments.stream in place: it must not be treated as a flow / agent
		// business variable, and it must no longer influence the decision (which only looks at
		// progressToken below). A silent no-op is harder to diagnose, hence the log line.
		if core.StripLegacyStream(arguments) {
			xlog.Info("ignoring the deprecated arguments.stream (subscribe to progress through params._meta.progressToken instead)")
		}
		// Progress subscription: the client expresses it in params._meta.progressToken, the same place as MCP.
		pump := core.NewProgressPump(extractProgressToken(req.Params))

		// Look up the svc_endpoints row (protocol=rpc) by name.
		ep, err := findEndpoint(ai, name)
		if err != nil {
			// The tool does not exist: the resource name (params.name) is invalid, so MCP InvalidParams(-32602).
			writeRPCErrorCode(w, req.ID, errCodeInvalidParams, "endpoint.not_found",
				map[string]string{"name": name}, "%s", err.Error())
			return
		}
		// The credential may not call this endpoint: use exactly the same wording **and code** as
		// "does not exist", so "exists but unauthorized" never leaks.
		// The code has to match too — aligning only the wording lets the frontend tell the two apart
		// once it translates them.
		// This check comes before the exposed check — not even the fact "not exposed" may be probed by
		// an unauthorized caller.
		if !ai.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			writeRPCErrorCode(w, req.ID, errCodeInvalidParams, "endpoint.not_found",
				map[string]string{"name": name}, "service endpoint does not exist: %s", name)
			return
		}
		if !ep.Exposed.Bool() {
			// The endpoint is not exposed: the resource is unavailable (the argument points at an
			// internal-only endpoint), which MCP classifies as InvalidParams.
			writeRPCErrorCode(w, req.ID, errCodeInvalidParams, "endpoint.not_exposed",
				map[string]string{"name": name}, "service endpoint is not exposed: %s", name)
			return
		}

		// Call chain: reject re-entry (f → a → f) and pass the entity appended to it down to the
		// execution layer. The check comes after the exposed one: an unauthorized / non-exposed
		// endpoint must not even reveal "is it on the chain".
		// The chain is reported by the caller, so without it nothing is detected — which is why the
		// depth cap (max_call_depth / max_recursion_depth) must stay as the backstop; this only
		// catches precise loops.
		chain, chainTooLong := callchain.ParseParams(req.Params)
		if chainTooLong {
			writeRPCError(w, req.ID, errCodeInvalidParams,
				"call_chain exceeds the limit of %d entries", callchain.MaxLen)
			return
		}
		self := callchain.Key(ep.TargetType, ep.TargetID)
		if callchain.Contains(chain, self) {
			xlog.Info("detected a call loop schema=%s endpoint=%s chain=%s", ai.Schema, ep.Name, callchain.Format(chain))
			writeRPCError(w, req.ID, errCodeCallLoop,
				"detected a call loop: %s is already on this call chain (%s)", self, callchain.Format(chain))
			return
		}
		// Append before passing down: sibling branches each derive from the parent chain and do not
		// affect each other (two parallel nodes of f both calling a are still allowed).
		chain = callchain.Append(chain, self)

		reqID := req.ID
		if reqID == nil || conv.ToString(reqID) == "" {
			reqID = uuid.NewString()
		}

		switch ep.TargetType {
		case "agent":
			runAgent(w, r, ai, ep.TargetID, req.Params, arguments, reqID, pump, chain)
		case "flow":
			runFlow(w, r, ai, ep.TargetID, req.Params, arguments, reqID, pump, chain)
		default:
			// Unsupported target_type: the target the argument points at is invalid, so MCP InvalidParams(-32602).
			writeRPCError(w, reqID, errCodeInvalidParams, "unsupported target_type %q for endpoint %s", ep.TargetType, ep.Name)
		}
	}
}

// extractName pulls the "name" field (string) out of params.
func extractName(params interface{}) (string, error) {
	mp, ok := params.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("params must be an object")
	}
	name, _, err := conv.GetPropertyAsString(mp, "name")
	return name, err
}

// extractArguments pulls the "arguments" field (map) out of params.
func extractArguments(params interface{}) (map[string]interface{}, error) {
	mp, ok := params.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("params must be an object")
	}
	argumentsObj, err := conv.GetProperty(mp, "arguments")
	if err != nil {
		return nil, err
	}
	arguments, ok := argumentsObj.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("arguments must be an object")
	}
	return arguments, nil
}

// findEndpoint looks up this tenant's protocol=rpc svc_endpoints by exact name (case-insensitive).
// It uses an exact query rather than the ILIKE of List, so that "order" cannot hit "order-v2".
func findEndpoint(authInfo *auth.AuthInfo, name string) (*models.SvcEndpoint, error) {
	ep, err := svcendpoints.NewRepository().FindByName(authInfo, "rpc", name)
	if err != nil {
		return nil, fmt.Errorf("failed to read the service endpoint: %w", err)
	}
	if ep == nil {
		return nil, fmt.Errorf("service endpoint does not exist: %s", name)
	}
	return ep, nil
}

// listTools returns this tenant's protocol=rpc endpoints with exposed=true, mapped to tool definitions.
// The logic mirrors NewMCPServer + buildTool of MCP: input_schema becomes the tool InputSchema.
func listTools(authInfo *auth.AuthInfo) ([]rpcTool, error) {
	repo := svcendpoints.NewRepository()
	listReq := &svcendpoints.ListSvcEndpointsRequest{
		Protocol: "rpc",
		Exposed:  boolPtr(true),
	}
	// ListAll is required: List defaults to limit=10 and would silently cut off the 11th endpoint onwards.
	endpoints, err := repo.ListAll(authInfo, listReq)
	if err != nil {
		return nil, fmt.Errorf("list rpc endpoints: %w", err)
	}

	tools := make([]rpcTool, 0, len(endpoints))
	for i := range endpoints {
		ep := endpoints[i]
		// Endpoint scope of the credential: unauthorized targets do not even appear in the list
		// (the call side checks again — filtering the list alone could be bypassed by a hand-written
		// JSON-RPC call that addresses the name directly).
		if !authInfo.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			continue
		}
		tool := rpcTool{
			Name:        ep.Name,
			Description: ep.Description,
		}
		// input_schema is used directly as the tool InputSchema (a JSON Schema object).
		// It is only adopted when the schema is valid (it parses and its top-level type is object),
		// otherwise an open object is used.
		if !ep.InputSchema.IsNil() {
			var raw json.RawMessage
			if err := ep.InputSchema.Unmarshal(&raw); err == nil && len(raw) > 0 {
				if isObjectSchema(raw) {
					tool.InputSchema = raw
				}
			}
		}
		if tool.InputSchema == nil {
			tool.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// writeToolsList writes the tools/list response (a standard JSON-RPC 2.0 response, not streamed).
func writeToolsList(w http.ResponseWriter, reqID interface{}, authInfo *auth.AuthInfo) {
	tools, err := listTools(authInfo)
	if err != nil {
		xhttpErr(w, http.StatusInternalServerError, "list tools failed: %s", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result: rpcListToolsResult{
			Tools:         tools,
			ServerVersion: protocol.CurrentProtocolVersion,
		},
	})
}

// isObjectSchema checks that the top-level JSON Schema type is object (consistent with the MCP server).
func isObjectSchema(raw json.RawMessage) bool {
	var s struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s.Type == "object"
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool {
	return &b
}

// runFlow executes a flow-typed target and writes the JSON-RPC response.
// When the client did not subscribe to progress (no progressToken): run synchronously and answer with a
// single JSON body. When it did: run in the background and push notifications/progress over SSE, with
// the final result still being the response carrying the id inside the stream.
// params is passed through only to read _meta (the proper place for the session id); the business input
// remains input.
func runFlow(w http.ResponseWriter, r *http.Request, authInfo *auth.AuthInfo, flowId string, params any, input map[string]interface{}, reqID interface{}, pump *core.ProgressPump, chain []string) {
	repo := flows.NewRepository()
	// Production entry point: only the released version runs; refuse when there is none, never fall back to the draft.
	Instance, err := repo.GetCurrentFlowVersion(authInfo, flowId)
	if err != nil {
		if errors.Is(err, flows.ErrNoReleasedVersion) {
			writeRPCErrorCode(w, reqID, errCodeInvalidParams, "flow.not_released",
				map[string]string{"name": flowId},
				"flow %s has no released version yet; release it from the editor before calling it", flowId)
			return
		}
		// The instance does not exist / the query failed: the resource the argument points at is
		// missing, so MCP InvalidParams(-32602).
		writeRPCError(w, reqID, errCodeInvalidParams, "get flow instance %s: %v", flowId, err)
		return
	}
	// A deprecated flow cannot run: status lives on the entity row and not in the version snapshot, so
	// an external entry point has to check it itself; it cannot rely on the best-effort act of
	// retracting the endpoint when deprecating (a failure there is only logged).
	if !models.IsRunnable(int(Instance.Status)) {
		writeRPCErrorCode(w, reqID, errCodeInvalidParams, "flow.deprecated",
			map[string]string{"name": Instance.Name}, "flow %s is deprecated and cannot be called", Instance.Name)
		return
	}

	// The session id is only recognized in params._meta (the same place as progressToken).
	// The ownership check lives here rather than in NewFlow — that one also serves the internal path
	// where an agent calls a flow and shares the caller's session.
	sid := core.MetaSessionID(params)
	if sid != "" {
		// Format and visibility are checked in one go (a session belongs to a user; owner only records
		// the creator); when it does not exist the id is used to create it — the protocol endpoint has
		// no separate "create session" call, so a caller-supplied UUID means the first creation.
		// Error codes are narrowed by category: a bad client value is InvalidParams, while quota and
		// storage failures map elsewhere.
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeFlow, Instance.ID, authInfo.ID, "",
		); serr != nil {
			writeRPCError(w, reqID, sessionErrCode(serr), "session %s: %v", sid, serr)
			return
		}
		input = core.WithSessionID(input, sid) // NewFlow strips it out of the business input again
	}
	// The output language sits in the same slot and takes the same route (the reserved key is stripped by NewFlow):
	// params._meta.language is a pin, so the flow run carries it as its builtin `language` and forwards it to whatever
	// it calls next. A malformed tag counts as "not supplied" - _meta is advisory.
	if lang, _ := core.NormalizeOutputLanguage(core.OutputLanguageFromMeta(params)); lang != "" {
		input = core.WithOutputLanguage(input, lang)
	}
	// The call chain travels through the reserved key and is stripped by NewFlow (the same mechanism
	// as _call_depth), so it never becomes a business variable.
	input = callchain.WithArguments(input, chain)

	execCtx, err := executors.NewFlow(Instance, input, conv.ToString(reqID), uuid.Must(uuid.NewV7()).String(), authInfo.Schema, authInfo.ID, r.Context())
	if err != nil {
		// Initialization failed: protocol-level InternalError (MCP return error -> -32603).
		writeRPCError(w, reqID, errCodeInternalError, "init flow exec: %v", err)
		return
	}

	if !pump.Enabled() {
		executors.Start(execCtx)
		writeFlowResult(w, reqID, execCtx)
		return
	}

	// Progress subscribed: run in the background and let the main goroutine consume the progress and
	// result channels into SSE (both channels are pushed with category=text; the final result is only
	// the result inside the response).
	writeSSEHeaders(w)
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
		case chunk, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			if chunk.Message != "" {
				notifyProgress(w, pump, chunk.Category, xjson.SanitizeJSON(core.CleanProgressMarkers(chunk.Message)), nil)
			}
		case chunk, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			if chunk != "" {
				notifyProgress(w, pump, core.CategoryText, xjson.SanitizeJSON(chunk), nil)
			}
		case <-errCh:
			// The background execution ended: the business error is already mirrored into execCtx.Error
			// and is wrapped uniformly by writeFlowResult.
			break loop
		case <-r.Context().Done():
			execCtx.CloseChans()
			return
		}
	}

	writeFlowResult(w, reqID, execCtx)
}

// writeFlowResult writes the final flow result (checking execCtx.GetError).
func writeFlowResult(w http.ResponseWriter, reqID interface{}, execCtx *flow.ExecContext) {
	if e := execCtx.GetError(); e != "" {
		writeToolError(w, reqID, e)
		return
	}
	writeResult(w, reqID, execCtx.GetResult())
}

// runAgent executes an agent-typed target and writes the JSON-RPC response.
// When the client did not subscribe to progress: run synchronously and answer with a single JSON body.
// When it did: run in the background and push notifications/progress over SSE, with the final result
// still being the response carrying the id inside the stream.
// params is passed through only to read _meta (the proper place for the session id); the business input
// remains input.
func runAgent(w http.ResponseWriter, r *http.Request, authInfo *auth.AuthInfo, agentID string, params any, input map[string]interface{}, reqID interface{}, pump *core.ProgressPump, chain []string) {
	repo := agents.NewRepository()
	agentInst, err := repo.GetAgent(authInfo, agentID)
	if err != nil {
		// The instance does not exist / the query failed: the resource the argument points at is
		// missing, so MCP InvalidParams(-32602).
		writeRPCError(w, reqID, errCodeInvalidParams, "get agent instance %s: %v", agentID, err)
		return
	}

	// The session id is only recognized in params._meta (the same place as progressToken).
	sid := core.MetaSessionID(params)
	// A key of the same name in the business input is stripped: it would become the model-visible
	// {{args.session_id}}. It is no longer a session switch here, only protection against the control
	// field leaking into the prompt by mistake.
	input = core.StripSessionID(input)
	// The same protection for the language hint (supplied through _meta, see the block below).
	input = core.StripOutputLanguage(input)

	// Session ownership: when a session_id is given, the session must belong to this user (the same
	// rule as HTTP chat). owner only records the creator: a parent and child agent sharing a session
	// would necessarily disagree on owner, and closing on owner would reject every legitimate
	// call_agent cascade, so the check closes on the user; a missing session is created with that id.
	if sid != "" {
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeAgent, agentInst.ID, authInfo.ID, "",
		); serr != nil {
			writeRPCError(w, reqID, sessionErrCode(serr), "session %s: %v", sid, serr)
			return
		}
	}

	// user_id is always injected: verifyAPIKey / verifyOAuth only accept credentials bound to a user
	// (see auth.go), RPC has no tenant-level or anonymous identity — run-domain isolation, session
	// ownership and session memory all key off it.
	// session_id comes from the caller (already stripped from the business input): when given it is a
	// session call, otherwise a single turn that records no memory.
	agentArguments := map[string]interface{}{
		"args":      input,
		"user_id":   authInfo.ID,
		"_run_type": "rpc",
	}
	if sid != "" {
		agentArguments["session_id"] = sid
	}
	// The chain goes into a top-level reserved key, not inside args: args are the model-visible
	// business input, and a control field there would become {{args._call_chain}}.
	if len(chain) > 0 {
		agentArguments[callchain.ArgumentKey] = chain
	}
	// params._meta.language pins the reply language for this call. Same top-level reserved key as the chain: a
	// programmatic call whose arguments are pure JSON otherwise leaves the model no language signal at all.
	if lang, _ := core.NormalizeOutputLanguage(core.OutputLanguageFromMeta(params)); lang != "" {
		agentArguments[core.OutputLanguageKey] = lang
	}
	// Production entry point: read the released current version. Without one NewAgent reports
	// ErrNoReleasedVersion — it never falls back to the draft, otherwise "production run" would be a lie.
	agent.SetReleaseMode(agentArguments)
	inst, err := agent.NewAgent(agentInst, authInfo.Schema, agentArguments)
	if err != nil {
		// Initialization failed: protocol-level InternalError (MCP return error -> -32603).
		writeRPCError(w, reqID, errCodeInternalError, "init agent: %v", err)
		return
	}
	if inst == nil {
		// The instance is unsupported / missing: the resource the argument points at is missing, so
		// MCP InvalidParams(-32602).
		writeRPCError(w, reqID, errCodeInvalidParams, "agent %s not supported or not found", agentID)
		return
	}
	defer inst.Close()

	if !pump.Enabled() {
		// Use the request ctx: a closed connection / dropped stream cancels this run on the same ctx,
		// without waiting for it to finish its LLM turns and sandbox calls (the same rule as HTTP chat).
		if runErr := inst.Stream(r.Context()); runErr != nil {
			// Termination caused by the client disconnecting: the connection is gone, so do not write
			// another 500; the reason is already stored with this round (status / error of
			// recordSessionRound).
			if r.Context().Err() != nil {
				return
			}
			xhttpErr(w, http.StatusInternalServerError, "run agent: %v", runErr)
			return
		}
		inst.CloseChans()
		writeAgentResult(w, reqID, inst)
		return
	}

	// Progress subscribed: run in the background and let the main goroutine consume the "structured
	// event" channel into SSE. Thinking (EventThinking) and tool calls only appear here; the earlier
	// plain-text progress channel has been removed.
	writeSSEHeaders(w)
	errCh := make(chan error, 1)
	go func() {
		errCh <- inst.Stream(r.Context())
		inst.CloseChans()
	}()

	eventCh := inst.GetEventChan()
	// After the client disconnects, stop writing SSE but keep draining the event channel: the run
	// shares the request ctx and has already been cancelled by the disconnect — wait for it to wind
	// down (persistence, killing resident processes) before returning. Returning directly would let
	// defer inst.Close() run while Stream is still active, fighting over the same one-shot working directory.
	gone := r.Context().Done()

loop:
	for eventCh != nil {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			if gone == nil {
				continue // already disconnected: read only, never write
			}
			if item, ok := ev.ToProgress(); ok {
				notifyProgress(w, pump, item.Category, xjson.SanitizeJSON(item.Message), item.Meta)
			}
		case <-errCh:
			// The background execution ended: the business error is already mirrored into inst.Error
			// and is wrapped uniformly by writeAgentResult.
			break loop
		case <-gone:
			gone = nil // nil blocks that branch for good, avoiding a busy loop
		}
	}

	if r.Context().Err() != nil {
		return // the client disconnected: nobody receives the result, so no response either
	}
	writeAgentResult(w, reqID, inst)
}

// notifyProgress writes one notifications/progress frame (SSE).
//
// Only reached when the client subscribed (the pump got a token); nothing is sent when both message and
// extra are empty, which avoids pushing empty frames — and therefore avoids burning a sequence number.
// Sequence numbers are assigned monotonically by the pump (a MUST of the spec).
func notifyProgress(w http.ResponseWriter, pump *core.ProgressPump, category, message string, extra map[string]any) {
	if !pump.Enabled() {
		return
	}
	if message == "" && len(extra) == 0 {
		return
	}
	writeSSE(w, rpcProgress{
		Jsonrpc: "2.0",
		Method:  "notifications/progress",
		Params: rpcProgressMsg{
			ProgressToken: pump.Token,
			Progress:      pump.Next(),
			Message:       message,
			Meta:          withCategory(category, extra),
		},
	})
}

// withCategory merges category into _meta (without mutating the map passed in).
func withCategory(category string, extra map[string]any) map[string]any {
	meta := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		meta[k] = v
	}
	meta["category"] = category
	return meta
}

// writeAgentResult writes the final agent result (checking inst.GetError).
func writeAgentResult(w http.ResponseWriter, reqID interface{}, inst core.Agent) {
	if e := inst.GetError(); e != "" {
		writeToolError(w, reqID, e)
		return
	}
	writeResult(w, reqID, inst.GetResult())
}

// extractProgressToken reads the subscription the client expressed in params._meta.progressToken
// (the same place as MCP).
//
// nil means "not subscribed" — and then not a single progress frame may be sent: the spec requires
// notifications/progress to reference only a token supplied by an active request. A value of the wrong
// type (neither string nor integer) counts as not supplied as well.
func extractProgressToken(params interface{}) any {
	mp, ok := params.(map[string]interface{})
	if !ok {
		return nil
	}
	meta, ok := mp["_meta"].(map[string]interface{})
	if !ok {
		return nil
	}
	t := meta["progressToken"]
	if !core.ValidProgressToken(t) {
		return nil
	}
	return t
}

// writeSSEHeaders sets the SSE response headers.
func writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// writeSSE writes one message in SSE event format.
func writeSSE(w http.ResponseWriter, payload interface{}) {
	data, _ := json.Marshal(payload)
	w.Write([]byte("event: message\ndata: " + string(data) + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeResult writes the final result (SSE when streaming, a single JSON body otherwise).
func writeResult(w http.ResponseWriter, reqID interface{}, content string) {
	content = xjson.SanitizeJSON(content)
	result := rpcToolResultMsg{
		Content: []rpcToolContent{{Type: "text", Text: content}},
	}
	if isSSE(w) {
		writeSSE(w, rpcResponse{
			Jsonrpc: "2.0",
			ID:      reqID,
			Result:  result,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result:  result,
	})
}

// writeToolError writes a business error result (flagged with isError; SSE when streaming, a single
// JSON body otherwise).
func writeToolError(w http.ResponseWriter, reqID interface{}, message string) {
	message = xjson.SanitizeJSON(message)
	result := rpcToolResultMsg{
		Content: []rpcToolContent{{Type: "text", Text: message}},
		IsError: true,
	}
	if isSSE(w) {
		writeSSE(w, rpcResponse{
			Jsonrpc: "2.0",
			ID:      reqID,
			Result:  result,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result:  result,
	})
}

// isSSE reports whether the current response is streaming (SSE headers already written).
func isSSE(w http.ResponseWriter) bool {
	return w.Header().Get("Content-Type") == "text/event-stream"
}

// writeVersionError returns a structured error when the protocol version is unsupported (aligned with MCP).
func writeVersionError(w http.ResponseWriter, requested int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    protocol.ErrCodeInvalidParams,
			"message": "Unsupported protocol version",
			"data": protocol.VersionError{
				Requested: requested,
				Supported: []int{protocol.MinSupportedVersion, protocol.CurrentProtocolVersion},
			},
		},
	})
}

// sessionErrCode maps a session resolution error to a JSON-RPC error code.
//
// A missing session, a non-UUID one, or one not belonging to this entity all mean the caller passed
// bad arguments → InvalidParams; only a storage failure is InternalError. Everything used to be
// InternalError, leaving the client unable to tell "I passed something wrong" from "the server broke".
func sessionErrCode(err error) int {
	switch {
	case errors.Is(err, sessions.ErrSessionRequired),
		errors.Is(err, sessions.ErrSessionInvalid),
		errors.Is(err, sessions.ErrSessionForbidden):
		return errCodeInvalidParams
	case errors.As(err, new(*quota.ExceededError)):
		// A missing session is auto-created and hits the quota cap: an explainable business rejection,
		// not a server fault.
		return errCodeQuotaExceeded
	default:
		return errCodeInternalError
	}
}

// rpcErrorData holds the business code of a protocol error (inside error.data, leaving error.code alone).
//
// Why the business code does not go into error.code: JSON-RPC codes have standard semantics
// (-32602 / -32603 etc.) and external MCP hosts rely on them to tell "protocol error" from "business
// error"; putting a business value there makes them read it as a protocol fault. See
// nexus/docs/error-codes.md.
type rpcErrorData struct {
	Error  string            `json:"error"`
	Params map[string]string `json:"params,omitempty"`
}

// writeRPCErrorCode is writeRPCError plus the machine-readable business code and rendering parameters.
//
// The message is English like every other diagnostic string; the display copy of the end user comes
// from data.error through the frontend locale bundle, and msg is only the fallback for an unmapped code.
func writeRPCErrorCode(w http.ResponseWriter, reqID interface{}, code int, errCode string, params map[string]string, format string, args ...interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Error: &rpcError{
			Code:    code,
			Message: fmt.Sprintf(format, args...),
			Data:    rpcErrorData{Error: errCode, Params: params},
		},
	})
}

// writeRPCError writes a protocol-level error: HTTP always stays 200 and the error goes into the error
// field of the JSON-RPC response body, aligned with how an MCP server returns tool errors (in MCP a
// returned error is wrapped into a protocol error by the SDK).
// It is only for business errors "after the request has been parsed as valid JSON-RPC"; transport-level
// errors still use xhttpErr with an HTTP status code.
//
// Use writeRPCErrorCode when the frontend / client needs a translatable code.
func writeRPCError(w http.ResponseWriter, reqID interface{}, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Error: &rpcError{
			Code:    code,
			Message: msg,
		},
	})
}

// xhttpErr writes a plain JSON error (a transport-level error: HTTP status code plus a protocol body error).
func xhttpErr(w http.ResponseWriter, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
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
