---
name: svc-endpoints-enabled-column
overview: 为 svc_endpoints 表增加 enabled 列，支持软启用/禁用端点，无需删除即可停用。涉及 Model、SQL、API Handler、服务层、前端共 5 个层面的改动。
todos:
  - id: add-model-field
    content: Model 层：SvcEndpoint 结构体新增 Enabled bool 字段
    status: pending
  - id: update-repository
    content: Repository 层：postgres.go 中 columns 添加 enabled、scanEndpoint 添加扫描、Create 插入 enabled=true、Update 更新 enabled、修复 crated_at 拼写
    status: pending
    dependencies:
      - add-model-field
  - id: update-handler
    content: Handler 层：UpdateSvcEndpoint 请求体新增 enabled 字段（*bool 指针）并传入 repo.Update
    status: pending
    dependencies:
      - add-model-field
  - id: update-service
    content: 服务层：ExecuteAgent 中查询端点后检查 enabled 状态，禁用则返回错误
    status: pending
    dependencies:
      - add-model-field
  - id: update-frontend
    content: 前端：AgentInstances.vue 和 WFInstances.vue 端点列表每项添加 el-switch 启用/禁用开关，change 事件调 Update API
    status: pending
    dependencies:
      - add-model-field
---

## 用户需求
为 `svc_endpoints` 表增加 `enabled` 列，实现不删除端点就能启用/禁用的软开关。

## 核心功能
- 端点新建时默认 `enabled=true`
- 前端端点列表每项增加启用/禁用开关，可即时切换
- 后端 Update 接口支持修改 `enabled` 状态
- 服务层 ExecuteAgent 查询时仅匹配 `enabled=true` 的端点，禁用端点不响应请求
- 顺便修复 postgres.go 中 `crated_at` 拼写错误为 `created_at`

## 设计约束
- `is_published` 仍由 Create/Delete 硬操作管理（有端点即 published，全部清空即 unpublished），不受 `enabled` toggle 影响
- `enabled` 仅控制端点是否对外服务，不改变发布状态


## 技术选型
沿用项目现有技术栈：Go 后端 (Gin + PostgreSQL) + Vue3 前端 (Element Plus)。

## 实现方案

### 整体策略
在 `svc_endpoints` 表新增 `enabled BOOLEAN DEFAULT true` 列，穿透 Model→Repository→Handler→Service→Frontend 五层，每层做最小改动。

### 关键决策
1. **enabled 不影响 is_published**：Create 仍设 `is_published=true`，Delete 在无剩余端点时清 `is_published=false`。enabled toggle 只是静默改变端点的服务可用性，不触发发布状态变更。
2. **List 不过滤 enabled**（前端展示需要看到禁用端点），仅在服务层 ExecuteAgent 中过滤 `enabled=true`（确保禁用端点不响应外部请求）。
3. **ListSvcEndpointsRequest 不加 enabled 过滤参数**：当前无需按 enabled 筛选列表，保持接口简洁；将来需要时再加。
4. **修复 typo**：`crated_at` → `created_at`，一并修正避免潜在的列名校验问题。

### 改动范围

| 层级 | 文件 | 改动点 |
|------|------|--------|
| Model | `nexus/shared/models/models.go` | SvcEndpoint 加 `Enabled bool` 字段 |
| Repository 接口 | `nexus/shared/repository/svcendpoints/repository.go` | 无改动（接口不变） |
| Repository 实现 | `nexus/shared/repository/svcendpoints/postgres.go` | columns 加 enabled、scanEndpoint 加扫描、Create INSERT + enabled、Update SET enabled、Select 查 enabled、修复 crated_at typo |
| API Handler | `nexus/admin/svcendpoints/handlers.go` | UpdateSvcEndpoint 的 req 结构体加 `Enabled *bool` 字段（指针类型支持可选更新） |
| 服务层 | `nexus/service/handlers.go` | ExecuteAgent 的 List 查询后加 enabled=true 过滤（或在查询前手动过滤，简化方案是查询后过滤） |
| 前端 Agent | `nexus_desk/src/components/AgentInstances.vue` | 端点列表每项加 el-switch 切换启用/禁用 |
| 前端 WF | `nexus_desk/src/components/WFInstances.vue` | 同上 |

### 实现细节

**Model 层**：`Enabled bool \`json:"enabled"\`` — 用 bool 零值 false 即可，数据库默认 true。

**Repository 层**：
- columns 常量改为 `"id, name, description, created_at, target_id, target_type, protocol, config, enabled"`
- scanEndpoint 加 `&e.Enabled`
- Create INSERT 加 `enabled` 列默认 `true`
- Update SQL 改为 `UPDATE ... SET name=$1, description=$2, config=$3, enabled=$4 WHERE id=$5`
- Delete 逻辑不变（硬删除，不依赖 enabled）

**Handler 层**：UpdateSvcEndpoint 的 req 结构体加 `Enabled *bool \`json:"enabled"\``。因为 Update 接口是可选的字段更新，用指针区分"未传"和"传了 false"。repo.Update 接收 enabled 值。

**服务层**：ExecuteAgent 中 List 查询当前已按 name+protocol 过滤，结果最多 1 条。在拿到结果后检查 `svcEndpoint.Enabled`，若为 false 则返回"端点已禁用"错误。

**前端**：端点列表中每个 `endpoint-item` 的 `endpoint-item-header` 内，在删除按钮前加一个 `el-switch`，绑定 `ep.enabled`，change 事件调用 update API 更新 enabled 状态。样式上 switch 与删除按钮放在一起（右侧）。

### 数据库迁移
需在 PostgreSQL 执行：`ALTER TABLE {schema}.svc_endpoints ADD COLUMN enabled BOOLEAN DEFAULT true;`（各 schema 独立执行，不在此次代码改动范围内）。

