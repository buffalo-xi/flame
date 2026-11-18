---
name: mcp-node-implementation
overview: 在工作流（pipe）中新增 mcp 节点：后端实现 McpExecutor 调用远端 MCP 工具并流式接收 progress 通知，复用 handler 脚本 + 固定输出变量（result/progress/error）机制；前端新增 McpNodeProps 节点配置面板。
todos:
  - id: mcp-client-progress
    content: 在 invoke.go 新增 InvokeWithProgress 流式入口，建连时注入 ProgressNotificationHandler 并设置 progress token
    status: pending
  - id: mcp-executor
    content: 实现 mcp.go 的 McpExecutor：解析配置、建连、流式收集 progress、handler 脚本、按规则写 result/progress/error
    status: pending
    dependencies:
      - mcp-client-progress
  - id: node-type-constant
    content: 在 nodeResult.go 的 NodeType 常量新增 NodeTypeMCP = "mcp"
    status: pending
    dependencies:
      - mcp-executor
  - id: frontend-explore
    content: 用 [subagent:code-explorer] 核对前端 MCP 连接/工具 API 与 HttpNodeProps.vue 可复用结构
    status: pending
  - id: mcp-node-props
    content: 新增 McpNodeProps.vue 配置面板：连接选择、工具选择、参数、handler、outputs
    status: pending
    dependencies:
      - frontend-explore
  - id: register-frontend
    content: 在 PropsPanel.vue 和 NodePanel.vue 注册 mcp 节点类型与卡片
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
在工作流（pipe）中新增 `mcp` 节点，用于调用 MCP 工具。复用项目已有的 MCP 客户端能力与 HTTP/LLM 节点的 handler 脚本 + 固定输出变量 + 流式绑定机制，支持流式接收进度通知并自动维护 progress 数组。

## 核心特性
- 新增 `mcp` 节点类型：配置连接（connection_id）、工具名（tool_name）、参数（arguments，支持模板渲染）
- 固定输出变量：`result`(string)、`progress`(array[string])、`error`(string)
- 支持 handler 脚本（lua/js/py/template），与 HTTP/LLM 节点对齐
- 流式接收 MCP progress 通知（`InvokeWithProgress` 边调边推）
- 输出变量写入规则：progress 数组始终自动维护，result/error 在有 handler 时交给脚本

## 输出变量写入规则（核心决策）
| 场景 | progress 数组 | result | error |
|------|--------------|--------|-------|
| 无 handler | 自动写 | 自动写 | 自动写（仅 IsError） |
| 有 handler | 自动写 | 脚本写 | 脚本写 |

- `progress` 数组仅在 CallTool 返回后写一次（定稿），逐条 progress 到达时只「收集 + 有 handler 才同步执行脚本」
- progress 写一次时机：CallTool 返回后、收尾 handler 之前（节点先定稿 progress，脚本只读不抢）
- 逐条 progress 的 handler eventData：`progress`（本条结构化对象）+ `progress_raw`（本条完整原始 JSON 字符串）
- 收尾 handler 的 eventData：`result`、`is_error`(bool)、全量 `progress` 数组、`done`(true)


## 技术栈
- 后端：Go + Gin，`github.com/modelcontextprotocol/go-sdk v1.7.0-pre.1`，`flame/tools/xsql`
- 前端：Vue 3 + Element Plus（`nexus_desk`），组件内 `fetch` 调用 admin API
- 数据库：复用 `mcp_connections` 表（连接配置）+ `mcp_connection_tools`（工具列表）

## 实现方案

### 策略
后端新增 `executors/mcp.go`，复用 `http.go` 的 `parseHandler`/`executeHandlerScript`/`mergeInputs`/`resolveInputVars`/`RenderTemplate`/`BuildScriptTemplateContext` 等包级函数。MCP 客户端层新增带进度回调的流式入口 `InvokeWithProgress`。前端新增 `McpNodeProps.vue` 并注册到 `PropsPanel.vue`/`NodePanel.vue`。

### 关键技术决策

