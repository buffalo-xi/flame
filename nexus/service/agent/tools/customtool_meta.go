package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/shared/capability"
)

// 自定义工具的元工具（仅超级智能体，CategoryCustomTool）。
//
// 为什么需要这一层：超级智能体能改**已有**工具里的文件（gate.go 把 tools/{name} 当实体放行），
// 但两件事它做不到 —— 一是建实体根（被文件闸明确拒绝，否则可以 mkdir 绕过脚本表造出
// 「盘上有、库里没有」的孤儿），二是改 custom_tools 那一行（描述 / 参数 schema / 语言 /
// 入口 / 超时 / 出网 / 配置）。没有元工具，超级智能体只能改别人建好的工具。
//
// 与 create_agent / create_shared_skill 的分工一致：**元工具建壳，内容交给文件工具**。
// create 会写一份可跑的最小骨架（SkeletonForLang，管理端创建同源），随后超级智能体用
// write_file 覆盖 tools/<name>/<entry> 即可；写文件前实体已存在且 status=1，闸门放行。

// CustomToolInput 新建自定义工具的入参（字段与表一一对应）。
type CustomToolInput struct {
	Name           string
	DisplayName    string
	Description    string
	Lang           string
	Entry          string
	Parameters     map[string]any
	Output         map[string]any
	TimeoutMS      int
	MaxOutputBytes int
	Egress         bool
	ConfigFields   []map[string]any
	Configs        map[string]any
}

// CustomToolUpdate 更新自定义工具的入参。
//
// 指针语义与 SkillInput 同款：nil = 不改；Parameters/Output/ConfigFields/Configs
// 用 nil 判「不改」，非 nil 且为空表 = 清空。
//
// 没有 Name：改名会让 agent_tool_grants 里的 custom:<name> 静默失效，
// 改名等价于「删了重建」，由调用方显式做两步。
type CustomToolUpdate struct {
	DisplayName    *string
	Description    *string
	Lang           *string
	Entry          *string
	Parameters     map[string]any
	Output         map[string]any
	TimeoutMS      *int
	MaxOutputBytes *int
	Egress         *bool
	ConfigFields   []map[string]any
	Configs        map[string]any
}

// CustomToolRefs 删除被拒时的引用方明细（空 = 没人引用）。
type CustomToolRefs struct {
	Agents []string
	Skills []string
	Groups []string
}

// Total 引用总数。
func (r *CustomToolRefs) Total() int {
	if r == nil {
		return 0
	}
	return len(r.Agents) + len(r.Skills) + len(r.Groups)
}

// CustomToolAdmin 自定义工具的增删改（元工具用；由 service/agent 注入）。
//
// 与只读的 CustomToolSource 分开：那是执行热路径的视图（List/Get/Status），
// 这里只有管理端语义，且只有超级智能体会走到。
type CustomToolAdmin interface {
	Create(ctx context.Context, in CustomToolInput) (*CustomToolSpec, error)
	Update(ctx context.Context, name string, in CustomToolUpdate) (*CustomToolSpec, error)
	// Delete 删表行 + 删盘上目录。
	//
	// force=false 时若仍被 agent 授权 / skill 依赖 / 自定义组引用则**拒绝**并把引用方
	// 返回给调用方展示。拒绝而不是静默清理：删掉被引用的工具会让那些 agent 的工具目录
	// 里突然少一个工具，属于无报错的功能消失。
	//
	// 引用行本身不清理（即使 force）：ref 指向不存在的工具时解析为空，保留它反而让
	// 「误删后重建」直接恢复授权 —— 与 admin 的口径一致。
	Delete(ctx context.Context, name string, force bool) (*CustomToolRefs, error)
	// ListAll 列出全部工具（含锁定 / 废弃态），供超级智能体自省。
	ListAll(ctx context.Context) ([]CustomToolSpec, error)
}

// ---- create_custom_tool ----

type createCustomToolTool struct{}

