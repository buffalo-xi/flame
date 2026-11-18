package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"flame/nexus/shared/callchain"
	"flame/nexus/shared/capability"
)

var (
	toolMap = make(map[string]Tool)
	mu      sync.RWMutex
)

// ErrToolNotFound 工具未注册。
var ErrToolNotFound = errors.New("tool not found")

// Register 注册内置工具（各工具文件 init() 调用）。
func Register(t Tool) {
	mu.Lock()
	defer mu.Unlock()
	toolMap[t.Meta().Name] = t
}

// Get 按名字获取工具。
func Get(name string) (Tool, bool) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := toolMap[name]
	return t, ok
}

// List 返回所有已注册工具。
func List() []Tool {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Tool, 0, len(toolMap))
	for _, t := range toolMap {
		out = append(out, t)
	}
	return out
}

// Names 返回所有已注册工具名（升序，便于稳定输出与比对）。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(toolMap))
	for name := range toolMap {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ExecuteTool 统一调度：授权硬闸 → 内置工具 → 插件工具 → 外部连接工具 → 自定义工具 → not found。
//
// 顺序是刻意的：**先用名字过授权闸**，再去找工具。否则一个未授权的插件工具名
// 会先被交给插件执行器（起 python 进程、花租户的检索配额），被拒之前代价已经付了。
func ExecuteTool(ctx context.Context, c *Context, name string, args map[string]any) (any, error) {
	if c == nil {
		return nil, errors.New("tool context is nil")
	}
	if args == nil {
		args = map[string]any{}
	}

	// 授权硬闸门：AllowedTools 非 nil 时只放行本 agent 被授权的工具。
	// 与「声明给模型的 tools」同源 —— 否则文本形态工具调用（模型把调用写进正文，
	// 如 DeepSeek 的 |DSML| 标记）会绕过工具授权体系：模型即便没被声明过某工具，
	// 也能靠写出标记把它执行掉。
	if c.AllowedTools != nil && !c.AllowedTools[name] {
		return nil, fmt.Errorf("tool %s is not granted to agent %s", name, c.AgentName)
	}

	if tool, ok := Get(name); ok {
		return executeBuiltin(ctx, c, tool, name, args)
	}

	// 插件工具：名字不进本包的工具表（运行时从磁盘发现），交给注入的执行器。
	// 参数校验在插件侧按 manifest 的 schema 做（这里拿不到 schema）。
	if runner := c.pluginRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		return runner.Invoke(ctx, c.Schema, name, args)
	}

	// 外部连接工具（mcp__<conn>__<tool> / rpc__<conn>__<tool>）：连接名 → 连接记录 →
	// 调用协议的解析全在实现方（仓储 + SDK），这里只按名字转出去。
	// 放在授权闸之后：未授权的名字连库都不会查。
	if runner := c.externalRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		// 祖先链经 ctx 交给执行器：rpc__ 出站时写进 _meta.call_chain，
		// mcp__ 忽略它（MCP 侧没有这套约定）。业务签名不必再加一个参数。
		ctx = callchain.With(ctx, c.CallChain)
		return runner.Invoke(ctx, c.Schema, name, args, c.MaxOutputLimit())
	}

	// 本地端点（local__<端点名>）：进程内跑 flow，不经过任何网络连接。
	// 与 mcp__/rpc__ 分开注入：那两类要解析连接记录，这一类要复用调用方的会话身份。
	// 放在授权闸之后：未授权的名字连端点表都不会查。
	if runner := c.endpointRunner(); runner != nil && runner.Has(name) {
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		return runner.Invoke(ctx, LocalCall{
			Schema:    c.Schema,
			AgentName: c.AgentName,
			AgentID:   c.AgentID,
			UserID:    c.UserID,
			SessionID: c.SessionID,
			RunID:     c.RunID,
			CallDepth: c.CallDepth,
			CallChain: c.CallChain,
			ToolName:  name,
			Args:      args,
			MaxOutput: c.MaxOutputLimit(),
			// 子调用模式：由 ChildMode 统一算（自身草稿 && 跟随草稿开关），
			// 不能直接传 c.DefinitionMode —— 那样开关关掉时本地 flow 仍会读草稿。
			DefinitionMode: c.ChildMode(),
		})
	}

	// 自定义工具：命名空间固定 custom__<name>，与内置/插件不重叠。
	// 参数在 executeCustomTool 里按 output/必填自行处理（这里同样拿不到 Meta）。
	if IsCustomToolName(name) {
		return executeCustomTool(ctx, c, name, args)
	}

	return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
}

