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

// JSON-RPC 2.0 错误码
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeInvalidParams  = -32602
	errCodeInternalError  = -32603
	// errCodeQuotaExceeded 配额不足（会话数上限等）：JSON-RPC 留给实现自定义的 -32000..-32099，
	// 与控制台 507 对应，客户端据此提示「去清理」而不是当成服务端故障。
	errCodeQuotaExceeded = -32000
	// errCodeCallLoop 检测到循环调用（f → a → f）：链路本身成环，重试不会成功，
	// 客户端应据此报「配置成环」而不是当服务端故障。
	errCodeCallLoop = -32001
)

// rpcRequest 客户端发来的 JSON-RPC 请求
type rpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version"` // 协议主版本号；缺省按 MinSupportedVersion
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      interface{} `json:"id"`
}

// rpcError JSON-RPC 错误对象
type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// rpcResponse JSON-RPC 响应信封，result 与 error 二选一
type rpcResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

// rpcToolContent 工具结果中的一段文本内容（对齐 MCP TextContent）
type rpcToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// rpcToolResultMsg 工具调用结果：content 恒为文本段数组（对齐 MCP CallToolResult）
type rpcToolResultMsg struct {
	Content []rpcToolContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// rpcProgressMsg 进度通知参数（字段位置对齐 MCP 2026-07-28 的 notifications/progress）。
//
// progressToken 在 params 顶层（它标识「本次调用的哪一条订阅」），_meta 是 params 内的一个键。
// Progress 是「第几条通知」，只保证单调递增，不代表完成度 —— 总量未知时 Total 省略。
type rpcProgressMsg struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total,omitempty"`
	Message       string  `json:"message"`
	// Meta 用 map[string]any：agent 事件（tool_call/tool_result）要在 _meta 里带
	// call_id / ok / duration_ms 这类非字符串字段。
	Meta map[string]any `json:"_meta,omitempty"`
}

// rpcProgress 进度通知（notifications/progress）
type rpcProgress struct {
	Jsonrpc string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  rpcProgressMsg `json:"params"`
}

// rpcTool 工具定义（对齐 MCP tools/list 返回的 Tool 结构）
type rpcTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// rpcListToolsResult tools/list 的 result 结构（对齐 MCP ListToolsResult）
type rpcListToolsResult struct {
	Tools         []rpcTool `json:"tools"`
	ServerVersion int       `json:"serverVersion"` // 服务端当前协议版本，供客户端对齐
}

