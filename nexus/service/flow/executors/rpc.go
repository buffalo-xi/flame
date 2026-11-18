package executors

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/rpc/client"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	rpcconnrepo "flame/nexus/shared/repository/rpcconnections"
)

var _ Executor = (*RpcExecutor)(nil)

// RPC 节点固定输出变量
var rpcFixedOutputVars = map[string]string{
	"result":   "string",        // 调用结果（flattenResult.Content）
	"progress": "array[string]", // 进度通知数组（每条完整原始 JSON 字符串）
	"error":    "string",        // 调用出错信息
}

// rpcReservedOutputVars 脚本不可写的固定变量集合。
// 这三个变量由执行器无条件写入（有没有 handler 都一样），脚本只能处理自定义变量。
var rpcReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(rpcFixedOutputVars))
	for k := range rpcFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

type RpcExecutor struct{}

// rpcProgressCollector 收集 progress 通知的原始 JSON 字符串。
// onProgress 在 stdio 读取循环 / HTTP SSE 解析中与阻塞在调用上的主协程并发回调，故 append 需加锁。
type rpcProgressCollector struct {
	mu   sync.Mutex
	list []string
}

func (c *rpcProgressCollector) append(raw string) {
	c.mu.Lock()
	c.list = append(c.list, raw)
	c.mu.Unlock()
}

func (c *rpcProgressCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.list))
	copy(out, c.list)
	return out
}

func (e *RpcExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. 解析 rpc 配置
	rpcCfg, ok := node.Data.Config["rpc"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "rpc config not found")
	}

	// 2. 初始化输出变量
	if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// 3. 解析输入变量并构建模板上下文
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. 解析 target_type（默认 connection 兼容旧配置）
	targetType, _ := rpcCfg["target_type"].(string)
	if targetType == "" {
		targetType = "connection"
	}

	// 5. 解析 handler 配置（校验非 template）
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(rpcCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 6. 渲染 arguments 模板（复用 MCP 的通用 arguments 渲染逻辑）
	arguments, argErr := renderMcpArguments(rpcCfg, tmplCtx)
	if argErr != nil {
		return failf(nr, flow.ErrCodeConfig, argErr.Error())
	}

	// 是否订阅进度：节点级开关，默认关闭。
	// 关闭时两条路径都走同步语义 —— 进程内直调不消费事件通道、外部连接不带 progressToken，
	// progress 输出变量恒为空数组。开启后才逐条接收（代价是每次调用都要过一遍全量事件）。
	wantProgress, _ := rpcCfg["progress"].(bool)

	if targetType == "endpoint" {
		return e.executeEndpoint(ctx, nr, inputArgs, rpcCfg, arguments, wantProgress, hasHandler, handlerLang, handlerScript, handlerTimeout)
	}

	// ===== 外部连接 =====
	// 7. 解析 connection_id 与 tool_name
	connectionID, _ := rpcCfg["connection_id"].(string)
	if strings.TrimSpace(connectionID) == "" {
		return failf(nr, flow.ErrCodeConfig, "rpc connection_id 不能为空")
	}
	toolName, _ := rpcCfg["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return failf(nr, flow.ErrCodeConfig, "rpc tool_name 不能为空")
	}

	// 8. 查询连接
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	conn, connErr := rpcconnrepo.NewRepository().GetByID(authInfo, connectionID)
	if connErr != nil {
		return failf(nr, flow.ErrCodeNotFound, fmt.Sprintf("RPC 连接不存在: %v", connErr))
	}
	if !conn.IsActive {
		return failf(nr, flow.ErrCodeConfig, "RPC 连接未启用")
	}

	// 9. 收集器 + onProgress 回调
	coll := &rpcProgressCollector{}
	onProgress := func(p *client.ProgressNotificationParams) {
		if p == nil {
			return
		}
		raw, mErr := json.Marshal(p)
		if mErr != nil {
			return
		}
		rawStr := string(raw)
		coll.append(rawStr)

		if hasHandler {
			eventData := map[string]interface{}{
				"progress":     rpcProgressToMap(p),
				"progress_raw": rawStr,
			}
			scriptInput := mergeInputs(nr, inputArgs, eventData)
			if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
				// 逐条 handler 执行错误：记录日志但不中断调用（progress 属基础设施输出）
				nr.AddLogf("progress handler 执行失败: %s", err.ErrMsg)
			}
		}
	}

	// 10. 调用工具：订阅进度时带 token 并发接收 progress，否则同步一次拿结果
	var (
		res     *client.ToolResult
		callErr error
	)
	// 祖先链经 ctx 交给 client（出站只带不追加，追加发生在对端即将执行实体时）
	stdCtx := callchain.With(ctx.StdCtx, ctx.CallChain)
	if wantProgress {
		res, callErr = client.InvokeWithProgress(stdCtx, conn, toolName, arguments, onProgress)
	} else {
		res, callErr = client.Invoke(stdCtx, conn, toolName, arguments)
	}

	// 11. 调用返回后：写 progress 数组（定稿，覆盖语义）
	if snap := coll.snapshot(); snap != nil {
		nr.SetOutputVarWithFire("progress", snap)
	}

	if callErr != nil {
		code := flow.ErrCodeOther
		var ie *client.InvokeError
		if errors.As(callErr, &ie) {
			code = ie.Code
		}
		return failf(nr, code, callErr.Error())
	}

	// 12. 收尾
	content := ""
	isError := false
	structured := interface{}(nil)
	if res != nil {
		content = res.Content
		isError = res.IsError
		structured = res.Structured
	}
	return e.finishRpcResult(ctx, nr, inputArgs, hasHandler, handlerLang, handlerScript, handlerTimeout, content, isError, structured, coll.snapshot())
}

