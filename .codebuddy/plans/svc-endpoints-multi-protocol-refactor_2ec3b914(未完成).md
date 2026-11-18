---
name: svc-endpoints-multi-protocol-refactor
overview: 重构 svc_endpoints 表模型以支持 target_type/target_id/protocol 三字段关联，使一个 agent/工作流可按多种协议（mcp/http/llm）分别发布多行，并在前端发布时增加协议选择。
todos:
  - id: update-model
    content: 更新 SvcEndpoint 模型，新增 TargetID、TargetType、Protocol 字段
    status: pending
  - id: update-repository-interface
    content: 扩展 Repository 接口：新增 Unpublish 方法；ListSvcEndpointsRequest 新增 TargetType/Protocol 过滤字段
    status: pending
    dependencies:
      - update-model
  - id: rewrite-postgres-repo
    content: 重写 postgres.go：Create 生成 UUID + target 验证 + 重复检查 + 双表 is_published 联动；Delete 改为 Unpublish（批量删协议行）；GetByID/List SQL 兼容新旧字段
    status: pending
    dependencies:
      - update-repository-interface
  - id: update-admin-handlers
    content: 改造 admin handlers：Create/Update 请求体新增 target_id/target_type/protocol；新增 Unpublish handler
    status: pending
    dependencies:
      - rewrite-postgres-repo
  - id: update-service-handlers
    content: 改造 service/handlers.go：ExecuteAgent 兼容新字段（protocol 过滤 + workflow target_type 分支调用 RunWorkflow）
    status: pending
    dependencies:
      - update-model
  - id: update-agent-publish-ui
    content: 修改 AgentInstances.vue：上架抽屉新增协议选择下拉框（mcp/http/llm）；提交 body 适配新字段；下架改用 unpublish API
    status: pending
    dependencies:
      - update-admin-handlers
  - id: update-workflow-publish-ui
    content: 改造 WFInstances.vue：新增 publish 抽屉（协议选择+表单）替换 togglePublish；新增 unpublish 逻辑；Save 不再发送 is_published
    status: pending
    dependencies:
      - update-admin-handlers
---

## 产品概述
对 `svc_endpoints` 服务端点表进行协议化改造，使其支持同一智能体或工作流以多种协议（MCP、HTTP、LLM）发布，每种协议独立一行记录。

## 核心功能
- **数据库模型升级**：`svc_endpoints` 新增 `target_id`、`target_type`（agent/workflow）、`protocol`（mcp/http/llm）三列，`id` 改为独立 UUID
- **协议选择发布**：Agent 和 Workflow 发布时，前端显示协议选择器（mcp/http/llm），用户必须选择一种协议
- **唯一性约束**：同一 target（agent 或 workflow）的同一种协议只能存在一条记录，不可重复发布
- **发布与下架联动**：发布时在 `svc_endpoints` 中创建行并更新对应 `agent_instances`/`wf_instances` 的 `is_published` 状态；下架时删除该 target 的所有协议行并恢复 `is_published=false`
- **兼容旧数据**：不含 `target_id`/`target_type`/`protocol` 的旧记录通过 `run_mode` 字段推断（standalone→agent, workflow→workflow），旧 `id` 即视为 `target_id`

## 技术选型
- 后端：Go + Gin + PostgreSQL（沿用现有技术栈）
- 前端：Vue 3 + Element Plus（沿用现有技术栈）
- 数据库迁移：不在此计划范围内（假设用户已自行完成 DDL）

## 实现方案

### 核心策略
采用向后兼容的方式扩展 `svc_endpoints` 表模型，保持现有 API 接口签名基本不变，在 repository 层集中处理新旧字段的兼容逻辑。

### 关键技术决策

1. **`run_mode` 字段保留但仅作兼容**：新发布记录 `run_mode` 从 `target_type` 派生（agent→"standalone"，workflow→"workflow"）；旧记录（无 `target_type`）通过 `run_mode` 反推 `target_type`，确保 `ExecuteAgent` 等消费方正常读取
2. **下架语义为全部下线**：删除一个 target 的所有协议行（而非逐协议删除），前端仍为单一「下架」按钮
3. **重复发布检查在 repository 层**：Create 时查询 `target_type + target_id + protocol` 是否已存在，存在则拒绝
4. **Workflow 发布流程改造**：从简单切换 `is_published` 布尔值改为与 Agent 一致的抽屉表单 + 协议选择 + `svc_endpoints` 写入

