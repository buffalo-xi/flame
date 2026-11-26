package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	llmmodelsrepo "flame/nexus/shared/repository/llmmodels"
	"flame/tools/xsql"
)

var _ Executor = (*LLMExecutor)(nil)

// Fixed output variables of the LLM node: identical in both response modes (in complete mode a reasoning model returns
// reasoning_content too, and it must not be dropped just because some frontend mode did not generate it).
var llmFixedOutputVars = map[string]string{
	"content":           "string",
	"reasoning_content": "string",
	"tool_calls":        "array[object]",
}

// llmReservedOutputVars is the set of fixed variables a script may not write (same rule as mcp/rpc).
// Fixed variables are always written by the executor; a script only handles custom output variables.
var llmReservedOutputVars = func() map[string]struct{} {
	m := make(map[string]struct{}, len(llmFixedOutputVars))
	for k := range llmFixedOutputVars {
		m[k] = struct{}{}
	}
	return m
}()

type LLMExecutor struct{}

func (e *LLMExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	llmCfg, ok := node.Data.Config["llm"].(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeConfig, "llm config not found")
	}

	// 1. Resolve the response mode
	responseMode, _ := llmCfg["response_mode"].(string)
	if responseMode == "" {
		responseMode = "complete"
	}
	if responseMode != "complete" && responseMode != "sse_stream" {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("unsupported response_mode: %s (valid values are complete or sse_stream)", responseMode))
	}

	// 2. Initialise the output variables (merged storage: the frontend already wrote fixed and custom variables into outputs).
	// The fixed variables are filled in again as a fallback, so there is something to write even if the frontend missed them.
	var outputs map[string]interface{}
	if raw, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		outputs = raw
	}
	outputs = ensureFixedOutputs(outputs, llmFixedOutputVars)
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)
	if locals, ok := node.Data.Config["locals"].(map[string]interface{}); ok {
		nr.InitLocalVars(locals)
	}

	// 3. Resolve the input variables and build the template context
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. Render the message list
	messages, err := buildLLMMessages(nr, node.Data.Config, llmCfg, tmplCtx)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, err.Error())
	}

	// 5. Resolve the model
	model, herr := resolveLLMModel(ctx, llmCfg)
	if herr != nil {
		return failf(nr, herr.ErrCode, herr.ErrMsg)
	}
	model.GatewayModel = xsql.String(ctx.Schema) + ":" + model.GatewayModel
	modelInput := buildModelInput(llmCfg, model)

	// 6. Resolve the handler config
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(llmCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 7. Execute
	if responseMode == "complete" {
		err = e.executeComplete(ctx, nr, modelInput, messages, handlerScript, handlerLang, handlerTimeout, hasHandler)
	} else {
		err = e.executeStream(ctx, nr, modelInput, messages, handlerScript, handlerLang, handlerTimeout, hasHandler)
	}

	if err != nil {
		// err already carries ErrCode/ErrMsg (from nr.Finish)
		return newExecError(nr.ErrCode, nr.ErrMsg)
	}
	return nil
}

// resolveLLMModel looks the model up by gateway_model; an empty value falls back to the default model.
func resolveLLMModel(ctx *flow.ExecContext, llmCfg map[string]interface{}) (*models.LLMModel, *scriptExecError) {
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	modelRepo := llmmodelsrepo.NewPostgresRepository()

	gatewayModel, _ := llmCfg["gateway_model"].(string)
	if gatewayModel == "" {
		def, err := modelRepo.GetDefault(authInfo)
		if err != nil {
			return nil, &scriptExecError{ErrCode: flow.ErrCodeNotFound, ErrMsg: fmt.Sprintf("no model specified and no default model exists: %v", err)}
		}
		if string(def.GatewayID) == "" {
			return nil, &scriptExecError{ErrCode: flow.ErrCodeConfig, ErrMsg: "the default model is not enabled (gateway_id is empty), so it cannot be called"}
		}
		return def, nil
	}

	model, err := modelRepo.GetByGatewayModel(authInfo, gatewayModel)
	if err != nil {
		return nil, &scriptExecError{ErrCode: flow.ErrCodeNotFound, ErrMsg: fmt.Sprintf("failed to look up model %s: %v", gatewayModel, err)}
	}
	return model, nil
}

