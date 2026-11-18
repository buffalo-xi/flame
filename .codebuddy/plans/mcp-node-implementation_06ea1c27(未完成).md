---
name: mcp-node-implementation
overview: 新增 mcp 节点：支持输入变量（inputs）+ connection_id/tool_name/arguments + 固定输出变量 result/progress/error + handler 脚本（仅 lua/python/javascript）；后端 InvokeWithProgress 流式进度；顺带收紧 HTTP/LLM/on_error handler 语言（方式 B 显式报错排除 template）。
todos:
  - id: mcp-client-progress
    content: 在 invoke.go 新增 InvokeWithProgress 流式入口，建连时注入 ProgressNotificationHandler 并设置 progress token
    status: pending
  - id: parse-handler-error
    content: 改 parseHandler 加 error 返回并校验非 template，同步更新 http.go 与 llm.go 调用点
    status: pending
  - id: mcp-executor
    content: 实现 mcp.go 的 McpExecutor：输入变量解析、建连、流式收集 progress、handler 脚本、按规则写 result/progress/error
    status: pending
    dependencies:
      - mcp-client-progress
      - parse-handler-error
  - id: node-type-constant
    content: 在 nodeResult.go 的 NodeType 常量新增 NodeTypeMCP = "mcp"
    status: pending
    dependencies:
      - mcp-executor
  - id: frontend-explore
    content: 用 [subagent:code-explorer] 核对前端 MCP 连接/工具 API 与 HttpNodeProps.vue 可复用结构
    status: pending
  - id: mcp-node-props
    content: 新增 McpNodeProps.vue：输入变量、连接/工具选择、参数、handler（仅 lua/python/javascript）、outputs
    status: pending
    dependencies:
      - frontend-explore
  - id: register-frontend
    content: 在 PropsPanel.vue 和 NodePanel.vue 注册 mcp 节点类型与卡片，并收紧 HttpNodeProps/LLMNodeProps/OnErrorConfig 的 handler 语言下拉框
    status: pending
    dependencies:
      - mcp-node-props
  - id: build-verify
    content: go build 验证后端编译通过，前端 lint 通过并复核字段一致
    status: pending
    dependencies:
      - mcp-executor
      - register-frontend
---

## 产品概述
在工作流（pipe）中新增 `mcp` 节点，用于调用远端 MCP 工具。节点支持**输入变量**、**handler 脚本**、**流式进度接收**，并复用项目 HTTP/LLM 节点成熟的「固定输出变量 + 流式绑定」机制。同时顺带收紧 HTTP/LLM/on_error 的 handler 脚本语言（显式报错排除 template）。

## 核心特性
- 新增 `mcp` 节点类型，配置：`connection_id`（连接）、`tool_name`（工具名）、`arguments`（参数，支持模板渲染）、`inputs`（输入变量）、`handler`（脚本）、`outputs`（输出变量）
- **输入变量**：与 HTTP/LLM 一致，`config.inputs` 定义输入变量来源，`resolveInputVars` 解析，供 arguments 模板用 `{{.input.xxx}}` 引用
- 固定输出变量：`result`(string)、`progress`(array[string])、`error`(string)
- handler 脚本仅限 lua/python/javascript（排除 template）
- 流式接收 MCP progress 通知：`InvokeWithProgress` 边调边推

## 输出变量写入规则
| 场景 | progress 数组 | result | error |
|------|--------------|--------|-------|
| 无 handler | 自动写 | 自动写 | 自动写（仅 IsError） |
| 有 handler | 自动写（基础设施输出） | 脚本写 | 脚本写 |

- `progress` 数组**仅在 CallTool 返回后写一次**（定稿）；逐条 progress 到达时只「收集 + 有 handler 才同步执行脚本」，不写变量
- progress 写一次时机：CallTool 返回后、收尾 handler 之前
- 逐条 progress 的 handler eventData：`progress`（本条结构化对象）+ `progress_raw`（本条完整原始 JSON 字符串）
- 收尾 handler 的 eventData：`result`、`is_error`(bool)、全量 `progress` 数组、`done`(true)


## 技术栈
- 后端：Go + Gin，`github.com/modelcontextprotocol/go-sdk v1.7.0-pre.1`，`flame/tools/xsql`
- 前端：Vue 3 + Element Plus（`nexus_desk`），组件内 `fetch` 调用 admin API
- 数据库：复用 `mcp_connections`（连接配置）与 `mcp_connection_tools`（工具列表）

