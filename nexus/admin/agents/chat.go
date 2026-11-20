package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// sseKeepAlive is the SSE heartbeat interval.
//
// 15 seconds: below the 60-second idle timeout of common reverse proxies, yet not so short that a quiet stream becomes noise.
// The heartbeat doubles as a half-open connection probe: a failed write means the peer is gone (see ChatAgent).
const sseKeepAlive = 15 * time.Second

// The session record retention cap lives in the agent layer (service/agent/agent_impl.go maxSessionRecordKeep).

// chatRequest is an agent chat request.
type chatRequest struct {
	Message   string   `json:"message"`
	SessionID string   `json:"session_id"` // optional: a new session is created when empty
	Files     []string `json:"files"`      // attachments for this turn (session-workspace relative paths, from /session/upload)
	// FollowDrafts decides whether the **sub-entities this turn calls** (agent / flow) read the draft or the released version.
	// A pointer distinguishes "not sent" from an explicit false: the default is true (follow drafts, the same default as the flow debugger).
	// It only affects the ones being called; the agent of this conversation always reads its draft.
	FollowDrafts *bool `json:"follow_drafts"`
}

// ChatAgent chats with one agent over SSE and supports multi-turn session history.
// Events follow AG-UI semantics:
//
//	RUN_STARTED / TEXT_MESSAGE_CONTENT / RUN_FINISHED / RUN_ERROR
//	THINKING_DELTA (model reasoning) / TOOL_CALL_START / TOOL_CALL_END (tool calls)
func ChatAgent(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	id := c.Param("id")

	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	message := strings.TrimSpace(req.Message)
	// Attachments alone may be sent: with an empty body the attachment list says what this turn is about
	if message == "" && len(req.Files) == 0 {
		xhttp.ErrCode(c, "agent.message_required", nil, "message is required", "")
		return
	}

	instance, err := repo.GetAgent(ai, id)
	if err != nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
		return
	}

	// Session handling: create when empty, otherwise go through the shared ownership check (sessions.Lookup)
	sessRepo := sessions.NewRepository()
	sessionID := strings.TrimSpace(req.SessionID)
	// The session working directory is located by the session's owner (that is where attachments live), recorded separately:
	// the session row, not the caller identity, is the single source of truth for the directory - lookupSessionForUser has
	// already proven they are the same user, and taking the session's own value keeps pointing at the right directory if the rule is ever relaxed.
	sessUserID := ai.ID
	if sessionID == "" {
		sessionID, err = sessRepo.Create(ai.Schema, models.SessionOwnerTypeAgent, instance.ID, ai.ID, "")
		if err != nil {
			respondQuotaErr(c, err) // too many sessions -> 507
			return
		}
	} else {
		// Sessions are private: narrowed by owner (owner only records the creator; see lookupSessionForUser)
		sess, lerr := lookupSessionForUser(ai, ai.Schema, sessionID)
		if lerr != nil {
			respondSessionErr(c, lerr, notOwnedSession)
			return
		}
		// For a tenant-level session (empty user_id) the caller identity locates the directory, avoiding a users//sessions/... path
		if sess.UserID != "" {
			sessUserID = sess.UserID
		}
	}

	// The attachment list is appended only to what the model sees; the stored record (_record_input) and history replay keep the user's own text.
	// History is no longer read here: the agent layer reads session_records by session_id, sharing one
	// memory implementation with the protocol endpoints (otherwise one session entered through two doors could differ in history length, order and count).
	// "/name args": the user picked a skill command. It is resolved here so a wrong pick gets a proper error code instead of
	// a failure inside the run; the injection and the tool activation belong to the agent layer, which re-resolves the name
	// against its own visibility surface (the argument travels through the run args and can be forged).
	st, serr := getStorage()
	var commandName, commandArgs string
	if name, rest, ok := agent.ParseCommand(message); ok && serr == nil {
		mgr := agent.NewSkillManager(ai.Schema, instance.ID, instance.Name, ai.ID, st,
			instance.SkillDiscovery, instance.IsSupervisor, tools.ModeDraft)
		detail, gerr := mgr.Get(context.Background(), tools.SkillRef{Name: name})
		switch {
		case gerr != nil:
			xhttp.ErrCode(c, "common.internal", nil, "failed to read the skill: "+gerr.Error(), "")
			return
		case detail != nil && !detail.UserInvocable:
			xhttp.ErrCode(c, "skill.not_invocable", map[string]any{"name": name},
				"the skill "+name+" is not available as a user command", "")
			return
		case detail != nil:
			commandName, commandArgs = name, rest
		}
	}

	prompt := message
	var block string
	if serr == nil {
		block = attachmentPrompt(st, ai.Schema, sessUserID, sessionID, req.Files)
	}
	if block != "" {
		if prompt == "" {
			prompt = block // attachments only, no body: do not start with a blank line
		} else {
			prompt = prompt + "\n\n" + block
		}
	}

	runID := uuid.NewString()

	args := map[string]interface{}{
		"user_id":       ai.ID,
		"session_id":    sessionID,
		"run_id":        runID,
		"_user_message": prompt,  // the current input to the model (attachment list included)
		"_record_input": message, // the text stored (without the attachment list)
		"_run_type":     "call",
	}
	// The starting entity joins the chain too: the agent of this conversation is the "current executing entity",
	// so when it makes an outbound call (rpc__ / mcp__ / call_agent) the chain already contains itself -
	// a loop back is rejected at the first level instead of wasting a full turn before the depth cap stops it.
	args[callchain.ArgumentKey] = []string{callchain.Key(callchain.KindAgent, instance.ID)}
	if commandName != "" {
		agent.WithCommand(args, commandName, commandArgs)
	}
	// Admin chat is a "trial chat": the top-level agent always reads the draft, so a change can be verified at once.
	// Only production calls (MCP / RPC / endpoints / flows) read the released version - those paths use SetReleaseMode.
	agent.SetDraftMode(args)
	// This switch decides which copy sub-calls (call_agent / local__flow / run_flow) read, defaulting to drafts.
	// Turned off it uses released versions: a callee with no release reports an error instead of silently falling back to its draft.
	followDrafts := true
	if req.FollowDrafts != nil {
		followDrafts = *req.FollowDrafts
	}
	agent.SetFollowDrafts(args, followDrafts)
	inst, err := agent.NewAgent(instance, ai.Schema, args)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to init the agent: "+err.Error(), "")
		return
	}

	// SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		xhttp.ErrCode(c, "common.internal", nil, "streaming is not supported by this server", "")
		return
	}

	sendSSE(c.Writer, flusher, "RUN_STARTED", map[string]interface{}{"agent": instance.Name, "session_id": sessionID})

	// Turn duration: from RUN_STARTED to the end of the run. It is both stored (session_records.duration_ms)
	// and returned with the finish event, so the seconds seen right after a run equal the ones shown in history after a refresh.
	startedAt := time.Now()

	// Run the agent loop in the background
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		if e := inst.Stream(context.Background()); e != nil {
			inst.LogError("agent stream error: " + e.Error())
		}
		inst.CloseChans()
	}()

	// Streaming output: thinking / text / tool calls.
	// A client disconnect (page closed / request cancelled) ends the turn on purpose: otherwise the SSE connection is gone
	// while the agent runs the whole turn, with exec / browser still holding sandbox resources.
	//
	// Both disconnect signals must be caught:
	//   - reqCtx.Done: browser abort / page close, fired as soon as FIN arrives;
	//   - a failed heartbeat write: a dropped network / sleep / proxy idle timeout sends no FIN, so only a write reveals it.
	// The old implementation checked ctx while handling an event, so a disconnect during a long model thought or tool call
	// only took effect on the next event, with the sandbox spinning meanwhile. This waits in the select directly.
	clientGone := false
	signalGone := func(why string) {
		if clientGone {
			return
		}
		clientGone = true
		inst.LogError("client gone (" + why + "), ending this turn")
		agent.StopRunCause(runID, sessionID, agent.ErrClientGone)
	}

	eventCh := inst.GetEventChan()
	reqCtx := c.Request.Context()
	ticker := time.NewTicker(sseKeepAlive)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-reqCtx.Done():
			signalGone("connection closed")
			reqCtx = nil // a nil channel blocks forever, so the branch cannot fire twice
		case <-ticker.C:
			if clientGone {
				continue
			}
			// An SSE comment frame: both a keepalive (against the 60s proxy idle timeout) and a half-open connection probe.
			if _, err := fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
				signalGone("heartbeat write failed")
				continue
			}
			flusher.Flush()
		case ev, ok := <-eventCh:
			if !ok {
				break loop
			}
			if clientGone {
				continue // drain the channel only; never write to a closed connection
			}
			switch ev.Type {
			case core.EventThinking:
				if d := cleanAgentChunk(ev.Delta); d != "" {
					sendSSE(c.Writer, flusher, "THINKING_DELTA", map[string]interface{}{"delta": d})
				}
			case core.EventText:
				if d := cleanAgentChunk(ev.Delta); d != "" {
					sendSSE(c.Writer, flusher, "TEXT_MESSAGE_CONTENT", map[string]interface{}{"delta": d})
				}
			case core.EventToolCall:
				sendSSE(c.Writer, flusher, "TOOL_CALL_START", map[string]interface{}{
					"call_id": ev.CallID,
					"tool":    ev.Tool,
					"args":    ev.Args,
				})
			case core.EventToolResult:
				sendSSE(c.Writer, flusher, "TOOL_CALL_END", map[string]interface{}{
					"call_id":     ev.CallID,
					"tool":        ev.Tool,
					"result":      ev.Result,
					"ok":          ev.OK,
					"duration_ms": ev.DurationMS,
				})
			}
		}
	}

	<-runDone

	// From the same source as the stored value: the duration the agent layer froze when writing session_records, avoiding
	// "8.4s now, 8.3s in history after a refresh" (the two must match exactly).
	elapsedMs := inst.GetDurationMs()

	if e := inst.GetError(); e != "" {
		sendSSE(c.Writer, flusher, "RUN_ERROR", map[string]interface{}{"message": e, "duration_ms": elapsedMs})
	} else {
		// Turn artifacts: scan the workspace once after the run and send "which files this turn wrote" with the finish event.
		// It reports the objective fact (written), makes no judgement, and the UI renders it in the "this turn's output" area.
		sendSSE(c.Writer, flusher, "RUN_FINISHED", map[string]interface{}{
			"duration_ms": elapsedMs,
			"artifacts":   runArtifacts(ai.Schema, sessUserID, sessionID, startedAt),
		})
	}

	// The agent layer already stored this turn when Stream ended (only when a session_id exists),
	// so nothing is written here - two rows per conversation would otherwise land in session_records.

	fmt.Fprint(c.Writer, "\n")
	flusher.Flush()
}