// buildModelInput builds the gateway ModelInput, merging the model defaults with node-level overrides.
// Merge rules:
//   - max_tokens / temperature / top_p: scalars merged per field; a node value overrides the model default, a missing field falls back to it
//   - extra_params: the whole object is replaced; when configured on the node it fully overrides the model's extra_params
func buildModelInput(llmCfg map[string]interface{}, model *models.LLMModel) *gateway.ModelInput {
	mi := &gateway.ModelInput{
		GatewayModel: string(model.GatewayModel),
		GatewayID:    string(model.GatewayID),
	}

	// Model default base_params
	var bp map[string]any
	if !model.BaseParams.IsNil() {
		_ = model.BaseParams.Unmarshal(&bp)
	}
	if v, ok := bpFloat(bp, "max_tokens"); ok {
		mi.MaxTokens = int(v)
	}
	if v, ok := bpFloat(bp, "temperature"); ok {
		mi.Temperature = v
	}
	if v, ok := bpFloat(bp, "top_p"); ok {
		mi.TopP = v
	}

	// Model default extra_params
	mi.ExtraParams = make(map[string]any)
	if !model.ExtraParams.IsNil() {
		var ep map[string]any
		if err := model.ExtraParams.Unmarshal(&ep); err == nil {
			for k, v := range ep {
				mi.ExtraParams[k] = v
			}
		}
	}

	// Node-level params override (a key being present means it is enabled)
	params, _ := llmCfg["params"].(map[string]interface{})
	if params == nil {
		return mi
	}
	if v, ok := bpFloat(params, "max_tokens"); ok {
		mi.MaxTokens = int(v)
	}
	if v, ok := bpFloat(params, "temperature"); ok {
		mi.Temperature = v
	}
	if v, ok := bpFloat(params, "top_p"); ok {
		mi.TopP = v
	}
	if raw, ok := params["extra_params"]; ok {
		if nodeEP, ok := raw.(map[string]interface{}); ok {
			// Field-level merge: the node overrides the same key, the model's other keys are kept
			for k, v := range nodeEP {
				mi.ExtraParams[k] = v
			}
		} else if raw == nil {
			mi.ExtraParams = make(map[string]any)
		}
	}
	return mi
}

