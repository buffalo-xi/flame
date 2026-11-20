package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/flow/executors"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	flows "flame/nexus/shared/repository/flows"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// upgrader upgrades an HTTP GET to WebSocket on the same port as the nexus HTTP service.
var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// Hub is a minimal broadcast centre that keeps track of the WS connection set (it can later be extended
// to push per channel).
type Hub struct {
	clients    map[*websocket.Conn]bool
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	broadcast  chan []byte
}

// NewHub creates a Hub instance.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
		broadcast:  make(chan []byte),
	}
}

// Run starts the Hub event loop (call it in its own goroutine).
func (h *Hub) Run() {
	for {
		select {
		case conn := <-h.register:
			h.clients[conn] = true
		case conn := <-h.unregister:
			if _, ok := h.clients[conn]; ok {
				delete(h.clients, conn)
				conn.Close()
			}
		case message := <-h.broadcast:
			for conn := range h.clients {
				if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
					log.Printf("[ws/hub] broadcast write error: %v", err)
					conn.Close()
					delete(h.clients, conn)
				}
			}
		}
	}
}

// getAuth returns the identity AuthMiddleware validated and wrote into the context.
func getAuth(c *gin.Context) *auth.AuthInfo {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok := v.(*auth.AuthInfo); ok {
			return info
		}
	}
	return &auth.AuthInfo{}
}

// DebugEvent is one debug output frame.
type DebugEvent map[string]interface{}

// ControlRequest is a control command the client sends as a message (used for single-step debugging).
type ControlRequest struct {
	Action     string      `json:"action"`     // run/pause/step/stop/setBreakpoint/inspect/inspect_node
	StepKind   string      `json:"step_kind"`  // meaningful for step: over/in/out
	Breakpoint string      `json:"breakpoint"` // used by setBreakpoint (nodeID)
	On         bool        `json:"on"`         // used by setBreakpoint: true=set false=clear (default false)
	Input      interface{} `json:"input"`      // the flow input passed on run
	Key        string      `json:"key"`        // used by inspect (variable name, empty means all)
	NodeID     string      `json:"node_id"`    // used by inspect_node (the node whose data to show)
	// FollowDrafts is used by run: whether the child flows started by this debug session follow their
	// draft or the released version. A pointer distinguishes "the frontend sent nothing", which defaults
	// to true (consistent with the existing debug semantics).
	FollowDrafts *bool `json:"follow_drafts"`
}

// DebugSource is the content source every debug endpoint (flow/agent) must implement.
// Part A: Attach injects the connection and write lock after the upgrade; OnConnect is called once the
// identity and object id are known and may validate permissions and bind a data source.
// Part B: OnMessage handles the single-step control commands the client sends and returns the debug
// frame to push back.
type DebugSource interface {
	// Kind returns the debug kind, such as "flow" / "agent".
	Kind() string
	// Attach injects the underlying WebSocket connection, the shared write lock and the connection-level
	// context, so the source can push frames such as a suspension snapshot on its own.
	// writeMu is created and owned by debugConn: every write to conn (by debugConn itself and by the
	// source's event callbacks) must take writeMu first, satisfying the gorilla/websocket single-writer rule.
	// connCtx is cancelled when the connection drops; the source should use it as the parent context of
	// its execution goroutine.
	Attach(conn *websocket.Conn, writeMu *sync.Mutex, connCtx context.Context)
	// WriteMu returns the shared write lock, so debugConn can serialize its response writes against the
	// source's event callbacks.
	WriteMu() *sync.Mutex
	// OnConnect binds the object and authenticates. A returned error means no permission / the object
	// does not exist, and the connection is rejected.
	OnConnect(info *auth.AuthInfo, objectID string) error
	// OnMessage handles a single-step control command and returns the debug frame; close == true closes
	// the connection.
	OnMessage(info *auth.AuthInfo, objectID string, ctrl ControlRequest) (DebugEvent, bool)
}

// DebugHandler is a thin wrapper: it takes the object id from the path and enters the generic connection
// loop (authentication happens with the first message).
// srcFactory creates a new DebugSource instance per connection, keeping connection-level state (the
// execution engine, conn) isolated.
func DebugHandler(srcFactory func() DebugSource, paramName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		objectID := c.Param(paramName)
		debugConn(c, srcFactory(), objectID)
	}
}

