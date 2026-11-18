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

// upgrader 将 HTTP GET 升级为 WebSocket，与 nexus HTTP 服务共用同一端口。
var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// Hub 极简广播中心，演示维护 WS 连接集合（后续可扩展为按 channel 分组推送）。
type Hub struct {
	clients    map[*websocket.Conn]bool
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	broadcast  chan []byte
}

// NewHub 创建一个 Hub 实例。
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
		broadcast:  make(chan []byte),
	}
}

// Run 启动 Hub 的事件循环（在单独 goroutine 中调用）。
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

// getAuth 取出 AuthMiddleware 已校验并写入 context 的登录信息。
func getAuth(c *gin.Context) *auth.AuthInfo {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok := v.(*auth.AuthInfo); ok {
			return info
		}
	}
	return &auth.AuthInfo{}
}

// DebugEvent 是一帧调试输出。
type DebugEvent map[string]interface{}

// ControlRequest 是客户端通过消息发送的控制指令（单步调试用）。
type ControlRequest struct {
	Action     string      `json:"action"`     // run/pause/step/stop/setBreakpoint/inspect/inspect_node
	StepKind   string      `json:"step_kind"`  // step 时有效：over/in/out
	Breakpoint string      `json:"breakpoint"` // setBreakpoint 时使用（nodeID）
	On         bool        `json:"on"`         // setBreakpoint 时使用：true=设 false=取消（默认 false）
	Input      interface{} `json:"input"`      // run 时传入工作流入参
	Key        string      `json:"key"`        // inspect 时使用（变量名，空表示全部）
	NodeID     string      `json:"node_id"`    // inspect_node 时使用（要查看数据的节点 id）
	// FollowDrafts run 时使用：本次调试调起的子流程是跟草稿还是走正式版本。
	// 用指针是为了区分「前端没传」，缺省按 true（与既有调试语义一致）。
	FollowDrafts *bool `json:"follow_drafts"`
}

// DebugSource 各调试端点（flow/agent）需要实现的内容源。
// A 部分：Attach 在升级后注入连接与写锁；OnConnect 在拿到身份与对象 id 时调用，可做权限校验与数据源绑定。
// B 部分：OnMessage 处理客户端发来的单步控制指令，返回要回推的调试帧。
type DebugSource interface {
	// Kind 返回调试类型标识，如 "flow" / "agent"
	Kind() string
	// Attach 注入底层 WebSocket 连接、共享写锁及连接级 context，供源主动推送挂起快照等帧。
	// writeMu 由 debugConn 创建并持有，所有写入 conn 的操作（包括 debugConn 自身
	// 和源的事件回调）都须先获取 writeMu，以满足 gorilla/websocket「单写者」约束。
	// connCtx 在连接断开时被 cancel，源应将其作为执行 goroutine 的父 context。
	Attach(conn *websocket.Conn, writeMu *sync.Mutex, connCtx context.Context)
	// WriteMu 返回共享写锁，供 debugConn 在写响应时与源的事件回调互斥。
	WriteMu() *sync.Mutex
	// OnConnect 绑定对象 + 鉴权。返回 error 表示无权限/对象不存在，连接将被拒。
	OnConnect(info *auth.AuthInfo, objectID string) error
	// OnMessage 处理单步控制指令，返回调试帧；close 为 true 时关闭连接。
	OnMessage(info *auth.AuthInfo, objectID string, ctrl ControlRequest) (DebugEvent, bool)
}

// DebugHandler 薄包装：取路径上的对象 id，进入通用连接循环（鉴权在首条消息完成）。
// srcFactory 每次连接创建一个新 DebugSource 实例，使连接级状态（如执行引擎、conn）彼此隔离。
func DebugHandler(srcFactory func() DebugSource, paramName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		objectID := c.Param(paramName)
		debugConn(c, srcFactory(), objectID)
	}
}