// NewRPCHandler 返回 RPC 处理函数：先做租户识别 + 认证（Bearer 分流），
// 再解析 JSON-RPC 请求，按 svc_endpoints（protocol=rpc）的 name 分发到 agent / flow。
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

		// 解析 JSON-RPC 请求体
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			xhttpErr(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
			return
		}
		if req.Jsonrpc != "2.0" || req.Method == "" {
			xhttpErr(w, http.StatusBadRequest, "invalid jsonrpc request")
			return
		}

		// 版本归一 + 校验（复用公共包，避免散落硬编码）
		req.Version = protocol.NormalizeVersion(req.Version)
		if !protocol.IsVersionSupported(req.Version) {
			writeVersionError(w, req.Version)
			return
		}

		// tools/list：返回本租户 protocol=rpc 且 exposed 的工具列表（对齐 MCP）
		if req.Method == "tools/list" {
			writeToolsList(w, req.ID, ai)
			return
		}

		// 从 params 提取 name / arguments（参数格式错：对齐 MCP 的 InvalidParams(-32602)，HTTP 保持 200）
		name, err := extractName(req.Params)
		if err != nil {
			writeRPCError(w, req.ID, errCodeInvalidParams, "获取服务名称错误: %s", err.Error())
			return
		}
		arguments, err := extractArguments(req.Params)
		if err != nil {
			writeRPCError(w, req.ID, errCodeInvalidParams, "获取参数错误: %s", err.Error())
			return
		}

		// 已废弃的 arguments.stream 就地剥掉：它既不该被当成 flow / agent 的业务变量，
		// 也不该再影响判定（判定只看下面的 progressToken）。静默失效更难查，故留日志。
		if core.StripLegacyStream(arguments) {
			xlog.Info("忽略已废弃的 arguments.stream（改用 params._meta.progressToken 订阅进度）")
		}
		// 进度订阅：客户端在 params._meta.progressToken 里表达，与 MCP 同一位置。
		pump := core.NewProgressPump(extractProgressToken(req.Params))

		// 按 name 查 svc_endpoints（protocol=rpc）
		ep, err := findEndpoint(ai, name)
		if err != nil {
			// 工具不存在：资源名（params.name）指向无效，对齐 MCP 的 InvalidParams(-32602)
			writeRPCError(w, req.ID, errCodeInvalidParams, "%s", err.Error())
			return
		}
		// 凭证无权调用该端点：与「不存在」用完全相同的措辞，不泄漏「存在但未授权」。
		// 放在 exposed 检查之前 —— 连「未对外开放」这个事实都不该被无权调用方探到。
		if !ai.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			writeRPCError(w, req.ID, errCodeInvalidParams, "服务端点不存在: %s", name)
			return
		}
		if !ep.Exposed.Bool() {
			// 端点未对外开放：资源不可用（参数指向一个仅内部端点），对齐 MCP 归为 InvalidParams
			writeRPCError(w, req.ID, errCodeInvalidParams, "服务端点未对外开放: %s", name)
			return
		}

		// 调用链：拒绝重入（f → a → f），并把本实体追加后下传给执行层。
		// 检查放在 exposed 之后：无权 / 未开放的端点连「链上有没有它」都不该被探到。
		// 链是调用方自报的，不传就检测不到 —— 所以深度上限（max_call_depth /
		// max_recursion_depth）必须继续留着兜底，这里只做精确拦截。
		chain, chainTooLong := callchain.ParseParams(req.Params)
		if chainTooLong {
			writeRPCError(w, req.ID, errCodeInvalidParams,
				"call_chain 超过上限 %d 段", callchain.MaxLen)
			return
		}
		self := callchain.Key(ep.TargetType, ep.TargetID)
		if callchain.Contains(chain, self) {
			xlog.Info("检测到循环调用 schema=%s endpoint=%s chain=%s", ai.Schema, ep.Name, callchain.Format(chain))
			writeRPCError(w, req.ID, errCodeCallLoop,
				"检测到循环调用：%s 已在本调用链中（%s）", self, callchain.Format(chain))
			return
		}
		// 追加后下传：兄弟分支各自从父链派生，互不影响（f 的两个并列节点都调 a 仍然放行）
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
			// 不支持的 target_type：参数指向的 target 非法，对齐 MCP 的 InvalidParams(-32602)
			writeRPCError(w, reqID, errCodeInvalidParams, "unsupported target_type %q for endpoint %s", ep.TargetType, ep.Name)
		}
	}
}

// extractName 从 params 中取出 "name" 字段（string）
func extractName(params interface{}) (string, error) {
	mp, ok := params.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("params must be an object")
	}
	name, _, err := conv.GetPropertyAsString(mp, "name")
	return name, err
}

// extractArguments 从 params 中取出 "arguments" 字段（map）
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

// findEndpoint 按 name 精确查本租户 protocol=rpc 的 svc_endpoints（大小写不敏感）。
// 用精确查询而非 List 的 ILIKE，避免 "order" 命中 "order-v2" 这类歧义。
func findEndpoint(authInfo *auth.AuthInfo, name string) (*models.SvcEndpoint, error) {
	ep, err := svcendpoints.NewRepository().FindByName(authInfo, "rpc", name)
	if err != nil {
		return nil, fmt.Errorf("获取服务端点错误: %w", err)
	}
	if ep == nil {
		return nil, fmt.Errorf("服务端点不存在: %s", name)
	}
	return ep, nil
}

