package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ===== update_flow_arguments =====
//
// 入口参数（flows.arguments）不是独立实体：只是 flows 行里的一个 JSON 字段，没有 id，
// 也没有自己的表。增 / 改本质是同一个 upsert 动作，所以不按 create / update / delete
// 三件套拆开 —— 一次「加 city 并把 required 补上」用三把工具要三次往返，中间态还会是
// 「required 指向不存在的参数」。
//
// 为什么要这把工具：没有它，改参数只能 delete_flow + create_flow 重建，而重建会换掉
// flows.id —— 服务凭证的白名单按目标实体 id 授权（svc_auth_binding_targets），旧行会
// 静默失效；运行记录、版本历史、断点也一起清零。

type updateFlowArgumentsTool struct{}

func (t *updateFlowArgumentsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_flow_arguments",
		Category: CategoryFlowAdmin,
		Description: "增删改工作流的入口参数（flows.arguments）：set 按参数名整体替换（不存在就新增），" +
			"remove 按名字删除。声明过的参数就是 run_flow 的 inputs 键，也是对外开放端点对外的 input_schema。" +
			"**只动入口参数**：名称 / 描述 / diagram / 限额一律不碰（改结构用节点工具）。" +
			"改动落在工作副本：run_flow 试跑与端点 input_schema 立即生效；" +
			"但正式调用跑的是**已发布版本**，要在编辑页「发布」后才换成新声明。" +
			"改 type / 加必填会让已有调用方传错或漏传，动手前先 read_flow 看现状。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow": map[string]any{"type": "string", "description": flowRefHint},
				"set": map[string]any{
					"type": "object",
					"description": "按参数名整体替换该参数的声明（名字不存在就新增）。形状与 create_flow 的 arguments 一致：" +
						`{"city":{"type":"string","description":"城市名"}}。` +
						"type 只支持 string / number / boolean / object / array（array 必须带 items.type；object 可再带 properties / required）。",
				},
				"remove": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "要删除的参数名。会同时从 required 里摘掉；**不会**改节点 config，" +
						"仍在引用它的 source_type=argument 变量会变成取不到值",
				},
				"required": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "必填清单，**整体替换**：不传 = 保持现状（被删的自动剔除）；传 [] = 全部改成可选。" +
						"名字必须存在于 set 或现有参数里 —— 要先 read_flow 拿现状，别只写这次新增的那个",
				},
			},
			"required": []any{"flow"},
		},
	}
}

func (t *updateFlowArgumentsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	ref := strings.TrimSpace(argString(args, "flow"))
	if ref == "" {
		return nil, errors.New("flow 不能为空（工作流 id 或名称）")
	}
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	setRaw, _ := args["set"].(map[string]any)
	remove, _ := argStringSlice(args, "remove")
	rawRequired, hasRequired := args["required"]
	// required 单独给也是合法编辑：只调必填、不动声明
	if len(setRaw) == 0 && len(remove) == 0 && !hasRequired {
		return nil, errors.New("set / remove / required 至少要给一个；只想看现状用 read_flow")
	}
	// 同一个名字既要改又要删，意图不自洽：拒掉而不是靠执行顺序决定结果。
	for _, name := range remove {
		if _, both := setRaw[name]; both {
			return nil, fmt.Errorf("参数 %q 同时出现在 set 与 remove 里：一次调用里要么改要么删", name)
		}
	}

	detail, err := c.Flows.Get(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("工作流 %q 不存在；先用 list_flows 确认名字", ref)
	}

	// 整份文档原样解码再改局部：人写的 description / default 等字段不能被一次编辑清洗掉。
	doc := map[string]any{}
	if len(detail.Arguments) > 0 {
		if err := json.Unmarshal(detail.Arguments, &doc); err != nil {
			return nil, fmt.Errorf("现有入口参数不是合法 JSON: %w", err)
		}
	}
	doc["type"] = "object"
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}

	for _, name := range remove {
		delete(props, name)
	}
	// set 逐条校验：只重写被点名的参数，其余保持原文
	for name, spec := range setRaw {
		clean, err := validateFlowArgProps(map[string]any{name: spec}, "")
		if err != nil {
			return nil, err
		}
		props[name] = clean[name]
	}
	doc["properties"] = props

	// required：传了就整体替换（先 read_flow 拿现状），没传就保持并摘掉被删项
	if hasRequired && rawRequired != nil {
		req, err := flowArgRequired(rawRequired, props, "")
		if err != nil {
			return nil, err
		}
		doc["required"] = flowArgAnySlice(req)
	} else if cur, ok := doc["required"].([]any); ok {
		kept := make([]any, 0, len(cur))
		for _, item := range cur {
			if name, ok := item.(string); ok {
				if _, exists := props[name]; exists {
					kept = append(kept, name)
				}
			}
		}
		if len(kept) > 0 {
			doc["required"] = kept
		} else {
			delete(doc, "required")
		}
	}

	merged, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("序列化入口参数失败: %w", err)
	}
	if _, err := c.Flows.UpdateArguments(ctx, ref, ref, json.RawMessage(merged)); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	changed := make(map[string]bool, len(setRaw)+len(remove))
	for name := range setRaw {
		changed[name] = true
	}
	for _, name := range remove {
		changed[name] = true
	}
	touched := make([]string, 0, len(changed))
	for name := range changed {
		touched = append(touched, name)
	}
	sort.Strings(touched)

	return map[string]any{
		"flow_id":   detail.ID,
		"flow_name": detail.Name,
		"arguments": names,
		"changed":   touched,
		"note":      "已写入工作副本：run_flow 立即生效；正式入口要等编辑页「发布」后才换成新声明",
	}, nil
}