// debugConn is the generic connection lifecycle: upgrade → authenticate with the first message → bind the
// object → welcome frame → heartbeat → read loop.
// Authentication happens with the first message (a browser WebSocket cannot set handshake headers): the
// client's first message after connecting must be {"token":"xxx"} or {"auth":"Bearer xxx"}, and only then
// does the debug session start.
// Everything that differs (debug content / single-step control) is delegated to DebugSource.
func debugConn(c *gin.Context, src DebugSource, objectID string) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[ws/debug/%s] upgrade failed: %v", src.Kind(), err)
		return
	}
	defer conn.Close()

	// Shared write lock: every conn.WriteMessage must hold it, satisfying the gorilla/websocket
	// single-writer rule. It is created by debugConn and handed to DebugSource through Attach, so both
	// sides share it.
	var writeMu sync.Mutex

	// Connection-level context: cancelled on disconnect so the execution goroutine cannot leak.
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()

	// safeWrite takes the lock and writes one text frame; used by debugConn itself (it shares writeMu
	// with the source).
	safeWrite := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	// Inject the connection, the write lock and the connection-level context, so the source can push
	// frames such as a suspension snapshot on its own.
	src.Attach(conn, &writeMu, connCtx)

	// First-message authentication: read the first text message within a deadline.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Printf("[ws/debug/%s] read auth message failed: %v", src.Kind(), err)
		return
	}
	var authMsg struct {
		Token string `json:"token"`
		Auth  string `json:"auth"`
	}
	if err := json.Unmarshal(msg, &authMsg); err != nil {
		_ = safeWrite(mustJSON(DebugEvent{
			"type":  "error",
			"error": "invalid auth message, expect {\"token\":\"...\"}",
		}))
		return
	}
	tokenStr := strings.TrimSpace(authMsg.Token)
	if tokenStr == "" {
		tokenStr = strings.TrimSpace(strings.TrimPrefix(authMsg.Auth, "Bearer "))
	}
	info, err := auth.ValidateToken(tokenStr)
	if err != nil {
		_ = safeWrite(mustJSON(DebugEvent{
			"type":  "error",
			"error": "unauthorized: " + err.Error(),
		}))
		return
	}

	// Part A: bind the object and check permissions.
	if err := src.OnConnect(info, objectID); err != nil {
		_ = safeWrite(mustJSON(DebugEvent{
			"type":  "error",
			"error": err.Error(),
		}))
		return
	}

	// Welcome frame.
	welcome := map[string]interface{}{
		"type":      "welcome",
		"kind":      src.Kind(),
		"object_id": objectID,
		"user":      info.Name,
		"schema":    info.Schema,
		"at":        time.Now().Format(time.RFC3339),
		"hint":      "send {\"action\":\"step\"} etc. to drive debugging",
	}
	if data, err := json.Marshal(welcome); err == nil {
		_ = safeWrite(data)
	}

	// ready frame: pushed once authentication and OnConnect succeeded, telling the frontend the channel
	// is usable (control commands may be sent).
	if data, err := json.Marshal(DebugEvent{
		"type":      "debug",
		"kind":      src.Kind(),
		"event":     "ready",
		"object_id": objectID,
		"user":      info.Name,
	}); err == nil {
		_ = safeWrite(data)
	}

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Heartbeat plus an exit signal (the ping goroutine exits reliably through the done channel, so it
	// cannot leak).
	pingTicker := time.NewTicker(30 * time.Second)
	pingDone := make(chan struct{})
	defer func() {
		pingTicker.Stop()
		close(pingDone)
	}()
	go func() {
		for {
			select {
			case <-pingDone:
				return
			case <-pingTicker.C:
				writeMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws/debug/%s] read error: %v", src.Kind(), err)
			}
			break
		}

		// Part B: parse the client control command (a non-text / non-JSON frame is echoed back as is, which
		// keeps raw debugging easy).
		var ctrl ControlRequest
		if mt == websocket.TextMessage {
			if err := json.Unmarshal(message, &ctrl); err != nil {
				_ = safeWrite(mustJSON(DebugEvent{
					"type":     "echo",
					"kind":     src.Kind(),
					"user":     info.Name,
					"msg_type": msgTypeName(mt),
					"bytes":    len(message),
					"text":     string(message),
				}))
				continue
			}
		}

		ev, closeConn := src.OnMessage(info, objectID, ctrl)
		if ev == nil {
			continue
		}
		if data, err := json.Marshal(ev); err == nil {
			if writeErr := safeWrite(data); writeErr != nil {
				log.Printf("[ws/debug/%s] write error: %v", src.Kind(), writeErr)
				return
			}
		}
		if closeConn {
			return
		}
	}
	// The read loop exited → the deferred connCancel() cancels the execution context → the execution
	// goroutine notices and exits.
}