// bpFloat reads a float field from base_params/params (tolerating json.Number, ints and floats).
func bpFloat(m map[string]interface{}, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// buildLLMMessages renders the config.llm.messages templates.
func buildLLMMessages(nr *flow.NodeResult, nodeCfg map[string]interface{},
	llmCfg map[string]interface{}, tmplCtx map[string]interface{}) ([]gateway.Message, error) {
	raw, ok := llmCfg["messages"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("messages cannot be empty")
	}

	// Before rendering, check whether a prompt references a secret global variable: after rendering it is one concatenated
	// string and there is no telling which part came from a secret, so by then it is too late. This only warns (into the node
	// log) and never blocks.
	contents := make([]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if c, ok := m["content"].(string); ok {
			contents = append(contents, c)
		}
	}
	WarnSecretPromptRefs(nr, nodeCfg, contents...)

	msgs := make([]gateway.Message, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if role == "" {
			role = "user"
		}
		rendered, err := RenderTemplate(content, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to render the messages template: %w", err)
		}
		msgs = append(msgs, gateway.Message{Role: role, Content: rendered})
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("messages are empty after rendering")
	}
	return msgs, nil
}

// executeComplete runs the non-streaming call.
func (e *LLMExecutor) executeComplete(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	modelInput *gateway.ModelInput,
	messages []gateway.Message,
	handlerScript, handlerLang string, handlerTimeout int, hasHandler bool,
) error {
	resp, err := gateway.Current.Generate(ctx.StdCtx, *modelInput, messages, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("LLM call failed: %v", err))
	}

	// 1. Fixed output variables are always written by the executor (with or without a handler); scripts only handle custom ones.
	if err := nr.SetOutputVarWithFire("content", resp.Content); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write content: %v", err))
	}
	if resp.ReasoningContent != "" {
		if err := nr.SetOutputVarWithFire("reasoning_content", resp.ReasoningContent); err != nil {
			return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write reasoning_content: %v", err))
		}
	}
	if err := nr.SetOutputVarWithFire("tool_calls", resp.ToolCalls); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write tool_calls: %v", err))
	}

	// 2. Optional script post-processing: returning a fixed variable is rejected and logged
	if hasHandler {
		inputs := map[string]interface{}{
			"mode":              "complete",
			"content":           resp.Content,
			"reasoning_content": resp.ReasoningContent,
			"tool_calls":        resp.ToolCalls,
			"done":              true,
		}
		if serr := executeLLMHandlerScript(ctx, nr, handlerLang, handlerScript, inputs, handlerTimeout); serr != nil {
			return failf(nr, serr.ErrCode, serr.ErrMsg)
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// llmStreamErrCode is the error code when the stream breaks:
// a cancelled / timed-out ctx is attributed to the cancel class first; everything else (unwrap failure, connection drop)
// becomes a connection error.
func llmStreamErrCode(ctx *flow.ExecContext) string {
	if ctx != nil {
		switch ctx.StdCtx.Err() {
		case context.DeadlineExceeded:
			return flow.ErrCodeTimeout
		case context.Canceled:
			return flow.ErrCodeCancel
		}
	}
	return flow.ErrCodeConnection
}

// executeStream runs the streaming call.
func (e *LLMExecutor) executeStream(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	modelInput *gateway.ModelInput,
	messages []gateway.Message,
	handlerScript, handlerLang string, handlerTimeout int, hasHandler bool,
) error {
	stream, err := gateway.Current.Stream(ctx.StdCtx, *modelInput, messages, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("LLM streaming call failed: %v", err))
	}

	var lastToolCalls []gateway.ToolCall
	for chunk := range stream {
		// A mid-stream failure (unwrap failure / read drop / ctx cancel) is delivered through chunk.Error;
		// not checking it would return half the content as a success.
		if chunk.Error != nil {
			return failf(nr, llmStreamErrCode(ctx), fmt.Sprintf("LLM streaming call interrupted: %v", chunk.Error))
		}
		// 1. Fixed output variables are accumulated frame by frame by the executor (with or without a handler)
		if chunk.Content != "" {
			if aerr := nr.AppendOutputVarWithFire("content", chunk.Content); aerr != nil {
				return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to append content: %v", aerr))
			}
		}
		if chunk.ReasoningContent != "" {
			if aerr := nr.AppendOutputVarWithFire("reasoning_content", chunk.ReasoningContent); aerr != nil {
				return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to append reasoning_content: %v", aerr))
			}
		}
		// tool_calls are updated only on non-empty frames: the Done frame carries an empty array, and overwriting with it
		// would wipe the complete snapshot
		if len(chunk.ToolCalls) > 0 {
			lastToolCalls = chunk.ToolCalls
		}

		// 2. Per-frame script post-processing: custom variables only (fixed ones are already blocked by llmReservedOutputVars)
		if hasHandler {
			inputs := map[string]interface{}{
				"mode":              "sse_stream",
				"content":           chunk.Content,
				"reasoning_content": chunk.ReasoningContent,
				"tool_calls":        chunk.ToolCalls,
				"done":              chunk.Done,
			}
			if serr := executeLLMHandlerScript(ctx, nr, handlerLang, handlerScript, inputs, handlerTimeout); serr != nil {
				return failf(nr, serr.ErrCode, serr.ErrMsg)
			}
		}
	}

	if len(lastToolCalls) > 0 {
		if err := nr.SetOutputVarWithFire("tool_calls", lastToolCalls); err != nil {
			return failf(nr, flow.ErrCodeVar, fmt.Sprintf("failed to write tool_calls: %v", err))
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// executeLLMHandlerScript runs the response handler script.
//
// The fixed output variables content / reasoning_content / tool_calls are already written by the executor and a script may
// not write them again; returned fields go to custom output variables: strings are appended, everything else overwrites.
func executeLLMHandlerScript(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	lang, script string, inputs map[string]interface{}, timeout int,
) *scriptExecError {
	scriptResult, err := ExecuteScript(ctx.StdCtx, lang, script, inputs, nr, timeout)
	if err != nil {
		return &scriptExecError{
			ErrCode: scriptErrCode(err),
			ErrMsg:  fmt.Sprintf("handler script failed: %v", err),
		}
	}
	if scriptResult == nil {
		return nil
	}
	if err := applyHandlerAppend(nr, scriptResult, llmReservedOutputVars); err != nil {
		return &scriptExecError{
			ErrCode: flow.ErrCodeVar,
			ErrMsg:  fmt.Sprintf("failed to write the handler script return value: %v", err),
		}
	}
	return nil
}

func (e *LLMExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{
			flow.ErrCodeConfig,
			flow.ErrCodeConnection,
			flow.ErrCodeTimeout,
			flow.ErrCodeNotFound,
			flow.ErrCodeScript,
			flow.ErrCodeBusy,
			flow.ErrCodeVar,
			flow.ErrCodeOther,
		},
		Actions:      []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost:      true,
		FixedOutputs: outputNames(llmFixedOutputVars),
	}
}

func NewLLMExecutor() *LLMExecutor {
	return &LLMExecutor{}
}

func init() {
	RegisterExecutor("llm", NewLLMExecutor())
}
