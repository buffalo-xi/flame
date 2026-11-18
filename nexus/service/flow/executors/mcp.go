package executors

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/mcp/client"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	mcpconnrepo "flame/nexus/shared/repository/mcpconnections"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var _ Executor = (*McpExecutor)(nil)

// MCP 节点固定输出变量
var mcpFixedOutputVars = map[string]string{
	"result":   "string",        // 调用结果（flattenResult.Content）
	"progress": "array[string]", // 进度通知数组（每条完整原始 JSON 字符串）
	"error":    "string",        // 调用出错信息
}

// mcpReservedOutputVars 脚本不可写的固定变量集合（与 rpcReservedOutputVars 同口径）。
// 这三个变量由执行器无条件写入（有没有 handler 都一样），脚本只能处理自定义变量。
var mcpReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(mcpFixedOutputVars))
	for k := range mcpFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

type McpExecutor struct{}

// progressCollector 收集 progress 通知的原始 JSON 字符串。
// onProgress 在 SDK 后台读协程中并发回调，故 append 需加锁。
type progressCollector struct {
	mu   sync.Mutex
	list []string
}

func (c *progressCollector) append(raw string) {
	c.mu.Lock()
	c.list = append(c.list, raw)
	c.mu.Unlock()
}

func (c *progressCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.list))
	copy(out, c.list)
	return out
}

func (e *McpExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. 解析 mcp 配置
	mcpCfg, ok := node.Data.Config["mcp"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "mcp config not found")
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

	// 4. 解析 connection_id 与 tool_name
	connectionID, _ := mcpCfg["connection_id"].(string)
	if strings.TrimSpace(connectionID) == "" {
		return failf(nr, flow.ErrCodeConfig, "mcp connection_id 不能为空")
	}
	toolName, _ := mcpCfg["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return failf(nr, flow.ErrCodeConfig, "mcp tool_name 不能为空")
	}

	// 5. 解析 handler 配置（校验非 template）
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(mcpCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 6. 渲染 arguments 模板
	arguments, argErr := renderMcpArguments(mcpCfg, tmplCtx)
	if argErr != nil {
		return failf(nr, flow.ErrCodeConfig, argErr.Error())
	}

	// 是否订阅进度：节点级开关，默认关闭。
	// 关闭时走不带 progressToken 的同步调用 —— progress 输出变量恒为空数组，
	// 也不会让服务端把 agent 的思考/文本全量推一遍（每次调用都过一遍网，代价不小）。
	wantProgress, _ := mcpCfg["progress"].(bool)

	// 7. 查询连接
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	conn, connErr := mcpconnrepo.NewRepository().GetByID(authInfo, connectionID)
	if connErr != nil {
		return failf(nr, flow.ErrCodeNotFound, fmt.Sprintf("MCP 连接不存在: %v", connErr))
	}
	if !conn.IsActive {
		return failf(nr, flow.ErrCodeConfig, "MCP 连接未启用")
	}

	// 8. 收集器 + onProgress 回调
	coll := &progressCollector{}
	onProgress := func(p *mcp.ProgressNotificationParams) {
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
				"progress":     progressToMap(p),
				"progress_raw": rawStr,
			}
			scriptInput := mergeInputs(nr, inputArgs, eventData)
			if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
				// 逐条 handler 执行错误：记录日志但不中断调用（progress 属基础设施输出）
				nr.AddLogf("progress handler 执行失败: %s", err.ErrMsg)
			}
		}
	}

	// 9. 调用工具：订阅进度时带 token 并发接收 progress，否则同步一次拿结果
	var (
		res     *client.ToolResult
		callErr error
	)
	// 祖先链经 ctx 交给 client：MCP 节点也可能调回本系统的 /service/mcp/{schema}，
	// 不带链的话对端看不到「自己已在祖先里」。
	stdCtx := callchain.With(ctx.StdCtx, ctx.CallChain)
	if wantProgress {
		res, callErr = client.InvokeWithProgress(stdCtx, conn, toolName, arguments, onProgress)
	} else {
		res, callErr = client.InvokeSync(stdCtx, conn, toolName, arguments)
	}

	// 10. CallTool 返回后：写 progress 数组（定稿，覆盖语义）
	progressSnapshot := coll.snapshot()
	if progressSnapshot != nil {
		nr.SetOutputVarWithFire("progress", progressSnapshot)
	}

	if callErr != nil {
		code := flow.ErrCodeOther
		var ie *client.InvokeError
		if errors.As(callErr, &ie) {
			code = ie.Code
		}
		return failf(nr, code, callErr.Error())
	}

	// 11. 收尾（与 rpc.finishRpcResult 同口径：固定变量一律由执行器写）
	content := ""
	isError := false
	var structured interface{}
	if res != nil {
		content = res.Content
		isError = res.IsError
		structured = res.Structured
	}
	return e.finishMcpResult(ctx, nr, inputArgs, hasHandler, handlerLang, handlerScript, handlerTimeout,
		content, isError, structured, progressSnapshot)
}

