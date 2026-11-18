---
name: svc-endpoints-multi-protocol-refactor
overview: 重构 svc_endpoints 表模型以支持 target_type/target_id/protocol 三字段关联，移除 run_mode 字段，使一个 agent/工作流可按多种协议（mcp/http/llm）分别发布多行，并在前端发布时增加协议选择。
todos:
  - id: update-model
    content: 更新 SvcEndpoint 模型：移除 RunMode 字段，新增 TargetID、TargetType、Protocol 字段
    status: pending
  - id: update-repository-interface
    content: 扩展 Repository 接口：ListSvcEndpointsRequest.RunMode 改为 TargetType；新增 Unpublish(targetType,targetID) 方法
    status: pending
    dependencies:
      - update-model
  - id: rewrite-postgres-repo
    content: 使用 [subagent:code-explorer] 搜索所有 run_mode 引用后重写 postgres.go：所有 SQL 移除 run_mode 列；Create 改为 UUID+target 验证+协议重复检查+双表联动；原 Delete 逻辑替换为 Unpublish
    status: pending
    dependencies:
      - update-repository-interface
  - id: update-admin-handlers
    content: 改造 admin handlers 和 admin.go 路由：Create/Update 请求体 run_mode→target_type+protocol；新增 Unpublish handler；路由注册加 POST /unpublish
    status: pending
    dependencies:
      - rewrite-postgres-repo
  - id: update-service-handlers
    content: 改造 service/handlers.go：ExecuteAgent 用 TargetType 替代 RunMode 分支；RunStandalone 用 target_id 直查 agent_instances；新增 workflow 分支调用 RunWorkflow
    status: pending
    dependencies:
      - update-model
  - id: update-agent-publish-ui
    content: 修改 AgentInstances.vue：上架抽屉新增协议选择 el-select；提交 body 移除 run_mode 改为 target_id+target_type+protocol；下架改用 POST /unpublish
    status: pending
    dependencies:
      - update-admin-handlers
  - id: update-workflow-publish-ui
    content: 改造 WFInstances.vue：新增 publish 抽屉(协议选择+表单)替换 togglePublish；新增 unpublish 逻辑；Save body 移除 is_published
    status: pending
    dependencies:
      - update-admin-handlers
---

## 产品概述
对 `svc_endpoints` 服务端点表进行协议化改造，使其支持同一智能体或工作流以多种协议（MCP、HTTP、LLM）发布，每种协议独立一行记录。同时移除原有的 `run_mode` 字段，用新增的 `target_type` 字段区分 agent 和 workflow。

## 核心功能
- **数据库模型升级**：`svc_endpoints` 新增 `target_id`、`target_type`（agent/workflow）、`protocol`（mcp/http/llm）三列，`id` 改为独立 UUID，移除 `run_mode` 字段
- **协议选择发布**：Agent 和 Workflow 发布时，前端显示协议选择器（mcp/http/llm），用户必须选择一种协议
- **唯一性约束**：同一 target（agent 或 workflow）的同一种协议只能存在一条记录，不可重复发布
- **发布与下架联动**：发布时在 `svc_endpoints` 中创建行并更新对应 `agent_instances`/`wf_instances` 的 `is_published` 状态；下架时删除该 target 的所有协议行并恢复 `is_published=false`
- **移除 run_mode**：`SvcEndpoint` 结构体移除 `RunMode` 字段，所有引用的地方改用 `TargetType`（"agent"/"workflow"）；`ListSvcEndpointsRequest` 的 `RunMode` 过滤改为 `TargetType`

## 技术选型
- 后端：Go + Gin + PostgreSQL（沿用现有技术栈）
- 前端：Vue 3 + Element Plus（沿用现有技术栈）
- 数据库迁移：不在此计划范围内（假设用户已自行完成 DDL）

## 实现方案

### 核心策略
彻底移除 `run_mode`，用 `target_type` 承接区分 agent/workflow 的语义。在 `SvcEndpoint` 结构体、SQL 查询、API 请求/响应体中全面替换，不再保留兼容逻辑。

### 关键技术决策

1. **`RunMode` 完全移除**：`SvcEndpoint.RunMode` 字段删除，所有 SQL SELECT 不再包含 `run_mode` 列，INSERT 不再写入 `run_mode`。服务执行方（`ExecuteAgent`、`RunStandalone`、`RunWorkflow`）全部通过 `target_type` 判断分支，不再依赖 `run_mode`
2. **下架语义为全部下线**：删除一个 target 的所有协议行（而非逐协议删除），前端仍为单一「下架」按钮。新增 `Unpublish(targetType, targetID)` 接口替代原来的 `Delete(id)`
3. **重复发布检查在 repository 层**：Create 时查询 `target_type + target_id + protocol` 是否已存在，存在则拒绝
4. **Workflow 发布流程改造**：从简单切换 `is_published` 布尔值改为与 Agent 一致的抽屉表单 + 协议选择 + `svc_endpoints` 写入；Workflow 的 Save 不再发送 `is_published` 字段
5. **RunMode 的 `ListSvcEndpointsRequest` 过滤改为 `TargetType`**：`RunMode string` 字段替换为 `TargetType string`，前端列表过滤同步调整
6. **Update handler 移除 run_mode 验证**：不再验证 `run_mode` 为 "standalone"/"pipeline"，改为验证 `target_type` 为 "agent"/"workflow"

