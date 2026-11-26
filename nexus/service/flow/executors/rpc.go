package executors

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/service/agent"
	"flame/nexus/service/flow/core"
	"flame/nexus/service/rpc/client"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	rpcconnrepo "flame/nexus/shared/repository/rpcconnections"
)

var _ Executor = (*RpcExecutor)(nil)

// The RPC node's fixed output variables
var rpcFixedOutputVars = map[string]string{
	"result":   "string",        // the call result (flattenResult.Content)
	"progress": "array[string]", // the progress notifications (each one the full raw JSON string)
	"error":    "string",        // the call error message
}

// rpcReservedOutputVars is the set of fixed variables a script may not write.
// The executor writes these three unconditionally (handler or not), so a script only deals with custom
// variables.
var rpcReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(rpcFixedOutputVars))
	for k := range rpcFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

type RpcExecutor struct{}

// rpcProgressCollector collects the raw JSON strings of the progress notifications.
// onProgress is called concurrently from the stdio read loop / the HTTP SSE parser while the main goroutine is
// blocked on the call, so append needs a lock.
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

	// 1. Parse the rpc config
	rpcCfg, ok := node.Data.Config["rpc"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "rpc config not found")
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

	// 4. Resolve target_type (an empty value means connection, for compatibility with old configs)
	targetType, _ := rpcCfg["target_type"].(string)
	if targetType == "" {
		targetType = "connection"
	}

	// 5. Parse the handler config (and reject template)
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(rpcCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 6. Render the arguments template (reusing the generic arguments rendering from MCP)
	arguments, argErr := renderMcpArguments(rpcCfg, tmplCtx)
	if argErr != nil {
		return failf(nr, flow.ErrCodeConfig, argErr.Error())
	}

	// Whether to subscribe to progress: a node-level switch, off by default.
	// When off both paths take synchronous semantics — an in-process call does not consume the event channel
	// and an external connection sends no progressToken, so the progress output variable stays an empty
	// array. Only when on does it receive them one by one (at the cost of walking the whole event stream on
	// every call).
	wantProgress, _ := rpcCfg["progress"].(bool)

	if targetType == "endpoint" {
		return e.executeEndpoint(ctx, nr, inputArgs, rpcCfg, arguments, wantProgress, hasHandler, handlerLang, handlerScript, handlerTimeout)
	}

	// ===== external connection =====
	// 7. Resolve connection_id and tool_name
	connectionID, _ := rpcCfg["connection_id"].(string)
	if strings.TrimSpace(connectionID) == "" {
		return failf(nr, flow.ErrCodeConfig, "rpc connection_id must not be empty")
	}
	toolName, _ := rpcCfg["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return failf(nr, flow.ErrCodeConfig, "rpc tool_name must not be empty")
	}

	// 8. Look up the connection
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	conn, connErr := rpcconnrepo.NewRepository().GetByID(authInfo, connectionID)
	if connErr != nil {
		return failf(nr, flow.ErrCodeNotFound, fmt.Sprintf("RPC connection not found: %v", connErr))
	}
	if !conn.IsActive {
		return failf(nr, flow.ErrCodeConfig, "the RPC connection is not enabled")
	}

	// 9. Collector plus the onProgress callback
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
				// A per-notification handler failure: logged, but it does not abort the call (progress is
				// infrastructure output)
				nr.AddLogf("the progress handler failed: %s", err.ErrMsg)
			}
		}
	}

	// 10. Call the tool: with progress subscription a token is sent and progress received concurrently;
	// otherwise one synchronous call returns the result
	var (
		res     *client.ToolResult
		callErr error
	)
	// The ancestor chain is handed to the client through ctx (outbound only carries it, never appends; the
	// append happens on the peer when it is about to execute the entity)
	stdCtx := callchain.With(ctx.StdCtx, ctx.CallChain)
	if wantProgress {
		res, callErr = client.InvokeWithProgress(stdCtx, conn, toolName, arguments, onProgress)
	} else {
		res, callErr = client.Invoke(stdCtx, conn, toolName, arguments)
	}

	// 11. After the call returns: write the progress array (final, overwrite semantics)
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

	// 12. Finish
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