// Reading and writing session records all happen in the agent layer (service/agent/agent_impl.go):
// reading history, storing the turn and refreshing the title live there; an entry point only creates / resolves the session and injects _record_input.
// That way agents called from web chat, RPC, MCP, call_agent and flow nodes share one memory contract.

// sendSSE sends one event with standard SSE framing.
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte("{}")
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}

// stopRequest locates the run to stop:
//   - session_id: required. It anchors "which runs to stop" and is the only basis for the ownership check -
//     without it there is no way to tell whether the run being stopped belongs to this agent;
//   - run_id: optional. Given, exactly that run stops; omitted, every run of the session stops (the UI only knows the session).
//
// "run_id alone" is not accepted: the run ledger is process-wide and spans agents, so a bare run_id could stop
// someone else's run; and RUN_STARTED never returns a run_id, so a normal front end does not have one -
// that path should never have been open (the docs said session_id was required, while the code accepted either).
type stopRequest struct {
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
}

// StopAgentRun stops a running agent.
//
// Idempotent: stopped:0 when nothing runs, not an error (calling twice is harmless).
// The turn is still stored after a stop (empty output, duration_ms recorded), and the SSE side receives
// RUN_ERROR "run stopped (user request)".
//
// The stop button on the chat page does **not** come through here: it aborts the SSE connection itself (the backend
// ends the run with ErrClientGone). The reason: the ledger is in-process, and with several instances this HTTP request may be
// load-balanced to an instance that is not running this turn and only get stopped:0. This API is for the admin side /
// operations / entry points with no connection to abort (RPC, MCP, scripts).
func StopAgentRun(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	// GetAgent is still required: confirm the agent is visible in this tenant before talking about stopping
	if _, err := repo.GetAgent(ai, c.Param("id")); err != nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
		return
	}

	var req stopRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	if req.SessionID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "session_id is required", "")
		return
	}

	// Ownership: the same rule as ChatAgent (narrowed by the session owner).
	// Checking the user is enough - stop cancels every run of the session, so a cross-user call would interrupt someone else.
	if _, lerr := lookupSessionForUser(ai, ai.Schema, req.SessionID); lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return
	}

	stopped := 0
	if req.RunID != "" {
		// session_id is passed along: the ledger compares ownership, preventing a cross-session stop.
		if agent.StopRunCause(req.RunID, req.SessionID, agent.ErrRunStopped) {
			stopped = 1
		}
	} else {
		stopped = agent.StopSession(req.SessionID)
	}

	xhttp.OkJSON(c, "Success", gin.H{"stopped": stopped}, "")
}

// cleanAgentChunk strips internal markers from agent output, keeping displayable text only.
func cleanAgentChunk(s string) string {
	r := strings.NewReplacer(
		"[[action]]", "",
		"[[think]]", "",
		"[[tools]]", "",
		"[[content]]", "",
		"<think>", "",
		"</think>", "",
	)
	return r.Replace(s)
}