func mustJSON(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}

func msgTypeName(mt int) string {
	switch mt {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return fmt.Sprintf("unknown(%d)", mt)
	}
}

// ---------- the /debug/flow debug source (wired to the flow engine) ----------

// FlowDebugSource creates a flow debug source instance (one per connection, holding connection-level state).
func FlowDebugSource() DebugSource { return &flowDebugSource{} }

// flowDebugSource holds the execution engine and debug controller of this WS connection.
// Concurrency: after Attach/OnConnect the fields may be accessed by two goroutines at once
//   - the read-loop goroutine (OnMessage)
//   - the execution goroutine (pushSnapshot / pushFinished)
//
// Writes are serialized through writeMu (shared with debugConn); reads are guarded by mu.
type flowDebugSource struct {
	mu       sync.Mutex      // guards started, execCtx and the other shared fields
	writeMu  *sync.Mutex     // the WebSocket write lock shared with debugConn
	connCtx  context.Context // connection-level context, cancelled on disconnect
	conn     *websocket.Conn // written only in Attach, read-only afterwards
	objectID string          // written only in OnConnect, read-only afterwards
	authInfo *auth.AuthInfo  // written in OnConnect; run needs the schema to build execCtx
	execCtx  *flow.ExecContext
	ctrl     *flow.DebugControl
	started  bool
	// nodeResults keeps the data of the last execution per node_id (guarded by mu).
	// Both clicking a node during a run (inspect_node) and the end of the run (pushFinished) read from it.
	nodeResults map[string]*flow.NodeResult
	streamWG    sync.WaitGroup // wait for pumpStream to flush the remaining stream messages before closing
}

func (s *flowDebugSource) Kind() string { return "flow" }

func (s *flowDebugSource) Attach(conn *websocket.Conn, writeMu *sync.Mutex, connCtx context.Context) {
	s.conn = conn
	s.writeMu = writeMu
	s.connCtx = connCtx
	// On disconnect (connCtx cancelled): wake every suspension point so a suspended node notices the
	// cancellation and winds down. Otherwise it would block forever on cond.Wait (leaking the execution
	// goroutine and never persisting the cancelled status).
	go func() {
		<-connCtx.Done()
		s.mu.Lock()
		if s.ctrl != nil {
			s.ctrl.Stop()
		}
		s.mu.Unlock()
	}()
}

func (s *flowDebugSource) WriteMu() *sync.Mutex { return s.writeMu }

// writeJSON takes the lock and writes one JSON text frame (safe to call from event callbacks).
func (s *flowDebugSource) writeJSON(v interface{}) {
	if s.conn == nil || s.writeMu == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *flowDebugSource) OnConnect(info *auth.AuthInfo, objectID string) error {
	s.objectID = objectID
	// Check that the flow exists and belongs to this schema.
	repo := flows.NewRepository()
	if _, err := repo.GetFlow(info, objectID); err != nil {
		return fmt.Errorf("flow instance %s does not exist or access is denied: %w", objectID, err)
	}
	// Keep the identity (run needs the schema to build execCtx).
	s.authInfo = info
	// The debug controller is created on connect (carrying the breakpoints set before run), but execCtx is
	// neither built nor started here. It is deferred until the run command arrives (see buildExecCtx), so
	// that the latest diagram saved right before the run is picked up and every run gets a fresh runID.
	ctrl := flow.NewDebugControl()
	ctrl.Enabled = true
	// Note: on connect the default DebugRun is kept (no suspension). The mode switches only on user action:
	// clicking run runs at full speed; pause / single step switch to pause/step, which is what suspends
	// before a node.
	ctrl.SetEventHook(func(snap *flow.DebugSnapshot) {
		s.pushSnapshot(snap)
	})
	// Mark nodes as "already run" when they start: node_executing is pushed whether or not the run
	// suspends, so the frontend can highlight every executed node (even a full-speed run without
	// breakpoints shows the execution trace).
	ctrl.SetExecHook(func(nodeID string, nodeName string) {
		s.pushNodeExecuting(nodeID, nodeName)
	})
	ctrl.SetFailHook(func(nodeID string, nodeName string, errMsg string) {
		s.pushNodeFailed(nodeID, nodeName, errMsg)
	})
	ctrl.SetSkipHook(func(nodeID string, nodeName string) {
		s.pushNodeSkipped(nodeID, nodeName)
	})
	// Accumulate the latest data when a node finishes (de-duplicated by node_id, keeping the latest
	// StartTime — the same rule as persistence).
	ctrl.SetDoneHook(func(nr *flow.NodeResult) {
		s.mu.Lock()
		if s.nodeResults == nil {
			s.nodeResults = make(map[string]*flow.NodeResult)
		}
		if prev, ok := s.nodeResults[nr.NodeID]; !ok ||
			nr.StartTime.After(prev.StartTime) ||
			(nr.StartTime.Equal(prev.StartTime) && nr.ID > prev.ID) {
			s.nodeResults[nr.NodeID] = nr
		}
		s.mu.Unlock()
	})
	s.ctrl = ctrl
	s.started = false
	return nil
}

