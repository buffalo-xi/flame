package executors

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flame/nexus/service/flow/core"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var _ Executor = (*HttpExecutor)(nil)

// HTTP 节点固定输出变量（不论有没有 handler，一律由执行器写入）
var httpFixedOutputVars = map[string]string{
	"mode":            "string",
	"status":          "number",
	"headers":         "string",
	"body":            "string",
	"sse_events":      "array[string]",
	"stream_finished": "boolean",
}

// httpReservedOutputVars 脚本不可写的固定变量集合（与 mcp/rpc/llm 同口径）。
var httpReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(httpFixedOutputVars))
	for k := range httpFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

// SSE 事件结构
type SSEEvent struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	Data  string `json:"data"`
}

type HttpExecutor struct{}

func (e *HttpExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. 解析 http 配置
	httpCfg, ok := node.Data.Config["http"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "http config not found")
	}

	// 2. 初始化输出变量（固定变量兜底补齐：老节点漏配时也要有变量可写）
	var outputs map[string]interface{}
	if raw, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		outputs = raw
	}
	outputs = ensureFixedOutputs(outputs, httpFixedOutputVars)
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)

	// 3. 解析输入变量
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. 构建请求
	req, reqErr := buildHTTPRequest(httpCfg, tmplCtx)
	if reqErr != nil {
		return failf(nr, flow.ErrCodeConfig, reqErr.Error())
	}

	// 记录请求信息（渲染后的实际 url/method/headers）到 HttpResult
	nr.HttpResult = &flow.HttpResult{
		URL:     req.URL.String(),
		Method:  req.Method,
		Headers: req.Header,
	}

	// 5. 解析超时
	reqTimeout := resolveTimeout(httpCfg)

	// 6. 发送请求
	httpCtx := ctx.StdCtx
	if reqTimeout > 0 {
		var cancel context.CancelFunc
		httpCtx, cancel = context.WithTimeout(ctx.StdCtx, time.Duration(reqTimeout)*time.Second)
		defer cancel()
	}

	resp, doErr := doHTTPRequest(httpCtx, req)
	if doErr != nil {
		return failf(nr, flow.ErrCodeConnection, doErr.Error())
	}
	defer resp.Body.Close()

	// 7. 确定响应模式
	mode, modeErr := determineResponseMode(httpCfg, resp)
	if modeErr != nil {
		return failf(nr, flow.ErrCodeConfig, modeErr.Error())
	}

	// 存 mode 输出变量（两种响应模式都写，handler 内部不再重复写）
	if err := nr.SetOutputVarWithFire("mode", mode); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write mode: %v", err))
	}

	// 记录响应状态码到 HttpResult
	if nr.HttpResult != nil {
		nr.HttpResult.Status = resp.StatusCode
	}

	// 8. 解析 handler 配置
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(httpCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}

	// 9. 按模式处理响应
	switch mode {
	case "sse_stream":
		return e.handleSSEStream(resp, node, ctx, processID, nr, inputArgs, handlerScript, handlerLang, handlerTimeout)
	default: // "complete"
		return e.handleComplete(resp, node, ctx, processID, nr, inputArgs, handlerScript, handlerLang, handlerTimeout)
	}
}