func (t *createCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_custom_tool",
		Category: CategoryCustomTool,
		Description: "新建一个租户级**自定义工具**（仅超级智能体）。自定义工具是租户自己的代码：" +
			"模型给参数、拿到结构化结果，运行时走 /v1/exec 一次性进程（要求租户已开通 " + capability.Exec + "）。" +
			"本工具只建实体（表行 + tools/<name>/ 目录 + 可跑的最小骨架），代码请随后用 " +
			"write_file 写 /tools/<name>/<entry>（**前导 / 表示从租户根算**，少了它文件会落进会话工作区，工具读不到），" +
			"再用 grant_agent_tool 授 ref `custom:<name>` 给目标 agent。" +
			"工具名创建后不可改（会打断已有授权）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "工具短名：字母/数字/短横线，不能以短横线开头，不能含双下划线或点",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "给模型看的用途说明（决定它何时被调用，写清边界）",
				},
				"display_name": map[string]any{"type": "string", "description": "管理端展示名（不进模型上下文，可空）"},
				"lang": map[string]any{
					"type":        "string",
					"description": "python（默认）/ js / lua / sh",
					"enum":        []any{"python", "js", "lua", "sh"},
				},
				"entry": map[string]any{
					"type":        "string",
					"description": "入口文件（相对 tools/<name>/，必须留在该目录内）；缺省按语言取 main.py/js/lua/sh",
				},
				"parameters": map[string]any{
					"type":        "object",
					"description": "入参 JSON Schema（发给模型），如 {\"type\":\"object\",\"properties\":{...}}",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "出参 JSON Schema（不发给模型，仅用于校验脚本输出）",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"description": "单次执行超时（毫秒，缺省 60000，上限 600000）",
				},
				"max_output_bytes": map[string]any{
					"type":        "integer",
					"description": "stdout 上限（字节，缺省 65536，上限 1048576）；超限即失败",
				},
				"egress": map[string]any{
					"type":        "boolean",
					"description": "是否允许脚本访问外网；需要平台已开通 " + capability.Egress + "，否则本工具直接拒绝",
				},
				"config_fields": map[string]any{
					"type":        "array",
					"description": "配置项声明（[{key,label,type,help,secret,required,default}]），据此渲染表单并决定哪些打码",
				},
				"configs": map[string]any{
					"type":        "object",
					"description": "配置值（明文；secret 项的用途是执行时注入，不要写进脚本）",
				},
			},
			"required": []any{"name", "description"},
		},
	}
}

func (t *createCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("自定义工具管理不可用")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}
	entry := strings.TrimSpace(argString(args, "entry"))
	if err := validateScriptEntry(entry); err != nil {
		return nil, err
	}
	egress := argBool(args, "egress")
	if err := requireCustomToolCaps(c, egress); err != nil {
		return nil, err
	}

	in := CustomToolInput{
		Name:        name,
		DisplayName: strings.TrimSpace(argString(args, "display_name")),
		Description: strings.TrimSpace(argString(args, "description")),
		Lang:        argString(args, "lang"),
		Entry:       entry,
		TimeoutMS:   argInt(args, "timeout_ms"),
		Egress:      egress,
	}
	in.Parameters, _ = args["parameters"].(map[string]any)
	in.Output, _ = args["output"].(map[string]any)
	in.Configs, _ = args["configs"].(map[string]any)
	in.ConfigFields = argMapSlice(args, "config_fields")
	if v := argInt(args, "max_output_bytes"); v > 0 {
		if v > maxCustomToolOutput {
			return nil, fmt.Errorf("max_output_bytes 最大 %d", maxCustomToolOutput)
		}
		in.MaxOutputBytes = v
	}
	if in.TimeoutMS > maxCustomToolTimeoutMS {
		return nil, fmt.Errorf("timeout_ms 最大 %d", maxCustomToolTimeoutMS)
	}

	spec, err := c.CustomToolsAdmin.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tool": spec,
		"next": []string{
			// 路径必须带前导 "/"（= 从租户根算）。少了它会被当成工作区相对路径，
			// 文件写进 users/{uid}/sessions|runs/... 而工具仍读 tools/<name>/ 下的骨架 —— 改了不生效。
			"write_file /tools/" + name + "/" + entryOrDefault(entry, in.Lang),
			"grant_agent_tool ref=custom:" + name,
		},
	}, nil
}

// ---- update_custom_tool ----

type updateCustomToolTool struct{}

