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
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// sseKeepAlive SSE 心跳间隔。
//
// 15 秒：小于常见反向代理的 60 秒空闲超时，又不至于把一条安静的流刷成噪音。
// 心跳同时充当半开连接的探针 —— 写失败即判定对端已断（见 Chat）。
const sseKeepAlive = 15 * time.Second

// 会话记录保留上限在 agent 层（service/agent/agent_impl.go maxSessionRecordKeep）。

// chatRequest agent 对话请求
type chatRequest struct {
	Message   string   `json:"message"`
	SessionID string   `json:"session_id"` // 可选：为空则新建会话
	Files     []string `json:"files"`      // 本轮附件（会话工作区相对路径，来自 /session/upload）
	// FollowDrafts 本轮**调起的子实体**（agent / flow）读草稿还是已发布版本。
	// 指针以区分「没传」与「显式 false」：缺省 true（跟草稿，与 flow 调试器同一缺省）。
	// 只影响被调起的那些；本对话的 agent 自身始终读草稿。
	FollowDrafts *bool `json:"follow_drafts"`
}

// ChatAgent 与指定 agent 对话，SSE 流式返回，支持多轮会话历史。
// 事件对齐 AG-UI 语义：
//
//	RUN_STARTED / TEXT_MESSAGE_CONTENT / RUN_FINISHED / RUN_ERROR
//	THINKING_DELTA（模型推理）/ TOOL_CALL_START / TOOL_CALL_END（工具调用）
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
	// 只带附件也允许发送：正文为空时由附件清单承担「这一轮要干什么」
	if message == "" && len(req.Files) == 0 {
		xhttp.ErrJSON(c, "message is required", nil, "")
		return
	}

	instance, err := repo.GetAgent(ai, id)
	if err != nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
		return
	}

	// 会话处理：空则新建，非空走统一归属校验（sessions.Lookup）
	sessRepo := sessions.NewRepository()
	sessionID := strings.TrimSpace(req.SessionID)
	// 会话工作目录按「会话归属用户」定位（附件就在那下面），单独记一份：
	// 目录的唯一事实来源是会话行，而不是调用方身份 —— 两者已被 lookupSessionForUser
	// 校验为同一个用户，取会话自己的值可以在将来放宽口径时仍然指对目录。
	sessUserID := ai.ID
	if sessionID == "" {
		sessionID, err = sessRepo.Create(ai.Schema, models.SessionOwnerTypeAgent, instance.ID, ai.ID, "")
		if err != nil {
			respondQuotaErr(c, err) // 会话数超限 → 507
			return
		}
	} else {
		// 会话私有：按归属用户收口（owner 只记录创建者，见 lookupSessionForUser）
		sess, lerr := lookupSessionForUser(ai, ai.Schema, sessionID)
		if lerr != nil {
			respondSessionErr(c, lerr, notOwnedSession)
			return
		}
		// 租户级会话（user_id 为空）沿用调用方身份定位目录，避免拼出 users//sessions/…
		if sess.UserID != "" {
			sessUserID = sess.UserID
		}
	}

	// 附件清单只拼进「喂给模型」的这一条；落库（_record_input）与历史回放仍是用户原文。
	// 历史不再由这里读：agent 层按 session_id 从 session_records 读，与协议端点共用同一份
	// 记忆实现（否则同一个会话从两个入口进来，历史长度、顺序、条数都可能不一样）。
	prompt := message
	var block string
	if st, serr := getStorage(); serr == nil {
		block = attachmentPrompt(st, ai.Schema, sessUserID, sessionID, req.Files)
	}
	if block != "" {
		if prompt == "" {
			prompt = block // 只带附件、没有正文时，别以空行开头
		} else {
			prompt = prompt + "\n\n" + block
		}
	}

	runID := uuid.NewString()

	args := map[string]interface{}{
		"user_id":       ai.ID,
		"session_id":    sessionID,
		"run_id":        runID,
		"_user_message": prompt,  // 喂给模型的当前输入（含附件清单）
		"_record_input": message, // 落库的原文（不含附件清单）
		"_run_type":     "call",
	}
	// 起点实体同样进链：这次对话的 agent 就是「当前执行实体」，
	// 它再出站（rpc__ / mcp__ / call_agent）时链里已含自己 ——
	// 绕回来能在第一层就拒，而不是白跑一整轮才被深度上限拦住。
	args[callchain.ArgumentKey] = []string{callchain.Key(callchain.KindAgent, instance.ID)}
	// 管理端聊天是「试聊」：顶层 agent 恒读草稿，改完立刻能验证。
	// 正式调用（MCP / RPC / 端点 / flow）才读已发布版本 —— 那几条路走 SetReleaseMode。
	agent.SetDraftMode(args)
	// 子调用（call_agent / local__flow / run_flow）读哪份由本开关决定，缺省跟草稿。
	// 关掉后走已发布版本：被调实体没发布过会明确报错，不静默回退草稿。
	followDrafts := true
	if req.FollowDrafts != nil {
		followDrafts = *req.FollowDrafts
	}
	agent.SetFollowDrafts(args, followDrafts)
	inst, err := agent.NewAgent(instance, ai.Schema, args)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to init agent: "+err.Error(), nil, "")
		return
	}

	// SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		xhttp.ErrJSON(c, "Streaming unsupported", nil, "")
		return
	}

	sendSSE(c.Writer, flusher, "RUN_STARTED", map[string]interface{}{"agent": instance.Name, "session_id": sessionID})

	// 本轮耗时：从 RUN_STARTED 到 run 结束。既落库（session_records.duration_ms），
	// 也随结束事件回给前端 —— 保证「刚跑完看到的秒数」与「刷新后历史里的秒数」是同一个值。
	startedAt := time.Now()

	// 后台运行 agent 循环
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		if e := inst.Stream(context.Background()); e != nil {
			inst.LogError("agent stream error: " + e.Error())
		}
		inst.CloseChans()
	}()

	// 流式输出：思考 / 正文 / 工具调用。
	// 客户端断开（关页面 / 取消请求）时主动终止本轮：否则 SSE 连接已经没了，
	// agent 还会把整轮跑完 —— 期间 exec / browser 继续占用沙箱资源。
	//
	// 两个断开信号都要接住：
	//   - reqCtx.Done：浏览器 abort / 关页面，FIN 到达即触发；
	//   - 心跳写失败：断网 / 休眠 / 中间代理空闲超时不会送 FIN，只能靠「写一次」发现。
	// 旧实现是在「收到一条事件时」顺带检查 ctx —— 模型长思考或长工具调用期间断连
	// 要等下一个事件才生效，那段时间沙箱仍在空转。这里改成 select 直接等。
	clientGone := false
	signalGone := func(why string) {
		if clientGone {
			return
		}
		clientGone = true
		inst.LogError("客户端已断开（" + why + "），终止本轮")
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
			signalGone("连接关闭")
			reqCtx = nil // 置 nil 后该分支永久阻塞，避免重复触发
		case <-ticker.C:
			if clientGone {
				continue
			}
			// SSE 注释帧：既是保活（防代理 60s 空闲断开），也是半开连接的探针。
			if _, err := fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
				signalGone("心跳写入失败")
				continue
			}
			flusher.Flush()
		case ev, ok := <-eventCh:
			if !ok {
				break loop
			}
			if clientGone {
				continue // 只排空 channel，不再往已断开的连接写
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

	// 与落库值同源：agent 层写 session_records 时冻结的耗时，避免「刚看到 8.4s、
	// 刷新后历史里 8.3s」的错位（两个值必须完全一致）。
	elapsedMs := inst.GetDurationMs()

	if e := inst.GetError(); e != "" {
		sendSSE(c.Writer, flusher, "RUN_ERROR", map[string]interface{}{"message": e, "duration_ms": elapsedMs})
	} else {
		// 本轮产物：run 结束后扫一次工作区，把「这轮写过哪些文件」随结束事件下发。
		// 只报客观事实（写过），不做价值判断；前端拿它渲染「本轮产出」区域。
		sendSSE(c.Writer, flusher, "RUN_FINISHED", map[string]interface{}{
			"duration_ms": elapsedMs,
			"artifacts":   runArtifacts(ai.Schema, sessUserID, sessionID, startedAt),
		})
	}

	// 本轮落库已由 agent 层在 Stream 结束时完成（有 session_id 才写），
	// 这里不再重复写，否则一次对话会在 session_records 里留下两行。

	fmt.Fprint(c.Writer, "\n")
	flusher.Flush()
}