// handleComplete 处理非流式响应
func (e *HttpExecutor) handleComplete(
	resp *http.Response,
	node *flow.Node, ctx *flow.ExecContext, processID string,
	nr *flow.NodeResult, inputArgs map[string]interface{},
	handlerScript, handlerLang string, handlerTimeout int,
) error {
	// 读取全部 body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("failed to read the response body: %v", err))
	}

	bodyStr := bodyToString(bodyBytes, resp.Header.Get("Content-Type"))

	// 序列化 headers
	headersJSON, _ := json.Marshal(resp.Header)
	headersStr := string(headersJSON)

	// 1. 固定输出变量一律由执行器写（有没有 handler 都一样）；mode 已在 Execute 里写过
	if err := nr.SetOutputVarWithFire("status", float64(resp.StatusCode)); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write status: %v", err))
	}
	if err := nr.SetOutputVarWithFire("headers", headersStr); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write headers: %v", err))
	}
	if err := nr.SetOutputVarWithFire("body", bodyStr); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write body: %v", err))
	}
	if err := nr.SetOutputVarWithFire("stream_finished", true); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write stream_finished: %v", err))
	}

	// 2. 脚本可选后处理：return 固定变量会被拦下并记日志，脚本只写自定义变量
	if handlerScript != "" {
		eventData := map[string]interface{}{
			"mode":    "complete",
			"status":  float64(resp.StatusCode),
			"headers": resp.Header,
			"body":    bodyStr,
			"done":    true,
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

// handleSSEStream 处理 SSE 流式响应
func (e *HttpExecutor) handleSSEStream(
	resp *http.Response,
	node *flow.Node, ctx *flow.ExecContext, processID string,
	nr *flow.NodeResult, inputArgs map[string]interface{},
	handlerScript, handlerLang string, handlerTimeout int,
) error {
	// 响应元数据由执行器写；mode 已在 Execute 里写过，这里不再重复
	headersJSON, _ := json.Marshal(resp.Header)
	if err := nr.SetOutputVarWithFire("status", float64(resp.StatusCode)); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write status: %v", err))
	}
	if err := nr.SetOutputVarWithFire("headers", string(headersJSON)); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write headers: %v", err))
	}

	scanner := bufio.NewScanner(resp.Body)
	// 增大 buffer 以支持长 data 行
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentEvent SSEEvent
	eventCount := 0
	// 事件无条件收集：有没有 handler 都要给下游完整的 sse_events
	var collectedEvents []map[string]interface{}

	// runEventHandler 逐帧脚本后处理（只写自定义变量）
	runEventHandler := func(ev SSEEvent) error {
		if handlerScript == "" {
			return nil
		}
		eventData := map[string]interface{}{
			"mode":  "sse_stream",
			"event": ev.toMap(),
		}
		scriptInput := mergeInputs(nr, inputArgs, eventData)
		if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
			nr.Finish(flow.StatusFailed, err.ErrCode, err.ErrMsg)
			return err
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()

		// 空行标志一个事件的结束
		if line == "" {
			if currentEvent.Data != "" || currentEvent.Event != "" {
				eventCount++
				collectedEvents = append(collectedEvents, currentEvent.toMap())
				if err := runEventHandler(currentEvent); err != nil {
					return err
				}
			}
			currentEvent = SSEEvent{}
			continue
		}

		// 解析 SSE 行
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = trimLeadingSpace(data)
			if currentEvent.Data != "" {
				currentEvent.Data += "\n" + data
			} else {
				currentEvent.Data = data
			}
		} else if strings.HasPrefix(line, "event:") {
			currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "id:") {
			currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		// 忽略注释行（以 : 开头）和其他行
	}

	// 处理最后一个事件（如果没有空行结尾）
	if currentEvent.Data != "" || currentEvent.Event != "" {
		eventCount++
		collectedEvents = append(collectedEvents, currentEvent.toMap())
		if err := runEventHandler(currentEvent); err != nil {
			return err
		}
	}

	// 扫描错误
	if err := scanner.Err(); err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("SSE read error: %v", err))
	}

	// 强制 SSE 但没有收到任何 data 帧 -> 报错
	if eventCount == 0 {
		return failf(nr, flow.ErrCodeConfig, "response_mode is forced to sse_stream but no SSE data frame arrived")
	}

	if err := nr.SetOutputVar("sse_events", collectedEvents); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write sse_events: %v", err))
	}

	if handlerScript != "" {
		// 流关闭时额外触发一次收尾脚本
		eventData := map[string]interface{}{
			"mode":        "sse_stream",
			"done":        true,
			"event_count": float64(eventCount),
		}
		scriptInput := mergeInputs(nr, inputArgs, eventData)
		if err := e.executeHandlerScript(ctx, nr, handlerLang, handlerScript, scriptInput, handlerTimeout); err != nil {
			nr.Finish(flow.StatusFailed, err.ErrCode, err.ErrMsg)
			return err
		}
	}

	if err := nr.SetOutputVarWithFire("stream_finished", true); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write stream_finished: %v", err))
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// executeHandlerScript 执行 handler 脚本并处理结果
func (e *HttpExecutor) executeHandlerScript(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	lang, script string, inputs map[string]interface{}, timeout int,
) *scriptExecError {
	scriptResult, err := ExecuteScript(ctx.StdCtx, lang, script, inputs, nr, timeout)
	if err != nil {
		return &scriptExecError{
			ErrCode: scriptErrCode(err),
			ErrMsg:  fmt.Sprintf("failed to run the handler script: %v", err),
		}
	}

	// 逐帧调用：return 的 string 字段追加、非 string 覆盖；
	// 固定变量（mode/status/headers/body/sse_events/stream_finished）不许脚本写。
	if scriptResult != nil {
		if err := applyHandlerAppend(nr, scriptResult, httpReservedOutputVars); err != nil {
			return &scriptExecError{
				ErrCode: flow.ErrCodeVar,
				ErrMsg:  fmt.Sprintf("failed to write the handler script result: %v", err),
			}
		}
	}
	return nil
}

