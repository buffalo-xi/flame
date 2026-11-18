# `intent` 节点

让模型在几条出边里**选一条**（单选，没有阈值）。

## config 形状

```json
{
  "inputs": {
    "messages": {"type": "array[object]", "source_type": "argument", "source_key": "messages"},
    "input": {"type": "string", "source_type": "argument", "source_key": "input"}
  },
  "llm": {
    "gateway_model": "",
    "system_prompt": "你是意图分类器，请根据对话历史和用户输入判断用户意图。\n\n对话历史：\n{{json .input.messages}}\n\n用户输入：\n{{.input.input}}",
    "params": {}
  },
  "children": [
    {"id": "a1b2c", "label": "查订单", "description": "用户想查询订单状态或物流"},
    {"id": "d3e4f", "label": "退款", "description": "用户想退款或投诉"}
  ],
  "outputs": {
    "intent": {"type": "string"},
    "index": {"type": "number"}
  }
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `children` | ✅ | — | 候选意图；每项 `{id, label, description}` |
| `children[].description` | 建议 | — | 意图描述，拼进分类 prompt；空则只有 label 可参考 |
| `llm.system_prompt` | 建议 | — | 分类提示词；`{{ .input.* }}` 取 `inputs` |
| `llm.gateway_model` | — | 空 = 租户缺省模型 | |
| `llm.params` | — | `{}` | `max_tokens` / `temperature` / `top_p` / `extra_params` |
| `inputs` | 建议 | — | 供 prompt 使用；`input.messages` / `input.input` 是前端默认两个 |
| `outputs` | — | 自动补齐 | 固定 `intent` / `index` |

**`llm.messages` 不读**：`intent` 自己拼 messages，只认 `system_prompt`。
**没有阈值/置信度配置**：模型选不出就返回 -1，走默认分支。

## 输出变量

固定：`intent`(string) = 命中分支的 `label`，未命中为 `""`；`index`(number) = 命中下标，未命中 `-1`。

## 出边怎么连

与 `branch` 相同：命中分支 `source<childId>left|right`，默认分支 `source<nodeId>left|right|bottom`。
未命中且没有默认出边 → 节点成功但什么都不做。

## 用 `create_flow` 建（`branches`）

和 `branch` 一模一样的写法：`key` = `children[].id`（`default` 是可选兜底），每条子链生成一条
`source<childId>right` 出边；`key: "default"` 用 `source<节点自己的id>right`。

**`branches` 只建边，不会回填 `children`** —— 候选意图仍然要自己写在 `config.children` 里
（执行器只读 `config["children"]`，没有它直接报「意图节点没有配置任何分支」）。
工具会用 `config.children[].id` 校验 `branches[].key`，对不上当场拒绝创建。

```json
{
  "type": "intent", "label": "识别用户意图",
  "config": {
    "llm": {"system_prompt": "..."},
    "inputs": {"input": {"type": "string", "source_type": "argument", "source_key": "input"}},
    "children": [
      {"id": "a1", "label": "查订单", "description": "查询订单状态或物流"},
      {"id": "b2", "label": "退款",   "description": "申请退款或投诉"}
    ]
  },
  "branches": [
    {"key": "a1", "steps": [{"type": "code", "label": "查单", "config": {"code": {"lang": "lua", "script": "..."}}}]},
    {"key": "b2", "steps": [{"type": "code", "label": "退款", "config": {"code": {"lang": "lua", "script": "..."}}}]},
    {"key": "default", "steps": [{"type": "result", "label": "没听懂", "config": {"code": {"lang": "template", "script": "请问您想做什么？"}}}]}
  ]
}
```

- **必须是所在链的最后一步**：分支节点执行完只走默认边，后续步骤要写进各子链。
- `children` 有、`branches` 漏掉的候选只给 `warnings`（命中后跳过），不阻断创建。

## 提示词与全局变量

`llm.system_prompt` 与 `llm` 节点同一套规则：全局变量经 `config.inputs` 的
`{"source_type": "global", "source_key": "NAME"}` 进入，prompt 里写 `{{ .input.xxx }}`；
**没有** `{{ .global_vars.X }}`（已下线，载入即报错）。

引用 `secret` 类全局变量不阻断，但会记告警 —— 值会进模型上下文与运行记录。

## 常见错误

- `意图节点没有配置任何分支` —— `config.children` 空（`branches` 不会替你补）。
- 永远走默认分支 —— `children[].description` 太笼统，或 `system_prompt` 里没给出编号/选择规则。
- 分类结果解析失败 —— 模型没按 JSON 回；执行器会兜底找文本里的 `{"index":n}`、纯数字、首个 `{...}`。

源码：`service/flow/executors/intent.go`
