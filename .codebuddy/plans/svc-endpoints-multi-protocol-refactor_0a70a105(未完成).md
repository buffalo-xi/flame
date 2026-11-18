---
name: svc-endpoints-multi-protocol-refactor
overview: 重构 svc_endpoints 表模型：移除 run_mode/arguments/outputs 字段，新增 target_type/target_id/protocol 三字段关联及 config(jsonb) 配置字段，使一个 agent/工作流可按多种协议分别发布多行，并在前端发布时增加协议选择。
todos:
  - id: update-model
    content: 更新 SvcEndpoint 模型：移除 RunMode/Arguments/Outputs，新增 TargetID/TargetType/Protocol/Config(xsql.JSONB) 字段
    status: pending
  - id: update-repository-interface
    content: 扩展 Repository 接口：ListSvcEndpointsRequest.RunMode→TargetType；新增 Unpublish(targetType,targetID) 方法
    status: pending
    dependencies:
      - update-model
  - id: rewrite-postgres-repo
    content: 使用 [subagent:code-explorer] 搜索所有 run_mode/arguments/outputs 引用后重写 postgres.go：SQL 移除旧三列+新增四列；Create 改为 UUID+target 验证+协议重复检查+config 验证+双表联动；原 Delete 替换为 Unpublish
    status: pending
    dependencies:
      - update-repository-interface
  - id: update-admin-handlers
    content: 改造 admin handlers 和 admin.go：Create/Update 请求体移除旧字段新增 target_id/target_type/protocol/config；新增 Unpublish handler；路由注册 POST /unpublish
    status: pending
    dependencies:
      - rewrite-postgres-repo
  - id: update-service-handlers
    content: 改造 service/handlers.go：ExecuteAgent 用 TargetType 替代 RunMode 分支；RunStandalone 用 target_id 直查 agent_instances；新增 workflow 分支调用 RunWorkflow
    status: pending
    dependencies:
      - update-model
  - id: update-agent-publish-ui
    content: 修改 AgentInstances.vue：上架抽屉移除 arguments/outputs，新增协议选择+config 输入区；提交 body 改为 target_id+target_type+protocol+config；下架改用 POST /unpublish
    status: pending
    dependencies:
      - update-admin-handlers
  - id: update-workflow-publish-ui
    content: 改造 WFInstances.vue：新增 publish 抽屉(协议选择+config+name+description)替换 togglePublish；新增 unpublish 逻辑；Save body 移除 is_published
    status: pending
    dependencies:
      - update-admin-handlers
  - id: update-admin-js
    content: 修改 admin.js：svcEndpoints 配置新增 unpublish 路径
    status: pending
    dependencies:
      - update-admin-handlers
---

## 产品概述
对 `svc_endpoints` 服务端点表进行协议化改造，支持同一智能体或工作流以多种协议（MCP、HTTP、LLM）发布，每种协议独立一行，每种协议通过 `config` JSONB 字段存储协议特定配置。同时精简表结构：新增 `target_id`/`target_type`/`protocol`/`config` 四列，移除 `run_mode`/`arguments`/`outputs` 三列。

## 核心功能
- **数据库模型精简**：新增 `target_id`、`target_type`（agent/workflow）、`protocol`（mcp/http/llm）、`config`（JSONB）四列，`id` 改为独立 UUID；移除 `run_mode`、`arguments`、`outputs` 三列
- **协议选择与配置发布**：Agent 和 Workflow 发布时，前端显示协议选择器（mcp/http/llm），用户选择协议后填写对应的 config（JSON 格式配置）。发布表单不再包含 arguments/outputs 输入区
- **唯一性约束**：同一 target（agent 或 workflow）的同一种协议只能存在一条记录
- **发布与下架联动**：发布时插入 `svc_endpoints` 行并更新 `agent_instances`/`wf_instances.is_published=true`；下架时删除该 target 所有协议行并恢复 `is_published=false`

