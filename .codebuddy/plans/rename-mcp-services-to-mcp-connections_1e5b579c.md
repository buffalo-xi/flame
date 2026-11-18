---
name: rename-mcp-services-to-mcp-connections
overview: 将 mcp_services 表相关的全部代码（后端 Go + 前端 Vue/JS）统一改名为 mcp_connections，保持命名一致。遵循规则：把单词 Service/Services 替换为 Connection/Connections，保留 Mcp 前缀（MCP 协议前缀不变）；包名/目录 mcpservices → mcpconnections；API 路由 slug /mcpservices → /mcpconnections。数据库表已由用户手动改名为 mcp_connections，本计划只改代码中的 SQL 字符串与标识符。
todos:
  - id: rename-model-repo
    content: 改名 Model/Repository/Factory：models.go 的 McpService→McpConnection，repository.go 接口与 ListMcpConnectionsRequest，factory.go 返回类型
    status: completed
  - id: rename-pkg-dirs
    content: git mv 两个 mcpservices 目录为 mcpconnections，更新 package 声明与 admin.go 的 import 路径
    status: completed
    dependencies:
      - rename-model-repo
  - id: rename-backend-impl
    content: postgres.go 改表名(6处)与类型引用；handlers.go 改函数名/req/错误信息；admin.go 改路由组、slug 与调用
    status: completed
    dependencies:
      - rename-pkg-dirs
  - id: verify-backend-build
    content: 在 nexus 目录执行 go build ./... 验证后端编译通过
    status: completed
    dependencies:
      - rename-backend-impl
  - id: rename-frontend-config
    content: admin.js 将 mcpServices key 与 URL 改为 mcpConnections / /admin/mcpconnections/
    status: completed
  - id: rename-frontend-settings
    content: Settings.vue 重命名标识符/函数/API调用/UI中文标签为 Connection，保留 'mcp' 状态键与状态辅助函数
    status: completed
    dependencies:
      - verify-backend-build
  - id: verify-frontend-build
    content: 构建或 lint nexus_desk 确认无未定义变量与引用一致
    status: completed
    dependencies:
      - rename-frontend-settings
---

## 用户需求
用户已将数据库表 `mcp_services` 重命名为 `mcp_connections`，要求把"刚才实现"的 MCP 服务功能相关代码全部同步改名，范围涵盖后端（Go）和前端（Vue/JS），先做计划评审，暂不实施。

## 核心改动
- 数据库表名 `mcp_services` → `mcp_connections`（仅改 Go 源码中的 SQL 字符串；建表/迁移由用户已执行）
- 后端 Model 结构体 `McpService` → `McpConnection`
- 后端 Repository 包名/目录 `mcpservices` → `mcpconnections`，接口 `McpServiceRepository` → `McpConnectionRepository`，请求结构体 `ListMcpServicesRequest` → `ListMcpConnectionsRequest`
- 后端 Handler 函数 `GetMcpService/CreateMcpService/UpdateMcpService/DeleteMcpService/ListMcpServices` → 对应 `Connection(s)` 命名
- 后端 API 路由 slug `/mcpservices` → `/mcpconnections`
- 前端 `admin.js` 配置 key `mcpServices` → `mcpConnections` 及 URL `/admin/mcpservices/` → `/admin/mcpconnections/`
- 前端 `Settings.vue` 中所有相关标识符、函数名、API 调用、UI 中文标签同步改名

## 改名规则
- 单词 `Service` → `Connection`，`Services` → `Connections`（保留 `Mcp` 前缀）
- 包名/目录 `mcpservices` → `mcpconnections`（全小写无分隔）
- API slug `mcpservices` → `mcpconnections`
- 内部状态键 `activeTab === 'mcp'`、以及仅含 `Mcp` 前缀无 `Service` 单词的函数（`getMcpStatusType`/`getMcpStatusText`）、`hasAuth` 保持不变，避免无谓改动


## 技术选型
沿用现有栈：后端 Go（Gin + PostgreSQL，模块 `flame/nexus`），前端 Vue3 + Element Plus（`nexus_desk`）。纯重命名重构，不新增功能、不改表结构语义。

## 实现方案

### 整体策略
按依赖层级自底向上改名：Model → Repository 接口/工厂 → 包目录重命名 + 实现层（postgres/handlers/admin）→ 前端配置 → 前端组件。每一层改动后确保类型/包名/路由一致，最后用 `go build` 与前端构建验证。

