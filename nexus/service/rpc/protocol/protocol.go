// Package protocol 定义自研 RPC 的协议版本与信封结构。
// server / client 均引用本包，避免版本常量与结构在两处漂移。
package protocol

// CurrentProtocolVersion 当前协议主版本号。
// 仅在不兼容变更（删字段/改响应结构/改错误语义）时 +1。
//
// 「新增可选字段」不升版：尚未实现的一方会忽略未知字段，行为退化为「不订阅进度」，
// 调用仍然成功 —— 属兼容变更。params._meta.progressToken 与通知里的
// progressToken / progress / total 都是这样进来的，所以版本仍是 1。
const CurrentProtocolVersion = 1

// MinSupportedVersion 向后兼容的最低主版本号。
// 小于该版本的客户端将被明确拒绝，而非静默降级。
const MinSupportedVersion = 1

// ErrCodeInvalidParams JSON-RPC 2.0 标准错误码（参数错误），与 MCP 版本不匹配时复用。
const ErrCodeInvalidParams = -32602

// Request 客户端发来的 JSON-RPC 请求信封。
type Request struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version,omitempty"` // 协议主版本号；缺省(0)按 MinSupportedVersion 处理
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      interface{} `json:"id"`
}

// VersionError 协议版本不支持时的错误 data 结构（对齐 MCP 的 supported 反馈）。
type VersionError struct {
	Requested int   `json:"requested"`
	Supported []int `json:"supported"`
}

// NormalizeVersion 归一版本号：未传(0)按 MinSupportedVersion 处理。
func NormalizeVersion(v int) int {
	if v == 0 {
		return MinSupportedVersion
	}
	return v
}

// IsVersionSupported 判断版本是否在服务端支持范围内。
func IsVersionSupported(v int) bool {
	return v >= MinSupportedVersion && v <= CurrentProtocolVersion
}
