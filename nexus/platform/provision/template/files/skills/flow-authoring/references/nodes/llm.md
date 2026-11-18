# `llm` 节点

调一次模型，拿文本结果（可流式）。固定输出三个变量，脚本只能写自定义变量。

## config 形状

```json
{
  "llm": {
    "gateway_model": "",
    "messages": [
      {"role": "system", "content": "你是数据分析师"},
      {"role": "user", "content": "根据数据写 3 条要点：{{ .input.data }}"}
    ],
    "params": {"temperature": 0.3, "max_tokens": 800},
    "response_mode": "complete"
  },
  "inputs": {"data": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
  "outputs": {
    "content": {"type": "string", "description": "回复正文"},
    "reasoning_content": {"type": "string", "description": "推理过程"},
    "tool_calls": {"type": "array[object]", "description": "工具调用列表"}
  }
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `llm.messages` | ✅ | — | `[{role, content}]`；`role` 缺省 `user`；`content` 支持模板；空数组 → `config` 错误 |
| `llm.gateway_model` | — | 空 = 租户缺省模型 | 填了但找不到 → `not_found`；租户没配缺省模型也 `not_found` |
| `llm.response_mode` | — | `complete` | 只接受 `complete` / `sse_stream`，其它值直接 `config` 错误 |
| `llm.params` | — | `{}` | `max_tokens` / `temperature` / `top_p` / `extra_params` |
| `llm.handler` | — | — | 后处理脚本 `{script, lang, timeout{enabled,seconds}}`；**不支持 `template`** |
| `config.inputs` | — | — | 模板 `{{ .input.x }}` 的来源 |
| `config.outputs` | 建议写全 | 执行器补齐 | 固定三变量；漏声明也能跑（`ensureFixedOutputs` 补），前端会写全 |
| `config.locals` | — | — | 局部变量，handler 里用 `{{ .local.x }}` |

## 输出变量

固定：`content`(string) / `reasoning_content`(string，推理模型才有值) / `tool_calls`(array[object])。
`handler` 只能写自定义输出；写这三个会被拦。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 提示词与全局变量

全局变量经输入变量进入提示词：`config.inputs` 里声明 `{"source_type": "global", "source_key": "NAME"}`，
`messages[].content` 里写 `{{ .input.xxx }}`。**没有** `{{ .global_vars.X }}` 这种写法
（已下线，配置里留着它会在流程载入时直接报错，而不是渲染成 `<no value>`）。

引用 `secret` 类全局变量**不阻断**，但会记一条告警：值会进模型上下文，也会进 `session_records` 与 SSE。
密钥优先用于连接 / 工具配置（`http.headers`、`mcp` / `rpc` 的连接参数），别往提示词里塞。

## 常见错误

- `messages 不能为空` —— `messages` 是空数组或不是数组。
- `不支持的 response_mode` —— 写了 `auto` 之类。
- `未指定模型且不存在缺省模型` —— `gateway_model` 空且租户没配默认模型。

源码：`service/flow/executors/llm.go`
