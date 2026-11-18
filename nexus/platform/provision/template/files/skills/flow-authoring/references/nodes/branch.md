# `branch` 节点

按条件把流程导向不同的出边。每个子分支都要有**自己的一条出边**（`intent` 同理）—— 用工具创建时靠 `branches` 表达（见下「用 create_flow 建」），画布上则是手工连线。

## config 形状

```json
{
  "branch": {"lang": "lua", "timeout": {"enabled": false, "seconds": 30}},
  "inputs": {
    "score": {"type": "number", "source_type": "node", "source_ref": "2", "source_key": "total"}
  },
  "children": [
    {"id": "a1b2c", "label": "高分", "condition": "return input.score >= 90", "exclusive": true},
    {"id": "d3e4f", "label": "中分", "condition": "return input.score >= 60", "exclusive": false}
  ]
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `children` | ✅ | — | 普通分支数组；空数组 → `config` 错误 |
| `children[].id` | ✅ | — | 与出边的 `source_handle` 对应（`source<id>left` / `source<id>right`）；空 id 的分支被忽略 |
| `children[].label` | — | `id` | 只用于日志 |
| `children[].condition` | — | 空 = 跳过该分支 | 脚本文本，需 `return` 一个值；真值判定：bool 本身、字符串仅 `"true"/"True"/"TRUE"`、数字非 0 |
| `children[].exclusive` | — | `true` | **缺省排他**：命中且子链跑完后立即结束本节点，不再尝试后续分支与默认分支。显式写 `false` 才是非排他（继续比对，多条命中的都执行） |
| `branch.lang` | — | `lua` | 条件脚本语言 |
| `branch.timeout` | — | 关闭 | `{enabled,seconds}`；关闭时不设节点级超时，由运行上限兜底 |
| `inputs` | — | — | 所有分支共用的变量，条件脚本里是 `input.*` |

**不读** `outputs` / `locals`：`branch` 节点没有输出变量。

## 出边怎么连

| 分支 | `source_handle` |
|---|---|
| 普通分支 `children[i]` | `source<childId>left` 或 `source<childId>right`（前缀匹配，每 key 取第一条） |
| 默认分支 | `source<nodeId>left` / `right` / `bottom`（key = **分支节点自己的 id**） |

全部不命中且没有默认分支出边时，节点成功但**没有任何子链被执行**（不会报错）。
每条分支的子链在独立子进程里跑，变量与主链隔离，沿目标节点一路跑到底；子链出错会带着错误往上抛（不会触发 branch 自己的 `on_error`）。
命中分支之后，branch 节点**不会**再沿自己的第一条出边继续（出边全部由分支语义消费）。

## 用 `create_flow` 建（`branches`）

工具不让你手写 diagram，但**每条子分支必须有出边**这件事得表达出来，所以分支这一步要带
`branches`（`key` = `children[].id`，`default` 是可选兜底）：

```json
{
  "type": "branch", "label": "判断是否等于45",
  "config": {
    "inputs": {"result": {"type": "number", "source_type": "node", "source_ref": "计算5*9", "source_key": "result"}},
    "children": [
      {"id": "c1", "label": "等于45",   "condition": "return input.result == 45", "exclusive": true},
      {"id": "c2", "label": "不等于45", "condition": "return input.result ~= 45", "exclusive": true}
    ]
  },
  "branches": [
    {"key": "c1", "steps": [{"type": "result", "label": "输出正确", "config": {"code": {"lang": "template", "script": "正确"}}}]},
    {"key": "c2", "steps": [{"type": "result", "label": "输出错误", "config": {"code": {"lang": "template", "script": "错误"}}}]}
  ]
}
```

生成结果：分支节点向 c1 链首拉一条 `sourcec1right`、向 c2 链首拉一条 `sourcec2right`；
`key: "default"` 的链用 `source<分支节点自己的 id>right`。

- **必须是所在链的最后一步**：branch 执行完只走 default 边，分支之后的主链没有语义。
- 子链可以再嵌 `branch`（id 按建图顺序连续分配，不必心算）。
- `children` 有、`branches` 漏掉的子分支只给 `warnings`（命中后会被跳过），不阻断创建。

## 默认分支是「收尾链」，不是 else

按 `children` 顺序逐条求值，规则：

| 情况 | 结果 |
|---|---|
| 某条条件为真且 `exclusive=true`（缺省） | 跑该分支子链 → **结束节点**，后续分支与默认分支都不跑 |
| 某条条件为真且 `exclusive=false` | 跑该分支子链 → 继续比对下一条 |
| 条件为假 / 空条件 | 跳过，继续下一条 |
| 全部比完、无任何命中 | 跑默认分支（没有默认出边则节点成功结束） |
| 全部比完、有命中但都是 `exclusive=false` | **仍会跑默认分支**（它是收尾链，不是 else） |

所以「所有情况都要执行的共同尾链」只有非排他分支走得到；排他分支命中后节点就结束了，需要收尾就在该分支自己的子链末尾继续连节点。

## 常见错误

- `分支节点没有配置任何分支` —— `children` 空。
- `分支 X 条件执行失败` —— 条件脚本报错（**直接失败**，不是跳过）。
- 分支永远不走 —— `condition` 写成 `input.score > 90`（没有 `return`），或 `inputs` 里没声明 `score`。

源码：`service/flow/executors/branch.go`