// buildExecCtx builds the execution context (with a fresh runID) when the run command arrives.
// It runs at run time rather than at OnConnect, so the diagram saved right before the run is picked up.
func (s *flowDebugSource) buildExecCtx(input map[string]interface{}, runID string, followDrafts bool) (*flow.ExecContext, error) {
	if s.authInfo == nil {
		return nil, fmt.Errorf("auth info is missing")
	}
	repo := flows.NewRepository()
	pi, err := repo.GetFlow(s.authInfo, s.objectID)
	if err != nil {
		return nil, fmt.Errorf("flow instance %s does not exist or access is denied: %w", s.objectID, err)
	}
	// The flow being debugged is the starting point of this call and goes on the chain too (see
	// shared/callchain): when it loops back to itself through an outbound step (rpc / mcp / agent node),
	// the loop is rejected at the first level.
	// WithArguments returns a new map and leaves the caller's input untouched.
	input = callchain.WithArguments(input,
		[]string{callchain.Key(callchain.KindFlow, s.objectID)})

	execCtx, err := executors.NewFlow(pi, input, "debug-"+s.objectID, runID, s.authInfo.Schema, s.authInfo.ID, s.connCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to build the execution context: %w", err)
	}
	execCtx.Debug = s.ctrl
	// Scope of the debug session: followDrafts decides whether the child flows it calls read their draft or
	// the released version, and every entity followed is recorded.
	// The main flow itself always reads the working copy (GetFlow); this switch only governs "the ones it
	// calls". Production entry points (MCP/RPC/local__) never set the field, so there is no path where
	// "forgot to pass it once" ships a draft.
	execCtx.DebugScope = flow.NewDebugScope(followDrafts)
	// Once the flow ends (success, failure or cancellation):
	// 1) close the progress/result channels so pumpStream exits;
	// 2) push the finished event.
	execCtx.OnFlowDone = func(flowErr error) {
		execCtx.CloseChans()
		s.pushFinished(flowErr)
	}
	return execCtx, nil
}

func (s *flowDebugSource) pushSnapshot(snap *flow.DebugSnapshot) {
	s.writeJSON(DebugEvent{
		"type":      "debug",
		"kind":      "flow",
		"event":     "suspended",
		"object_id": s.objectID,
		"node_id":   snap.NodeID,
		"node_type": snap.NodeType,
		"process":   snap.Process,
		"depth":     snap.Depth,
		"stack":     snap.Stack,
	})
}

// pushNodeExecuting is pushed when a node starts, marking it as "already run" (whether or not it suspends).
func (s *flowDebugSource) pushNodeExecuting(nodeID string, nodeName string) {
	s.writeJSON(DebugEvent{
		"type":      "debug",
		"kind":      "flow",
		"event":     "node_executing",
		"object_id": s.objectID,
		"node_id":   nodeID,
		"node_name": nodeName,
		"at":        time.Now().Format(time.RFC3339),
	})
}

