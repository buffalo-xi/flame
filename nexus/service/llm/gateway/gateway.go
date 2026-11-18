package gateway

import "context"

// ModelInput 模型输入参数
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

// ProviderInput 供应商输入参数
type ProviderInput struct {
	ID       string
	Name     string
	URL      string
	APIKey   string
	Protocol string
	// ExtraConfig 服务商的额外配置（对应 llm_providers.extra_config），原样透传进
	// LiteLLM 的 litellm_params。用于 api_version（Azure）、vertex_project / vertex_location /
	// vertex_credentials（Vertex AI）、timeout、max_retries 等任意 LiteLLM 支持的参数。
	ExtraConfig map[string]any
}

// Message 消息
type Message struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Name       string           `json:"name,omitempty"`         // tool 消息的工具名
	ToolCallID string           `json:"tool_call_id,omitempty"` // tool 消息的调用 ID
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`   // assistant 消息的工具调用（OpenAI 格式）
}

// OpenAIToolCall OpenAI 协议的工具调用（assistant 消息回填用）。
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
	// FromText 标记该调用来自正文里的「文本形态工具调用」（如 DeepSeek 的 |DSML| 标记），
	// 而非上游返回的结构化 tool_calls。上游既然不会返回结构化调用，也就多半不认
	// assistant.tool_calls / role:"tool" 的回填，必须改走纯文本回填（见 agentImpl.Stream）。
	FromText bool
}

// ToolDef 工具定义
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// StreamChunk 流式响应块
type StreamChunk struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCall
	Error            error
	Done             bool
}

// GenerateResponse 非流式响应
type GenerateResponse struct {
	Content string
	// ReasoningContent 思考内容（模型支持 reasoning 时才有值），
	// 与 StreamChunk.ReasoningContent 对齐：非流式模式不能丢掉推理内容。
	ReasoningContent string
	ToolCalls        []ToolCall
}

// ProvisionedModel 网关上已注册的一条模型记录，供同步（配置比对 / 清理多余）使用。
type ProvisionedModel struct {
	// GatewayID 网关侧主键（LiteLLM 的 model_id UUID），对应 DB 的 gateway_id
	GatewayID string
	// ModelName 网关上的模型名，形如 "<schema>:<gateway_model>"
	ModelName string
	// LitellmParams 网关实际保存的调用参数（model / api_key / api_base 等）
	LitellmParams map[string]any
	// ModelInfo 网关实际保存的模型元信息（context_window / max_tokens 等）
	ModelInfo map[string]any
}

// Client LLM 网关客户端接口
type Client interface {
	// Provision 检查模型是否已在网关注册，未注册则注册；返回 gatewayID
	Provision(schema string, model ModelInput, provider ProviderInput) (gatewayID string, err error)
	// Deprovision 从网关删除模型
	Deprovision(schema string, model ModelInput, provider ProviderInput) error
	// DeprovisionAll 禁用（删除）某 schema 下所有已注册到网关的模型，返回实际删除的数量
	DeprovisionAll(schema string) (int, error)
	// ListProvisioned 列出某 schema 下已注册到网关的模型（按 "<schema>:" 前缀过滤）
	ListProvisioned(schema string) ([]ProvisionedModel, error)
	// Update 按 GatewayID 覆盖更新已注册模型的参数，用于把本地配置对齐到网关
	Update(schema string, model ModelInput, provider ProviderInput) error
	// Stream 流式调用
	Stream(ctx context.Context, model ModelInput, messages []Message, tools []ToolDef) (<-chan StreamChunk, error)
	// Generate 非流式调用
	Generate(ctx context.Context, model ModelInput, messages []Message, tools []ToolDef) (GenerateResponse, error)
}

// Current 全局网关客户端实例
var Current Client

// SetCurrent 设置全局网关客户端
func SetCurrent(c Client) {
	Current = c
}
