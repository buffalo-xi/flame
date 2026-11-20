package flow

// OutputBinding declares a streaming binding from an output variable to a channel.
// One variable may carry several bindings (for example result + progress at the same time).
type OutputBinding struct {
	Channel string `json:"channel"` // "result" | "progress"
	// Category is only meaningful for the progress channel: which class this outbound frame
	// belongs to ("text" | "thinking", same values as the Category* constants of agent/core),
	// defaulting to text. It is a property of the progress channel, not a third channel.
	Category string `json:"category,omitempty"`
	// Stream is only meaningful for the result channel: false = accumulate into the final result
	// only, without emitting each frame. The pointer distinguishes "field absent" (older flows)
	// from "explicitly turned off"; nil counts as true so existing behaviour is preserved.
	Stream    *bool             `json:"stream,omitempty"`
	Transform *BindingTransform `json:"transform,omitempty"` // optional script transform
}

// StreamEnabled reports whether this binding emits each frame (default true).
func (b *OutputBinding) StreamEnabled() bool {
	return b == nil || b.Stream == nil || *b.Stream
}

// CategoryOrText returns the category used on the progress channel, defaulting to text.
func (b *OutputBinding) CategoryOrText() string {
	if b == nil || b.Category == "" {
		return "text"
	}
	return b.Category
}

// BindingTransform is the script transform configuration of a binding.
type BindingTransform struct {
	Lang    string                 `json:"lang"`              // "template" | "lua" | "python" | "js"
	Script  string                 `json:"script"`            // script body
	Inputs  map[string]interface{} `json:"inputs,omitempty"`  // script inputs (same shape as the code node inputs)
	Timeout *BindingTimeout        `json:"timeout,omitempty"` // timeout control
}

// BindingTimeout is the timeout control (same shape as the code node timeout).
type BindingTimeout struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
}
