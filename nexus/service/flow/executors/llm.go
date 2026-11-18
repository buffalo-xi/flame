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

// LLM 节点固定输出变量：两种响应模式一致（complete 模式遇到推理模型同样返回
// reasoning_content，不能因为前端某个模式没生成就丢掉）。
var llmFixedOutputVars = map[string]string{
	"content":           "string",
	"reasoning_content": "string",
	"tool_calls":        "array[object]",
}

// llmReservedOutputVars 脚本不可写的固定变量集合（与 mcp/rpc 同口径）。
// 固定变量一律由执行器写入，脚本只能处理自定义输出变量。
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

	// 1. 解析响应模式
	responseMode, _ := llmCfg["response_mode"].(string)
	if responseMode == "" {
		responseMode = "complete"
	}
	if responseMode != "complete" && responseMode != "sse_stream" {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("不支持的 response_mode: %s，有效值为 complete 或 sse_stream", responseMode))
	}

	// 2. 初始化输出变量（融合存储：outputs 已由前端写入固定+自定义变量）。
	// 固定变量再兜底补齐一次，前端漏配也有变量可写。
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

	// 3. 解析输入变量并渲染模板上下文
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}
	tmplCtx := BuildScriptTemplateContext(nr, inputArgs)

	// 4. 渲染消息列表
	messages, err := buildLLMMessages(nr, node.Data.Config, llmCfg, tmplCtx)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, err.Error())
	}

	// 5. 解析模型
	model, herr := resolveLLMModel(ctx, llmCfg)
	if herr != nil {
		return failf(nr, herr.ErrCode, herr.ErrMsg)
	}
	model.GatewayModel = xsql.String(ctx.Schema) + ":" + model.GatewayModel
	modelInput := buildModelInput(llmCfg, model)

	// 6. 解析 handler 配置
	handlerScript, handlerLang, handlerTimeout, handlerErr := parseHandler(llmCfg, ctx)
	if handlerErr != nil {
		return failf(nr, flow.ErrCodeConfig, handlerErr.Error())
	}
	hasHandler := strings.TrimSpace(handlerScript) != ""

	// 7. 执行
	if responseMode == "complete" {
		err = e.executeComplete(ctx, nr, modelInput, messages, handlerScript, handlerLang, handlerTimeout, hasHandler)
	} else {
		err = e.executeStream(ctx, nr, modelInput, messages, handlerScript, handlerLang, handlerTimeout, hasHandler)
	}

	if err != nil {
		// err 已含 ErrCode/ErrMsg（来自 nr.Finish）
		return newExecError(nr.ErrCode, nr.ErrMsg)
	}
	return nil
}

// resolveLLMModel 根据 gateway_model 查找模型；为空时回退到默认模型
func resolveLLMModel(ctx *flow.ExecContext, llmCfg map[string]interface{}) (*models.LLMModel, *scriptExecError) {
	authInfo := &auth.AuthInfo{Schema: ctx.Schema}
	modelRepo := llmmodelsrepo.NewPostgresRepository()

	gatewayModel, _ := llmCfg["gateway_model"].(string)
	if gatewayModel == "" {
		def, err := modelRepo.GetDefault(authInfo)
		if err != nil {
			return nil, &scriptExecError{ErrCode: flow.ErrCodeNotFound, ErrMsg: fmt.Sprintf("未指定模型且不存在缺省模型: %v", err)}
		}
		if string(def.GatewayID) == "" {
			return nil, &scriptExecError{ErrCode: flow.ErrCodeConfig, ErrMsg: "缺省模型未启用（gateway_id 为空），无法调用"}
		}
		return def, nil
	}

	model, err := modelRepo.GetByGatewayModel(authInfo, gatewayModel)
	if err != nil {
		return nil, &scriptExecError{ErrCode: flow.ErrCodeNotFound, ErrMsg: fmt.Sprintf("查找模型 %s 失败: %v", gatewayModel, err)}
	}
	return model, nil
}

// buildModelInput 构造网关 ModelInput，融合模型默认参数与节点级覆盖参数。
// 合并规则：
//   - max_tokens / temperature / top_p：标量（按字段）合并，节点有则覆盖模型默认，缺字段回退模型默认
//   - extra_params：整对象替换，节点配了则完全覆盖模型的 extra_params
func buildModelInput(llmCfg map[string]interface{}, model *models.LLMModel) *gateway.ModelInput {
	mi := &gateway.ModelInput{
		GatewayModel: string(model.GatewayModel),
		GatewayID:    string(model.GatewayID),
	}

	// 模型默认 base_params
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

	// 模型默认 extra_params
	mi.ExtraParams = make(map[string]any)
	if !model.ExtraParams.IsNil() {
		var ep map[string]any
		if err := model.ExtraParams.Unmarshal(&ep); err == nil {
			for k, v := range ep {
				mi.ExtraParams[k] = v
			}
		}
	}

	// 节点级 params 覆盖（键存在=启用）
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
			// 字段级合并：节点覆盖同名 key，模型其余 key 保留
			for k, v := range nodeEP {
				mi.ExtraParams[k] = v
			}
		} else if raw == nil {
			mi.ExtraParams = make(map[string]any)
		}
	}
	return mi
}

