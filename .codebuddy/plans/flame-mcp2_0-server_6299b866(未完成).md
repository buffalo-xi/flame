---
name: flame-mcp2.0-server
overview: 基于官方 Go SDK github.com/modelcontextprotocol/go-sdk@v1.7.0-pre.1（原生支持 MCP 2026-07-28/v2），为 flame 实现 MCP Server 端能力：从各租户 schema 的 svc_endpoints 表（protocol='mcp' AND enabled=true）动态加载工具并以 Streamable HTTP 单端点 POST /service/mcp 对外暴露；租户通过配置文件约定的 HTTP 头标识（值即 Postgres schema 名），每租户 schema 内 mcp_server_configs 按 auth_type 分行（apikey/oauth，最多两行）配置认证；首期仅 Server，Client 后续。
todos:
  - id: explore-deps
    content: 用 [subagent:code-explorer] 探查 agent/pipe 执行入口、config 与租户 schema 路由
    status: pending
  - id: add-sdk
    content: 在 nexus/go.mod 引入 go-sdk v1.7.0-pre.1 并 go mod tidy
    status: pending
    dependencies:
      - explore-deps
  - id: config-tenant-header
    content: 在 shared/config 加 mcp.server.tenant_header 并写入 config.yaml
    status: pending
    dependencies:
      - explore-deps
  - id: input-schema-column
    content: SvcEndpoint 模型与 svcendpoints 仓库补齐 input_schema 列及 Enabled 过滤
    status: pending
    dependencies:
      - explore-deps
  - id: server-config-table
    content: 新增 mcp_server_configs 建表 SQL（每租户 schema，auth_type 唯一最多两行）
    status: pending
    dependencies:
      - explore-deps
  - id: tenant-auth
    content: 实现 tenant.go 与 auth.go（schema 路由 + apikey/oauth 双认证）
    status: pending
    dependencies:
      - config-tenant-header
      - server-config-table
  - id: server-endpoint
    content: 实现 service/mcp/server/server.go 注册工具并注册 POST /service/mcp 端点
    status: pending
    dependencies:
      - input-schema-column
      - tenant-auth
---

## 用户需求
为 flame 后端（flame/nexus）新增 MCP 2.0 Server 能力，将各租户在 `svc_endpoints` 表中配置的 MCP 工具通过 Streamable HTTP 单端点对外暴露，供任意 MCP 2.0 客户端调用。首期仅实现 Server 端，Client 接入外部 MCP Server 后续再做。

## 产品概述
在现有 flame 后端之上新增一套 MCP 2.0 Server 层：外部客户端通过 `POST /service/mcp` 端点（无状态、请求自包含）调用工具；服务端通过全局配置文件约定的 HTTP 头识别租户，将标识值作为 Postgres schema 名路由到对应租户，读取该租户 `mcp_server_configs`（按 auth_type 分行，最多两行）中的认证配置完成鉴权，再加载该租户 `svc_endpoints` 中 `protocol='mcp' AND enabled=true` 的行注册为 MCP tools。

## 核心特性
- Streamable HTTP 单端点 `POST /service/mcp`，URL 不加版本号，版本经协议层 `_meta` 与请求头协商（MCP 2026-07-28）。
- 租户识别：全局配置文件约定租户头名（如 `X-Flame-Tenant`），请求头取值直接作为 Postgres schema 名路由。
- 每租户 `mcp_server_configs` 表按 `auth_type` 分行（最多两行：apikey、oauth），配置 `enabled`、`auth_config`（JSONB）；调用方任选一种认证方式，禁用方式返回 401。
- 工具动态来源于 `svc_endpoints`：`name`→tool 名、`description`→描述、`input_schema`(JSONB)→输入 JSON Schema、`target_type`+`target_id`→路由到 `service/agent` 或 `service/pipe` 执行。
- 认证：apikey（头比对）与 oauth（JWT + JWKS 验签 + audience/scope）均首期支持；全局租户头与认证头为两个独立头。


## 技术栈
- 语言/框架：Go 1.25 + Gin（沿用 flame/nexus 现有栈）
- MCP SDK：`github.com/modelcontextprotocol/go-sdk`（核心包 `mcp`），版本 `v1.7.0-pre.1`（已原生支持 MCP 2026-07-28，无需自研 2.0 无状态层）
- 数据库：沿用 Postgres，租户隔离采用 schema 隔离（标识值即 schema 名）
- 复用：flame/tools 下的 xhttp/xlog/xsql、现有 `shared/config`、`shared/db` 租户 schema 路由、`shared/models`、`shared/repository/svcendpoints`

## 实现方案
采用官方 SDK + 现有租户 schema 隔离策略。MCP 2.0 无状态由 `mcp.NewStreamableHTTPHandler(func(*http.Request)*mcp.Server, opts)` 的 `getServer` 回调承载：回调从配置约定的头取租户标识 → 作为 schema 名设置 `search_path` → 查该 schema 的 `mcp_server_configs` 做认证 → 查 `svc_endpoints` 构建工具集 → 返回 `*mcp.Server` 实例。工具注册用 `mcp.AddTool(server, &mcp.Tool{...}, handler)`，handler 按 `target_type` 路由调用 `service/agent` 或 `service/pipe`，结果包成 `mcp.NewTextResult`。