func (t *updateCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_custom_tool",
		Category: CategoryCustomTool,
		Description: "修改自定义工具的**元数据**（描述 / 参数 schema / 语言 / 入口 / 超时 / 出网 / 配置；仅超级智能体）。" +
			"只传要改的字段，其余保持原样。工具名与状态不能改（改名会打断授权，状态切换只走管理端）。" +
			"代码内容请用 write_file 改 tools/<name>/<entry>。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":             map[string]any{"type": "string", "description": "要改的工具短名"},
				"display_name":     map[string]any{"type": "string", "description": "管理端展示名（传空串 = 清空）"},
				"description":      map[string]any{"type": "string", "description": "给模型看的用途说明"},
				"lang":             map[string]any{"type": "string", "enum": []any{"python", "js", "lua", "sh"}},
				"entry":            map[string]any{"type": "string", "description": "入口文件（相对工具目录）"},
				"parameters":       map[string]any{"type": "object", "description": "入参 JSON Schema（整份替换）"},
				"output":           map[string]any{"type": "object", "description": "出参 JSON Schema（整份替换）"},
				"timeout_ms":       map[string]any{"type": "integer", "description": "单次执行超时（毫秒）"},
				"max_output_bytes": map[string]any{"type": "integer", "description": "stdout 上限（字节）"},
				"egress":           map[string]any{"type": "boolean", "description": "是否允许出网（需 " + capability.Egress + "）"},
				"config_fields":    map[string]any{"type": "array", "description": "配置项声明（整份替换）"},
				"configs":          map[string]any{"type": "object", "description": "配置值（按键合并，未提供的键保持原值）"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *updateCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("自定义工具管理不可用")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}

	var in CustomToolUpdate
	if v, ok := args["display_name"].(string); ok {
		in.DisplayName = &v
	}
	if v, ok := args["description"].(string); ok {
		in.Description = &v
	}
	if v, ok := args["lang"].(string); ok {
		in.Lang = &v
	}
	if v, ok := args["entry"].(string); ok {
		if err := validateScriptEntry(v); err != nil {
			return nil, err
		}
		in.Entry = &v
	}
	if v, ok := args["parameters"].(map[string]any); ok {
		in.Parameters = v
	}
	if v, ok := args["output"].(map[string]any); ok {
		in.Output = v
	}
	if v := argInt(args, "timeout_ms"); v > 0 {
		if v > maxCustomToolTimeoutMS {
			return nil, fmt.Errorf("timeout_ms 最大 %d", maxCustomToolTimeoutMS)
		}
		in.TimeoutMS = &v
	}
	if v := argInt(args, "max_output_bytes"); v > 0 {
		if v > maxCustomToolOutput {
			return nil, fmt.Errorf("max_output_bytes 最大 %d", maxCustomToolOutput)
		}
		in.MaxOutputBytes = &v
	}
	if v, ok := args["egress"].(bool); ok {
		if err := requireCustomToolCaps(c, v); err != nil {
			return nil, err
		}
		in.Egress = &v
	}
	if _, ok := args["config_fields"].([]any); ok {
		in.ConfigFields = argMapSlice(args, "config_fields")
	}
	if v, ok := args["configs"].(map[string]any); ok {
		in.Configs = v
	}

	spec, err := c.CustomToolsAdmin.Update(ctx, name, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tool": spec}, nil
}

// ---- delete_custom_tool ----

type deleteCustomToolTool struct{}

func (t *deleteCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_custom_tool",
		Category: CategoryCustomTool,
		Description: "删除自定义工具及其代码目录（仅超级智能体）。若它仍被 agent 授权 / skill 依赖 / " +
			"工具组引用，默认**拒绝**并列出引用方；确认要删再传 force=true。授权行不会被清理，" +
			"因此误删后重建同名工具可自动恢复授权。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "要删除的工具短名"},
				"force": map[string]any{"type": "boolean", "description": "忽略引用直接删除"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *deleteCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("自定义工具管理不可用")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}
	force := argBool(args, "force")
	refs, err := c.CustomToolsAdmin.Delete(ctx, name, force)
	// 未 force 且仍被引用：服务层「拒绝就是返回 (refs, nil)」（它没删任何东西），
	// 只有带 force 时才会既删成功又返回引用方，所以判据里必须带上 !force，
	// 否则 force 删除会被误报成拒绝。
	if err == nil && !force && refs.Total() > 0 {
		return nil, fmt.Errorf("自定义工具 %q 仍被引用（agents=%v skills=%v groups=%v），确认删除请传 force=true",
			name, refs.Agents, refs.Skills, refs.Groups)
	}
	if err != nil {
		if refs.Total() > 0 {
			return nil, fmt.Errorf("%w（引用方：agents=%v skills=%v groups=%v，确认删除请传 force=true）",
				err, refs.Agents, refs.Skills, refs.Groups)
		}
		return nil, err
	}
	out := map[string]any{"name": name, "deleted": true}
	if refs.Total() > 0 {
		// 保留的引用行：提示重建同名工具可自动恢复授权。
		out["kept_refs"] = map[string]any{
			"agents": refs.Agents, "skills": refs.Skills, "groups": refs.Groups,
		}
	}
	return out, nil
}

// ---- list_custom_tools ----

type listCustomToolsTool struct{}

func (t *listCustomToolsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "list_custom_tools",
		Category: CategoryCustomTool,
		Description: "列出本租户全部自定义工具（含锁定 / 废弃态；仅超级智能体）。" +
			"模型在工具目录里只能看到已授权的 custom__*，看不到未授权或被锁定的，本工具补这个盲区。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listCustomToolsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("自定义工具管理不可用")
	}
	list, err := c.CustomToolsAdmin.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "tools": list}, nil
}