// listTools 返回本租户 protocol=rpc 且 exposed=true 的端点列表，映射为工具定义。
// 逻辑对齐 MCP 的 NewMCPServer + buildTool：input_schema 作为工具的 InputSchema。
func listTools(authInfo *auth.AuthInfo) ([]rpcTool, error) {
	repo := svcendpoints.NewRepository()
	listReq := &svcendpoints.ListSvcEndpointsRequest{
		Protocol: "rpc",
		Exposed:  boolPtr(true),
	}
	// 必须用 ListAll：List 有默认 limit=10，会把第 11 个端点起静默截掉
	endpoints, err := repo.ListAll(authInfo, listReq)
	if err != nil {
		return nil, fmt.Errorf("list rpc endpoints: %w", err)
	}

	tools := make([]rpcTool, 0, len(endpoints))
	for i := range endpoints {
		ep := endpoints[i]
		// 凭证的端点访问范围：无权目标连列表里都不出现
		// （调用侧还会再拦一次 —— 只过滤列表的话，手写 JSON-RPC 按 name 直调就能绕过）
		if !authInfo.TargetAllowedWith(ep.TargetType, ep.TargetID, ep.TargetSupervisor) {
			continue
		}
		tool := rpcTool{
			Name:        ep.Name,
			Description: ep.Description,
		}
		// input_schema 直接作为工具的 InputSchema（JSON Schema object）
		// 仅当 schema 合法（解析成功且顶层 type=object）才采用，否则回退开放 object
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

// writeToolsList 写 tools/list 响应（JSON-RPC 2.0 标准响应，非流式）
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

// isObjectSchema 校验 JSON Schema 顶层为 object 类型（与 MCP server 一致）
func isObjectSchema(raw json.RawMessage) bool {
	var s struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s.Type == "object"
}

// boolPtr 返回指向 b 的指针
func boolPtr(b bool) *bool {
	return &b
}

// runFlow 执行 flow 类型目标，返回 JSON-RPC 响应。
// 客户端没订阅进度（无 progressToken）：同步执行，回单条 JSON。
// 订阅了进度：后台执行 + SSE 推 notifications/progress，最终结果仍是流里那条带 id 的响应。
// params 原样传进来只为读 _meta（会话 id 的正位），业务入参仍是 input。
func runFlow(w http.ResponseWriter, r *http.Request, authInfo *auth.AuthInfo, flowId string, params any, input map[string]interface{}, reqID interface{}, pump *core.ProgressPump, chain []string) {
	repo := flows.NewRepository()
	// 正式入口：只运行已发布的正式版本；没有正式版本就拒绝，不退回草稿
	Instance, err := repo.GetCurrentFlowVersion(authInfo, flowId)
	if err != nil {
		if errors.Is(err, flows.ErrNoReleasedVersion) {
			writeRPCError(w, reqID, errCodeInvalidParams,
				"flow %s 尚未发布正式版本，请先在编辑页发布后再调用", flowId)
			return
		}
		// 实例不存在/查询失败：参数指向的资源不存在，对齐 MCP 的 InvalidParams(-32602)
		writeRPCError(w, reqID, errCodeInvalidParams, "get flow instance %s: %v", flowId, err)
		return
	}
	// 废弃的 flow 不可运行：status 在实体行、版本快照里没有，外部入口必须自己判一次，
	// 不能只靠「切废弃时收回端点」这个 best-effort 动作（失败只记日志）。
	if !models.IsRunnable(int(Instance.Status)) {
		writeRPCError(w, reqID, errCodeInvalidParams, "flow %s 已废弃，不可调用", Instance.Name)
		return
	}

	// 会话 id 只认 params._meta 一处（与 progressToken 同一处）。
	// 归属校验放在这里，NewFlow 里不做 —— 它还要服务「agent 调 flow 时共用调用方会话」的内部路径。
	sid := core.MetaSessionID(params)
	if sid != "" {
		// 格式与可见性一次判完（会话归属用户，owner 只记录创建者）；不存在则用该 id 建档 ——
		// 协议端点没有独立的「新建会话」接口，调用方自带 UUID 即表示首次创建。
		// 错误码按类别收敛：客户端传错是 InvalidParams，配额不足 / 存储故障另有映射。
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeFlow, Instance.ID, authInfo.ID, "",
		); serr != nil {
			writeRPCError(w, reqID, sessionErrCode(serr), "session %s: %v", sid, serr)
			return
		}
		input = core.WithSessionID(input, sid) // NewFlow 会再把它剥离出业务入参
	}
	// 调用链走保留键，NewFlow 会剥离（与 _call_depth 同一套机制），不进业务变量
	input = callchain.WithArguments(input, chain)

	execCtx, err := executors.NewFlow(Instance, input, conv.ToString(reqID), uuid.Must(uuid.NewV7()).String(), authInfo.Schema, authInfo.ID, r.Context())
	if err != nil {
		// 初始化失败：协议层 InternalError（对齐 MCP return error -> -32603）
		writeRPCError(w, reqID, errCodeInternalError, "init flow exec: %v", err)
		return
	}

	if !pump.Enabled() {
		executors.Start(execCtx)
		writeFlowResult(w, reqID, execCtx)
		return
	}

	// 订阅了进度：后台执行，主协程消费 progress/result channel 并写 SSE
	// （两个通道都按 category=text 推送；最终结果只认响应里的 result）
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
			// 后台执行结束：业务错误已双写进 execCtx.Error，统一交给 writeFlowResult 包装
			break loop
		case <-r.Context().Done():
			execCtx.CloseChans()
			return
		}
	}

	writeFlowResult(w, reqID, execCtx)
}

