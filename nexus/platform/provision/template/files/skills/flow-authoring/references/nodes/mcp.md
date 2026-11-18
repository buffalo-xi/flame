# `mcp` 节点

调一个 MCP 连接上的工具。

## config 形状

```json
{
  "mcp": {
    "connection_id": "<MCP 连接 id>",
    "tool_name": "calculator",
    "arguments": "{\"a\": 6, \"b\": 7, \"op\": \"{{ .input.op }}\"}",
    "progress": false
  },
  "inputs": {"op": {"type": "string", "source_type": "argument", "source_key": "op"}},
  "outputs": {
    "result": {"type": "string", "description": "工具返回结果"},
    "progress": {"type": "array[string]", "description": "进度通知数组"},
    "error": {"type": "string", "description": "调用错误信息"}
  }
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `mcp.connection_id` | ✅ | — | 连接必须存在且启用，否则 `not_found` / `config` |
| `mcp.tool_name` | ✅ | — | 该连接上暴露的工具名 |
| `mcp.arguments` | — | `{}` | **前端存成 JSON 字符串**；对象形态也接受。字符串值支持模板 |
| `mcp.progress` | — | `false` | 关时 `progress` 恒为空数组，服务端不会把思考/文本全量推一遍 |
| `mcp.handler` | — | — | 后处理脚本 `{script, lang, timeout{enabled,seconds}}`；**不支持 `template`** |
| `config.outputs` | 建议写全 | 无兜底 | 三个固定变量；**漏声明会在写入时报 `output var ... not defined`** |

## 输出变量

固定：`result`(string) / `progress`(array[string]) / `error`(string)。
工具的 `isError=true` 时写 `error`，节点仍是**成功**；只有调用异常才 failed。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 常见错误

- `mcp config not found` —— `config` 里没有 `mcp` 子对象。
- `mcp connection_id 不能为空` / `tool_name 不能为空`。
- `MCP 连接不存在` / `连接未启用`。
- `arguments JSON 解码失败` —— 字符串形态的 `arguments` 不是合法 JSON（注意模板渲染后仍要合法）。

源码：`service/flow/executors/mcp.go`