// executeBuiltin 执行一个内置工具：身份硬闸 → 参数归位与必填校验 → 包超时 → 执行。
func executeBuiltin(ctx context.Context, c *Context, tool Tool, name string, args map[string]any) (any, error) {
	// 元工具硬闸门：管理类工具（分类落在 supervisorOnlyCategories 里）仅 supervisor 可用。
	// 授权目录已按身份过滤，这里是执行层纵深防御 —— 即便授权被误配或有历史残留，非 supervisor 也执行不了。
	if IsSupervisorOnlyTool(name) && !c.IsSupervisor {
		return nil, fmt.Errorf("tool %s is restricted to supervisor agents", name)
	}

	meta := tool.Meta()

	// 能力硬闸门：平台没给这个租户开通的能力，执行层直接拒。
	// 目录置灰只是提示 —— 已绑定该工具的 agent 仍持有旧工具清单，必须在这里兜住。
	if key := CapabilityOf(meta.Category); key != "" && c.Caps != nil && !c.Caps[key] {
		return nil, fmt.Errorf("工具 %s 需要能力 %s（%s），平台未给本租户开通", name, key, capability.Label(key))
	}

	// 参数别名归位：把模型自造的参数名（如 skill_name）映射回 schema 规范名（name）
	normalizeArgs(meta, args)
	if err := validateRequired(meta, args); err != nil {
		return nil, err
	}

	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	return tool.Execute(ctx, c, args)
}

// argAliases 常见「模型自造参数名」→ 工具 schema 规范名 的兼容映射。
// 规则：仅当工具**确实声明**了规范名（properties 里有它）且入参缺少它时，才把来源名重命名过去。
// 因此不会动到工具自己的合法参数 —— call_agent 的 agent_name、bind_skill 的 agent/skill
// 都是规范名，不参与映射（它们的 properties 里没有 name / path）。
var argAliases = map[string][]string{
	"name":     {"skill_name", "agent_name", "tool_name", "skill"},
	"path":     {"file_path", "filepath", "filename", "file"},
	"dir":      {"dir_path", "directory", "folder"},
	"agent_md": {"agent_markdown"},
	"skill_md": {"skill_markdown"},
}

// normalizeArgs 按 schema 声明的属性把别名参数归位到规范名；找不到来源则原样返回。
func normalizeArgs(meta ToolMeta, args map[string]any) {
	if len(args) == 0 {
		return
	}
	props, _ := meta.Parameters["properties"].(map[string]any)
	for canonical, aliases := range argAliases {
		if props != nil {
			if _, declared := props[canonical]; !declared {
				continue // 该工具没有这个规范参数，不映射
			}
		}
		if _, ok := args[canonical]; ok {
			continue // 已给规范名，不覆盖
		}
		for _, alias := range aliases {
			if v, ok := args[alias]; ok {
				args[canonical] = v
				delete(args, alias)
				break
			}
		}
	}
}

// validateRequired 校验 args 是否包含 schema 声明的 required 字段。
// 报错里带上「实收参数」与该工具「期望的参数」，便于模型自我纠正。
func validateRequired(meta ToolMeta, args map[string]any) error {
	required, _ := meta.Parameters["required"].([]any)
	var missing []string
	for _, r := range required {
		key, _ := r.(string)
		if key == "" {
			continue
		}
		if _, ok := args[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("tool %s: missing required argument(s) %s; received %s; expected params: %s",
		meta.Name, quoteList(missing), mapKeys(args), propKeys(meta))
}

// quoteList 渲染成 ["a", "b"] 形式。
func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// mapKeys 返回 map 的键（升序，便于稳定输出）。
func mapKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return quoteList(keys)
}

// propKeys 返回 schema 声明的属性名（升序）。
func propKeys(meta ToolMeta) string {
	props, _ := meta.Parameters["properties"].(map[string]any)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return quoteList(keys)
}