### 关键决策与权衡
1. **SDK 选官方 go-sdk 而非 mark3labs/mcp-go**：已核实 `v1.7.0-pre.1` 原生支持 2026-07-28，省去自研无状态层，降低风险与维护成本。
2. **Server 放 `service/mcp/server/` 而非 `shared/mcp/`**：Server 需 import `service/agent`、`service/pipe`，而 `shared` 是底层被依赖方，放 `shared` 会倒置依赖/潜在循环。与现有 agent/pipe 同级最干净。
3. **租户标识 = schema 名**：避免跨租户映射表，直接 `SET search_path` 复用 flame 现有 schema 隔离机制。
4. **apikey + oauth 双支持、按 auth_type 分行**：`mcp_server_configs` 最多两行（apikey/oauth），`auth_type` 唯一；调用方带 `X-API-Key` 类头走 apikey 行校验，带 `Authorization: Bearer` 走 oauth 行校验，禁用方式返回 401。oauth 采用 JWKS 远程验签（不内嵌密钥），首期仅校验签名+audience+scope。
5. **input_schema 列而非复用 config**：语义清晰，与协议层 `arguments` 无冲突。

## 实现要点（防回归）
- `nexus/go.mod` 加 `github.com/modelcontextprotocol/go-sdk v1.7.0-pre.1`；`go mod tidy` 更新 go.sum；保留已有 `replace => ../tools`。
- `SvcEndpoint` 加 `InputSchema xsql.JSONB`；`svcendpoints/postgres.go` columns/scan/Create/Update 同步补 `input_schema`；`ListSvcEndpointsRequest` 加 `Enabled *bool`。
- `mcp_server_configs` 建表 SQL（每租户 schema，按 auth_type 分行最多两行）：`id, auth_type(text unique), enabled(bool), auth_config(jsonb), created_at, updated_at`。
- 复用现有 `shared/db` 的租户 schema 路由/`authInfo` 机制，MCP 端点中间件里设置 `search_path` 并做认证。
- 不改动现有 `McpConnection` CRUD、admin 路由、models 其他字段；仅扩展。
- 认证失败/租户缺失返回标准 JSON-RPC 错误，日志不记录凭证明文。

## 架构设计
```mermaid
flowchart LR
  A[外部 MCP 2.0 Client] -->|POST /service/mcp + X-Flame-Tenant 头| B[StreamableHTTPHandler]
  B -->|getServer 回调| C{取租户头→schema 名}
  C -->|SET search_path| D[(租户 schema)]
  D --> E[mcp_server_configs 按 auth_type 分行]
  E -->|apikey/oauth| F[认证中间件]
  F -->|通过| G[加载 svc_endpoints protocol=mcp enabled=true]
  G --> H[mcp.Server 实例]
  H -->|tools/call| I[handler 路由]
  I -->|target_type=agent| J[service/agent]
  I -->|target_type=pipe| K[service/pipe]
```

## 目录结构
```
flame/nexus/
├── go.mod                                    # [MODIFY] 增加 go-sdk 依赖，go mod tidy
├── config.yaml                               # [MODIFY] 增加 mcp.server.tenant_header 配置段
├── cmd/
│   └── router.go                             # [MODIFY] /service 组注册 POST /mcp，委托 StreamableHTTPHandler
├── shared/
│   ├── config/                               # [MODIFY] 配置结构体加 Mcp.Server.TenantHeader
│   └── models/models.go                      # [MODIFY] SvcEndpoint 加 InputSchema xsql.JSONB
├── shared/repository/svcendpoints/
│   ├── postgres.go                           # [MODIFY] columns/scan/Create/Update 补 input_schema
│   └── repository.go                         # [MODIFY] ListSvcEndpointsRequest 加 Enabled *bool
├── service/mcp/server/
│   ├── server.go                             # [NEW] 构建 mcp.Server，注册 svc_endpoints 工具，getServer 回调，handler 路由
│   ├── auth.go               # [NEW] apikey/oauth 认证（读 mcp_server_configs 按 auth_type 分行）
│   └── tenant.go             # [NEW] 从配置头取租户标识→schema 名→search_path 路由
└── db/
    └── migrations/                           # [NEW] mcp_server_configs 建表 SQL（每租户 schema，auth_type 唯一）
```

## 待执行前需确认的代码事实（由 code-explorer 探查）
- `service/agent` 的 `core.Agent.Run` 签名与 `service/pipe` 的 flow 执行入口函数签名（handler 调用所需）。
- `shared/config` 加载方式与结构体（加 tenant_header 段）。
- 现有租户 schema 路由/`authInfo` 中间件实现（tenant.go 复用参考）。


## Agent Extensions
### SubAgent
- **code-explorer**
  - Purpose: 探查 service/agent 的 core.Agent.Run 签名、service/pipe 的 flow 执行入口签名、shared/config 加载方式、现有租户 schema 路由/authInfo 中间件，确认 server.go/auth.go/tenant.go 所需的最小契约与可复用代码。
  - Expected outcome: 输出 agent/pipe 可被暴露为 MCP tool 的调用参数契约、config 结构体扩展点、租户 schema 路由现有实现位置，避免方案建立在错误假设上。
