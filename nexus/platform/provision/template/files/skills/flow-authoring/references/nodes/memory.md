# `memory` 节点

读当前会话的记忆消息，作为后续 prompt 的历史。

## config 形状

```json
{
  "outputs": {"messages": {"type": "array[object]"}},
  "limit": 10,
  "window": {"enabled": false, "unit": "hour", "value": 24}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `outputs` | — | 自动补齐 | 固定输出 `messages`(array[object]) |
| `limit` | — | 未设 → 取 `ResolveMaxSessionRecords()` | 取最近 N 条；`<=0` 视为未设 |
| `window` | — | `{enabled:false, unit:"hour", value:24}` | 时间窗；`enabled=true` 且 `value>0` 才生效；`unit`：`second` / `minute` / `hour` / `day` |

**不读** `inputs` / `locals`。没有 `session_id` 或 `limit` 无效时 → 输出**空数组**且节点成功。

## 输出变量

`messages`：`[{role, content, timestamp}]`，旧 → 新；一条会话记录会拆成 user / assistant 两条。

## 出边

普通节点：执行完沿**第一条出边**继续。

## 常见错误

- 拿到空数组但不是报错 —— 多半是没有会话上下文（无状态调用），或 `limit` / `window` 配成了 0。

源码：`service/flow/executors/memory.go`
