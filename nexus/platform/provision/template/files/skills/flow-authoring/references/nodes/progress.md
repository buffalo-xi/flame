# `progress` 节点

向外发一条「进度」（思考过程 / 中间状态），不进入最终结果。

## config 形状

```json
{
  "category": "text",
  "code": {
    "lang": "template",
    "script": "正在处理第 {{ .input.index }} 条…",
    "timeout": {"enabled": false, "seconds": 10}
  },
  "inputs": {"index": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "index"}}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `code` | ✅ | — | `{lang, script, timeout}`；`lang` 通常 `template` |
| `code.script` | ✅ | — | 进度文本；空 / 缺失 → `script` 错误 |
| `category` | — | `text` | 节点级配置；`text`（进度）或 `thinking`（思考），决定前端展示在哪个区 |
| `inputs` | — | — | 模板来源 |

**不提供 `outputs` 面板**：`output` 由执行器自动写。

## 输出变量

`output`(string)：本次进度文本。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 与 result 的区别

| | `progress` | `result` |
|---|---|---|
| 去向 | 进度通道（可多条） | 结果通道（最终答复） |
| 分栏 | `category: text` / `thinking` | 无 |
| 流式 | 逐条发 | `stream` 控制 |

源码：`service/flow/executors/progress.go`