## 实现方案

### 策略
后端新增 `executors/mcp.go`，复用 `http.go` 的 `parseHandler`/`executeHandlerScript`/`mergeInputs`/`resolveInputVars`/`RenderTemplate`/`BuildScriptTemplateContext` 等包级函数。MCP 客户端层新增带进度回调的流式入口 `InvokeWithProgress`（不改现有函数签名）。前端新增 `McpNodeProps.vue` 并注册到 `PropsPanel.vue`/`NodePanel.vue`。

### 关键技术决策

#### 1. 输入变量（复用 HTTP/LLM 模式）
MCP 节点 config 含 `inputs`（与 HTTP/LLM 同结构：每个变量含 `source_type`/`source_key`/`source_ref`/`type`）。执行流程：
- `inputArgs, err := resolveInputVars(node, ctx, processID)` 解析输入变量
- `tmplCtx := BuildScriptTemplateContext(nr, inputArgs)` 构建模板上下文
- arguments 中字符串用 `RenderTemplate(str, tmplCtx)` 渲染，引用 `{{.input.xxx}}`

#### 2. 新增 `InvokeWithProgress`（不改现有函数）
现有 `NewClientSession`/`newStdioSession` 均 `mcp.NewClient(ClientImpl, nil)` 且被 `discover.go` 等多处复用，不能改签名。故新增独立建连函数：
- 建连时注入 `&mcp.ClientOptions{ ProgressNotificationHandler: func(ctx, req){ onProgress(req.Params) } }`
- `params.SetProgressToken(uuid.NewString())` 设置 progress token
- **v1 统一走一次性建连**（streamable_http 与 stdio 都临时建连，不复用 stdio 进程池）。理由：池化 session 的 progress handler 是建连时定的，流式 progress 需按「当前调用」路由，路由复杂度高；一次性建连语义清晰、无并发串扰。stdio 池化 + 流式 progress 作为后续独立优化。

```go
// nexus/service/mcp/client/invoke.go 新增
func InvokeWithProgress(
    ctx context.Context,
    conn *models.McpConnection,
    toolName string,
    arguments map[string]any,
    onProgress func(*mcp.ProgressNotificationParams),
) (*ToolResult, error)
```

#### 3. 并发与加锁（onProgress 与主协程并发）
`ProgressNotificationHandler` 在 SDK 后台读协程触发，与阻塞在 `CallTool` 的主协程并发执行：
- `progressCollector` 用 `sync.Mutex` 保护 `append`
- 有 handler 时逐条同步执行脚本：`nr.OutputVars` 由 `NodeResult.mu` 保护，写变量安全；collector append 独立加锁
- CallTool 返回后主协程写 progress 数组 + 收尾 handler，此时 onProgress 已结束，无竞争

```go
// nexus/service/pipe/executors/mcp.go
var mcpFixedOutputVars = map[string]string{
    "result":   "string",
    "progress": "array[string]",
    "error":    "string",
}

type progressCollector struct {
    mu   sync.Mutex
    list []string
}
func (c *progressCollector) append(raw string)
func (c *progressCollector) snapshot() []string
```

#### 4. handler 脚本语言收紧（方式 B 显式报错）
`parseHandler` 当前签名 `(script, lang string, timeout int)` 无 error 返回，且不做校验。改为加 error 返回：
- 签名改为 `parseHandler(cfg, ctx) (script, lang string, timeout int, err error)`
- `lang == "template"` 时返回错误「handler 不支持 template 语言」
- http.go:104 与 llm.go:83 两个调用点捕获 error 后 `nr.Finish(StatusFailed, ErrCodeConfig, ...)` 并返回
- MCP 节点同样调用 `parseHandler` 获得校验，行为一致

前端收紧 3 处 handler/post 语言下拉框（过滤掉 template，不影响 Code/Progress/Result/Branch/Loop 的代码/条件脚本）：
- `HttpNodeProps.vue`：handler 语言下拉框
- `LLMNodeProps.vue`：`handlerLanguages` computed
- `OnErrorConfig.vue`：handler + post 语言下拉框

#### 5. result 值形态
`result` 固定变量存 `flattenResult` 的 `Content`(string)；`Structured` 暂不单独落变量（后续可议）。