### 执行注意事项
- **blast radius**：删除 `RunMode` 字段影响 `models.go`、`postgres.go`（7处 SQL + 3处逻辑）、`repository.go`（1处）、`handlers.go`（2处请求体 + 逻辑）、`service/handlers.go`（1处分支）、`AgentInstances.vue`（1处 body 字段）。需同时修改确保编译通过
- **事务一致性**：Create 在同一事务中插入 `svc_endpoints` + 更新 `is_published`；Unpublish 在同一事务中删除所有协议行 + 恢复 `is_published`
- **workflow 发布约束**：`wf_instances` 的 `is_published=true` 时禁止直接更新/删除的约束由 wfinstances repository 保持不变，但发布动作本身通过 `svc_endpoints` Create 完成，不通过 `wf_instances` 的 Update

## 架构设计

### 数据流（发布）
```
前端 [选协议] → POST /admin/svcendpoints/create {target_id, target_type, protocol, name, ...}
  → repository.Create():
    1. 验证 target_type∈{agent,workflow} + protocol∈{mcp,http,llm}
    2. 查 agent_instances/wf_instances 确认 target_id 存在
    3. 查 svc_endpoints 确认无重复（同 target_type + 同 target_id + 同 protocol）
    4. 事务内：INSERT svc_endpoints (id=UUID) + UPDATE agent_instances/wf_instances SET is_published=true
```

### 数据流（下架）
```
前端 [下架] → POST /admin/svcendpoints/unpublish {target_type, target_id}
  → repository.Unpublish():
    1. 事务内：DELETE svc_endpoints WHERE target_type+target_id
    2. UPDATE agent_instances/wf_instances SET is_published=false, published_at=NULL
```

### 数据流（服务执行）
```
POST /service/agent/v1/execute {name, ...}
  → ExecuteAgent():
    1. 查 svc_endpoints WHERE name=xxx（需匹配 target_type 和 protocol 由请求决定或遍历）
    2. 若 target_type="agent" → RunStandalone(c, schema, target_id, agentArguments, req)
    3. 若 target_type="workflow" → RunWorkflow(c, schema, target_id, arguments, req)（用 target_id 直接查 wf_instances，不再通过 name）
```

## 目录结构

```
nexus/
├── shared/
│   ├── models/
│   │   └── models.go                                    # [MODIFY] SvcEndpoint 移除 RunMode，新增 TargetID/TargetType/Protocol
│   └── repository/
│       └── svcendpoints/
│           ├── repository.go                            # [MODIFY] ListSvcEndpointsRequest.RunMode→TargetType；接口新增 Unpublish
│           └── postgres.go                              # [MODIFY] 所有 SQL 移除 run_mode 列；Create 重写（UUID/target 验证/重复检查/双表联动）；Delete 替换为 Unpublish
├── admin/
│   ├── admin.go                                         # [MODIFY] 路由注册：新增 POST /unpublish，DELETE /:id 替换语义
│   └── svcendpoints/
│       └── handlers.go                                  # [MODIFY] Create/Update 请求体 run_mode→target_type/protocol；新增 Unpublish handler；Update 验证 target_type 枚举
└── service/
    └── handlers.go                                      # [MODIFY] ExecuteAgent：RunMode→TargetType 分支；RunStandalone 用 target_id 直接 GetByID 查 agent/wf；RunWorkflow 通过 target_id 查 wf_instances

nexus_desk/
└── src/
    ├── components/
    │   ├── AgentInstances.vue                            # [MODIFY] 上架抽屉新增协议选择 el-select(mcp/http/llm)；提交 body 移除 run_mode，改用 target_id+target_type+protocol；下架改用 POST /unpublish
    │   └── WFInstances.vue                               # [MODIFY] 新增 publish 抽屉模板(协议选择+表单)替代 togglePublish；新增 unpublish 调用；Save body 移除 is_published
    └── config/
        └── admin.js                                      # [MODIFY] svcEndpoints 新增 unpublish 路径
```

## 关键代码结构

```go
// SvcEndpoint 改造后的结构体（移除 RunMode，新增三字段）
type SvcEndpoint struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Description string    `json:"description"`
    CreatedAt   xsql.Time `json:"created_at"`
    Arguments   xsql.JSON `json:"arguments"`
    Outputs     xsql.JSON `json:"outputs"`
    TargetID    string    `json:"target_id"`
    TargetType  string    `json:"target_type"` // "agent" | "workflow"
    Protocol    string    `json:"protocol"`    // "mcp" | "http" | "llm"
}

// Repository 接口：RunMode→TargetType，新增 Unpublish
type ListSvcEndpointsRequest struct {
    Page       int      `json:"page"`
    Limit      int      `json:"limit"`
    Name       string   `json:"name"`
    TargetType string   `json:"target_type"` // 替代原 RunMode
    Protocol   string   `json:"protocol"`
    SortBy     []string `json:"sort_by"`
    SortOrder  []string `json:"sort_order"`
}

type SvcEndpointRepository interface {
    GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error)
    Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error)
    Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error
    Delete(authInfo *auth.AuthInfo, id string) error // 保留单行删除
    Unpublish(authInfo *auth.AuthInfo, targetType, targetID string) error // 新增：批量删协议行+恢复is_published
    List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error)
}

// Create handler 请求体变更
var req struct {
    TargetID    string    `json:"target_id"`
    TargetType  string    `json:"target_type"`  // 替代原 run_mode
    Protocol    string    `json:"protocol"`
    Name        string    `json:"name"`
    Description string    `json:"description"`
    Arguments   xsql.JSON `json:"arguments"`
    Outputs     xsql.JSON `json:"outputs"`
}
```

## Agent Extensions

### SubAgent
- **code-explorer**
  - Purpose: 在重写 postgres.go 和修改 service/handlers.go 时，深度搜索所有引用 `run_mode`、`RunMode` 的位置，确保无遗漏
  - Expected outcome: 列出所有需要变更的代码位置（包括 SQL 字符串中硬编码的列名、结构体字段引用、分支判断语句），确保 `run_mode` 完全移除且编译通过
