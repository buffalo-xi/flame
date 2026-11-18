package tools

import "context"

// EndpointRunner 执行本租户本地端点工具（local__<端点名>），由 agent 包注入。
//
// 为什么不和 ExternalRunner 合并：mcp__ / rpc__ 是「连出去」，要解析连接记录与协议；
// 本地端点是「发出去」的端点，进程内跑 flow，且必须带调用方的 user / session
// 才能让 flow 落在同一个场地上。两者的身份透传需求不同，混在一起会让接口长成
// 「一半参数对一半实现无意义」的形状。
//
// 与 ExternalRunner 一样：Has 只做前缀判定、不查库（授权闸在前面已经过了），
// 端点不存在 / 已被删除由 Invoke 给明确错误。
type EndpointRunner interface {
	Has(name string) bool
	Invoke(ctx context.Context, in LocalCall) (any, error)
}

// LocalCall 一次本地端点调用的入参。
//
// 身份字段全部来自 tools.Context（Schema / UserID / SessionID / RunID），
// 目标是让 flow 复用调用方的租户与会话 —— 与 call_agent 透传 session_id 同口径。
type LocalCall struct {
	Schema    string
	AgentName string
	AgentID   string
	UserID    string
	SessionID string
	RunID     string
	// CallDepth 调用方 agent 的当前栈深度（顶层 0）。本地端点执行时以 +1 下传，
	// 用来拦住 agent → flow → agent 的互相调用（平级工具调用不累加）。
	CallDepth int
	// CallChain 调用方当前的祖先链（不含本次目标）：本地端点执行时原样下传，
	// 由端点侧追加「即将执行的实体」后再交给 agent / flow。
	CallChain []string
	ToolName  string // local__<端点名>
	Args      map[string]any
	MaxOutput int
	// DefinitionMode 调用方 agent 的读定义模式：调试中的 agent 调本地端点时，
	// 被跑的 flow 也要跟随草稿（否则调试链上出现「一半草稿、一半线上」）。
	DefinitionMode DefinitionMode
}

// endpointRunner 取本上下文应使用的本地端点执行器（Context 显式注入优先，便于单测）。
func (c *Context) endpointRunner() EndpointRunner {
	if c == nil {
		return nil
	}
	return c.Endpoints
}
