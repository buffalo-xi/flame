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

// NewMCPServer 按租户加载 svc_endpoints（protocol=mcp AND exposed=true），动态构建 Server 实例。
// 每个 endpoint 注册为一个 MCP 工具，input_schema 直接用作工具 InputSchema。
func NewMCPServer(authInfo *auth.AuthInfo) (*mcp.Server, error) {
	listReq := &svcendpoints.ListSvcEndpointsRequest{
		Protocol: "mcp",
		Exposed:  boolPtr(true),
	}
	// 必须用 ListAll：List 有默认 limit=10，会把第 11 个端点起静默截掉
	endpoints, err := svcendpoints.NewRepository().ListAll(authInfo, listReq)
	if err != nil {
		return nil, fmt.Errorf("list mcp endpoints: %w", err)
	}

	impl := &mcp.Implementation{
		Name:    "flame-mcp-server",
		Version: "2.0",
	}
	// 只认一版协议：2026-07-28（无 initialize 握手，每个请求自带 _meta 里的 protocolVersion）。
	// 新版客户端在 _meta 里报了别的版本 → SDK 回 -32022 UnsupportedProtocolVersion；
	// 旧版客户端仍会走 initialize，被协商成 2025-11-25 后，紧接着带
	// MCP-Protocol-Version 头的请求会被传输层以 400 Unsupported protocol version 拒掉。
	s := mcp.NewServer(impl, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{mcpspec.ProtocolVersion},
	})

	for i := range endpoints {
		ep := endpoints[i]
		// 凭证的端点访问范围：无权目标不注册。
		// MCP 只需这一处 —— tools/list 由注册表生成，调用也只能命中已注册的 handler，
		// 不存在 rpc 那种「手写请求按 name 直调」的绕过路径。
		if !authInfo.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			continue
		}
		tool := buildTool(ep)
		s.AddTool(tool, makeToolHandler(authInfo, ep))
	}
	return s, nil
}

// buildTool 从 SvcEndpoint 构造 mcp.Tool
func buildTool(ep models.SvcEndpoint) *mcp.Tool {
	t := &mcp.Tool{
		Name:        ep.Name,
		Description: ep.Description,
	}
	// input_schema 直接作为工具的 InputSchema（JSON Schema object）
	// 仅当用户 schema 合法（解析成功且顶层 type=object）才采用，否则回退到开放 object
	if !ep.InputSchema.IsNil() {
		var raw json.RawMessage
		if err := ep.InputSchema.Unmarshal(&raw); err == nil && len(raw) > 0 {
			if isObjectSchema(raw) {
				t.InputSchema = raw
			}
		}
	}
	// 兜底：无 input_schema 或非法时给一个开放 object schema（任意输入均合法）
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t
}

// isObjectSchema 校验 JSON Schema 顶层为 object 类型（SDK 要求）
func isObjectSchema(raw json.RawMessage) bool {
	var s struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s.Type == "object"
}

