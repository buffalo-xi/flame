# `on_error`：按错误码重试 / 跳过 / 脚本兜底

`config.on_error` 是四块可选配置，**匹配依据是错误码**（`codes` 必须写，不写等于没开）：

```json
{
  "on_error": {
    "retry":   {"codes": ["timeout", "connection"], "patterns": [], "max": 2, "delay_ms": 1000},
    "skip":    {"codes": ["not_found"], "patterns": []},
    "handler": {"codes": ["other"], "patterns": ["%rate limit%"], "lang": "lua",
                "script": "return 'skip'", "inputs": {}},
    "post":    {"set_outputs": {"text": "暂时不可用"},
                "handler": {"lang": "lua", "script": "return { text = '兜底' }", "inputs": {}}}
  }
}
```

## 匹配顺序

`retry` → `skip` → `handler` → 都没命中就失败。

## retry

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `codes` | ✅ | — | 错误码列表；**空数组 = 不重试**。可用码见下 |
| `patterns` | — | — | 仅在 `codes` 含 `other` 时生效：按消息匹配，`%x%` 包含、`x%` 前缀、`%x` 后缀，无 `%` 也是包含 |
| `max` | — | `0` | 重试次数（不含首次）；`<=0` 不重试 |
| `delay_ms` | — | `0` | **固定**间隔毫秒，没有退避 |

## skip

| 键 | 说明 |
|---|---|
| `codes` / `patterns` | 命中后节点记 `skipped`（按成功继续），错误码可查；下游拿到的是**空输出** |

「查询失败也要往下走」用它；需要补默认值就配 `post.set_outputs`。

## handler

| 键 | 说明 |
|---|---|
| `codes` / `patterns` | 命中才跑脚本 |
| `lang` | 缺省 `lua`；**不支持 `template`** |
| `script` | 入参：`error_code` / `error_message` / `error_node_id` / `error_node_type` / `retry_count` + `inputs` |
| `inputs` | 额外变量声明 |

脚本返回：

- 字符串 `"skip"`（兼容旧写法）或 `{action: "skip"}` → 按跳过处理；
- map 里带 `error_code` / `error_message` → **改写**错误码与文案后仍然失败；
- 其它 → 仍然失败。

## post（兜底，只在特定节点生效）

| 键 | 说明 |
|---|---|
| `set_outputs` | 静态输出，直接写进节点输出变量 |
| `handler` | 兜底脚本 `{lang, script, inputs}`，return 的 map 覆盖输出变量 |

- 只在**最终失败 / 跳过**时执行（`handler` 脚本自己报错的那条路径不执行）。
- **只有 `http` / `llm` / `code` / `mcp` / `rpc` / `call` 支持 post**；其余节点即使写了也会被忽略
  （执行器声明 `HasPost=false`，runner 直接把 `post` 置空）。
- 前端 `on_error` 面板也只给这几类节点 + `wait`（wait 仅跳过）。

## 三个硬规则（不是建议，是代码行为）

1. **`config` / `internal` / 取消 / 控制信号一律绕过 `on_error`**：配置类错误重试也没用，
   直接修配置；`break` / `continue` / `return` / `exit` 是控制流，不是错误。
2. **节点的 `nr.Status` 必须是 `failed` 才会进 `on_error`**：`branch` / `intent` 的子链错误
   按容器语义原样上抛（本节点状态不是 failed），所以**不会触发它们自己的 on_error**。
   `process` / `loop` / `function` 则相反：子链错误会让容器本身 `failed`，容器的 on_error 会生效（虽然前端没面板）。
3. **错误码只在节点声明的范围内有效**：声明外的码会被降级成 `other`，你的 `codes` 里写那个码就不匹配了。
   降级时错误文案后面会附上原始码（`… (原始错误码 var)`），照着它改 `codes` 就行。
4. **节点没声明支持的动作会被直接忽略**：`retry` / `skip` / `handler` 和 `post` 一样，必须在该节点的能力
   声明里，否则配置被丢弃并记一条警告。比如 `intent` 只声明了 `retry`，给它配 `skip` 不生效；
   `branch` / `fork` / `assign` 不支持任何动作，`wait` 只支持 `skip`。

## 重试留下的记录

每次尝试都是一条独立记录（首次 + 每次重试）。`skip` / `handler` / `post` 都作用在**最后一次尝试**上：
节点最终的 `status`、`error_code`，以及 `post.set_outputs` 写进去的输出，都落在最后那条记录里。

- 所以「重试都失败、但配了 skip」时节点是 `skipped` 而不是 `failed`；
- handler 脚本里的 `error_code` / `retry_count` 也是最后一次的值，不是第一次的。

## 错误码

`timeout` / `busy`（沙箱并发额度耗尽，脚本没跑）/ `cancel` / `config` / `script` / `capability`（平台未开通）/
`auth` / `not_found` / `connection`（网络或沙箱 worker 连不上，**可重试**）/ `var` /
`internal`（平台侧故障，**不走 on_error**）/ `other`。

源码：`service/flow/executors/runner.go`（`parseOnErrorConfig` / `applyOnError` / `shouldBypassOnError`）、
`service/flow/core/nodeResult.go`（错误码常量）
