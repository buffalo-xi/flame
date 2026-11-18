# `wait` 节点

并发同步点：等一个 `fork` 组里的并发分支全部结束。

**只在并发组场景（用户明确要求并发、且声明了 `concurrency=group`）才用到**；
并发默认关闭，单独一个 `wait` 没有意义。

## config 形状

```json
{
  "fork_node_id": "<同一 process 链上的 fork 节点 id>",
  "timeout": {"enabled": false, "seconds": 30}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `fork_node_id` | ✅ | — | `fork` 节点的 id，必须在**同一条 process 链**上；没填、指错、不在同一条链上都是 `config` 错误（不再报 `internal`） |
| `timeout` | — | 未启用 → 走实例/系统上限（**不是 30 秒**） | `{enabled, seconds}` |

无输出变量。

## 行为

扫并发组成员的状态：任一 `failed` / `cancelled` → 本节点失败（`other`）；
超时还没全部完成 → `timeout`。同一 fork 只能被一个 `wait` 等（重复 wait → `config`，属于图结构错误）。

## 出边

普通节点：同步完成后沿**第一条出边**继续。

源码：`service/flow/executors/wait.go`