// pushNodeFailed is pushed when a node fails, marking it as "failed".
func (s *flowDebugSource) pushNodeFailed(nodeID string, nodeName string, errMsg string) {
	s.writeJSON(DebugEvent{
		"type":      "debug",
		"kind":      "flow",
		"event":     "node_failed",
		"object_id": s.objectID,
		"node_id":   nodeID,
		"node_name": nodeName,
		"error":     errMsg,
		"at":        time.Now().Format(time.RFC3339),
	})
}

// pushNodeSkipped is pushed when a node is skipped, marking it as "skipped".
func (s *flowDebugSource) pushNodeSkipped(nodeID string, nodeName string) {
	s.writeJSON(DebugEvent{
		"type":      "debug",
		"kind":      "flow",
		"event":     "node_skipped",
		"object_id": s.objectID,
		"node_id":   nodeID,
		"node_name": nodeName,
		"at":        time.Now().Format(time.RFC3339),
	})
}

// pumpStream consumes the progress/result channels of the execution context and streams them to the
// frontend. Progress (ProgressChan) becomes a progress event; results (ResultChan) become a result_stream
// event. It exits once the channels are closed, and also when the connection drops (connCtx.Done).
func (s *flowDebugSource) pumpStream(ec *flow.ExecContext) {
	defer s.streamWG.Done()
	progressCh := ec.GetProgressChan()
	resultCh := ec.GetResultChan()
	for progressCh != nil || resultCh != nil {
		select {
		case chunk, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			if chunk.Message != "" {
				s.writeJSON(DebugEvent{
					"type": "debug", "kind": "flow", "event": "progress",
					"object_id": s.objectID, "message": chunk.Message,
					"category": chunk.Category,
					"at":       time.Now().Format(time.RFC3339),
				})
			}
		case chunk, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			if chunk != "" {
				s.writeJSON(DebugEvent{
					"type": "debug", "kind": "flow", "event": "result_stream",
					"object_id": s.objectID, "message": chunk,
					"at": time.Now().Format(time.RFC3339),
				})
			}
		case <-s.connCtx.Done():
			return
		}
	}
}

// pushFinished pushes the final event once the flow ended (called on success, failure and cancellation).
func (s *flowDebugSource) pushFinished(flowErr error) {
	errMsg := ""
	if flowErr != nil {
		errMsg = flowErr.Error()
	}
	// The final result of the flow (execCtx.Result, accumulated / set as a string).
	resultStr := ""
	runID := ""
	s.mu.Lock()
	ec := s.execCtx
	if ec != nil {
		runID = ec.GetRunID()
	}
	// Take the node data accumulated by this run (de-duplicated by node_id, keeping the last execution).
	snapshots := make([]*flow.NodeResult, 0, len(s.nodeResults))
	for _, nr := range s.nodeResults {
		snapshots = append(snapshots, nr)
	}
	// Reset started: this run is over, so the next run takes the first-start branch again (rebuild execCtx
	// with a new runID).
	s.started = false
	s.execCtx = nil
	s.mu.Unlock()
	if ec != nil {
		resultStr = ec.GetResult()
	}
	// Wait for pumpStream to flush the remaining progress/result messages, so the frontend receives the
	// streamed messages before finished.
	s.streamWG.Wait()

	// Push the node snapshots one by one (avoiding a single oversized frame; one node per frame).
	for _, nr := range snapshots {
		s.writeJSON(DebugEvent{
			"type":      "debug",
			"kind":      "flow",
			"event":     "node_snapshot",
			"object_id": s.objectID,
			"node":      nr.BuildSnapshot(),
		})
	}

	// The finished frame only carries metadata, not the nodes array (node data was pushed one by one
	// through node_snapshot). It also carries the complete run object (same fields as flow_run_records),
	// so the frontend can reuse it for display and history queries alike.
	finished := DebugEvent{
		"type":       "debug",
		"kind":       "flow",
		"event":      "finished",
		"object_id":  s.objectID,
		"error":      errMsg,
		"result":     resultStr,
		"run_id":     runID,
		"node_count": len(snapshots),
		"at":         time.Now().Format(time.RFC3339),
	}
	if ec != nil {
		run := executors.BuildRunRecord(ec, flowErr)
		finished["run"] = map[string]any{
			"id":                      run.ID,
			"flow_id":                 run.FlowID,
			"status":                  run.Status,
			"run_type":                run.RunType,
			"result":                  run.Result,
			"err_msg":                 run.ErrMsg,
			"node_count":              run.NodeCount,
			"duration_ms":             run.DurationMs,
			"started_at":              run.StartedAt,
			"finished_at":             run.FinishedAt,
			"run_args":                json.RawMessage(run.RunArgs),
			"run_limits":              json.RawMessage(run.RunLimits),
			"run_flow_vars":           json.RawMessage(run.RunFlowVars),
			"run_flow_vars_truncated": run.RunFlowVarsTruncated,
			"run_flow_vars_size":      run.RunFlowVarsSize,
			"progress":                json.RawMessage(run.Progress),
			"progress_truncated":      run.ProgressTruncated,
			"progress_size":           run.ProgressSize,
		}
	}
	s.writeJSON(finished)

	// Close the debug channel proactively once the run ended (success, failure or cancellation).
	// The close is done under the write lock so it cannot race an in-flight write (gorilla/websocket
	// single-writer rule).
	if s.writeMu != nil && s.conn != nil {
		s.writeMu.Lock()
		_ = s.conn.Close()
		s.writeMu.Unlock()
	}
}