// ---- 共用校验 ----

const (
	maxCustomToolTimeoutMS = 600_000
	maxCustomToolOutput    = 1 << 20
)

// validateCustomToolName 校验短名：与 admin 的创建接口同一套规则。
//
// 双下划线与 custom__ 前缀都要拒：对外名是 custom__<name>，名字里再带下划线会把前缀
// 切乱，让模型看到 custom__a__b 这种无法反解的工具名。
func validateCustomToolName(name string) error {
	if name == "" {
		return errors.New("name 不能为空")
	}
	if !ValidName(name) {
		return fmt.Errorf("工具名 %q 只能包含字母、数字与短横线，且不能以短横线开头", name)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("工具名 %q 不能包含连续下划线（对外名是 custom__<name>）", name)
	}
	if strings.HasPrefix(name, CustomToolPrefix) {
		return fmt.Errorf("工具名 %q 不能以 %q 开头（那是内部命名空间）", name, CustomToolPrefix)
	}
	return nil
}

// validateScriptEntry 入口文件必须是工具目录内的相对路径。
//
// 与 run_skill_script 的越界检查同一条理由：entry 来自模型，一个 ../ 就能让沙箱
// 去执行工具目录之外的文件。
func validateScriptEntry(entry string) error {
	if entry == "" {
		return nil // 由服务层按语言补默认
	}
	if strings.ContainsRune(entry, '\\') {
		return fmt.Errorf("entry %q 请用 / 分隔", entry)
	}
	if path.IsAbs(entry) {
		return fmt.Errorf("entry %q 必须是相对工具目录的路径", entry)
	}
	clean := path.Clean(entry)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("entry %q 不能跳出工具目录", entry)
	}
	return nil
}

// requireCustomToolCaps 建工具前先做能力体检。
//
// 为什么在创建时就拦：工具建好但租户没开 sandbox.exec，模型调用时才报「能力未开通」，
// 那时已经授了权、写进 system prompt，排查成本远高于现在报错。
// egress 额外要求 net.egress（它是跨通道的出口策略，不随 exec 自动获得）。
func requireCustomToolCaps(c *Context, egress bool) error {
	if c.Caps == nil {
		return nil // 无租户语义的调用方（单测 / 内部），不按能力拦
	}
	if egress && !c.Caps[capability.Egress] {
		return fmt.Errorf("egress=true 需要平台开通 %s（自定义工具走 exec 通道，出网是单独一档）", capability.Egress)
	}
	if !c.Caps[capability.Exec] {
		return fmt.Errorf("自定义工具运行需要平台开通 %s，当前租户未开通", capability.Exec)
	}
	return nil
}

// argMapSlice 取 []map[string]any 形态的参数。
//
// 不能直接断言：JSON 解出来的数组是 []any，元素才是 map[string]any。
// 返回 nil 表示「没传」；空数组会返回非 nil 的空表（= 清空）。
func argMapSlice(args map[string]any, key string) []map[string]any {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// entryOrDefault 仅用于给模型下一步提示，真正的默认值由服务层落。
func entryOrDefault(entry, lang string) string {
	if entry != "" {
		return entry
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh", "shell":
		return "main.sh"
	}
	return "main.py"
}