// makeToolHandler 返回该工具的调用处理函数
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
		// 已废弃的 arguments.stream：剥掉（既不当控制参数，也不当业务变量），留日志便于排查。
		if core.StripLegacyStream(input) {
			xlog.Info("忽略已废弃的 arguments.stream（改用 params._meta.progressToken 订阅进度）endpoint=%s", ep.Name)
		}

		// 调用链：拒绝重入（f → a → f），与 /service/rpc 同一套语义（见 shared/callchain）。
		// 链只认 params._meta，不进 arguments —— 后者是模型可见的业务入参。
		// 同样是协作式检测：不传就检测不到，深度上限仍必须留着兜底。
		chain, chainTooLong := callChain(req)
		if chainTooLong {
			return nil, fmt.Errorf("call_chain 超过上限 %d 段", callchain.MaxLen)
		}
		self := callchain.Key(ep.TargetType, ep.TargetID)
		if callchain.Contains(chain, self) {
			xlog.Info("检测到循环调用 schema=%s endpoint=%s chain=%s", authInfo.Schema, ep.Name, callchain.Format(chain))
			return nil, fmt.Errorf("检测到循环调用：%s 已在本调用链中（%s）", self, callchain.Format(chain))
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

// runFlowTool 执行 flow 类型目标。
// 客户端没订阅进度（无 progressToken）：同步执行并返回最终结果。
// 订阅了进度：在后台执行，消费 progress/result channel 并通过 notifications/progress 推送
// （两个通道都按 category=text 推送；最终结果只认 tools/call 的返回值）。
func runFlowTool(authInfo *auth.AuthInfo, flowId string, input map[string]interface{}, req *mcp.CallToolRequest, ctx context.Context, chain []string) (*mcp.CallToolResult, error) {
	repo := flows.NewRepository()
	// 正式入口：只运行已发布的正式版本。没有正式版本时明确报错，
	// 不退回草稿 —— 草稿只在编辑页调试/run_flow 试跑里生效。
	Instance, err := repo.GetCurrentFlowVersion(authInfo, flowId)
	if err != nil {
		if errors.Is(err, flows.ErrNoReleasedVersion) {
			return nil, fmt.Errorf("flow %s 尚未发布正式版本，请先在编辑页发布后再调用", flowId)
		}
		return nil, fmt.Errorf("get flow instance %s: %w", flowId, err)
	}
	// 废弃的 flow 不可运行：status 在实体行、版本快照里没有，外部入口必须自己判一次，
	// 不能只靠「切废弃时收回端点」这个 best-effort 动作（失败只记日志）。
	if !models.IsRunnable(int(Instance.Status)) {
		return nil, fmt.Errorf("flow %s 已废弃，不可调用", Instance.Name)
	}

	// 要不要推进度只由客户端决定：params._meta.progressToken 存在 = 订阅了。
	// 端点侧没有自己的开关（flows.stream 已废弃）—— server 无权替客户端决定收不收通知。
	pump := core.NewProgressPump(progressToken(req))

	// 会话 id 只认 params._meta 一处（与 agent 端点同一口径）。
	// 归属校验放在这里而不是 NewFlow 里 —— 后者还要服务「agent 调 flow 时共用调用方会话」
	// 的内部路径，那里的 owner 是父 agent。
	sid := metaSessionID(req)
	if sid != "" {
		// 会话归属用户（owner 只记录创建者）：父子 agent 共用同一 session 时 owner 对不上，
		// 用 owner 收口会把合法级联整体拒掉，所以这里与 HTTP chat 一样按 user 收口。
		// 不存在则用该 id 建档 —— 协议端点靠调用方自带 UUID 表达「首次创建」。
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeFlow, Instance.ID, authInfo.ID, "",
		); serr != nil {
			return nil, fmt.Errorf("session %s: %w", sid, serr)
		}
		input = core.WithSessionID(input, sid) // NewFlow 会再把它剥离出业务入参
	}
	// 调用链走保留键，NewFlow 会剥离（与 _call_depth 同一套机制），不进业务变量
	input = callchain.WithArguments(input, chain)

	execCtx, err := executors.NewFlow(Instance, input, uuid.NewString(), uuid.Must(uuid.NewV7()).String(), authInfo.Schema, authInfo.ID, ctx)
	if err != nil {
		return nil, fmt.Errorf("init flow exec: %w", err)
	}

	if !pump.Enabled() {
		// 错误已双写进 execCtx.Error，交由 finalFlowResult 统一处理
		_ = executors.Start(execCtx)
		return finalFlowResult(execCtx)
	}

	// 订阅了进度：后台执行，主协程消费 channel 并推送通知
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
			// 后台执行结束：业务错误已双写进 execCtx.Error，
			// 统一交给 finalFlowResult 包装（isError 结果 vs 文本结果）
			break loop
		case <-ctx.Done():
			execCtx.CloseChans()
			return nil, ctx.Err()
		}
	}

	return finalFlowResult(execCtx)
}