## 技术选型
- 后端：Go + Gin + PostgreSQL（沿用现有技术栈）
- 前端：Vue 3 + Element Plus（沿用现有技术栈）
- `config` 字段类型：`xsql.JSONB`（项目已有先例，WFInstance.Diagram/Arguments 等均使用该类型）
- 数据库 DDL 由用户自行完成

## 实现方案

### 核心策略
彻底移除 `run_mode`、`arguments`、`outputs` 三个字段，用 `target_type` 区分 agent/workflow，用 `config`（JSONB）存储协议特定配置。`SvcEndpoint` 模型最终保留：`ID, Name, Description, CreatedAt, TargetID, TargetType, Protocol, Config`。

### 关键技术决策

1. **三字段移除 + 四字段新增**：`SvcEndpoint` 删除 `RunMode`/`Arguments`/`Outputs`，新增 `TargetID`/`TargetType`/`Protocol`/`Config`。所有 SQL 同步调整。`Config` 使用 `xsql.JSONB` 类型与 `WFInstance.Diagram` 一致
2. **服务执行路径简化**：`ExecuteAgent` 用 `target_type` 分支（替代 `run_mode`）；`RunStandalone` 和 `RunWorkflow` 通过 `target_id` 直查对应实例表
3. **下架语义为全部下线**：一次性删除该 target 所有协议行。新增 `Unpublish(targetType, targetID)` 接口
4. **重复发布检查**：Create 时检查 `target_type + target_id + protocol` 组合是否已存在
5. **Workflow 发布改造**：从简单切换 `is_published` 改为与 Agent 一致的抽屉 + 协议选择 + config 配置 + `svc_endpoints` 写入
6. **Config 在 Create 中验证为有效 JSON**：复用现有 JSON 验证模式（与原来 arguments/outputs 验证类似），但不强制要求具体格式，只验证合法 JSON

### 执行注意事项
- **blast radius**：`models.go`、`postgres.go`（全部 SQL）、`repository.go`（接口+过滤）、`handlers.go`（请求体+逻辑）、`admin.go`（路由）、`service/handlers.go`（分支+查询）、`AgentInstances.vue`（表单+提交+下架）、`WFInstances.vue`（发布/下架）、`admin.js`（API 路径）
- **事务一致性**：Create 同一事务 INSERT + UPDATE is_published；Unpublish 同一事务 DELETE 全部协议行 + UPDATE is_published=false
- **xsql.JSONB 的 Scan/Value**：需确认 `xsql.JSONB` 实现了 `sql.Scanner` 和 `driver.Valuer` 接口（与 `xsql.JSON` 类似），确保 SQL Scan 和参数绑定正常工作

## 架构设计

### 数据流（发布）
```
前端 [协议选择 + config 配置] → POST /admin/svcendpoints/create
  body: {target_id, target_type, protocol, config, name, description}
  → repository.Create():
    1. 验证 target_type∈{agent,workflow}, protocol∈{mcp,http,llm}
    2. 验证 config 为合法 JSON（若不为空）
    3. 查 agent_instances/wf_instances 确认 target_id 存在
    4. 查 svc_endpoints 确认无重复（target_type+target_id+protocol）
    5. 生成 UUID 作为 id
    6. 事务：INSERT svc_endpoints(id,name,description,target_id,target_type,protocol,config)
            + UPDATE agent_instances/wf_instances SET is_published=true, published_at=NOW()
```

### 数据流（下架）
```
前端 [下架] → POST /admin/svcendpoints/unpublish {target_type, target_id}
  → repository.Unpublish():
    1. 事务：DELETE FROM svc_endpoints WHERE target_type=$1 AND target_id=$2
    2. UPDATE agent_instances/wf_instances SET is_published=false, published_at=NULL
```

### 数据流（服务执行）
```
POST /service/agent/v1/execute {name, arguments, ...}
  → ExecuteAgent():
    1. 查 svc_endpoints WHERE name=$name LIMIT 1 获取 target_type + target_id
    2. target_type="agent" → RunStandalone (target_id 直查 agent_instances)
    3. target_type="workflow" → RunWorkflow (target_id 直查 wf_instances)
```