### 输出写入流程（固化）
```
CallTool 返回前（onProgress 并发回调）:
  raw = json.Marshal(p) → coll.append(raw)（加锁）
  if handler: 同步执行 handler(eventData={progress:结构化对象, progress_raw:raw})

CallTool 返回后（主协程）:
  nr.SetOutputVarWithFire("progress", coll.snapshot())  // 写一次定稿，覆盖语义
  if handler: 收尾 handler(eventData={result, is_error, progress:全量数组, done})
  else:
    if res.IsError: SetOutputVarWithFire("error", ...); Finish(success)
    else: SetOutputVarWithFire("result", ...); Finish(success)
```

## 架构设计
```mermaid
flowchart TD
    A[前端 McpNodeProps.vue] -->|保存节点配置| B[Pipe 工作流]
    B --> C[McpExecutor.Execute]
    C --> D[resolveInputVars 解析输入变量]
    D --> E[BuildScriptTemplateContext + 渲染 arguments]
    C --> F[mcpconnections.GetByID 查连接]
    C --> G[parseHandler + 校验非 template]
    C --> H[InvokeWithProgress]
    H --> I[一次性建连 + ProgressNotificationHandler]
    I --> J[onProgress 流式回调]
    J --> K[progressCollector 加锁 append]
    J -->|有handler| L[逐条同步执行 handler 脚本]
    H --> M[CallTool 返回 ToolResult]
    M --> N[写 progress 数组定稿]
    N --> O{有 handler?}
    O -->|是| P[收尾 handler 脚本写 result/error]
    O -->|否| Q[自动写 result 或 error]
```

## 目录结构
```
d:/proj/flame/
├── nexus/service/mcp/client/invoke.go        # [MODIFY] 新增 InvokeWithProgress + 带 ProgressNotificationHandler 的一次性建连
├── nexus/service/pipe/executors/mcp.go       # [NEW] MCP 节点 executor（输入变量、流式收集 progress、handler、写 result/progress/error）
├── nexus/service/pipe/executors/http.go      # [MODIFY] parseHandler 加 error 返回 + 校验非 template；调用点处理 error
├── nexus/service/pipe/executors/llm.go       # [MODIFY] parseHandler 调用点处理 error
├── nexus/service/pipe/flow/nodeResult.go     # [MODIFY] NodeType 常量加 NodeTypeMCP = "mcp"
└── nexus_desk/src/components/pipeinstances/pipe/
    ├── McpNodeProps.vue                       # [NEW] MCP 节点配置面板（输入变量、连接/工具选择、参数、handler、outputs）
    ├── HttpNodeProps.vue                      # [MODIFY] handler 语言下拉框过滤 template
    ├── LLMNodeProps.vue                       # [MODIFY] handlerLanguages 过滤 template
    ├── OnErrorConfig.vue                      # [MODIFY] handler/post 语言下拉框过滤 template
    ├── PropsPanel.vue                         # [MODIFY] 注册 mcp 分支 + import + 图标
    └── NodePanel.vue                          # [MODIFY] 加 MCP 节点卡片 + 图标
```

## 实现注意事项
- 输出变量必须先在 `InitOutputVars` 定义，否则 `SetOutputVarWithFire` 报 "output var not defined"；progress 是 `array[string]`，不在 `InitOutputBindings` 绑定范围（仅 string/number/boolean 可绑），符合预期
- `progressCollector.append` 加锁粒度保持最小临界区，避免长时间持锁
- `InvokeWithProgress` 内一次性建连需 `defer session.Close()` 释放资源
- `parseHandler` 改签名是破坏性变更，需同步更新 http.go:104、llm.go:83 两处调用点，并确认无其他调用点（已核对仅此两处 + 新增的 mcp.go）
- MCP 节点 `Capabilities()` 返回 `ErrCodeConfig/Connection/Timeout/Auth/NotFound/Script/Var/Other`，`Actions` 含 retry/skip/handler，`HasPost: true`（对齐 http/llm）


## Agent Extensions
### SubAgent
- **code-explorer**
  - Purpose: 实现前端前核对 `admin.js` 中 MCP 连接/工具 API 配置键与 `HttpNodeProps.vue` 中「输入变量」「handler」「outputs」编辑区块的可复用结构，确保 `McpNodeProps.vue` 与现有面板交互一致，并确认 `parseHandler` 仅 http.go/llm.go 两处调用无遗漏
  - Expected outcome: 产出前端 API 配置键（`adminApi.mcpConnections`）、可复用组件结构清单、以及 `parseHandler` 调用点完整清单，避免实现偏离既有模式或遗漏改动
