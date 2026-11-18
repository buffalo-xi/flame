# 节点参考（按需读，别全读）

正文只讲主线；**具体节点怎么配，按类型读单文件**：

```
read_file("/skills/flow-authoring/references/nodes/<type>.md")
```

每个文件 2–6 KB。不要一次读完整个目录 —— 全读一遍 = 把正文又抄一遍，白费上下文。

## 索引

| type | 中文名 | `create_flow` 可建 | 一句话 | 文件 |
|---|---|---|---|---|
| `llm` | LLM | ✅ | 调模型出文本（可流式） | `llm.md` |
| `http` | HTTP 请求 | ✅ | 发 HTTP，支持 SSE | `http.md` |
| `code` | 代码 | ✅ | 沙箱里跑脚本（语言以系统提示的「工作流脚本语言」清单为准，缺省 lua） | `code.md` |
| `mcp` | MCP | ✅ | 调一个 MCP 工具 | `mcp.md` |
| `rpc` | RPC | ✅ | 调 RPC 连接工具，或本租户已对外开放端点 | `rpc.md` |
| `branch` | 条件分支 | ⚠️ | 条件成立走哪条出边（要出边） | `branch.md` |
| `intent` | 意图 | ⚠️ | 让模型从几条出边里选一条（要出边） | `intent.md` |
| `loop` | 循环 | ⚠️ | 重复执行自己的子节点（要子节点） | `loop.md` |
| `process` | 嵌入流程 | ⚠️ | 内联子流程容器（要子节点） | `process.md` |
| `function` | 函数 | ⚠️ | 可被 `call` 调用的内联函数（要子节点） | `function.md` |
| `call` | 调用 | ✅ | 调本 flow 内另一个 `function`（`target` 要填对 id） | `call.md` |
| `assign` | 赋值 | ✅ | 往输出/容器变量写值 | `assign.md` |
| `memory` | 记忆 | ✅ | 取会话记忆消息 | `memory.md` |
| `wait` | 并发同步 | ⚠️ | 等一个并发组（要同链的 `fork`） | `wait.md` |
| `fork` | 并发组 | ✅ | 并发组标记 | `fork.md` |
| `result` | 结果输出 | ✅ | 向外发一次结果 | `result.md` |
| `progress` | 进度输出 | ✅ | 向外发一条进度 | `progress.md` |
| `break` / `continue` / `return` / `exit` | 控制信号 | ✅ | 断循环 / 跳过一轮 / 返回函数 / 终止 | `control-signals.md` |
| `start` / `entry` | 开始 / 入口 | — | 画布结构节点，无执行器 | `visual.md` |
| `comment` / `layout` | 注释 / 布局 | — | comment 不连线；layout 可连线、运行时直通，可用 `create_flow` 建作折行拐点 | `visual.md` |
| `knowledge` | 知识 | ❌ | **后端无执行器，别用；工具也不允许创建**（只为渲染存量流程图保留） | `visual.md` |

> ✅ = 创建后直接能跑；⚠️ = `create_flow` 允许建，但缺结构时会空转（`branch`/`intent` 没写 `branches`、
> `loop`/`process`/`function` 没有子节点、`wait` 没有并发组），返回的 `warnings` 会逐条说明，
> 工具覆盖不到的部分要 `read_flow` 取出 diagram 到前端画布补。
> `create_flow` 的 `steps` 描述节点本身，外加 `branch`/`intent` 的 `branches`（每分支一条 `source<childId>` 出边）；
> 容器子节点、并发组仍不建。

## 所有节点共用的 `config` 键

| 键 | 谁读 | 说明 |
|---|---|---|
| `inputs` | 除 `process` / `function` / `call` / `memory` / `wait` / `fork` / 控制信号外 | 变量声明 `{type, source_type, source_key, source_ref}`，模板里用 `{{ .input.名字 }}` |
| `outputs` | 除 `branch` / `fork` / `wait` / 控制信号 / `result` / `progress` 外 | 输出变量声明 `{type, description?, binding?}`；下游 `source_key` 只能取这里声明过的名字 |
| `locals` | `code` / `loop` / `llm` / `process` / `function` | 节点局部变量 |
| `targets` | `code`（返回值分发）、`assign`；容器内任意节点 | 可写目标声明：祖先容器 outputs/locals（`source_type: node`，`source_ref: <容器ID>`）或流程变量（`source_type: flow`）；`env`/`argument`/`builtin` 只读 |
| `on_error` | 所有执行节点 | 按错误码重试 / 跳过 / 脚本兜底，见 `../on-error.md`。**动作必须在该节点的能力声明内**（`post` 仅 6 类、`intent` 只支持 `retry`、`wait` 只支持 `skip`、`branch`/`fork`/`assign` 不支持任何动作），声明外的配置会被丢弃并记警告 |
| `concurrency` | 执行型节点（llm / code / http / mcp / rpc / call 等） | **默认关闭：不要写这一项**（工具会摘掉并回 warning）。只有用户明确要求并发时才写，并按他的要求二选一：`{"mode":"mono"}` 单体并发（一个节点内部并发处理多份数据）、`{"mode":"fork","fork_node_id":"<id>"}` 并发组（多节点同时跑、`wait` 汇合）。同时要在工具参数里声明 `concurrency=mono\|group`，否则仍被摘掉。**`loop` / `process` 容器永远不带这一项** |

## 连线的三条事实

1. 除 `branch` / `intent` 外，一个节点**只能有一条出边**：多条时运行期报 `config` 错误
   （以前是静默只走 `out_edges[0]`，其余边永远不执行）。要分支必须用 `branch` / `intent`，它们自己挑边。
2. 容器（`process` / `loop` / `function`）里必须有且只有一个 `entry` 子节点（`parentNode = 容器 id`、`type = "entry"`），容器从这个 entry 沿第一条出边跑到底；找不到 entry 时三个容器都报 `config` 错误（不再静默跳过）。
3. 容器（`process` / `loop` / `function`）跑完后，回到容器节点自己的出边继续；`branch` / `intent` 例外：命中分支的子链在自己那条出边之后一路跑到底，分支节点本身**不再沿自己的出边往下走**（未命中的分支边全部跳过）。

## 证据

本节事实来自 `service/flow/executors/runner.go`（出边与 `concurrency`）、`execContext.go`（`inputs` / `targets`）、各执行器的 `Execute`（键名与缺省值），以及前端 `desktop/.../flows/nodeFactory.js`（新建节点写出的默认结构）。