## 目录结构

```
nexus/
├── shared/
│   ├── models/
│   │   └── models.go                  # [MODIFY] SvcEndpoint: 移除 RunMode/Arguments/Outputs，新增 TargetID/TargetType/Protocol/Config(xsql.JSONB)
│   └── repository/
│       └── svcendpoints/
│           ├── repository.go          # [MODIFY] RunMode→TargetType；新增 Unpublish 接口方法
│           └── postgres.go            # [MODIFY] 全部 SQL 移除 run_mode/arguments/outputs，新增 target_id/target_type/protocol/config；Create 生成 UUID+验证+重复检查+双表联动；原 Delete 替换为 Unpublish
├── admin/
│   ├── admin.go                       # [MODIFY] 路由新增 POST /unpublish
│   └── svcendpoints/
│       └── handlers.go                # [MODIFY] Create/Update 请求体移除 run_mode/arguments/outputs，新增 target_id/target_type/protocol/config；新增 Unpublish handler
└── service/
    └── handlers.go                    # [MODIFY] ExecuteAgent 用 TargetType 替代 RunMode 分支；RunStandalone 用 target_id 直查 agent_instances；新增 workflow target_type→RunWorkflow 分支

nexus_desk/
└── src/
    ├── components/
    │   ├── AgentInstances.vue         # [MODIFY] 上架抽屉移除 arguments/outputs 输入框，新增协议选择+config 输入区；提交 body 改为 target_id+target_type+protocol+config+name+description；下架改用 POST /unpublish
    │   └── WFInstances.vue            # [MODIFY] 新增 publish 抽屉(协议选择+config+name+description)替换 togglePublish；新增 unpublish 调用；Save body 移除 is_published
    └── config/
        └── admin.js                    # [MODIFY] svcEndpoints 新增 unpublish API 路径
```

## 关键代码结构

```go
// SvcEndpoint 精简后的结构体
type SvcEndpoint struct {
    ID          string     `json:"id"`
    Name        string     `json:"name"`
    Description string     `json:"description"`
    CreatedAt   xsql.Time  `json:"created_at"`
    TargetID    string     `json:"target_id"`
    TargetType  string     `json:"target_type"` // "agent" | "workflow"
    Protocol    string     `json:"protocol"`    // "mcp" | "http" | "llm"
    Config      xsql.JSONB `json:"config"`      // JSONB，协议特定配置
}

// List 过滤条件变更
type ListSvcEndpointsRequest struct {
    Page       int      `json:"page"`
    Limit      int      `json:"limit"`
    Name       string   `json:"name"`
    TargetType string   `json:"target_type"` // 替代原 RunMode
    Protocol   string   `json:"protocol"`
    SortBy     []string `json:"sort_by"`
    SortOrder  []string `json:"sort_order"`
}

// Repository 接口新增 Unpublish
type SvcEndpointRepository interface {
    GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error)
    Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error)
    Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error
    Delete(authInfo *auth.AuthInfo, id string) error
    Unpublish(authInfo *auth.AuthInfo, targetType, targetID string) error
    List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error)
}

// Create handler 请求体（移除 arguments/outputs，新增四关联字段）
var req struct {
    TargetID    string     `json:"target_id"`
    TargetType  string     `json:"target_type"`
    Protocol    string     `json:"protocol"`
    Config      xsql.JSONB `json:"config"`
    Name        string     `json:"name"`
    Description string     `json:"description"`
}
```

## Agent Extensions

### SubAgent
- **code-explorer**
  - Purpose: 在重写 postgres.go 和修改 service/handlers.go 时，深度搜索整个 nexus Go 代码中所有引用 `run_mode`、`RunMode`、`Arguments`、`Outputs` 的位置（含 SQL 硬编码列名、结构体字段引用、分支判断语句），确保三字段完全移除且无遗漏
  - Expected outcome: 列出所有需变更位置的完整清单，确保编译通过