func (s *flowDebugSource) OnMessage(info *auth.AuthInfo, objectID string, ctrlMsg ControlRequest) (DebugEvent, bool) {
	switch ctrlMsg.Action {
	case "run":
		// Full-speed run: without breakpoints it runs to the end; with breakpoints it suspends on a hit.
		// Execution only starts on the first run command (until then OnConnect keeps DebugRun, so nothing
		// suspends for no reason).
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()

		if !started {
			// A brand-new run: clear the node data accumulated by the previous one.
			s.mu.Lock()
			s.nodeResults = make(map[string]*flow.NodeResult)
			s.mu.Unlock()
			// First run: build the execution context (picking up the diagram saved right before the run)
			// and generate a fresh runID.
			input, _ := ctrlMsg.Input.(map[string]interface{})
			runID := uuid.Must(uuid.NewV7()).String()
			// Only a brand-new run decides the scope: flipping the switch after it started must not affect
			// this run.
			followDrafts := true
			if ctrlMsg.FollowDrafts != nil {
				followDrafts = *ctrlMsg.FollowDrafts
			}
			ec, err := s.buildExecCtx(input, runID, followDrafts)
			if err != nil {
				return DebugEvent{
					"type": "debug", "kind": "flow", "event": "error",
					"object_id": objectID, "error": err.Error(),
				}, false
			}
			s.mu.Lock()
			s.execCtx = ec
			s.started = true
			s.mu.Unlock()

			// Push the run-level information as soon as the run starts (arguments / limits / start time),
			// so that clicking the start node while suspended on a breakpoint shows this run's parameters
			// (without waiting for finished).
			s.writeJSON(DebugEvent{
				"type":       "debug",
				"kind":       "flow",
				"event":      "run_started",
				"object_id":  objectID,
				"run_id":     runID,
				"started_at": time.Now().Format(time.RFC3339),
				"run_args":   ec.RunArguments,
				"run_limits": ec.ResolveRunLimits(),
			})

			s.ctrl.Control(flow.DebugRun, "")
			go executors.Start(ec)
			// Consume the progress/result channels and stream them to the frontend (which also keeps
			// SendProgress from blocking once the buffer fills).
			s.streamWG.Add(1)
			go s.pumpStream(ec)
		} else {
			// Already started: run means "continue at full speed", releasing any suspension.
			s.ctrl.Control(flow.DebugRun, "")
			s.ctrl.Resume()
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "resumed",
			"object_id": objectID, "mode": "run", "at": time.Now().Format(time.RFC3339),
		}, false

	case "pause":
		// Pause before the next node.
		s.ctrl.Control(flow.DebugPause, "")
		s.ctrl.Resume()
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "control",
			"object_id": objectID, "mode": "pause", "at": time.Now().Format(time.RFC3339),
		}, false

	case "step":
		// Single step: over/in/out.
		kind := flow.DebugStepKind(ctrlMsg.StepKind)
		if kind != flow.StepOver && kind != flow.StepIn && kind != flow.StepOut {
			kind = flow.StepOver
		}
		s.ctrl.Control(flow.DebugStep, kind)
		s.ctrl.Resume()
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "control",
			"object_id": objectID, "mode": "step", "step_kind": string(kind),
			"at": time.Now().Format(time.RFC3339),
		}, false

	case "stop":
		if s.ctrl != nil {
			s.ctrl.Stop()
		}
		s.mu.Lock()
		ec := s.execCtx
		s.mu.Unlock()
		if ec != nil {
			ec.Cancel()
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "stopped",
			"object_id": objectID, "at": time.Now().Format(time.RFC3339),
		}, true

	case "setBreakpoint":
		// on defaults to false (clear); the frontend sends on:true to set a breakpoint.
		on := ctrlMsg.On
		if s.ctrl != nil && ctrlMsg.Breakpoint != "" {
			s.ctrl.SetBreakpoint(ctrlMsg.Breakpoint, on)
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "breakpoint",
			"object_id": objectID, "node_id": ctrlMsg.Breakpoint, "on": on,
		}, false

	case "inspect":
		s.mu.Lock()
		ec := s.execCtx
		s.mu.Unlock()
		if ec == nil {
			return DebugEvent{
				"type": "debug", "kind": "flow", "event": "inspect",
				"object_id": objectID, "error": "exec context not initialized",
			}, false
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "inspect",
			"object_id": objectID,
			"run_args":  ec.RunArguments,
			"flow_vars": ec.RunFlowVars,
		}, false

	case "inspect_node":
		// Clicking a node during a run: take the single snapshot from the accumulated latest node data.
		s.mu.Lock()
		var nr *flow.NodeResult
		if s.nodeResults != nil {
			nr = s.nodeResults[ctrlMsg.NodeID]
		}
		s.mu.Unlock()
		if nr == nil {
			return DebugEvent{
				"type": "debug", "kind": "flow", "event": "node_inspect",
				"object_id": objectID, "node_id": ctrlMsg.NodeID,
				"error": "node not executed yet",
			}, false
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "node_inspect",
			"object_id": objectID, "node": nr.BuildSnapshot(),
		}, false

	default:
		return DebugEvent{
			"type": "debug", "kind": "flow", "object_id": objectID,
			"error": "unknown action: " + ctrlMsg.Action,
		}, false
	}
}

