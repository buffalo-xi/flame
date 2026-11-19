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
		Description: "List this tenant's global variable names and types (**values are not included**). Check it before writing node config so the names you use are exact. " +
			"Usage in workflows: add an entry to the node's config.inputs — " +
			"`{\"source_type\": \"global\", \"source_key\": \"<name>\"}` — and reference it in templates as `{{ .input.<name> }}`, " +
			"not `{{.global_vars.<name>}}` (that form is retired; leaving it in a config makes the flow fail to load). " +
			"In AGENT.md / SKILL.md keep using `${global_vars.<name>}`. " +
			"kind has four values: text / number / bool / secret. " +
			"**Never put secret variables into prompts**: using them in llm messages or an intent system_prompt is not blocked but logs a warning, " +
			"and the value ends up in the model context and run records; secrets belong in connection / tool config (http headers, mcp/rpc connection params). " +
			"A misspelled name fails hard at run time — it does not silently run empty.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Optional: only list one kind — text / number / bool / secret. Omit for all",
				},
				"keyword": map[string]any{
					"type":        "string",
					"description": "Optional: case-insensitive substring filter on name or description",
				},
			},
			"required": []any{},
		},
	}
}

func (t *listGlobalVarsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c == nil || c.Schema == "" {
		return nil, errors.New("missing tenant context (schema is empty)")
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
		"Values are not returned here: references go by name, and the value is resolved per tenant at run time",
		"Workflows: write {\"source_type\": \"global\", \"source_key\": \"<name>\"} into the node's config.inputs, and `{{ .input.<name> }}` in templates; " +
			"AGENT.md / SKILL.md use `${global_vars.<name>}`",
	}
	if len(items) == 0 {
		notes = append(notes, "This tenant has no global variables yet (create them under Settings → Global variables); for configuration use environment variables or ask the user to create one first")
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
