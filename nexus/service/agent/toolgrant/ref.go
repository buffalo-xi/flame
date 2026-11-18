// Package toolgrant 实现工具授权体系的解析与展开：
//   - ref 语法解析与校验（ValidateRef / ParseRef）
//   - 可引用目录（Catalog：天然组、预设组、工具组、单个工具）
//   - 最终工具集解析（Resolver.Resolve：required ∪ 按需注入的候选 − deny）
//
// ref 语法（统一 type:subject[:member]，用 SplitN(ref, ":", 3) 解析，
// 第三段原样保留，容忍工具名里含 ':'）：
//
//	builtin:file | builtin:exec | builtin:shell | builtin:browser | builtin:agent | builtin:skill | builtin:super   （内置类组）
//	builtin:<toolName>                                                            （单个内置工具）
//	mcp:<connName>[:<toolName>]                                                   （外部 MCP 连接）
//	rpc:<connName>[:<toolName>]                                                   （外部 RPC 连接）
//	local:rpc[:<endpointName>]                                                    （本地端点，仅 rpc 协议 + 仅工作流端点）
//	plugin:<toolName>                                                             （磁盘 Python 插件，一工具一 ref）
//	custom:<toolName>                                                             （租户自定义自定义工具，一工具一 ref）
//	group:<groupName>                                                             （工具组，租户自定义）
//	preset:<presetName>                                                           （预设组，代码内置）
//
// 历史别名：custom:<groupName> 是 group:<groupName> 的旧写法，只在 ParseRef 里识别
// （读兼容），任何写路径都不再产出它。详见 LegacyScopeCustom 的注释。
package toolgrant

import (
	"fmt"
	"regexp"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// ref 前缀（scope）。
const (
	ScopeBuiltin = "builtin" // 内置工具
	ScopeMCP     = "mcp"     // 外部 MCP 连接
	ScopeRPC     = "rpc"     // 外部 RPC 连接
	ScopeLocal   = "local"   // 本地端点（仅 rpc 协议）
	ScopePlugin  = "plugin"  // 磁盘 Python 插件（一个工具一个 ref，不接受第三段）
	ScopeCustom  = "custom"  // 租户自定义自定义工具（一个工具一个 ref，不接受第三段）
	ScopeGroup   = "group"   // 工具组（租户自定义，组名即 ref 的 subject）
	ScopePreset  = "preset"  // 预设组（代码内置）
)

// LocalProtocolRPC 本地端点当前唯一开放的协议（其余协议只用于对外暴露，不进授权体系）。
const LocalProtocolRPC = "rpc"

// LocalTargetFlow 本地端点中唯一进授权体系的目标类型（工作流）。
//
// agent 端点不进工具目录，两条理由：
//  1. agent 之间的调用已有受控通道 call_agent —— agent_relations（无行即拒绝）+
//     IsRunnable + 「main 不可被其它 agent 调用」；而端点内部直调（flow 的 rpc 节点
//     target_type=endpoint）这些闸门一概不经，做成工具等于开后门；
//  2. 端点名恒等于实体名（svcendpoints.Create 强制），agent 与 flow 同名时
//     local:rpc:<name> 的 ref 与 local__<name> 的工具名都会撞车（map 静默覆盖）。
//
// 与 svcendpoints 的字面量 "flow" 对齐（该仓校验 target_type ∈ {agent, flow}），有测试守住。
const LocalTargetFlow = "flow"

// MaxRefLength ref 最大长度（与 DB 列 varchar(160) 对齐）。
const MaxRefLength = 160

// namePattern 连接名 / 端点名 / 组名 / 工具名的允许字符（宽松：字母数字与 _ - .）。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ParsedRef 解析后的引用。
type ParsedRef struct {
	Scope   string // builtin | mcp | rpc | local | plugin | script | group | preset
	Subject string // builtin: 类别名或工具名；mcp/rpc: 连接名；local: 协议名；group/preset: 组名
	Member  string // mcp/rpc: 工具名；local: 端点名；其余为空
}

// ParseRef 解析 ref；语法非法返回错误。
// 注意：这里只做格式解析，不校验"该连接/工具是否存在"（由 Catalog 负责）。
func ParseRef(ref string) (*ParsedRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("ref is empty")
	}
	if len(ref) > MaxRefLength {
		return nil, fmt.Errorf("ref too long (max %d)", MaxRefLength)
	}
	parts := strings.SplitN(ref, ":", 3)
	p := &ParsedRef{Scope: parts[0]}
	if len(parts) > 1 {
		p.Subject = parts[1]
	}
	if len(parts) > 2 {
		p.Member = parts[2]
	}

	switch p.Scope {
	case ScopeBuiltin, ScopeMCP, ScopeRPC, ScopeLocal, ScopePlugin, ScopeCustom, ScopeGroup, ScopePreset:
	default:
		return nil, fmt.Errorf("unsupported ref scope %q", p.Scope)
	}
	if p.Subject == "" {
		return nil, fmt.Errorf("ref %q missing subject", ref)
	}
	if !namePattern.MatchString(p.Subject) {
		return nil, fmt.Errorf("invalid subject in ref %q", ref)
	}
	if p.Member != "" && !namePattern.MatchString(p.Member) {
		return nil, fmt.Errorf("invalid member in ref %q", ref)
	}
	// 本地端点只开放 rpc 协议
	if p.Scope == ScopeLocal && p.Subject != LocalProtocolRPC {
		return nil, fmt.Errorf("local endpoint protocol %q is not allowed (only %q)", p.Subject, LocalProtocolRPC)
	}
	// 这些 scope 不支持第三段。plugin 也是：一个工具一个 ref，实现由租户配置决定，
	// 不进授权面（给 agent 授权「用 web_search」而不是「用 tavily」）。
	if (p.Scope == ScopeBuiltin || p.Scope == ScopePlugin || p.Scope == ScopeCustom ||
		p.Scope == ScopeGroup || p.Scope == ScopePreset) && p.Member != "" {
		return nil, fmt.Errorf("ref %q does not accept a third segment", ref)
	}
	return p, nil
}