// 会话记录的读写已全部收口到 agent 层（service/agent/agent_impl.go）：
// 读历史、写本轮、刷新标题都在那里，入口只负责建/解析会话与注入 _record_input。
// 这样 Web 对话、RPC、MCP、call_agent、flow 节点调起的 agent 共用一套记忆口径。

// sendSSE 以标准 SSE 分帧发送一个事件。
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte("{}")
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}

// stopRequest 终止运行的定位参数：
//   - session_id：必填。它既是「停哪批运行」的锚点，也是归属校验的唯一依据 ——
//     没有它就无法判断要停的这条运行是不是本 agent 的；
//   - run_id：可选。给了就精确停这一条，不给则停该会话下所有在跑的运行（UI 只知道会话）。
//
// 不接受「只给 run_id」：run 台账是进程级、跨 agent 的，只凭一个 run_id 就能停掉
// 别人的运行；而 RUN_STARTED 从不回传 run_id，正常前端也拿不到它 ——
// 这条路本来就不该开（此前文档写着「必须同时给 session_id」，实现却只要求二选一）。
type stopRequest struct {
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
}

// StopAgentRun 终止正在运行的 agent。
//
// 幂等：没在跑时返回 stopped:0，不报错（重复调用不会出问题）。
// 终止后本轮仍会落库（output 为空、duration_ms 照记），SSE 侧收到
// RUN_ERROR「运行已终止（用户请求）」。
//
// 对话页的停止按钮**不走这里**：它直接 abort 掉那条 SSE 连接（后端按
// ErrClientGone 终止）。原因是台账在进程内，多实例部署时这条 HTTP 请求可能被
// 负载均衡分到没跑这一轮的实例上，只会拿到 stopped:0。本接口留给管理端 /
// 运维 / 没有连接可断的入口（RPC、MCP、脚本）。
func StopAgentRun(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	// 仍要过 GetAgent：先确认这个 agent 在本租户可见，再谈停止
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
		xhttp.ErrJSON(c, "session_id is required", nil, "")
		return
	}

	// 归属校验：与 ChatAgent 同一口径（按会话归属用户收口）。
	// 只校验 user 就够了 —— stop 按会话取消全部在跑的运行，跨用户就能打断别人。
	if _, lerr := lookupSessionForUser(ai, ai.Schema, req.SessionID); lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return
	}

	stopped := 0
	if req.RunID != "" {
		// session_id 一并传入：run 台账会做归属比对，防止跨会话误停。
		if agent.StopRunCause(req.RunID, req.SessionID, agent.ErrRunStopped) {
			stopped = 1
		}
	} else {
		stopped = agent.StopSession(req.SessionID)
	}

	xhttp.OkJSON(c, "Success", gin.H{"stopped": stopped}, "")
}

// cleanAgentChunk 清理 agent 输出中的内部标记，只保留可展示文本。
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
