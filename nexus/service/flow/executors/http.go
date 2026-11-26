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

// The HTTP node's fixed output variables (written by the executor whether or not there is a handler)
var httpFixedOutputVars = map[string]string{
	"mode":            "string",
	"status":          "number",
	"headers":         "string",
	"body":            "string",
	"sse_events":      "array[string]",
	"stream_finished": "boolean",
}

// httpReservedOutputVars is the set of fixed variables a script may not write (same rule as mcp/rpc/llm).
var httpReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(httpFixedOutputVars))
	for k := range httpFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

// SSEEvent is the structure of an SSE event
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

	// 1. Parse the http config
	httpCfg, ok := node.Data.Config["http"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "http config not found")
	}

	// 2. Initialise the output variables (the fixed ones are topped up: an old node must still have the
	// variables to write even when the config misses them)
	var outputs map[string]interface{}
	if raw, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		outputs = raw
	}
	outputs = ensureFixedOutputs(outputs, httpFixedOutputVars)
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)

	// 3. Resolve the input variables
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. Build the request
	req, reqErr := buildHTTPRequest(httpCfg, tmplCtx)
	if reqErr != nil {
		return failf(nr, flow.ErrCodeConfig, reqErr.Error())
	}

	// Record the request details (the rendered url/method/headers) into HttpResult
	nr.HttpResult = &flow.HttpResult{
		URL:     req.URL.String(),
		Method:  req.Method,
		Headers: req.Header,
	}

	// 5. Resolve the timeout
	reqTimeout := resolveTimeout(httpCfg)

	// 6. Send the request
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

	// 7. Decide the response mode
	mode, modeErr := determineResponseMode(httpCfg, resp)
	if modeErr != nil {
		return failf(nr, flow.ErrCodeConfig, modeErr.Error())
	}

	// Store the mode output variable (written in both response modes, so the handler does not repeat it)
	if err := nr.SetOutputVarWithFire("mode", mode); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write mode: %v", err))
	}

	// Record the response status into HttpResult
	if nr.HttpResult != nil {
		nr.HttpResult.Status = resp.StatusCode
	}

	// 8. Parse the handler config
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(httpCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}

	// 9. Handle the response by mode
	switch mode {
	case "sse_stream":
		return e.handleSSEStream(resp, node, ctx, processID, nr, inputArgs, handlerScript, handlerLang, handlerTimeout)
	default: // "complete"
		return e.handleComplete(resp, node, ctx, processID, nr, inputArgs, handlerScript, handlerLang, handlerTimeout)
	}
}

// handleComplete handles a non-streaming response
func (e *HttpExecutor) handleComplete(
	resp *http.Response,
	node *flow.Node, ctx *flow.ExecContext, processID string,
	nr *flow.NodeResult, inputArgs map[string]interface{},
	handlerScript, handlerLang string, handlerTimeout int,
) error {
	// Read the whole body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("failed to read the response body: %v", err))
	}

	bodyStr := bodyToString(bodyBytes, resp.Header.Get("Content-Type"))

	// Serialise the headers
	headersJSON, _ := json.Marshal(resp.Header)
	headersStr := string(headersJSON)

	// 1. The fixed output variables are always written by the executor (handler or not); mode was already
	// written in Execute
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

	// 2. Optional script post-processing: returning a fixed variable is caught and logged, the script only
	// writes custom variables
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

// handleSSEStream handles a streamed SSE response
func (e *HttpExecutor) handleSSEStream(
	resp *http.Response,
	node *flow.Node, ctx *flow.ExecContext, processID string,
	nr *flow.NodeResult, inputArgs map[string]interface{},
	handlerScript, handlerLang string, handlerTimeout int,
) error {
	// The response metadata is written by the executor; mode was already written in Execute, so it is not
	// repeated here
	headersJSON, _ := json.Marshal(resp.Header)
	if err := nr.SetOutputVarWithFire("status", float64(resp.StatusCode)); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write status: %v", err))
	}
	if err := nr.SetOutputVarWithFire("headers", string(headersJSON)); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write headers: %v", err))
	}

	scanner := bufio.NewScanner(resp.Body)
	// A larger buffer to support long data lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentEvent SSEEvent
	eventCount := 0
	// Events are collected unconditionally: with or without a handler, downstream gets the complete
	// sse_events
	var collectedEvents []map[string]interface{}

	// runEventHandler is the per-frame script post-processing (custom variables only)
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

		// An empty line marks the end of an event
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

		// Parse an SSE line
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
		// Comment lines (starting with :) and other lines are ignored
	}

	// Handle the last event (when the stream does not end with a blank line)
	if currentEvent.Data != "" || currentEvent.Event != "" {
		eventCount++
		collectedEvents = append(collectedEvents, currentEvent.toMap())
		if err := runEventHandler(currentEvent); err != nil {
			return err
		}
	}

	// A scan error
	if err := scanner.Err(); err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("SSE read error: %v", err))
	}

	// SSE forced but not a single data frame arrived -> an error
	if eventCount == 0 {
		return failf(nr, flow.ErrCodeConfig, "response_mode is forced to sse_stream but no SSE data frame arrived")
	}

	if err := nr.SetOutputVar("sse_events", collectedEvents); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write sse_events: %v", err))
	}

	if handlerScript != "" {
		// One extra finishing call when the stream closes
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

// executeHandlerScript runs the handler script and handles its result
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

	// A per-frame call: a returned string field is appended and a non-string field overwritten; the fixed
	// variables (mode/status/headers/body/sse_events/stream_finished) are off-limits to the script.
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
		Actions:      []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost:      true,
		FixedOutputs: outputNames(httpFixedOutputVars),
	}
}

func NewHttpExecutor() *HttpExecutor {
	return &HttpExecutor{}
}

func init() {
	RegisterExecutor("http", NewHttpExecutor())
}

// resolveInputVars resolves the input variables in config.inputs and writes them into nr.InputVars
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

// buildHTTPRequest builds the HTTP request
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

	// Parse the url and append the query params
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

	// Set Content-Type from body_type (when it was not set explicitly)
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

// KV is a key-value pair
type KV struct {
	Key   string
	Value string
}

// parseKeyValuePairs parses a key-value config such as headers/params
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

// doHTTPRequest performs the HTTP request
func doHTTPRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	client := &http.Client{
		Timeout: 0, // the timeout is controlled by the context
	}
	return client.Do(req.WithContext(ctx))
}

// determineResponseMode decides the response mode
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

	// Automatic detection: treat it as SSE only when the Content-Type is text/event-stream
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/event-stream" {
		return "sse_stream", nil
	}

	return "complete", nil
}

// bodyToString turns the response body into a string, base64-encoding non-text types
func bodyToString(body []byte, contentType string) string {
	if isTextContent(contentType) {
		return string(body)
	}
	return base64.StdEncoding.EncodeToString(body)
}

// isTextContent reports whether a Content-Type is a text type
func isTextContent(contentType string) bool {
	if contentType == "" {
		return true // treated as text by default
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

// resolveTimeout resolves the request timeout; -1 when not enabled, leaving it to the run context.
func resolveTimeout(httpCfg map[string]interface{}) int {
	return parseTimeoutConfig(httpCfg["timeout"])
}

// parseHandler parses the handler config.
// A handler script only supports lua/python/javascript: template is pure rendering (no side effects, no
// callbacks) and does not fit handler semantics, so it is an explicit error.
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

// mergeInputs merges the input args and the event data into the script input
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

// trimLeadingSpace removes leading space (at most one)
func trimLeadingSpace(s string) string {
	if len(s) > 0 && s[0] == ' ' {
		return s[1:]
	}
	return s
}