#### 1. 新增流式调用入口 `InvokeWithProgress`（不改现有函数）
现有 `NewClientSession`/`newStdioSession` 均 `mcp.NewClient(ClientImpl, nil)` 且被 `discover.go` 等多处复用，不能改签名。故新增独立建连函数：
- 建连时注入 `&mcp.ClientOptions{ ProgressNotificationHandler: func(ctx, req){ onProgress(req.Params) } }`
- `params.SetProgressToken(uuid.NewString())` 设置 progress token
- **v1 统一走一次性建连**（streamable_http 与 stdio 都临时建连，不复用 stdio 进程池）。理由：stdio 池化 session 的 progress handler 是建连时定的，流式 progress 需要按「当前调用」路由到正确的 onProgress，路由复杂度高；一次性建连语义清晰、无并发串扰。stdio 池化 + 流式 progress 作为后续独立优化。

#### 2. 并发与加锁（onProgress 与主协程并发）
`ProgressNotificationHandler` 在 SDK 后台读协程触发，与阻塞在 `CallTool` 的主协程并发执行：
- `progressCollector` 用 `sync.Mutex` 保护 `append`
- 有 handler 时逐条同步执行脚本：`nr.OutputVars` 由 `NodeResult.mu` 保护，写变量安全；collector append 独立加锁
- CallTool 返回后主协程写 progress 数组 + 收尾 handler，此时 onProgress 已全部结束，无竞争

#### 3. result 值形态
`flattenResult` 返回 `ToolResult{Content string, Structured any, IsError bool}`。固定变量 `result` 存 `Content`（string）；`Structured` 暂不单独落变量（后续可议）。

### 性能与可靠性
- 一次性建连 + 流式回调，无额外常驻资源；progress 通知按到达顺序逐条处理
- 加锁粒度为 collector append 的最小临界区，避免长时间持锁
- 复用 `xsql.JSONB`/现有仓储/脚本执行，不引入新类型

## 架构设计

```mermaid
flowchart TD
    A[前端 McpNodeProps.vue] -->|保存节点配置| B[Pipe 工作流]
    B --> C[McpExecutor.Execute]
    C --> D[resolveInputVars + 模板渲染]
    C --> E[mcpconnections.GetByID]
    C --> F[parseHandler]
    C --> G[InvokeWithProgress]
    G --> H[一次性建连 + ProgressNotificationHandler]
    H --> I[onProgress 流式回调]
    I --> J[progressCollector 加锁 append]
    I -->|有handler| K[逐条同步执行 handler 脚本]
    G --> L[CallTool 返回 ToolResult]
    L --> M[写 progress 数组定稿]
    M --> N{有 handler?}
    N -->|是| O[收尾 handler 脚本写 result/error]
    N -->|否| P[自动写 result 或 error]
```

## 目录结构
```
d:/proj/flame/
├── nexus/service/mcp/client/invoke.go        # [MODIFY] 新增 InvokeWithProgress 流式入口 + 带 handler 的一次性建连
├── nexus/service/pipe/executors/mcp.go       # [NEW] MCP 节点 executor
├── nexus/service/pipe/flow/nodeResult.go     # [MODIFY] NodeType 加 NodeTypeMCP = "mcp"
└── nexus_desk/src/components/pipeinstances/pipe/
    ├── McpNodeProps.vue                       # [NEW] MCP 节点配置面板
    ├── PropsPanel.vue                         # [MODIFY] 注册 mcp 分支 + import + 图标
    └── NodePanel.vue                          # [MODIFY] 加 MCP 节点卡片
```

## 关键代码结构

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


## Agent Extensions
### SubAgent
- **code-explorer**
  - Purpose: 实现前二次核对前端 MCP 连接/工具的 API 路径与 `admin.js` 配置键，以及确认 `HttpNodeProps.vue` 中可复用的 handler/outputs 编辑结构，确保 McpNodeProps.vue 与现有面板组件交互一致
  - Expected outcome: 产出前端 API 配置键与可复用组件结构清单，避免 McpNodeProps.vue 与既有交互模式偏离
