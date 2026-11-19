package gatewaytest

import (
	"context"
	"encoding/json"
	"time"

	"flame/nexus/service/llm/gateway"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// testRequest is the test request body
type testRequest struct {
	// Provider config
	Provider struct {
		Name     string `json:"name"`     // provider name, e.g. openai, deepseek
		URL      string `json:"url"`      // API endpoint
		APIKey   string `json:"api_key"`  // API key
		Protocol string `json:"protocol"` // protocol, e.g. openai
		// ExtraConfig is passed through into litellm_params verbatim
		// (api_version / vertex_project / vertex_location / vertex_credentials ...)
		ExtraConfig map[string]any `json:"extra_config,omitempty"`
	} `json:"provider"`
	// Model config
	Model struct {
		Name          string  `json:"name"`           // model name, e.g. gpt-4o-mini, deepseek-chat
		GatewayModel  string  `json:"gateway_model"`  // gateway model name, used both as the registration name and the chat request's model field
		MaxTokens     int     `json:"max_tokens"`     // max tokens
		Temperature   float64 `json:"temperature"`    // temperature
		ContextWindow int     `json:"context_window"` // context window
	} `json:"model"`
	// Messages are the chat messages used by the test (optional, defaults to a simple greeting)
	Messages []gateway.Message `json:"messages,omitempty"`
	// Schema is the tenant identifier (optional, defaults to "test")
	Schema string `json:"schema,omitempty"`
}

// testStepResult is the result of one step
type testStepResult struct {
	Step   string `json:"step"`
	Status string `json:"status"` // "ok" | "error"
	Detail any    `json:"detail,omitempty"`
}

// testResponse is the test response body
type testResponse struct {
	Steps     []testStepResult `json:"steps"`
	GatewayID string           `json:"gateway_id,omitempty"`
}

// RunGatewayTest runs the full LiteLLM gateway test: Provision -> Generate -> Deprovision
func RunGatewayTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	var req testRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	// Defaults
	if req.Schema == "" {
		req.Schema = "t0"
	}
	if req.Provider.Protocol == "" {
		req.Provider.Protocol = "openai"
	}
	if len(req.Messages) == 0 {
		req.Messages = []gateway.Message{
			{Role: "user", Content: "Hello, reply with OK only and nothing else."},
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
	// The model field of chat/completions reads GatewayModel and must be "<schema>:<gateway_model>"
	model.GatewayModel = req.Schema + ":" + req.Model.GatewayModel
	response.GatewayID = gatewayID
	response.Steps = append(response.Steps, testStepResult{
		Step: "provision", Status: "ok", Detail: map[string]string{
			"gateway_id":    gatewayID,
			"gateway_model": model.GatewayModel,
		},
	})

	// Step 2: Generate (non-streaming)
	genResp, err := gateway.Current.Generate(ctx, model, req.Messages, nil)
	if err != nil {
		response.Steps = append(response.Steps, testStepResult{
			Step: "generate", Status: "error", Detail: err.Error(),
		})
		// Try to clean up
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

// ProvisionTest tests Provision on its own
func ProvisionTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
		return
	}

	var req testRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Provision failed: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Provision success", gin.H{
		"gateway_id":    gatewayID,
		"gateway_model": req.Schema + ":" + provider.Name + ":" + model.Name,
	}, "")
}

// DeprovisionTest tests Deprovision on its own
func DeprovisionTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Deprovision failed: "+err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Deprovision success", req.GatewayID, "")
}

// GenerateTest tests Generate on its own (the stream flag switches to streaming)
func GenerateTest(c *gin.Context) {
	if gateway.Current == nil {
		xhttp.ErrCode(c, "common.internal", nil, "gateway not initialized", "")
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
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body: "+err.Error(), "")
		return
	}

	if req.Schema == "" {
		req.Schema = "t0"
	}
	if len(req.Messages) == 0 {
		req.Messages = []gateway.Message{
			{Role: "user", Content: "Hello, reply with OK."},
		}
	}

	// The model field of chat/completions reads GatewayModel and must be "<schema>:<gateway_model>"
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
			xhttp.ErrCode(c, "common.internal", nil, "Stream failed: "+err.Error(), "")
			return
		}

		// Collect the streamed result
		var content string
		var toolCalls []gateway.ToolCall
		for chunk := range ch {
			if chunk.Error != nil {
				xhttp.ErrCode(c, "common.internal", nil, "Stream error: "+chunk.Error.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Generate failed: "+err.Error(), "")
		return
	}

	detail := map[string]any{
		"content":    genResp.Content,
		"tool_calls": formatToolCalls(genResp.ToolCalls),
	}
	xhttp.OkJSON(c, "Generate success", detail, "")
}

// formatToolCalls renders ToolCall.Args as JSON strings so they are readable
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