// executeEndpoint calls an endpoint of this tenant directly: look up svc_endpoints (protocol=rpc, exposed is not
// checked) -> run in process -> collect progress while streaming -> finish.
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
		return failf(nr, flow.ErrCodeConfig, "rpc endpoint is not configured (fill in the endpoint name)")
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
				nr.AddLogf("the progress handler failed: %s", err.ErrMsg)
			}
		}
	}

	authInfo := &auth.AuthInfo{Schema: ctx.Schema, ID: ctx.UserID}
	// ctx.CallDepth is this flow's stack depth (0 at the top level): several rpc nodes in the same flow read
	// the same value, so sibling calls do not accumulate and only real nesting (a sub-flow calling another
	// endpoint) increases it.
	// ctx.DebugScope: follow drafts inside a debug session; nil for a real run
	result, isError, runErr := runEndpoint(ctx.StdCtx, authInfo, ctx.SessionID, ctx.CallDepth, ctx.CallChain, endpointName, arguments, wantProgress, ctx.DebugScope, onProgress)

	// Write the final progress array
	if snap := coll.snapshot(); snap != nil {
		nr.SetOutputVarWithFire("progress", snap)
	}

	if runErr != nil {
		// A missing default model is a configuration error, the same class the llm node reports: retrying is useless
		// and on_error can be configured to notice it. Reporting "other" would send people to investigate the platform.
		if _, isCfg := agent.AsConfigError(runErr); isCfg {
			return failf(nr, flow.ErrCodeConfig, runErr.Error())
		}
		return failf(nr, flow.ErrCodeOther, runErr.Error())
	}

	return e.finishRpcResult(ctx, nr, inputArgs, hasHandler, handlerLang, handlerScript, handlerTimeout, result, isError, nil, coll.snapshot())
}

// finishRpcResult finishes the node: the fixed output variables are always written by the executor (handler or
// not), and the handler is only optional post-processing that may write custom output variables only.
// isError=true is a remote business error (write the error variable, the node still succeeds); a node exception
// was already handled as failed by the caller.
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
	// 1. The fixed variables are always written by the node. This step used to be skipped in the hasHandler
	// branch, which meant "a script that forgets to write result leaves it empty forever" — pushing an
	// obligation onto the config.
	if isError {
		nr.SetOutputVarWithFire("error", result)
	} else {
		nr.SetOutputVarWithFire("result", result)
	}

	// 2. Optional script post-processing (custom variables); the fixed ones are caught by
	// rpcReservedOutputVars.
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

// rpcProgressToMap turns RPC ProgressNotificationParams into a structured map (for the script's eventData).
// message and meta are passed through in full (meta carries category, see the Category* constants in
// agent/core).
func rpcProgressToMap(p *client.ProgressNotificationParams) map[string]interface{} {
	m := map[string]interface{}{
		"message": p.Message,
	}
	if p.Meta != nil {
		m["meta"] = p.Meta
	}
	return m
}

// executeHandlerScript runs the handler script and handles its result (same as HTTP/LLM/MCP).
func (e *RpcExecutor) executeHandlerScript(
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

	// A per-chunk call: a returned string field is appended and a non-string field overwritten (a snapshot);
	// the fixed variables (result/progress/error) are off-limits to scripts and written by the executor
	// itself.
	if scriptResult != nil {
		if err := applyHandlerAppend(nr, scriptResult, rpcReservedOutputVars); err != nil {
			return &scriptExecError{
				ErrCode: flow.ErrCodeVar,
				ErrMsg:  fmt.Sprintf("failed to write the handler script result: %v", err),
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
		Actions:      []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost:      true,
		FixedOutputs: outputNames(rpcFixedOutputVars),
	}
}

func NewRpcExecutor() *RpcExecutor {
	return &RpcExecutor{}
}

func init() {
	RegisterExecutor("rpc", NewRpcExecutor())
}