// ValidateRef 校验 ref 是否可作为授权项 / 组成员使用。
// allowGroup=false 时拒绝 group:（组成员不允许引用工具组，避免嵌套）。
func ValidateRef(ref string, allowGroup bool) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if !allowGroup && p.Scope == ScopeGroup {
		return fmt.Errorf("tool group reference is not allowed here")
	}
	return nil
}

// IsGroup 判断该 ref 是否为「组」形态：
// builtin 类别、mcp/rpc 连接（无 member）、local:rpc（无 member）、group:、preset:。
// builtin 需要额外传入内置类别集合来区分「类别」与「工具」。
func (p *ParsedRef) IsGroup(builtinCategories map[string]bool) bool {
	switch p.Scope {
	case ScopeGroup, ScopePreset:
		return true
	case ScopeMCP, ScopeRPC:
		return p.Member == ""
	case ScopeLocal:
		return p.Member == ""
	case ScopePlugin:
		// 插件工具只有单工具 ref：组与工具合一，授权时一律按 tool 写
		return false
	case ScopeBuiltin:
		return p.Member == "" && builtinCategories[p.Subject]
	}
	return false
}

// ItemTypeOfRef 由 ref 形态推导 item_type（组 → models.GrantItemGroup，单工具 → models.GrantItemTool）。
//
// 所有写入口（元工具、管理端 agent 授权 / skill 依赖 / 组成员的维护）都必须用它推导，
// **不接受调用方传入**。原因：调用方同时给 ref 与 item_type 时两者很容易矛盾
// （item_type=tool + ref=group:x，或 item_type=group + ref=builtin:read_file），
// 矛盾行不会报错，只会静默污染前端的勾选态 —— 读的人还以为数据坏了。
//
// 大模型侧的元工具本来就不暴露 item_type 参数，这里再收口一次，保证两条入口同源。
func ItemTypeOfRef(ref string) string {
	p, err := ParseRef(ref)
	if err != nil {
		return models.GrantItemTool
	}
	if p.IsGroup(BuiltinCategories()) {
		return models.GrantItemGroup
	}
	return models.GrantItemTool
}

// ToolNameOfRef 返回该 ref 在 LLM 工具列表中的名字（运行时绑定用）：
//
//	builtin:read_file        -> read_file
//	mcp:fs:read_file         -> mcp__fs__read_file
//	rpc:erp:create_order     -> rpc__erp__create_order
//	local:rpc:weather        -> local__weather
//	plugin:web_search        -> web_search
//
// 组形态的 ref 没有单一工具名，返回空串。
func ToolNameOfRef(ref string) string {
	p, err := ParseRef(ref)
	if err != nil {
		return ""
	}
	switch p.Scope {
	case ScopeBuiltin:
		return p.Member
	case ScopeMCP, ScopeRPC:
		if p.Member == "" {
			return ""
		}
		return fmt.Sprintf("%s__%s__%s", p.Scope, p.Subject, p.Member)
	case ScopeLocal:
		if p.Member == "" {
			return ""
		}
		return "local__" + p.Member
	case ScopePlugin:
		// 插件工具名就是它自己（不加前缀）：模型看到的必须是干净的 web_search
		return p.Subject
	case ScopeCustom:
		// 自定义工具加前缀：它的名字来自租户，必须和内建/插件/插件实现名隔开
		return tools.CustomToolPrefix + p.Subject
	}
	return ""
}