// debugConn 通用连接生命周期：升级 → 首条消息鉴权 → 绑定对象 → 欢迎帧 → 心跳 → 读循环。
// 鉴权采用首条消息验证（浏览器 WebSocket 无法自定义握手头），即连接建立后客户端第一条
// 消息须为 {"token":"xxx"} 或 {"auth":"Bearer xxx"}，校验通过才进入调试会话。
// 差异（调试内容/单步控制）全部委托给 DebugSource。
func debugConn(c *gin.Context, src DebugSource, objectID string) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[ws/debug/%s] upgrade failed: %v", src.Kind(), err)
		return
	}
	defer conn.Close()

	// 共享写锁：所有 conn.WriteMessage 都须持锁，满足 gorilla/websocket「单写者」约束。
	// 该锁由 debugConn 创建，通过 Attach 传给 DebugSource，双方共用。
	var writeMu sync.Mutex

	// 连接级 context：断开时 cancel，确保执行 goroutine 不泄漏（Fix #3）。
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()

	// safeWrite 加锁写一帧文本消息，供 debugConn 自身使用（与源共享 writeMu）。
	safeWrite := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	// 注入连接、写锁与连接级 context，供源主动推送挂起快照等帧
	src.Attach(conn, &writeMu, connCtx)

	// 首条消息鉴权：限时读取第一条文本消息
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

	// A 部分：绑定对象 + 权限校验
	if err := src.OnConnect(info, objectID); err != nil {
		_ = safeWrite(mustJSON(DebugEvent{
			"type":  "error",
			"error": err.Error(),
		}))
		return
	}

	// 欢迎帧
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

	// ready 帧：鉴权 + OnConnect 成功后主动推送，告知前端通道可用（可发控制指令）
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

	// 心跳 + 退出信号（Fix #4：ping goroutine 通过 done channel 可靠退出，避免泄漏）
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

		// B 部分：解析客户端控制指令（非文本/非 JSON 时回显原样，便于裸调试）
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
	// 读循环退出 → defer connCancel() 取消执行 context → 执行 goroutine 感知退出
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

// ---------- /debug/flow 的调试源（对接 flow 执行引擎） ----------

// FlowDebugSource 创建 flow 调试源实例（每连接一个新实例，承载连接级状态）。
func FlowDebugSource() DebugSource { return &flowDebugSource{} }

// FlowDebugSource 持有本次 WS 连接的执行引擎与调试控制器。
// 并发安全：所有字段在 Attach/OnConnect 后可能被两个 goroutine 同时访问
//   - 读循环 goroutine（OnMessage）
//   - 执行 goroutine（pushSnapshot / pushFinished）
//
// 写操作通过 writeMu（与 debugConn 共享）串行化；读操作通过 mu 保护。
type flowDebugSource struct {
	mu       sync.Mutex      // 保护 started、execCtx 等共享字段的读写
	writeMu  *sync.Mutex     // 与 debugConn 共享的 WebSocket 写锁（Fix #1）
	connCtx  context.Context // 连接级 context，断开时 cancel（Fix #3）
	conn     *websocket.Conn // 仅在 Attach 时写入，之后只读
	objectID string          // 仅在 OnConnect 时写入，之后只读
	authInfo *auth.AuthInfo  // OnConnect 时写入，run 时构造 execCtx 要用 schema
	execCtx  *flow.ExecContext
	ctrl     *flow.DebugControl
	started  bool
	// nodeResults 按 node_id 去重保留最后一次执行的节点数据（mu 保护）。
	// 运行中点节点（inspect_node）与运行结束（pushFinished）都从这里取。
	nodeResults map[string]*flow.NodeResult
	streamWG    sync.WaitGroup // 等待 pumpStream 推完剩余流式消息后再关连接
}

func (s *flowDebugSource) Kind() string { return "flow" }

