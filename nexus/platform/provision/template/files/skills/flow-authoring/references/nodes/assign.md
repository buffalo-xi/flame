# `assign` 节点

纯赋值：把模板渲染出的值写进本节点的输出变量，或写进**祖先容器**的变量 / **流程变量**。

## config 形状

```json
{
  "inputs": {"raw": {"type": "string", "source_type": "node", "source_ref": "2", "source_key": "body"}},
  "outputs": {"total": {"type": "number", "description": "解析后的数字"}},
  "targets": {"container_var": {"type": "string", "source_type": "node", "source_ref": "<容器ID>"}},
  "assignments": [
    {"kind": "output", "target": "total", "path": "", "value": "{{ .input.raw }}"}
  ]
}
```

## 字段

| 键 | 必填 | 说明 |
|---|---|---|
| `assignments` | ✅ | 数组，每项 `{kind, target, path, value}` |
| `assignments[].kind` | ✅ | 只能是 `output`（写本节点输出）或 `target`（写祖先容器变量或流程变量） |
| `assignments[].target` | ✅ | 变量名；`kind: target` 时要在 `targets` 里声明过 |
| `assignments[].path` | — | 子路径；`path` 以 `[` 开头或目标类型是数组 → 拼成 `target+path`，否则 `target+"."+path` |
| `assignments[].value` | — | Go 模板字符串；按目标变量的 `type` 转成 number / boolean / string |
| `inputs` | — | 模板上下文（`{{ .input.x }}`） |
| `outputs` | 写 `kind: output` 时必须有 | 目标变量声明，类型决定转换 |
| `targets` | 写 `kind: target` 时必须有 | 可写目标声明：祖先容器的 outputs/locals（`source_type: node`，`source_ref: <容器ID>`）或流程变量（`source_type: flow`） |

可写目标只有这两类；`env` / `argument` / `builtin` 是本次运行的只读输入，写入会报错。
写 `source_type: flow` 不会触发流程变量上的 output binding（binding 只对 `source_type: node` 生效）。

**不读** `locals`。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 常见错误

- `assignments` 缺失/为空 → 节点什么也不做（不报错），容易误以为已赋值。
- `unknown assign kind` —— `kind` 写成 `variable` 之类。
- 写进 `target` 但没在 `targets` 声明 → `config` 错误。
- 想赋成数字却给了 `"abc"` —— 类型转换失败，报 `var`（`on_error.codes` 里写 `var` 能跳过或重试）。

源码：`service/flow/executors/assign.go`
