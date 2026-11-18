package gatewaytest

import (
	"context"
	"encoding/json"
	"time"

	"flame/nexus/service/llm/gateway"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// testRequest 测试请求体
type testRequest struct {
	// Provider 配置
	Provider struct {
		Name     string `json:"name"`     // 供应商名称，如 openai、deepseek
		URL      string `json:"url"`      // API 地址
		APIKey   string `json:"api_key"`  // API Key
		Protocol string `json:"protocol"` // 协议，如 openai
		// ExtraConfig 额外配置，原样透传进 litellm_params
		// （api_version / vertex_project / vertex_location / vertex_credentials 等）
		ExtraConfig map[string]any `json:"extra_config,omitempty"`
	} `json:"provider"`
	// Model 配置
	Model struct {
		Name          string  `json:"name"`           // 模型名称，如 gpt-4o-mini、deepseek-chat
		GatewayModel  string  `json:"gateway_model"`  // 网关模型名，注册名与 chat 请求的 model 字段都用它
		MaxTokens     int     `json:"max_tokens"`     // 最大 token 数
		Temperature   float64 `json:"temperature"`    // 温度
		ContextWindow int     `json:"context_window"` // 上下文窗口
	} `json:"model"`
	// Chat 测试用的消息（可选，默认用 "你好，请回复 OK"）
	Messages []gateway.Message `json:"messages,omitempty"`
	// Schema 租户标识（可选，默认用 "test"）
	Schema string `json:"schema,omitempty"`
}

// testStepResult 单步结果
type testStepResult struct {
	Step   string `json:"step"`
	Status string `json:"status"` // "ok" | "error"
	Detail any    `json:"detail,omitempty"`
}

// testResponse 测试响应体
type testResponse struct {
	Steps     []testStepResult `json:"steps"`
	GatewayID string           `json:"gateway_id,omitempty"`
}

// RunGatewayTest 运行完整的 LiteLLM 网关测试：Provision → Generate → Deprovision
func RunGatewayTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	var req testRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	// 默认值
	if req.Schema == "" {
		req.Schema = "t0"
	}
	if req.Provider.Protocol == "" {
		req.Provider.Protocol = "openai"
	}
	if len(req.Messages) == 0 {
		req.Messages = []gateway.Message{
			{Role: "user", Content: "你好，请回复 OK，不要多说话。"},
		}
	}

	provider := gateway.ProviderInput{
		Name:        req.Provider.Name,
		URL:         req.Provider.URL,
		APIKey:      req.Provider.APIKey,
		Protocol:    req.Provider.Protocol,
		ExtraConfig: req.Provider.ExtraConfig,
	}
	model := gateway.ModelInput{
		Name:          req.Model.Name,
		GatewayModel:  req.Model.GatewayModel,
		MaxTokens:     req.Model.MaxTokens,
		Temperature:   req.Model.Temperature,
		ContextWindow: req.Model.ContextWindow,
	}

	response := testResponse{}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	// Step 1: Provision
	gatewayID, err := gateway.Current.Provision(req.Schema, model, provider)
	if err != nil {
		response.Steps = append(response.Steps, testStepResult{
			Step: "provision", Status: "error", Detail: err.Error(),
		})
		xhttp.OkJSON(c, "Test completed with errors", response, "")
		return
	}
	model.GatewayID = gatewayID
	// chat/completions 的 model 字段读的是 GatewayModel，必须是 "<schema>:<gateway_model>"
	model.GatewayModel = req.Schema + ":" + req.Model.GatewayModel
	response.GatewayID = gatewayID
	response.Steps = append(response.Steps, testStepResult{
		Step: "provision", Status: "ok", Detail: map[string]string{
			"gateway_id":    gatewayID,
			"gateway_model": model.GatewayModel,
		},
	})

	// Step 2: Generate (非流式)
	genResp, err := gateway.Current.Generate(ctx, model, req.Messages, nil)
	if err != nil {
		response.Steps = append(response.Steps, testStepResult{
			Step: "generate", Status: "error", Detail: err.Error(),
		})
		// 尝试清理
		_ = gateway.Current.Deprovision(req.Schema, model, provider)
		xhttp.OkJSON(c, "Test completed with errors", response, "")
		return
	}
	response.Steps = append(response.Steps, testStepResult{
		Step: "generate", Status: "ok", Detail: map[string]any{
			"content":    genResp.Content,
			"tool_calls": genResp.ToolCalls,
		},
	})

	// Step 3: Deprovision
	err = gateway.Current.Deprovision(req.Schema, model, provider)
	if err != nil {
		response.Steps = append(response.Steps, testStepResult{
			Step: "deprovision", Status: "error", Detail: err.Error(),
		})
		xhttp.OkJSON(c, "Test completed with errors", response, "")
		return
	}
	response.Steps = append(response.Steps, testStepResult{
		Step: "deprovision", Status: "ok", Detail: "model deleted from gateway",
	})

	xhttp.OkJSON(c, "All steps passed", response, "")
}