// executeEndpoint 本租户端点直调：查 svc_endpoints（protocol=rpc，不校验 exposed）→
// 进程内执行 → 流式收集 progress → 收尾。
func (e *RpcExecutor) executeEndpoint(
	ctx *flow.ExecContext,
	nr *flow.NodeResult,
	inputArgs map[string]interface{},
	rpcCfg map[string]interface{},
	arguments map[string]interface{},
	wantProgress bool,
	hasHandler bool,
	handlerLang, handlerScript string,
	handlerTimeout int,
) error {
	endpointName, _ := rpcCfg["endpoint"].(string)
	if strings.TrimSpace(endpointName) == "" {
		return failf(nr, flow.ErrCodeConfig, "rpc endpoint 未配置（应填端点名）")
	}

	coll := &rpcProgressCollector{}
	onProgress := func(p rpcEndpointProgress) {
		meta := make(map[string]interface{}, len(p.Meta)+1)
		for k, v := range p.Meta {
			meta[k] = v
		}
		meta["category"] = p.Category
		rawBytes, _ := json.Marshal(map[string]interface{}{
			"message": p.Message,
			"_meta":   meta,
		})
		rawStr := string(rawBytes)
		coll.append(rawStr)

		if hasHandler {
			eventData := map[string]interface{}{
				"progress": map[string]interface{}{
					"message": p.Message,
					"meta":    meta,
				},
				"progress_raw": rawStr,
			}
			scriptInput := mergeInputs(nr, inputArgs, eventData)
			if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
				nr.AddLogf("progress handler 执行失败: %s", err.ErrMsg)
			}
		}
	}

	authInfo := &auth.AuthInfo{Schema: ctx.Schema, ID: ctx.UserID}
	// ctx.CallDepth 是本 flow 的栈深度（顶层 0）：同一 flow 里的多个 rpc 节点读到同一个值，
	// 平级调用不累加，只有真正嵌套（子 flow 继续调端点）才递增。
	// ctx.DebugScope：调试会话里跟着走草稿；正式运行时为 nil
	result, isError, runErr := runEndpoint(ctx.StdCtx, authInfo, ctx.SessionID, ctx.CallDepth, ctx.CallChain, endpointName, arguments, wantProgress, ctx.DebugScope, onProgress)

	// 写 progress 数组定稿
	if snap := coll.snapshot(); snap != nil {
		nr.SetOutputVarWithFire("progress", snap)
	}

	if runErr != nil {
		return failf(nr, flow.ErrCodeOther, runErr.Error())
	}

	return e.finishRpcResult(ctx, nr, inputArgs, hasHandler, handlerLang, handlerScript, handlerTimeout, result, isError, nil, coll.snapshot())
}

