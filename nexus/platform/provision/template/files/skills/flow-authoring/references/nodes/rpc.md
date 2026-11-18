# `rpc` 节点

两种用法：调一个 RPC 连接上的工具，或**进程内直调本租户已对外开放的端点**。

## config 形状

```json
{
  "rpc": {
    "target_type": "endpoint",
    "endpoint": "<端点名>",
    "arguments": {"x": "{{ .input.x }}"}
  },
  "inputs": {"x": {"type": "string", "source_type": "argument", "source_key": "x"}},
  "outputs": {
    "result": {"type": "string", "description": "调用结果"},
    "progress": {"type": "array[string]", "description": "进度通知数组"},
    "error": {"type": "string", "description": "调用错误信息"}
  }
}
```

外部连接形态：

```json
{"rpc": {"target_type": "connection", "connection_id": "<RPC 连接 id>",
         "tool_name": "foo", "arguments": {"x": 1}}}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `rpc.target_type` | — | `connection` | `connection` = 外部 RPC 连接；`endpoint` = 本租户已对外开放端点（同进程直调） |
| `rpc.connection_id` + `rpc.tool_name` | `connection` 模式必填 | — | 连接须启用 |
| `rpc.endpoint` | `endpoint` 模式必填 | — | **端点名**（= 流程名 / 智能体名，租户内唯一，实体改名时同步）。写名字不写 id：id 在导出导入后会变。本租户直调，不要求端点已对外开放 |
| `rpc.arguments` | — | `{}` | 对象或 JSON 字符串，值支持模板 |
| `rpc.progress` | — | `false` | 关时 `progress` 恒为空数组 |
| `rpc.handler` | — | — | 后处理脚本，**不支持 `template`** |
| `config.outputs` | 建议写全 | 无兜底 | 漏声明会在写入时报 `output var ... not defined` |

## 输出变量

固定：`result`(string) / `progress`(array[string]) / `error`(string)。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 常见错误

- `rpc config not found` / `endpoint` 缺失（或误写成 `endpoint_id`）。
- `RPC 连接不存在` / `连接未启用`。
- 端点直调报"不存在" —— 端点名写错（名字 = 流程名 / 智能体名），或那个流程根本没有 rpc 端点。

源码：`service/flow/executors/rpc.go`