### 执行注意事项
- **blast radius**：`SvcEndpoint` 结构体新增字段采用 `sql.NullString` 或零值兼容，确保旧数据 Scan 不报错；GetByID/List 的 SQL SELECT 增加新列，但旧列继续保留
- **事务一致性**：Create 在同一事务中插入 `svc_endpoints` + 更新 `is_published`；Delete 在同一事务中删除所有协议行 + 恢复 `is_published`
- **workflow 发布约束调整**：`wf_instances` 的 `is_published=true` 时禁止直接更新/删除的约束保持不变，但发布动作本身通过 `svc_endpoints` Create 完成，不通过 `wf_instances` 的 Update

## 架构设计

### 数据流（发布）
```
前端 [选协议] → POST /admin/svcendpoints/create {target_id, target_type, protocol, name, ...}
  → repository.Create():
    1. 验证 target_type + protocol 枚举
    2. 查 agent_instances/wf_instances 确认 target_id 存在
    3. 查 svc_endpoints 确认无重复（同 target + 同 protocol）
    4. 事务内：INSERT svc_endpoints (id=UUID) + UPDATE agent_instances/wf_instances SET is_published=true
```

### 数据流（下架）
```
前端 [下架] → DELETE /admin/svcendpoints/unpublish {target_type, target_id}
  → repository.Unpublish():
    1. 事务内：DELETE svc_endpoints WHERE target_type+target_id
    2. UPDATE agent_instances/wf_instances SET is_published=false
```

### 数据流（服务执行）
```
POST /service/agent/v1/execute {name, ...}
  → ExecuteAgent():
    1. 查 svc_endpoints WHERE name=xxx AND protocol='mcp'（或兼容的 run_mode 旧记录）
    2. 若 target_type=agent → RunStandalone
    3. 若 target_type=workflow → RunWorkflow（新增）
```

## 目录结构

```
nexus/
├── shared/
│   ├── models/
│   │   └── models.go                                    # [MODIFY] SvcEndpoint 新增 TargetID/TargetType/Protocol
│   └── repository/
│       └── svcendpoints/
│           ├── repository.go                            # [MODIFY] 接口新增 Unpublish；ListSvcEndpointsRequest 新增 TargetType/Protocol 过滤
│           └── postgres.go                              # [MODIFY] 所有 SQL 适配新字段；Create 重写（UUID、target 验证、重复检查）；Delete 改为 Unpublish；GetByID/List 兼容新旧列
├── admin/
│   └── svcendpoints/
│       └── handlers.go                                  # [MODIFY] Create/Update 请求体新增字段；新增 Unpublish handler
└── service/
    └── handlers.go                                      # [MODIFY] ExecuteAgent 兼容新字段（protocol 过滤 + workflow 分支）

nexus_desk/
└── src/
    ├── components/
    │   ├── AgentInstances.vue                            # [MODIFY] 上架抽屉新增协议选择(el-select)；提交 body 改为 target_id/target_type/protocol；下架改用 unpublish API
    │   └── WFInstances.vue                               # [MODIFY] 「发布」按钮改为打开协议选择抽屉（新增 publish 抽屉模板+逻辑）；下架新增 unpublish 调用；Save 不再发送 is_published
    └── config/
        └── admin.js                                      # [MODIFY] svcEndpoints 新增 unpublish API 路径
```

## 关键代码结构

```go
// SvcEndpoint 扩展后的结构体
type SvcEndpoint struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Description string    `json:"description"`
    RunMode     string    `json:"run_mode"`
    CreatedAt   xsql.Time `json:"created_at"`
    Arguments   xsql.JSON `json:"arguments"`
    Outputs     xsql.JSON `json:"outputs"`
    // 新增字段
    TargetID   string `json:"target_id"`
    TargetType string `json:"target_type"` // "agent" | "workflow"
    Protocol   string `json:"protocol"`    // "mcp" | "http" | "llm"
}

// Repository 接口新增方法
type SvcEndpointRepository interface {
    // ... 现有方法 ...
    // Unpublish 根据 target_type + target_id 删除所有协议行并恢复 is_published
    Unpublish(authInfo *auth.AuthInfo, targetType, targetID string) error
}

// ListSvcEndpointsRequest 新增过滤字段
type ListSvcEndpointsRequest struct {
    // ... 现有字段 ...
    TargetType string   `json:"target_type" form:"target_type"`
    Protocol   string   `json:"protocol" form:"protocol"`
}
```