// finishMcpResult 收尾：固定输出变量一律由执行器写（与 rpc.finishRpcResult 同口径），
// 有没有 handler 都一样；handler 只做可选后处理，且只能写自定义输出变量。
//
// 这条策略是从 RPC 抄过来的，原因也一样：把 result/error 的写入义务推给 handler 配置，
// 脚本一旦忘了写就是永久空值，而且「这个变量的值是远端返回的还是脚本编的」在界面上看不出来。
//
// isError=true 为工具自身的业务错误（写 error 变量，节点仍 success）；
// 调用异常已在调用方处理为 failed。
func (e *McpExecutor) finishMcpResult(
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
	// 1. 固定变量固定由节点写
	if isError {
		nr.SetOutputVarWithFire("error", result)
	} else {
		nr.SetOutputVarWithFire("result", result)
	}

	// 2. 脚本可选收尾后处理（写自定义变量）；固定变量已在 mcpReservedOutputVars 中拦下
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

// progressToMap 将 ProgressNotificationParams 转为结构化 map（供脚本 eventData）。
// meta 里带 category（thinking / text / tool_call / tool_result），
// handler 可据此分流；与 rpcProgressToMap 保持同一口径。
func progressToMap(p *mcp.ProgressNotificationParams) map[string]interface{} {
	m := map[string]interface{}{
		"progress": p.Progress,
		"total":    p.Total,
	}
	if p.Message != "" {
		m["message"] = p.Message
	}
	if p.Meta != nil {
		m["meta"] = p.Meta
	}
	if p.ProgressToken != nil {
		m["progress_token"] = p.ProgressToken
	}
	return m
}

// renderMcpArguments 渲染 arguments 模板。arguments 配置为 JSON 对象字符串，
// 字符串值支持 {{.input.xxx}} 模板；渲染后整体 JSON 解码为 map[string]any。
func renderMcpArguments(mcpCfg map[string]interface{}, tmplCtx map[string]interface{}) (map[string]interface{}, error) {
	raw, exists := mcpCfg["arguments"]
	if !exists || raw == nil {
		return map[string]interface{}{}, nil
	}

	// 若已是对象（map），递归渲染字符串值
	if obj, ok := raw.(map[string]interface{}); ok {
		return renderMcpArgumentsMap(obj, tmplCtx)
	}

	// 若为 JSON 字符串，先模板渲染再解码
	if str, ok := raw.(string); ok {
		rendered, err := RenderTemplate(str, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("arguments 模板渲染失败: %w", err)
		}
		if strings.TrimSpace(rendered) == "" {
			return map[string]interface{}{}, nil
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(rendered), &out); err != nil {
			return nil, fmt.Errorf("arguments JSON 解码失败: %w", err)
		}
		return out, nil
	}

	return nil, fmt.Errorf("arguments 配置格式非法，应为 JSON 对象或对象字符串")
}

// renderMcpArgumentsMap 递归渲染对象内字符串值的模板。
func renderMcpArgumentsMap(obj map[string]interface{}, tmplCtx map[string]interface{}) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(obj))
	for k, v := range obj {
		rendered, err := renderMcpValue(v, tmplCtx)
		if err != nil {
			return nil, err
		}
		out[k] = rendered
	}
	return out, nil
}

func renderMcpValue(v interface{}, tmplCtx map[string]interface{}) (interface{}, error) {
	switch val := v.(type) {
	case string:
		return RenderTemplate(val, tmplCtx)
	case map[string]interface{}:
		return renderMcpArgumentsMap(val, tmplCtx)
	case []interface{}:
		arr := make([]interface{}, len(val))
		for i, item := range val {
			r, err := renderMcpValue(item, tmplCtx)
			if err != nil {
				return nil, err
			}
			arr[i] = r
		}
		return arr, nil
	default:
		return val, nil
	}
}

// executeHandlerScript 执行 handler 脚本并处理结果（与 HTTP/LLM 一致）。
func (e *McpExecutor) executeHandlerScript(
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

	// 逐 chunk 调用：return 的 string 字段追加（流式累积）、非 string 覆盖（快照）
	// 固定变量（result/progress/error）不允许脚本写，由执行器自己写。
	if scriptResult != nil {
		if err := applyHandlerAppend(nr, scriptResult, mcpReservedOutputVars); err != nil {
			return &scriptExecError{
				ErrCode: flow.ErrCodeVar,
				ErrMsg:  fmt.Sprintf("handler 脚本返回值写入失败: %v", err),
			}
		}
	}
	return nil
}

func (e *McpExecutor) Capabilities() NodeCapabilities {
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

func NewMcpExecutor() *McpExecutor {
	return &McpExecutor{}
}

func init() {
	RegisterExecutor("mcp", NewMcpExecutor())
}
