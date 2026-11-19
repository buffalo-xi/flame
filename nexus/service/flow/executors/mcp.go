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

// The MCP node's fixed output variables
var mcpFixedOutputVars = map[string]string{
	"result":   "string",        // the call result (flattenResult.Content)
	"progress": "array[string]", // the progress notifications (each one the full raw JSON string)
	"error":    "string",        // the call error message
}

// mcpReservedOutputVars is the set of fixed variables a script may not write (same rule as
// rpcReservedOutputVars). The executor writes these three unconditionally (handler or not), so a script only
// deals with custom variables.
var mcpReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(mcpFixedOutputVars))
	for k := range mcpFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

type McpExecutor struct{}

// progressCollector collects the raw JSON strings of the progress notifications.
// onProgress is called concurrently from the SDK's background reader goroutine, so append needs a lock.
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

	// 1. Parse the mcp config
	mcpCfg, ok := node.Data.Config["mcp"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "mcp config not found")
	}

	// 2. Initialise the output variables
	if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// 3. Resolve the input variables and build the template context
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. Resolve connection_id and tool_name
	connectionID, _ := mcpCfg["connection_id"].(string)
	if strings.TrimSpace(connectionID) == "" {
		return failf(nr, flow.ErrCodeConfig, "mcp connection_id must not be empty")
	}
	toolName, _ := mcpCfg["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return failf(nr, flow.ErrCodeConfig, "mcp tool_name must not be empty")
	}

	// 5. Parse the handler config (and reject template)
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(mcpCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 6. Render the arguments template
	arguments, argErr := renderMcpArguments(mcpCfg, tmplCtx)
	if argErr != nil {
		return failf(nr, flow.ErrCodeConfig, argErr.Error())
	}

	// Whether to subscribe to progress: a node-level switch, off by default.
	// When off it takes the synchronous path without a progressToken — the progress output variable stays an
	// empty array, and the server is not made to push all of the agent's thinking / text (which crosses the
	// network on every call and is not cheap).
	wantProgress, _ := mcpCfg["progress"].(bool)

	// 7. Look up the connection
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	conn, connErr := mcpconnrepo.NewRepository().GetByID(authInfo, connectionID)
	if connErr != nil {
		return failf(nr, flow.ErrCodeNotFound, fmt.Sprintf("MCP connection not found: %v", connErr))
	}
	if !conn.IsActive {
		return failf(nr, flow.ErrCodeConfig, "the MCP connection is not enabled")
	}

	// 8. Collector plus the onProgress callback
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
				// A per-notification handler failure: logged, but it does not abort the call (progress is
				// infrastructure output)
				nr.AddLogf("the progress handler failed: %s", err.ErrMsg)
			}
		}
	}

	// 9. Call the tool: with progress subscription a token is sent and progress is received concurrently;
	// otherwise one synchronous call returns the result
	var (
		res     *client.ToolResult
		callErr error
	)
	// The ancestor chain is handed to the client through ctx: an MCP node may also call back into this
	// system's /service/mcp/{schema}, and without the chain the peer cannot see that it is already an
	// ancestor.
	stdCtx := callchain.With(ctx.StdCtx, ctx.CallChain)
	if wantProgress {
		res, callErr = client.InvokeWithProgress(stdCtx, conn, toolName, arguments, onProgress)
	} else {
		res, callErr = client.InvokeSync(stdCtx, conn, toolName, arguments)
	}

	// 10. After CallTool returns: write the progress array (final, overwrite semantics)
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

	// 11. Finish (same rule as rpc.finishRpcResult: the fixed variables are always written by the executor)
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

// finishMcpResult finishes the node: the fixed output variables are always written by the executor (the same
// rule as rpc.finishRpcResult), handler or not; the handler is optional post-processing and may only write
// custom output variables.
//
// The policy was copied from RPC for the same reason: pushing the obligation to write result/error onto the
// handler config means a script that forgets makes the variable permanently empty, and the UI cannot tell
// "did a remote return this value or did a script invent it".
//
// isError=true is the tool's own business error (write the error variable, the node still succeeds); a call
// exception was already handled as failed by the caller.
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
	// 1. The fixed variables are always written by the node
	if isError {
		nr.SetOutputVarWithFire("error", result)
	} else {
		nr.SetOutputVarWithFire("result", result)
	}

	// 2. Optional script post-processing (custom variables); the fixed ones are caught by
	// mcpReservedOutputVars
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

// progressToMap turns ProgressNotificationParams into a structured map (for the script's eventData).
// meta carries category (thinking / text / tool_call / tool_result) so a handler can route on it; it matches
// rpcProgressToMap.
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

// renderMcpArguments renders the arguments template. The arguments config is a JSON object string whose string
// values support the {{.input.xxx}} template; the rendered JSON is decoded into a map[string]any as a whole.
func renderMcpArguments(mcpCfg map[string]interface{}, tmplCtx map[string]interface{}) (map[string]interface{}, error) {
	raw, exists := mcpCfg["arguments"]
	if !exists || raw == nil {
		return map[string]interface{}{}, nil
	}

	// Already an object (map): render the string values recursively
	if obj, ok := raw.(map[string]interface{}); ok {
		return renderMcpArgumentsMap(obj, tmplCtx)
	}

	// A JSON string: render the template first, then decode
	if str, ok := raw.(string); ok {
		rendered, err := RenderTemplate(str, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to render the arguments template: %w", err)
		}
		if strings.TrimSpace(rendered) == "" {
			return map[string]interface{}{}, nil
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(rendered), &out); err != nil {
			return nil, fmt.Errorf("failed to decode the arguments JSON: %w", err)
		}
		return out, nil
	}

	return nil, fmt.Errorf("the arguments config is malformed; expected a JSON object or an object string")
}

// renderMcpArgumentsMap renders the templates of the string values inside an object, recursively.
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

// executeHandlerScript runs the handler script and handles its result (same as HTTP/LLM).
func (e *McpExecutor) executeHandlerScript(
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

	// A per-chunk call: a returned string field is appended (streaming accumulation) and a non-string field
	// overwritten (a snapshot). The fixed variables (result/progress/error) are off-limits to scripts and
	// written by the executor itself.
	if scriptResult != nil {
		if err := applyHandlerAppend(nr, scriptResult, mcpReservedOutputVars); err != nil {
			return &scriptExecError{
				ErrCode: flow.ErrCodeVar,
				ErrMsg:  fmt.Sprintf("failed to write the handler script result: %v", err),
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
