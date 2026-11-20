package gateway

import "context"

// ModelInput holds the model input parameters.
type ModelInput struct {
	ID            string
	Name          string
	GatewayModel  string
	GatewayID     string
	ContextWindow int
	MaxTokens     int
	Temperature   float64
	TopP          float64
	ExtraParams   map[string]any
}

// ProviderInput holds the provider input parameters.
type ProviderInput struct {
	ID       string
	Name     string
	URL      string
	APIKey   string
	Protocol string
	// ExtraConfig is the provider's extra config (llm_providers.extra_config), passed through as is into
	// the litellm_params of LiteLLM. It carries any parameter LiteLLM supports, such as api_version
	// (Azure), vertex_project / vertex_location / vertex_credentials (Vertex AI), timeout, max_retries.
	ExtraConfig map[string]any
}

// Message is a chat message.
type Message struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Name       string           `json:"name,omitempty"`         // the tool name of a tool message
	ToolCallID string           `json:"tool_call_id,omitempty"` // the call id of a tool message
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`   // tool calls of an assistant message (OpenAI format)
}

// OpenAIToolCall is a tool call in the OpenAI protocol (used to echo back in assistant messages).
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolCall is a tool call.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
	// FromText marks a call that came from a "text-shaped tool call" in the body (such as the |DSML|
	// markers of DeepSeek) rather than from structured tool_calls returned upstream. An upstream that
	// does not return structured calls most likely does not understand assistant.tool_calls /
	// role:"tool" echoes either, so the echo has to go back as plain text (see agentImpl.Stream).
	FromText bool
}

// ToolDef is a tool definition.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// StreamChunk is one chunk of a streaming response.
type StreamChunk struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCall
	Error            error
	Done             bool
}

// GenerateResponse is a non-streaming response.
type GenerateResponse struct {
	Content string
	// ReasoningContent is the thinking content (present when the model supports reasoning), aligned
	// with StreamChunk.ReasoningContent: non-streaming mode must not drop the reasoning either.
	ReasoningContent string
	ToolCalls        []ToolCall
}

// ProvisionedModel is one model record already registered on the gateway, used by the sync (comparing
// config and cleaning up leftovers).
type ProvisionedModel struct {
	// GatewayID is the gateway-side primary key (the model_id UUID of LiteLLM), matching gateway_id in the DB.
	GatewayID string
	// ModelName is the model name on the gateway, shaped "<schema>:<gateway_model>".
	ModelName string
	// LitellmParams are the call parameters actually stored on the gateway (model / api_key / api_base ...).
	LitellmParams map[string]any
	// ModelInfo is the model metadata actually stored on the gateway (context_window / max_tokens ...).
	ModelInfo map[string]any
}

// Client is the LLM gateway client interface.
type Client interface {
	// Provision checks whether the model is registered on the gateway and registers it when it is not;
	// it returns the gatewayID.
	Provision(schema string, model ModelInput, provider ProviderInput) (gatewayID string, err error)
	// Deprovision removes the model from the gateway.
	Deprovision(schema string, model ModelInput, provider ProviderInput) error
	// DeprovisionAll disables (removes) every model of a schema registered on the gateway and returns
	// how many were actually removed.
	DeprovisionAll(schema string) (int, error)
	// ListProvisioned lists the models of a schema registered on the gateway (filtered by the "<schema>:" prefix).
	ListProvisioned(schema string) ([]ProvisionedModel, error)
	// Update overwrites the parameters of a registered model by GatewayID, aligning the gateway with the
	// local configuration.
	Update(schema string, model ModelInput, provider ProviderInput) error
	// Stream is a streaming call.
	Stream(ctx context.Context, model ModelInput, messages []Message, tools []ToolDef) (<-chan StreamChunk, error)
	// Generate is a non-streaming call.
	Generate(ctx context.Context, model ModelInput, messages []Message, tools []ToolDef) (GenerateResponse, error)
}

// Current is the global gateway client instance.
var Current Client

// SetCurrent sets the global gateway client.
func SetCurrent(c Client) {
	Current = c
}
