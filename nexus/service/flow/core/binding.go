package flow

// OutputBinding 定义输出变量到 channel 的流式绑定。
// 一个变量可配置多个 binding（如同时绑 result + progress）。
type OutputBinding struct {
	Channel string `json:"channel"` // "result" | "progress"
	// Category 只对 progress 通道有意义：这条外发属于哪一类
	// （"text" | "thinking"，取值与 agent/core 的 Category* 同值），缺省 text。
	// 它是 progress 通道的一个属性，不是第三个通道。
	Category string `json:"category,omitempty"`
	// Stream 只对 result 通道有意义：false = 只累积到最终结果，不逐条外发。
	// 用指针是为了区分"没配这个字段"（旧流程）与"显式关掉"——nil 视为 true，保持存量行为。
	Stream    *bool             `json:"stream,omitempty"`
	Transform *BindingTransform `json:"transform,omitempty"` // 可选的脚本转换
}

// StreamEnabled 该 binding 是否逐条外发（缺省 true）。
func (b *OutputBinding) StreamEnabled() bool {
	return b == nil || b.Stream == nil || *b.Stream
}

// CategoryOrText 返回到进度通道的类别，缺省 text。
func (b *OutputBinding) CategoryOrText() string {
	if b == nil || b.Category == "" {
		return "text"
	}
	return b.Category
}

// BindingTransform 定义 binding 的脚本转换配置。
type BindingTransform struct {
	Lang    string                 `json:"lang"`              // "template" | "lua" | "python" | "js"
	Script  string                 `json:"script"`            // 脚本内容
	Inputs  map[string]interface{} `json:"inputs,omitempty"`  // 脚本输入变量定义（与 code 节点 inputs 同格式）
	Timeout *BindingTimeout        `json:"timeout,omitempty"` // 超时控制
}

// BindingTimeout 超时控制（与 code 节点 timeout 同结构）
type BindingTimeout struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
}