// finalFlowResult 在 flow 执行收尾时统一检查 execCtx.Error：
// 有错则返回 isError 标记的结果，否则返回最终结果。
func finalFlowResult(execCtx *flow.ExecContext) (*mcp.CallToolResult, error) {
	if e := execCtx.GetError(); e != "" {
		return errorResult(e), nil
	}
	return textResult(execCtx.GetResult()), nil
}

// runAgentTool 执行 agent 类型目标。
// 客户端没订阅进度（无 progressToken）：同步执行并返回最终结果。
// 订阅了进度：在后台执行，消费结构化事件 channel 并通过 notifications/progress 推送
// （category = thinking / text / tool_call / tool_result）；
// Agent 没有 final 帧：最终答案随 tools/call 返回，流里靠 tool_call 边界切分。
func runAgentTool(authInfo *auth.AuthInfo, agentID string, input map[string]interface{}, req *mcp.CallToolRequest, ctx context.Context, chain []string) (*mcp.CallToolResult, error) {
	repo := agents.NewRepository()
	agentInst, err := repo.GetAgent(authInfo, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent instance %s: %w", agentID, err)
	}

	// 同 runFlowTool：进度订阅只由 progressToken 表达，agent 端点曾恒为非流式，
	// 那只是「没有开关可回退」的副产物，现在与 flow 端点同一口径。
	pump := core.NewProgressPump(progressToken(req))

	// 会话 id 只认 params._meta 一处（与 progressToken 同一处）。
	sid := metaSessionID(req)
	// 业务入参里的同名键一律剥掉：它会变成模型可见的 {{args.session_id}}。
	// 这里不再拿它当会话开关，只是防止误传把控制字段漏进提示词。
	input = core.StripSessionID(input)

	// 会话归属：给了 session_id 就必须是这个用户的会话（与 HTTP chat 同一口径）。
	// owner 只记录创建者：父子 agent 共用同一 session 时 owner 必然对不上，
	// 用 owner 收口会把 call_agent 的合法级联整体拒掉，所以按 user 收口；
	// 不存在则用该 id 建档（调用方自带 UUID 即首次创建的意图）。
	if sid != "" {
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeAgent, agentInst.ID, authInfo.ID, "",
		); serr != nil {
			return nil, fmt.Errorf("session %s: %w", sid, serr)
		}
	}

	// user_id 恒定注入：verifyAPIKey / verifyOAuth 只放行绑定了用户的凭证（见 auth.go），
	// MCP 没有租户级/无主身份 —— 运行域隔离、会话归属与会话记忆全都以它为归属依据。
	// session_id 由调用方自带（已从业务入参剥离）：给了就是会话调用，不给则单轮、不记记忆。
	agentArguments := map[string]interface{}{
		"args":      input,
		"user_id":   authInfo.ID,
		"_run_type": "mcp",
	}
	if sid != "" {
		agentArguments["session_id"] = sid
	}
	// 链放顶层保留键，不放 args 内部：args 是模型可见的业务入参，
	// 控制字段混进去会变成 {{args._call_chain}}。
	if len(chain) > 0 {
		agentArguments[callchain.ArgumentKey] = chain
	}
	// 正式入口：读已发布的当前版本（没有版本就拒绝，不退回草稿）
	agent.SetReleaseMode(agentArguments)
	inst, err := agent.NewAgent(agentInst, authInfo.Schema, agentArguments)
	if err != nil {
		return nil, fmt.Errorf("init agent: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("agent %s not supported or not found", agentID)
	}
	defer inst.Close()

	if !pump.Enabled() {
		// 用调用方的 ctx：对端关连接 / 取消请求即在同一条 ctx 上取消本次运行，
		// 不必等它把 LLM 轮次和沙箱调用跑完（与 HTTP 试聊同一口径）。
		// 错误已双写进 inst.Error，交由 finalAgentResult 统一处理。
		_ = inst.Stream(ctx)
		inst.CloseChans()
		if ctx.Err() != nil {
			return nil, ctx.Err() // 调用方已断开：不再回一条没人收的结果
		}
		return finalAgentResult(inst)
	}

	// 订阅了进度：后台执行，主协程消费「结构化事件」channel 并推送通知。
	// 思考（EventThinking）与工具调用只在这里出现，早先的纯文本进度通道已删除。
	ss := req.Session

	errCh := make(chan error, 1)
	go func() {
		errCh <- inst.Stream(ctx)
		inst.CloseChans()
	}()

	eventCh := inst.GetEventChan()
	// 调用方断开后不再推通知，但仍把事件通道读干净：运行与本次调用共用同一条 ctx，
	// 已随断开取消 —— 等它自行收尾（落库、杀常驻进程）再返回。直接 return 会让
	// defer inst.Close() 在 Stream 还没退出时跑，与它抢同一个一次性工作目录。
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
				continue // 已断开：只读不推
			}
			if item, ok := ev.ToProgress(); ok {
				notifyCategory(ctx, ss, pump, item.Category, item.Message, item.Meta)
			}
		case <-errCh:
			// 后台执行结束：业务错误已双写进 inst.Error，统一交给 finalAgentResult 包装
			break loop
		case <-gone:
			gone = nil // 置 nil 后该分支永久阻塞，避免空转
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err() // 调用方已断开：结果无人接收
	}
	return finalAgentResult(inst)
}