// writeFlowResult 写 flow 最终结果（检查 execCtx.GetError）
func writeFlowResult(w http.ResponseWriter, reqID interface{}, execCtx *flow.ExecContext) {
	if e := execCtx.GetError(); e != "" {
		writeToolError(w, reqID, e)
		return
	}
	writeResult(w, reqID, execCtx.GetResult())
}

// runAgent 执行 agent 类型目标，返回 JSON-RPC 响应。
// 客户端没订阅进度：同步执行，回单条 JSON。
// 订阅了进度：后台执行 + SSE 推 notifications/progress，最终结果仍是流里那条带 id 的响应。
// params 原样传进来只为读 _meta（会话 id 的正位），业务入参仍是 input。
func runAgent(w http.ResponseWriter, r *http.Request, authInfo *auth.AuthInfo, agentID string, params any, input map[string]interface{}, reqID interface{}, pump *core.ProgressPump, chain []string) {
	repo := agents.NewRepository()
	agentInst, err := repo.GetAgent(authInfo, agentID)
	if err != nil {
		// 实例不存在/查询失败：参数指向的资源不存在，对齐 MCP 的 InvalidParams(-32602)
		writeRPCError(w, reqID, errCodeInvalidParams, "get agent instance %s: %v", agentID, err)
		return
	}

	// 会话 id 只认 params._meta 一处（与 progressToken 同一处）。
	sid := core.MetaSessionID(params)
	// 业务入参里的同名键一律剥掉：它会变成模型可见的 {{args.session_id}}。
	// 这里不再拿它当会话开关，只是防止误传把控制字段漏进提示词。
	input = core.StripSessionID(input)

	// 会话归属：给了 session_id 就必须是这个用户的会话（与 HTTP chat 同一口径）。
	// owner 只记录创建者：父子 agent 共用同一 session 时 owner 必然对不上，用 owner 收口
	// 会把 call_agent 的合法级联整体拒掉，所以按 user 收口；不存在则用该 id 建档。
	if sid != "" {
		if _, serr := sessions.ResolveOrCreateForUser(
			authInfo.Schema, sid, models.SessionOwnerTypeAgent, agentInst.ID, authInfo.ID, "",
		); serr != nil {
			writeRPCError(w, reqID, sessionErrCode(serr), "session %s: %v", sid, serr)
			return
		}
	}

	// user_id 恒定注入：verifyAPIKey / verifyOAuth 只放行绑定了用户的凭证（见 auth.go），
	// RPC 没有租户级/无主身份 —— 运行域隔离、会话归属与会话记忆全都以它为归属依据。
	// session_id 由调用方自带（已从业务入参剥离）：给了就是会话调用，不给则单轮、不记记忆。
	agentArguments := map[string]interface{}{
		"args":      input,
		"user_id":   authInfo.ID,
		"_run_type": "rpc",
	}
	if sid != "" {
		agentArguments["session_id"] = sid
	}
	// 链放顶层保留键，不放 args 内部：args 是模型可见的业务入参，
	// 控制字段混进去会变成 {{args._call_chain}}。
	if len(chain) > 0 {
		agentArguments[callchain.ArgumentKey] = chain
	}
	// 正式入口：读已发布的当前版本。没有当前版本时 NewAgent 直接报
	// ErrNoReleasedVersion —— 不退回草稿，否则「正式运行」名不副实。
	agent.SetReleaseMode(agentArguments)
	inst, err := agent.NewAgent(agentInst, authInfo.Schema, agentArguments)
	if err != nil {
		// 初始化失败：协议层 InternalError（对齐 MCP return error -> -32603）
		writeRPCError(w, reqID, errCodeInternalError, "init agent: %v", err)
		return
	}
	if inst == nil {
		// 实例不支持/不存在：参数指向的资源不存在，对齐 MCP 的 InvalidParams(-32602)
		writeRPCError(w, reqID, errCodeInvalidParams, "agent %s not supported or not found", agentID)
		return
	}
	defer inst.Close()

	if !pump.Enabled() {
		// 用请求 ctx：客户端关连接 / 断流即在同一条 ctx 上取消本次运行，
		// 不必等它把 LLM 轮次和沙箱调用跑完（与 HTTP 试聊同一口径）。
		if runErr := inst.Stream(r.Context()); runErr != nil {
			// 客户端断开导致的终止：连接已经没了，别再往上写一条 500；
			// 终止原因已随本轮落库（recordSessionRound 的 status / error）。
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

	// 订阅了进度：后台执行，主协程消费「结构化事件」channel 并写 SSE。
	// 思考（EventThinking）与工具调用只在这里出现，早先的纯文本进度通道已删除。
	writeSSEHeaders(w)
	errCh := make(chan error, 1)
	go func() {
		errCh <- inst.Stream(r.Context())
		inst.CloseChans()
	}()

	eventCh := inst.GetEventChan()
	// 客户端断开后不再写 SSE，但仍把事件通道读干净：运行与本次请求共用同一条 ctx，
	// 已随断开取消 —— 等它自行收尾（落库、杀常驻进程）再返回。直接 return 会让
	// defer inst.Close() 在 Stream 还没退出时跑，与它抢同一个一次性工作目录。
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
				continue // 已断开：只读不写
			}
			if item, ok := ev.ToProgress(); ok {
				notifyProgress(w, pump, item.Category, xjson.SanitizeJSON(item.Message), item.Meta)
			}
		case <-errCh:
			// 后台执行结束：业务错误已双写进 inst.Error，统一交给 writeAgentResult 包装
			break loop
		case <-gone:
			gone = nil // 置 nil 后该分支永久阻塞，避免空转
		}
	}

	if r.Context().Err() != nil {
		return // 客户端已断开：结果无人接收，也不再写响应
	}
	writeAgentResult(w, reqID, inst)
}