// ---------- the /debug/agent debug source (skeleton) ----------

// AgentDebugSource creates an agent debug source instance (one per connection).
func AgentDebugSource() DebugSource { return &agentDebugSource{} }

type agentDebugSource struct {
	conn    *websocket.Conn
	writeMu *sync.Mutex
}

func (s *agentDebugSource) Kind() string { return "agent" }

func (s *agentDebugSource) Attach(conn *websocket.Conn, writeMu *sync.Mutex, _ context.Context) {
	s.conn = conn
	s.writeMu = writeMu
}

func (s *agentDebugSource) WriteMu() *sync.Mutex { return s.writeMu }

func (s *agentDebugSource) OnConnect(info *auth.AuthInfo, objectID string) error {
	// TODO: check that objectID belongs to info.Schema and bind a service/agent/core session.
	return nil
}

func (s *agentDebugSource) OnMessage(info *auth.AuthInfo, objectID string, ctrl ControlRequest) (DebugEvent, bool) {
	// The agent debug source is not implemented yet: return an explicit not-implemented frame instead of
	// placeholder data.
	return DebugEvent{
		"type":      "debug",
		"kind":      "agent",
		"object_id": objectID,
		"event":     "not_implemented",
		"error":     "agent debugging is not yet implemented",
		"action":    ctrl.Action,
		"at":        time.Now().Format(time.RFC3339),
	}, false
}

// ---------- the /ws business skeleton (same authentication as /debug) ----------

// EchoHandler is the generic WebSocket entry skeleton: echo plus heartbeat.
func EchoHandler(hub *Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := getAuth(c) // the identity is available for per-tenant isolation when needed
		_ = info

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[ws] upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		if hub != nil {
			hub.register <- conn
			defer func() { hub.unregister <- conn }()
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		pingTicker := time.NewTicker(30 * time.Second)
		pingDone := make(chan struct{})
		defer func() {
			pingTicker.Stop()
			close(pingDone)
		}()
		go func() {
			for {
				select {
				case <-pingDone:
					return
				case <-pingTicker.C:
					if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			}
		}()

		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("[ws] read error: %v", err)
				}
				break
			}
			switch mt {
			case websocket.TextMessage, websocket.BinaryMessage:
				if err := conn.WriteMessage(mt, message); err != nil {
					log.Printf("[ws] write error: %v", err)
					return
				}
			case websocket.CloseMessage:
				return
			}
		}
	}
}
