# 控制信号：`break` / `continue` / `return` / `exit`

四个节点都**没有 config**（`config` 留空即可），执行时立即以 `success` 收尾并抛出一个内部信号：

| 节点 | 信号 | 生效范围 | 典型用途 |
|---|---|---|---|
| `break` | `loop_break` | **最内层** `loop`：结束循环，循环成功 | 找到目标就跳出 |
| `continue` | `loop_continue` | 最内层 `loop`：跳过本轮剩余节点 | 跳过空元素 |
| `return` | `function_return` | 最内层 `function`：正常返回；**在 loop 里则退出整个 loop**（循环成功）；在顶层 = 结束本次 run | 函数提前返回 |
| `exit` | `flow_exit` | 一路向上，**终止整个 run**（所有容器都成功收尾） | 命中终止条件，立刻收工 |

## 约束

- `break` / `continue` **只能在 `loop` 内**：`function` 体内直接写会报 `config` 错误（`break/continue 只能在 loop 内使用`）。
- 这四者都不会触发 `on_error`：它们不是错误。
- 节点自身状态记 `success`，但**不沿出边继续**。

## 常见错误

- 「continue 没生效」—— 它在一个 `process` 里，而 `process` 不在 `loop` 内：信号会一路传到 loop 才生效，中途不会再执行任何节点。
- 「return 没返回」—— 写在了 loop 里：语义是退出 loop，不是退出 run。

源码：`service/flow/executors/control.go`
