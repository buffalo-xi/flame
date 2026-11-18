# 连边：`create_edge` / `update_edge` / `delete_edge`

`create_flow` 用 `branches` 表达分支子链，除此之外的连线（容器内的子节点、`fork` / `wait`
并发组、改接、删边）用这三个工具。它们**只动 `edges`**：不建节点、不改 `config`。

> **规则只认工具**：handle 形状、各类型可用位置、硬拦项、按位置的建议，全部写在
> `create_edge` / `update_edge` 的参数说明里，**以参数说明与报错为准**。
> 本文不再抄一份规则 —— 两处各写一套，改了一处忘了另一处，模型就会按过期说法写错。
>
> handle 拿不准**直接调**：缺了会按两个节点的位置把该填的值写进报错；给的组合偏了会回
> `suggested_edge`（合法、已落库，照它 `update_edge` 即可少绕线）。

## 一条边的形状

```json
{"id": "04958", "source": "45063", "target": "07003",
 "source_handle": "source38245right", "target_handle": "target07003left"}
```

| 字段 | 说明 |
|---|---|
| `id` | 边 id（5 位数字串，工具自动生成且不与节点 id / 子分支 id 撞车） |
| `source` | 发出节点 id |
| `target` | 到达节点 id |
| `source_handle` | `source` + **handle owner** + 位置；owner 一般是节点 id，`branch` / `intent` 的子分支是 `config.children[].id` |
| `target_handle` | `target` + **到达节点 id** + 位置（owner 恒等于 `target`） |

`create_flow` 生成的边同样带 handle：主链按排版后的位置算（同一行 `right` → `left`），
分支子链首边的 `source_handle` 是**语义 key**（`source<children[].id>right`，执行器靠前缀匹配找出边），
读到它别改 —— 改了就断分支。

## 用法

```json
// 连一条边
{"flow": "daily-sales-report", "source": "2", "target": "3",
 "source_handle": "source2right", "target_handle": "target3left"}

// 分支子分支的出边：owner 是 children[].id
{"flow": "calc-branch", "source": "3", "target": "5",
 "source_handle": "sourcec1right", "target_handle": "target5left"}

// 默认分支出边：owner 是分支节点自己的 id
{"flow": "calc-branch", "source": "3", "target": "6",
 "source_handle": "source3right", "target_handle": "target6left"}
```

```json
// 改接：改 target 必须同时给 target_handle（handle 里嵌的是节点 id）
{"flow": "daily-sales-report", "edge_id": "04958", "target": "9", "target_handle": "target9left"}

// 删边
{"flow": "daily-sales-report", "edge_id": "04958"}
```

## 返回体

| 工具 | 返回 |
|---|---|
| `create_edge` | `flow_id` `flow_name` `edge` `edge_count`；顶替旧入边时多 `replaced_edge` + `note`；handle 与位置不匹配时多 `suggested_edge` + `handle_hint` |
| `update_edge` | 同上，`edge` 是更新后的边 |
| `delete_edge` | `flow_id` `flow_name` `deleted`（被删的边） `edge_count` |

## 常见误判

- **拿节点 id 当边 id**：`edge_id` 只能来自 `read_flow` 的 `diagram.edges[].id`，不是 `nodes[].id`。
- **以为 `branches` 和 `create_edge` 互斥**：`branches` 建的是分支节点的出边；已经有
  `branches` 的流程再用 `create_edge` 连同一个 `source_handle` 会撞「一个出口只能一条边」。
- **给一个节点连两条出边**：只有 `branch` / `intent` 的多出边有语义（每个子分支一条）。
  其余节点（含 `fork`）只能有一条出边。注意工具只按 `source_handle` 判重
  （`source<id>right` 与 `source<id>bottom` 是两个出口，能各连一条），所以「同一节点两条出边」
  工具拦不住 —— 运行期会报 `config` 错误（以前是静默只走第一条，第二条永远不执行）。
- **以为能连容器内外的节点**：`parent_node` 不同就直接拒绝，这是画布的硬约束。
- **照本文抄 handle**：本文不列位置表；位置由工具参数说明定义，拿不准就让工具报错或给建议。
- **改完不看结构**：边工具只改边，节点 `config` 一概不动；改完 `read_flow` 回读一次再试跑。
