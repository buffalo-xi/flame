package tools

import (
	"context"
	"strings"
)

// 外部工具名（模型侧看到的扁平名，由 toolgrant.ToolNameOfRef 的同口径规则生成）：
//
//	mcp__<连接名>__<工具名>
//	rpc__<连接名>__<工具名>
//	local__<端点名>
//
// 前缀是必须的：工具名在模型上下文里是全局的，连接侧的工具名由外部服务给，
// 与内置工具（read_file）、插件工具（web_search）、自定义工具（custom__*）重名时
// 必须能分得开。
const (
	ExternalPrefixMCP   = "mcp__"
	ExternalPrefixRPC   = "rpc__"
	ExternalPrefixLocal = "local__"
)

// IsExternalToolName 判断是否为外部工具名（mcp / rpc / local 三类）。
func IsExternalToolName(name string) bool {
	return strings.HasPrefix(name, ExternalPrefixMCP) ||
		strings.HasPrefix(name, ExternalPrefixRPC) ||
		strings.HasPrefix(name, ExternalPrefixLocal)
}

// ExternalRunner 执行外部连接的工具（mcp__ / rpc__），由上层注入。
//
// 为什么要注入而不是本包直连：连接名 → 连接记录 → 协议的解析全是仓储与 SDK 的活，
// 本包只该知道「名字 → 执行」。与 PluginRunner 同一套惯例（见 tool.go），
// 好处是工具层可以单测，也不会把 MCP SDK 拖进来。
//
// Has 只做前缀判定，**不查库**：授权闸已经在本包前面过了（见 ExecuteTool），
// 这里只需要回答「这个形状的名字归不归我管」。连接不存在由 Invoke 给明确错误。
type ExternalRunner interface {
	Has(name string) bool
	// Invoke 执行一次外部工具调用。
	// schema 用于定位租户；maxOutput 是返回内容的字节上限（0 = 不截断）。
	Invoke(ctx context.Context, schema, toolName string, args map[string]any, maxOutput int) (any, error)
}

// globalExternals 进程级外部工具执行器（启动时由 main / agent 运行时注入）。
var globalExternals ExternalRunner

// SetExternalRunner 设置全局外部工具执行器（启动时调用）。
func SetExternalRunner(r ExternalRunner) { globalExternals = r }

// CurrentExternalRunner 返回当前生效的外部工具执行器；未注入时为 nil。
func CurrentExternalRunner() ExternalRunner { return globalExternals }

// externalRunner 取本上下文应使用的外部工具执行器：Context 上显式注入的优先（便于单测）。
func (c *Context) externalRunner() ExternalRunner {
	if c.Externals != nil {
		return c.Externals
	}
	return globalExternals
}
