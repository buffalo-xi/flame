# `result` 节点

向外发一次「结果」（对话正文 / 最终答复）。脚本渲染出什么就发什么。

## config 形状

```json
{
  "code": {
    "lang": "template",
    "script": "{{ .input.summary }}",
    "timeout": {"enabled": false, "seconds": 10}
  },
  "inputs": {"summary": {"type": "string", "source_type": "node", "source_ref": "3", "source_key": "content"}},
  "stream": false
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `code` | ✅ | — | 复用脚本节点结构 `{lang, script, timeout}`；**`lang` 通常是 `template`**（纯渲染，走沙箱外） |
| `code.script` | ✅ | — | 结果文本；空 / 缺失 → `script` 错误 |
| `inputs` | — | — | 模板来源 |
| `stream` | — | `false` | **是 bool 不是对象**；`true` = 逐条作为流式结果外发，`false` = 只累积、run 结束一次性给 |

**不要写 `config.outputs`**：前端保存时会把它剥掉（节点输出 `output` 由执行器自动写）。

## 输出变量

`output`(string)：本次结果文本（不对外声明，需要的话用 `assign` 接）。

## 出边

普通节点：执行完沿**第一条出边**继续（一般连到下一个 `result` 或直接结束）。

## 常见错误

- `result code not found` —— 少写 `code` 子对象。
- 结果里出现 `<no value>` —— `inputs` 没声明或模板名字写错。

源码：`service/flow/executors/result.go`
