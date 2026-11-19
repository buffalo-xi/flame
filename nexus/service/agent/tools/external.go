package tools

import (
	"context"
	"strings"
)

// External tool names (the flat names the model sees, produced by the same rule as toolgrant.ToolNameOfRef):
//
//	mcp__<connection name>__<tool name>
//	rpc__<connection name>__<tool name>
//	local__<endpoint name>
//
// The prefix is mandatory: tool names are global in the model context, and connection-side tool names come from external
// services, so they must stay distinguishable from built-in tools (read_file), plugin tools (web_search) and custom tools
// (custom__*) when the names collide.
const (
	ExternalPrefixMCP   = "mcp__"
	ExternalPrefixRPC   = "rpc__"
	ExternalPrefixLocal = "local__"
)

// IsExternalToolName reports whether a name is an external tool name (mcp / rpc / local).
func IsExternalToolName(name string) bool {
	return strings.HasPrefix(name, ExternalPrefixMCP) ||
		strings.HasPrefix(name, ExternalPrefixRPC) ||
		strings.HasPrefix(name, ExternalPrefixLocal)
}

// ExternalRunner runs the tools of external connections (mcp__ / rpc__); injected by the layer above.
//
// Why it is injected instead of connecting directly here: resolving connection name -> connection record -> protocol is all
// repositories and SDKs, and this package should only know "name -> execution". It follows the same convention as
// PluginRunner (see tool.go), which keeps the tool layer unit-testable and keeps the MCP SDK out of it.
//
// Has only checks the prefix and **does not query the database**: the grant gate has already run in front of this package
// (see ExecuteTool), so all it has to answer is "does a name of this shape belong to me". A missing connection yields an
// explicit error from Invoke.
type ExternalRunner interface {
	Has(name string) bool
	// Invoke runs one external tool call.
	// schema locates the tenant; maxOutput is the byte cap on the returned content (0 = no truncation).
	Invoke(ctx context.Context, schema, toolName string, args map[string]any, maxOutput int) (any, error)
}

// globalExternals is the process-level external tool runner (injected by main / the agent runtime at startup).
var globalExternals ExternalRunner

// SetExternalRunner sets the global external tool runner (called at startup).
func SetExternalRunner(r ExternalRunner) { globalExternals = r }

// CurrentExternalRunner returns the external tool runner in effect; nil when nothing was injected.
func CurrentExternalRunner() ExternalRunner { return globalExternals }

// externalRunner picks the external tool runner this context should use: one injected on the Context wins (handy for tests).
func (c *Context) externalRunner() ExternalRunner {
	if c.Externals != nil {
		return c.Externals
	}
	return globalExternals
}