// finalAgentResult 在 agent 执行收尾时统一检查 inst.GetError()：
// 有错则返回 isError 标记的结果，否则返回最终结果。
func finalAgentResult(inst core.Agent) (*mcp.CallToolResult, error) {
	if e := inst.GetError(); e != "" {
		return errorResult(e), nil
	}
	return textResult(inst.GetResult()), nil
}

// textResult 构造文本型工具返回
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: s},
		},
	}
}

// errorResult 构造带 isError 标记的文本型工具返回（工具已执行但业务结果失败）
func errorResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: s},
		},
		IsError: true,
	}
}

// callChain 取客户端在 params._meta["call_chain"] 里给的祖先链。
//
// tooLong 表示超过 MaxLen：必须直接拒绝，不能截断 ——
// 被截掉的那几段里可能正好藏着要检测的重入。
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

// metaSessionID 取客户端在 params._meta["session_id"] 里给的会话 id。
//
// 与 progressToken 对称：都是「影响这次调用怎么处理」的元数据，都不属于端点的业务参数。
// 返回空串 = 没给（无状态运行，走 runs/ 兜底场地）。
func metaSessionID(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	s, _ := req.Params.Meta[core.MetaSessionIDKey].(string)
	return strings.TrimSpace(s)
}

// progressToken 取客户端在 params._meta.progressToken 里表达过的订阅意愿。
//
// 返回 nil 表示「没订阅」—— 这时一帧进度都不能发：MCP 2026-07-28 要求
// notifications/progress 只引用活跃请求里提供过的 token。
// 早先的做法是没传就兜底生成一个 UUID，等于往客户端从未订阅过的 token 上推送。
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

// notifyCategory 通过标准 notifications/progress 推送一条指定 category 的增量。
// category 取值与事件映射统一在 agent/core（Category* 常量 / Event.ToProgress）。
// message 与 extra 都为空时不发，避免推送空帧 —— 也因此不会白白消耗一个序号。
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
		// progress MUST 递增：这里给的是「第几条通知」而非真实完成度，
		// 总量未知，故不填 Total（规范允许省略）。
		Progress: pump.Next(),
		Message:  message,
	})
}

// boolPtr 辅助
func boolPtr(b bool) *bool {
	return &b
}

// NewMCPHandler 返回 gin 路由处理函数：先做租户识别 + 认证（Bearer 分流），
// 再委托给 SDK 的 StreamableHTTPHandler（无状态模式）。
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
			// 允许不带 session 头（无状态模式）
			JSONResponse: false,
			// 让 SDK 处理协议细节；认证失败已由外层拦截
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

// xhttpErr 写简单 JSON 错误（认证失败不记录凭证明文）
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