// bpFloat 从 base_params/params 中读取浮点型字段（容忍 json.Number、整型、浮点型）
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

// buildLLMMessages 渲染 config.llm.messages 模板
func buildLLMMessages(nr *flow.NodeResult, nodeCfg map[string]interface{},
	llmCfg map[string]interface{}, tmplCtx map[string]interface{}) ([]gateway.Message, error) {
	raw, ok := llmCfg["messages"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("messages 不能为空")
	}

	// 渲染前先看「提示词有没有引用密钥类型的全局变量」：渲染后就是拼好的字符串，
	// 分不出哪一段来自 secret，那时再查已经晚了。只告警（记节点日志），不阻断。
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
			return nil, fmt.Errorf("messages 模板渲染失败: %w", err)
		}
		msgs = append(msgs, gateway.Message{Role: role, Content: rendered})
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("messages 渲染后为空")
	}
	return msgs, nil
}

// executeComplete 非流式执行
func (e *LLMExecutor) executeComplete(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	modelInput *gateway.ModelInput,
	messages []gateway.Message,
	handlerScript, handlerLang string, handlerTimeout int, hasHandler bool,
) error {
	resp, err := gateway.Current.Generate(ctx.StdCtx, *modelInput, messages, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("LLM 调用失败: %v", err))
	}

	// 1. 固定输出变量一律由执行器写（有没有 handler 都一样），脚本只管自定义变量。
	if err := nr.SetOutputVarWithFire("content", resp.Content); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("写入 content 失败: %v", err))
	}
	if resp.ReasoningContent != "" {
		if err := nr.SetOutputVarWithFire("reasoning_content", resp.ReasoningContent); err != nil {
			return failf(nr, flow.ErrCodeVar, fmt.Sprintf("写入 reasoning_content 失败: %v", err))
		}
	}
	if err := nr.SetOutputVarWithFire("tool_calls", resp.ToolCalls); err != nil {
		return failf(nr, flow.ErrCodeVar, fmt.Sprintf("写入 tool_calls 失败: %v", err))
	}

	// 2. 脚本可选后处理：return 固定变量会被拦下并记日志
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

// llmStreamErrCode 流中断时的错误码：
// ctx 已取消/超时优先归因到取消类，其余（解包失败、连接断开）归连接错误。
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

// executeStream 流式执行
func (e *LLMExecutor) executeStream(
	ctx *flow.ExecContext, nr *flow.NodeResult,
	modelInput *gateway.ModelInput,
	messages []gateway.Message,
	handlerScript, handlerLang string, handlerTimeout int, hasHandler bool,
) error {
	stream, err := gateway.Current.Stream(ctx.StdCtx, *modelInput, messages, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("LLM 流式调用失败: %v", err))
	}

	var lastToolCalls []gateway.ToolCall
	for chunk := range stream {
		// 流中途出错（解包失败 / 读取断开 / ctx 取消）由 chunk.Error 传递，
		// 不检查会带着半截内容当成功返回。
		if chunk.Error != nil {
			return failf(nr, llmStreamErrCode(ctx), fmt.Sprintf("LLM 流式调用中断: %v", chunk.Error))
		}
		// 1. 固定输出变量由执行器逐帧累积（有没有 handler 都一样）
		if chunk.Content != "" {
			if aerr := nr.AppendOutputVarWithFire("content", chunk.Content); aerr != nil {
				return failf(nr, flow.ErrCodeVar, fmt.Sprintf("追加 content 失败: %v", aerr))
			}
		}
		if chunk.ReasoningContent != "" {
			if aerr := nr.AppendOutputVarWithFire("reasoning_content", chunk.ReasoningContent); aerr != nil {
				return failf(nr, flow.ErrCodeVar, fmt.Sprintf("追加 reasoning_content 失败: %v", aerr))
			}
		}
		// tool_calls 只在非空帧更新：Done 帧带的是空数组，直接覆盖会把完整快照冲掉
		if len(chunk.ToolCalls) > 0 {
			lastToolCalls = chunk.ToolCalls
		}

		// 2. 脚本逐帧后处理：只写自定义变量（固定变量已被 llmReservedOutputVars 拦下）
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
			return failf(nr, flow.ErrCodeVar, fmt.Sprintf("写入 tool_calls 失败: %v", err))
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// executeLLMHandlerScript 执行响应处理脚本（handler）。
//
// 固定输出变量 content / reasoning_content / tool_calls 已由执行器写好，脚本不许再写；
// return 的字段写到自定义输出变量：string 追加、非 string 覆盖。
func executeLLMHandlerScript(
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
	if scriptResult == nil {
		return nil
	}
	if err := applyHandlerAppend(nr, scriptResult, llmReservedOutputVars); err != nil {
		return &scriptExecError{
			ErrCode: flow.ErrCodeVar,
			ErrMsg:  fmt.Sprintf("handler 脚本返回值写入失败: %v", err),
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
		Actions: []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost: true,
	}
}

func NewLLMExecutor() *LLMExecutor {
	return &LLMExecutor{}
}

func init() {
	RegisterExecutor("llm", NewLLMExecutor())
}