type scriptExecError struct {
	ErrCode string
	ErrMsg  string
}

func (e *scriptExecError) Error() string {
	return e.ErrMsg
}

func (e *HttpExecutor) Capabilities() NodeCapabilities {
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

func NewHttpExecutor() *HttpExecutor {
	return &HttpExecutor{}
}

func init() {
	RegisterExecutor("http", NewHttpExecutor())
}

// resolveInputVars 解析 config.inputs 中的输入变量，并统一写入 nr.InputVars
func resolveInputVars(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) (map[string]interface{}, error) {
	cfg := node.Data.Config
	inputs, exist := cfg["inputs"].(map[string]interface{})
	if !exist {
		empty := make(map[string]interface{})
		nr.SetInputVars(empty)
		return empty, nil
	}
	vars, err := ctx.GetVariables(inputs, processID)
	if err != nil {
		return nil, err
	}
	nr.SetInputVars(vars)
	return vars, nil
}

// buildHTTPRequest 构建 HTTP 请求
func buildHTTPRequest(httpCfg map[string]interface{}, tmplCtx map[string]interface{}) (*http.Request, error) {
	// URL
	rawURL, _ := httpCfg["url"].(string)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	renderedURL, err := RenderTemplate(rawURL, tmplCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to render the url template: %w", err)
	}

	// 解析并追加 query params
	parsedURL, err := url.Parse(strings.TrimSpace(renderedURL))
	if err != nil {
		return nil, fmt.Errorf("failed to parse the url: %w", err)
	}

	params := parseKeyValuePairs(httpCfg, "params", tmplCtx)
	if len(params) > 0 {
		q := parsedURL.Query()
		for _, p := range params {
			q.Add(p.Key, p.Value)
		}
		parsedURL.RawQuery = q.Encode()
	}

	// Body
	bodyType, _ := httpCfg["body_type"].(string)
	if bodyType == "" {
		bodyType = "json"
	}
	bodyStr, _ := httpCfg["body"].(string)
	var reqBody io.Reader
	if bodyStr != "" {
		renderedBody, err := RenderTemplate(bodyStr, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to render the body template: %w", err)
		}
		reqBody = strings.NewReader(renderedBody)
	}

	// Method
	method, _ := httpCfg["method"].(string)
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequest(method, parsedURL.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build the request: %w", err)
	}

	// Headers
	headers := parseKeyValuePairs(httpCfg, "headers", tmplCtx)
	for _, h := range headers {
		req.Header.Set(h.Key, h.Value)
	}

	// 根据 body_type 设置 Content-Type（如果没有显式设置）
	if bodyStr != "" && req.Header.Get("Content-Type") == "" {
		switch bodyType {
		case "json":
			req.Header.Set("Content-Type", "application/json")
		case "form":
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}

	return req, nil
}

// KV 键值对
type KV struct {
	Key   string
	Value string
}

// parseKeyValuePairs 解析 headers/params 等 key-value 配置
func parseKeyValuePairs(cfg map[string]interface{}, field string, tmplCtx map[string]interface{}) []KV {
	raw, ok := cfg[field].([]interface{})
	if !ok {
		return nil
	}
	result := make([]KV, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		val, _ := m["value"].(string)
		if key == "" {
			continue
		}
		renderedVal, err := RenderTemplate(val, tmplCtx)
		if err != nil {
			renderedVal = val
		}
		result = append(result, KV{Key: key, Value: renderedVal})
	}
	return result
}

// doHTTPRequest 执行 HTTP 请求
func doHTTPRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	client := &http.Client{
		Timeout: 0, // 由 context 控制超时
	}
	return client.Do(req.WithContext(ctx))
}

// determineResponseMode 确定响应模式
func determineResponseMode(httpCfg map[string]interface{}, resp *http.Response) (string, error) {
	forced, _ := httpCfg["response_mode"].(string)

	if forced != "" {
		switch forced {
		case "complete", "sse_stream":
			return forced, nil
		default:
			return "", fmt.Errorf("unsupported response_mode: %s (expected complete or sse_stream)", forced)
		}
	}

	// 自动判断：仅当 Content-Type 包含 text/event-stream 时识别为 SSE
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/event-stream" {
		return "sse_stream", nil
	}

	return "complete", nil
}