// notifyProgress 写一条 notifications/progress（SSE 帧）。
//
// 只有客户端订阅过（pump 拿到 token）才会走到这里；message 与 extra 都为空时不发，
// 避免推空帧 —— 也因此不会白白消耗一个序号。序号由 pump 单调分配（规范 MUST）。
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

// withCategory 把 category 合并进 _meta（不改动传入的 map）。
func withCategory(category string, extra map[string]any) map[string]any {
	meta := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		meta[k] = v
	}
	meta["category"] = category
	return meta
}

// writeAgentResult 写 agent 最终结果（检查 inst.GetError）
func writeAgentResult(w http.ResponseWriter, reqID interface{}, inst core.Agent) {
	if e := inst.GetError(); e != "" {
		writeToolError(w, reqID, e)
		return
	}
	writeResult(w, reqID, inst.GetResult())
}

// extractProgressToken 取客户端在 params._meta.progressToken 里表达的订阅意愿（与 MCP 同一位置）。
//
// 返回 nil 表示没订阅 —— 这时一帧进度都不能发：规范要求 notifications/progress
// 只引用活跃请求里提供过的 token。类型不合规（非 string / 非整数）同样视为未提供。
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

// writeSSEHeaders 设置 SSE 响应头
func writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// writeSSE 以 SSE 事件格式写一条消息
func writeSSE(w http.ResponseWriter, payload interface{}) {
	data, _ := json.Marshal(payload)
	w.Write([]byte("event: message\ndata: " + string(data) + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeResult 写最终结果（流式场景用 SSE，非流式用单条 JSON）
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

// writeToolError 写业务错误结果（isError 标记；流式用 SSE，非流式用单条 JSON）
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

// isSSE 判断当前响应是否为流式（已写 SSE 头）
func isSSE(w http.ResponseWriter) bool {
	return w.Header().Get("Content-Type") == "text/event-stream"
}

// writeVersionError 协议版本不支持时返回结构化错误（对齐 MCP）。
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

// sessionErrCode 把会话解析错误映射成 JSON-RPC 错误码。
//
// 缺失 / 非 UUID / 不属于本实体，都是调用方给错了参数 → InvalidParams；
// 只有存储层故障才是 InternalError。原先一律 InternalError，
// 客户端因此无从分辨「我传错了」和「服务端坏了」。
func sessionErrCode(err error) int {
	switch {
	case errors.Is(err, sessions.ErrSessionRequired),
		errors.Is(err, sessions.ErrSessionInvalid),
		errors.Is(err, sessions.ErrSessionForbidden):
		return errCodeInvalidParams
	case errors.As(err, new(*quota.ExceededError)):
		// 会话不存在 + 自动建会话时撞上配额上限：这是可解释的业务拒绝，不是服务端故障
		return errCodeQuotaExceeded
	default:
		return errCodeInternalError
	}
}

// writeRPCError 写协议层错误：HTTP 始终 200，错误置于 JSON-RPC 响应体的 error 字段，
// 与 MCP server 的工具错误返回方式对齐（MCP 中 return error 即被 SDK 包成协议 error）。
// 仅用于「已解析为合法 JSON-RPC 请求之后」的业务错误；传输层错误仍用 xhttpErr 给 HTTP 状态码。
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

// xhttpErr 写简单 JSON 错误（传输层错误：HTTP 状态码 + 协议体 error 并存）
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
