# `fork` 节点

并发组标记：本身不做事，只是给「要并发执行的节点」一个组名（节点 id）。

## config 形状

```json
{"max_concurrency": -1}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `max_concurrency` | — | `-1`（不限流） | 该组同时最多跑几个；`>=1` 才限流。**必须是数字**，类型不对（如字符串）直接报 `config` 错误，不再静默当成“不限制” |

无输出变量，从不失败。

## 怎么让节点加入并发组

**并发默认关闭**：工具会摘掉节点上的 `config.concurrency` 并回 warning，除非用户明确要求并发、
且调用时声明了 `concurrency=group`（`create_flow` / `create_node` / `insert_node`）。

开启后在**要并发的那个节点**上写 `config.concurrency`：

- 缺省 = 串行，正常沿出边走；
- `{"mode": "mono"}` = 单体并发（一个节点内部并发处理多份数据，不属于任何 fork 组）；
- `{"mode": "fork", "fork_node_id": "<fork 节点 id>"}` = 加入该 `fork` 组（并发组并发）。

被标记并发执行的节点**只执行自己，不沿出边**继续 —— 后续由 `wait` 节点汇合。

## 出边

`fork` 节点本身按普通节点走**它唯一的那条出边**（通常直接连到 `wait` 或不连）。

并发分支**不是**从 `fork` 拉出去的 —— 是靠成员节点自己的 `config.concurrency` 声明（见上）。
所以 `fork` 上连第二条出边没有任何意义，运行期会报 `config` 错误。

源码：`service/flow/executors/fork.go`、`runner.go`