### 关键决策
1. **目录重命名用 `git mv`**：保持 git 历史；移动后必须同步更新每个文件顶部的 `package mcpservices` → `package mcpconnections`，以及所有 `import .../mcpservices` 路径（含 `admin.go` 与 `handlers.go` 内部 import）。
2. **SQL 表名仅改字符串**：`postgres.go` 中所有 `mcp_services` 出现处（GetByID 的 SELECT、Create 的 INSERT、Update 的 UPDATE、Delete 的 DELETE、List 的 COUNT 与 SELECT 共 6 处）改为 `mcp_connections`；与数据库迁移后的真实表名一致。
3. **Slug 同步改名**：`admin.go` 路由组路径 `/mcpservices` → `/mcpconnections`，前端 `admin.js` 的 URL 同步；网关 `nexus_gw` 按 `/admin/*` 前缀代理，无需改动。已确认除 `Settings.vue` 与 `admin.js` 外无其它前端调用方，改 slug 安全。
4. **保留无关标识符**：状态辅助函数 `getMcpStatusType`/`getMcpStatusText`、`hasAuth`、状态键 `'mcp'` 不含 `Service` 单词，按规则保留不改，降低回归风险。
5. **错误信息同步**：Handler 内 `"MCP service not found"` / `"MCP service deleted successfully"` 改为 `"MCP connection ..."`，与命名一致。

### 后端改动清单
| 文件 | 改动 |
|------|------|
| `nexus/shared/models/models.go` | 结构体 `McpService` → `McpConnection`（行 230-241），注释同步 |
| `nexus/shared/repository/mcpservices/repository.go` | `ListMcpServicesRequest` → `ListMcpConnectionsRequest`；`McpServiceRepository` → `McpConnectionRepository`；方法签名 `models.McpService` → `models.McpConnection` |
| `nexus/shared/repository/mcpservices/factory.go` | `NewRepository()` 返回类型 `McpServiceRepository` → `McpConnectionRepository` |
| 目录 `nexus/shared/repository/mcpservices` → `nexus/shared/repository/mcpconnections` | `git mv`，`package mcpservices` → `package mcpconnections` |
| 目录 `nexus/admin/mcpservices` → `nexus/admin/mcpconnections` | `git mv`，`package mcpservices` → `package mcpconnections` |
| `nexus/shared/repository/mcpconnections/postgres.go` | `mcp_services`→`mcp_connections`（6 处）；`models.McpService`→`models.McpConnection`；`ListMcpServicesRequest`→`ListMcpConnectionsRequest` |
| `nexus/admin/mcpconnections/handlers.go` | `Get/Create/Update/Delete/ListMcpService(s)` → `...McpConnection(s)`；内部 `repository.ListMcpServicesRequest` → `ListMcpConnectionsRequest`；错误信息 "MCP service"→"MCP connection" |
| `nexus/admin/admin.go` | import 路径 `.../admin/mcpservices` → `.../admin/mcpconnections`；路由组 `mcpServicesGroup`→`mcpConnectionsGroup`；路径 `/mcpservices`→`/mcpconnections`；调用 `mcpservices.Xxx`→`mcpconnections.Xxx` |

### 前端改动清单
| 文件 | 改动 |
|------|------|
| `nexus_desk/src/config/admin.js` | key `mcpServices` → `mcpConnections`；URL 全部 `/admin/mcpservices/` → `/admin/mcpconnections/` |
| `nexus_desk/src/components/Settings.vue` | 标识符 `mcpServicesData`→`mcpConnectionsData`、`mcpServiceLoading`→`mcpConnectionLoading`、`mcpServiceSettings`→`mcpConnectionSettings`、`mcpServiceDialogVisible`→`mcpConnectionDialogVisible`、`mcpServiceDialogTitle`→`mcpConnectionDialogTitle`、`currentMcpService`→`currentMcpConnection`；函数 `openAddMcpServiceDialog`/`openEditMcpServiceDialog`/`deleteMcpService` → `...McpConnection...`；`apiConfig.admin.mcpServices` → `apiConfig.admin.mcpConnections`；UI 文本 "MCP 服务"→"MCP 连接"、"添加服务"→"添加连接"、placeholder "输入 MCP 服务名称"→"输入 MCP 连接名称"、"MCP 服务 URL"→"MCP 连接 URL"、"输入服务描述"→"输入连接描述"、"暂无 MCP 服务"→"暂无 MCP 连接"、dialog 标题 "添加MCP服务"→"添加MCP连接" / "编辑MCP服务"→"编辑MCP连接"；状态键 `'mcp'` 与 `getMcpStatusType/getMcpStatusText/hasAuth` 保持不变 |

## 实现注意
- 目录 `git mv` 后逐文件改 `package` 与 `import`，避免编译报 "imported and not used" 或包名不匹配。
- 后端改名后必须 `cd nexus && go build ./...` 全量通过（含 `admin` 与 `shared/repository/mcpconnections`）。
- 前端改名后 `mcpConnectionsData` 等需在 `onMounted` 调用的 `fetchMcpConnections` 中一致赋值（原 `fetchMcpServices` 一并改名）。
- 不触碰 `svc_endpoints` 的 enabled 列计划（当前 building 状态），两者互不耦合，改名完成后再继续该项。

## 架构设计
无新增架构，仅跨层重命名以保持语义一致。调用链：前端 `Settings.vue` → `admin.js` (slug `/admin/mcpconnections`) → `admin.go` 路由组 → `mcpconnections` handlers → `mcpconnections` repository(postgres) → 表 `mcp_connections`。