// finishRpcResult 收尾：固定输出变量一律由执行器写（与有没有 handler 无关），
// handler 只作为可选的后处理，且只能写自定义输出变量。
// isError=true 为远端业务错误（写 error 变量，节点仍 success）；节点异常已在调用方处理为 failed。
func (e *RpcExecutor) finishRpcResult(
	ctx *flow.ExecContext,
	nr *flow.NodeResult,
	inputArgs map[string]interface{},
	hasHandler bool,
	handlerLang, handlerScript string,
	handlerTimeout int,
	result string,
	isError bool,
	structured interface{},
	progressSnapshot []string,
) error {
	// 1. 固定变量固定由节点写。以前这一步在 hasHandler 分支里被跳过，
	// 结果是"脚本忘记写 result 就永远空"，属于把义务推给配置。
	if isError {
		nr.SetOutputVarWithFire("error", result)
	} else {
		nr.SetOutputVarWithFire("result", result)
	}

	// 2. 脚本可选的收尾后处理（写自定义变量），固定变量已在 rpcReservedOutputVars 中拦下。
	if hasHandler {
		eventData := map[string]interface{}{
			"done":     true,
			"is_error": isError,
			"result":   result,
			"progress": progressSnapshot,
		}
		if structured != nil {
			eventData["structured"] = structured
		}
		scriptInput := mergeInputs(nr, inputArgs, eventData)
		if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
			nr.Finish(flow.StatusFailed, err.ErrCode, err.ErrMsg)
			return err
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// rpcProgressToMap 将 RPC ProgressNotificationParams 转为结构化 map（供脚本 eventData）。
// 完整透传 message 与 meta（含 category，取值见 agent/core 的 Category* 常量）。
func rpcProgressToMap(p *client.ProgressNotificationParams) map[string]interface{} {
	m := map[string]interface{}{
		"message": p.Message,
	}
	if p.Meta != nil {
		m["meta"] = p.Meta
	}
	return m
}

// executeHandlerScript 执行 handler 脚本并处理结果（与 HTTP/LLM/MCP 一致）。
func (e *RpcExecutor) executeHandlerScript(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	lang, script string, inputs map[string]interface{}, timeout int,
) *scriptExecError {
	scriptResult, err := ExecuteScript(ctx.StdCtx, lang, script, inputs, nr, timeout)
	if err != nil {
		return &scriptExecError{
			ErrCode: scriptErrCode(err),
			ErrMsg:  fmt.Sprintf("handler 脚本执行失败: %v", err),
		}
	}

	// 逐 chunk 调用：return 的 string 字段追加、非 string 覆盖（快照）；
	// 固定变量（result/progress/error）不允许脚本写，由执行器自己写。
	if scriptResult != nil {
		if err := applyHandlerAppend(nr, scriptResult, rpcReservedOutputVars); err != nil {
			return &scriptExecError{
				ErrCode: flow.ErrCodeVar,
				ErrMsg:  fmt.Sprintf("handler 脚本返回值写入失败: %v", err),
			}
		}
	}
	return nil
}

func (e *RpcExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{
			flow.ErrCodeConfig,
			flow.ErrCodeConnection,
			flow.ErrCodeTimeout,
			flow.ErrCodeAuth,
			flow.ErrCodeNotFound,
			flow.ErrCodeScript,
			flow.ErrCodeBusy,
			flow.ErrCodeVar,
			flow.ErrCodeOther,
		},
		Actions: []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost: true,
	}
}

func NewRpcExecutor() *RpcExecutor {
	return &RpcExecutor{}
}

func init() {
	RegisterExecutor("rpc", NewRpcExecutor())
}