// ProvisionTest 单独测试 Provision
func ProvisionTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	var req testRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	if req.Schema == "" {
		req.Schema = "t0"
	}
	if req.Provider.Protocol == "" {
		req.Provider.Protocol = "openai"
	}

	provider := gateway.ProviderInput{
		Name:        req.Provider.Name,
		URL:         req.Provider.URL,
		APIKey:      req.Provider.APIKey,
		Protocol:    req.Provider.Protocol,
		ExtraConfig: req.Provider.ExtraConfig,
	}
	model := gateway.ModelInput{
		Name:          req.Model.Name,
		MaxTokens:     req.Model.MaxTokens,
		Temperature:   req.Model.Temperature,
		ContextWindow: req.Model.ContextWindow,
	}

	gatewayID, err := gateway.Current.Provision(req.Schema, model, provider)
	if err != nil {
		xhttp.ErrJSON(c, "Provision failed: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Provision success", gin.H{
		"gateway_id":    gatewayID,
		"gateway_model": req.Schema + ":" + provider.Name + ":" + model.Name,
	}, "")
}

// DeprovisionTest 单独测试 Deprovision
func DeprovisionTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	var req struct {
		Schema    string `json:"schema,omitempty"`
		GatewayID string `json:"gateway_id"`
		ModelName string `json:"model_name"`
		Provider  struct {
			Name     string `json:"name"`
			Protocol string `json:"protocol,omitempty"`
		} `json:"provider"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	if req.Schema == "" {
		req.Schema = "t0"
	}
	if req.Provider.Protocol == "" {
		req.Provider.Protocol = "openai"
	}

	model := gateway.ModelInput{
		Name:      req.ModelName,
		GatewayID: req.GatewayID,
	}
	provider := gateway.ProviderInput{
		Name:     req.Provider.Name,
		Protocol: req.Provider.Protocol,
	}

	err := gateway.Current.Deprovision(req.Schema, model, provider)
	if err != nil {
		xhttp.ErrJSON(c, "Deprovision failed: "+err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Deprovision success", req.GatewayID, "")
}

// GenerateTest 单独测试 Generate（支持 stream 参数走流式）
func GenerateTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrJSON(c, "Gateway not initialized", nil, "")
		return
	}

	var req struct {
		Schema   string `json:"schema,omitempty"`
		Provider struct {
			Name string `json:"name"`
		} `json:"provider"`
		Model struct {
			Name         string  `json:"name"`
			GatewayModel string  `json:"gateway_model"`
			MaxTokens    int     `json:"max_tokens"`
			Temperature  float64 `json:"temperature"`
		} `json:"model"`
		Messages []gateway.Message `json:"messages"`
		Stream   bool              `json:"stream,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body: "+err.Error(), nil, "")
		return
	}

	if req.Schema == "" {
		req.Schema = "t0"
	}
	if len(req.Messages) == 0 {
		req.Messages = []gateway.Message{
			{Role: "user", Content: "你好，请回复 OK"},
		}
	}

	// chat/completions 的 model 字段读的是 GatewayModel，必须是 "<schema>:<gateway_model>"
	model := gateway.ModelInput{
		Name:         req.Model.Name,
		GatewayModel: req.Schema + ":" + req.Model.GatewayModel,
		MaxTokens:    req.Model.MaxTokens,
		Temperature:  req.Model.Temperature,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	if req.Stream {
		ch, err := gateway.Current.Stream(ctx, model, req.Messages, nil)
		if err != nil {
			xhttp.ErrJSON(c, "Stream failed: "+err.Error(), nil, "")
			return
		}

		// 收集流式结果
		var content string
		var toolCalls []gateway.ToolCall
		for chunk := range ch {
			if chunk.Error != nil {
				xhttp.ErrJSON(c, "Stream error: "+chunk.Error.Error(), nil, "")
				return
			}
			if chunk.Done {
				break
			}
			content += chunk.Content
			if len(chunk.ToolCalls) > 0 {
				toolCalls = chunk.ToolCalls
			}
		}

		xhttp.OkJSON(c, "Stream success", gin.H{
			"content":    content,
			"tool_calls": toolCalls,
		}, "")
		return
	}

	genResp, err := gateway.Current.Generate(ctx, model, req.Messages, nil)
	if err != nil {
		xhttp.ErrJSON(c, "Generate failed: "+err.Error(), nil, "")
		return
	}

	detail := map[string]any{
		"content":    genResp.Content,
		"tool_calls": formatToolCalls(genResp.ToolCalls),
	}
	xhttp.OkJSON(c, "Generate success", detail, "")
}

// formatToolCalls 将 ToolCall 的 Args 格式化为 JSON 字符串便于阅读
func formatToolCalls(calls []gateway.ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		item := map[string]any{
			"id":   tc.ID,
			"name": tc.Name,
		}
		if tc.Args != nil {
			argsJSON, _ := json.Marshal(tc.Args)
			item["args"] = json.RawMessage(argsJSON)
		}
		result = append(result, item)
	}
	return result
}