// bodyToString 将响应 body 转为字符串，非文本类型做 base64 编码
func bodyToString(body []byte, contentType string) string {
	if isTextContent(contentType) {
		return string(body)
	}
	return base64.StdEncoding.EncodeToString(body)
}

// isTextContent 判断 Content-Type 是否为文本类型
func isTextContent(contentType string) bool {
	if contentType == "" {
		return true // 默认视为文本
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case mediaType == "application/json":
		return true
	case mediaType == "application/xml":
		return true
	case strings.Contains(mediaType, "+json"):
		return true
	case strings.Contains(mediaType, "+xml"):
		return true
	case strings.HasPrefix(mediaType, "application/javascript"):
		return true
	default:
		return false
	}
}

// resolveTimeout 解析请求超时；未启用时返回 -1，由运行上下文控制。
func resolveTimeout(httpCfg map[string]interface{}) int {
	return parseTimeoutConfig(httpCfg["timeout"])
}

// parseHandler 解析 handler 配置。
// handler 脚本仅支持 lua/python/javascript，template 为纯渲染（无副作用、无 callbacks），
// 不适合 handler 语义，故显式报错。
func parseHandler(httpCfg map[string]interface{}, ctx *flow.ExecContext) (script, lang string, timeout int, err error) {
	handlerCfg, ok := httpCfg["handler"].(map[string]interface{})
	if !ok {
		return
	}
	script, _ = handlerCfg["script"].(string)
	lang, _ = handlerCfg["lang"].(string)
	if lang == "" {
		lang = "lua"
	}
	if lang == "template" {
		return "", "", 0, fmt.Errorf("the handler does not support the template language")
	}

	timeout = -1
	if tCfg, ok := handlerCfg["timeout"].(map[string]interface{}); ok {
		if enabled, _ := tCfg["enabled"].(bool); enabled {
			if sec, ok := tCfg["seconds"].(float64); ok {
				timeout = int(sec)
			}
		}
	}
	timeout = ctx.ResolveScriptTimeout(lang, timeout)
	return
}

// mergeInputs 合并 input args 和事件数据，构建脚本输入
func mergeInputs(nr *flow.NodeResult, inputArgs map[string]interface{}, eventData map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(inputArgs)+len(eventData))
	for k, v := range inputArgs {
		merged[k] = v
	}
	for k, v := range eventData {
		merged[k] = v
	}
	return merged
}

func (ev *SSEEvent) toMap() map[string]interface{} {
	m := make(map[string]interface{})
	if ev.ID != "" {
		m["id"] = ev.ID
	}
	if ev.Event != "" {
		m["event"] = ev.Event
	}
	m["data"] = ev.Data
	return m
}

// trimLeadingSpace 去除前导空格（最多一个）
func trimLeadingSpace(s string) string {
	if len(s) > 0 && s[0] == ' ' {
		return s[1:]
	}
	return s
}
