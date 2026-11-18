package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	globalvarsrepo "flame/nexus/shared/repository/globalvars"
)

// ===== list_global_vars =====
//
// 为什么需要这把工具：全局变量在工作流里经节点输入（source_type: "global"）、在 AGENT.md / SKILL.md 里用
// `${global_vars.X}` 引用，但**名字只能靠人告诉模型**。没有它，模型只能猜，猜错就是运行期硬失败
// （错误信息里会带变量名，但要先跑一次才知道）。技能文档教会了「怎么用」，这把工具补的是
// 「有哪些」。
//
// 安全口径：
//   - **永不返回值**，只回 name / kind / description / has_value。值不需要出现在模型上下文里 ——
//     引用靠名字，而值一旦进了上下文就会进 session 记录与 SSE，那正是我们一直在堵的口子；
//   - 分类放 flow-admin（仅超级智能体）：消费方是写工作流与文档的那个身份，普通 agent 拿到
//     这串名字也没有能用的地方。

type listGlobalVarsTool struct{}

func (t *listGlobalVarsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "list_global_vars",
		Category: CategoryFlowAdmin,
		Description: "列出本租户的全局变量名与类型（**不含值**）。写节点配置前先查一遍，把名字写准。" +
			"工作流里的用法：在节点的 config.inputs 里加一项 " +
			"`{\"source_type\": \"global\", \"source_key\": \"名字\"}`，模板里写 `{{ .input.变量名 }}` —— " +
			"不是 `{{.global_vars.名字}}`，那个写法已下线，配置里留着它流程载入就会报错。" +
			"AGENT.md / SKILL.md 里仍用 `${global_vars.名字}`。" +
			"kind 有四种：text / number / bool / secret。" +
			"**secret 类不要写进提示词**：放进 llm 的 messages / intent 的 system_prompt 不阻断但会记告警，" +
			"值会进模型上下文与运行记录；密钥只用于连接 / 工具配置（http headers、mcp/rpc 连接参数）。" +
			"名字拼错会运行期硬失败，不会静默空跑。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "可选，只看某一类：text / number / bool / secret。不传 = 全部",
				},
				"keyword": map[string]any{
					"type":        "string",
					"description": "可选，按名字或描述做子串过滤（不区分大小写）",
				},
			},
			"required": []any{},
		},
	}
}

func (t *listGlobalVarsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c == nil || c.Schema == "" {
		return nil, errors.New("缺少租户上下文（schema 为空）")
	}

	kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
	keyword := strings.ToLower(strings.TrimSpace(argString(args, "keyword")))

	rows, err := globalvarsrepo.NewRepository().List(c.Schema)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if kind != "" && strings.ToLower(r.Kind) != kind {
			continue
		}
		if keyword != "" &&
			!strings.Contains(strings.ToLower(r.Name), keyword) &&
			!strings.Contains(strings.ToLower(r.Description), keyword) {
			continue
		}
		items = append(items, map[string]any{
			"name":        r.Name,
			"kind":        r.Kind,
			"description": r.Description,
			// 只回「有没有值」：模型不需要真值（引用靠名字），而值进上下文就是泄漏
			"has_value": r.Value != "",
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	notes := []string{
		"这里不返回值：引用靠名字，值由运行期按租户解析",
		"工作流：节点 config.inputs 写 {\"source_type\": \"global\", \"source_key\": \"名字\"}，模板用 `{{ .input.变量名 }}`；" +
			"AGENT.md / SKILL.md 写 `${global_vars.名字}`",
	}
	if len(items) == 0 {
		notes = append(notes, "本租户还没有全局变量（在「设置 → 全局变量」里创建），需要配置就用环境变量或让用户先建")
	}

	return map[string]any{
		"total": len(items),
		"items": items,
		"notes": notes,
	}, nil
}

func init() {
	Register(&listGlobalVarsTool{})
}