func (s *flowDebugSource) Attach(conn *websocket.Conn, writeMu *sync.Mutex, connCtx context.Context) {
	s.conn = conn
	s.writeMu = writeMu
	s.connCtx = connCtx
	// 连接断开（connCtx 取消）时：唤醒所有挂起点，让挂起的节点能感知取消并正常收尾，
	// 否则会永久阻塞在 cond.Wait 上（执行 goroutine 泄漏，且无法落库 cancelled 状态）。
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

// writeJSON 加锁写一帧 JSON 文本消息（供事件回调安全调用）。
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
	// 校验 Flow 是否存在且属于该 schema
	repo := flows.NewRepository()
	if _, err := repo.GetFlow(info, objectID); err != nil {
		return fmt.Errorf("flow instance %s 不存在或无权限: %w", objectID, err)
	}
	// 保存鉴权信息（run 时构造 execCtx 需要 schema）。
	s.authInfo = info
	// 连接时创建调试控制器（承接 run 前设置的断点），但【不构造 execCtx、不启动】。
	// execCtx 推迟到收到 run 指令时构造（见 buildExecCtx），
	// 以保证运行前自动保存的最新 diagram 能被读到、且每次 run 生成全新 runID。
	ctrl := flow.NewDebugControl()
	ctrl.Enabled = true
	// 注意：连接时保持默认 DebugRun（不挂起）。模式切换推迟到用户操作：
	// 点运行→run 全速；点暂停/单步→pause/step 才会在节点前挂起。
	ctrl.SetEventHook(func(snap *flow.DebugSnapshot) {
		s.pushSnapshot(snap)
	})
	// 节点开始执行时标记「已运行」：无论是否挂起都推 node_executing，
	// 让前端高亮所有执行过的节点（无断点全速跑也能看到执行轨迹）。
	ctrl.SetExecHook(func(nodeID string, nodeName string) {
		s.pushNodeExecuting(nodeID, nodeName)
	})
	ctrl.SetFailHook(func(nodeID string, nodeName string, errMsg string) {
		s.pushNodeFailed(nodeID, nodeName, errMsg)
	})
	ctrl.SetSkipHook(func(nodeID string, nodeName string) {
		s.pushNodeSkipped(nodeID, nodeName)
	})
	// 节点完成时累积最新数据（按 node_id 去重，保留 StartTime 最晚，与落库去重同规则）
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

// buildExecCtx 在收到 run 指令时构造执行上下文（含全新 runID）。
// 调用时机放在 run 而非 OnConnect，保证读到运行前刚保存的最新 diagram。
func (s *flowDebugSource) buildExecCtx(input map[string]interface{}, runID string, followDrafts bool) (*flow.ExecContext, error) {
	if s.authInfo == nil {
		return nil, fmt.Errorf("auth info 缺失")
	}
	repo := flows.NewRepository()
	pi, err := repo.GetFlow(s.authInfo, s.objectID)
	if err != nil {
		return nil, fmt.Errorf("flow instance %s 不存在或无权限: %w", s.objectID, err)
	}
	// 调试运行的 flow 是本次调用的起点，同样进链（见 shared/callchain）：
	// 它出站（rpc / mcp / agent 节点）绕回自己时，第一层就能被拒。
	// WithArguments 返回新 map，不改调用方传进来的 input。
	input = callchain.WithArguments(input,
		[]string{callchain.Key(callchain.KindFlow, s.objectID)})

	execCtx, err := executors.NewFlow(pi, input, "debug-"+s.objectID, runID, s.authInfo.Schema, s.authInfo.ID, s.connCtx)
	if err != nil {
		return nil, fmt.Errorf("构造执行上下文失败: %w", err)
	}
	execCtx.Debug = s.ctrl
	// 调试会话的作用域：followDrafts 决定被调子流程读草稿还是正式版本，跟随过的实体记账。
	// 主流程自身始终读工作副本（GetFlow），这个开关只管「它调起来的那些」。
	// 正式入口（MCP/RPC/local__）不设这个字段，因此不存在「漏传一次就上线草稿」的路径。
	execCtx.DebugScope = flow.NewDebugScope(followDrafts)
	// 流程执行结束后（无论成功/失败/被取消）：
	// 1) 关闭进度/结果 channel，让 pumpStream 退出；
	// 2) 回推 finished 事件。
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

// pushNodeExecuting 节点开始执行时回推，标记「已运行」节点（无论是否挂起）。
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

// pushNodeFailed 节点执行失败时回推，标记「出错」节点。
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

// pushNodeSkipped 节点被跳过时回推，标记「跳过」节点。
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

// pumpStream 消费执行上下文的进度/结果 channel，流式推给前端。
// 进度（ProgressChan）→ progress 事件；结果（ResultChan）→ result_stream 事件。
// channel 关闭后退出；连接断开（connCtx.Done）时也退出。
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

// pushFinished 流程执行结束后回推终态事件（成功/失败/被取消均会调用）。
func (s *flowDebugSource) pushFinished(flowErr error) {
	errMsg := ""
	if flowErr != nil {
		errMsg = flowErr.Error()
	}
	// 工作流最终返回信息（execCtx.Result，string 累积/设置）
	resultStr := ""
	runID := ""
	s.mu.Lock()
	ec := s.execCtx
	if ec != nil {
		runID = ec.GetRunID()
	}
	// 取出本次运行累积的节点数据（按 node_id 去重，保留最后一次执行）
	snapshots := make([]*flow.NodeResult, 0, len(s.nodeResults))
	for _, nr := range s.nodeResults {
		snapshots = append(snapshots, nr)
	}
	// 复位 started：本次运行已结束，允许下次 run 重新走首次启动分支（重建 execCtx + 新 runID）。
	s.started = false
	s.execCtx = nil
	s.mu.Unlock()
	if ec != nil {
		resultStr = ec.GetResult()
	}
	// 等待 pumpStream 推完 channel 里剩余的进度/结果消息，确保前端先收到流式消息再收到 finished。
	s.streamWG.Wait()

	// 逐个推送节点快照（避免单个超大帧；每帧仅一个节点的数据）。
	for _, nr := range snapshots {
		s.writeJSON(DebugEvent{
			"type":      "debug",
			"kind":      "flow",
			"event":     "node_snapshot",
			"object_id": s.objectID,
			"node":      nr.BuildSnapshot(),
		})
	}

	// finished 帧只带元信息，不带 nodes 数组（节点数据已通过 node_snapshot 逐个推完）。
	// 附带完整 run 对象（字段与 flow_run_records 一致），供前端展示/历史查询统一复用。
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

	// 运行结束（成功/失败/被取消）后主动关闭调试通道。
	// 持写锁关闭，避免与正在进行的写操作并发（gorilla/websocket 单写者约束）。
	if s.writeMu != nil && s.conn != nil {
		s.writeMu.Lock()
		_ = s.conn.Close()
		s.writeMu.Unlock()
	}
}

func (s *flowDebugSource) OnMessage(info *auth.AuthInfo, objectID string, ctrlMsg ControlRequest) (DebugEvent, bool) {
	switch ctrlMsg.Action {
	case "run":
		// 全速运行：无断点则一路跑完；有断点则命中断点挂起。
		// 首次收到 run 才启动执行（此前 OnConnect 保持 DebugRun，不会无端挂起）。
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()

		if !started {
			// 全新运行：清空上一轮的节点数据累积
			s.mu.Lock()
			s.nodeResults = make(map[string]*flow.NodeResult)
			s.mu.Unlock()
			// 首次运行：构造执行上下文（读到运行前刚保存的最新 diagram）+ 生成全新 runID。
			input, _ := ctrlMsg.Input.(map[string]interface{})
			runID := uuid.Must(uuid.NewV7()).String()
			// 只有「全新运行」这一次能决定作用域：跑起来之后再切开关不该影响这一轮
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

			// 运行启动即回推运行级信息（入参/限额/开始时间），
			// 供断点挂起时点「开始节点」查看本次运行参数（无需等 finished）。
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
			// 消费进度/结果 channel，流式推给前端（并避免缓冲满后 SendProgress 阻塞）。
			s.streamWG.Add(1)
			go s.pumpStream(ec)
		} else {
			// 已启动：run 表示"继续全速"，解除任何挂起
			s.ctrl.Control(flow.DebugRun, "")
			s.ctrl.Resume()
		}
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "resumed",
			"object_id": objectID, "mode": "run", "at": time.Now().Format(time.RFC3339),
		}, false

	case "pause":
		// 下一节点前暂停
		s.ctrl.Control(flow.DebugPause, "")
		s.ctrl.Resume()
		return DebugEvent{
			"type": "debug", "kind": "flow", "event": "control",
			"object_id": objectID, "mode": "pause", "at": time.Now().Format(time.RFC3339),
		}, false

	case "step":
		// 单步：over/in/out
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
		// on 默认 false（取消）；前端设断点发 on:true
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
		// 运行中点节点：从累积的最新节点数据里取单个快照回推
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

// ---------- /debug/agent 的调试源（骨架） ----------

// AgentDebugSource 创建 agent 调试源实例（每连接一个新实例）。
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
	// TODO: 校验 objectID 是否属于 info.Schema，并绑定 service/agent/core 会话
	return nil
}

func (s *agentDebugSource) OnMessage(info *auth.AuthInfo, objectID string, ctrl ControlRequest) (DebugEvent, bool) {
	// Fix #8: agent 调试源尚未实现，返回明确的 not-implemented 帧（而非占位数据）
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

// ---------- /ws 业务骨架（与 /debug 鉴权一致） ----------

// EchoHandler 通用 WebSocket 接入骨架：echo 回显 + 心跳。
func EchoHandler(hub *Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := getAuth(c) // 取登录信息（按需使用，如按租户隔离）
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
