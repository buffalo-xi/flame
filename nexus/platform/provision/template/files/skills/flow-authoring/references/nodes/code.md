# `code` 节点

在沙箱里跑一段脚本，把返回值落到声明过的输出变量。

## config 形状

```json
{
  "code": {
    "lang": "lua",
    "script": "return { total = input.a + input.b }",
    "timeout": {"enabled": true, "seconds": 10}
  },
  "inputs": {
    "a": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"},
    "b": {"type": "number", "source_type": "argument", "source_key": "b"}
  },
  "outputs": {"total": {"type": "number", "description": "求和结果"}}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `code.script` | ✅ | — | 脚本全文；拿到 `input` 对象，**return 一个 map** |
| `code.lang` | — | `lua` | **只能**取系统提示「工作流脚本语言」里列出的那些；别凭印象写 —— `js` 这类常见语言多半没开池，要到运行期才报 `unsupported_lang`。没有那份清单时用 `lua`；`template` 只在 Result / Progress 节点用，Code 节点不提供 |
| `code.timeout` | — | 未启用 → 走运行上限 | `{enabled, seconds}` |
| `config.inputs` | — | — | 解析后作为脚本的 `input` |
| `config.outputs` | ✅ | — | 脚本 return 的每个 key 都要在这里声明，否则 `output var X not defined` |
| `config.targets` | — | — | 想让结果写进**祖先容器**的 outputs/locals 或**流程变量**时用（`source_type: node` / `flow`） |
| `config.locals` | — | — | 节点局部变量 |

## 输出变量

没有固定输出，全靠 `config.outputs` 声明。return 了**未声明**的 key 会直接报错
（`脚本返回了未声明的变量: X`），不是静默丢弃；声明了但没 return 的变量保持空。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 常见错误

- `script not found` —— `code` 子对象在，但 `script` 不是字符串。
- `脚本返回了未声明的变量: X` —— return 了没在 `outputs` / `targets` / `locals` 里声明的 key。
- `变量同时声明在 outputs 和 targets` —— 同名变量只能出现在三者之一。
- `运行上下文变量（env/argument/builtin 源）只读，不能作为赋值目标` —— 可写的只有祖先容器变量和流程变量；
  写流程变量不会触发它上面的 output binding（binding 仅对 `source_type: node` 生效）。
- `平台未给本租户开通能力` —— 脚本执行能力被平台关闭，重试无用，找管理员。
- 沙箱 worker 连不上 → `connection`（可重试）；选了平台不支持的语言 → `config`。

源码：`service/flow/executors/code.go`、`script_exec.go`
