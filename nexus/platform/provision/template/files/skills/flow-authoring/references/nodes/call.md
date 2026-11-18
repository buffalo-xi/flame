# `call` 节点

调用**同一个 flow 内**的另一个 `function` 节点（支持递归，受递归深度限制）。

## config 形状

```json
{
  "target": "f_9x2k",
  "args": {
    "a": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"},
    "b": {"type": "number", "source_type": "argument", "source_key": "b"}
  },
  "outputs": {"sum": {"type": "number"}}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `target` | ✅ | — | 目标节点的 **id**，且该节点 `type` 必须是 `function`；否则 `config` 错误 |
| `args` | — | `{}` | 实参声明，键必须与目标函数的 `params` 一一对应（必填缺失 / 多余键 → `config` 错误）；在**父流程**上下文解析后注入 |
| `outputs` | — | — | 会被目标函数的 `outputs` **整体覆盖**（前端调 `call` 面板时会自动同步） |

**不读** `inputs` / `locals`。递归深度超限 → `internal` 错误。

## 输出变量

镜像目标函数跑完后的 `OutputVars`（名字与类型都来自函数），同时镜像状态。

## 出边

普通节点：调用返回后沿**第一条出边**继续。

## 与 `process` 的区别

| | `process` | `call` |
|---|---|---|
| 执行谁 | 自己的子节点 | 本 flow 内另一个 `function` 节点的子节点 |
| 复用 | 不能复用 | 可被多处调用 / 递归 |
| 参数 | 无 | `args` → 函数 `params` |

## 常见错误

- `call target 不能为空` / `目标节点不是 function`。
- `missing required arg "x" for function <id>` —— 目标函数的必填形参（`required` 缺省为 `true`）没有对应实参。
- `unknown arg "x" for function <id>` —— `args` 里出现了目标函数 `params` 未声明的键。
- `args 解析失败` —— 实参的 `source_key` 在上游没有对应输出。
- 递归超限 —— 函数直接或间接调用了自己且没有终止条件。

源码：`service/flow/executors/call.go`
